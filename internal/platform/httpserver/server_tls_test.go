package httpserver

import (
	"context"
	"crypto/x509"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/platform/testutil/pki"
)

// TestServerSeparatedTrust verifies the real listener using independent CAs.
func TestServerSeparatedTrust(t *testing.T) {
	f := pki.New(t)
	cfg := testOptions()
	cfg.TLSCertFile, cfg.TLSKeyFile, cfg.ClientCAFile = f.Server.CertFile, f.Server.KeyFile, f.ClientCAFile
	var hits atomic.Int64
	server, err := New(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.TLS != nil && len(r.TLS.VerifiedChains) > 0 {
			w.Header().Set("X-Test-Identity", r.TLS.PeerCertificates[0].Subject.CommonName)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
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
		name       string
		identity   *pki.Identity
		wrongRoots bool
		wantCN     string
		reject     bool
	}{
		{name: "FM-TLS-01/independent_server_trust", identity: &f.Core, wrongRoots: true, reject: true},
		{name: "FM-TLS-02/core", identity: &f.Core, wantCN: "fleet-core"},
		{name: "FM-TLS-03/ingress", identity: &f.Ingress, wantCN: "gateway-ingress"},
		{name: "FM-TLS-04/untrusted_issuer", identity: &f.Untrusted, reject: true},
		{name: "FM-TLS-08/no_client_certificate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tlsConfig := f.Client(tc.identity)
			if tc.wrongRoots {
				raw, err := os.ReadFile(f.ClientCAFile)
				if err != nil {
					t.Fatal(err)
				}
				tlsConfig.RootCAs = x509.NewCertPool()
				if !tlsConfig.RootCAs.AppendCertsFromPEM(raw) {
					t.Fatal("invalid fixture CA")
				}
			}
			transport := &http.Transport{TLSClientConfig: tlsConfig, DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			before := hits.Load()
			resp, err := client.Get("https://" + server.Addr().String() + "/test")
			if resp != nil {
				defer func() {
					if err := resp.Body.Close(); err != nil {
						t.Errorf("close response body: %v", err)
					}
				}()
			}
			if tc.reject {
				if err == nil || hits.Load() != before {
					t.Fatalf("expected TLS rejection before handler; err=%v", err)
				}
				if !strings.Contains(err.Error(), "certificate") && !strings.Contains(err.Error(), "tls:") {
					t.Fatalf("unexpected transport error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusNoContent || resp.Header.Get("X-Test-Identity") != tc.wantCN || hits.Load() != before+1 {
				t.Fatalf("unexpected response status=%d identity=%q", resp.StatusCode, resp.Header.Get("X-Test-Identity"))
			}
		})
	}
}

// TestServerTLSRejectsInvalidFiles checks fail-closed startup configuration.
func TestServerTLSRejectsInvalidFiles(t *testing.T) {
	f := pki.New(t)
	bad := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(bad, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Options)
		want   string
	}{
		{"FM-TLS-05/invalid_client_CA", func(o *Options) { o.ClientCAFile = bad }, "http client CA contains no usable certificates"},
		{"FM-TLS-06/missing_client_CA_file", func(o *Options) { o.ClientCAFile = bad + ".missing" }, "load http client CA"},
		{"FM-TLS-07/mismatched_server_key", func(o *Options) { o.TLSKeyFile = f.Core.KeyFile }, "load http TLS certificate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testOptions()
			cfg.TLSCertFile, cfg.TLSKeyFile, cfg.ClientCAFile = f.Server.CertFile, f.Server.KeyFile, f.ClientCAFile
			tc.mutate(&cfg)
			if _, err := New(cfg, http.NewServeMux()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}
