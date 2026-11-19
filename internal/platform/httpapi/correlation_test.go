package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AR-IMMS/fleet-manager/internal/shared/correlation"
)

type countingGenerator struct{ calls int }

func (g *countingGenerator) NewID() (string, error) { g.calls++; return "selected", nil }

func TestNestedMiddlewareReusesCorrelation(t *testing.T) {
	for _, inherited := range []string{"", "upstream", "bad\nvalue"} {
		t.Run(inherited, func(t *testing.T) {
			generator := &countingGenerator{}
			called := false
			middleware := RequestIDMiddleware(generator)
			handler := middleware(middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				id, ok := correlation.RequestIDFromContext(r.Context())
				if !ok || id != w.Header().Get(RequestIDHeader) {
					t.Fatal("correlation mismatch")
				}
			})))
			req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(correlation.WithRequestID(context.Background(), inherited))
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if inherited == "" && generator.calls != 1 {
				t.Fatal("duplicate generation")
			}
			if inherited != "" && generator.calls != 0 {
				t.Fatal("inherited ID replaced")
			}
			if inherited == "bad\nvalue" && (called || res.Code != 500) {
				t.Fatal("invalid inherited ID accepted")
			}
		})
	}
}
