package opamp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"

	platformconfig "github.com/AR-IMMS/fleet-manager/internal/platform/config"
	"github.com/gorilla/websocket"
	"github.com/open-telemetry/opamp-go/server"
)

// OpAMPServer owns one SDK listener and its connections. Start and Stop are
// serialized by application composition; a successfully started server cannot restart.
type OpAMPServer struct {
	server    server.OpAMPServer
	callbacks *CallbackAdapter
	config    platformconfig.OpAMPConfig
	listener  *connectionListener
	cancel    context.CancelFunc
	sessions  *connectionSessions
	started   bool
	stopped   bool
	stopErr   error
	mu        sync.Mutex
}

// NewServer validates settings without opening a listener or reading TLS files.
func NewServer(cfg platformconfig.OpAMPConfig, callbacks *CallbackAdapter, logger *slog.Logger) (*OpAMPServer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid OpAMP config: %w", err)
	}
	if callbacks == nil || logger == nil {
		return nil, fmt.Errorf("OpAMP callbacks and logger are required")
	}
	if _, err := NewCallbackAdapter(callbacks.authenticator, callbacks.logger); err != nil {
		return nil, err
	}
	if strings.ContainsAny(cfg.OpAMPPath, "{}") {
		return nil, fmt.Errorf("OpAMP path must be literal")
	}
	return &OpAMPServer{config: cfg, callbacks: callbacks, server: server.New(sdkLogger{logger.With("component", "opamp")})}, nil
}

// Start binds synchronously and installs mandatory authentication callbacks.
// Failed startup releases acquired sockets; no resources are acquired by construction.
func (s *OpAMPServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
		return fmt.Errorf("OpAMP server cannot be restarted")
	}
	var tlsConfig *tls.Config
	if s.config.TLSCertFile != "" {
		cert, err := tls.LoadX509KeyPair(s.config.TLSCertFile, s.config.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("load OpAMP TLS key pair")
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	}
	listener, err := net.Listen("tcp", s.config.ListenAddress)
	if err != nil {
		return fmt.Errorf("bind OpAMP listener")
	}
	s.listener = newConnectionListener(listener)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.sessions = newConnectionSessions(ctx)
	err = s.server.Start(server.StartSettings{
		Settings: server.Settings{Callbacks: s.sessions.callbacks(s.callbacks), MaxMessageSize: s.config.MaxMessageBytes},
		Listener: s.listener, ListenPath: s.config.OpAMPPath, TLSConfig: tlsConfig,
		HTTPMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !websocket.IsWebSocketUpgrade(r) {
					http.Error(w, "WebSocket upgrade required", http.StatusUpgradeRequired)
					return
				}
				next.ServeHTTP(w, r)
			})
		},
	})
	if err != nil {
		cancel()
		s.listener.closeConnections()
		s.stopped = true
		return fmt.Errorf("start OpAMP server")
	}
	s.started = true
	return nil
}

// Stop rejects new work, closes even hijacked WebSockets, and waits for callbacks
// within ctx's budget. Repeated calls return the first shutdown result.
func (s *OpAMPServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return s.stopErr
	}
	if !s.started {
		return nil
	}
	s.stopped = true
	s.sessions.drain()
	s.cancel()
	s.listener.closeConnections()
	if err := s.server.Stop(ctx); err != nil {
		s.stopErr = fmt.Errorf("stop OpAMP server")
	}
	select {
	case <-s.sessions.idle:
	case <-ctx.Done():
		s.stopErr = errors.Join(s.stopErr, ctx.Err())
	}
	if ctx.Err() != nil {
		s.stopErr = errors.Join(s.stopErr, ctx.Err())
	}
	return s.stopErr
}

// Addr exposes the bound address for diagnostics and ephemeral-port tests.
func (s *OpAMPServer) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// SDK diagnostic arguments can include raw protocol input. Keep only safe labels.
type sdkLogger struct{ logger *slog.Logger }

func (l sdkLogger) Debugf(ctx context.Context, _ string, _ ...interface{}) {
	l.logger.DebugContext(ctx, "OpAMP transport diagnostic")
}
func (l sdkLogger) Errorf(ctx context.Context, _ string, _ ...interface{}) {
	l.logger.WarnContext(ctx, "OpAMP transport operation failed")
}
