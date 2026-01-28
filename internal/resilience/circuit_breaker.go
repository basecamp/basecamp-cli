package resilience

import (
	"time"
)

// CircuitBreaker implements the circuit breaker pattern with cross-process persistence.
type CircuitBreaker struct {
	config CircuitBreakerConfig
	store  *Store
}

// NewCircuitBreaker creates a new circuit breaker with the given config.
func NewCircuitBreaker(store *Store, config CircuitBreakerConfig) *CircuitBreaker {
	// Apply defaults for zero values
	if config.FailureThreshold <= 0 {
		config.FailureThreshold = 5
	}
	if config.SuccessThreshold <= 0 {
		config.SuccessThreshold = 2
	}
	if config.OpenTimeout <= 0 {
		config.OpenTimeout = 30 * time.Second
	}

	return &CircuitBreaker{
		config: config,
		store:  store,
	}
}

// now returns the current time.
func (cb *CircuitBreaker) now() time.Time {
	return time.Now()
}

// Allow checks if a request should be allowed.
// Returns true if the request can proceed, false if it should be rejected.
// In half-open state, atomically reserves an attempt slot to prevent thundering herd.
//
// Optimization: closed state only reads (no disk write), open/half-open may write.
//
// Stale attempt cleanup: If half-open attempts are stuck at max (e.g., from a crashed
// process that never called RecordSuccess/RecordFailure), they are automatically
// reset after OpenTimeout. This prevents permanent blocking from process crashes.
func (cb *CircuitBreaker) Allow() (bool, error) {
	// First, do a read-only check for the common case (closed state)
	state, err := cb.store.Load()
	if err != nil {
		// On error, allow the request (fail open)
		return true, nil
	}

	cbState := &state.CircuitBreaker
	now := cb.now()

	// Fast path: closed state - no state change needed
	if cbState.IsClosed() {
		return true, nil
	}

	// Open state: check if timeout expired, may need to transition
	if cbState.IsOpen() {
		if now.Sub(cbState.OpenedAt) < cb.config.OpenTimeout {
			// Still in timeout, reject
			return false, nil
		}
		// Timeout expired, transition to half-open and reserve a slot
		var allowed bool
		err := cb.store.Update(func(s *State) error {
			// Re-check state in case another process changed it
			if s.CircuitBreaker.IsOpen() && now.Sub(s.CircuitBreaker.OpenedAt) >= cb.config.OpenTimeout {
				s.CircuitBreaker.State = CircuitHalfOpen
				s.CircuitBreaker.Successes = 0
				s.CircuitBreaker.Failures = 0
				s.CircuitBreaker.HalfOpenAttempts = 1
				s.CircuitBreaker.HalfOpenLastAttemptAt = now
				s.UpdatedAt = now
				allowed = true
			} else if s.CircuitBreaker.IsHalfOpen() {
				// Another process already transitioned, try to reserve a slot
				// Clean up stale attempts from crashed processes if stuck for too long
				if cb.shouldResetStaleAttempts(s, now) {
					s.CircuitBreaker.HalfOpenAttempts = 0
					s.UpdatedAt = now
				}
				if cb.config.HalfOpenMaxRequests > 0 && s.CircuitBreaker.HalfOpenAttempts >= cb.config.HalfOpenMaxRequests {
					allowed = false
				} else {
					s.CircuitBreaker.HalfOpenAttempts++
					s.CircuitBreaker.HalfOpenLastAttemptAt = now
					s.UpdatedAt = now
					allowed = true
				}
			} else if s.CircuitBreaker.IsClosed() {
				// Circuit closed while we were checking
				allowed = true
			} else {
				allowed = false
			}
			return nil
		})
		if err != nil {
			return true, nil // Fail open
		}
		return allowed, nil
	}

	// Half-open state: atomically reserve a slot
	if cbState.IsHalfOpen() {
		if cb.config.HalfOpenMaxRequests <= 0 {
			// No limit configured
			return true, nil
		}

		var allowed bool
		err := cb.store.Update(func(s *State) error {
			// Re-check state in case it changed
			if !s.CircuitBreaker.IsHalfOpen() {
				// State changed, re-evaluate
				if s.CircuitBreaker.IsClosed() {
					allowed = true
				} else {
					allowed = false
				}
				return nil
			}
			// Clean up stale attempts from crashed processes if stuck for too long
			if cb.shouldResetStaleAttempts(s, now) {
				s.CircuitBreaker.HalfOpenAttempts = 0
				s.UpdatedAt = now
			}
			if s.CircuitBreaker.HalfOpenAttempts >= cb.config.HalfOpenMaxRequests {
				allowed = false
			} else {
				s.CircuitBreaker.HalfOpenAttempts++
				s.CircuitBreaker.HalfOpenLastAttemptAt = now
				s.UpdatedAt = now
				allowed = true
			}
			return nil
		})
		if err != nil {
			return true, nil // Fail open
		}
		return allowed, nil
	}

	return true, nil
}

