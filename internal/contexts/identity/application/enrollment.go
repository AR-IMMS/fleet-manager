package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
)

// EnrollmentStore persists invitation operations and returns their canonical
// result. Implementations protect replay secrets at rest and enforce idempotency.
type EnrollmentStore interface {
	CreateInvitation(context.Context, domain.EnrollmentInvitation) (domain.EnrollmentInvitation, error)
	Enroll(context.Context, domain.EnrollmentAttempt) (domain.EnrollmentResult, error)
	Recover(context.Context, string, string, string) (domain.EnrollmentResult, error)
	ChangeGate(context.Context, ChangeEnrollmentGateCommand) (domain.EnrollmentGate, error)
	GetStatus(context.Context, string) (domain.EnrollmentStatus, error)
	GetInstallationByAgentID(context.Context, string) (domain.EnrollmentInstallation, error)
	GetActiveInstallationByNode(context.Context, string) (domain.EnrollmentInstallation, error)
}

// CreateInvitationCommand contains Core's stable operation and idempotency inputs.
type CreateInvitationCommand struct {
	OperationID     string
	CorePrincipalID string
	IdempotencyKey  string
	ActorID         string
	NodeID          string
	RequestedTTL    time.Duration
}

// InvitationResult is the canonical response for a pending invitation operation.
type InvitationResult struct {
	OperationID  string
	InvitationID string
	NodeID       string
	Secret       string
	Status       domain.EnrollmentInvitationStatus
	ExpiresAt    time.Time
	Replayed     bool
}

// ExchangeInvitationCommand contains the Agent's stable attempt and recovery proof.
type ExchangeInvitationCommand struct {
	AttemptID       string
	AgentID         string
	InvitationToken string
	RecoveryProof   string
}

// ChangeEnrollmentGateCommand carries a Core operation that opens or closes one Node gate.
type ChangeEnrollmentGateCommand struct {
	OperationID string
	NodeID      string
	Status      domain.EnrollmentGateStatus
}

// EnrollmentService owns invitation policy while its store owns durable
// idempotency and secret-at-rest protection.
// EnrollmentService owns invitation, exchange, recovery, and gate use cases.
type EnrollmentService struct {
	Store            EnrollmentStore
	Entropy          io.Reader
	Now              func() time.Time
	DefaultInviteTTL time.Duration
	MaximumInviteTTL time.Duration
	RecoveryTTL      time.Duration
	OpAMPEndpoint    string
}

// Exchange validates an invitation and returns one generated credential. The
// store atomically redeems the invitation and retains an encrypted replay result.
func (s EnrollmentService) Exchange(
	ctx context.Context,
	command ExchangeInvitationCommand,
) (domain.EnrollmentResult, error) {
	if err := s.validate(); err != nil {
		return domain.EnrollmentResult{}, err
	}
	if ctx == nil {
		return domain.EnrollmentResult{}, fmt.Errorf("enrollment request context is required")
	}
	if err := validateEnrollmentInput(command.AttemptID, "attempt ID"); err != nil {
		return domain.EnrollmentResult{}, err
	}
	if err := validateEnrollmentInput(command.AgentID, "Agent ID"); err != nil {
		return domain.EnrollmentResult{}, err
	}
	if len(command.InvitationToken) < 32 || strings.TrimSpace(command.InvitationToken) != command.InvitationToken {
		return domain.EnrollmentResult{}, fmt.Errorf("invitation secret is invalid")
	}
	if len(command.RecoveryProof) < 32 || strings.TrimSpace(command.RecoveryProof) != command.RecoveryProof {
		return domain.EnrollmentResult{}, fmt.Errorf("enrollment recovery proof is invalid")
	}
	if err := validateOpAMPEndpoint(s.OpAMPEndpoint); err != nil {
		return domain.EnrollmentResult{}, err
	}

	credentialID, err := randomSecret(s.entropy(), 18)
	if err != nil {
		return domain.EnrollmentResult{}, fmt.Errorf("generate credential ID: %w", err)
	}
	credential, err := randomSecret(s.entropy(), 32)
	if err != nil {
		return domain.EnrollmentResult{}, fmt.Errorf("generate enrollment credential: %w", err)
	}
	attempt := domain.EnrollmentAttempt{
		AttemptID:           command.AttemptID,
		AgentID:             command.AgentID,
		InvitationTokenHash: digest(command.InvitationToken),
		RecoveryProofHash:   digest(command.RecoveryProof),
		RecoveryUntil:       s.now().Add(s.RecoveryTTL),
		Result: domain.EnrollmentResult{
			AgentID:       command.AgentID,
			CredentialID:  credentialID,
			Credential:    credential,
			OpAMPEndpoint: s.OpAMPEndpoint,
		},
	}
	return s.Store.Enroll(ctx, attempt)
}

