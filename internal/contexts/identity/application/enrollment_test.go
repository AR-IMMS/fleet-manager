package application

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
)

func TestCreateInvitationReplaysSecretForIdenticalOperation(t *testing.T) {
	store := &memoryEnrollmentStore{invitations: make(map[string]domain.EnrollmentInvitation)}
	service := EnrollmentService{
		Store:            store,
		Entropy:          bytes.NewReader(bytes.Repeat([]byte{0x5a}, 256)),
		Now:              func() time.Time { return time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC) },
		DefaultInviteTTL: 10 * time.Minute,
		MaximumInviteTTL: time.Hour,
		RecoveryTTL:      24 * time.Hour,
	}
	command := CreateInvitationCommand{
		OperationID:    "operation-1",
		IdempotencyKey: "idempotency-1",
		ActorID:        "core-user-1",
		NodeID:         "node-1",
		RequestedTTL:   5 * time.Minute,
	}

	first, err := service.CreateInvitation(context.Background(), command)
	if err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	second, err := service.CreateInvitation(context.Background(), command)
	if err != nil {
		t.Fatalf("replay invitation: %v", err)
	}
	if first.InvitationID == "" || first.Secret == "" {
		t.Fatal("created invitation must include its ID and one-time secret")
	}
	if second.InvitationID != first.InvitationID || second.Secret != first.Secret {
		t.Fatal("same operation and request must replay the original invitation result")
	}
	if first.ExpiresAt != second.ExpiresAt {
		t.Fatal("replayed invitation must preserve its original expiry")
	}
}

func TestCreateInvitationRejectsChangedRequestForSameOperation(t *testing.T) {
	store := &memoryEnrollmentStore{invitations: make(map[string]domain.EnrollmentInvitation)}
	service := EnrollmentService{
		Store:            store,
		Entropy:          bytes.NewReader(bytes.Repeat([]byte{0x33}, 256)),
		Now:              time.Now,
		DefaultInviteTTL: 10 * time.Minute,
		MaximumInviteTTL: time.Hour,
		RecoveryTTL:      24 * time.Hour,
	}
	command := CreateInvitationCommand{
		OperationID: "operation-2", IdempotencyKey: "idempotency-2",
		ActorID: "core-user-1", NodeID: "node-1", RequestedTTL: 5 * time.Minute,
	}
	if _, err := service.CreateInvitation(context.Background(), command); err != nil {
		t.Fatalf("create initial invitation: %v", err)
	}
	command.NodeID = "node-2"
	if _, err := service.CreateInvitation(context.Background(), command); !errors.Is(err, domain.ErrEnrollmentConflict) {
		t.Fatalf("changed request should conflict, got %v", err)
	}
}

func TestExchangeReplayAndRecoveryReturnSameCredential(t *testing.T) {
	store := &memoryEnrollmentStore{
		invitations: make(map[string]domain.EnrollmentInvitation),
		attempts:    make(map[string]domain.EnrollmentAttempt),
	}
	service := EnrollmentService{
		Store:            store,
		Entropy:          bytes.NewReader(bytes.Repeat([]byte{0x17}, 512)),
		Now:              time.Now,
		DefaultInviteTTL: 10 * time.Minute,
		MaximumInviteTTL: time.Hour,
		RecoveryTTL:      24 * time.Hour,
		OpAMPEndpoint:    "wss://fleet.example.test/v1/opamp",
	}
	command := ExchangeInvitationCommand{
		AttemptID:       "attempt-1",
		AgentID:         "agent-1",
		InvitationToken: "invitation-secret-012345678901234567890123",
		RecoveryProof:   "recovery-secret-01234567890123456789012345",
	}

	first, err := service.Exchange(context.Background(), command)
	if err != nil {
		t.Fatalf("exchange invitation: %v", err)
	}
	second, err := service.Exchange(context.Background(), command)
	if err != nil {
		t.Fatalf("replay exchange: %v", err)
	}
	recovered, err := service.Recover(context.Background(), command.AttemptID, command.AgentID, command.RecoveryProof)
	if err != nil {
		t.Fatalf("recover exchange: %v", err)
	}
	if first.Credential == "" || first.CredentialID == "" {
		t.Fatal("exchange must return a credential and its non-secret ID")
	}
	if second.CredentialID != first.CredentialID || second.Credential != first.Credential {
		t.Fatal("same enrollment attempt must replay the original credential")
	}
	if recovered.CredentialID != first.CredentialID || recovered.Credential != first.Credential {
		t.Fatal("recovery proof must return the original credential")
	}
}

