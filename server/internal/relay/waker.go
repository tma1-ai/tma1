package relay

import (
	"context"
	"errors"
)

// Waker delivers a wake prompt to a target's terminal. Implementations
// declare via CanWake whether they can reach a given target; the
// Registry tries them in reliability order.
type Waker interface {
	Name() string
	CanWake(t Target) bool
	Wake(ctx context.Context, t Target, prompt string) error
}

var (
	errNoWaker     = errors.New("no waker can reach the target")
	errNoWorkerBin = errors.New("no agent binary resolved for worker fallback")
	// errTargetBusy means the target terminal is reachable but actively
	// processing. WakeWith stops on it (does NOT fall through to a less
	// precise waker) so we never spawn a duplicate worker for a terminal
	// that's simply busy.
	errTargetBusy = errors.New("target terminal is busy")
	// errSessionNotFound means the recorded terminal id no longer maps to
	// a live session (closed / reused / forged). WakeWith falls through.
	errSessionNotFound = errors.New("terminal session not found")
)

// Registry holds wakers in reliability order (most reliable first, the
// universal worker fallback last) and dispatches to the first one that
// can reach the target.
type Registry struct {
	wakers []Waker
}

func NewRegistry(wakers ...Waker) *Registry {
	return &Registry{wakers: wakers}
}

// CanWake reports whether any registered waker can reach the target.
func (r *Registry) CanWake(t Target) bool {
	for _, w := range r.wakers {
		if w.CanWake(t) {
			return true
		}
	}
	return false
}

// WakeWith tries each applicable waker in order; the first whose Wake
// succeeds wins and its Name is returned. A waker that errors falls
// through to the next. Returns an error only when no waker applied or
// every applicable one failed.
func (r *Registry) WakeWith(ctx context.Context, t Target, prompt string) (string, error) {
	var lastErr error
	applied := false
	for _, w := range r.wakers {
		if !w.CanWake(t) {
			continue
		}
		applied = true
		if err := w.Wake(ctx, t, prompt); err != nil {
			// A busy terminal is reachable — don't fall through to a worker
			// and duplicate the agent. Surface it as-is.
			if errors.Is(err, errTargetBusy) {
				return "", err
			}
			lastErr = err
			continue
		}
		return w.Name(), nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	if !applied {
		return "", errNoWaker
	}
	return "", errNoWaker
}
