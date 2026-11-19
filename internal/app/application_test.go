package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

// applicationTestConfig loads production defaults without process env access.
func applicationTestConfig(t *testing.T) Config {
	t.Helper()

	cfg, err := LoadConfig(func(string) (string, bool) {
		return "", false
	})
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}

	return cfg
}

// applicationTestLogger keeps lifecycle tests independent of console output.
func applicationTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestNewApplicationRejectsMissingLogger verifies mandatory dependency wiring.
func TestNewApplicationRejectsMissingLogger(t *testing.T) {
	application, err := NewApplication(applicationTestConfig(t), nil)
	if err == nil || application != nil {
		t.Fatal("expected missing logger to reject construction")
	}
}

// TestNewApplicationRejectsInvalidConfig prevents deferred startup failures.
func TestNewApplicationRejectsInvalidConfig(t *testing.T) {
	cfg := applicationTestConfig(t)
	cfg.ShutdownTimeout = 0

	application, err := NewApplication(cfg, applicationTestLogger())
	if err == nil || application != nil {
		t.Fatal("expected invalid configuration to reject construction")
	}
}

// TestApplicationRejectsCancelledStartup verifies cancellation before DB access.
func TestApplicationRejectsCancelledStartup(t *testing.T) {
	application, err := NewApplication(
		applicationTestConfig(t),
		applicationTestLogger(),
	)
	if err != nil {
		t.Fatalf("construct application: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := application.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}

	if err := application.Run(ctx); !errors.Is(err, ErrAlreadyRun) {
		t.Fatalf("expected lifecycle reuse rejection, got %v", err)
	}
}
