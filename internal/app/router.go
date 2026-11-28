package app

import (
	"net/http"
	"time"

	identityhttp "github.com/AR-IMMS/fleet-manager/internal/contexts/identity/delivery/http"
	"github.com/AR-IMMS/fleet-manager/internal/platform/httpapi"
	"github.com/go-chi/chi/v5"

	"github.com/AR-IMMS/fleet-manager/internal/platform/httpserver"
)

// newRouter assembles HTTP routes owned by the application.
//
// The router does not own the HTTP server or dependency lifecycles.
// Context-specific routes can be mounted here as their delivery adapters
// become available.
func newRouter(
	checker httpserver.ReadinessChecker,
	readinessTimeout time.Duration,
	docsEnabled bool,
	enrollmentHandlers ...*identityhttp.EnrollmentHandler,
) (http.Handler, error) {
	health, err := httpserver.NewHealthHandler(checker, readinessTimeout)
	if err != nil {
		return nil, err
	}

	router := chi.NewRouter()
	router.Use(httpapi.RequestIDMiddleware(nil))

	router.Get("/health/live", health.Live)
	router.Get("/health/ready", health.Ready)
	for _, handler := range enrollmentHandlers {
		if handler != nil {
			handler.Mount(router)
		}
	}
	if docsEnabled {
		docs := httpapi.NewAPIDocs()
		router.Get("/openapi.yaml", docs.Spec)
		router.Get("/docs", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/docs/", http.StatusTemporaryRedirect)
		})
		router.Handle("/docs/*", docs.UI())
	}

	return router, nil
}
