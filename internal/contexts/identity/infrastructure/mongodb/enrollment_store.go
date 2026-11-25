package mongodb

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/application"
	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	invitationsCollectionName = "identity_enrollment_invitations"
	invitationEscrowName      = "identity_enrollment_invitation_escrow"
	attemptsCollectionName    = "identity_enrollment_attempts"
	gatesCollectionName       = "identity_enrollment_gates"
	gateOperationsName        = "identity_enrollment_gate_operations"
)

var errEnrollmentStoreUnavailable = domain.ErrEnrollmentUnavailable

type invitationRecord struct {
	ID              string    `bson:"_id"`
	OperationID     string    `bson:"operation_id"`
	CorePrincipalID string    `bson:"core_principal_id"`
	IdempotencyHash string    `bson:"idempotency_hash"`
	Fingerprint     string    `bson:"fingerprint"`
	ActorID         string    `bson:"actor_id"`
	NodeID          string    `bson:"node_id"`
	InvitationID    string    `bson:"invitation_id"`
	TokenHash       string    `bson:"token_hash"`
	Status          string    `bson:"status"`
	ExpiresAt       time.Time `bson:"expires_at"`
	AgentID         string    `bson:"agent_id,omitempty"`
	CredentialID    string    `bson:"credential_id,omitempty"`
}

type invitationEscrow struct {
	InvitationID string    `bson:"_id"`
	Ciphertext   string    `bson:"ciphertext"`
	ExpiresAt    time.Time `bson:"expires_at"`
}

type attemptRecord struct {
	AttemptID           string    `bson:"attempt_id"`
	AgentID             string    `bson:"agent_id"`
	CredentialID        string    `bson:"credential_id"`
	ExpiresAt           time.Time `bson:"expires_at"`
	InvitationTokenHash string    `bson:"invitation_token_hash"`
	RecoveryProofHash   string    `bson:"recovery_proof_hash"`
	ResultCiphertext    string    `bson:"result_ciphertext"`
	CreatedAt           time.Time `bson:"created_at"`
}

type gateRecord struct {
	NodeID               string   `bson:"_id"`
	Status               string   `bson:"status"`
	OperationID          string   `bson:"operation_id,omitempty"`
	Version              int64    `bson:"version"`
	Touch                int64    `bson:"touch"`
	BoundAgentIDs        []string `bson:"bound_agent_ids"`
	PendingOperationID   string   `bson:"pending_operation_id,omitempty"`
	PreviousState        string   `bson:"previous_state,omitempty"`
	LastOperationID      string   `bson:"last_operation_id,omitempty"`
	LastOperationVersion int64    `bson:"last_operation_version,omitempty"`
	LastOperationState   string   `bson:"last_operation_state,omitempty"`
}

type gateOperationRecord struct {
	OperationID string     `bson:"_id"`
	NodeID      string     `bson:"node_id"`
	Status      string     `bson:"status"`
	Gate        gateRecord `bson:"gate"`
}

// EnrollmentStore persists recoverable enrollment results as authenticated
// ciphertext. Enrollment and credential insertion share a Mongo transaction.
type EnrollmentStore struct {
	database *mongo.Database
	client   *mongo.Client
	verifier *Verifier
	aead     cipher.AEAD
	clock    func() time.Time
	entropy  io.Reader
}

