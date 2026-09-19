package resilience

import (
	"context"
	"fmt"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// RateLimiter implements the token bucket algorithm with cross-process persistence.
type RateLimiter struct {
	config RateLimiterConfig
	store  *Store
	// clock is the time the limiter reads. A field so that a test can put
	// the deadline boundary where it wants it instead of racing a real one.
	clock func() time.Time
	// onWait, when set, is called each time a caller is turned away by the
	// bucket and settles in to sleep for a refill or a block. A test that
	// wants to know the wait path was taken has to hear it from in here: a
	// look at the bucket from outside can be overtaken by a refill landing
	// between the look and the take, and would report a wait that never
	// happened. Nothing in production sets it.
	onWait func()
}

// NewRateLimiter creates a new rate limiter with the given config.
func NewRateLimiter(store *Store, config RateLimiterConfig) *RateLimiter {
	// Apply defaults for zero values
	if config.MaxTokens <= 0 {
		config.MaxTokens = 50
	}
	if config.RefillRate <= 0 {
		config.RefillRate = 10
	}
	if config.TokensPerRequest <= 0 {
		config.TokensPerRequest = 1
	}

	return &RateLimiter{
		config: config,
		store:  store,
		clock:  time.Now,
	}
}

// now returns the current time.
func (rl *RateLimiter) now() time.Time {
	return rl.clock()
}

// refill adds tokens based on elapsed time since last refill.
// Accepts `now` to ensure consistent timestamps within a transaction.
func (rl *RateLimiter) refill(state *RateLimiterState, now time.Time) {
	// Initialize if first access (LastRefillAt is zero)
	if state.LastRefillAt.IsZero() {
		state.Tokens = rl.config.MaxTokens
		state.LastRefillAt = now
		return
	}

	elapsed := now.Sub(state.LastRefillAt)
	state.LastRefillAt = now

	// Add tokens based on elapsed time
	tokensToAdd := elapsed.Seconds() * rl.config.RefillRate
	state.Tokens += tokensToAdd

	// Cap at max tokens
	if state.Tokens > rl.config.MaxTokens {
		state.Tokens = rl.config.MaxTokens
	}
}

// Allow checks if a request is allowed.
// Returns true if the request can proceed, false if it should be rejected.
// On success, consumes tokens from the bucket.
func (rl *RateLimiter) Allow() (bool, error) {
	allowed, _, _ := rl.take()
	return allowed, nil
}

// take is one non-blocking pass through the bucket. When the request is not
// allowed, wait is how long until it could be: the remainder of a Retry-After
// block (blocked is true) or the refill time for the missing tokens. A store
// error allows the request (fail open), as every primitive here does.
func (rl *RateLimiter) take() (allowed bool, wait time.Duration, blocked bool) {
	err := rl.store.Update(func(state *State) error {
		rlState := &state.RateLimiter
		now := rl.now()

		// Check Retry-After block, against the transaction's own now the
		// way the refill below is, and not against a second reading of the
		// clock inside the state.
		if blockEnds := rlState.RetryAfterUntil; blockEnds.After(now) {
			allowed, blocked, wait = false, true, blockEnds.Sub(now)
			return nil
		}

		rl.refill(rlState, now)

		if rlState.Tokens >= rl.config.TokensPerRequest {
			rlState.Tokens -= rl.config.TokensPerRequest
			allowed = true
		} else {
			deficit := rl.config.TokensPerRequest - rlState.Tokens
			allowed, wait = false, time.Duration(deficit/rl.config.RefillRate*float64(time.Second))
		}

		state.UpdatedAt = now
		return nil
	})

	if err != nil {
		return true, 0, false
	}

	return allowed, wait, blocked
}

// minRefillWait floors the sleep between token attempts: a deficit of a few
// microseconds is not worth a wakeup, and under contention the refill is
// consumed by whichever process reaches the lock first anyway.
const minRefillWait = 5 * time.Millisecond

// sleepWithin is how long to wait before the next attempt at the bucket:
// wait, floored at minRefillWait and jittered so that processes which woke
// together do not retry in lockstep. Jitter never pushes the sleep past the
// remaining budget — a wake there would be rejected unheard after having
// waited out the very refill or block it was waiting for — so when the
// jittered sleep would not fit, the bare wait is used.
func sleepWithin(wait, remaining time.Duration) time.Duration {
	sleep := jittered(max(wait, minRefillWait))
	if sleep > remaining {
		return wait
	}
	return sleep
}

// Wait consumes tokens for one request, sleeping for refills or for a
// Retry-After block to lift, until deadline. It returns nil when the request
// may proceed, a *GateError when the deadline would pass first, or ctx.Err().
// A Retry-After block that outlasts the deadline is reported immediately
// rather than waited on, with the remaining time in the message, and a
// budget spent queueing names the limit that held the gate: the server's
// block, or our own bucket.
// Cancellation and the deadline are checked before every attempt, so an
// expired or canceled gate consumes nothing, and cancellation outranks the
// deadline.
func (rl *RateLimiter) Wait(ctx context.Context, deadline time.Time) error {
	return rl.waitSince(ctx, rl.now(), deadline)
}

// waitSince is Wait for a gate that started queueing at start. It carries
// the cause of the sleep it is in, so that a budget which runs out can name
// what it was spent on.
func (rl *RateLimiter) waitSince(ctx context.Context, start, deadline time.Time) error {
	sleptOnServerBlock := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining := deadline.Sub(rl.now())
		if remaining <= 0 {
			return rl.budgetError(sleptOnServerBlock, rl.now().Sub(start))
		}
		allowed, wait, blocked := rl.take() //nolint:contextcheck // lock acquisition is context-independent by design
		if allowed {
			return nil
		}
		if wait > remaining {
			return rl.gateError(blocked, wait, rl.now().Sub(start))
		}
		sleptOnServerBlock = blocked
		if rl.onWait != nil {
			rl.onWait()
		}
		if err := pause(ctx, sleepWithin(wait, remaining)); err != nil {
			return err
		}
	}
}

