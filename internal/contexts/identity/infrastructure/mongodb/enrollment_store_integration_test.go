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
)

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
		OperationID: "operation-enrollment-1", IdempotencyKey: "idempotency-enrollment-1",
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
	status, err := service.GetStatus(ctx, invitation.OperationID)
	if err != nil {
		t.Fatal("read secret-free enrollment status", err)
	}
	if status.Status != "ENROLLED" || status.AgentID != command.AgentID || status.CredentialID != first.CredentialID {
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
	if err != nil || status.Status != "ENROLLED" {
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
	secondInvitation, err := service.CreateInvitation(ctx, application.CreateInvitationCommand{
		OperationID: "operation-second-agent", IdempotencyKey: "idempotency-second-agent",
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
		OperationID: "operation-after-close", IdempotencyKey: "idempotency-after-close",
		ActorID: "core-service", NodeID: "node-enrollment-1",
	}); !errors.Is(err, domain.ErrInvitationRejected) {
		t.Fatalf("closed gate should reject new invitations, got %v", err)
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
		OperationID: "operation-concurrent", IdempotencyKey: "idempotency-concurrent",
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
		OperationID: "operation-race", IdempotencyKey: "idempotency-race", ActorID: "core-service", NodeID: "node-race",
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

func mustBSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := bson.Marshal(value)
	if err != nil {
		t.Fatal("marshal BSON for secret scan", err)
	}
	return encoded
}
