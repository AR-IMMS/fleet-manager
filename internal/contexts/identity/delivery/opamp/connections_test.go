package opamp

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server/types"
)

type deadlineConnection struct {
	net.Conn
	deadline     time.Time
	readDeadline time.Time
}

func (c *deadlineConnection) SetReadDeadline(deadline time.Time) error {
	c.readDeadline = deadline
	return nil
}

func (c *deadlineConnection) SetWriteDeadline(deadline time.Time) error {
	c.deadline = deadline
	return nil
}

func TestTransportDeadlineResetRemainsBounded(t *testing.T) {
	raw := &deadlineConnection{}
	tracked := &trackedConnection{Conn: raw}
	before := time.Now()
	if err := tracked.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if raw.deadline.Before(before) || raw.deadline.After(time.Now().Add(10*time.Second)) {
		t.Fatal("SDK reset removed write bound")
	}
	short := time.Now().Add(time.Millisecond)
	if err := tracked.SetWriteDeadline(short); err != nil {
		t.Fatal(err)
	}
	if !raw.deadline.Equal(short) {
		t.Fatal("shorter deadline was extended")
	}
}

func TestHandshakeReadDeadlineSurvivesHTTPReset(t *testing.T) {
	raw := &deadlineConnection{}
	handshake := time.Now().Add(time.Second)
	tracked := &trackedConnection{Conn: raw, handshakeDeadline: handshake}
	if err := tracked.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if !raw.readDeadline.Equal(handshake) {
		t.Fatal("HTTP reset removed handshake bound")
	}
	tracked.protocol.Store(true)
	if err := tracked.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if !raw.readDeadline.IsZero() {
		t.Fatal("handshake timeout leaked into protocol lifetime")
	}
}

type testConnection struct{ net.Conn }

func (c testConnection) Connection() net.Conn                               { return c.Conn }
func (c testConnection) Disconnect() error                                  { return c.Close() }
func (testConnection) Send(context.Context, *protobufs.ServerToAgent) error { return nil }

func TestConnectionLifetimeIndependentOfUpgradeAndOtherConnections(t *testing.T) {
	root, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()
	sessions := newConnectionSessions(root)
	first, peer1 := net.Pipe()
	second, peer2 := net.Pipe()
	t.Cleanup(func() { _ = first.Close(); _ = second.Close(); _ = peer1.Close(); _ = peer2.Close() })
	var contexts []context.Context
	callbacks := types.ConnectionCallbacks{
		OnConnected: func(ctx context.Context, _ types.Connection) { contexts = append(contexts, ctx) },
		OnMessage: func(context.Context, types.Connection, *protobufs.AgentToServer) *protobufs.ServerToAgent {
			return &protobufs.ServerToAgent{}
		},
		OnConnectionClose: func(types.Connection) {},
	}
	a, b := sessions.wrap(callbacks), sessions.wrap(callbacks)
	handshake, cancel := context.WithCancel(context.Background())
	cancel()
	a.OnConnected(handshake, testConnection{first})
	b.OnConnected(handshake, testConnection{second})
	if len(contexts) != 2 || contexts[0].Err() != nil || contexts[1].Err() != nil {
		t.Fatal("upgrade cancellation leaked into session")
	}
	a.OnConnectionClose(testConnection{first})
	if contexts[0].Err() != context.Canceled || contexts[1].Err() != nil {
		t.Fatal("connection lifetimes not isolated")
	}
	sessions.drain()
	cancelRoot()
	if contexts[1].Err() != context.Canceled {
		t.Fatal("server stop did not cancel session")
	}
	if b.OnMessage(context.Background(), testConnection{second}, nil) != nil {
		t.Fatal("message processed after draining")
	}
	b.OnConnectionClose(testConnection{second})
	select {
	case <-sessions.idle:
	default:
		t.Fatal("callback registry did not drain")
	}
}
