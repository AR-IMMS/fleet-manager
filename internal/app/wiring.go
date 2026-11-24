package app

import (
	"context"
	"fmt"
	"log/slog"

	identityapp "github.com/AR-IMMS/fleet-manager/internal/contexts/identity/application"
	identityhttp "github.com/AR-IMMS/fleet-manager/internal/contexts/identity/delivery/http"
	identityopamp "github.com/AR-IMMS/fleet-manager/internal/contexts/identity/delivery/opamp"
	identitymongo "github.com/AR-IMMS/fleet-manager/internal/contexts/identity/infrastructure/mongodb"
	httpserver "github.com/AR-IMMS/fleet-manager/internal/platform/http-server"
	"github.com/AR-IMMS/fleet-manager/internal/platform/mongodb"
)

// runtimeState holds the components needed by application orchestration.
//
// Concrete persistence adapters stay inside composition and are not exposed
// to the application lifecycle.
type runtimeState struct {
	readiness     *FleetReadiness
	http          *httpserver.Server
	opamp         *identityopamp.OpAMPServer
	authenticator *identityapp.Authenticator
	identityStore *identitymongo.Verifier
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

	if err := identitymongo.Migrate(ctx, db.Handle()); err != nil {
		return runtime, fmt.Errorf("migrate identity schema: %w", err)
	}
	if err := identitymongo.EnsureIndexes(ctx, db.Handle()); err != nil {
		return runtime, fmt.Errorf("reconcile identity indexes: %w", err)
	}
	runtime.identityStore, err = identitymongo.NewVerifier(db.Handle())
	if err != nil {
		return runtime, fmt.Errorf("initialize identity store: %w", err)
	}
	runtime.authenticator, err = identityapp.NewAuthenticator(
		runtime.identityStore,
		cfg.AuthTimeout,
	)
	if err != nil {
		return runtime, fmt.Errorf("initialize identity authenticator: %w", err)
	}
	var enrollmentHandlers []*identityhttp.EnrollmentHandler
	if cfg.Enrollment.Enabled {
		enrollmentStore, storeErr := identitymongo.NewEnrollmentStore(db.Handle(), cfg.Enrollment.EncryptionKey)
		if storeErr != nil {
			return runtime, fmt.Errorf("initialize enrollment store: %w", storeErr)
		}
		if transactionErr := enrollmentStore.VerifyTransactionSupport(ctx); transactionErr != nil {
			return runtime, fmt.Errorf("verify Mongo transaction support for enrollment: %w", transactionErr)
		}
		enrollmentService := &identityapp.EnrollmentService{
			Store: enrollmentStore, Revoker: runtime.identityStore, DefaultInviteTTL: cfg.Enrollment.DefaultInviteTTL,
			MaximumInviteTTL: cfg.Enrollment.MaximumInviteTTL, RecoveryTTL: cfg.Enrollment.RecoveryTTL,
			OpAMPEndpoint: cfg.Enrollment.OpAMPEndpoint,
		}
		enrollmentHandler, handlerErr := identityhttp.NewEnrollmentHandler(enrollmentService, cfg.Enrollment.CoreClientCN)
		if handlerErr != nil {
			return runtime, fmt.Errorf("initialize enrollment HTTP adapter: %w", handlerErr)
		}
		if cfg.Enrollment.OTLPIngressClientCN != "" {
			if err := enrollmentHandler.SetOTLPVerifier(runtime.identityStore, cfg.Enrollment.OTLPIngressClientCN); err != nil {
				return runtime, fmt.Errorf("initialize OTLP introspection adapter: %w", err)
			}
		}
		enrollmentHandlers = append(enrollmentHandlers, enrollmentHandler)
	}

	if cfg.Enabled {
		callbacks, callbackErr := identityopamp.NewCallbackAdapter(
			runtime.authenticator,
			runtime.identityStore,
			logger,
		)
		if callbackErr != nil {
			return runtime, fmt.Errorf("initialize OpAMP callbacks: %w", callbackErr)
		}
		runtime.opamp, err = identityopamp.NewServer(cfg.OpAMPConfig, callbacks, logger)
		if err != nil {
			return runtime, fmt.Errorf("initialize OpAMP server: %w", err)
		}
		if err := runtime.opamp.Start(); err != nil {
			return runtime, fmt.Errorf("start OpAMP server: %w", err)
		}
		resources.add("OpAMP", runtime.opamp.Stop)
		logger.InfoContext(ctx, "OpAMP listener started", "address", runtime.opamp.Addr().String())
	}
	readinessChecks := []ReadinessCheck{{
		Name:  "storage",
		Check: db.Ping,
	}}
	if runtime.opamp != nil {
		readinessChecks = append(readinessChecks, ReadinessCheck{
			Name: "opamp",
			Check: func(context.Context) error {
				if err := runtime.opamp.Err(); err != nil {
					return fmt.Errorf("OpAMP listener failed: %w", err)
				}
				if runtime.opamp.Addr() == nil {
					return fmt.Errorf("OpAMP listener is unavailable")
				}
				return nil
			},
		})
	}
	runtime.readiness, err = NewFleetReadiness(readinessChecks...)
	if err != nil {
		return runtime, err
	}

	router, err := newRouter(
		runtime.readiness,
		cfg.HTTP.ReadinessTimeout,
		enrollmentHandlers...,
	)
	if err != nil {
		return runtime, err
	}

	runtime.http, err = httpserver.New(
		httpserver.Options{
			ListenAddress:     cfg.HTTP.ListenAddress,
			TLSCertFile:       cfg.HTTP.TLSCertFile,
			TLSKeyFile:        cfg.HTTP.TLSKeyFile,
			ClientCAFile:      cfg.HTTP.ClientCAFile,
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
