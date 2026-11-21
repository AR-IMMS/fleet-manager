package opamp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/application"
	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
	platformconfig "github.com/AR-IMMS/fleet-manager/internal/platform/config"
	"github.com/gorilla/websocket"
	"github.com/open-telemetry/opamp-go/client"
	clienttypes "github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"google.golang.org/protobuf/proto"
)

func newTestServer(t *testing.T, cfg platformconfig.OpAMPConfig, verify verifierFunc) *OpAMPServer {
	t.Helper()
	auth, err := application.NewAuthenticator(verify, cfg.AuthTimeout)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	callbacks, err := NewCallbackAdapter(auth, sessionAuthorizerFunc(func(context.Context, domain.Principal, []byte) error { return nil }), logger)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(cfg, callbacks, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
	})
	return srv
}

func TestAuthenticationTimeoutAndStopCancellation(t *testing.T) {
	cfg := serverConfig(t)
	cfg.AuthTimeout = 20 * time.Millisecond
	srv := newTestServer(t, cfg, func(ctx context.Context, _ string) (domain.Principal, error) {
		<-ctx.Done()
		return domain.Principal{}, ctx.Err()
	})
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	dialer := websocket.Dialer{HandshakeTimeout: time.Second}
	conn, response, err := dialer.Dial("ws://"+srv.Addr().String()+cfg.OpAMPPath, http.Header{"Authorization": []string{"Bearer slow"}})
	if conn != nil {
		_ = conn.Close()
	}
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil || response == nil || response.StatusCode != 503 {
		t.Fatal("late authentication was admitted", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = srv.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("shutdown cancellation lost", err)
	}
}

func TestInvalidUIDDisconnects(t *testing.T) {
	srv := newTestServer(t, serverConfig(t), validVerifier)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	conn, response, err := websocket.DefaultDialer.Dial("ws://"+srv.Addr().String()+srv.config.OpAMPPath, http.Header{"Authorization": []string{"Bearer valid"}})
	if response != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	payload, err := proto.Marshal(&protobufs.AgentToServer{InstanceUid: []byte{1}})
	if err != nil {
		t.Fatal(err)
	}
	if err = conn.WriteMessage(websocket.BinaryMessage, append([]byte{0}, payload...)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = conn.ReadMessage(); err == nil {
		t.Fatal("invalid UID accepted")
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatal("invalid UID did not disconnect")
	}
}

func validVerifier(_ context.Context, credential string) (domain.Principal, error) {
	if credential == "reject" {
		return domain.Principal{}, application.ErrUnauthenticated
	}
	if credential == "unavailable" {
		return domain.Principal{}, application.ErrVerifierUnavailable
	}
	return domain.NewPrincipal(credential, "credential-001")
}

func testTLS(t *testing.T, cfg *platformconfig.OpAMPConfig) *tls.Config {
	t.Helper()
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cert := fixture.TLS.Certificates[0]
	fixture.Close()
	dir := t.TempDir()
	cfg.TLSCertFile = filepath.Join(dir, "cert.pem")
	cfg.TLSKeyFile = filepath.Join(dir, "key.pem")
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.TLSCertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.TLSKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
}

func TestNativeClientsAndActiveShutdown(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "ws", true: "wss"}[secure], func(t *testing.T) {
			cfg := serverConfig(t)
			var clientTLS *tls.Config
			scheme := "ws://"
			if secure {
				clientTLS = testTLS(t, &cfg)
				scheme = "wss://"
			}
			srv := newTestServer(t, cfg, validVerifier)
			if err := srv.Start(); err != nil {
				t.Fatal(err)
			}
			if err := srv.Start(); err == nil {
				t.Fatal("double start accepted")
			}
			url := scheme + srv.Addr().String() + cfg.OpAMPPath
			for i := byte(1); i <= 2; i++ {
				cl := client.NewWebSocket(nil)
				if err := cl.SetAgentDescription(&protobufs.AgentDescription{IdentifyingAttributes: []*protobufs.KeyValue{{Key: "service.name", Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "test-agent"}}}}}); err != nil {
					t.Fatal(err)
				}
				received := make(chan struct{}, 1)
				settings := clienttypes.StartSettings{OpAMPServerURL: url, TLSConfig: clientTLS, Header: http.Header{"Authorization": []string{"Bearer agent" + string(rune('0'+i))}}, InstanceUid: clienttypes.InstanceUid{i}, Callbacks: clienttypes.Callbacks{OnMessage: func(context.Context, *clienttypes.MessageData) {
					select {
					case received <- struct{}{}:
					default:
					}
				}}}
				if err := cl.Start(context.Background(), settings); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					_ = cl.Stop(ctx)
				})
				select {
				case <-received:
				case <-time.After(5 * time.Second):
					t.Fatal("native client received no response")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := srv.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			if err := srv.Stop(ctx); err != nil {
				t.Fatal("repeat stop", err)
			}
			srv.sessions.mu.Lock()
			remaining := len(srv.sessions.active)
			srv.sessions.mu.Unlock()
			if remaining != 0 {
				t.Fatal("active callbacks remain", remaining)
			}
			if err := srv.Start(); err == nil {
				t.Fatal("restart accepted")
			}
		})
	}
}

