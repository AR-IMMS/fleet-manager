//go:build integration

package mongodb

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/application"
	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	driver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestEnrollmentGateCompensationIsVersionFenced(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	if err := EnsureIndexes(ctx, database); err != nil {
		t.Fatal(err)
	}
	store, err := NewEnrollmentStore(database, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.VerifyTransactionSupport(ctx); err != nil {
		t.Fatal(err)
	}

	if err := store.verifier.storeCredentialForNode(ctx, "agent-compensate", "node-compensate", "credential-compensate", "token-compensate-012345678901234567890123456789"); err != nil {
		t.Fatal(err)
	}
	closed, err := store.CloseGate(ctx, "node-compensate", "lifecycle-op")
	if err != nil {
		t.Fatal(err)
	}
	if closed.PreviousState != domain.EnrollmentGateOpen || closed.Version != 1 || closed.ActiveInstallationID != "agent-compensate" || closed.ActiveBindingID != "credential-compensate" {
		t.Fatalf("close transition = %+v", closed)
	}
	replay, err := store.CloseGate(ctx, "node-compensate", "lifecycle-op")
	if err != nil || replay.Version != closed.Version {
		t.Fatalf("close replay=%+v err=%v", replay, err)
	}
	if _, err := store.ChangeGate(ctx, application.ChangeEnrollmentGateCommand{OperationID: "legacy-op", NodeID: "node-compensate", Status: domain.EnrollmentGateOpen}); !errors.Is(err, domain.ErrEnrollmentConflict) {
		t.Fatalf("legacy gate mutation during pending transition error=%v", err)
	}
	if err := store.CompleteGate(ctx, "node-compensate", "other-op", closed.Version, domain.EnrollmentGateOpen); !errors.Is(err, domain.ErrEnrollmentConflict) {
		t.Fatalf("wrong operation completion error=%v", err)
	}
	if err := store.CompleteGate(ctx, "node-compensate", "lifecycle-op", closed.Version, domain.EnrollmentGateOpen); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteGate(ctx, "node-compensate", "lifecycle-op", closed.Version, domain.EnrollmentGateOpen); err != nil {
		t.Fatalf("completion replay: %v", err)
	}

	var gate gateRecord
	if err := database.Collection(gatesCollectionName).FindOne(ctx, bson.M{"_id": "node-compensate"}).Decode(&gate); err != nil {
		t.Fatal(err)
	}
	if gate.Status != string(domain.EnrollmentGateOpen) || gate.PendingOperationID != "" {
		t.Fatalf("compensated gate=%+v", gate)
	}
}

func TestEnrollmentCancellationAndAgentCredentialRevocation(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	if err := EnsureIndexes(ctx, database); err != nil {
		t.Fatal(err)
	}
	store, err := NewEnrollmentStore(database, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.VerifyTransactionSupport(ctx); err != nil {
		t.Fatal(err)
	}
	service := application.EnrollmentService{Store: store, Revoker: store.verifier, DefaultInviteTTL: time.Minute, MaximumInviteTTL: time.Hour, RecoveryTTL: time.Hour, OpAMPEndpoint: "wss://fleet.example.test/v1/opamp"}

	invitation, err := service.CreateInvitation(ctx, application.CreateInvitationCommand{OperationID: "cancel-op", CorePrincipalID: "core", IdempotencyKey: "cancel-key", ActorID: "operator", NodeID: "node-cancel"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CancelInvitation(ctx, invitation.InvitationID, "cancel-op"); err != nil {
		t.Fatal(err)
	}
	if err := service.CancelInvitation(ctx, invitation.InvitationID, "cancel-op"); err != nil {
		t.Fatalf("cancel replay: %v", err)
	}
	status, err := service.GetStatus(ctx, "cancel-op")
	if err != nil || status.Status != string(domain.InvitationCancelled) {
		t.Fatalf("cancelled status=%+v err=%v", status, err)
	}
	if _, err := service.Exchange(ctx, application.ExchangeInvitationCommand{AttemptID: "cancel-attempt", AgentID: "agent-cancel", InvitationToken: invitation.Secret, RecoveryProof: "recovery-cancel-012345678901234567890123456789"}); !errors.Is(err, domain.ErrInvitationRejected) {
		t.Fatalf("cancelled exchange error=%v", err)
	}

	if err := store.verifier.StoreActiveCredential(ctx, "agent-revoke", "credential-revoke", "token-revoke-012345678901234567890123456789"); err != nil {
		t.Fatal(err)
	}
	otlpToken := "otlp-revoke-012345678901234567890123456789"
	if err := store.verifier.storeScopedCredentialForNode(ctx, "agent-revoke", "", "otlp-credential-revoke", otlpToken, "otlp.write"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.verifier.VerifyOTLP(ctx, otlpToken); err != nil {
		t.Fatalf("OTLP token before revocation: %v", err)
	}
	if err := service.RevokeAgent(ctx, "agent-revoke"); err != nil {
		t.Fatal(err)
	}
	if err := service.RevokeAgent(ctx, "agent-revoke"); err != nil {
		t.Fatalf("revoke replay: %v", err)
	}
	if _, err := store.verifier.Verify(ctx, "token-revoke-012345678901234567890123456789"); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("revoked token verify error=%v", err)
	}
	if _, err := store.verifier.VerifyOTLP(ctx, otlpToken); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("revoked OTLP token verify error=%v", err)
	}
}

func TestEnrollmentExchangeRecoveryAndGateSerialization(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal("apply enrollment schema migration", err)
	}
	if err := EnsureIndexes(ctx, database); err != nil {
		t.Fatal("reconcile enrollment indexes", err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	store, err := NewEnrollmentStore(database, key)
	if err != nil {
		t.Fatal("create enrollment store", err)
	}
	if err := store.VerifyTransactionSupport(ctx); err != nil {
		t.Fatal("verify transaction support", err)
	}
	service := application.EnrollmentService{
		Store: store, DefaultInviteTTL: 10 * time.Minute, MaximumInviteTTL: time.Hour,
		RecoveryTTL: 24 * time.Hour, OpAMPEndpoint: "wss://fleet.example.test/v1/opamp",
	}
	invitation, err := service.CreateInvitation(ctx, application.CreateInvitationCommand{
		OperationID: "operation-enrollment-1", CorePrincipalID: "core-service", IdempotencyKey: "idempotency-enrollment-1",
		ActorID: "core-service", NodeID: "node-enrollment-1",
	})
	if err != nil {
		t.Fatal("create enrollment invitation", err)
	}
	command := application.ExchangeInvitationCommand{
		AttemptID: "attempt-enrollment-1", AgentID: "agent-enrollment-1",
		InvitationToken: invitation.Secret, RecoveryProof: "recovery-proof-enrollment-1-012345678901234567890",
	}
	first, err := service.Exchange(ctx, command)
	if err != nil {
		t.Fatal("exchange invitation", err)
	}
	command.RecoveryProof = "recovery-proof-enrollment-1-012345678901234567890"
	replayed, err := service.Exchange(ctx, command)
	if err != nil {
		t.Fatal("replay exchange", err)
	}
	if first.CredentialID != replayed.CredentialID || first.Credential != replayed.Credential || first.NodeID != replayed.NodeID {
		t.Fatal("same attempt must recover its exact original response")
	}
	if !sameOTLPCredential(first.OTLPCredential, replayed.OTLPCredential) {
		t.Fatal("same attempt must replay its separate OTLP credential")
	}
	if _, err := service.Exchange(ctx, application.ExchangeInvitationCommand{
		AttemptID: "attempt-enrollment-replay", AgentID: command.AgentID, InvitationToken: invitation.Secret,
		RecoveryProof: "replay-proof-enrollment-1-012345678901234567890123",
	}); !errors.Is(err, domain.ErrInvitationRejected) {
		t.Fatalf("a new attempt must not replay a consumed invitation, got %v", err)
	}
	if _, err := store.verifier.VerifyOTLP(ctx, first.Credential); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("OpAMP token accepted for OTLP: %v", err)
	}
	identity, err := store.verifier.VerifyOTLP(ctx, first.OTLPCredential.Token)
	if err != nil || identity.AgentID != command.AgentID || identity.Scope != "otlp.write" || identity.ExpiresAt == nil || !identity.ExpiresAt.After(time.Now()) {
		t.Fatalf("OTLP credential verification identity=%+v err=%v", identity, err)
	}
	if _, err := database.Collection(collectionName).UpdateOne(ctx, bson.M{"credential_id": first.OTLPCredential.ID}, bson.M{"$set": bson.M{"expires_at": time.Now().Add(-time.Second)}}); err != nil {
		t.Fatal("expire OTLP credential fixture", err)
	}
	if _, err := store.verifier.VerifyOTLP(ctx, first.OTLPCredential.Token); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("expired OTLP credential error=%v", err)
	}
	if _, err := store.verifier.Verify(ctx, first.OTLPCredential.Token); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("OTLP token accepted for OpAMP: %v", err)
	}

	// A newly constructed adapter simulates process restart and must recover
	// the same result from encrypted durable state.
	restartedStore, err := NewEnrollmentStore(database, key)
	if err != nil {
		t.Fatal("recreate enrollment store", err)
	}
	restarted := service
	restarted.Store = restartedStore
	recovered, err := restarted.Recover(ctx, command.AttemptID, command.AgentID, command.RecoveryProof)
	if err != nil {
		t.Fatal("recover exchange after restart", err)
	}
	if recovered.Credential != first.Credential || recovered.CredentialID != first.CredentialID {
		t.Fatal("recovery after restart must return the original credential")
	}
	if !sameOTLPCredential(recovered.OTLPCredential, first.OTLPCredential) {
		t.Fatal("recovery after restart must return the original OTLP credential")
	}
	status, err := service.GetStatus(ctx, invitation.OperationID)
	if err != nil {
		t.Fatal("read secret-free enrollment status", err)
	}
	if status.Status != "EXCHANGED" || status.AgentID != command.AgentID || status.CredentialID != first.CredentialID || status.InvitationID != invitation.InvitationID {
		t.Fatalf("Core status does not reflect enrolled identity: %+v", status)
	}
	if status.CredentialID == first.Credential {
		t.Fatal("Core enrollment status must never expose the credential secret")
	}
	restartedStore.clock = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if _, err := restarted.Recover(ctx, command.AttemptID, command.AgentID, command.RecoveryProof); !errors.Is(err, domain.ErrEnrollmentRecoveryRejected) {
		t.Fatalf("recovery must fail after escrow expiry, got %v", err)
	}
	status, err = restarted.GetStatus(ctx, invitation.OperationID)
	if err != nil || status.Status != "EXCHANGED" {
		t.Fatalf("secret-free enrollment status should survive escrow expiry: status=%+v err=%v", status, err)
	}
	var persistedInvitation bson.M
	if err := database.Collection(invitationsCollectionName).FindOne(ctx, bson.M{"operation_id": invitation.OperationID}).Decode(&persistedInvitation); err != nil {
		t.Fatal("read invitation record", err)
	}
	if strings.Contains(string(mustBSON(t, persistedInvitation)), invitation.Secret) {
		t.Fatal("raw invitation secret must not appear in its Mongo record")
	}
	if escrowCount, err := database.Collection(invitationEscrowName).CountDocuments(ctx, bson.M{"_id": invitation.InvitationID}); err != nil || escrowCount != 0 {
		t.Fatalf("redeemed invitation secret escrow should be deleted: count=%d err=%v", escrowCount, err)
	}
	var persistedAttempt bson.M
	if err := database.Collection(attemptsCollectionName).FindOne(ctx, bson.M{"attempt_id": command.AttemptID}).Decode(&persistedAttempt); err != nil {
		t.Fatal("read attempt record", err)
	}
	if strings.Contains(string(mustBSON(t, persistedAttempt)), first.Credential) {
		t.Fatal("raw Fleet credential must not appear in its Mongo record")
	}
	if strings.Contains(string(mustBSON(t, persistedAttempt)), first.OTLPCredential.Token) {
		t.Fatal("raw OTLP credential must not appear in its Mongo record")
	}
	secondInvitation, err := service.CreateInvitation(ctx, application.CreateInvitationCommand{
		OperationID: "operation-second-agent", CorePrincipalID: "core-service", IdempotencyKey: "idempotency-second-agent",
		ActorID: "core-service", NodeID: "node-enrollment-1",
	})
	if err != nil {
		t.Fatal("create second invitation for Node policy test", err)
	}
	if _, err := service.Exchange(ctx, application.ExchangeInvitationCommand{
		AttemptID: "attempt-second-agent", AgentID: "agent-second", InvitationToken: secondInvitation.Secret,
		RecoveryProof: "recovery-proof-second-agent-012345678901234567890123456789",
	}); !errors.Is(err, domain.ErrEnrollmentConflict) {
		t.Fatalf("Node already bound to another Agent should conflict, got %v", err)
	}

	gate, err := service.ChangeGate(ctx, application.ChangeEnrollmentGateCommand{
		OperationID: "gate-close-enrollment-1", NodeID: "node-enrollment-1", Status: domain.EnrollmentGateClosed,
	})
	if err != nil {
		t.Fatal("close enrollment gate", err)
	}
	if gate.Status != domain.EnrollmentGateClosed || len(gate.BoundAgentIDs) != 1 || gate.BoundAgentIDs[0] != command.AgentID {
		t.Fatalf("gate response lost enrolled binding: %+v", gate)
	}
	gateReplay, err := service.ChangeGate(ctx, application.ChangeEnrollmentGateCommand{
		OperationID: "gate-close-enrollment-1", NodeID: "node-enrollment-1", Status: domain.EnrollmentGateClosed,
	})
	if err != nil || gateReplay.Version != gate.Version {
		t.Fatalf("gate operation retry should return its original version: replay=%+v err=%v", gateReplay, err)
	}
	if _, err := service.ChangeGate(ctx, application.ChangeEnrollmentGateCommand{
		OperationID: "gate-close-enrollment-1", NodeID: "node-enrollment-1", Status: domain.EnrollmentGateOpen,
	}); !errors.Is(err, domain.ErrEnrollmentConflict) {
		t.Fatalf("changed request for same gate operation should conflict, got %v", err)
	}
	if _, err := service.CreateInvitation(ctx, application.CreateInvitationCommand{
		OperationID: "operation-after-close", CorePrincipalID: "core-service", IdempotencyKey: "idempotency-after-close",
		ActorID: "core-service", NodeID: "node-enrollment-1",
	}); !errors.Is(err, domain.ErrInvitationRejected) {
		t.Fatalf("closed gate should reject new invitations, got %v", err)
	}
}

func sameOTLPCredential(left, right *domain.OTLPCredential) bool {
	if left == nil || right == nil || left.ID != right.ID || left.Token != right.Token || left.Scope != right.Scope {
		return false
	}
	if left.ExpiresAt == nil || right.ExpiresAt == nil {
		return left.ExpiresAt == nil && right.ExpiresAt == nil
	}
	return left.ExpiresAt.UnixMilli() == right.ExpiresAt.UnixMilli()
}

func TestExpiredInvitationExchangeReturnsExpiredError(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal("apply enrollment schema migration", err)
	}
	if err := EnsureIndexes(ctx, database); err != nil {
		t.Fatal("reconcile enrollment indexes", err)
	}
	store, err := NewEnrollmentStore(database, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal("create enrollment store", err)
	}
	service := application.EnrollmentService{
		Store: store, DefaultInviteTTL: time.Minute, MaximumInviteTTL: time.Hour,
		RecoveryTTL: 24 * time.Hour, OpAMPEndpoint: "wss://fleet.example.test/v1/opamp",
	}
	invitation, err := service.CreateInvitation(ctx, application.CreateInvitationCommand{
		OperationID: "operation-expired-invitation", CorePrincipalID: "core-service",
		IdempotencyKey: "idempotency-expired-invitation", ActorID: "core-service", NodeID: "node-expired-invitation",
	})
	if err != nil {
		t.Fatal("create invitation", err)
	}
	store.clock = func() time.Time { return invitation.ExpiresAt.Add(time.Second) }
	_, err = service.Exchange(ctx, application.ExchangeInvitationCommand{
		AttemptID: "attempt-expired-invitation", AgentID: "agent-expired-invitation",
		InvitationToken: invitation.Secret,
		RecoveryProof:   "recovery-proof-expired-invitation-012345678901234567890123",
	})
	if !errors.Is(err, domain.ErrInvitationExpired) {
		t.Fatalf("expired invitation should return the expired error, got %v", err)
	}
}

