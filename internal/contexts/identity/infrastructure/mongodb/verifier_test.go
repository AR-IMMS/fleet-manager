package mongodb

import (
	"context"
	"errors"
	"testing"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
)

func TestCredentialInputsRequireHighEntropyTokenLength(t *testing.T) {
	t.Parallel()
	verifier := &Verifier{}
	if err := verifier.StoreActiveCredential(context.Background(), "agent-1", "credential-1", "short-token"); !errors.Is(err, errInvalidCredentialRecord) {
		t.Fatalf("short credential should be rejected, got %v", err)
	}
	if _, err := verifier.Verify(context.Background(), "short-token"); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("short token should be rejected before persistence access, got %v", err)
	}
}
