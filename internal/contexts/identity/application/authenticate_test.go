package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
)

// verifierFunc adapts a typed function to IdentityVerifier.
//
// Each test owns its verifier, avoiding shared state and external dependencies.
type verifierFunc func(context.Context, string) (domain.Principal, error)

func (f verifierFunc) Verify(
	ctx context.Context,
	credential string,
) (domain.Principal, error) {
	return f(ctx, credential)
}

// TestAuthenticateOutcomes verifies identity admission and error classification.
func TestAuthenticateOutcomes(t *testing.T) {
	t.Parallel()

	principal, err := domain.NewPrincipal("installation-001", "credential-001")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}

	tests := []struct {
		name      string
		principal domain.Principal
		verifyErr error
		wantErr   error
	}{
		{
			name:      "authorized installation",
			principal: principal,
		},
		{
			name:      "rejected credential",
			verifyErr: ErrUnauthenticated,
			wantErr:   ErrUnauthenticated,
		},
		{
			name:      "wrapped authentication rejection",
			verifyErr: fmt.Errorf("rejected: %w", ErrUnauthenticated),
			wantErr:   ErrUnauthenticated,
		},
		{
			name:      "upstream failure is sanitized",
			verifyErr: errors.New("upstream error containing sensitive details"),
			wantErr:   ErrVerifierUnavailable,
		},
		{
			name:    "empty successful identity",
			wantErr: ErrVerifierUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			verifier := verifierFunc(func(
				ctx context.Context,
				credential string,
			) (domain.Principal, error) {
				if credential != "test-credential" {
					t.Errorf("unexpected credential passed to verifier")
				}

				if _, ok := ctx.Deadline(); !ok {
					t.Error("verification context must have a deadline")
				}

				return tt.principal, tt.verifyErr
			})

			auth, err := NewAuthenticator(verifier, time.Second)
			if err != nil {
				t.Fatalf("NewAuthenticator() error: %v", err)
			}

			got, err := auth.Authenticate(context.Background(), "test-credential")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected error %v, got %v", tt.wantErr, err)
			}

			if tt.wantErr != nil {
				if got.Valid() {
					t.Fatal("failed authentication must not return a valid principal")
				}
				return
			}

			if got.InstallationID() != principal.InstallationID() {
				t.Errorf("unexpected authenticated identity: %q", got.InstallationID())
			}
		})
	}
}

// TestAuthenticateMissingCredential ensures invalid input never reaches Core.
func TestAuthenticateMissingCredential(t *testing.T) {
	t.Parallel()

	verifier := verifierFunc(func(
		context.Context,
		string,
	) (domain.Principal, error) {
		t.Fatal("verifier must not be called for an empty credential")
		return domain.Principal{}, nil
	})

	auth, err := NewAuthenticator(verifier, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	_, err = auth.Authenticate(context.Background(), "")
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}
}

// TestAuthenticateDiscardsLateSuccess prevents a verifier from admitting a
// connection after its authentication deadline has expired.
func TestAuthenticateDiscardsLateSuccess(t *testing.T) {
	t.Parallel()

	principal, err := domain.NewPrincipal("installation-001", "credential-001")
	if err != nil {
		t.Fatal(err)
	}

	verifier := verifierFunc(func(
		ctx context.Context,
		_ string,
	) (domain.Principal, error) {
		<-ctx.Done()
		return principal, nil
	})

	auth, err := NewAuthenticator(verifier, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	got, err := auth.Authenticate(context.Background(), "test-credential")
	if !errors.Is(err, ErrVerifierUnavailable) {
		t.Fatalf("expected ErrVerifierUnavailable, got %v", err)
	}

	if got.Valid() {
		t.Fatal("late success must not return an authenticated identity")
	}
}

// TestAuthenticatePropagatesCancellation verifies that canceled requests do not
// become authenticated even if the verifier returns a successful result.
func TestAuthenticatePropagatesCancellation(t *testing.T) {
	t.Parallel()

	verifier := verifierFunc(func(
		ctx context.Context,
		_ string,
	) (domain.Principal, error) {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Error("verifier must receive the canceled context")
		}
		return domain.Principal{}, ctx.Err()
	})

	auth, err := NewAuthenticator(verifier, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = auth.Authenticate(ctx, "test-credential")
	if !errors.Is(err, ErrVerifierUnavailable) {
		t.Fatalf("expected ErrVerifierUnavailable, got %v", err)
	}
}

func TestNewAuthenticatorRejectsInvalidDependencies(t *testing.T) {
	t.Parallel()

	if _, err := NewAuthenticator(nil, time.Second); err == nil {
		t.Error("expected missing verifier to be rejected")
	}

	verifier := verifierFunc(func(
		context.Context,
		string,
	) (domain.Principal, error) {
		return domain.Principal{}, nil
	})

	if _, err := NewAuthenticator(verifier, 0); err == nil {
		t.Error("expected non-positive timeout to be rejected")
	}
}

func TestAuthorizeSupervisorMessageValidatesAndCopiesUID(t *testing.T) {
	t.Parallel()
	principal, err := domain.NewPrincipal("agent-1", "credential-1")
	if err != nil {
		t.Fatal(err)
	}
	var received []byte
	authorizer := sessionAuthorizerFunc(func(_ context.Context, got domain.Principal, uid []byte) error {
		if got.InstallationID() != "agent-1" || got.CredentialID() != "credential-1" {
			t.Fatalf("unexpected principal: %#v", got)
		}
		received = uid
		return nil
	})
	uid := []byte("0123456789abcdef")
	if err := AuthorizeSupervisorMessage(context.Background(), authorizer, principal, uid); err != nil {
		t.Fatal(err)
	}
	uid[0] = 'x'
	if received[0] != '0' {
		t.Fatal("authorizer received caller-owned UID memory")
	}
}

func TestAuthorizeSupervisorMessageRejectsInvalidUIDAndRevocation(t *testing.T) {
	t.Parallel()
	principal, _ := domain.NewPrincipal("agent-1", "credential-1")
	called := false
	authorizer := sessionAuthorizerFunc(func(context.Context, domain.Principal, []byte) error {
		called = true
		return domain.ErrSessionUnauthorized
	})
	if err := AuthorizeSupervisorMessage(context.Background(), authorizer, principal, []byte("short")); !errors.Is(err, domain.ErrSessionUnauthorized) {
		t.Fatalf("invalid UID should fail closed, got %v", err)
	}
	if called {
		t.Fatal("invalid UID reached persistence adapter")
	}
	if err := AuthorizeSupervisorMessage(context.Background(), authorizer, principal, []byte("0123456789abcdef")); !errors.Is(err, domain.ErrSessionUnauthorized) {
		t.Fatalf("revoked session should be denied, got %v", err)
	}
}

type sessionAuthorizerFunc func(context.Context, domain.Principal, []byte) error

func (f sessionAuthorizerFunc) BindAndAuthorize(ctx context.Context, principal domain.Principal, uid []byte) error {
	return f(ctx, principal, uid)
}
