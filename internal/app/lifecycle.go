package app

import (
	"context"
	"errors"
	"fmt"
)

// shutdownStep identifies one acquired resource and its cleanup operation.
//
// Cleanup functions must honor context cancellation and return safe errors
// that do not expose credentials or private connection information.
type shutdownStep struct {
	name string
	stop func(context.Context) error
}

// lifecycle tracks cleanup for resources acquired during one Run invocation.
//
// It is owned by a single orchestration goroutine and is not concurrency-safe.
// The zero value is ready for use.
type lifecycle struct {
	steps []shutdownStep
}

// add registers cleanup immediately after successful resource acquisition.
//
// Callers must supply a nonempty name and a nonnil cleanup function.
func (l *lifecycle) add(name string, stop func(context.Context) error) {
	l.steps = append(l.steps, shutdownStep{
		name: name,
		stop: stop,
	})
}

// stopAll releases acquired resources in reverse registration order.
//
// Every cleanup is attempted, including after an earlier failure or deadline.
// All steps share the caller's deadline; this method does not grant additional
// timeout budgets or launch detached cleanup goroutines.
//
// Registered steps are consumed so repeated calls do not close them again.
func (l *lifecycle) stopAll(ctx context.Context) error {
	steps := l.steps
	l.steps = nil

	var failures []error

	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]

		if err := step.stop(ctx); err != nil {
			failures = append(
				failures,
				fmt.Errorf("stop %s: %w", step.name, err),
			)
		}
	}

	return errors.Join(failures...)
}
