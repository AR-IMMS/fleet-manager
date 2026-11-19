package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// ErrNotReady means the application cannot currently accept useful work.
var ErrNotReady = errors.New("application is not ready")

// ReadinessCheck provides one named dependency check.
//
// Name is a stable diagnostic label, not a connection string.
// Check must honor the supplied context and avoid detached work.
type ReadinessCheck struct {
	Name  string
	Check func(context.Context) error
}

// FleetReadiness combines dependency availability with application drain state.
//
// Checks are fixed at construction and evaluated sequentially within the
// caller's overall deadline. Draining is irreversible.
type FleetReadiness struct {
	checks   []ReadinessCheck
	draining atomic.Bool
}

// NewFleetReadiness validates and copies the supplied dependency checks.
//
// At least one check is required so incomplete wiring cannot accidentally
// report readiness. Check names must be unique.
func NewFleetReadiness(
	checks ...ReadinessCheck,
) (*FleetReadiness, error) {
	if len(checks) == 0 {
		return nil, fmt.Errorf("at least one readiness check is required")
	}

	names := make(map[string]struct{}, len(checks))

	for _, check := range checks {
		if strings.TrimSpace(check.Name) == "" {
			return nil, fmt.Errorf("readiness check name is required")
		}

		if check.Check == nil {
			return nil, fmt.Errorf("readiness check function is required")
		}

		if _, exists := names[check.Name]; exists {
			return nil, fmt.Errorf("readiness check names must be unique")
		}

		names[check.Name] = struct{}{}
	}

	return &FleetReadiness{
		checks: append([]ReadinessCheck(nil), checks...),
	}, nil
}

// Check verifies dependencies while the application is accepting work.
//
// Dependency error details are intentionally hidden. Cancellation is checked
// before and after dependency calls to reject late successful results.
func (r *FleetReadiness) Check(ctx context.Context) error {
	if err := r.checkState(ctx); err != nil {
		return err
	}

	for _, check := range r.checks {
		if err := check.Check(ctx); err != nil {
			if stateErr := r.checkState(ctx); stateErr != nil {
				return stateErr
			}

			return ErrNotReady
		}

		if err := r.checkState(ctx); err != nil {
			return err
		}
	}

	return nil
}

// BeginDrain prevents subsequent checks from reporting readiness.
//
// Call this before stopping listeners, workers, or infrastructure resources.
func (r *FleetReadiness) BeginDrain() {
	r.draining.Store(true)
}

// checkState rejects cancellation or draining without contacting dependencies.
func (r *FleetReadiness) checkState(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if r.draining.Load() {
		return ErrNotReady
	}

	return nil
}
