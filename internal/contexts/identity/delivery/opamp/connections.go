package opamp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
)

// connectionListener also tracks sockets that have not authenticated/upgraded yet.
// SDK Stop only shuts down HTTP; hijacked sockets need explicit ownership here.
type connectionListener struct {
	net.Listener
	mu          sync.Mutex
	closed      bool
	failure     error
	failures    chan error
	connections map[*trackedConnection]struct{}
}

func newConnectionListener(listener net.Listener) *connectionListener {
	return &connectionListener{
		Listener:    listener,
		failures:    make(chan error, 1),
		connections: make(map[*trackedConnection]struct{}),
	}
}

func (l *connectionListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		l.mu.Lock()
		var temporary interface{ Temporary() bool }
		isTemporary := errors.As(err, &temporary) && temporary.Temporary()
		if !l.closed && !isTemporary && l.failure == nil {
			l.failure = err
			l.failures <- err
		}
		l.mu.Unlock()
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	// Bound slow TLS/HTTP handshakes. Successful protocol connections clear this.
	deadline := time.Now().Add(10 * time.Second)
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, err
	}
	tracked := &trackedConnection{Conn: conn, owner: l, handshakeDeadline: deadline}
	l.connections[tracked] = struct{}{}
	return tracked, nil
}

func (l *connectionListener) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.failure
}

func (l *connectionListener) Errors() <-chan error { return l.failures }

func (l *connectionListener) closeConnections() {
	l.mu.Lock()
	l.closed = true
	connections := make([]*trackedConnection, 0, len(l.connections))
	for c := range l.connections {
		connections = append(connections, c)
	}
	l.mu.Unlock()
	_ = l.Close()
	for _, c := range connections {
		_ = c.Close()
	}
}

type trackedConnection struct {
	net.Conn
	owner             *connectionListener
	handshakeDeadline time.Time
	protocol          atomic.Bool
}

// net/http also resets read deadlines while parsing headers. Preserve the
// acceptance-time bound until the authenticated protocol connection is ready.
func (c *trackedConnection) SetReadDeadline(deadline time.Time) error {
	if !c.protocol.Load() && (deadline.IsZero() || deadline.After(c.handshakeDeadline)) {
		deadline = c.handshakeDeadline
	}
	return c.Conn.SetReadDeadline(deadline)
}

func (c *trackedConnection) SetDeadline(deadline time.Time) error {
	if err := c.SetReadDeadline(deadline); err != nil {
		return err
	}
	return c.SetWriteDeadline(deadline)
}

// Gorilla resets the socket write deadline before each frame. Clamp that reset
// here because the pinned SDK does not set a WebSocket write timeout itself.
func (c *trackedConnection) SetWriteDeadline(deadline time.Time) error {
	limit := time.Now().Add(10 * time.Second)
	if deadline.IsZero() || deadline.After(limit) {
		deadline = limit
	}
	return c.Conn.SetWriteDeadline(deadline)
}

func (c *trackedConnection) Close() error {
	err := c.Conn.Close()
	c.owner.mu.Lock()
	delete(c.owner.connections, c)
	c.owner.mu.Unlock()
	return err
}

// connectionSessions tracks callback lifetimes, not installation/UID ownership.
type connectionSessions struct {
	ctx      context.Context
	mu       sync.Mutex
	stopping bool
	active   map[types.Connection]context.CancelFunc
	idle     chan struct{}
}

func newConnectionSessions(ctx context.Context) *connectionSessions {
	return &connectionSessions{ctx: ctx, active: make(map[types.Connection]context.CancelFunc), idle: make(chan struct{})}
}
func (s *connectionSessions) drain() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopping = true
	if len(s.active) == 0 {
		close(s.idle)
	}
}

func (s *connectionSessions) callbacks(adapter *CallbackAdapter) types.Callbacks {
	callbacks := adapter.Callbacks()
	connecting := callbacks.OnConnecting
	callbacks.OnConnecting = func(r *http.Request) types.ConnectionResponse {
		if s.ctx.Err() != nil {
			return types.ConnectionResponse{Accept: false, HTTPStatusCode: http.StatusServiceUnavailable}
		}
		ctx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(s.ctx, cancel)
		defer stop()
		defer cancel()
		response := connecting(r.WithContext(ctx))
		if response.Accept {
			response.ConnectionCallbacks = s.wrap(response.ConnectionCallbacks)
		}
		return response
	}
	return callbacks
}

func (s *connectionSessions) wrap(original types.ConnectionCallbacks) types.ConnectionCallbacks {
	callbacks := original
	// SDK callbacks are sequential within one connection. Cross-connection state is locked.
	var sessionCtx context.Context
	callbacks.OnConnected = func(_ context.Context, conn types.Connection) {
		s.mu.Lock()
		if s.stopping {
			s.mu.Unlock()
			_ = conn.Disconnect()
			return
		}
		var cancel context.CancelFunc
		sessionCtx, cancel = context.WithCancel(s.ctx)
		s.active[conn] = cancel
		s.mu.Unlock()
		transport := conn.Connection()
		if tlsConn, ok := transport.(interface{ NetConn() net.Conn }); ok {
			transport = tlsConn.NetConn()
		}
		if tracked, ok := transport.(*trackedConnection); ok {
			tracked.protocol.Store(true)
		}
		if err := transport.SetDeadline(time.Time{}); err != nil {
			_ = conn.Disconnect()
			return
		}
		original.OnConnected(sessionCtx, conn)
	}
	callbacks.OnMessage = func(_ context.Context, conn types.Connection, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
		if sessionCtx == nil || sessionCtx.Err() != nil {
			return nil
		}
		return original.OnMessage(sessionCtx, conn, msg)
	}
	callbacks.OnConnectionClose = func(conn types.Connection) {
		original.OnConnectionClose(conn)
		s.mu.Lock()
		defer s.mu.Unlock()
		if cancel, ok := s.active[conn]; ok {
			cancel()
			delete(s.active, conn)
			if s.stopping && len(s.active) == 0 {
				close(s.idle)
			}
		}
	}
	return callbacks
}
