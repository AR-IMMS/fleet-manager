package http

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/application"
	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
	"github.com/go-chi/chi/v5"
)

func TestCoreInvitationRequiresVerifiedClientCertificate(t *testing.T) {
	store := &httpEnrollmentStore{}
	service := enrollmentTestService(store)
	handler, err := NewEnrollmentHandler(&service, "fleet-core")
	if err != nil {
		t.Fatal("create handler", err)
	}
	router := chi.NewRouter()
	handler.Mount(router)
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/enrollment/invitations", strings.NewReader(`{"operation_id":"op-1","idempotency_key":"idem-1","node_id":"node-1"}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing mTLS client identity should be rejected, got %d", response.Code)
	}
	if store.createCalls != 0 {
		t.Fatal("unauthenticated Core request reached enrollment application")
	}
}

func TestAgentExchangeReturnsCredentialOnSuccessAndRedactsValidationError(t *testing.T) {
	store := &httpEnrollmentStore{}
	service := enrollmentTestService(store)
	handler, err := NewEnrollmentHandler(&service, "fleet-core")
	if err != nil {
		t.Fatal("create handler", err)
	}
	router := chi.NewRouter()
	handler.Mount(router)

	request := httptest.NewRequest(http.MethodPost, "/v1/enrollment/exchanges", strings.NewReader(`{"enrollment_attempt_id":"attempt-1","agent_id":"agent-1","invitation_token":"invitation-secret-012345678901234567890123","recovery_proof":"recovery-secret-01234567890123456789012345"}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("valid exchange status = %d, body %s", response.Code, response.Body.String())
	}
	var body exchangeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal("decode response", err)
	}
	if body.Credential != "generated-credential-for-test" || body.CredentialID != "credential-1" {
		t.Fatalf("exchange did not return generated credential: %+v", body)
	}

	secret := "invitation-secret-that-must-not-leak"
	request = httptest.NewRequest(http.MethodPost, "/v1/enrollment/exchanges", strings.NewReader(`{"invitation_token":"`+secret+`"}`))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid request status = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), secret) {
		t.Fatal("validation response leaked the submitted invitation secret")
	}
}

func TestCoreActorComesFromVerifiedClientCertificate(t *testing.T) {
	store := &httpEnrollmentStore{}
	service := enrollmentTestService(store)
	handler, err := NewEnrollmentHandler(&service, "fleet-core")
	if err != nil {
		t.Fatal("create handler", err)
	}
	router := chi.NewRouter()
	handler.Mount(router)
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/enrollment/invitations", strings.NewReader(`{"operation_id":"op-2","idempotency_key":"idem-2","node_id":"node-2"}`))
	certificate := &x509.Certificate{Subject: pkix.Name{CommonName: "fleet-core"}}
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}, VerifiedChains: [][]*x509.Certificate{{certificate}}}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || store.lastActor != "fleet-core" {
		t.Fatalf("verified certificate should supply Core actor: status=%d actor=%q", response.Code, store.lastActor)
	}
}

func enrollmentTestService(store application.EnrollmentStore) application.EnrollmentService {
	return application.EnrollmentService{
		Store: store, DefaultInviteTTL: time.Minute, MaximumInviteTTL: time.Hour,
		RecoveryTTL: time.Hour, OpAMPEndpoint: "wss://fleet.example.test/v1/opamp",
	}
}

type httpEnrollmentStore struct {
	createCalls int
	lastActor   string
}

func (s *httpEnrollmentStore) CreateInvitation(_ context.Context, invitation domain.EnrollmentInvitation) (domain.EnrollmentInvitation, error) {
	s.createCalls++
	s.lastActor = invitation.ActorID
	return invitation, nil
}

func (*httpEnrollmentStore) Enroll(_ context.Context, attempt domain.EnrollmentAttempt) (domain.EnrollmentResult, error) {
	attempt.Result.NodeID = "node-1"
	attempt.Result.CredentialID = "credential-1"
	attempt.Result.Credential = "generated-credential-for-test"
	return attempt.Result, nil
}

func (*httpEnrollmentStore) Recover(context.Context, string, string, string) (domain.EnrollmentResult, error) {
	return domain.EnrollmentResult{AgentID: "agent-1", CredentialID: "credential-1", Credential: "generated-credential-for-test", OpAMPEndpoint: "wss://fleet.example.test/v1/opamp"}, nil
}

func (*httpEnrollmentStore) ChangeGate(_ context.Context, command application.ChangeEnrollmentGateCommand) (domain.EnrollmentGate, error) {
	return domain.EnrollmentGate{NodeID: command.NodeID, Status: command.Status, OperationID: command.OperationID, Version: 1}, nil
}

func (*httpEnrollmentStore) GetStatus(_ context.Context, operationID string) (domain.EnrollmentStatus, error) {
	return domain.EnrollmentStatus{OperationID: operationID, NodeID: "node-1", Status: "ENROLLED", AgentID: "agent-1", CredentialID: "credential-1"}, nil
}