func TestInvitationExpiringBeforeConditionalConsumeDoesNotEnroll(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal("apply enrollment schema migration", err)
	}
	if err := EnsureIndexes(ctx, database); err != nil {
		t.Fatal("reconcile enrollment indexes", err)
	}
	store, err := NewEnrollmentStore(database, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal("create enrollment store", err)
	}
	service := application.EnrollmentService{
		Store: store, DefaultInviteTTL: time.Minute, MaximumInviteTTL: time.Hour,
		RecoveryTTL: 24 * time.Hour, OpAMPEndpoint: "wss://fleet.example.test/v1/opamp",
	}
	invitation, err := service.CreateInvitation(ctx, application.CreateInvitationCommand{
		OperationID: "operation-expiry-race", CorePrincipalID: "core-service",
		IdempotencyKey: "idempotency-expiry-race", ActorID: "core-service", NodeID: "node-expiry-race",
	})
	if err != nil {
		t.Fatal("create invitation", err)
	}
	clockCalls := 0
	store.clock = func() time.Time {
		clockCalls++
		if clockCalls >= 3 {
			return invitation.ExpiresAt.Add(time.Second)
		}
		return invitation.ExpiresAt.Add(-time.Second)
	}
	_, err = service.Exchange(ctx, application.ExchangeInvitationCommand{
		AttemptID: "attempt-expiry-race", AgentID: "agent-expiry-race",
		InvitationToken: invitation.Secret,
		RecoveryProof:   "recovery-proof-expiry-race-012345678901234567890123",
	})
	if !errors.Is(err, domain.ErrInvitationExpired) {
		t.Fatalf("invitation expiring before conditional consume should be rejected as expired, got %v", err)
	}
	if clockCalls < 3 {
		t.Fatalf("test did not advance the clock before conditional consume: calls=%d", clockCalls)
	}
	if count, err := database.Collection(collectionName).CountDocuments(ctx, bson.M{"agent_id": "agent-expiry-race"}); err != nil || count != 0 {
		t.Fatalf("expired invitation must not create an identity credential: count=%d err=%v", count, err)
	}
	if count, err := database.Collection(attemptsCollectionName).CountDocuments(ctx, bson.M{"attempt_id": "attempt-expiry-race"}); err != nil || count != 0 {
		t.Fatalf("expired invitation must not persist an exchange attempt: count=%d err=%v", count, err)
	}
	var persisted invitationRecord
	if err := database.Collection(invitationsCollectionName).FindOne(ctx, bson.M{"operation_id": invitation.OperationID}).Decode(&persisted); err != nil {
		t.Fatal("read invitation after rejected exchange", err)
	}
	if persisted.Status != string(domain.InvitationPending) {
		t.Fatalf("rejected exchange must leave invitation pending: status=%q", persisted.Status)
	}
	if count, err := database.Collection(invitationEscrowName).CountDocuments(ctx, bson.M{"_id": invitation.InvitationID}); err != nil || count != 1 {
		t.Fatalf("rejected exchange must retain invitation escrow: count=%d err=%v", count, err)
	}
}

