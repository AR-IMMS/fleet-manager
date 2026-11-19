package app

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/AR-IMMS/fleet-manager/internal/platform/http-server"
)

// newRouter assembles HTTP routes owned by the application.
//
// The router does not own the HTTP server or dependency lifecycles.
// Context-specific routes can be mounted here as their delivery adapters
// become available.
func newRouter(
	checker httpserver.ReadinessChecker,
	readinessTimeout time.Duration,
) (http.Handler, error) {
	health, err := httpserver.NewHealthHandler(checker, readinessTimeout)
	if err != nil {
		return nil, err
	}

	router := chi.NewRouter()

	router.Get("/health/live", health.Live)
	router.Get("/health/ready", health.Ready)

	return router, nil
}
