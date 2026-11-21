package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
)

// ErrAlreadyRun means the application lifecycle has already been started.
var ErrAlreadyRun = errors.New("application can only run once")

// Application coordinates runtime startup and shutdown.
//
// Construction does not open external resources. Run owns one lifecycle;
// an Application must not be copied after use.
type Application struct {
	config  Config
	logger  *slog.Logger
	started atomic.Bool
}

// NewApplication validates configuration and mandatory composition inputs.
//
// Configuration may contain secrets and must not be logged. The supplied
// logger remains caller-owned and is not installed as the global logger.
func NewApplication(
	cfg Config,
	logger *slog.Logger,
) (*Application, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid application configuration: %w", err)
	}

	if logger == nil {
		return nil, fmt.Errorf("application logger is required")
	}

	return &Application{
		config: cfg,
		logger: logger.With("component", "application"),
	}, nil
}

// Run initializes dependencies and owns their cleanup on every exit path.
//
// Runtime cancellation is a normal shutdown request. Startup and cleanup
// failures are returned to the entrypoint. HTTP health routes and identity
// persistence are initialized here; OpAMP activation remains separately gated.
func (a *Application) Run(ctx context.Context) (runErr error) {
	if !a.started.CompareAndSwap(false, true) {
		return ErrAlreadyRun
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	var resources lifecycle
	var runtime *runtimeState

	// Cleanup also runs when startup stops after acquiring a resource.
	defer func() {
		if runtime != nil && runtime.readiness != nil {
			runtime.readiness.BeginDrain()
		}

		// Runtime context is cancelled during normal shutdown. Use a fresh
		// context with one total budget for all cleanup steps.
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(),
			a.config.ShutdownTimeout,
		)
		defer cancel()

		a.logger.InfoContext(shutdownCtx, "application stopping")

		if err := resources.stopAll(shutdownCtx); err != nil {
			runErr = errors.Join(runErr, err)
			return
		}

		a.logger.InfoContext(shutdownCtx, "application resources released")
	}()

	a.logger.InfoContext(ctx, "application starting")

	var err error
	runtime, err = wireRuntime(ctx, a.config, a.logger, &resources)
	if err != nil {
		return fmt.Errorf("initialize application runtime: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil

	case err, ok := <-runtime.http.Errors():
		if !ok || err == nil {
			return fmt.Errorf("http server exited unexpectedly")
		}

		return fmt.Errorf("http server failed: %w", err)
	}
}
