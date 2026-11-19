package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestLifecycleStopsInReverseOrderAndContinuesAfterFailure verifies that one
// cleanup failure does not prevent remaining resources from being released.
func TestLifecycleStopsInReverseOrderAndContinuesAfterFailure(t *testing.T) {
	var resources lifecycle
	var order []string
	expectedFailure := errors.New("listener cleanup failed")

	resources.add("storage", func(context.Context) error {
		order = append(order, "storage")
		return nil
	})

	resources.add("listener", func(context.Context) error {
		order = append(order, "listener")
		return expectedFailure
	})

	err := resources.stopAll(context.Background())

	if !errors.Is(err, expectedFailure) {
		t.Fatalf("expected cleanup failure, got %v", err)
	}

	if !reflect.DeepEqual(order, []string{"listener", "storage"}) {
		t.Fatalf("unexpected cleanup order: %v", order)
	}

	if err := resources.stopAll(context.Background()); err != nil {
		t.Fatalf("repeated cleanup: %v", err)
	}

	if len(order) != 2 {
		t.Fatal("cleanup steps ran more than once")
	}
}

// TestLifecyclePreservesSharedDeadline verifies that cleanup steps receive
// the original total shutdown budget rather than fresh per-step timeouts.
func TestLifecyclePreservesSharedDeadline(t *testing.T) {
	var resources lifecycle

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	expectedDeadline, _ := ctx.Deadline()

	for _, name := range []string{"storage", "listener"} {
		resources.add(name, func(stepCtx context.Context) error {
			deadline, ok := stepCtx.Deadline()
			if !ok || !deadline.Equal(expectedDeadline) {
				t.Fatal("cleanup deadline changed")
			}
			return nil
		})
	}

	if err := resources.stopAll(ctx); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}
