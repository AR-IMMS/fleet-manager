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

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
	"github.com/go-chi/chi/v5"
)

type otlpVerifierFunc func(context.Context, string) (domain.OTLPIdentity, error)

func (f otlpVerifierFunc) VerifyOTLP(ctx context.Context, token string) (domain.OTLPIdentity, error) {
	return f(ctx, token)
}

func TestOTLPIntrospectionRequiresGatewayCertificateAndHidesToken(t *testing.T) {
	service := enrollmentTestService(&httpEnrollmentStore{})
	handler, err := NewEnrollmentHandler(&service, "fleet-core")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("s", 48)
	if err := handler.SetOTLPVerifier(otlpVerifierFunc(func(_ context.Context, got string) (domain.OTLPIdentity, error) {
		if got != token {
			t.Fatal("wrong token passed to verifier")
		}
		return domain.OTLPIdentity{AgentID: "agent-1", CredentialID: "credential-1", Scope: "otlp.write"}, nil
	}), "gateway-ingress"); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	handler.Mount(router)
	call := func(cn string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/internal/v1/otlp/credentials/introspect", strings.NewReader(`{"token":"`+token+`"}`))
		r.Header.Set("Content-Type", "application/json")
		if cn != "" {
			cert := &x509.Certificate{Subject: pkix.Name{CommonName: cn}}
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	for _, cn := range []string{"", "fleet-core", "other"} {
		if w := call(cn); w.Code != http.StatusUnauthorized {
			t.Fatalf("CN %q: status %d", cn, w.Code)
		}
	}
	w := call("gateway-ingress")
	if w.Code != http.StatusOK {
		t.Fatalf("gateway status %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), token) {
		t.Fatal("response exposed token")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("response can be cached")
	}
	var result otlpIntrospectionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Active || result.AgentID != "agent-1" || result.Scope != "otlp.write" {
		t.Fatalf("unexpected response: %+v", result)
	}
}
