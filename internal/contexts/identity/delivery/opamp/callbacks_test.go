package opamp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/application"
	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
)

// verifierFunc supplies isolated authentication behavior for each test.
type verifierFunc func(context.Context, string) (domain.Principal, error)

func (f verifierFunc) Verify(
	ctx context.Context,
	credential string,
) (domain.Principal, error) {
	return f(ctx, credential)
}

// TestConnectingAuthentication verifies the public authentication boundary.
//
// It checks response classification without opening a socket or depending on
// WebSocket implementation details.
func TestConnectingAuthentication(t *testing.T) {
	t.Parallel()

	principal, err := domain.NewPrincipal("installation-001")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		headers      []string
		verifyErr    error
		wantCalls    int
		wantAccepted bool
		wantStatus   int
	}{
		{
			name:         "valid credential",
			headers:      []string{"Bearer test-credential"},
			wantCalls:    1,
			wantAccepted: true,
		},
		{
			name:       "missing credential",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "unsupported authentication scheme",
			headers:    []string{"Basic test-credential"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "empty bearer token",
			headers:    []string{"Bearer"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "multiple authorization headers",
			headers:    []string{"Bearer first", "Bearer second"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "ambiguous combined credentials",
			headers:    []string{"Bearer first,second"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "credential rejected by verifier",
			headers:    []string{"Bearer test-credential"},
			verifyErr:  application.ErrUnauthenticated,
			wantCalls:  1,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "verifier unavailable",
			headers:    []string{"Bearer test-credential"},
			verifyErr:  errors.New("upstream unavailable"),
			wantCalls:  1,
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls := 0
			verifier := verifierFunc(func(
				_ context.Context,
				credential string,
			) (domain.Principal, error) {
				calls++

				if credential != "test-credential" {
					t.Error("unexpected credential passed to verifier")
				}

				return principal, tt.verifyErr
			})

			authenticator, err := application.NewAuthenticator(
				verifier,
				time.Second,
			)
			if err != nil {
				t.Fatal(err)
			}

			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			handler, err := NewCallbackAdapter(authenticator, logger)
			if err != nil {
				t.Fatal(err)
			}

			request := httptest.NewRequest(
				http.MethodGet,
				"http://localhost/v1/opamp",
				nil,
			)

			for _, value := range tt.headers {
				request.Header.Add("Authorization", value)
			}

			response := handler.Callbacks().OnConnecting(request)

			if response.Accept != tt.wantAccepted {
				t.Errorf(
					"expected accepted=%t, got %t",
					tt.wantAccepted,
					response.Accept,
				)
			}

			if response.HTTPStatusCode != tt.wantStatus {
				t.Errorf(
					"expected HTTP status %d, got %d",
					tt.wantStatus,
					response.HTTPStatusCode,
				)
			}

			if calls != tt.wantCalls {
				t.Errorf("expected %d verifier calls, got %d", tt.wantCalls, calls)
			}

			if tt.wantAccepted && response.ConnectionCallbacks.OnMessage == nil {
				t.Error("accepted connection must have message callbacks")
			}

			if tt.wantStatus == http.StatusUnauthorized &&
				response.HTTPResponseHeader["WWW-Authenticate"] != "Bearer" {
				t.Error("unauthorized response must include a Bearer challenge")
			}
		})
	}
}
