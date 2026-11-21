package opamp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/application"
	"github.com/AR-IMMS/fleet-manager/internal/contexts/identity/domain"
	platformconfig "github.com/AR-IMMS/fleet-manager/internal/platform/config"
	"github.com/open-telemetry/opamp-go/server"
)

func serverConfig(t *testing.T) platformconfig.OpAMPConfig {
	t.Helper()
	cfg, err := platformconfig.LoadConfig(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	cfg.ListenAddress = "127.0.0.1:0"
	return cfg.OpAMPConfig
}

type nilSessionAuthorizer struct{}

func (*nilSessionAuthorizer) BindAndAuthorize(context.Context, domain.Principal, []byte) error {
	return nil
}

func TestServerRequiresDependenciesAndValidConfig(t *testing.T) {
	cfg := serverConfig(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewServer(cfg, nil, logger); err == nil {
		t.Fatal("nil callbacks accepted")
	}
	if _, err := NewCallbackAdapter(nil, nil, logger); err == nil {
		t.Fatal("nil authenticator accepted")
	}
	var typedNil *application.Authenticator
	if _, err := NewCallbackAdapter(typedNil, nil, logger); err == nil {
		t.Fatal("typed nil accepted")
	}
	validAuthenticator, err := application.NewAuthenticator(verifierFunc(validVerifier), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCallbackAdapter(validAuthenticator, nil, logger); err == nil {
		t.Fatal("nil session authorizer accepted")
	}
	var typedNilAuthorizer *nilSessionAuthorizer
	if _, err := NewCallbackAdapter(validAuthenticator, typedNilAuthorizer, logger); err == nil {
		t.Fatal("typed nil session authorizer accepted")
	}
	if _, err := NewServer(cfg, &CallbackAdapter{}, logger); err == nil {
		t.Fatal("zero callback adapter accepted")
	}
}

func TestStopBeforeStart(t *testing.T) {
	s := &OpAMPServer{}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type failingSDK struct {
	server.OpAMPServer
	listener net.Listener
}

func (s *failingSDK) Start(settings server.StartSettings) error {
	s.listener = settings.Listener
	return errors.New("private SDK failure")
}

func TestSDKStartupFailureReleasesListener(t *testing.T) {
	srv := newTestServer(t, serverConfig(t), validVerifier)
	fake := &failingSDK{}
	srv.server = fake
	if err := srv.Start(); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("startup failure not redacted", err)
	}
	conn, err := net.DialTimeout("tcp", fake.listener.Addr().String(), time.Second)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("failed startup left listener open")
	}
}

func TestSDKLogsRedactProtocolDetails(t *testing.T) {
	var buf bytes.Buffer
	logger := sdkLogger{slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	logger.Errorf(context.Background(), "secret token=%s", "private-value")
	logger.Debugf(context.Background(), "payload=%s", "private-value")
	if strings.Contains(buf.String(), "private-value") || strings.Contains(buf.String(), "secret token") {
		t.Fatal("SDK diagnostics leaked")
	}
}