// NewEnrollmentStore creates a transactional Mongo adapter. The encryption
// key is a deployment secret and must contain exactly 32 bytes.
func NewEnrollmentStore(database *mongo.Database, encryptionKey []byte) (*EnrollmentStore, error) {
	if database == nil {
		return nil, fmt.Errorf("enrollment database is required")
	}
	if len(encryptionKey) != 32 {
		return nil, fmt.Errorf("enrollment encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("initialize enrollment encryption: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize enrollment encryption: %w", err)
	}
	verifier, err := NewVerifier(database)
	if err != nil {
		return nil, err
	}
	return &EnrollmentStore{
		database: database,
		client:   database.Client(),
		verifier: verifier,
		aead:     aead,
		clock:    func() time.Time { return time.Now().UTC() },
		entropy:  rand.Reader,
	}, nil
}

// EnsureEnrollmentIndexes installs indexes used by enrollment idempotency and
// uniqueness. It is separate from versioned migration history.
func EnsureEnrollmentIndexes(ctx context.Context, database *mongo.Database) error {
	if database == nil {
		return fmt.Errorf("enrollment index database is required")
	}
	_, err := database.Collection(invitationsCollectionName).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "operation_id", Value: 1}}, Options: options.Index().SetName("operation_id_1").SetUnique(true)},
		{Keys: bson.D{{Key: "core_principal_id", Value: 1}, {Key: "idempotency_hash", Value: 1}}, Options: options.Index().SetName("core_principal_id_1_idempotency_hash_1").SetUnique(true).SetPartialFilterExpression(bson.M{"core_principal_id": bson.M{"$type": "string"}})},
		{Keys: bson.D{{Key: "invitation_id", Value: 1}}, Options: options.Index().SetName("invitation_id_1").SetUnique(true)},
		{Keys: bson.D{{Key: "token_hash", Value: 1}}, Options: options.Index().SetName("token_hash_1").SetUnique(true)},
	})
	if err != nil {
		return fmt.Errorf("reconcile enrollment invitation indexes: %w", err)
	}
	_, err = database.Collection(attemptsCollectionName).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "attempt_id", Value: 1}}, Options: options.Index().SetName("attempt_id_1").SetUnique(true)},
		{Keys: bson.D{{Key: "agent_id", Value: 1}}, Options: options.Index().SetName("agent_id_1").SetUnique(true)},
		{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: options.Index().SetName("expires_at_1").SetExpireAfterSeconds(0)},
	})
	if err != nil {
		return fmt.Errorf("reconcile enrollment attempt indexes: %w", err)
	}
	_, err = database.Collection(invitationEscrowName).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: options.Index().SetName("expires_at_1").SetExpireAfterSeconds(0),
	})
	if err != nil {
		return fmt.Errorf("reconcile invitation escrow indexes: %w", err)
	}
	_, err = database.Collection(gateOperationsName).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "node_id", Value: 1}, {Key: "operation_id", Value: 1}},
		Options: options.Index().SetName("node_id_1_operation_id_1"),
	})
	if err != nil {
		return fmt.Errorf("reconcile enrollment gate operation indexes: %w", err)
	}
	if err := dropLegacyActorIdempotencyIndex(ctx, database.Collection(invitationsCollectionName)); err != nil {
		return fmt.Errorf("remove legacy actor-scoped invitation idempotency index: %w", err)
	}
	return nil
}

func dropLegacyActorIdempotencyIndex(ctx context.Context, collection *mongo.Collection) error {
	indexes := collection.Indexes()
	exists, err := hasIndex(ctx, indexes, "actor_id_1_idempotency_hash_1")
	if err != nil || !exists {
		return err
	}
	if err := indexes.DropOne(ctx, "actor_id_1_idempotency_hash_1"); err != nil {
		stillExists, inspectErr := hasIndex(ctx, indexes, "actor_id_1_idempotency_hash_1")
		if inspectErr == nil && !stillExists {
			return nil
		}
		return err
	}
	return nil
}

