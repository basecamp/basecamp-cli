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
func (cb *CircuitBreaker) Allow() (bool, error) {
	state, err := cb.store.Load()
	if err != nil {
		// On error, allow the request (fail open)
		return true, nil
	}

	cbState := &state.CircuitBreaker
	now := cb.now()

	switch {
	case cbState.IsClosed():
		return true, nil

	case cbState.IsOpen():
		// Check if we should transition to half-open
		if now.Sub(cbState.OpenedAt) >= cb.config.OpenTimeout {
			// Transition to half-open
			return cb.store.Update(func(s *State) error {
				s.CircuitBreaker.State = CircuitHalfOpen
				s.CircuitBreaker.Successes = 0
				s.UpdatedAt = now
				return nil
			}) == nil, nil
		}
		return false, nil

	case cbState.IsHalfOpen():
		// Allow limited requests in half-open state
		return true, nil

	default:
		return true, nil
	}
}

// RecordSuccess records a successful request.
func (cb *CircuitBreaker) RecordSuccess() error {
	return cb.store.Update(func(state *State) error {
		cbState := &state.CircuitBreaker
		now := cb.now()

		switch {
		case cbState.IsHalfOpen():
			cbState.Successes++
			if cbState.Successes >= cb.config.SuccessThreshold {
				cbState.State = CircuitClosed
				cbState.Failures = 0
				cbState.Successes = 0
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
			// Any failure in half-open state opens the circuit
			cbState.State = CircuitOpen
			cbState.OpenedAt = now
			cbState.Successes = 0
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
