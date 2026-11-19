// Package messaging provides broker-neutral message processing adapters.
package messaging

import (
	"context"
	"errors"
	"fmt"

	"github.com/AR-IMMS/fleet-manager/internal/shared/correlation"
)

// Handler processes one message; the broker integration owns acknowledgement.
type Handler[T any] func(context.Context, T) error

// InvalidIDPolicy explicitly selects behavior for invalid or repeated input IDs.
type InvalidIDPolicy string

const (
	// RejectInvalid rejects ambiguous or invalid incoming correlation values.
	RejectInvalid InvalidIDPolicy = "reject"
	// ReplaceInvalid generates a new ID for invalid incoming correlation values.
	ReplaceInvalid InvalidIDPolicy = "replace"
)

var (
	// ErrInvalidRequestID reports invalid incoming correlation without exposing values.
	ErrInvalidRequestID = errors.New("invalid message request ID")
	// ErrCorrelation reports internal extraction, inherited-ID or generation failure.
	ErrCorrelation = errors.New("message correlation failed")
)

// RequestIDOptions supplies broker-specific policy. Callbacks must be concurrency-safe.
type RequestIDOptions[T any] struct {
	Extract      func(context.Context, T) ([]string, error)
	Validate     func(string) error
	Generate     func() (string, error)
	InvalidInput InvalidIDPolicy
}

// RequestIDMiddleware wraps a handler without publishing or acknowledging messages.
func RequestIDMiddleware[T any](opts RequestIDOptions[T], next Handler[T]) (Handler[T], error) {
	if opts.Extract == nil || opts.Validate == nil || opts.Generate == nil || next == nil {
		return nil, fmt.Errorf("message correlation callbacks and handler are required")
	}
	if opts.InvalidInput != RejectInvalid && opts.InvalidInput != ReplaceInvalid {
		return nil, fmt.Errorf("message invalid-input policy must be explicit")
	}
	valid := func(id string) bool { return id != "" && opts.Validate(id) == nil }
	return func(ctx context.Context, msg T) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		id, exists := correlation.RequestIDFromContext(ctx)
		if exists {
			if !valid(id) {
				return ErrCorrelation
			}
		} else {
			values, err := opts.Extract(ctx, msg)
			if err != nil {
				return ErrCorrelation
			}
			if len(values) == 1 && valid(values[0]) {
				id = values[0]
			} else {
				if len(values) != 0 && opts.InvalidInput == RejectInvalid {
					return ErrInvalidRequestID
				}
				id, err = opts.Generate()
				if err != nil || !valid(id) {
					return ErrCorrelation
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return next(correlation.WithRequestID(ctx, id), msg)
	}, nil
}