func hasIndex(ctx context.Context, indexes mongo.IndexView, name string) (bool, error) {
	cursor, err := indexes.List(ctx)
	if err != nil {
		return false, err
	}
	var specifications []struct {
		Name string `bson:"name"`
	}
	if err := cursor.All(ctx, &specifications); err != nil {
		return false, err
	}
	if err := cursor.Close(ctx); err != nil {
		return false, err
	}
	for _, specification := range specifications {
		if specification.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// VerifyTransactionSupport fails startup when the Mongo deployment cannot
// provide the atomicity required by enrollment and gate operations.
func (s *EnrollmentStore) VerifyTransactionSupport(ctx context.Context) error {
	session, err := s.client.StartSession()
	if err != nil {
		return errEnrollmentStoreUnavailable
	}
	defer session.EndSession(ctx)
	if _, err := session.WithTransaction(ctx, func(context.Context) (any, error) { return nil, nil }); err != nil {
		return errEnrollmentStoreUnavailable
	}
	return nil
}

// CreateInvitation stores a pending invitation or returns its canonical result
// for an identical idempotent retry.
func (s *EnrollmentStore) CreateInvitation(ctx context.Context, invitation domain.EnrollmentInvitation) (domain.EnrollmentInvitation, error) {
	if err := invitation.Validate(); err != nil {
		return domain.EnrollmentInvitation{}, err
	}
	ciphertext, err := s.seal([]byte(invitation.Secret), []byte(invitation.InvitationID))
	if err != nil {
		return domain.EnrollmentInvitation{}, errEnrollmentStoreUnavailable
	}
	var stored invitationRecord
	var storedSecret string
	replayed := false
	session, err := s.client.StartSession()
	if err != nil {
		return domain.EnrollmentInvitation{}, errEnrollmentStoreUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		collection := s.database.Collection(invitationsCollectionName)
		filter := bson.M{"core_principal_id": invitation.CorePrincipalID, "idempotency_hash": invitation.IdempotencyHash}
		if findErr := collection.FindOne(txCtx, filter).Decode(&stored); findErr == nil {
			if stored.OperationID != invitation.OperationID || stored.Fingerprint != invitation.Fingerprint {
				return nil, domain.ErrEnrollmentConflict
			}
			var escrow invitationEscrow
			if escrowErr := s.database.Collection(invitationEscrowName).FindOne(txCtx, bson.M{"_id": stored.InvitationID, "expires_at": bson.M{"$gt": s.clock()}}).Decode(&escrow); escrowErr != nil {
				return nil, domain.ErrInvitationRejected
			}
			storedSecret = escrow.Ciphertext
			replayed = true
			return stored, nil
		} else if !errors.Is(findErr, mongo.ErrNoDocuments) {
			return nil, findErr
		}
		if err := s.touchOpenGate(txCtx, invitation.NodeID, ""); err != nil {
			return nil, err
		}
		stored = invitationRecord{
			ID: invitation.OperationID, OperationID: invitation.OperationID,
			CorePrincipalID: invitation.CorePrincipalID,
			IdempotencyHash: invitation.IdempotencyHash, Fingerprint: invitation.Fingerprint,
			ActorID: invitation.ActorID, NodeID: invitation.NodeID, InvitationID: invitation.InvitationID,
			TokenHash: tokenDigest(invitation.Secret),
			Status:    string(invitation.Status), ExpiresAt: invitation.ExpiresAt,
		}
		if _, insertErr := collection.InsertOne(txCtx, stored); insertErr != nil {
			if mongo.IsDuplicateKeyError(insertErr) {
				// Resolve a concurrent insert after the transaction aborts. The
				// winner is canonical for this principal/key; the caller below
				// still checks its operation and normalized request fingerprint.
				return nil, insertErr
			}
			return nil, insertErr
		}
		if _, insertErr := s.database.Collection(invitationEscrowName).InsertOne(txCtx, invitationEscrow{
			InvitationID: invitation.InvitationID, Ciphertext: ciphertext, ExpiresAt: invitation.ExpiresAt,
		}); insertErr != nil {
			return nil, insertErr
		}
		storedSecret = ciphertext
		return stored, nil
	})
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			collection := s.database.Collection(invitationsCollectionName)
			filter := bson.M{"core_principal_id": invitation.CorePrincipalID, "idempotency_hash": invitation.IdempotencyHash}
			if findErr := collection.FindOne(ctx, filter).Decode(&stored); findErr == nil {
				if stored.OperationID != invitation.OperationID || stored.Fingerprint != invitation.Fingerprint {
					return domain.EnrollmentInvitation{}, domain.ErrEnrollmentConflict
				}
				var escrow invitationEscrow
				if escrowErr := s.database.Collection(invitationEscrowName).FindOne(ctx, bson.M{"_id": stored.InvitationID, "expires_at": bson.M{"$gt": s.clock()}}).Decode(&escrow); escrowErr != nil {
					if errors.Is(escrowErr, mongo.ErrNoDocuments) {
						return domain.EnrollmentInvitation{}, domain.ErrInvitationRejected
					}
					return domain.EnrollmentInvitation{}, errEnrollmentStoreUnavailable
				}
				storedSecret = escrow.Ciphertext
				replayed = true
			} else if errors.Is(findErr, mongo.ErrNoDocuments) {
				// A duplicate on another unique key (for example an operation
				// ID collision) is not an idempotent replay.
				return domain.EnrollmentInvitation{}, domain.ErrEnrollmentConflict
			} else {
				return domain.EnrollmentInvitation{}, errEnrollmentStoreUnavailable
			}
		} else {
			if errors.Is(err, domain.ErrInvitationRejected) || errors.Is(err, domain.ErrEnrollmentConflict) {
				return domain.EnrollmentInvitation{}, err
			}
			return domain.EnrollmentInvitation{}, errEnrollmentStoreUnavailable
		}
	}
	if stored.OperationID != invitation.OperationID || stored.Fingerprint != invitation.Fingerprint {
		return domain.EnrollmentInvitation{}, domain.ErrEnrollmentConflict
	}
	if storedSecret == "" {
		return domain.EnrollmentInvitation{}, domain.ErrInvitationRejected
	}
	secret, err := s.open(storedSecret, []byte(stored.InvitationID))
	if err != nil {
		return domain.EnrollmentInvitation{}, errEnrollmentStoreUnavailable
	}
	invitation.InvitationID = stored.InvitationID
	invitation.Secret = string(secret)
	invitation.Status = domain.EnrollmentInvitationStatus(stored.Status)
	invitation.ExpiresAt = stored.ExpiresAt
	invitation.Replayed = replayed
	return invitation, nil
}

