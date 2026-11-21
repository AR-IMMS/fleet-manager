package grpcapi

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

func TestUnaryResponseMetadataOverTransport(t *testing.T) {
	interceptor, err := RequestIDUnaryInterceptor(RequestIDOptions{
		MetadataKey: "example-request-id", ReturnHeader: true, InvalidInput: RejectInvalid,
		Validate: func(string) error { return nil }, Generate: func() (string, error) { return "generated", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(grpc.ChainUnaryInterceptor(interceptor, interceptor))
	healthpb.RegisterHealthServer(server, health.NewServer())
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close(); <-done })
	conn, err := grpc.NewClient("passthrough:///test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("example-request-id", "client-id"))
	var headers metadata.MD
	_, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}, grpc.Header(&headers))
	if err != nil {
		t.Fatal(err)
	}
	values := headers.Get("example-request-id")
	if len(values) != 1 || values[0] != "client-id" {
		t.Fatalf("unexpected response headers: %v", values)
	}
}
