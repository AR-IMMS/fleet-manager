package mongodb

import (
	"context"
	"encoding/base64"
	"errors"
	"io"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Recover returns the same credential only for the original attempt and proof
// while its encrypted recovery escrow remains within its configured TTL.
func (s *EnrollmentStore) Recover(ctx context.Context, attemptID, agentID, recoveryProofHash string) (domain.EnrollmentResult, error) {
	var prior attemptRecord
	err := s.database.Collection(attemptsCollectionName).FindOne(ctx, bson.M{
		"attempt_id": attemptID, "agent_id": agentID, "recovery_proof_hash": recoveryProofHash,
		"expires_at": bson.M{"$gt": s.clock()},
	}).Decode(&prior)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return domain.EnrollmentResult{}, domain.ErrEnrollmentRecoveryRejected
	}
	if err != nil {
		return domain.EnrollmentResult{}, errEnrollmentStoreUnavailable
	}
	result, err := s.openResult(prior.ResultCiphertext, []byte(prior.AttemptID))
	if err != nil {
		return domain.EnrollmentResult{}, errEnrollmentStoreUnavailable
	}
	return result, nil
}

// GetStatus returns the invitation and exchange state without disclosing secrets.
func (s *EnrollmentStore) GetStatus(ctx context.Context, operationID string) (domain.EnrollmentStatus, error) {
	var invitation invitationRecord
	err := s.database.Collection(invitationsCollectionName).FindOne(ctx, bson.M{"operation_id": operationID}).Decode(&invitation)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return domain.EnrollmentStatus{}, domain.ErrEnrollmentNotFound
	}
	if err != nil {
		return domain.EnrollmentStatus{}, errEnrollmentStoreUnavailable
	}
	status := domain.EnrollmentStatus{
		OperationID: operationID, InvitationID: invitation.InvitationID, NodeID: invitation.NodeID, Status: invitation.Status,
	}
	if invitation.Status == string(domain.InvitationPending) && !invitation.ExpiresAt.After(s.clock()) {
		status.Status = string(domain.InvitationExpired)
	}
	if invitation.Status == string(domain.InvitationRedeemed) {
		status.Status = "EXCHANGED"
		status.AgentID = invitation.AgentID
		status.CredentialID = invitation.CredentialID
	}
	return status, nil
}

// GetInstallationByAgentID returns only identifiers and active/revoked state.
func (s *EnrollmentStore) GetInstallationByAgentID(ctx context.Context, agentID string) (domain.EnrollmentInstallation, error) {
	var found record
	err := s.database.Collection(collectionName).FindOne(ctx, bson.M{"agent_id": agentID}).Decode(&found)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return domain.EnrollmentInstallation{}, domain.ErrEnrollmentNotFound
	}
	if err != nil {
		return domain.EnrollmentInstallation{}, errEnrollmentStoreUnavailable
	}
	if found.Status != "ACTIVE" && found.Status != "REVOKED" {
		return domain.EnrollmentInstallation{}, errEnrollmentStoreUnavailable
	}
	return domain.EnrollmentInstallation{
		InstallationID: found.AgentID, NodeID: found.NodeID, Status: found.Status,
	}, nil
}

// GetActiveInstallationByNode returns the sole active installation for a Node.
func (s *EnrollmentStore) GetActiveInstallationByNode(ctx context.Context, nodeID string) (domain.EnrollmentInstallation, error) {
	cursor, err := s.database.Collection(collectionName).Find(ctx, bson.M{"node_id": nodeID, "status": "ACTIVE"}, options.Find().SetLimit(2))
	if err != nil {
		return domain.EnrollmentInstallation{}, errEnrollmentStoreUnavailable
	}
	var matches []record
	allErr := cursor.All(ctx, &matches)
	closeErr := cursor.Close(ctx)
	if allErr != nil || closeErr != nil {
		return domain.EnrollmentInstallation{}, errEnrollmentStoreUnavailable
	}
	switch len(matches) {
	case 0:
		return domain.EnrollmentInstallation{}, domain.ErrEnrollmentNotFound
	case 1:
		found := matches[0]
		return domain.EnrollmentInstallation{
			InstallationID: found.AgentID, NodeID: found.NodeID, Status: found.Status,
		}, nil
	default:
		return domain.EnrollmentInstallation{}, domain.ErrEnrollmentConflict
	}
}

func (s *EnrollmentStore) seal(plaintext, associatedData []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(s.entropy, nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(nonce, nonce, plaintext, associatedData)
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (s *EnrollmentStore) open(value string, associatedData []byte) ([]byte, error) {
	sealed, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil || len(sealed) < s.aead.NonceSize() {
		return nil, errors.New("invalid enrollment ciphertext")
	}
	nonce, ciphertext := sealed[:s.aead.NonceSize()], sealed[s.aead.NonceSize():]
	return s.aead.Open(nil, nonce, ciphertext, associatedData)
}

func (s *EnrollmentStore) openResult(value string, associatedData []byte) (domain.EnrollmentResult, error) {
	plaintext, err := s.open(value, associatedData)
	if err != nil {
		return domain.EnrollmentResult{}, err
	}
	var result domain.EnrollmentResult
	if err := bson.Unmarshal(plaintext, &result); err != nil {
		return domain.EnrollmentResult{}, err
	}
	return result, nil
}
