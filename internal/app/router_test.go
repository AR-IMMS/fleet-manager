package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/platform/httpapi"
)

// readinessFunc supplies focused readiness behavior for router tests.
type readinessFunc func(context.Context) error

func (f readinessFunc) Check(ctx context.Context) error {
	return f(ctx)
}

// TestRouterHealthRoutes verifies route wiring and dependency isolation.
func TestRouterHealthRoutes(t *testing.T) {
	dependencyErr := errors.New("dependency unavailable")

	cases := []struct {
		name       string
		path       string
		checkErr   error
		wantStatus int
	}{
		{
			name:       "live ignores dependency failure",
			path:       "/health/live",
			checkErr:   dependencyErr,
			wantStatus: http.StatusOK,
		},
		{
			name:       "ready when dependency is available",
			path:       "/health/ready",
			wantStatus: http.StatusOK,
		},
		{
			name:       "not ready when dependency fails",
			path:       "/health/ready",
			checkErr:   dependencyErr,
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router, err := newRouter(
				readinessFunc(func(context.Context) error {
					return tc.checkErr
				}),
				time.Second,
			)
			if err != nil {
				t.Fatalf("construct router: %v", err)
			}

			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != tc.wantStatus {
				t.Fatalf(
					"expected status %d, got %d",
					tc.wantStatus,
					response.Code,
				)
			}
		})
	}
}

// TestRouterRejectsMissingReadiness prevents incomplete health wiring.
func TestRouterRejectsMissingReadiness(t *testing.T) {
	if _, err := newRouter(nil, time.Second); err == nil {
		t.Fatal("expected missing readiness checker to fail")
	}
}

// TestRouterRejectsUnsupportedMethod keeps health routes read-only.
func TestRouterRejectsUnsupportedMethod(t *testing.T) {
	router, err := newRouter(
		readinessFunc(func(context.Context) error { return nil }),
		time.Second,
	)
	if err != nil {
		t.Fatalf("construct router: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/health/live", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", response.Code)
	}
}

func TestRouterReturnsRequestIDHeader(t *testing.T) {
	router, err := newRouter(readinessFunc(func(context.Context) error { return nil }), time.Second)
	if err != nil {
		t.Fatalf("construct router: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	request.Header.Set(httpapi.RequestIDHeader, "router-request-01")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if got := response.Header().Get(httpapi.RequestIDHeader); got != "router-request-01" {
		t.Fatalf("request ID response header = %q", got)
	}
}