// CancelInvitation atomically cancels an unconsumed invitation and erases its replay secret.
func (s *EnrollmentStore) CancelInvitation(ctx context.Context, invitationID, operationID string) error {
	session, err := s.client.StartSession()
	if err != nil {
		return errEnrollmentStoreUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		invites := s.database.Collection(invitationsCollectionName)
		var invitation invitationRecord
		if findErr := invites.FindOne(txCtx, bson.M{"invitation_id": invitationID}).Decode(&invitation); errors.Is(findErr, mongo.ErrNoDocuments) {
			return nil, domain.ErrEnrollmentNotFound
		} else if findErr != nil {
			return nil, findErr
		}
		if invitation.OperationID != operationID {
			return nil, domain.ErrEnrollmentConflict
		}
		switch invitation.Status {
		case string(domain.InvitationCancelled), string(domain.InvitationExpired):
			return nil, nil
		case string(domain.InvitationRedeemed):
			return nil, domain.ErrEnrollmentConflict
		case string(domain.InvitationPending):
		default:
			return nil, domain.ErrEnrollmentConflict
		}
		result, updateErr := invites.UpdateOne(txCtx, bson.M{"_id": invitation.ID, "status": string(domain.InvitationPending)}, bson.M{"$set": bson.M{"status": string(domain.InvitationCancelled)}})
		if updateErr != nil {
			return nil, updateErr
		}
		if result.MatchedCount != 1 {
			return nil, domain.ErrEnrollmentConflict
		}
		if _, deleteErr := s.database.Collection(invitationEscrowName).DeleteOne(txCtx, bson.M{"_id": invitation.InvitationID}); deleteErr != nil {
			return nil, deleteErr
		}
		return nil, nil
	})
	if err != nil {
		if errors.Is(err, domain.ErrEnrollmentNotFound) || errors.Is(err, domain.ErrEnrollmentConflict) {
			return err
		}
		return errEnrollmentStoreUnavailable
	}
	return nil
}