func TestInstallationLookupsAreSecretFreeAndReflectRevocation(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal("apply identity schema migration", err)
	}
	if err := EnsureIndexes(ctx, database); err != nil {
		t.Fatal("reconcile identity indexes", err)
	}
	store, err := NewEnrollmentStore(database, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal("create enrollment store", err)
	}
	service := application.EnrollmentService{
		Store: store, DefaultInviteTTL: 10 * time.Minute, MaximumInviteTTL: time.Hour,
		RecoveryTTL: 24 * time.Hour, OpAMPEndpoint: "wss://fleet.example.test/v1/opamp",
	}
	invitation, err := service.CreateInvitation(ctx, application.CreateInvitationCommand{
		OperationID: "operation-installation-read", CorePrincipalID: "core-service", IdempotencyKey: "idempotency-installation-read",
		ActorID: "core-service", NodeID: "node-installation-read",
	})
	if err != nil {
		t.Fatal("create invitation", err)
	}
	result, err := service.Exchange(ctx, application.ExchangeInvitationCommand{
		AttemptID: "attempt-installation-read", AgentID: "agent-installation-read",
		InvitationToken: invitation.Secret, RecoveryProof: "recovery-proof-installation-read-012345678901234567890",
	})
	if err != nil {
		t.Fatal("exchange invitation", err)
	}
	byID, err := service.GetInstallationByAgentID(ctx, result.AgentID)
	if err != nil {
		t.Fatal("get installation by agent ID", err)
	}
	byNode, err := service.GetActiveInstallationByNode(ctx, result.NodeID)
	if err != nil {
		t.Fatal("get active installation by Node ID", err)
	}
	if byID.InstallationID != result.AgentID || byID.NodeID != result.NodeID || byID.Status != "ACTIVE" || byNode != byID {
		t.Fatalf("installation lookup returned inconsistent binding: byID=%+v byNode=%+v", byID, byNode)
	}
	encoded, err := bson.Marshal(byID)
	if err != nil {
		t.Fatal("marshal secret-free installation", err)
	}
	if strings.Contains(string(encoded), result.Credential) || strings.Contains(string(encoded), invitation.Secret) {
		t.Fatal("installation lookup value contains a credential or invitation secret")
	}
	if err := store.verifier.Revoke(ctx, result.CredentialID); err != nil {
		t.Fatal("revoke credential", err)
	}
	revoked, err := service.GetInstallationByAgentID(ctx, result.AgentID)
	if err != nil || revoked.Status != "REVOKED" {
		t.Fatalf("installation lookup should reflect revocation: installation=%+v err=%v", revoked, err)
	}
	if _, err := service.GetActiveInstallationByNode(ctx, result.NodeID); !errors.Is(err, domain.ErrEnrollmentNotFound) {
		t.Fatalf("active Node lookup should exclude revoked installation, got %v", err)
	}
}

