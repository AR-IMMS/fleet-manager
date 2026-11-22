package httpserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testOptions supplies bounded timeouts and an ephemeral loopback listener.
func testOptions() Options {
	return Options{
		ListenAddress:     "127.0.0.1:0",
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       time.Second,
		WriteTimeout:      time.Second,
		IdleTimeout:       time.Second,
	}
}

// TestServerServesAndShutsDown verifies successful listener lifecycle.
func TestServerServesAndShutsDown(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /test", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	server, err := New(testOptions(), mux)
	if err != nil {
		t.Fatalf("construct server: %v", err)
	}

	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("start server: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("shutdown server: %v", err)
		}
	})

	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()

	response, err := client.Get("http://" + server.Addr().String() + "/test")
	if err != nil {
		t.Fatalf("send request: %v", err)
	}

	if err := response.Body.Close(); err != nil {
		t.Fatalf("close response: %v", err)
	}

	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", response.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	if err, ok := <-server.Errors(); ok {
		t.Fatalf("unexpected serving error: %v", err)
	}
}

// TestServerReturnsBindFailure ensures an occupied port fails during startup.
func TestServerReturnsBindFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}

	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Errorf("close listener: %v", err)
		}
	})

	cfg := testOptions()
	cfg.ListenAddress = listener.Addr().String()

	server, err := New(cfg, http.NewServeMux())
	if err != nil {
		t.Fatalf("construct server: %v", err)
	}

	if err := server.Start(context.Background()); err == nil {
		t.Fatal("expected occupied port to fail")
	}
}

// TestServerReportsUnexpectedListenerFailure verifies runtime error forwarding.
func TestServerReportsUnexpectedListenerFailure(t *testing.T) {
	server, err := New(testOptions(), http.NewServeMux())
	if err != nil {
		t.Fatalf("construct server: %v", err)
	}

	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("start server: %v", err)
	}

	if err := server.listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	select {
	case err := <-server.Errors():
		if !errors.Is(err, ErrServe) {
			t.Fatalf("expected serving failure, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serving failure was not reported")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestServerRejectsPublicPlaintext prevents accidental network exposure.
func TestServerRejectsPublicPlaintext(t *testing.T) {
	cfg := testOptions()
	cfg.ListenAddress = "0.0.0.0:8081"

	if _, err := New(cfg, http.NewServeMux()); err == nil {
		t.Fatal("expected public plaintext listener to be rejected")
	}
}

func TestServerTLSVerifiesOptionalCoreClientCertificate(t *testing.T) {
	certFile, keyFile, caFile, certificate := createHTTPTestCertificates(t)
	cfg := testOptions()
	cfg.TLSCertFile, cfg.TLSKeyFile, cfg.ClientCAFile = certFile, keyFile, caFile
	mux := http.NewServeMux()
	mux.HandleFunc("GET /test", func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil && len(r.TLS.VerifiedChains) > 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	server, err := New(cfg, mux)
	if err != nil {
		t.Fatalf("construct TLS server: %v", err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("start TLS server: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("shutdown TLS server: %v", err)
		}
	})

	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	clientCert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("load client certificate: %v", err)
	}
	clients := []struct {
		name   string
		certs  []tls.Certificate
		status int
	}{
		{name: "Core client certificate", certs: []tls.Certificate{clientCert}, status: http.StatusNoContent},
		{name: "Agent without client certificate", status: http.StatusAccepted},
	}
	for _, tc := range clients {
		t.Run(tc.name, func(t *testing.T) {
			transport := &http.Transport{TLSClientConfig: &tls.Config{
				RootCAs: roots, Certificates: tc.certs, MinVersion: tls.VersionTLS12,
			}}
			client := &http.Client{Transport: transport, Timeout: time.Second}
			defer client.CloseIdleConnections()
			response, err := client.Get("https://" + server.Addr().String() + "/test")
			if err != nil {
				t.Fatalf("send HTTPS request: %v", err)
			}
			_ = response.Body.Close()
			if response.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, tc.status)
			}
		})
	}
}

func createHTTPTestCertificates(t *testing.T) (string, string, string, *x509.Certificate) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal("generate test certificate key", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "fleet-test"},
		NotBefore:    now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IsCA:        true, BasicConstraintsValid: true, DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal("create test certificate", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal("parse test certificate", err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal("encode test certificate key", err)
	}
	dir := t.TempDir()
	certFile, keyFile, caFile := filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"), filepath.Join(dir, "client-ca.crt")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal("write test certificate", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		t.Fatal("write test key", err)
	}
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal("write client CA", err)
	}
	return certFile, keyFile, caFile, certificate
}
