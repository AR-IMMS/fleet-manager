package opamp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/application"
	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
)

// verifierFunc supplies isolated authentication behavior for each test.
type verifierFunc func(context.Context, string) (domain.Principal, error)

type sessionAuthorizerFunc func(context.Context, domain.Principal, []byte) error

func (f sessionAuthorizerFunc) BindAndAuthorize(ctx context.Context, principal domain.Principal, uid []byte) error {
	return f(ctx, principal, uid)
}

func TestMessageBindsUIDAndDisconnectsRevokedSession(t *testing.T) {
	t.Parallel()
	principal, _ := domain.NewPrincipal("agent-1", "credential-1")
	uid := []byte("0123456789abcdef")
	checks := 0
	authorizer := sessionAuthorizerFunc(func(_ context.Context, got domain.Principal, received []byte) error {
		checks++
		if got.InstallationID() != principal.InstallationID() || string(received) != string(uid) {
			t.Error("callback did not authorize the authenticated agent and reported UID")
		}
		if checks == 1 {
			return nil
		}
		return domain.ErrSessionUnauthorized
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authenticator, err := application.NewAuthenticator(verifierFunc(func(context.Context, string) (domain.Principal, error) {
		return principal, nil
	}), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewCallbackAdapter(authenticator, authorizer, logger)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://localhost/v1/opamp", nil)
	request.Header.Set("Authorization", "Bearer test-credential")
	response := adapter.Callbacks().OnConnecting(request)
	if !response.Accept {
		t.Fatal("expected authenticated connection")
	}
	client, peer := net.Pipe()
	defer func() {
		if err := peer.Close(); err != nil {
			t.Errorf("close peer: %v", err)
		}
	}()
	conn := testConnection{Conn: client}
	defer func() {
		if err := client.Close(); err != nil {
			t.Errorf("close client: %v", err)
		}
	}()
	message := &protobufs.AgentToServer{InstanceUid: uid}
	got := response.ConnectionCallbacks.OnMessage(context.Background(), conn, message)
	if got == nil || string(got.InstanceUid) != string(uid) {
		t.Fatal("first authenticated UID message should be echoed")
	}
	if got := response.ConnectionCallbacks.OnMessage(context.Background(), conn, message); got != nil {
		t.Fatal("revoked session must not receive a protocol response")
	}
	if checks != 2 {
		t.Fatalf("expected authorization on every message, got %d checks", checks)
	}
}

var _ types.Connection = testConnection{}

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

	principal, err := domain.NewPrincipal("installation-001", "credential-001")
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
			handler, err := NewCallbackAdapter(authenticator, sessionAuthorizerFunc(func(context.Context, domain.Principal, []byte) error { return nil }), logger)
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