func (rl *RateLimiter) gateError(blocked bool, wait, waited time.Duration) *GateError {
	if blocked {
		// Rounded up: a "retry after 40s" that is really 40.4s would send the
		// re-run into the tail of the block.
		retryAfter := ceilSeconds(wait)
		return &GateError{
			Message:  fmt.Sprintf("Rate limited by the server; retry after %s", retryAfter),
			Hint:     fmt.Sprintf("Wait %s, then re-run.", retryAfter),
			sentinel: basecamp.ErrRateLimited,
		}
	}
	return rl.clientLimitError(waited)
}

// budgetError is the rejection for a gate whose budget ran out while it was
// still sleeping, and sleptOnServerBlock is what that last sleep was for, as
// take saw it. A server Retry-After is the server's doing and nobody's
// parallelism; anything else is our own bucket.
//
// The cause is carried out of the sleep rather than read back from the store
// here, because the store is shared and by now says something else: another
// invocation's 429 can land while this gate sleeps for a refill of its own,
// and a block this gate never waited on can expire between the two. Either
// one would hand a client-side timeout the server's name.
func (rl *RateLimiter) budgetError(sleptOnServerBlock bool, waited time.Duration) *GateError {
	if !sleptOnServerBlock {
		return rl.clientLimitError(waited)
	}
	return &GateError{
		Message:  fmt.Sprintf("Rate limited by the server; waited %s", waited.Round(time.Second)),
		Hint:     "Re-run.",
		sentinel: basecamp.ErrRateLimited,
	}
}

// clientLimitError is the rejection for a wait our own token bucket imposed,
// reported as the request rate the bucket allows.
func (rl *RateLimiter) clientLimitError(waited time.Duration) *GateError {
	requestsPerSecond := rl.config.RefillRate / rl.config.TokensPerRequest
	return &GateError{
		Message:  fmt.Sprintf("Too many requests (client limit %g/s); waited %s", requestsPerSecond, waited.Round(time.Second)),
		Hint:     "Re-run, or lower parallelism.",
		sentinel: basecamp.ErrRateLimited,
	}
}

// SetRetryAfter sets a block until the given time due to a 429 response.
func (rl *RateLimiter) SetRetryAfter(until time.Time) error {
	return rl.store.Update(func(state *State) error {
		// Only update if the new time is later than the current block
		if until.After(state.RateLimiter.RetryAfterUntil) {
			state.RateLimiter.RetryAfterUntil = until
			state.UpdatedAt = rl.now()
		}
		return nil
	})
}

// SetRetryAfterDuration sets a block for the given duration.
func (rl *RateLimiter) SetRetryAfterDuration(d time.Duration) error {
	return rl.SetRetryAfter(rl.now().Add(d))
}

// Tokens returns the current number of available tokens.
// This also persists any initialization or refill that occurs.
func (rl *RateLimiter) Tokens() (float64, error) {
	var tokens float64

	err := rl.store.Update(func(state *State) error {
		now := rl.now()

		// Capture previous values to detect changes
		prevTokens := state.RateLimiter.Tokens
		prevLastRefillAt := state.RateLimiter.LastRefillAt

		rl.refill(&state.RateLimiter, now)
		tokens = state.RateLimiter.Tokens

		// Update timestamp if state changed
		if state.RateLimiter.Tokens != prevTokens ||
			!state.RateLimiter.LastRefillAt.Equal(prevLastRefillAt) {
			state.UpdatedAt = now
		}
		return nil
	})

	if err != nil {
		return 0, err
	}

	return tokens, nil
}

// RetryAfterRemaining returns the remaining duration of the Retry-After block,
// or 0 if there is no active block.
func (rl *RateLimiter) RetryAfterRemaining() (time.Duration, error) {
	state, err := rl.store.Load()
	if err != nil {
		return 0, err
	}

	return state.RateLimiter.BlockedFor(), nil
}

// Reset resets the rate limiter to a full bucket.
func (rl *RateLimiter) Reset() error {
	return rl.store.Update(func(state *State) error {
		state.RateLimiter = RateLimiterState{
			Tokens:       rl.config.MaxTokens,
			LastRefillAt: rl.now(),
		}
		state.UpdatedAt = rl.now()
		return nil
	})
}
