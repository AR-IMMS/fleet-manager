package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/application"
	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
	"github.com/AR-IMMS/fleet-manager/internal/platform/httpapi"
	"github.com/go-chi/chi/v5"
)

const maxEnrollmentRequestBytes = 32 << 10

// EnrollmentHandler adapts provisional HTTP requests to identity use cases.
// Routes are mounted only when enrollment is explicitly enabled.
type EnrollmentHandler struct {
	service *application.EnrollmentService
	coreCN  string
}

// NewEnrollmentHandler constructs the HTTP adapter with a trusted Core mTLS identity.
func NewEnrollmentHandler(service *application.EnrollmentService, coreClientCN string) (*EnrollmentHandler, error) {
	if service == nil || strings.TrimSpace(coreClientCN) == "" || strings.TrimSpace(coreClientCN) != coreClientCN {
		return nil, errors.New("enrollment handler dependencies are invalid")
	}
	return &EnrollmentHandler{service: service, coreCN: coreClientCN}, nil
}

// Mount registers provisional invitation and Agent exchange routes.
func (h *EnrollmentHandler) Mount(router chi.Router) {
	router.Post("/internal/v1/enrollment/invitations", h.createInvitation)
	router.Post("/internal/v1/enrollment/nodes/{node_id}/gate", h.changeGate)
	router.Get("/internal/v1/enrollment/operations/{operation_id}", h.getStatus)
	router.Get("/internal/v1/installations/{agent_id}", h.getInstallation)
	router.Get("/internal/v1/nodes/{node_id}/active-installation", h.getActiveInstallation)
	router.Post("/v1/enrollment/exchanges", h.exchange)
	router.Post("/v1/enrollment/recoveries", h.recover)
}

type createInvitationRequest struct {
	SchemaVersion int    `json:"schema_version"`
	OperationID   string `json:"operation_id"`
	NodeID        string `json:"node_id"`
	TTLSeconds    *int64 `json:"requested_ttl_seconds,omitempty"`
}