// Enroll atomically consumes a valid invitation, creates an identity credential,
// and stores an encrypted result for retry or recovery.
func (s *EnrollmentStore) Enroll(ctx context.Context, attempt domain.EnrollmentAttempt) (domain.EnrollmentResult, error) {
	if attempt.AttemptID == "" || attempt.AgentID == "" || attempt.Result.CredentialID == "" || attempt.Result.Credential == "" ||
		attempt.Result.OTLPCredential == nil || attempt.Result.OTLPCredential.ID == "" || len(attempt.Result.OTLPCredential.Token) < 32 ||
		attempt.Result.OTLPCredential.Scope != "otlp.write" || attempt.Result.OTLPCredential.ID == attempt.Result.CredentialID ||
		attempt.Result.OTLPCredential.Token == attempt.Result.Credential || !attempt.RecoveryUntil.After(s.clock()) {
		return domain.EnrollmentResult{}, domain.ErrInvitationRejected
	}
	result := attempt.Result
	session, err := s.client.StartSession()
	if err != nil {
		return domain.EnrollmentResult{}, errEnrollmentStoreUnavailable
	}
	defer session.EndSession(ctx)
	transactionResult, err := session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		attempts := s.database.Collection(attemptsCollectionName)
		var prior attemptRecord
		readErr := attempts.FindOne(txCtx, bson.M{"attempt_id": attempt.AttemptID}).Decode(&prior)
		if readErr == nil {
			if prior.AgentID != attempt.AgentID || prior.InvitationTokenHash != attempt.InvitationTokenHash || prior.RecoveryProofHash != attempt.RecoveryProofHash {
				return nil, domain.ErrEnrollmentConflict
			}
			if !prior.ExpiresAt.After(s.clock()) {
				return nil, domain.ErrEnrollmentRecoveryRejected
			}
			result, decodeErr := s.openResult(prior.ResultCiphertext, []byte(prior.AttemptID))
			return result, decodeErr
		}
		if !errors.Is(readErr, mongo.ErrNoDocuments) {
			return nil, readErr
		}
		invites := s.database.Collection(invitationsCollectionName)
		var invite invitationRecord
		now := s.clock()
		pendingInvitation := bson.M{"token_hash": attempt.InvitationTokenHash, "status": string(domain.InvitationPending)}
		activeInvitation := bson.M{"token_hash": attempt.InvitationTokenHash, "status": string(domain.InvitationPending), "expires_at": bson.M{"$gt": now}}
		if findErr := invites.FindOne(txCtx, activeInvitation).Decode(&invite); errors.Is(findErr, mongo.ErrNoDocuments) {
			var expired invitationRecord
			if expiredErr := invites.FindOne(txCtx, pendingInvitation).Decode(&expired); errors.Is(expiredErr, mongo.ErrNoDocuments) {
				return nil, domain.ErrInvitationRejected
			} else if expiredErr != nil {
				return nil, expiredErr
			}
			if !expired.ExpiresAt.After(now) {
				return nil, domain.ErrInvitationExpired
			}
			return nil, domain.ErrInvitationRejected
		} else if findErr != nil {
			return nil, findErr
		}
		if err := s.touchOpenGate(txCtx, invite.NodeID, attempt.AgentID); err != nil {
			return nil, err
		}
		updateResult, updateErr := invites.UpdateOne(txCtx, bson.M{"_id": invite.ID, "status": string(domain.InvitationPending), "expires_at": bson.M{"$gt": s.clock()}}, bson.M{"$set": bson.M{
			"status": string(domain.InvitationRedeemed), "agent_id": attempt.AgentID, "credential_id": result.CredentialID,
		}})
		if updateErr != nil {
			return nil, updateErr
		}
		if updateResult.MatchedCount != 1 {
			var current invitationRecord
			lookupErr := invites.FindOne(txCtx, bson.M{"_id": invite.ID}).Decode(&current)
			if errors.Is(lookupErr, mongo.ErrNoDocuments) {
				return nil, domain.ErrInvitationRejected
			}
			if lookupErr != nil {
				return nil, lookupErr
			}
			if current.Status != string(domain.InvitationPending) {
				return nil, domain.ErrInvitationRejected
			}
			if !current.ExpiresAt.After(s.clock()) {
				return nil, domain.ErrInvitationExpired
			}
			return nil, domain.ErrEnrollmentConflict
		}
		if _, deleteErr := s.database.Collection(invitationEscrowName).DeleteOne(txCtx, bson.M{"_id": invite.InvitationID}); deleteErr != nil {
			return nil, deleteErr
		}
		result.NodeID = invite.NodeID
		if err := s.verifier.storeCredentialForNode(txCtx, attempt.AgentID, invite.NodeID, result.CredentialID, result.Credential); err != nil {
			return nil, err
		}
		if err := s.verifier.storeScopedCredentialForNodeWithExpiry(txCtx, attempt.AgentID, "",
			result.OTLPCredential.ID, result.OTLPCredential.Token, "otlp.write", result.OTLPCredential.ExpiresAt); err != nil {
			return nil, err
		}
		serialized, marshalErr := bson.Marshal(result)
		if marshalErr != nil {
			return nil, errEnrollmentStoreUnavailable
		}
		resultCiphertext, sealErr := s.seal(serialized, []byte(attempt.AttemptID))
		if sealErr != nil {
			return nil, errEnrollmentStoreUnavailable
		}
		_, insertErr := attempts.InsertOne(txCtx, attemptRecord{
			AttemptID: attempt.AttemptID, AgentID: attempt.AgentID, CredentialID: result.CredentialID,
			InvitationTokenHash: attempt.InvitationTokenHash, RecoveryProofHash: attempt.RecoveryProofHash,
			ResultCiphertext: resultCiphertext, CreatedAt: s.clock(), ExpiresAt: attempt.RecoveryUntil,
		})
		if insertErr != nil {
			return nil, domain.ErrEnrollmentConflict
		}
		return result, nil
	})
	if err != nil {
		if errors.Is(err, domain.ErrEnrollmentConflict) || errors.Is(err, domain.ErrInvitationRejected) || errors.Is(err, domain.ErrInvitationExpired) || errors.Is(err, domain.ErrEnrollmentRecoveryRejected) {
			return domain.EnrollmentResult{}, err
		}
		return domain.EnrollmentResult{}, errEnrollmentStoreUnavailable
	}
	result, ok := transactionResult.(domain.EnrollmentResult)
	if !ok {
		return domain.EnrollmentResult{}, errEnrollmentStoreUnavailable
	}
	return result, nil
}

