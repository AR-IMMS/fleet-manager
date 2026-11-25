package domain

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrEnrollmentConflict         = errors.New("enrollment operation conflicts with existing state")
	ErrInvitationExpired          = errors.New("enrollment invitation is expired")
	ErrInvitationRejected         = errors.New("enrollment invitation is not valid")
	ErrEnrollmentRecoveryRejected = errors.New("enrollment recovery rejected")
	ErrEnrollmentNotFound         = errors.New("enrollment operation not found")
	ErrEnrollmentUnavailable      = errors.New("enrollment persistence unavailable")
)

type EnrollmentInvitationStatus string

const (
	InvitationPending   EnrollmentInvitationStatus = "PENDING"
	InvitationRedeemed  EnrollmentInvitationStatus = "REDEEMED"
	InvitationCancelled EnrollmentInvitationStatus = "CANCELLED"
	InvitationExpired   EnrollmentInvitationStatus = "EXPIRED"
)

// EnrollmentGateStatus is the Core-controlled state for one Node's enrollments.
type EnrollmentGateStatus string

const (
	EnrollmentGateOpen   EnrollmentGateStatus = "OPEN"
	EnrollmentGateClosed EnrollmentGateStatus = "CLOSED"
)

// EnrollmentGate is the durable Core-controlled enrollment state for one Node.
// EnrollmentGate reports the state and prior Agent binding observed by Core.
type EnrollmentGate struct {
	NodeID        string
	Status        EnrollmentGateStatus
	OperationID   string
	Version       int64
	BoundAgentIDs []string
}

// EnrollmentInvitation is the in-memory result of a preapproved installation.
// Secret is returned only to the trusted Core caller and is encrypted by the
// persistence adapter while a pending replay remains allowed.
type EnrollmentInvitation struct {
	OperationID     string
	CorePrincipalID string
	IdempotencyHash string
	Fingerprint     string
	ActorID         string
	NodeID          string
	InvitationID    string
	Secret          string
	Status          EnrollmentInvitationStatus
	ExpiresAt       time.Time
	Replayed        bool
}

// EnrollmentAttempt is a durable one-time exchange. Result is encrypted by
// the persistence adapter so a proven retry can recover the same credential.
// EnrollmentAttempt records one invitation redemption and time-bounded recovery.
type EnrollmentAttempt struct {
	AttemptID           string
	AgentID             string
	InvitationTokenHash string
	RecoveryProofHash   string
	RecoveryUntil       time.Time
	Result              EnrollmentResult
}

// OTLPCredential is issued separately for authenticated telemetry ingestion.
type OTLPCredential struct {
	ID        string
	Token     string
	Scope     string
	ExpiresAt *time.Time
}

// EnrollmentResult contains the one-time Fleet credential response.
type EnrollmentResult struct {
	AgentID             string
	NodeID              string
	CredentialID        string
	Credential          string
	OpAMPEndpoint       string
	CredentialExpiresAt *time.Time
	OTLPCredential      *OTLPCredential
}

// EnrollmentStatus is a secret-free enrollment summary for Core reconciliation.
type EnrollmentStatus struct {
	OperationID  string
	InvitationID string
	NodeID       string
	Status       string
	AgentID      string
	CredentialID string
}

// EnrollmentInstallation is a secret-free view of one Fleet credential binding.
// Status is ACTIVE or REVOKED; the Supervisor UID remains Fleet-owned and is
// intentionally omitted from Core's Node projection.
type EnrollmentInstallation struct {
	InstallationID string
	NodeID         string
	Status         string
}

// Validate checks the non-secret fields required to persist an invitation.
func (i EnrollmentInvitation) Validate() error {
	for _, value := range []string{
		i.OperationID, i.CorePrincipalID, i.IdempotencyHash, i.Fingerprint, i.ActorID,
		i.NodeID, i.InvitationID, i.Secret,
	} {
		if value == "" || strings.TrimSpace(value) != value {
			return ErrEnrollmentConflict
		}
	}
	if i.Status != InvitationPending || i.ExpiresAt.IsZero() {
		return ErrEnrollmentConflict
	}
	return nil
}