func TestWebSocketAuthenticationAndProtocol(t *testing.T) {
	cfg := serverConfig(t)
	cfg.MaxMessageBytes = 128
	srv := newTestServer(t, cfg, validVerifier)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	url := "ws://" + srv.Addr().String() + cfg.OpAMPPath
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	for _, tc := range []struct {
		token string
		code  int
	}{{"", 401}, {"reject", 401}, {"unavailable", 503}} {
		headers := http.Header{}
		if tc.token != "" {
			headers.Set("Authorization", "Bearer "+tc.token)
		}
		conn, response, err := dialer.Dial(url, headers)
		if conn != nil {
			_ = conn.Close()
		}
		if response != nil {
			_ = response.Body.Close()
		}
		if err == nil || response == nil || response.StatusCode != tc.code {
			t.Fatalf("unexpected auth result: response=%v err=%v", response, err)
		}
	}
	// Reconnects are authenticated independently; this slice does not bind UID ownership.
	for i := 0; i < 2; i++ {
		conn, response, err := dialer.Dial(url, http.Header{"Authorization": []string{"Bearer valid"}})
		if response != nil {
			_ = response.Body.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if err = conn.WriteMessage(websocket.BinaryMessage, []byte{0, 0xff}); err != nil {
			t.Fatal(err)
		}
		uid := make([]byte, 16)
		uid[0] = byte(i + 1)
		payload, err := proto.Marshal(&protobufs.AgentToServer{InstanceUid: uid})
		if err != nil {
			t.Fatal(err)
		}
		if err = conn.WriteMessage(websocket.BinaryMessage, append([]byte{0}, payload...)); err != nil {
			t.Fatal(err)
		}
		_, reply, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var message protobufs.ServerToAgent
		if len(reply) < 1 || reply[0] != 0 {
			t.Fatal("missing native header")
		}
		if err = proto.Unmarshal(reply[1:], &message); err != nil {
			t.Fatal(err)
		}
		if string(message.InstanceUid) != string(uid) || message.Capabilities != 0 {
			t.Fatal("incorrect bootstrap response")
		}
		if err = conn.WriteMessage(websocket.BinaryMessage, make([]byte, 256)); err != nil {
			t.Fatal(err)
		}
		if _, _, err = conn.ReadMessage(); err == nil {
			t.Fatal("oversized message accepted")
		}
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			t.Fatal("oversized message did not disconnect")
		}
		_ = conn.Close()
	}
}

func TestStartupFailuresAndIncompleteHandshakeShutdown(t *testing.T) {
	cfg := serverConfig(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupied.Close() }()
	cfg.ListenAddress = occupied.Addr().String()
	blocked := newTestServer(t, cfg, validVerifier)
	if err = blocked.Start(); err == nil {
		t.Fatal("occupied port accepted")
	}
	cfg = serverConfig(t)
	cfg.TLSCertFile = "missing-cert"
	cfg.TLSKeyFile = "missing-key"
	broken := newTestServer(t, cfg, validVerifier)
	if err = broken.Start(); err == nil || broken.Addr() != nil {
		t.Fatal("TLS failure opened listener")
	}
	cfg = serverConfig(t)
	srv := newTestServer(t, cfg, validVerifier)
	if err = srv.Start(); err != nil {
		t.Fatal(err)
	}
	raw, err := net.DialTimeout("tcp", srv.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err = raw.Write([]byte("GET /v1/opamp HTTP/1.1\r\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = srv.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	_ = raw.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = raw.Read(make([]byte, 1)); err == nil {
		t.Fatal("incomplete handshake left open")
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatal("incomplete handshake did not close")
	}
}
