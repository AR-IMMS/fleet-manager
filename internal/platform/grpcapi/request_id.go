// Package grpcapi provides configurable gRPC transport adapters.
package grpcapi

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/AR-IMMS/fleet-manager/internal/shared/correlation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// InvalidIDPolicy defines how invalid or repeated incoming IDs are handled.
type InvalidIDPolicy string

const (
	// RejectInvalid returns InvalidArgument for invalid incoming values.
	RejectInvalid InvalidIDPolicy = "reject"
	// ReplaceInvalid generates a new ID for invalid incoming values.
	ReplaceInvalid InvalidIDPolicy = "replace"
)

// RequestIDOptions defines adapter policy; callbacks must be concurrency-safe.
type RequestIDOptions struct {
	MetadataKey  string
	Validate     func(string) error
	Generate     func() (string, error)
	InvalidInput InvalidIDPolicy
	ReturnHeader bool
}

// RequestIDUnaryInterceptor selects one ID for a unary call and its response metadata.
func RequestIDUnaryInterceptor(opts RequestIDOptions) (grpc.UnaryServerInterceptor, error) {
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`).MatchString(opts.MetadataKey) || strings.HasPrefix(opts.MetadataKey, "grpc-") || strings.HasSuffix(opts.MetadataKey, "-bin") {
		return nil, fmt.Errorf("request ID metadata key must be a nonreserved lowercase text key")
	}
	if opts.Validate == nil || opts.Generate == nil {
		return nil, fmt.Errorf("request ID validation and generation callbacks are required")
	}
	if opts.InvalidInput != RejectInvalid && opts.InvalidInput != ReplaceInvalid {
		return nil, fmt.Errorf("gRPC invalid-input policy must be explicit")
	}
	valid := func(id string) bool {
		if id == "" {
			return false
		}
		for _, c := range id {
			if c < 0x20 || c > 0x7e {
				return false
			}
		}
		return opts.Validate(id) == nil
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		id, exists := correlation.RequestIDFromContext(ctx)
		if exists {
			if !valid(id) {
				return nil, status.Error(codes.Internal, "request correlation failed")
			}
		} else {
			md, _ := metadata.FromIncomingContext(ctx)
			values := md.Get(opts.MetadataKey)
			if len(values) == 1 && valid(values[0]) {
				id = values[0]
			} else {
				if len(values) != 0 && opts.InvalidInput == RejectInvalid {
					return nil, status.Error(codes.InvalidArgument, "invalid request ID")
				}
				var err error
				id, err = opts.Generate()
				if err != nil || !valid(id) {
					return nil, status.Error(codes.Internal, "request correlation failed")
				}
			}
		}
		ctx = correlation.WithRequestID(ctx, id)
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		// An outer copy of this interceptor already queues the same response header.
		if opts.ReturnHeader && ctx.Value(responseKey{opts.MetadataKey}) != id {
			if err := grpc.SetHeader(ctx, metadata.Pairs(opts.MetadataKey, id)); err != nil {
				return nil, status.Error(codes.Internal, "request correlation failed")
			}
			ctx = context.WithValue(ctx, responseKey{opts.MetadataKey}, id)
		}
		return next(ctx, req)
	}, nil
}

type responseKey struct{ name string }
