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
	}
}

// now returns the current time.
func (rl *RateLimiter) now() time.Time {
	return time.Now()
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

		// Check Retry-After block
		if rlState.IsBlocked() {
			allowed, blocked, wait = false, true, rlState.BlockedFor()
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

// Wait consumes tokens for one request, sleeping for refills or for a
// Retry-After block to lift, until deadline. It returns nil when the request
// may proceed, a *GateError when the deadline would pass first, or ctx.Err().
// A Retry-After block that outlasts the deadline is reported immediately
// rather than waited on, with the remaining time in the message.
// Cancellation and the deadline are checked before every attempt, so an
// expired or canceled gate consumes nothing, and cancellation outranks the
// deadline.
func (rl *RateLimiter) Wait(ctx context.Context, deadline time.Time) error {
	return rl.waitSince(ctx, rl.now(), deadline)
}

// waitSince is Wait for a gate that started queueing at start.
func (rl *RateLimiter) waitSince(ctx context.Context, start, deadline time.Time) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining := deadline.Sub(rl.now())
		if remaining <= 0 {
			return rl.gateError(false, 0, rl.now().Sub(start))
		}
		allowed, wait, blocked := rl.take() //nolint:contextcheck // lock acquisition is context-independent by design
		if allowed {
			return nil
		}
		if wait > remaining {
			return rl.gateError(blocked, wait, rl.now().Sub(start))
		}
		// Jitter never pushes the sleep to the deadline: a wake there would be
		// rejected unheard after having waited out the very refill or block
		// it was waiting for.
		sleep := jittered(max(wait, minRefillWait))
		if sleep > remaining {
			sleep = wait
		}
		if err := pause(ctx, sleep); err != nil {
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
