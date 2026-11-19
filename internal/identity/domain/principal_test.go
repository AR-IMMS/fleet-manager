package domain

import (
	"errors"
	"testing"
)

func TestNewPrincipal(t *testing.T) {
	t.Parallel()

	principal, err := NewPrincipal("installation-001")
	if err != nil {
		t.Fatalf("NewPrincipal() error: %v", err)
	}

	if !principal.Valid() {
		t.Fatal("expected a valid principal")
	}

	if principal.InstallationID() != "installation-001" {
		t.Errorf("unexpected installation identity: %q", principal.InstallationID())
	}
}

// TestNewPrincipalRejectsInvalidIdentity prevents ambiguous identity values.
func TestNewPrincipalRejectsInvalidIdentity(t *testing.T) {
	t.Parallel()

	for _, identity := range []string{"", " ", " installation-001", "installation-001\n"} {
		t.Run(identity, func(t *testing.T) {
			t.Parallel()

			_, err := NewPrincipal(identity)
			if !errors.Is(err, ErrInvalidPrincipal) {
				t.Fatalf("expected ErrInvalidPrincipal, got %v", err)
			}
		})
	}
}

func TestZeroPrincipalIsInvalid(t *testing.T) {
	t.Parallel()

	var principal Principal
	if principal.Valid() {
		t.Fatal("zero-value principal must not authenticate an installation")
	}
}
