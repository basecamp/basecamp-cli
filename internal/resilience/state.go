package resilience

import (
	"slices"
	"time"
)

const (
	// StateVersion is the current state schema version.
	StateVersion = 1
)

// State represents the persisted resilience state shared across CLI processes.
// This enables circuit breaker, rate limiter, and bulkhead patterns to
// coordinate across concurrent bcq invocations.
type State struct {
	// Version is the schema version for future migrations.
	Version int `json:"version"`

	// CircuitBreaker tracks the circuit breaker state.
	CircuitBreaker CircuitBreakerState `json:"circuit_breaker"`

	// RateLimiter tracks the token bucket state.
	RateLimiter RateLimiterState `json:"rate_limiter"`

	// Bulkhead tracks concurrent request limiting.
	Bulkhead BulkheadState `json:"bulkhead"`

	// UpdatedAt is when the state was last modified.
	UpdatedAt time.Time `json:"updated_at"`
}

// CircuitBreakerState tracks the circuit breaker pattern state.
// The circuit breaker prevents cascading failures by stopping requests
// when the error rate exceeds a threshold.
type CircuitBreakerState struct {
	// State is the current circuit state: "closed", "open", or "half_open".
	// - closed: normal operation, requests flow through
	// - open: circuit tripped, requests fail fast
	// - half_open: testing if service recovered, limited requests allowed
	State string `json:"state"`

	// Failures is the count of consecutive failures in the current window.
	Failures int `json:"failures"`

	// Successes is the count of consecutive successes (used in half_open state).
	Successes int `json:"successes"`

	// HalfOpenAttempts tracks in-flight requests during half-open state.
	// Incremented atomically when a request is allowed, decremented on completion.
	// Used to enforce HalfOpenMaxRequests limit across concurrent processes.
	HalfOpenAttempts int `json:"half_open_attempts,omitempty"`

	// HalfOpenLastAttemptAt is when the last half-open attempt was reserved.
	// Used to detect stale attempts from crashed processes that never completed.
	HalfOpenLastAttemptAt time.Time `json:"half_open_last_attempt_at"`

	// LastFailureAt is when the most recent failure occurred.
	LastFailureAt time.Time `json:"last_failure_at"`

	// OpenedAt is when the circuit transitioned to open state.
	OpenedAt time.Time `json:"opened_at"`
}

// Circuit breaker state constants.
const (
	CircuitClosed   = "closed"
	CircuitOpen     = "open"
	CircuitHalfOpen = "half_open"
)

// IsClosed returns true if the circuit is in closed (normal) state.
func (c *CircuitBreakerState) IsClosed() bool {
	return c.State == "" || c.State == CircuitClosed
}

// IsOpen returns true if the circuit is open (failing fast).
func (c *CircuitBreakerState) IsOpen() bool {
	return c.State == CircuitOpen
}

// IsHalfOpen returns true if the circuit is in half-open (testing) state.
func (c *CircuitBreakerState) IsHalfOpen() bool {
	return c.State == CircuitHalfOpen
}

// RateLimiterState tracks the token bucket rate limiter state.
// Uses a token bucket algorithm that refills over time.
type RateLimiterState struct {
	// Tokens is the current number of available tokens.
	Tokens float64 `json:"tokens"`

	// LastRefillAt is when tokens were last refilled.
	LastRefillAt time.Time `json:"last_refill_at"`

	// RetryAfterUntil is set when we receive a 429 with Retry-After header.
	// No requests should be made until this time passes.
	RetryAfterUntil time.Time `json:"retry_after_until"`
}

// IsBlocked returns true if we're within a Retry-After window.
func (r *RateLimiterState) IsBlocked() bool {
	if r.RetryAfterUntil.IsZero() {
		return false
	}
	return time.Now().Before(r.RetryAfterUntil)
}

// BlockedFor returns how long until the Retry-After window expires.
// Returns zero if not blocked.
func (r *RateLimiterState) BlockedFor() time.Duration {
	if r.RetryAfterUntil.IsZero() {
		return 0
	}
	remaining := time.Until(r.RetryAfterUntil)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// BulkheadState tracks concurrent request limiting across processes.
// The bulkhead pattern limits concurrent requests to prevent resource exhaustion.
type BulkheadState struct {
	// ActivePIDs is the list of process IDs currently holding permits.
	// Stale/dead PIDs are cleaned up by bulkhead operations, not when the state is loaded.
	ActivePIDs []int `json:"active_pids"`
}

// Count returns the number of active permits.
func (b *BulkheadState) Count() int {
	return len(b.ActivePIDs)
}

// HasPID returns true if the given PID holds a permit.
func (b *BulkheadState) HasPID(pid int) bool {
	return slices.Contains(b.ActivePIDs, pid)
}

// AddPID adds a PID to the active list if not already present.
func (b *BulkheadState) AddPID(pid int) {
	if !b.HasPID(pid) {
		b.ActivePIDs = append(b.ActivePIDs, pid)
	}
}

// RemovePID removes a PID from the active list.
func (b *BulkheadState) RemovePID(pid int) {
	for i, p := range b.ActivePIDs {
		if p == pid {
			b.ActivePIDs = append(b.ActivePIDs[:i], b.ActivePIDs[i+1:]...)
			return
		}
	}
}

// NewState returns a new State with default values.
// Note: RateLimiterState.LastRefillAt is left as zero so that the first refill
// call will initialize Tokens to MaxTokens based on the config.
func NewState() *State {
	return &State{
		Version: StateVersion,
		CircuitBreaker: CircuitBreakerState{
			State: CircuitClosed,
		},
		RateLimiter: RateLimiterState{
			// LastRefillAt left as zero for proper initialization
		},
		Bulkhead: BulkheadState{
			ActivePIDs: []int{},
		},
		UpdatedAt: time.Now(),
	}
}
