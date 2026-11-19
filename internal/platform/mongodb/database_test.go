package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"
)

func validTestOptions() Options {
	return Options{
		URI:              "mongodb://127.0.0.1:27017",
		Database:         "ar_imms_fleet",
		AppName:          "ar-imms-fleet-manager",
		ConnectTimeout:   2 * time.Second,
		OperationTimeout: 2 * time.Second,
	}
}

func TestOpen_RejectsInvalidOptions(t *testing.T) {
	opts := validTestOptions()
	opts.URI = ""

	_, err := Open(context.Background(), opts)
	if err == nil {
		t.Fatal("expected error for invalid options, got nil")
	}
}

func TestOpen_CancelledContextReturnsUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Open(ctx, validTestOptions())
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected wrapped context.Canceled, got %v", err)
	}
}

func TestSafeError_Classification(t *testing.T) {
	t.Run("preserves deadline exceeded", func(t *testing.T) {
		err := safeError(ErrUnavailable, context.Background(), context.DeadlineExceeded)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("expected ErrUnavailable, got %v", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected DeadlineExceeded, got %v", err)
		}
	})

	t.Run("preserves context error when cause is generic", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := safeError(ErrClose, ctx, errors.New("raw driver error"))
		if !errors.Is(err, ErrClose) {
			t.Fatalf("expected ErrClose, got %v", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected Canceled from ctx, got %v", err)
		}
	})

	t.Run("hides raw driver error when not context cancellation", func(t *testing.T) {
		rawErr := errors.New("secret connection string leaked")
		err := safeError(ErrUnavailable, context.Background(), rawErr)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("expected ErrUnavailable, got %v", err)
		}
		if errors.Is(err, rawErr) {
			t.Fatal("expected raw driver error not to be exposed")
		}
	})
}