func TestInvitationIdempotencyUsesCorePrincipalNotForwardedActor(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal("apply identity schema migration", err)
	}
	legacyIndex := "actor_id_1_idempotency_hash_1"
	if _, err := database.Collection(invitationsCollectionName).Indexes().CreateOne(ctx, driver.IndexModel{
		Keys:    bson.D{{Key: "actor_id", Value: 1}, {Key: "idempotency_hash", Value: 1}},
		Options: options.Index().SetName(legacyIndex).SetUnique(true),
	}); err != nil {
		t.Fatal("create legacy actor-scoped idempotency index", err)
	}
	if err := EnsureIndexes(ctx, database); err != nil {
		t.Fatal("reconcile identity indexes", err)
	}
	assertIndexNames(t, ctx, database.Collection(invitationsCollectionName), []string{
		"_id_", "core_principal_id_1_idempotency_hash_1", "invitation_id_1", "operation_id_1", "token_hash_1",
	})
	store, err := NewEnrollmentStore(database, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal("create enrollment store", err)
	}
	service := application.EnrollmentService{
		Store: store, DefaultInviteTTL: 10 * time.Minute, MaximumInviteTTL: time.Hour,
		RecoveryTTL: 24 * time.Hour, OpAMPEndpoint: "wss://fleet.example.test/v1/opamp",
	}
	create := func(principal, actor, operationID, nodeID string) (application.InvitationResult, error) {
		return service.CreateInvitation(ctx, application.CreateInvitationCommand{
			OperationID: operationID, CorePrincipalID: principal, IdempotencyKey: "shared-core-idempotency-key-12345",
			ActorID: actor, NodeID: nodeID,
		})
	}
	first, err := create("core-service-a", "human-a", "operation-core-a", "node-core-a")
	if err != nil {
		t.Fatal("create first invitation", err)
	}
	actorChanged, err := create("core-service-a", "human-b", "operation-core-a", "node-core-a")
	if err != nil || !actorChanged.Replayed || actorChanged.Secret != first.Secret {
		t.Fatalf("same Core principal/key with changed forwarded actor must replay original: replay=%+v err=%v", actorChanged, err)
	}
	otherPrincipal, err := create("core-service-b", "human-a", "operation-core-b", "node-core-b")
	if err != nil || otherPrincipal.Replayed || otherPrincipal.Secret == first.Secret {
		t.Fatalf("same key under another authenticated Core identity must be independent: result=%+v err=%v", otherPrincipal, err)
	}
	var firstRecord invitationRecord
	if err := database.Collection(invitationsCollectionName).FindOne(ctx, bson.M{"operation_id": "operation-core-a"}).Decode(&firstRecord); err != nil {
		t.Fatal("read first invitation record", err)
	}
	var otherPrincipalRecord invitationRecord
	if err := database.Collection(invitationsCollectionName).FindOne(ctx, bson.M{"operation_id": "operation-core-b"}).Decode(&otherPrincipalRecord); err != nil {
		t.Fatal("read other principal invitation record", err)
	}
	if firstRecord.ActorID != "human-a" {
		t.Fatalf("replay must not overwrite original audit actor: got %q", firstRecord.ActorID)
	}
	if firstRecord.CorePrincipalID == otherPrincipalRecord.CorePrincipalID || otherPrincipalRecord.ActorID != firstRecord.ActorID {
		t.Fatalf("same actor/key invitations should persist under separate Core principals: first=%+v other=%+v", firstRecord, otherPrincipalRecord)
	}
}