// shouldResetStaleAttempts returns true if half-open attempts should be reset.
// This handles the case where a process crashes before calling RecordSuccess/RecordFailure,
// leaving HalfOpenAttempts stuck at max. We reset after OpenTimeout to allow recovery.
//
// Note: We use HalfOpenLastAttemptAt (circuit-breaker-specific) instead of State.UpdatedAt
// because other components (rate limiter, bulkhead) also update State.UpdatedAt, which
// would prevent stale detection when they run before the circuit breaker check.
func (cb *CircuitBreaker) shouldResetStaleAttempts(s *State, now time.Time) bool {
	if !s.CircuitBreaker.IsHalfOpen() {
		return false
	}
	if s.CircuitBreaker.HalfOpenAttempts < cb.config.HalfOpenMaxRequests {
		return false
	}
	// If attempts are maxed out and no attempt has been reserved in OpenTimeout,
	// assume the attempts are from crashed processes
	lastAttempt := s.CircuitBreaker.HalfOpenLastAttemptAt
	if lastAttempt.IsZero() {
		// Fallback for state created before this field existed
		return false
	}
	return now.Sub(lastAttempt) >= cb.config.OpenTimeout
}

// RecordSuccess records a successful request.
func (cb *CircuitBreaker) RecordSuccess() error {
	return cb.store.Update(func(state *State) error {
		cbState := &state.CircuitBreaker
		now := cb.now()

		switch {
		case cbState.IsHalfOpen():
			// Release the reserved slot
			if cbState.HalfOpenAttempts > 0 {
				cbState.HalfOpenAttempts--
			}
			cbState.Successes++
			if cbState.Successes >= cb.config.SuccessThreshold {
				cbState.State = CircuitClosed
				cbState.Failures = 0
				cbState.Successes = 0
				cbState.HalfOpenAttempts = 0
				cbState.HalfOpenLastAttemptAt = time.Time{}
			}
		case cbState.IsClosed():
			// Reset consecutive failure count on success
			cbState.Failures = 0
		}

		state.UpdatedAt = now
		return nil
	})
}

// RecordFailure records a failed request.
func (cb *CircuitBreaker) RecordFailure() error {
	return cb.store.Update(func(state *State) error {
		cbState := &state.CircuitBreaker
		now := cb.now()

		cbState.LastFailureAt = now

		switch {
		case cbState.IsClosed():
			cbState.Failures++
			if cbState.Failures >= cb.config.FailureThreshold {
				cbState.State = CircuitOpen
				cbState.OpenedAt = now
			}

		case cbState.IsHalfOpen():
			// Release the reserved slot and open the circuit
			if cbState.HalfOpenAttempts > 0 {
				cbState.HalfOpenAttempts--
			}
			cbState.State = CircuitOpen
			cbState.OpenedAt = now
			cbState.Successes = 0
			cbState.HalfOpenAttempts = 0
			cbState.HalfOpenLastAttemptAt = time.Time{}
		}

		state.UpdatedAt = now
		return nil
	})
}

// State returns the current circuit breaker state.
func (cb *CircuitBreaker) State() (string, error) {
	state, err := cb.store.Load()
	if err != nil {
		return CircuitClosed, err
	}

	cbState := &state.CircuitBreaker

	// Check if open circuit should transition to half-open
	if cbState.IsOpen() {
		if cb.now().Sub(cbState.OpenedAt) >= cb.config.OpenTimeout {
			return CircuitHalfOpen, nil
		}
	}

	if cbState.State == "" {
		return CircuitClosed, nil
	}
	return cbState.State, nil
}

// Reset resets the circuit breaker to closed state.
func (cb *CircuitBreaker) Reset() error {
	return cb.store.Update(func(state *State) error {
		state.CircuitBreaker = CircuitBreakerState{State: CircuitClosed}
		state.UpdatedAt = cb.now()
		return nil
	})
}
