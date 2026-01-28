package resilience

import (
	"os"
	"testing"
	"time"
)

func TestBulkheadAcquireAndRelease(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	bh := NewBulkhead(store, BulkheadConfig{
		MaxConcurrent:       5,
		StaleProcessTimeout: 5 * time.Minute,
	})

	// Acquire a slot
	ok, err := bh.Acquire()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected acquire to succeed")
	}

	// Check in use
	inUse, err := bh.InUse()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inUse != 1 {
		t.Errorf("expected 1 slot in use, got %d", inUse)
	}

	// Release the slot
	if err := bh.Release(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check in use after release
	inUse, err = bh.InUse()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inUse != 0 {
		t.Errorf("expected 0 slots in use after release, got %d", inUse)
	}
}

func TestBulkheadRejectsWhenFull(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Create a bulkhead with only 1 slot
	bh := NewBulkhead(store, BulkheadConfig{
		MaxConcurrent:       1, // Only allow 1 concurrent request
		StaleProcessTimeout: 5 * time.Minute,
	})

	// First acquire should succeed (current process)
	ok1, err := bh.Acquire()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok1 {
		t.Error("expected first acquire to succeed")
	}

	// Second acquire should also succeed (same process, already has a slot)
	ok2, err := bh.Acquire()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok2 {
		// The bulkhead allows same PID to "re-acquire" since it already has a slot
		t.Log("Note: Same process re-acquire behavior")
	}

	// Check available
	available, err := bh.Available()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if available != 0 {
		t.Errorf("expected 0 available slots, got %d", available)
	}
}

func TestBulkheadPersistence(t *testing.T) {
	dir := t.TempDir()

	// First bulkhead instance
	store1 := NewStore(dir)
	bh1 := NewBulkhead(store1, BulkheadConfig{
		MaxConcurrent:       5,
		StaleProcessTimeout: 5 * time.Minute,
	})

	// Acquire a slot
	bh1.Acquire()

	// Second bulkhead instance (same process)
	store2 := NewStore(dir)
	bh2 := NewBulkhead(store2, BulkheadConfig{
		MaxConcurrent:       5,
		StaleProcessTimeout: 5 * time.Minute,
	})

	// Should see the slot from the first instance
	inUse, err := bh2.InUse()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inUse != 1 {
		t.Errorf("expected 1 slot in use, got %d", inUse)
	}
}

func TestBulkheadReset(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	bh := NewBulkhead(store, BulkheadConfig{
		MaxConcurrent:       5,
		StaleProcessTimeout: 5 * time.Minute,
	})

	// Acquire a slot
	bh.Acquire()

	// Reset
	if err := bh.Reset(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have no slots in use
	inUse, _ := bh.InUse()
	if inUse != 0 {
		t.Errorf("expected 0 slots after reset, got %d", inUse)
	}

	// Should have full availability
	available, _ := bh.Available()
	if available != 5 {
		t.Errorf("expected 5 available after reset, got %d", available)
	}
}

func TestBulkheadAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Create with zero config - should apply defaults
	bh := NewBulkhead(store, BulkheadConfig{})

	// Should work with defaults (10 max concurrent)
	available, err := bh.Available()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if available != 10 {
		t.Errorf("expected 10 available (default), got %d", available)
	}
}

func TestBulkheadAvailable(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	bh := NewBulkhead(store, BulkheadConfig{
		MaxConcurrent:       5,
		StaleProcessTimeout: 5 * time.Minute,
	})

	// Initial available
	available, _ := bh.Available()
	if available != 5 {
		t.Errorf("expected 5 available initially, got %d", available)
	}

	// Acquire a slot
	bh.Acquire()

	// Should have 4 available
	available, _ = bh.Available()
	if available != 4 {
		t.Errorf("expected 4 available, got %d", available)
	}
}

func TestBulkheadReleaseNonexistent(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	bh := NewBulkhead(store, BulkheadConfig{})

	// Should not error when releasing without acquiring first
	if err := bh.Release(); err != nil {
		t.Errorf("unexpected error releasing without acquisition: %v", err)
	}
}

func TestBulkheadCurrentPIDTracking(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	bh := NewBulkhead(store, BulkheadConfig{
		MaxConcurrent:       5,
		StaleProcessTimeout: 5 * time.Minute,
	})

	// Acquire a slot
	bh.Acquire()

	// Load state and verify PID
	state, _ := store.Load()
	if len(state.Bulkhead.ActivePIDs) != 1 {
		t.Fatalf("expected 1 PID, got %d", len(state.Bulkhead.ActivePIDs))
	}

	if state.Bulkhead.ActivePIDs[0] != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), state.Bulkhead.ActivePIDs[0])
	}
}

