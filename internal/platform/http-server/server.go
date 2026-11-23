package httpserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

var (
	// ErrServe means the listener stopped without a shutdown request.
	ErrServe = errors.New("http server stopped unexpectedly")

	// ErrShutdown means graceful shutdown did not complete.
	ErrShutdown = errors.New("http server shutdown failed")
)

// Options configures the HTTP listener and request timeouts.
//
// This baseline accepts plaintext only on an explicit loopback IP.
// Public listeners require an explicit TLS or trusted proxy design.
type Options struct {
	ListenAddress     string
	TLSCertFile       string
	TLSKeyFile        string
	ClientCAFile      string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// Validate checks listener safety and bounded request timeouts.
func (o Options) Validate() error {
	host, port, err := net.SplitHostPort(o.ListenAddress)
	if err != nil {
		return fmt.Errorf("invalid http listen address")
	}

	ip := net.ParseIP(host)

	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return fmt.Errorf("http port must be between 0 and 65535")
	}

	if o.ReadHeaderTimeout <= 0 ||
		o.ReadTimeout <= 0 ||
		o.WriteTimeout <= 0 ||
		o.IdleTimeout <= 0 {
		return fmt.Errorf("http timeouts must be positive")
	}
	if (o.TLSCertFile == "") != (o.TLSKeyFile == "") {
		return fmt.Errorf("http TLS certificate and key must be configured together")
	}
	if o.TLSCertFile == "" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("http listener must use an explicit loopback ip without TLS")
	}
	if o.ClientCAFile != "" && o.TLSCertFile == "" {
		return fmt.Errorf("http client CA requires TLS")
	}

	return nil
}

// Server owns one listener and serving goroutine.
//
// Start and Shutdown are called sequentially by application composition.
// A Server supports a single successful start.
type Server struct {
	options Options
	server  *http.Server

	listener net.Listener
	started  bool

	done   chan struct{}
	errors chan error

	shutdownOnce sync.Once
	shutdownErr  error
}

// New constructs a server without opening a listener.
//
// The caller provides routes and owns their dependencies.
func New(cfg Options, handler http.Handler) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if handler == nil {
		return nil, fmt.Errorf("http handler is required")
	}

	var tlsConfig *tls.Config
	if cfg.TLSCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load http TLS certificate")
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
		if cfg.ClientCAFile != "" {
			caPEM, err := os.ReadFile(cfg.ClientCAFile)
			if err != nil {
				return nil, fmt.Errorf("load http client CA")
			}
			clientCAs := x509.NewCertPool()
			if !clientCAs.AppendCertsFromPEM(caPEM) {
				return nil, fmt.Errorf("http client CA contains no usable certificates")
			}
			tlsConfig.ClientCAs = clientCAs
			tlsConfig.ClientAuth = tls.VerifyClientCertIfGiven
		}
	}

	return &Server{
		options: cfg,
		server: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
			ReadTimeout:       cfg.ReadTimeout,
			WriteTimeout:      cfg.WriteTimeout,
			IdleTimeout:       cfg.IdleTimeout,
			MaxHeaderBytes:    16 << 10,
			TLSConfig:         tlsConfig,
		},
		done:   make(chan struct{}),
		errors: make(chan error, 1),
	}, nil
}

// Start binds synchronously and then starts serving.
//
// Bind errors are returned before Start succeeds. Context controls listener
// acquisition; runtime cancellation is handled by application composition.
func (s *Server) Start(ctx context.Context) error {
	if s.started {
		return fmt.Errorf("http server already started")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	var listenerConfig net.ListenConfig

	listener, err := listenerConfig.Listen(ctx, "tcp", s.options.ListenAddress)
	if err != nil {
		return fmt.Errorf("bind http listener: %w", err)
	}

	s.listener = listener
	s.started = true

	go s.serve()

	return nil
}

// Addr returns the actual address after Start, including an ephemeral port.
func (s *Server) Addr() net.Addr {
	if s.listener == nil {
		return nil
	}

	return s.listener.Addr()
}

// Errors reports unexpected serving failure after successful startup.
//
// The channel closes on server exit. Normal shutdown sends no error.
func (s *Server) Errors() <-chan error {
	return s.errors
}

// Shutdown stops accepting requests and waits for active HTTP requests.
//
// If the deadline expires, active connections are closed. Handlers must honor
// request cancellation and manage any background work they create.
func (s *Server) Shutdown(ctx context.Context) error {
	if !s.started {
		return nil
	}

	s.shutdownOnce.Do(func() {
		err := s.server.Shutdown(ctx)
		if err != nil {
			// Preserve cancellation classification without exposing internal
			// server details to callers.
			s.shutdownErr = errors.Join(ErrShutdown, ctx.Err())

			if closeErr := s.server.Close(); closeErr != nil {
				s.shutdownErr = errors.Join(s.shutdownErr, ErrShutdown)
			}
		}

		<-s.done
	})

	return s.shutdownErr
}

// serve forwards unexpected listener failures to application composition.
func (s *Server) serve() {
	defer close(s.done)
	defer close(s.errors)

	listener := net.Listener(s.listener)
	if s.server.TLSConfig != nil {
		listener = tls.NewListener(listener, s.server.TLSConfig)
	}
	err := s.server.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.errors <- ErrServe
	}
}