// CloseGate atomically fences exchanges and snapshots the prior state for compensation.
func (s *EnrollmentStore) CloseGate(ctx context.Context, nodeID, operationID string) (application.EnrollmentGateTransition, error) {
	session, err := s.client.StartSession()
	if err != nil {
		return application.EnrollmentGateTransition{}, errEnrollmentStoreUnavailable
	}
	defer session.EndSession(ctx)
	value, err := session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		gates := s.database.Collection(gatesCollectionName)
		var gate gateRecord
		if findErr := gates.FindOne(txCtx, bson.M{"_id": nodeID}).Decode(&gate); errors.Is(findErr, mongo.ErrNoDocuments) {
			gate = gateRecord{NodeID: nodeID, Status: string(domain.EnrollmentGateOpen), BoundAgentIDs: []string{}}
			if _, insertErr := gates.InsertOne(txCtx, gate); insertErr != nil {
				return nil, insertErr
			}
		} else if findErr != nil {
			return nil, findErr
		}
		if gate.PendingOperationID != "" {
			if gate.PendingOperationID != operationID {
				return nil, domain.ErrEnrollmentConflict
			}
			transition, transitionErr := transitionFromGate(txCtx, s.database, gate, gate.PreviousState)
			return transition, transitionErr
		}
		if gate.LastOperationID == operationID {
			transition, transitionErr := transitionFromGate(txCtx, s.database, gate, gate.PreviousState)
			return transition, transitionErr
		}
		previous := gate.Status
		gate.PendingOperationID = operationID
		gate.PreviousState = previous
		gate.Status = string(domain.EnrollmentGateClosed)
		gate.Version++
		if _, updateErr := gates.UpdateOne(txCtx, bson.M{"_id": nodeID}, bson.M{"$set": bson.M{"status": gate.Status, "version": gate.Version, "pending_operation_id": operationID, "previous_state": previous}}); updateErr != nil {
			return nil, updateErr
		}
		transition, transitionErr := transitionFromGate(txCtx, s.database, gate, previous)
		return transition, transitionErr
	})
	if err != nil {
		if errors.Is(err, domain.ErrEnrollmentConflict) {
			return application.EnrollmentGateTransition{}, err
		}
		return application.EnrollmentGateTransition{}, errEnrollmentStoreUnavailable
	}
	transition, ok := value.(application.EnrollmentGateTransition)
	if !ok {
		return application.EnrollmentGateTransition{}, errEnrollmentStoreUnavailable
	}
	return transition, nil
}

