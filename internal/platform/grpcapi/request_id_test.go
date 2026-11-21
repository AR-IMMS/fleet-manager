package grpcapi

import (
	"context"
	"errors"
	"testing"

	"github.com/AR-IMMS/fleet-manager/internal/shared/correlation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type testStream struct{ headers metadata.MD }

func (*testStream) Method() string { return "/test" }
func (s *testStream) SetHeader(md metadata.MD) error {
	s.headers = metadata.Join(s.headers, md)
	return nil
}
func (s *testStream) SendHeader(md metadata.MD) error { return s.SetHeader(md) }
func (*testStream) SetTrailer(metadata.MD) error      { return nil }

func TestUnaryCorrelation(t *testing.T) {
	for _, tc := range []struct {
		name             string
		values           []string
		inherited        string
		policy           InvalidIDPolicy
		fail             bool
		invalidGenerated bool
		code             codes.Code
		calls            int
	}{
		{name: "generate", policy: RejectInvalid, calls: 1},
		{name: "caller", values: []string{"caller"}, policy: RejectInvalid},
		{name: "inherited", values: []string{"other"}, inherited: "upstream", policy: RejectInvalid},
		{name: "duplicate", values: []string{"a", "b"}, policy: RejectInvalid, code: codes.InvalidArgument},
		{name: "replace", values: []string{"bad\n"}, policy: ReplaceInvalid, calls: 1},
		{name: "failure", policy: RejectInvalid, fail: true, code: codes.Internal, calls: 1},
		{name: "bad inherited", inherited: "bad\n", policy: ReplaceInvalid, code: codes.Internal},
		{name: "invalid generated", policy: RejectInvalid, invalidGenerated: true, code: codes.Internal, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := 0
			interceptor, err := RequestIDUnaryInterceptor(RequestIDOptions{MetadataKey: "correlation", ReturnHeader: true, InvalidInput: tc.policy, Validate: func(string) error { return nil }, Generate: func() (string, error) {
				count++
				if tc.invalidGenerated {
					return "bad\n", nil
				}
				if tc.fail {
					return "", errors.New("secret")
				}
				return "generated", nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			stream := &testStream{}
			ctx := grpc.NewContextWithServerTransportStream(context.Background(), stream)
			ctx = metadata.NewIncomingContext(ctx, metadata.MD{"correlation": tc.values})
			ctx = correlation.WithRequestID(ctx, tc.inherited)
			called := false
			_, err = interceptor(ctx, nil, nil, func(ctx context.Context, req any) (any, error) {
				return interceptor(ctx, req, nil, func(ctx context.Context, _ any) (any, error) {
					called = true
					id, ok := correlation.RequestIDFromContext(ctx)
					headers := stream.headers.Get("correlation")
					if !ok || len(headers) != 1 || headers[0] != id {
						t.Fatal("ID/header mismatch", headers)
					}
					return "result", nil
				})
			})
			if status.Code(err) != tc.code || count != tc.calls || called != (tc.code == codes.OK) {
				t.Fatalf("err=%v calls=%d called=%v", err, count, called)
			}
			if err != nil && status.Convert(err).Message() == "secret" {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestUnaryConfigurationAndCancellation(t *testing.T) {
	opts := RequestIDOptions{MetadataKey: "id", InvalidInput: RejectInvalid, Validate: func(string) error { return nil }, Generate: func() (string, error) { t.Fatal("generated after cancellation"); return "", nil }}
	for _, key := range []string{"", "grpc-id", "id-bin", "UPPER", "bad key"} {
		invalid := opts
		invalid.MetadataKey = key
		if _, err := RequestIDUnaryInterceptor(invalid); err == nil {
			t.Fatal("invalid key accepted", key)
		}
	}
	interceptor, err := RequestIDUnaryInterceptor(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = interceptor(ctx, nil, nil, func(context.Context, any) (any, error) { t.Fatal("handler called"); return nil, nil })
	if status.Code(err) != codes.Canceled {
		t.Fatal(err)
	}
}
