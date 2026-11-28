package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// ReadinessChecker reports whether the application can currently serve work.
//
// Implementations must honor context cancellation and avoid detached work.
type ReadinessChecker interface {
	Check(context.Context) error
}

// HealthHandler exposes process liveness and dependency readiness.
//
// It does not own the HTTP server or the lifecycle of checked dependencies.
type HealthHandler struct {
	checker ReadinessChecker
	timeout time.Duration
}

// NewHealthHandler constructs health endpoints with a bounded readiness check.
func NewHealthHandler(
	checker ReadinessChecker,
	timeout time.Duration,
) (*HealthHandler, error) {
	if checker == nil {
		return nil, fmt.Errorf("readiness checker is required")
	}

	if timeout <= 0 {
		return nil, fmt.Errorf("readiness timeout must be positive")
	}

	return &HealthHandler{
		checker: checker,
		timeout: timeout,
	}, nil
}

// Live confirms that the process can handle an HTTP request.
//
// Dependency failures do not affect liveness.
func (h *HealthHandler) Live(w http.ResponseWriter, _ *http.Request) {
	writeHealth(w, http.StatusOK, `{"status":"alive"}`)
}

// Ready checks application readiness within the configured timeout.
//
// Failures return a generic response without exposing dependency details.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	err := h.checker.Check(ctx)
	if err != nil || ctx.Err() != nil {
		writeHealth(
			w,
			http.StatusServiceUnavailable,
			`{"status":"not_ready"}`,
		)
		return
	}

	writeHealth(w, http.StatusOK, `{"status":"ready"}`)
}

// writeHealth writes a fixed JSON response that must not be cached.
func writeHealth(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	// A disconnected client does not need a second response.
	_, _ = w.Write([]byte(body))
}
