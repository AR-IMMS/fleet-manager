package http

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/application"
	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
)

type otlpIntrospectionRequest struct {
	Token string `json:"token"`
}

type otlpIntrospectionResponse struct {
	SchemaVersion int        `json:"schema_version"`
	Active        bool       `json:"active"`
	AgentID       string     `json:"agent_id,omitempty"`
	CredentialID  string     `json:"credential_id,omitempty"`
	Scope         string     `json:"scope,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

// SetOTLPVerifier enables the private introspection route for one exact
// Gateway ingress client certificate identity.
func (h *EnrollmentHandler) SetOTLPVerifier(verifier application.OTLPVerifier, clientCN string) error {
	if h == nil || verifier == nil || strings.TrimSpace(clientCN) == "" || strings.TrimSpace(clientCN) != clientCN || clientCN == h.coreCN {
		return errors.New("OTLP introspection requires a distinct trusted Gateway client identity")
	}
	h.otlpVerifier, h.otlpClientCN = verifier, clientCN
	return nil
}

func (h *EnrollmentHandler) introspectOTLP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 ||
		r.TLS.PeerCertificates[0].Subject.CommonName != h.otlpClientCN {
		writeEnrollmentProblem(w, r, http.StatusUnauthorized, "INVALID_REQUEST", "The caller could not be authenticated.")
		return
	}
	var request otlpIntrospectionRequest
	if !decodeEnrollmentJSON(w, r, &request) {
		return
	}
	if len(request.Token) < 32 || len(request.Token) > 256 {
		writeEnrollmentProblem(w, r, http.StatusBadRequest, "INVALID_REQUEST", "The request body is invalid.")
		return
	}
	identity, err := h.otlpVerifier.VerifyOTLP(r.Context(), request.Token)
	if errors.Is(err, domain.ErrCredentialRejected) {
		writeEnrollmentJSON(w, r, http.StatusOK, otlpIntrospectionResponse{SchemaVersion: 1, Active: false})
		return
	}
	if err != nil {
		writeEnrollmentProblem(w, r, http.StatusServiceUnavailable, "TEMPORARY_UNAVAILABLE", "The credential authority is unavailable.")
		return
	}
	if identity.AgentID == "" || identity.CredentialID == "" || identity.Scope != "otlp.write" {
		writeEnrollmentProblem(w, r, http.StatusServiceUnavailable, "TEMPORARY_UNAVAILABLE", "The credential authority returned an invalid identity.")
		return
	}
	writeEnrollmentJSON(w, r, http.StatusOK, otlpIntrospectionResponse{
		SchemaVersion: 1, Active: true, AgentID: identity.AgentID,
		CredentialID: identity.CredentialID, Scope: identity.Scope, ExpiresAt: identity.ExpiresAt,
	})
}