func TestExchangeRejectsDifferentRecoveryProofForExistingAttempt(t *testing.T) {
	store := &memoryEnrollmentStore{
		invitations: make(map[string]domain.EnrollmentInvitation),
		attempts:    make(map[string]domain.EnrollmentAttempt),
	}
	service := EnrollmentService{
		Store:            store,
		Entropy:          bytes.NewReader(bytes.Repeat([]byte{0x25}, 512)),
		DefaultInviteTTL: 10 * time.Minute,
		MaximumInviteTTL: time.Hour,
		RecoveryTTL:      24 * time.Hour,
		OpAMPEndpoint:    "wss://fleet.example.test/v1/opamp",
	}
	command := ExchangeInvitationCommand{
		AttemptID: "attempt-2", AgentID: "agent-2",
		InvitationToken: "invitation-secret-012345678901234567890123",
		RecoveryProof:   "recovery-secret-01234567890123456789012345",
	}
	if _, err := service.Exchange(context.Background(), command); err != nil {
		t.Fatalf("initial exchange: %v", err)
	}
	command.RecoveryProof = "different-recovery-secret-01234567890123456789"
	if _, err := service.Exchange(context.Background(), command); !errors.Is(err, domain.ErrEnrollmentConflict) {
		t.Fatalf("different proof must conflict, got %v", err)
	}
}

type memoryEnrollmentStore struct {
	invitations map[string]domain.EnrollmentInvitation
	attempts    map[string]domain.EnrollmentAttempt
}

func (s *memoryEnrollmentStore) CreateInvitation(
	_ context.Context,
	invitation domain.EnrollmentInvitation,
) (domain.EnrollmentInvitation, error) {
	if current, exists := s.invitations[invitation.OperationID]; exists {
		if current.Fingerprint != invitation.Fingerprint || current.IdempotencyHash != invitation.IdempotencyHash {
			return domain.EnrollmentInvitation{}, domain.ErrEnrollmentConflict
		}
		return current, nil
	}
	s.invitations[invitation.OperationID] = invitation
	return invitation, nil
}

func (s *memoryEnrollmentStore) Enroll(
	_ context.Context,
	attempt domain.EnrollmentAttempt,
) (domain.EnrollmentResult, error) {
	if current, exists := s.attempts[attempt.AttemptID]; exists {
		if attempt.AgentID != current.AgentID ||
			attempt.InvitationTokenHash != current.InvitationTokenHash ||
			attempt.RecoveryProofHash != current.RecoveryProofHash {
			return domain.EnrollmentResult{}, domain.ErrEnrollmentConflict
		}
		return current.Result, nil
	}
	s.attempts[attempt.AttemptID] = attempt
	return attempt.Result, nil
}

func (s *memoryEnrollmentStore) Recover(
	_ context.Context,
	attemptID, agentID, recoveryProofHash string,
) (domain.EnrollmentResult, error) {
	attempt, exists := s.attempts[attemptID]
	if !exists || attempt.AgentID != agentID || attempt.RecoveryProofHash != recoveryProofHash {
		return domain.EnrollmentResult{}, domain.ErrEnrollmentRecoveryRejected
	}
	return attempt.Result, nil
}

func (s *memoryEnrollmentStore) ChangeGate(
	_ context.Context,
	command ChangeEnrollmentGateCommand,
) (domain.EnrollmentGate, error) {
	return domain.EnrollmentGate{
		NodeID: command.NodeID, Status: command.Status, OperationID: command.OperationID, Version: 1,
	}, nil
}

func (*memoryEnrollmentStore) GetStatus(_ context.Context, operationID string) (domain.EnrollmentStatus, error) {
	return domain.EnrollmentStatus{OperationID: operationID, Status: string(domain.InvitationPending)}, nil
}
