package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/application"
	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
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
	router.Post("/v1/enrollment/exchanges", h.exchange)
	router.Post("/v1/enrollment/recoveries", h.recover)
}

type createInvitationRequest struct {
	OperationID    string `json:"operation_id"`
	IdempotencyKey string `json:"idempotency_key"`
	NodeID         string `json:"node_id"`
	TTLSeconds     int64  `json:"ttl_seconds,omitempty"`
}

type createInvitationResponse struct {
	OperationID  string    `json:"operation_id"`
	InvitationID string    `json:"invitation_id"`
	Invitation   string    `json:"invitation_token"`
	NodeID       string    `json:"node_id"`
	Status       string    `json:"status"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type exchangeRequest struct {
	AttemptID       string `json:"enrollment_attempt_id"`
	AgentID         string `json:"agent_id"`
	InvitationToken string `json:"invitation_token"`
	RecoveryProof   string `json:"recovery_proof"`
}

type exchangeResponse struct {
	AgentID       string `json:"agent_id"`
	NodeID        string `json:"node_id"`
	CredentialID  string `json:"credential_id"`
	Credential    string `json:"credential"`
	OpAMPEndpoint string `json:"opamp_endpoint"`
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
	OperationID  string `json:"operation_id"`
	NodeID       string `json:"node_id"`
	Status       string `json:"status"`
	AgentID      string `json:"agent_id,omitempty"`
	CredentialID string `json:"credential_id,omitempty"`
}

func (h *EnrollmentHandler) createInvitation(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.coreActor(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var request createInvitationRequest
	if !decodeEnrollmentJSON(w, r, &request) {
		return
	}
	result, err := h.service.CreateInvitation(r.Context(), application.CreateInvitationCommand{
		OperationID: request.OperationID, IdempotencyKey: request.IdempotencyKey,
		ActorID: actor, NodeID: request.NodeID, RequestedTTL: time.Duration(request.TTLSeconds) * time.Second,
	})
	if err != nil {
		writeEnrollmentError(w, err)
		return
	}
	writeEnrollmentJSON(w, http.StatusCreated, createInvitationResponse{
		OperationID: result.OperationID, InvitationID: result.InvitationID,
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
		writeEnrollmentError(w, err)
		return
	}
	writeEnrollmentJSON(w, http.StatusCreated, exchangeResponse{
		AgentID: result.AgentID, NodeID: result.NodeID, CredentialID: result.CredentialID,
		Credential: result.Credential, OpAMPEndpoint: result.OpAMPEndpoint,
	})
}

func (h *EnrollmentHandler) changeGate(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.coreActor(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
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
		writeEnrollmentError(w, err)
		return
	}
	writeEnrollmentJSON(w, http.StatusOK, changeGateResponse{
		NodeID: result.NodeID, Status: string(result.Status), OperationID: result.OperationID,
		Version: result.Version, BoundAgentIDs: result.BoundAgentIDs,
	})
}

func (h *EnrollmentHandler) getStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.coreActor(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	status, err := h.service.GetStatus(r.Context(), chi.URLParam(r, "operation_id"))
	if err != nil {
		writeEnrollmentError(w, err)
		return
	}
	writeEnrollmentJSON(w, http.StatusOK, enrollmentStatusResponse{
		OperationID: status.OperationID, NodeID: status.NodeID, Status: status.Status,
		AgentID: status.AgentID, CredentialID: status.CredentialID,
	})
}

func (h *EnrollmentHandler) recover(w http.ResponseWriter, r *http.Request) {
	var request recoveryRequest
	if !decodeEnrollmentJSON(w, r, &request) {
		return
	}
	result, err := h.service.Recover(r.Context(), request.AttemptID, request.AgentID, request.RecoveryProof)
	if err != nil {
		writeEnrollmentError(w, err)
		return
	}
	writeEnrollmentJSON(w, http.StatusOK, exchangeResponse{
		AgentID: result.AgentID, NodeID: result.NodeID, CredentialID: result.CredentialID,
		Credential: result.Credential, OpAMPEndpoint: result.OpAMPEndpoint,
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
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}

func writeEnrollmentJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeEnrollmentError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, domain.ErrEnrollmentConflict):
		status = http.StatusConflict
	case errors.Is(err, domain.ErrEnrollmentNotFound):
		status = http.StatusNotFound
	case errors.Is(err, domain.ErrInvitationExpired), errors.Is(err, domain.ErrInvitationRejected), errors.Is(err, domain.ErrEnrollmentRecoveryRejected):
		status = http.StatusUnauthorized
	case strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "outside the allowed range"):
		status = http.StatusBadRequest
	}
	http.Error(w, http.StatusText(status), status)
}