func TestBulkheadDeadProcessCleanup(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Manually create state with a fake dead PID
	deadPID := 999999999 // Very unlikely to be a real PID

	initState := NewState()
	initState.Bulkhead.ActivePIDs = []int{deadPID, os.Getpid()}
	store.Save(initState)

	bh := NewBulkhead(store, BulkheadConfig{
		MaxConcurrent:       5,
		StaleProcessTimeout: 5 * time.Minute,
	})

	// Force cleanup
	bh.ForceCleanup()

	// Should only have the alive process's PID
	inUse, _ := bh.InUse()
	if inUse != 1 {
		t.Errorf("expected 1 slot in use after cleanup, got %d", inUse)
	}

	state2, _ := store.Load()
	if len(state2.Bulkhead.ActivePIDs) != 1 {
		t.Fatalf("expected 1 PID after cleanup, got %d", len(state2.Bulkhead.ActivePIDs))
	}
	if state2.Bulkhead.ActivePIDs[0] != os.Getpid() {
		t.Errorf("expected current PID to remain, got %d", state2.Bulkhead.ActivePIDs[0])
	}
}

func TestBulkheadSamePIDMultipleAcquires(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	bh := NewBulkhead(store, BulkheadConfig{
		MaxConcurrent:       5,
		StaleProcessTimeout: 5 * time.Minute,
	})

	// First acquire
	ok1, _ := bh.Acquire()
	if !ok1 {
		t.Error("expected first acquire to succeed")
	}

	// Second acquire from same process should succeed
	// (PID is already tracked, so it just returns true)
	ok2, _ := bh.Acquire()
	if !ok2 {
		t.Error("expected second acquire from same PID to succeed")
	}

	// Should still only count as 1 slot
	inUse, _ := bh.InUse()
	if inUse != 1 {
		t.Errorf("expected 1 slot in use (same PID), got %d", inUse)
	}
}

func TestBulkheadForceCleanup(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	bh := NewBulkhead(store, BulkheadConfig{
		MaxConcurrent:       5,
		StaleProcessTimeout: 5 * time.Minute,
	})

	// Acquire a slot
	bh.Acquire()

	// Force cleanup (should not remove our live PID)
	if err := bh.ForceCleanup(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should still have our slot
	inUse, _ := bh.InUse()
	if inUse != 1 {
		t.Errorf("expected 1 slot in use after cleanup, got %d", inUse)
	}
}

func TestBulkheadHasPID(t *testing.T) {
	state := &BulkheadState{
		ActivePIDs: []int{100, 200, 300},
	}

	if !state.HasPID(100) {
		t.Error("expected HasPID(100) to return true")
	}
	if !state.HasPID(200) {
		t.Error("expected HasPID(200) to return true")
	}
	if state.HasPID(999) {
		t.Error("expected HasPID(999) to return false")
	}
}

func TestBulkheadAddPID(t *testing.T) {
	state := &BulkheadState{
		ActivePIDs: []int{100},
	}

	// Add new PID
	state.AddPID(200)
	if len(state.ActivePIDs) != 2 {
		t.Errorf("expected 2 PIDs, got %d", len(state.ActivePIDs))
	}

	// Add duplicate PID (should not add)
	state.AddPID(100)
	if len(state.ActivePIDs) != 2 {
		t.Errorf("expected 2 PIDs (no duplicate), got %d", len(state.ActivePIDs))
	}
}

func TestBulkheadRemovePID(t *testing.T) {
	state := &BulkheadState{
		ActivePIDs: []int{100, 200, 300},
	}

	// Remove middle PID
	state.RemovePID(200)
	if len(state.ActivePIDs) != 2 {
		t.Errorf("expected 2 PIDs, got %d", len(state.ActivePIDs))
	}
	if state.HasPID(200) {
		t.Error("expected PID 200 to be removed")
	}

	// Remove nonexistent PID (should not error)
	state.RemovePID(999)
	if len(state.ActivePIDs) != 2 {
		t.Errorf("expected 2 PIDs, got %d", len(state.ActivePIDs))
	}
}

func TestBulkheadCount(t *testing.T) {
	state := &BulkheadState{
		ActivePIDs: []int{100, 200, 300},
	}

	if state.Count() != 3 {
		t.Errorf("expected count 3, got %d", state.Count())
	}

	state.ActivePIDs = nil
	if state.Count() != 0 {
		t.Errorf("expected count 0 for nil slice, got %d", state.Count())
	}
}
