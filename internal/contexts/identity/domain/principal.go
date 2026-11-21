package domain

import (
	"errors"
	"strings"
)

// ErrInvalidPrincipal indicates that authentication returned an unusable identity.
var ErrInvalidPrincipal = errors.New("invalid authenticated principal")

// ErrUIDConflict indicates a Supervisor UID is already bound to another installation.
var ErrUIDConflict = errors.New("supervisor instance UID conflict")

// ErrSessionUnauthorized indicates a credential or bound session is no longer active.
var ErrSessionUnauthorized = errors.New("identity session is not authorized")

// ErrCredentialRejected classifies missing, invalid, expired, or revoked credentials.
var ErrCredentialRejected = errors.New("credential rejected")

// ErrIdentityUnavailable classifies identity persistence failures.
var ErrIdentityUnavailable = errors.New("identity store unavailable")

// Principal identifies a Fleet installation authorized to establish a connection.
//
// The installation ID comes from a trusted identity verifier. An OpAMP instance
// UID is reported separately by the client and must never replace this identity.
type Principal struct {
	installationID string
	credentialID   string
}

// NewPrincipal creates an authenticated installation principal with its non-secret credential ID.
//
// No UUID format is imposed here because the cross-system identity format has
// not been finalized. Whitespace is rejected rather than silently normalized.
func NewPrincipal(installationID, credentialID string) (Principal, error) {
	if installationID == "" ||
		strings.TrimSpace(installationID) != installationID ||
		credentialID == "" || strings.TrimSpace(credentialID) != credentialID {
		return Principal{}, ErrInvalidPrincipal
	}

	return Principal{installationID: installationID, credentialID: credentialID}, nil
}

// InstallationID returns the stable installation identity owned by Core/Fleet.
func (p Principal) InstallationID() string {
	return p.installationID
}

// CredentialID returns the non-secret record identifier used to fence sessions.
func (p Principal) CredentialID() string { return p.credentialID }

// Valid reports whether the principal can identify an authenticated installation.
//
// The zero value is intentionally invalid, preventing an omitted verifier result
// from being accepted as an authenticated identity.
func (p Principal) Valid() bool {
	return p.installationID != "" &&
		strings.TrimSpace(p.installationID) == p.installationID &&
		p.credentialID != "" && strings.TrimSpace(p.credentialID) == p.credentialID
}
