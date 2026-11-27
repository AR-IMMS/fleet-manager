package http

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
	httpserver "github.com/AR-IMMS/fleet-manager/internal/platform/http-server"
	"github.com/AR-IMMS/fleet-manager/internal/platform/testutil/pki"
	"github.com/go-chi/chi/v5"
)

// TestServiceIdentityOverTLS exercises the real TLS listener and mounted
// introspection route. The Core probe exercises coreActor, not a business API.
func TestServiceIdentityOverTLS(t *testing.T) {
	f := pki.New(t)
	service := enrollmentTestService(&httpEnrollmentStore{})
	handler, err := NewEnrollmentHandler(&service, "fleet-core")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("s", 48)
	var calls atomic.Int64
	if err := handler.SetOTLPVerifier(otlpVerifierFunc(func(_ context.Context, got string) (domain.OTLPIdentity, error) {
		calls.Add(1)
		if got != token {
			return domain.OTLPIdentity{}, domain.ErrCredentialRejected
		}
		return domain.OTLPIdentity{AgentID: "agent-1", CredentialID: "credential-1", Scope: "otlp.write"}, nil
	}), "gateway-ingress"); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	handler.Mount(router)
	const probe = "/test-only/core-identity"
	router.Get(probe, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := handler.coreActor(r); !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	const introspect = "/internal/v1/otlp/credentials/introspect"
	server, err := httpserver.New(httpserver.Options{
		ListenAddress: "127.0.0.1:0", TLSCertFile: f.Server.CertFile, TLSKeyFile: f.Server.KeyFile, ClientCAFile: f.ClientCAFile,
		ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 3 * time.Second,
	}, router)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	for _, tc := range []struct {
		name, path string
		identity   *pki.Identity
		status     int
		verify     bool
	}{
		{"FM-AUTH-01/core_probe", probe, &f.Core, http.StatusNoContent, false},
		{"FM-AUTH-02/ingress_introspection", introspect, &f.Ingress, http.StatusOK, true},
		{"FM-AUTH-03/core_to_introspection", introspect, &f.Core, http.StatusUnauthorized, false},
		{"FM-AUTH-04/ingress_to_core_probe", probe, &f.Ingress, http.StatusUnauthorized, false},
		{"FM-AUTH-05/unknown_to_core_probe", probe, &f.Unknown, http.StatusUnauthorized, false},
		{"FM-AUTH-05/unknown_to_introspection", introspect, &f.Unknown, http.StatusUnauthorized, false},
		{"FM-AUTH-06/no_cert_core_probe", probe, nil, http.StatusUnauthorized, false},
		{"FM-AUTH-06/no_cert_introspection", introspect, nil, http.StatusUnauthorized, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &http.Transport{TLSClientConfig: f.Client(tc.identity), DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			before := calls.Load()
			address := "https://" + server.Addr().String() + tc.path
			var resp *http.Response
			var err error
			if tc.path == introspect {
				resp, err = client.Post(address, "application/json", strings.NewReader(`{"token":"`+token+`"}`))
			} else {
				resp, err = client.Get(address)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := resp.Body.Close(); err != nil {
					t.Errorf("close response body: %v", err)
				}
			}()
			if resp.StatusCode != tc.status {
				t.Fatalf("status=%d want=%d", resp.StatusCode, tc.status)
			}
			want := before
			if tc.verify {
				want++
			}
			if calls.Load() != want {
				t.Fatalf("verifier calls=%d want=%d", calls.Load(), want)
			}
		})
	}
}
