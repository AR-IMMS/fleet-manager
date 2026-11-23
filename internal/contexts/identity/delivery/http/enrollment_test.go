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
	"github.com/AR-IMMS/fleet-manager/internal/platform/httpapi"
	"github.com/go-chi/chi/v5"
)

func TestCoreInvitationRequiresVerifiedClientCertificate(t *testing.T) {
	store := &httpEnrollmentStore{}
	service := enrollmentTestService(store)
	handler, err := NewEnrollmentHandler(&service, "fleet-core")
	if err != nil {
		t.Fatal("create handler", err)
	}
	router := newEnrollmentRouter(handler)
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/enrollment/invitations", strings.NewReader(`{"schema_version":1,"operation_id":"op-1","node_id":"node-1","requested_ttl_seconds":1800}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing mTLS client identity should be rejected, got %d", response.Code)
	}
	if response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unauthorized response content type = %q", response.Header().Get("Content-Type"))
	}
	var problem httpapi.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal("decode unauthorized problem", err)
	}
	if problem.Status != response.Code || problem.Code != "INVALID_REQUEST" || problem.RequestID == "" ||
		problem.RequestID != response.Header().Get(httpapi.RequestIDHeader) {
		t.Fatalf("unauthorized problem does not match status/request ID: %+v header=%q", problem, response.Header().Get(httpapi.RequestIDHeader))
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
	router := newEnrollmentRouter(handler)

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
	request.Header.Set(httpapi.RequestIDHeader, "request-invalid-01")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid request status = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), secret) {
		t.Fatal("validation response leaked the submitted invitation secret")
	}
	if response.Header().Get("Content-Type") != "application/problem+json" || response.Header().Get(httpapi.RequestIDHeader) != "request-invalid-01" {
		t.Fatalf("problem response headers = content-type %q request-id %q", response.Header().Get("Content-Type"), response.Header().Get(httpapi.RequestIDHeader))
	}
	var problem httpapi.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal("decode validation problem", err)
	}
	if problem.Status != http.StatusBadRequest || problem.Code != "INVALID_REQUEST" || problem.RequestID != "request-invalid-01" {
		t.Fatalf("unexpected validation problem: %+v", problem)
	}
}