// Recover returns the same enrollment result only when the attempt, installation,
// and high-entropy recovery proof all match the persisted exchange.
func (s EnrollmentService) Recover(
	ctx context.Context,
	attemptID, agentID, recoveryProof string,
) (domain.EnrollmentResult, error) {
	if err := s.validate(); err != nil {
		return domain.EnrollmentResult{}, err
	}
	if ctx == nil {
		return domain.EnrollmentResult{}, fmt.Errorf("enrollment request context is required")
	}
	if err := validateEnrollmentInput(attemptID, "attempt ID"); err != nil {
		return domain.EnrollmentResult{}, err
	}
	if err := validateEnrollmentInput(agentID, "Agent ID"); err != nil {
		return domain.EnrollmentResult{}, err
	}
	if len(recoveryProof) < 32 || strings.TrimSpace(recoveryProof) != recoveryProof {
		return domain.EnrollmentResult{}, domain.ErrEnrollmentRecoveryRejected
	}
	return s.Store.Recover(ctx, attemptID, agentID, digest(recoveryProof))
}

// ChangeGate records an idempotent Core lifecycle gate operation.
func (s EnrollmentService) ChangeGate(ctx context.Context, command ChangeEnrollmentGateCommand) (domain.EnrollmentGate, error) {
	if err := s.validate(); err != nil {
		return domain.EnrollmentGate{}, err
	}
	if ctx == nil || validateEnrollmentInput(command.OperationID, "operation ID") != nil || validateEnrollmentInput(command.NodeID, "Node ID") != nil {
		return domain.EnrollmentGate{}, fmt.Errorf("enrollment gate operation is invalid")
	}
	if command.Status != domain.EnrollmentGateOpen && command.Status != domain.EnrollmentGateClosed {
		return domain.EnrollmentGate{}, fmt.Errorf("enrollment gate status is invalid")
	}
	return s.Store.ChangeGate(ctx, command)
}

// GetStatus returns the secret-free state Core needs to reconcile its own lifecycle journal.
func (s EnrollmentService) GetStatus(ctx context.Context, operationID string) (domain.EnrollmentStatus, error) {
	if err := s.validate(); err != nil {
		return domain.EnrollmentStatus{}, err
	}
	if ctx == nil || validateEnrollmentInput(operationID, "operation ID") != nil {
		return domain.EnrollmentStatus{}, fmt.Errorf("enrollment operation ID is invalid")
	}
	return s.Store.GetStatus(ctx, operationID)
}

// GetInstallationByAgentID returns a secret-free installation and credential state.
func (s EnrollmentService) GetInstallationByAgentID(ctx context.Context, agentID string) (domain.EnrollmentInstallation, error) {
	if err := s.validate(); err != nil {
		return domain.EnrollmentInstallation{}, err
	}
	if ctx == nil || validateEnrollmentInput(agentID, "Agent ID") != nil {
		return domain.EnrollmentInstallation{}, fmt.Errorf("enrollment Agent ID is invalid")
	}
	return s.Store.GetInstallationByAgentID(ctx, agentID)
}

// GetActiveInstallationByNode returns the active secret-free installation for a Node.
func (s EnrollmentService) GetActiveInstallationByNode(ctx context.Context, nodeID string) (domain.EnrollmentInstallation, error) {
	if err := s.validate(); err != nil {
		return domain.EnrollmentInstallation{}, err
	}
	if ctx == nil || validateEnrollmentInput(nodeID, "Node ID") != nil {
		return domain.EnrollmentInstallation{}, fmt.Errorf("enrollment Node ID is invalid")
	}
	return s.Store.GetActiveInstallationByNode(ctx, nodeID)
}