func TestConcurrentCreateInvitationReplaysCanonicalResult(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal("apply identity schema migration", err)
	}
	if err := EnsureIndexes(ctx, database); err != nil {
		t.Fatal("reconcile identity indexes", err)
	}
	store, err := NewEnrollmentStore(database, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal("create enrollment store", err)
	}
	service := application.EnrollmentService{
		Store: store, DefaultInviteTTL: 10 * time.Minute, MaximumInviteTTL: time.Hour,
		RecoveryTTL: 24 * time.Hour, OpAMPEndpoint: "wss://fleet.example.test/v1/opamp",
	}
	command := application.CreateInvitationCommand{
		OperationID: "operation-concurrent-invitation", CorePrincipalID: "core-service-concurrent",
		IdempotencyKey: "idempotency-concurrent-invitation", ActorID: "human-concurrent",
		NodeID: "node-concurrent-invitation",
	}
	const callers = 12
	var results [callers]application.InvitationResult
	var errorsByCaller [callers]error
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errorsByCaller[index] = service.CreateInvitation(ctx, command)
		}(index)
	}
	close(start)
	wait.Wait()

	canonicalID, canonicalSecret := results[0].InvitationID, results[0].Secret
	if canonicalID == "" || canonicalSecret == "" {
		t.Fatal("concurrent calls did not return a canonical invitation")
	}
	createdCount := 0
	for index, result := range results {
		if errorsByCaller[index] != nil {
			t.Fatalf("concurrent identical invitation %d failed: %v", index, errorsByCaller[index])
		}
		if result.InvitationID != canonicalID || result.Secret != canonicalSecret || result.OperationID != command.OperationID {
			t.Fatalf("concurrent invitation %d did not replay canonical result: got=%+v", index, result)
		}
		if !result.Replayed {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("non-replayed create count=%d, want exactly one", createdCount)
	}

	changedFingerprint := command
	changedFingerprint.NodeID = "different-node-concurrent"
	if _, err := service.CreateInvitation(ctx, changedFingerprint); !errors.Is(err, domain.ErrEnrollmentConflict) {
		t.Fatalf("changed fingerprint should conflict after concurrent create, got %v", err)
	}
}

