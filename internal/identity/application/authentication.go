package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/identity/domain"
)

var (
	// ErrUnauthenticated means the credential is missing, invalid, expired,
	// revoked, or otherwise not authorized to establish a connection.
	ErrUnauthenticated = errors.New("agent authentication rejected")

	// ErrVerifierUnavailable means authentication could not be completed.
	// The transport must reject the connection rather than assume authorization.
	ErrVerifierUnavailable = errors.New("identity verifier unavailable")
)

// IdentityVerifier resolves a credential into an authorized Fleet identity.
//
// Implementations must honor context cancellation, avoid logging credentials,
// and return ErrUnauthenticated for rejected credentials. Infrastructure failures
// must remain distinguishable from authentication rejection.
//
// A future Core adapter implements this port. Tests use a deterministic fake.
type IdentityVerifier interface {
	Verify(ctx context.Context, credential string) (domain.Principal, error)
}

// Authenticator applies the connection authentication policy.
//
// It does not parse HTTP headers or assign HTTP status codes. Those concerns
// belong to the OpAMP transport adapter.
type Authenticator struct {
	verifier IdentityVerifier
	timeout  time.Duration
}

// NewAuthenticator constructs an authenticator with a bounded verification time.
func NewAuthenticator(
	verifier IdentityVerifier,
	timeout time.Duration,
) (*Authenticator, error) {
	if verifier == nil {
		return nil, fmt.Errorf("identity verifier is required")
	}

	if timeout <= 0 {
		return nil, fmt.Errorf("authentication timeout must be positive")
	}

	return &Authenticator{
		verifier: verifier,
		timeout:  timeout,
	}, nil
}

// Authenticate verifies a credential before a connection is admitted.
//
// Rejected credentials and unavailable verification remain separate outcomes:
// the transport maps them to unauthorized and service-unavailable responses.
//
// The timeout is cooperative. Verifier implementations must propagate the
// supplied context to network calls instead of starting detached work.
func (a *Authenticator) Authenticate(
	ctx context.Context,
	credential string,
) (domain.Principal, error) {
	if credential == "" {
		return domain.Principal{}, ErrUnauthenticated
	}

	verifyCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	principal, err := a.verifier.Verify(verifyCtx, credential)

	// Discard late results, including successful results returned after cancellation.
	if verifyCtx.Err() != nil {
		return domain.Principal{}, ErrVerifierUnavailable
	}

	if err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			return domain.Principal{}, ErrUnauthenticated
		}

		// Do not expose arbitrary verifier error text: an adapter might include
		// sensitive upstream details in its error message.
		return domain.Principal{}, ErrVerifierUnavailable
	}

	if !principal.Valid() {
		return domain.Principal{}, ErrVerifierUnavailable
	}

	return principal, nil
}