func transitionFromGate(ctx context.Context, database *mongo.Database, gate gateRecord, previous string) (application.EnrollmentGateTransition, error) {
	transition := application.EnrollmentGateTransition{NodeID: gate.NodeID, OperationID: gate.PendingOperationID, Version: gate.Version, PreviousState: domain.EnrollmentGateStatus(previous)}
	if transition.OperationID == "" {
		transition.OperationID = gate.LastOperationID
		if gate.LastOperationVersion > 0 {
			transition.Version = gate.LastOperationVersion
		}
	}
	var active record
	if err := database.Collection(collectionName).FindOne(ctx, bson.M{"node_id": gate.NodeID, "status": "ACTIVE"}).Decode(&active); err == nil {
		transition.ActiveInstallationID = active.AgentID
		transition.ActiveBindingID = active.CredentialID
	} else if !errors.Is(err, mongo.ErrNoDocuments) {
		return application.EnrollmentGateTransition{}, err
	}
	return transition, nil
}

// CompleteGate commits or compensates only the transition version returned by CloseGate.
func (s *EnrollmentStore) CompleteGate(ctx context.Context, nodeID, operationID string, version int64, state domain.EnrollmentGateStatus) error {
	result, err := s.database.Collection(gatesCollectionName).UpdateOne(ctx, bson.M{"_id": nodeID, "pending_operation_id": operationID, "version": version},
		bson.M{"$set": bson.M{"status": string(state), "last_operation_id": operationID, "last_operation_version": version, "last_operation_state": string(state)}, "$inc": bson.M{"version": 1}, "$unset": bson.M{"pending_operation_id": "", "previous_state": ""}})
	if err != nil {
		return errEnrollmentStoreUnavailable
	}
	if result.MatchedCount == 1 {
		return nil
	}
	var gate gateRecord
	if lookupErr := s.database.Collection(gatesCollectionName).FindOne(ctx, bson.M{"_id": nodeID}).Decode(&gate); lookupErr != nil {
		if errors.Is(lookupErr, mongo.ErrNoDocuments) {
			return domain.ErrEnrollmentNotFound
		}
		return errEnrollmentStoreUnavailable
	}
	if gate.LastOperationID == operationID && gate.LastOperationVersion == version && gate.LastOperationState == string(state) {
		return nil
	}
	return domain.ErrEnrollmentConflict
}