func TestConcurrentExchangeReplaysOneCredential(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal("apply enrollment schema migration", err)
	}
	if err := EnsureIndexes(ctx, database); err != nil {
		t.Fatal("reconcile enrollment indexes", err)
	}
	store, err := NewEnrollmentStore(database, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal("create enrollment store", err)
	}
	service := application.EnrollmentService{
		Store: store, DefaultInviteTTL: 10 * time.Minute, MaximumInviteTTL: time.Hour,
		RecoveryTTL: 24 * time.Hour, OpAMPEndpoint: "wss://fleet.example.test/v1/opamp",
	}
	command := application.CreateInvitationCommand{
		OperationID: "operation-concurrent", CorePrincipalID: "core-service", IdempotencyKey: "idempotency-concurrent",
		ActorID: "core-service", NodeID: "node-concurrent",
	}
	var invitations [2]application.InvitationResult
	var inviteErrors [2]error
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range invitations {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			invitations[index], inviteErrors[index] = service.CreateInvitation(ctx, command)
		}(index)
	}
	close(start)
	wait.Wait()
	for _, err := range inviteErrors {
		if err != nil {
			t.Fatalf("concurrent idempotent invitation failed: %v", err)
		}
	}
	if invitations[0].InvitationID != invitations[1].InvitationID || invitations[0].Secret != invitations[1].Secret {
		t.Fatal("concurrent idempotent invitation requests must return one canonical result")
	}
	exchange := application.ExchangeInvitationCommand{
		AttemptID: "attempt-concurrent", AgentID: "agent-concurrent", InvitationToken: invitations[0].Secret,
		RecoveryProof: "recovery-proof-concurrent-012345678901234567890123456789",
	}
	var results [2]domain.EnrollmentResult
	var exchangeErrors [2]error
	start = make(chan struct{})
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], exchangeErrors[index] = service.Exchange(ctx, exchange)
		}(index)
	}
	close(start)
	wait.Wait()
	for _, err := range exchangeErrors {
		if err != nil {
			t.Fatalf("concurrent same-attempt exchange failed: %v", err)
		}
	}
	if results[0].CredentialID != results[1].CredentialID || results[0].Credential != results[1].Credential {
		t.Fatal("concurrent same-attempt exchanges must return one canonical credential")
	}
}

