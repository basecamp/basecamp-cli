package resilience

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCircuitBreakerDefaultsClosed(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	cb := NewCircuitBreaker(store, CircuitBreakerConfig{})

	state, err := cb.State()
	require.NoError(t, err)
	assert.Equal(t, CircuitClosed, state)
}

func TestCircuitBreakerAllowsWhenClosed(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	cb := NewCircuitBreaker(store, CircuitBreakerConfig{})

	allowed, err := cb.Allow()
	require.NoError(t, err)
	assert.True(t, allowed, "expected request to be allowed when circuit is closed")
}

func TestCircuitBreakerOpensAfterFailures(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	cb := NewCircuitBreaker(store, CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		OpenTimeout:      30 * time.Second,
	})

	// Record failures up to threshold
	for range 3 {
		err := cb.RecordFailure()
		require.NoError(t, err)
	}

	// Should be open now
	state, err := cb.State()
	require.NoError(t, err)
	assert.Equal(t, CircuitOpen, state)

	// Should reject requests
	allowed, err := cb.Allow()
	require.NoError(t, err)
	assert.False(t, allowed, "expected request to be rejected when circuit is open")
}

func TestCircuitBreakerClosesAfterSuccesses(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	cb := NewCircuitBreaker(store, CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		OpenTimeout:      1 * time.Millisecond, // Very short timeout for testing
	})

	// Open the circuit
	for range 3 {
		cb.RecordFailure()
	}

	// Wait for timeout to allow transition to half-open
	time.Sleep(10 * time.Millisecond)

	// This Allow() should trigger transition to half-open
	allowed, _ := cb.Allow()
	assert.True(t, allowed, "expected request to be allowed in half-open state")

	// Record successes to close the circuit
	for range 2 {
		err := cb.RecordSuccess()
		require.NoError(t, err)
	}

	// Should be closed now
	state, err := cb.State()
	require.NoError(t, err)
	assert.Equal(t, CircuitClosed, state)
}

func TestCircuitBreakerFailureInHalfOpenOpens(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	cb := NewCircuitBreaker(store, CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		OpenTimeout:      1 * time.Millisecond,
	})

	// Open the circuit
	for range 3 {
		cb.RecordFailure()
	}

	// Wait for timeout and transition to half-open
	time.Sleep(10 * time.Millisecond)
	cb.Allow()

	// Record one success, then failure
	cb.RecordSuccess()
	cb.RecordFailure()

	// Should be open again
	state, err := cb.State()
	require.NoError(t, err)
	assert.Equal(t, CircuitOpen, state)
}

func TestCircuitBreakerSuccessResetsFailureCount(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	cb := NewCircuitBreaker(store, CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		OpenTimeout:      30 * time.Second,
	})

	// Record some failures
	cb.RecordFailure()
	cb.RecordFailure()

	// Record success (should reset counter)
	cb.RecordSuccess()

	// Record failures again - should need 3 more
	cb.RecordFailure()
	cb.RecordFailure()

	// Should still be closed
	state, err := cb.State()
	require.NoError(t, err)
	assert.Equal(t, CircuitClosed, state)

	// One more failure should open
	cb.RecordFailure()
	state, err = cb.State()
	require.NoError(t, err)
	assert.Equal(t, CircuitOpen, state)
}

func TestCircuitBreakerReset(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	cb := NewCircuitBreaker(store, CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		OpenTimeout:      30 * time.Second,
	})

	// Open the circuit
	for range 3 {
		cb.RecordFailure()
	}

	state, _ := cb.State()
	assert.Equal(t, CircuitOpen, state)

	// Reset
	err := cb.Reset()
	require.NoError(t, err)

	// Should be closed
	state, err = cb.State()
	require.NoError(t, err)
	assert.Equal(t, CircuitClosed, state)
}

func TestCircuitBreakerPersistence(t *testing.T) {
	dir := t.TempDir()

	// First circuit breaker instance
	store1 := NewStore(dir)
	cb1 := NewCircuitBreaker(store1, CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		OpenTimeout:      30 * time.Second,
	})

	// Record some failures
	cb1.RecordFailure()
	cb1.RecordFailure()

	// Second circuit breaker instance (simulating new process)
	store2 := NewStore(dir)
	cb2 := NewCircuitBreaker(store2, CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		OpenTimeout:      30 * time.Second,
	})

	// One more failure should open the circuit
	cb2.RecordFailure()

	state, err := cb2.State()
	require.NoError(t, err)
	assert.Equal(t, CircuitOpen, state)
}

func TestCircuitBreakerAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Create with zero config - should apply defaults
	cb := NewCircuitBreaker(store, CircuitBreakerConfig{})

	// Should work with defaults
	allowed, err := cb.Allow()
	require.NoError(t, err)
	assert.True(t, allowed, "expected request to be allowed")
}