func TestExpiredInvitationExchangeReturnsGoneProblem(t *testing.T) {
	store := &httpEnrollmentStore{enrollErr: domain.ErrInvitationExpired}
	service := enrollmentTestService(store)
	handler, err := NewEnrollmentHandler(&service, "fleet-core")
	if err != nil {
		t.Fatal("create handler", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/enrollment/exchanges", strings.NewReader(`{"enrollment_attempt_id":"attempt-expired","agent_id":"agent-1","invitation_token":"invitation-secret-012345678901234567890123","recovery_proof":"recovery-proof-012345678901234567890123456789"}`))
	response := httptest.NewRecorder()
	newEnrollmentRouter(handler).ServeHTTP(response, request)
	if response.Code != http.StatusGone {
		t.Fatalf("expired invitation status = %d, body %s", response.Code, response.Body.String())
	}
	var problem httpapi.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal("decode expired invitation problem", err)
	}
	if problem.Status != http.StatusGone || problem.Code != "INVITATION_REJECTED" || problem.RequestID != response.Header().Get(httpapi.RequestIDHeader) {
		t.Fatalf("expired invitation problem is inconsistent: %+v", problem)
	}
}

func TestEnrollmentRejectsOversizedRequestBody(t *testing.T) {
	store := &httpEnrollmentStore{}
	service := enrollmentTestService(store)
	handler, err := NewEnrollmentHandler(&service, "fleet-core")
	if err != nil {
		t.Fatal("create handler", err)
	}
	secret := strings.Repeat("s", maxEnrollmentRequestBytes+1)
	body := `{"enrollment_attempt_id":"attempt-oversized","agent_id":"agent-1","invitation_token":"` + secret + `","recovery_proof":"recovery-proof-012345678901234567890123456789"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/enrollment/exchanges", strings.NewReader(body))
	response := httptest.NewRecorder()
	newEnrollmentRouter(handler).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized enrollment body status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("oversized enrollment content type = %q", response.Header().Get("Content-Type"))
	}
	if strings.Contains(response.Body.String(), secret) {
		t.Fatal("oversized request response leaked the submitted invitation token")
	}
}

func TestCoreInvitationUsesVerifiedClientCertificateAndAuditActorHeader(t *testing.T) {
	store := &httpEnrollmentStore{}
	service := enrollmentTestService(store)
	handler, err := NewEnrollmentHandler(&service, "fleet-core")
	if err != nil {
		t.Fatal("create handler", err)
	}
	router := newEnrollmentRouter(handler)
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/enrollment/invitations", strings.NewReader(`{"schema_version":1,"operation_id":"op-2","node_id":"node-2","requested_ttl_seconds":1800}`))
	request.Header.Set("X-ARIMMS-Operation-Id", "op-2")
	request.Header.Set("Idempotency-Key", "idempotency-key-0002")
	request.Header.Set("X-ARIMMS-Actor-Id", "human-actor-2")
	certificate := &x509.Certificate{Subject: pkix.Name{CommonName: "fleet-core"}}
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}, VerifiedChains: [][]*x509.Certificate{{certificate}}}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || store.lastActor != "human-actor-2" {
		t.Fatalf("verified Core request should preserve audit actor: status=%d actor=%q", response.Code, store.lastActor)
	}
}

func TestCreateInvitationRequiresMatchingOperationAndIdempotencyHeaders(t *testing.T) {
	store := &httpEnrollmentStore{}
	service := enrollmentTestService(store)
	handler, err := NewEnrollmentHandler(&service, "fleet-core")
	if err != nil {
		t.Fatal("create handler", err)
	}
	router := newEnrollmentRouter(handler)
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/enrollment/invitations", strings.NewReader(`{"schema_version":1,"operation_id":"op-header","node_id":"node-1","requested_ttl_seconds":1800}`))
	certificate := &x509.Certificate{Subject: pkix.Name{CommonName: "fleet-core"}}
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}, VerifiedChains: [][]*x509.Certificate{{certificate}}}
	request.Header.Set("X-ARIMMS-Operation-Id", "different-operation")
	request.Header.Set("Idempotency-Key", "idempotency-key-1234")
	request.Header.Set("X-ARIMMS-Actor-Id", "human-actor")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("mismatched operation ID should be rejected, got %d: %s", response.Code, response.Body.String())
	}
	if store.createCalls != 0 {
		t.Fatal("invalid operation metadata reached the enrollment application")
	}
}

func TestInvitationIdempotencyUsesCoreIdentityAndIgnoresAuditActorChanges(t *testing.T) {
	store := &httpEnrollmentStore{}
	service := enrollmentTestService(store)
	handler, err := NewEnrollmentHandler(&service, "fleet-core")
	if err != nil {
		t.Fatal("create handler", err)
	}
	router := newEnrollmentRouter(handler)
	post := func(coreCN, actor, operationID, nodeID string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/internal/v1/enrollment/invitations", strings.NewReader(`{"schema_version":1,"operation_id":"`+operationID+`","node_id":"`+nodeID+`","requested_ttl_seconds":1800}`))
		certificate := &x509.Certificate{Subject: pkix.Name{CommonName: coreCN}}
		request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}, VerifiedChains: [][]*x509.Certificate{{certificate}}}
		request.Header.Set("X-ARIMMS-Operation-Id", operationID)
		request.Header.Set("Idempotency-Key", "shared-idempotency-key-12345")
		request.Header.Set("X-ARIMMS-Actor-Id", actor)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	first := post("fleet-core", "human-a", "op-scope-1", "node-scope-1")
	if first.Code != http.StatusCreated {
		t.Fatalf("initial create status = %d: %s", first.Code, first.Body.String())
	}
	var firstBody createInvitationResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatal("decode initial invitation", err)
	}
	firstIdempotencyHash := store.lastIdempotencyHash

	actorChanged := post("fleet-core", "human-b", "op-scope-1", "node-scope-1")
	if actorChanged.Code != http.StatusOK {
		t.Fatalf("same Core principal/key with changed audit actor should replay, got %d: %s", actorChanged.Code, actorChanged.Body.String())
	}
	var replayBody createInvitationResponse
	if err := json.Unmarshal(actorChanged.Body.Bytes(), &replayBody); err != nil {
		t.Fatal("decode replayed invitation", err)
	}
	if replayBody.Invitation != firstBody.Invitation {
		t.Fatal("changed audit actor must not create a new invitation secret")
	}

	if stored, ok := store.invitations[firstIdempotencyHash]; !ok || stored.ActorID != "human-a" || stored.CorePrincipalID != "fleet-core" {
		t.Fatalf("verified Core identity and original audit actor should be persisted separately: found=%v core=%q actor=%q", ok, stored.CorePrincipalID, stored.ActorID)
	}
}

func TestEnrollmentResponsesMatchWireContractAndRedactSecrets(t *testing.T) {
	store := &httpEnrollmentStore{}
	service := enrollmentTestService(store)
	handler, err := NewEnrollmentHandler(&service, "fleet-core")
	if err != nil {
		t.Fatal("create handler", err)
	}
	router := newEnrollmentRouter(handler)

	request := httptest.NewRequest(http.MethodPost, "/internal/v1/enrollment/invitations", strings.NewReader(`{"schema_version":1,"operation_id":"op-contract","node_id":"node-1","requested_ttl_seconds":1800}`))
	certificate := &x509.Certificate{Subject: pkix.Name{CommonName: "fleet-core"}}
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}, VerifiedChains: [][]*x509.Certificate{{certificate}}}
	request.Header.Set("X-ARIMMS-Operation-Id", "op-contract")
	request.Header.Set("Idempotency-Key", "idempotency-key-contract")
	request.Header.Set("X-ARIMMS-Actor-Id", "human-actor")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get(httpapi.RequestIDHeader) == "" {
		t.Fatalf("invitation response status/cache/request-ID headers = %d/%q/%q", response.Code, response.Header().Get("Cache-Control"), response.Header().Get(httpapi.RequestIDHeader))
	}
	var invitationBody map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &invitationBody); err != nil {
		t.Fatal("decode invitation response", err)
	}
	if invitationBody["schema_version"] != float64(1) || invitationBody["invitation_token"] == nil {
		t.Fatalf("invitation response does not match proposal: %#v", invitationBody)
	}
	firstToken := invitationBody["invitation_token"]

	retry := httptest.NewRequest(http.MethodPost, "/internal/v1/enrollment/invitations", strings.NewReader(`{"schema_version":1,"operation_id":"op-contract","node_id":"node-1","requested_ttl_seconds":1800}`))
	retry.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}, VerifiedChains: [][]*x509.Certificate{{certificate}}}
	retry.Header.Set("X-ARIMMS-Operation-Id", "op-contract")
	retry.Header.Set("Idempotency-Key", "idempotency-key-contract")
	retry.Header.Set("X-ARIMMS-Actor-Id", "human-actor")
	retryResponse := httptest.NewRecorder()
	router.ServeHTTP(retryResponse, retry)
	if retryResponse.Code != http.StatusOK || retryResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("exact invitation replay status/cache headers = %d/%q", retryResponse.Code, retryResponse.Header().Get("Cache-Control"))
	}
	var replayBody map[string]any
	if err := json.Unmarshal(retryResponse.Body.Bytes(), &replayBody); err != nil {
		t.Fatal("decode replayed invitation", err)
	}
	if replayBody["invitation_token"] != firstToken {
		t.Fatal("exact pending invitation replay must return the original token")
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/enrollment/exchanges", strings.NewReader(`{"enrollment_attempt_id":"attempt-contract","agent_id":"agent-contract","invitation_token":"invitation-secret-012345678901234567890123","recovery_proof":"recovery-secret-01234567890123456789012345"}`))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get(httpapi.RequestIDHeader) == "" {
		t.Fatalf("exchange response status/cache/request-ID headers = %d/%q/%q", response.Code, response.Header().Get("Cache-Control"), response.Header().Get(httpapi.RequestIDHeader))
	}
	var exchangeBody map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &exchangeBody); err != nil {
		t.Fatal("decode exchange response", err)
	}
	if exchangeBody["credential_scope"] != "opamp.connect" || exchangeBody["credential_expires_at"] != nil {
		t.Fatalf("exchange response does not match proposal: %#v", exchangeBody)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/enrollment/recoveries", strings.NewReader(`{"enrollment_attempt_id":"attempt-contract","agent_id":"agent-1","recovery_proof":"recovery-proof-012345678901234567890123456789"}`))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get(httpapi.RequestIDHeader) == "" {
		t.Fatalf("recovery response status/cache/request-ID headers = %d/%q/%q", response.Code, response.Header().Get("Cache-Control"), response.Header().Get(httpapi.RequestIDHeader))
	}
	var recoveryBody map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &recoveryBody); err != nil {
		t.Fatal("decode recovery result", err)
	}
	if recoveryBody["credential_scope"] != "opamp.connect" || recoveryBody["credential_expires_at"] != nil {
		t.Fatalf("recovery result does not match exchange result contract: %#v", recoveryBody)
	}
}

func TestCoreInstallationLookupsRequireVerifiedCertificateAndNeverReturnSecrets(t *testing.T) {
	store := &httpEnrollmentStore{}
	service := enrollmentTestService(store)
	handler, err := NewEnrollmentHandler(&service, "fleet-core")
	if err != nil {
		t.Fatal("create handler", err)
	}
	router := newEnrollmentRouter(handler)

	for _, path := range []string{
		"/internal/v1/installations/agent-1",
		"/internal/v1/nodes/node-1/active-installation",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated lookup %s status = %d", path, response.Code)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/internal/v1/installations/agent-1", nil)
	certificate := &x509.Certificate{Subject: pkix.Name{CommonName: "fleet-core"}}
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}, VerifiedChains: [][]*x509.Certificate{{certificate}}}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated installation lookup status = %d: %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal("decode installation response", err)
	}
	if body["installation_id"] != "agent-1" || body["node_id"] != "node-1" || body["status"] != "ACTIVE" {
		t.Fatalf("unexpected installation response: %#v", body)
	}
	for _, secret := range []string{"credential", "credential_hash", "invitation_token"} {
		if _, exists := body[secret]; exists {
			t.Fatalf("installation response leaked %q: %#v", secret, body)
		}
	}

	request = httptest.NewRequest(http.MethodGet, "/internal/v1/nodes/node-1/active-installation", nil)
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}, VerifiedChains: [][]*x509.Certificate{{certificate}}}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"installation_id":"agent-1"`) {
		t.Fatalf("active installation by Node response = %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/internal/v1/enrollment/operations/op-1", nil)
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}, VerifiedChains: [][]*x509.Certificate{{certificate}}}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"schema_version":1`) ||
		!strings.Contains(response.Body.String(), `"invitation_id":"inv-1"`) || !strings.Contains(response.Body.String(), `"status":"EXCHANGED"`) {
		t.Fatalf("operation status does not match proposal: %d %s", response.Code, response.Body.String())
	}
}

func newEnrollmentRouter(handler *EnrollmentHandler) chi.Router {
	router := chi.NewRouter()
	router.Use(httpapi.RequestIDMiddleware(nil))
	handler.Mount(router)
	return router
}

func enrollmentTestService(store application.EnrollmentStore) application.EnrollmentService {
	return application.EnrollmentService{
		Store: store, DefaultInviteTTL: time.Minute, MaximumInviteTTL: time.Hour,
		RecoveryTTL: time.Hour, OpAMPEndpoint: "wss://fleet.example.test/v1/opamp",
	}
}

type httpEnrollmentStore struct {
	createCalls         int
	lastActor           string
	lastIdempotencyHash string
	invitations         map[string]domain.EnrollmentInvitation
	enrollErr           error
}

func (s *httpEnrollmentStore) CreateInvitation(_ context.Context, invitation domain.EnrollmentInvitation) (domain.EnrollmentInvitation, error) {
	s.createCalls++
	s.lastActor = invitation.ActorID
	s.lastIdempotencyHash = invitation.IdempotencyHash
	if s.invitations == nil {
		s.invitations = make(map[string]domain.EnrollmentInvitation)
	}
	if current, ok := s.invitations[invitation.IdempotencyHash]; ok {
		if current.OperationID != invitation.OperationID || current.Fingerprint != invitation.Fingerprint {
			return domain.EnrollmentInvitation{}, domain.ErrEnrollmentConflict
		}
		current.Replayed = true
		return current, nil
	}
	s.invitations[invitation.IdempotencyHash] = invitation
	return invitation, nil
}

func (s *httpEnrollmentStore) Enroll(_ context.Context, attempt domain.EnrollmentAttempt) (domain.EnrollmentResult, error) {
	if s.enrollErr != nil {
		return domain.EnrollmentResult{}, s.enrollErr
	}
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
	return domain.EnrollmentStatus{OperationID: operationID, InvitationID: "inv-1", NodeID: "node-1", Status: "EXCHANGED", AgentID: "agent-1", CredentialID: "credential-1"}, nil
}

func (*httpEnrollmentStore) GetInstallationByAgentID(_ context.Context, agentID string) (domain.EnrollmentInstallation, error) {
	return domain.EnrollmentInstallation{InstallationID: agentID, NodeID: "node-1", Status: "ACTIVE"}, nil
}

func (*httpEnrollmentStore) GetActiveInstallationByNode(_ context.Context, nodeID string) (domain.EnrollmentInstallation, error) {
	return domain.EnrollmentInstallation{InstallationID: "agent-1", NodeID: nodeID, Status: "ACTIVE"}, nil
}