func TestEnrollmentGateCloseRacingExchangeReportsWinningBinding(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal("apply enrollment schema migration", err)
	}
	if err := EnsureIndexes(ctx, database); err != nil {
		t.Fatal("reconcile enrollment indexes", err)
	}
	store, err := NewEnrollmentStore(database, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal("create enrollment store", err)
	}
	service := application.EnrollmentService{
		Store: store, DefaultInviteTTL: 10 * time.Minute, MaximumInviteTTL: time.Hour,
		RecoveryTTL: 24 * time.Hour, OpAMPEndpoint: "wss://fleet.example.test/v1/opamp",
	}
	invitation, err := service.CreateInvitation(ctx, application.CreateInvitationCommand{
		OperationID: "operation-race", CorePrincipalID: "core-service", IdempotencyKey: "idempotency-race", ActorID: "core-service", NodeID: "node-race",
	})
	if err != nil {
		t.Fatal("create invitation before race", err)
	}
	start := make(chan struct{})
	var wait sync.WaitGroup
	var exchangeResult domain.EnrollmentResult
	var exchangeErr error
	var gate domain.EnrollmentGate
	var gateErr error
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		exchangeResult, exchangeErr = service.Exchange(ctx, application.ExchangeInvitationCommand{
			AttemptID: "attempt-race", AgentID: "agent-race", InvitationToken: invitation.Secret,
			RecoveryProof: "recovery-proof-race-012345678901234567890123456789",
		})
	}()
	go func() {
		defer wait.Done()
		<-start
		gate, gateErr = service.ChangeGate(ctx, application.ChangeEnrollmentGateCommand{
			OperationID: "gate-close-race", NodeID: "node-race", Status: domain.EnrollmentGateClosed,
		})
	}()
	close(start)
	wait.Wait()
	if gateErr != nil {
		t.Fatal("close racing gate", gateErr)
	}
	if exchangeErr == nil {
		for _, agentID := range gate.BoundAgentIDs {
			if agentID == exchangeResult.AgentID {
				return
			}
		}
		t.Fatalf("gate close missed exchange that committed first: gate=%+v", gate)
	}
	if !errors.Is(exchangeErr, domain.ErrInvitationRejected) {
		t.Fatalf("exchange lost race with closed gate for unexpected reason: %v", exchangeErr)
	}
}

