package resilience

import (
	"context"
	"math/rand/v2"
	"time"
)

// DefaultMaxWait is how long OnOperationGate queues for a token or a slot
// before giving up. It is long enough to absorb a burst of parallel CLI
// invocations (10 workers draining a 50-token bucket refill at 10/s in well
// under a second) and short enough that a genuinely saturated machine still
// fails within one attention span.
const DefaultMaxWait = 10 * time.Second

// GateError is a gate rejection that already waited its turn. It unwraps to
// the SDK sentinel (basecamp.ErrRateLimited or basecamp.ErrBulkheadFull) so
// existing errors.Is checks keep working, and carries the message and hint
// the CLI shows: which limit, how long it waited, and what to do.
type GateError struct {
	Message  string
	Hint     string
	sentinel error
}

func (e *GateError) Error() string { return e.Message }

// Unwrap returns the SDK sentinel the rejection stands for.
func (e *GateError) Unwrap() error { return e.sentinel }

// pause sleeps for d or until ctx is done, whichever comes first.
func pause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// jittered stretches d by up to half again so that processes which woke
// together do not retry in lockstep and starve each other at the lock.
func jittered(d time.Duration) time.Duration {
	return d + rand.N(d/2+1) //nolint:gosec // jitter spreads retries; it guards nothing
}
