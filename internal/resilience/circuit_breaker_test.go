package resilience

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCircuitBreakerDefaultsClosed(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	cb := NewCircuitBreaker(store, CircuitBreakerConfig{})

	state, err := cb.State()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != CircuitClosed {
		t.Errorf("expected closed state, got %s", state)
	}
}

func TestCircuitBreakerAllowsWhenClosed(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	cb := NewCircuitBreaker(store, CircuitBreakerConfig{})

	allowed, err := cb.Allow()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Error("expected request to be allowed when circuit is closed")
	}
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
	for i := 0; i < 3; i++ {
		if err := cb.RecordFailure(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	// Should be open now
	state, err := cb.State()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != CircuitOpen {
		t.Errorf("expected open state, got %s", state)
	}

	// Should reject requests
	allowed, err := cb.Allow()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Error("expected request to be rejected when circuit is open")
	}
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
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	// Wait for timeout to allow transition to half-open
	time.Sleep(10 * time.Millisecond)

	// This Allow() should trigger transition to half-open
	allowed, _ := cb.Allow()
	if !allowed {
		t.Error("expected request to be allowed in half-open state")
	}

	// Record successes to close the circuit
	for i := 0; i < 2; i++ {
		if err := cb.RecordSuccess(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	// Should be closed now
	state, err := cb.State()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != CircuitClosed {
		t.Errorf("expected closed state, got %s", state)
	}
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
	for i := 0; i < 3; i++ {
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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != CircuitOpen {
		t.Errorf("expected open state, got %s", state)
	}
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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != CircuitClosed {
		t.Errorf("expected closed state, got %s", state)
	}

	// One more failure should open
	cb.RecordFailure()
	state, err = cb.State()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != CircuitOpen {
		t.Errorf("expected open state, got %s", state)
	}
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
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	state, _ := cb.State()
	if state != CircuitOpen {
		t.Errorf("expected open state, got %s", state)
	}

	// Reset
	if err := cb.Reset(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be closed
	state, err := cb.State()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != CircuitClosed {
		t.Errorf("expected closed state after reset, got %s", state)
	}
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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != CircuitOpen {
		t.Errorf("expected open state, got %s", state)
	}
}

func TestCircuitBreakerAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Create with zero config - should apply defaults
	cb := NewCircuitBreaker(store, CircuitBreakerConfig{})

	// Should work with defaults
	allowed, err := cb.Allow()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Error("expected request to be allowed")
	}
}

func TestCircuitBreakerStateFilePath(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	cb := NewCircuitBreaker(store, CircuitBreakerConfig{})

	// Record a failure to create the state file
	cb.RecordFailure()

	// Verify the state file exists
	stateFile := filepath.Join(dir, StateFileName)
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		t.Error("expected state file to exist")
	}
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
	if state != CircuitClosed {
		t.Errorf("expected closed, got %s", state)
	}

	// Failures -> open
	cb.RecordFailure()
	cb.RecordFailure()
	state, _ = cb.State()
	if state != CircuitOpen {
		t.Errorf("expected open, got %s", state)
	}

	// Wait -> half-open
	time.Sleep(10 * time.Millisecond)
	state, _ = cb.State()
	if state != CircuitHalfOpen {
		t.Errorf("expected half-open, got %s", state)
	}

	// Success -> closed
	cb.Allow() // Transition to half-open first
	cb.RecordSuccess()
	state, _ = cb.State()
	if state != CircuitClosed {
		t.Errorf("expected closed, got %s", state)
	}
}