// CreateInvitation creates or idempotently replays one preapproved invitation.
func (s EnrollmentService) CreateInvitation(
	ctx context.Context,
	command CreateInvitationCommand,
) (InvitationResult, error) {
	if err := s.validate(); err != nil {
		return InvitationResult{}, err
	}
	if ctx == nil {
		return InvitationResult{}, fmt.Errorf("enrollment request context is required")
	}
	for name, value := range map[string]string{
		"Core principal ID": command.CorePrincipalID,
		"operation ID":      command.OperationID,
		"idempotency key":   command.IdempotencyKey,
		"actor ID":          command.ActorID,
		"Node ID":           command.NodeID,
	} {
		if value == "" || strings.TrimSpace(value) != value {
			return InvitationResult{}, fmt.Errorf("enrollment %s is invalid", name)
		}
	}

	ttl := command.RequestedTTL
	if ttl == 0 {
		ttl = s.DefaultInviteTTL
	}
	if ttl < time.Minute || ttl > s.MaximumInviteTTL {
		return InvitationResult{}, fmt.Errorf("requested invitation lifetime is outside the allowed range")
	}

	invitationID, err := randomSecret(s.entropy(), 18)
	if err != nil {
		return InvitationResult{}, fmt.Errorf("generate enrollment invitation ID: %w", err)
	}
	secret, err := randomSecret(s.entropy(), 32)
	if err != nil {
		return InvitationResult{}, fmt.Errorf("generate enrollment invitation secret: %w", err)
	}
	invitation := domain.EnrollmentInvitation{
		OperationID:     command.OperationID,
		CorePrincipalID: command.CorePrincipalID,
		IdempotencyHash: digest(command.CorePrincipalID + "\x00" + command.IdempotencyKey),
		Fingerprint:     fingerprint(command, ttl),
		ActorID:         command.ActorID,
		NodeID:          command.NodeID,
		InvitationID:    invitationID,
		Secret:          secret,
		Status:          domain.InvitationPending,
		ExpiresAt:       s.now().Add(ttl),
	}
	canonical, err := s.Store.CreateInvitation(ctx, invitation)
	if err != nil {
		return InvitationResult{}, err
	}
	return InvitationResult{
		OperationID:  canonical.OperationID,
		InvitationID: canonical.InvitationID,
		NodeID:       canonical.NodeID,
		Secret:       canonical.Secret,
		Status:       canonical.Status,
		ExpiresAt:    canonical.ExpiresAt,
		Replayed:     canonical.Replayed,
	}, nil
}

func (s EnrollmentService) validate() error {
	if s.Store == nil {
		return fmt.Errorf("enrollment store is required")
	}
	if s.DefaultInviteTTL <= 0 || s.MaximumInviteTTL < s.DefaultInviteTTL || s.RecoveryTTL <= 0 {
		return fmt.Errorf("valid invitation and recovery lifetime bounds are required")
	}
	return nil
}

func (s EnrollmentService) entropy() io.Reader {
	if s.Entropy == nil {
		return rand.Reader
	}
	return s.Entropy
}

func (s EnrollmentService) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func randomSecret(entropy io.Reader, size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(entropy, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func fingerprint(command CreateInvitationCommand, ttl time.Duration) string {
	value := strings.Join([]string{
		command.CorePrincipalID,
		"CREATE_INVITATION",
		command.NodeID,
		ttl.String(),
	}, "\x00")
	return digest(value)
}

func validateEnrollmentInput(value, name string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("enrollment %s is invalid", name)
	}
	return nil
}

func validateOpAMPEndpoint(value string) error {
	endpoint, err := url.Parse(value)
	if err != nil || !strings.EqualFold(endpoint.Scheme, "wss") || endpoint.Hostname() == "" ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return fmt.Errorf("fleet OpAMP endpoint must be a WSS URL")
	}
	return nil
}
