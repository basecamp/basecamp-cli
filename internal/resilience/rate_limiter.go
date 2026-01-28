package resilience

import (
	"time"
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
	var allowed bool

	err := rl.store.Update(func(state *State) error {
		rlState := &state.RateLimiter
		now := rl.now()

		// Check Retry-After block
		if rlState.IsBlocked() {
			allowed = false
			return nil
		}

		rl.refill(rlState, now)

		if rlState.Tokens >= rl.config.TokensPerRequest {
			rlState.Tokens -= rl.config.TokensPerRequest
			allowed = true
		} else {
			allowed = false
		}

		state.UpdatedAt = now
		return nil
	})

	if err != nil {
		// On error, allow the request (fail open)
		return true, nil //nolint:nilerr // Intentional fail-open: allow request when state cannot be updated
	}

	return allowed, nil
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
