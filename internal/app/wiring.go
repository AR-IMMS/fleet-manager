package app

import (
	"context"
	"log/slog"

	httpserver "github.com/AR-IMMS/fleet-manager/internal/platform/http-server"
	"github.com/AR-IMMS/fleet-manager/internal/platform/mongodb"
)

// runtimeState holds the components needed by application orchestration.
//
// Concrete persistence adapters stay inside composition and are not exposed
// to the application lifecycle.
type runtimeState struct {
	readiness *FleetReadiness
	http      *httpserver.Server
}

// wireRuntime constructs and starts runtime dependencies.
//
// Resources are registered immediately after successful acquisition.
// A partial runtime is returned on failure so shutdown can mark readiness
// as draining before releasing any acquired resources.
func wireRuntime(
	ctx context.Context,
	cfg Config,
	logger *slog.Logger,
	resources *lifecycle,
) (*runtimeState, error) {
	runtime := &runtimeState{}

	db, err := mongodb.Open(ctx, mongodb.Options{
		URI:              cfg.Mongo.URI,
		Database:         cfg.Mongo.Database,
		AppName:          cfg.Mongo.AppName,
		ConnectTimeout:   cfg.Mongo.ConnectTimeout,
		OperationTimeout: cfg.Mongo.OperationTimeout,
	})
	if err != nil {
		return runtime, err
	}

	resources.add("storage", db.Close)

	runtime.readiness, err = NewFleetReadiness(ReadinessCheck{
		Name:  "storage",
		Check: db.Ping,
	})
	if err != nil {
		return runtime, err
	}

	router, err := newRouter(
		runtime.readiness,
		cfg.HTTP.ReadinessTimeout,
	)
	if err != nil {
		return runtime, err
	}

	runtime.http, err = httpserver.New(
		httpserver.Options{
			ListenAddress:     cfg.HTTP.ListenAddress,
			ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
			ReadTimeout:       cfg.HTTP.ReadTimeout,
			WriteTimeout:      cfg.HTTP.WriteTimeout,
			IdleTimeout:       cfg.HTTP.IdleTimeout,
		},
		router,
	)
	if err != nil {
		return runtime, err
	}

	if err := runtime.http.Start(ctx); err != nil {
		return runtime, err
	}

	resources.add("http", runtime.http.Shutdown)

	logger.InfoContext(
		ctx,
		"http listener started",
		"address", runtime.http.Addr().String(),
	)

	return runtime, nil
}
