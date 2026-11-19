package messaging

import (
	"context"
	"errors"
	"testing"

	"github.com/AR-IMMS/fleet-manager/internal/shared/correlation"
)

func TestMessageCorrelation(t *testing.T) {
	for _, tc := range []struct {
		name             string
		values           []string
		inherited        string
		policy           InvalidIDPolicy
		generatorError   bool
		invalidGenerated bool
		extractError     bool
		want             error
		calls            int
	}{
		{name: "generate", policy: RejectInvalid, calls: 1},
		{name: "incoming", values: []string{"caller"}, policy: RejectInvalid},
		{name: "inherited", values: []string{"conflict"}, inherited: "upstream", policy: RejectInvalid},
		{name: "duplicate", values: []string{"a", "b"}, policy: RejectInvalid, want: ErrInvalidRequestID},
		{name: "replace", values: []string{"bad"}, policy: ReplaceInvalid, calls: 1},
		{name: "generator failure", policy: RejectInvalid, generatorError: true, want: ErrCorrelation, calls: 1},
		{name: "invalid inherited", inherited: "bad", policy: ReplaceInvalid, want: ErrCorrelation},
		{name: "invalid generated", policy: RejectInvalid, invalidGenerated: true, want: ErrCorrelation, calls: 1},
		{name: "extract failure", policy: RejectInvalid, extractError: true, want: ErrCorrelation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count, handled := 0, false
			opts := RequestIDOptions[string]{InvalidInput: tc.policy,
				Extract: func(context.Context, string) ([]string, error) {
					if tc.extractError {
						return nil, errors.New("secret")
					}
					return tc.values, nil
				},
				Validate: func(id string) error {
					if id == "bad" {
						return errors.New("secret")
					}
					return nil
				},
				Generate: func() (string, error) {
					count++
					if tc.invalidGenerated {
						return "bad", nil
					}
					if tc.generatorError {
						return "", errors.New("secret")
					}
					return "generated", nil
				},
			}
			inner, err := RequestIDMiddleware(opts, func(ctx context.Context, msg string) error {
				handled = true
				if _, ok := correlation.RequestIDFromContext(ctx); !ok {
					t.Fatal("missing ID")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			outer, err := RequestIDMiddleware(opts, inner)
			if err != nil {
				t.Fatal(err)
			}
			err = outer(correlation.WithRequestID(context.Background(), tc.inherited), "message")
			if !errors.Is(err, tc.want) || count != tc.calls || handled != (tc.want == nil) {
				t.Fatalf("err=%v calls=%d handled=%v", err, count, handled)
			}
		})
	}
}

func TestMessageConstructorAndCancellation(t *testing.T) {
	if _, err := RequestIDMiddleware(RequestIDOptions[string]{}, nil); err == nil {
		t.Fatal("invalid configuration accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opts := RequestIDOptions[string]{InvalidInput: RejectInvalid, Extract: func(context.Context, string) ([]string, error) {
		t.Fatal("extracted after cancellation")
		return nil, nil
	}, Validate: func(string) error { return nil }, Generate: func() (string, error) { return "id", nil }}
	handler, err := RequestIDMiddleware(opts, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(handler(ctx, ""), context.Canceled) {
		t.Fatal("cancellation lost")
	}
}
