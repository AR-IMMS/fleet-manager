package domain

import (
	"errors"
	"strings"
)

// ErrInvalidPrincipal indicates that authentication returned an unusable identity.
var ErrInvalidPrincipal = errors.New("invalid authenticated principal")

// Principal identifies a Fleet installation authorized to establish a connection.
//
// The installation ID comes from a trusted identity verifier. An OpAMP instance
// UID is reported separately by the client and must never replace this identity.
type Principal struct {
	installationID string
}

// NewPrincipal creates an authenticated principal with a non-empty installation ID.
//
// No UUID format is imposed here because the cross-system identity format has
// not been finalized. Whitespace is rejected rather than silently normalized.
func NewPrincipal(installationID string) (Principal, error) {
	if installationID == "" ||
		strings.TrimSpace(installationID) != installationID {
		return Principal{}, ErrInvalidPrincipal
	}

	return Principal{installationID: installationID}, nil
}

// InstallationID returns the stable installation identity owned by Core/Fleet.
func (p Principal) InstallationID() string {
	return p.installationID
}

// Valid reports whether the principal can identify an authenticated installation.
//
// The zero value is intentionally invalid, preventing an omitted verifier result
// from being accepted as an authenticated identity.
func (p Principal) Valid() bool {
	return p.installationID != "" &&
		strings.TrimSpace(p.installationID) == p.installationID
}