func TestCircuitBreakerStateFilePath(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	cb := NewCircuitBreaker(store, CircuitBreakerConfig{})

	// Record a failure to create the state file
	cb.RecordFailure()

	// Verify the state file exists
	stateFile := filepath.Join(dir, StateFileName)
	_, err := os.Stat(stateFile)
	assert.False(t, os.IsNotExist(err), "expected state file to exist")
}

func TestCircuitBreakerStateTransitionsCorrectly(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	cb := NewCircuitBreaker(store, CircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 1,
		OpenTimeout:      1 * time.Millisecond,
	})

	// Start closed
	state, _ := cb.State()
	assert.Equal(t, CircuitClosed, state)

	// Failures -> open
	cb.RecordFailure()
	cb.RecordFailure()
	state, _ = cb.State()
	assert.Equal(t, CircuitOpen, state)

	// Wait -> half-open
	time.Sleep(10 * time.Millisecond)
	state, _ = cb.State()
	assert.Equal(t, CircuitHalfOpen, state)

	// Success -> closed
	cb.Allow() // Transition to half-open first
	cb.RecordSuccess()
	state, _ = cb.State()
	assert.Equal(t, CircuitClosed, state)
}

func TestCircuitBreakerResetsStaleHalfOpenAttempts(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Use longer timeouts for CI stability (50ms instead of 10ms)
	openTimeout := 50 * time.Millisecond
	staleTimeout := 100 * time.Millisecond

	cb := NewCircuitBreaker(store, CircuitBreakerConfig{
		FailureThreshold:    2,
		SuccessThreshold:    1,
		OpenTimeout:         openTimeout,
		HalfOpenMaxRequests: 1,
		StaleAttemptTimeout: staleTimeout,
	})

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()

	// Wait for timeout to allow half-open
	time.Sleep(openTimeout * 2)

	// First Allow() transitions to half-open and reserves a slot
	allowed, err := cb.Allow()
	require.NoError(t, err)
	assert.True(t, allowed, "expected first request to be allowed in half-open state")

	// Simulate a crash: don't call RecordSuccess/RecordFailure
	// The HalfOpenAttempts is now at max (1)

	// Second Allow() should be rejected (max reached, not yet stale)
	allowed, err = cb.Allow()
	require.NoError(t, err)
	assert.False(t, allowed, "expected second request to be rejected when half-open slots exhausted")

	// Wait for stale timeout period
	time.Sleep(staleTimeout + 50*time.Millisecond)

	// Now Allow() should reset stale attempts and allow
	allowed, err = cb.Allow()
	require.NoError(t, err)
	assert.True(t, allowed, "expected request to be allowed after stale attempts are reset")
}

func TestCircuitBreakerSetsHalfOpenLastAttemptAt(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Use longer timeouts for CI stability
	openTimeout := 50 * time.Millisecond

	cb := NewCircuitBreaker(store, CircuitBreakerConfig{
		FailureThreshold:    2,
		SuccessThreshold:    1,
		OpenTimeout:         openTimeout,
		HalfOpenMaxRequests: 1,
	})

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()

	// Wait for timeout to allow half-open
	time.Sleep(openTimeout * 2)

	// Check that HalfOpenLastAttemptAt is zero before Allow()
	state, _ := store.Load()
	assert.True(t, state.CircuitBreaker.HalfOpenLastAttemptAt.IsZero(), "expected HalfOpenLastAttemptAt to be zero before Allow()")

	// First Allow() transitions to half-open and reserves a slot
	before := time.Now()
	allowed, err := cb.Allow()
	after := time.Now()
	require.NoError(t, err)
	assert.True(t, allowed, "expected first request to be allowed")

	// HalfOpenLastAttemptAt should be set
	state, _ = store.Load()
	assert.False(t, state.CircuitBreaker.HalfOpenLastAttemptAt.IsZero(), "expected HalfOpenLastAttemptAt to be set after Allow()")
	assert.False(t, state.CircuitBreaker.HalfOpenLastAttemptAt.Before(before) || state.CircuitBreaker.HalfOpenLastAttemptAt.After(after), "HalfOpenLastAttemptAt should be between before and after Allow()")
}

func TestCircuitBreakerResetsStaleAttemptsWithZeroTimestamp(t *testing.T) {
	// Test that legacy state files (without HalfOpenLastAttemptAt) are treated as stale
	dir := t.TempDir()
	store := NewStore(dir)

	// Manually create a stuck half-open state without the timestamp
	store.Update(func(state *State) error {
		state.CircuitBreaker.State = CircuitHalfOpen
		state.CircuitBreaker.HalfOpenAttempts = 1
		// HalfOpenLastAttemptAt is zero (legacy state)
		return nil
	})

	cb := NewCircuitBreaker(store, CircuitBreakerConfig{
		FailureThreshold:    2,
		SuccessThreshold:    1,
		OpenTimeout:         50 * time.Millisecond,
		HalfOpenMaxRequests: 1,
	})

	// Should detect the zero timestamp as stale and allow
	allowed, err := cb.Allow()
	require.NoError(t, err)
	assert.True(t, allowed, "expected request to be allowed when timestamp is zero (legacy state)")
}