// ChangeGate serializes lifecycle changes against invitation creation and exchange.
func (s *EnrollmentStore) ChangeGate(ctx context.Context, command application.ChangeEnrollmentGateCommand) (domain.EnrollmentGate, error) {
	session, err := s.client.StartSession()
	if err != nil {
		return domain.EnrollmentGate{}, errEnrollmentStoreUnavailable
	}
	defer session.EndSession(ctx)
	transactionResult, err := session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		operations := s.database.Collection(gateOperationsName)
		var prior gateOperationRecord
		readErr := operations.FindOne(txCtx, bson.M{"_id": command.OperationID}).Decode(&prior)
		if readErr == nil {
			if prior.NodeID != command.NodeID || prior.Status != string(command.Status) {
				return nil, domain.ErrEnrollmentConflict
			}
			return gateToDomain(prior.Gate), nil
		}
		if !errors.Is(readErr, mongo.ErrNoDocuments) {
			return nil, readErr
		}
		gates := s.database.Collection(gatesCollectionName)
		var gate gateRecord
		if findErr := gates.FindOne(txCtx, bson.M{"_id": command.NodeID}).Decode(&gate); errors.Is(findErr, mongo.ErrNoDocuments) {
			gate = gateRecord{NodeID: command.NodeID, Status: string(domain.EnrollmentGateOpen), BoundAgentIDs: []string{}}
			if _, insertErr := gates.InsertOne(txCtx, gate); insertErr != nil {
				return nil, insertErr
			}
		} else if findErr != nil {
			return nil, findErr
		}
		if gate.PendingOperationID != "" {
			return nil, domain.ErrEnrollmentConflict
		}
		gate.Status = string(command.Status)
		gate.OperationID = command.OperationID
		gate.Version++
		if _, updateErr := gates.UpdateOne(txCtx, bson.M{"_id": command.NodeID}, bson.M{"$set": bson.M{"status": gate.Status, "operation_id": gate.OperationID, "version": gate.Version}}); updateErr != nil {
			return nil, updateErr
		}
		if _, insertErr := operations.InsertOne(txCtx, gateOperationRecord{
			OperationID: command.OperationID, NodeID: command.NodeID, Status: string(command.Status), Gate: gate,
		}); insertErr != nil {
			if mongo.IsDuplicateKeyError(insertErr) {
				return nil, domain.ErrEnrollmentConflict
			}
			return nil, insertErr
		}
		return gateToDomain(gate), nil
	})
	if err != nil {
		if errors.Is(err, domain.ErrEnrollmentConflict) {
			return domain.EnrollmentGate{}, err
		}
		return domain.EnrollmentGate{}, errEnrollmentStoreUnavailable
	}
	gate, ok := transactionResult.(domain.EnrollmentGate)
	if !ok {
		return domain.EnrollmentGate{}, errEnrollmentStoreUnavailable
	}
	return gate, nil
}

func (s *EnrollmentStore) touchOpenGate(ctx context.Context, nodeID, agentID string) error {
	gates := s.database.Collection(gatesCollectionName)
	var gate gateRecord
	err := gates.FindOne(ctx, bson.M{"_id": nodeID}).Decode(&gate)
	if errors.Is(err, mongo.ErrNoDocuments) {
		gate = gateRecord{NodeID: nodeID, Status: string(domain.EnrollmentGateOpen), BoundAgentIDs: []string{}}
		if agentID != "" {
			gate.BoundAgentIDs = []string{agentID}
		}
		if _, err := gates.InsertOne(ctx, gate); err != nil {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	if gate.Status != string(domain.EnrollmentGateOpen) {
		return domain.ErrInvitationRejected
	}
	if agentID != "" {
		for _, boundAgentID := range gate.BoundAgentIDs {
			if boundAgentID != agentID {
				return domain.ErrEnrollmentConflict
			}
		}
	}
	update := bson.M{"$inc": bson.M{"touch": 1}}
	if agentID != "" {
		update["$addToSet"] = bson.M{"bound_agent_ids": agentID}
	}
	result, err := gates.UpdateOne(ctx, bson.M{"_id": nodeID, "status": string(domain.EnrollmentGateOpen)}, update)
	if err != nil {
		return err
	}
	if result.MatchedCount != 1 {
		return domain.ErrInvitationRejected
	}
	return nil
}

func gateToDomain(record gateRecord) domain.EnrollmentGate {
	return domain.EnrollmentGate{
		NodeID: record.NodeID, Status: domain.EnrollmentGateStatus(record.Status),
		OperationID: record.OperationID, Version: record.Version, BoundAgentIDs: append([]string(nil), record.BoundAgentIDs...),
	}
}

func tokenDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// digestEnrollment provides a stable digest for schema-level secret lookup.
