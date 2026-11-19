package app

import (
	"context"
	"errors"
	"testing"
)

// TestFleetReadinessRejectsInvalidChecks verifies mandatory and unique inputs.
func TestFleetReadinessRejectsInvalidChecks(t *testing.T) {
	valid := ReadinessCheck{
		Name: "storage",
		Check: func(context.Context) error {
			return nil
		},
	}

	cases := []struct {
		name   string
		checks []ReadinessCheck
	}{
		{name: "missing checks"},
		{
			name: "missing name",
			checks: []ReadinessCheck{
				{Check: valid.Check},
			},
		},
		{
			name: "missing function",
			checks: []ReadinessCheck{
				{Name: "storage"},
			},
		},
		{
			name:   "duplicate names",
			checks: []ReadinessCheck{valid, valid},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewFleetReadiness(tc.checks...); err == nil {
				t.Fatal("expected invalid checks to fail")
			}
		})
	}
}

// TestFleetReadinessHidesDependencyFailure prevents leaking adapter errors.
func TestFleetReadinessHidesDependencyFailure(t *testing.T) {
	privateError := errors.New("private connection information")

	readiness, err := NewFleetReadiness(ReadinessCheck{
		Name: "storage",
		Check: func(context.Context) error {
			return privateError
		},
	})
	if err != nil {
		t.Fatalf("construct readiness: %v", err)
	}

	err = readiness.Check(context.Background())
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("expected not ready, got %v", err)
	}

	if errors.Is(err, privateError) {
		t.Fatal("dependency error escaped readiness boundary")
	}
}

// TestFleetReadinessDrainDuringCheck rejects late successful results.
func TestFleetReadinessDrainDuringCheck(t *testing.T) {
	var readiness *FleetReadiness

	readiness, err := NewFleetReadiness(ReadinessCheck{
		Name: "storage",
		Check: func(context.Context) error {
			readiness.BeginDrain()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("construct readiness: %v", err)
	}

	if err := readiness.Check(context.Background()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("expected not ready, got %v", err)
	}
}

// TestFleetReadinessUsesConfigurationSnapshot prevents caller slice mutation
// from changing checks after construction.
func TestFleetReadinessUsesConfigurationSnapshot(t *testing.T) {
	checks := []ReadinessCheck{
		{
			Name: "storage",
			Check: func(context.Context) error {
				return nil
			},
		},
	}

	readiness, err := NewFleetReadiness(checks...)
	if err != nil {
		t.Fatalf("construct readiness: %v", err)
	}

	checks[0].Check = func(context.Context) error {
		t.Fatal("caller mutation changed readiness checks")
		return ErrNotReady
	}

	if err := readiness.Check(context.Background()); err != nil {
		t.Fatalf("expected ready, got %v", err)
	}
}

// TestFleetReadinessCancelledContextSkipsChecks avoids work after cancellation.
func TestFleetReadinessCancelledContextSkipsChecks(t *testing.T) {
	readiness, err := NewFleetReadiness(ReadinessCheck{
		Name: "storage",
		Check: func(context.Context) error {
			t.Fatal("check must not run after cancellation")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("construct readiness: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := readiness.Check(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}