func TestRevokedAgentCanReEnrollWithoutLosingCredentialHistory(t *testing.T) {
	ctx, database := newMigrationTestDatabase(t)
	if err := Migrate(ctx, database); err != nil {
		t.Fatal("apply enrollment schema migration", err)
	}
	if err := EnsureIndexes(ctx, database); err != nil {
		t.Fatal("reconcile enrollment indexes", err)
	}
	store, err := NewEnrollmentStore(database, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal("create enrollment store", err)
	}
	if err := store.VerifyTransactionSupport(ctx); err != nil {
		t.Fatal("verify transaction support", err)
	}
	service := application.EnrollmentService{
		Store: store, Revoker: store.verifier, DefaultInviteTTL: 10 * time.Minute,
		MaximumInviteTTL: time.Hour, RecoveryTTL: 24 * time.Hour,
		OpAMPEndpoint: "wss://fleet.example.test/v1/opamp",
	}
	const agentID = "550e8400-e29b-41d4-a716-446655440001"
	const nodeID = "node-reenroll"
	firstInvite, err := service.CreateInvitation(ctx, application.CreateInvitationCommand{
		OperationID: "operation-reenroll-1", CorePrincipalID: "core", IdempotencyKey: "key-reenroll-1",
		ActorID: "operator", NodeID: nodeID,
	})
	if err != nil {
		t.Fatal("create first invitation", err)
	}
	first, err := service.Exchange(ctx, application.ExchangeInvitationCommand{
		AttemptID: "attempt-reenroll-1", AgentID: agentID, InvitationToken: firstInvite.Secret,
		RecoveryProof: "recovery-proof-reenroll-1-012345678901234567890123",
	})
	if err != nil {
		t.Fatal("first enrollment", err)
	}
	principal, err := domain.NewPrincipal(agentID, first.CredentialID)
	if err != nil {
		t.Fatal("create first principal", err)
	}
	uid := []byte("0123456789abcdef")
	if err := store.verifier.BindAndAuthorize(ctx, principal, uid); err != nil {
		t.Fatal("bind first Supervisor UID", err)
	}
	if err := service.RevokeAgent(ctx, agentID); err != nil {
		t.Fatal("revoke first credentials", err)
	}
	if _, err := store.verifier.Verify(ctx, first.Credential); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("old OpAMP credential after revoke = %v", err)
	}
	if _, err := store.verifier.VerifyOTLP(ctx, first.OTLPCredential.Token); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("old OTLP credential after revoke = %v", err)
	}
	secondInvite, err := service.CreateInvitation(ctx, application.CreateInvitationCommand{
		OperationID: "operation-reenroll-2", CorePrincipalID: "core", IdempotencyKey: "key-reenroll-2",
		ActorID: "operator", NodeID: nodeID,
	})
	if err != nil {
		t.Fatal("create replacement invitation", err)
	}
	second, err := service.Exchange(ctx, application.ExchangeInvitationCommand{
		AttemptID: "attempt-reenroll-2", AgentID: agentID, InvitationToken: secondInvite.Secret,
		RecoveryProof: "recovery-proof-reenroll-2-012345678901234567890123",
	})
	if err != nil {
		t.Fatalf("same-Agent re-enrollment: %v", err)
	}
	if second.AgentID != first.AgentID || second.NodeID != first.NodeID || second.CredentialID == first.CredentialID {
		t.Fatalf("replacement enrollment identity/history = first:%+v second:%+v", first, second)
	}
	if _, err := store.verifier.Verify(ctx, first.Credential); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("old OpAMP credential after replacement = %v", err)
	}
	if _, err := store.verifier.VerifyOTLP(ctx, first.OTLPCredential.Token); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("old OTLP credential after replacement = %v", err)
	}
	if _, err := store.verifier.Verify(ctx, second.Credential); err != nil {
		t.Fatalf("replacement OpAMP credential: %v", err)
	}
	if _, err := store.verifier.VerifyOTLP(ctx, second.OTLPCredential.Token); err != nil {
		t.Fatalf("replacement OTLP credential: %v", err)
	}
	secondPrincipal, err := domain.NewPrincipal(agentID, second.CredentialID)
	if err != nil {
		t.Fatal("create replacement principal", err)
	}
	if err := store.verifier.BindAndAuthorize(ctx, secondPrincipal, uid); err != nil {
		t.Fatalf("bind retained Supervisor UID: %v", err)
	}
	if count, err := database.Collection(attemptsCollectionName).CountDocuments(ctx, bson.M{"agent_id": agentID}); err != nil || count != 2 {
		t.Fatalf("retained attempts count=%d err=%v", count, err)
	}
	var credentials []record
	cursor, err := database.Collection(collectionName).Find(ctx, bson.M{"agent_id": agentID})
	if err != nil {
		t.Fatal("query credential history", err)
	}
	defer func() { _ = cursor.Close(ctx) }()
	if err := cursor.All(ctx, &credentials); err != nil {
		t.Fatal("read credential history", err)
	}
	if len(credentials) != 4 {
		t.Fatalf("credential history count=%d want 4", len(credentials))
	}
	active, revoked := 0, 0
	for _, credential := range credentials {
		switch credential.Status {
		case "ACTIVE":
			active++
		case "REVOKED":
			revoked++
		default:
			t.Fatalf("unexpected credential status %q", credential.Status)
		}
	}
	if active != 2 || revoked != 2 {
		t.Fatalf("active/revoked credential history=%d/%d want 2/2", active, revoked)
	}
}

func mustBSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := bson.Marshal(value)
	if err != nil {
		t.Fatal("marshal BSON for secret scan", err)
	}
	return encoded
}