type createInvitationResponse struct {
	SchemaVersion int       `json:"schema_version"`
	OperationID   string    `json:"operation_id"`
	InvitationID  string    `json:"invitation_id"`
	Invitation    string    `json:"invitation_token"`
	NodeID        string    `json:"node_id"`
	Status        string    `json:"status"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type exchangeRequest struct {
	AttemptID       string `json:"enrollment_attempt_id"`
	AgentID         string `json:"agent_id"`
	InvitationToken string `json:"invitation_token"`
	RecoveryProof   string `json:"recovery_proof"`
}

type exchangeResponse struct {
	AgentID             string     `json:"agent_id"`
	NodeID              string     `json:"node_id"`
	CredentialID        string     `json:"credential_id"`
	Credential          string     `json:"credential"`
	CredentialScope     string     `json:"credential_scope"`
	OpAMPEndpoint       string     `json:"opamp_endpoint"`
	CredentialExpiresAt *time.Time `json:"credential_expires_at"`
}

type recoveryRequest struct {
	AttemptID     string `json:"enrollment_attempt_id"`
	AgentID       string `json:"agent_id"`
	RecoveryProof string `json:"recovery_proof"`
}

type changeGateRequest struct {
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
}

type changeGateResponse struct {
	NodeID        string   `json:"node_id"`
	Status        string   `json:"status"`
	OperationID   string   `json:"operation_id"`
	Version       int64    `json:"version"`
	BoundAgentIDs []string `json:"bound_agent_ids"`
}

type enrollmentStatusResponse struct {
	SchemaVersion int    `json:"schema_version"`
	OperationID   string `json:"operation_id"`
	InvitationID  string `json:"invitation_id"`
	NodeID        string `json:"node_id"`
	Status        string `json:"status"`
	AgentID       string `json:"agent_id,omitempty"`
	CredentialID  string `json:"credential_id,omitempty"`
}

type installationResponse struct {
	InstallationID string `json:"installation_id"`
	NodeID         string `json:"node_id"`
	Status         string `json:"status"`
}

func (h *EnrollmentHandler) createInvitation(w http.ResponseWriter, r *http.Request) {
	corePrincipalID, ok := h.coreActor(r)
	if !ok {
		writeEnrollmentProblem(w, r, http.StatusUnauthorized, "INVALID_REQUEST", "The caller could not be authenticated.")
		return
	}
	var request createInvitationRequest
	if !decodeEnrollmentJSON(w, r, &request) {
		return
	}
	operationID := r.Header.Get("X-ARIMMS-Operation-Id")
	idempotencyKey := r.Header.Get("Idempotency-Key")
	actorID := r.Header.Get("X-ARIMMS-Actor-Id")
	if request.SchemaVersion != 1 || operationID == "" || operationID != request.OperationID || len(operationID) > 128 ||
		len(idempotencyKey) < 16 || len(idempotencyKey) > 128 || len(actorID) == 0 || len(actorID) > 128 ||
		strings.TrimSpace(actorID) != actorID {
		writeEnrollmentProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST", "The request body is invalid.")
		return
	}
	var requestedTTL time.Duration
	if request.TTLSeconds != nil {
		if *request.TTLSeconds < 60 || *request.TTLSeconds > 86400 {
			writeEnrollmentProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST", "The request body is invalid.")
			return
		}
		requestedTTL = time.Duration(*request.TTLSeconds) * time.Second
	}
	result, err := h.service.CreateInvitation(r.Context(), application.CreateInvitationCommand{
		OperationID: operationID, CorePrincipalID: corePrincipalID, IdempotencyKey: idempotencyKey,
		ActorID: actorID, NodeID: request.NodeID, RequestedTTL: requestedTTL,
	})
	if err != nil {
		writeEnrollmentError(w, r, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	w.Header().Set("Cache-Control", "no-store")
	writeEnrollmentJSON(w, r, status, createInvitationResponse{
		SchemaVersion: 1,
		OperationID:   result.OperationID, InvitationID: result.InvitationID,
		Invitation: result.Secret, NodeID: result.NodeID, Status: string(result.Status), ExpiresAt: result.ExpiresAt,
	})
}

func (h *EnrollmentHandler) exchange(w http.ResponseWriter, r *http.Request) {
	var request exchangeRequest
	if !decodeEnrollmentJSON(w, r, &request) {
		return
	}
	result, err := h.service.Exchange(r.Context(), application.ExchangeInvitationCommand{
		AttemptID: request.AttemptID, AgentID: request.AgentID,
		InvitationToken: request.InvitationToken, RecoveryProof: request.RecoveryProof,
	})
	if err != nil {
		writeEnrollmentError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeEnrollmentJSON(w, r, http.StatusCreated, exchangeResponse{
		AgentID: result.AgentID, NodeID: result.NodeID, CredentialID: result.CredentialID,
		Credential: result.Credential, CredentialScope: "opamp.connect", OpAMPEndpoint: result.OpAMPEndpoint,
		CredentialExpiresAt: result.CredentialExpiresAt,
	})
}

func (h *EnrollmentHandler) changeGate(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.coreActor(r); !ok {
		writeEnrollmentProblem(w, r, http.StatusUnauthorized, "INVALID_REQUEST", "The caller could not be authenticated.")
		return
	}
	var request changeGateRequest
	if !decodeEnrollmentJSON(w, r, &request) {
		return
	}
	result, err := h.service.ChangeGate(r.Context(), application.ChangeEnrollmentGateCommand{
		OperationID: request.OperationID, NodeID: chi.URLParam(r, "node_id"),
		Status: domain.EnrollmentGateStatus(request.Status),
	})
	if err != nil {
		writeEnrollmentError(w, r, err)
		return
	}
	writeEnrollmentJSON(w, r, http.StatusOK, changeGateResponse{
		NodeID: result.NodeID, Status: string(result.Status), OperationID: result.OperationID,
		Version: result.Version, BoundAgentIDs: result.BoundAgentIDs,
	})
}

func (h *EnrollmentHandler) getStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.coreActor(r); !ok {
		writeEnrollmentProblem(w, r, http.StatusUnauthorized, "INVALID_REQUEST", "The caller could not be authenticated.")
		return
	}
	status, err := h.service.GetStatus(r.Context(), chi.URLParam(r, "operation_id"))
	if err != nil {
		writeEnrollmentError(w, r, err)
		return
	}
	writeEnrollmentJSON(w, r, http.StatusOK, enrollmentStatusResponse{
		SchemaVersion: 1, OperationID: status.OperationID, InvitationID: status.InvitationID,
		NodeID: status.NodeID, Status: status.Status,
		AgentID: status.AgentID, CredentialID: status.CredentialID,
	})
}

func (h *EnrollmentHandler) getInstallation(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.coreActor(r); !ok {
		writeEnrollmentProblem(w, r, http.StatusUnauthorized, "INVALID_REQUEST", "The caller could not be authenticated.")
		return
	}
	installation, err := h.service.GetInstallationByAgentID(r.Context(), chi.URLParam(r, "agent_id"))
	if err != nil {
		writeEnrollmentError(w, r, err)
		return
	}
	writeEnrollmentJSON(w, r, http.StatusOK, installationResponse{
		InstallationID: installation.InstallationID, NodeID: installation.NodeID, Status: installation.Status,
	})
}

func (h *EnrollmentHandler) getActiveInstallation(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.coreActor(r); !ok {
		writeEnrollmentProblem(w, r, http.StatusUnauthorized, "INVALID_REQUEST", "The caller could not be authenticated.")
		return
	}
	installation, err := h.service.GetActiveInstallationByNode(r.Context(), chi.URLParam(r, "node_id"))
	if err != nil {
		writeEnrollmentError(w, r, err)
		return
	}
	writeEnrollmentJSON(w, r, http.StatusOK, installationResponse{
		InstallationID: installation.InstallationID, NodeID: installation.NodeID, Status: installation.Status,
	})
}

func (h *EnrollmentHandler) recover(w http.ResponseWriter, r *http.Request) {
	var request recoveryRequest
	if !decodeEnrollmentJSON(w, r, &request) {
		return
	}
	result, err := h.service.Recover(r.Context(), request.AttemptID, request.AgentID, request.RecoveryProof)
	if err != nil {
		writeEnrollmentError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeEnrollmentJSON(w, r, http.StatusOK, exchangeResponse{
		AgentID: result.AgentID, NodeID: result.NodeID, CredentialID: result.CredentialID,
		Credential: result.Credential, CredentialScope: "opamp.connect", OpAMPEndpoint: result.OpAMPEndpoint,
		CredentialExpiresAt: result.CredentialExpiresAt,
	})
}

func (h *EnrollmentHandler) coreActor(r *http.Request) (string, bool) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return "", false
	}
	actor := r.TLS.PeerCertificates[0].Subject.CommonName
	if actor != h.coreCN {
		return "", false
	}
	return actor, true
}

func decodeEnrollmentJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxEnrollmentRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeEnrollmentProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST", "The request body is invalid.")
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeEnrollmentProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST", "The request body is invalid.")
		return false
	}
	return true
}

func writeEnrollmentJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	if err := httpapi.WriteJSON(w, status, "application/json", value); err != nil && w.Header().Get("Content-Type") == "" {
		writeEnrollmentProblem(w, r, http.StatusInternalServerError, "TEMPORARY_UNAVAILABLE", "The response could not be encoded.")
	}
}

func writeEnrollmentError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := "TEMPORARY_UNAVAILABLE"
	detail := "The request could not be completed."
	switch {
	case errors.Is(err, domain.ErrEnrollmentConflict):
		status = http.StatusConflict
		code = "ENROLLMENT_CONFLICT"
		detail = "The enrollment operation conflicts with existing state."
	case errors.Is(err, domain.ErrEnrollmentNotFound):
		status = http.StatusNotFound
		code = "INVALID_REQUEST"
		detail = "The requested enrollment resource was not found."
	case errors.Is(err, domain.ErrInvitationExpired):
		status = http.StatusGone
		code = "INVITATION_REJECTED"
		detail = "The invitation is no longer valid."
	case errors.Is(err, domain.ErrInvitationRejected):
		status = http.StatusUnauthorized
		code = "INVITATION_REJECTED"
		detail = "The invitation could not be accepted."
	case errors.Is(err, domain.ErrEnrollmentRecoveryRejected):
		status = http.StatusUnauthorized
		code = "RECOVERY_REJECTED"
		detail = "The enrollment recovery request could not be verified."
	case errors.Is(err, domain.ErrEnrollmentUnavailable), errors.Is(err, domain.ErrIdentityUnavailable):
		status = http.StatusServiceUnavailable
		code = "TEMPORARY_UNAVAILABLE"
		detail = "The enrollment service is temporarily unavailable."
	case strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "outside the allowed range"):
		status = http.StatusBadRequest
		code = "INVALID_REQUEST"
		detail = "The enrollment request is invalid."
	}
	writeEnrollmentProblem(w, r, status, code, detail)
}

func writeEnrollmentProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	requestID := httpapi.RequestIDFromContext(r.Context())
	if requestID == "" {
		requestID = r.Header.Get(httpapi.RequestIDHeader)
	}
	problem := httpapi.Problem{
		Type: "about:blank", Title: http.StatusText(status), Status: status,
		Detail: detail, Instance: "/problems/" + url.PathEscape(requestID),
		Code: code, RequestID: requestID,
	}
	if err := httpapi.WriteProblem(w, status, problem); err != nil {
		http.Error(w, "request could not be processed", status)
	}
}
