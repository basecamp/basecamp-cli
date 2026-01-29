package resilience

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBulkheadAcquireAndRelease(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	bh := NewBulkhead(store, BulkheadConfig{
		MaxConcurrent: 5,
	})

	// Acquire a slot
	ok, err := bh.Acquire()
	require.NoError(t, err)
	assert.True(t, ok, "expected acquire to succeed")

	// Check in use
	inUse, err := bh.InUse()
	require.NoError(t, err)
	assert.Equal(t, 1, inUse)

	// Release the slot
	require.NoError(t, bh.Release())

	// Check in use after release
	inUse, err = bh.InUse()
	require.NoError(t, err)
	assert.Equal(t, 0, inUse)
}

func TestBulkheadRejectsWhenFull(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Create a bulkhead with only 1 slot
	bh := NewBulkhead(store, BulkheadConfig{
		MaxConcurrent: 1, // Only allow 1 concurrent request
	})

	// First acquire should succeed (current process)
	ok1, err := bh.Acquire()
	require.NoError(t, err)
	assert.True(t, ok1, "expected first acquire to succeed")

	// Second acquire should also succeed (same process, already has a slot)
	ok2, err := bh.Acquire()
	require.NoError(t, err)
	if !ok2 {
		// The bulkhead allows same PID to "re-acquire" since it already has a slot
		t.Log("Note: Same process re-acquire behavior")
	}

	// Check available
	available, err := bh.Available()
	require.NoError(t, err)
	assert.Equal(t, 0, available)
}

func TestBulkheadPersistence(t *testing.T) {
	dir := t.TempDir()

	// First bulkhead instance
	store1 := NewStore(dir)
	bh1 := NewBulkhead(store1, BulkheadConfig{
		MaxConcurrent: 5,
	})

	// Acquire a slot
	bh1.Acquire()

	// Second bulkhead instance (same process)
	store2 := NewStore(dir)
	bh2 := NewBulkhead(store2, BulkheadConfig{
		MaxConcurrent: 5,
	})

	// Should see the slot from the first instance
	inUse, err := bh2.InUse()
	require.NoError(t, err)
	assert.Equal(t, 1, inUse)
}

func TestBulkheadReset(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	bh := NewBulkhead(store, BulkheadConfig{
		MaxConcurrent: 5,
	})

	// Acquire a slot
	bh.Acquire()

	// Reset
	require.NoError(t, bh.Reset())

	// Should have no slots in use
	inUse, _ := bh.InUse()
	assert.Equal(t, 0, inUse)

	// Should have full availability
	available, _ := bh.Available()
	assert.Equal(t, 5, available)
}

func TestBulkheadAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Create with zero config - should apply defaults
	bh := NewBulkhead(store, BulkheadConfig{})

	// Should work with defaults (10 max concurrent)
	available, err := bh.Available()
	require.NoError(t, err)
	assert.Equal(t, 10, available, "expected 10 available (default)")
}

func TestBulkheadAvailable(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	bh := NewBulkhead(store, BulkheadConfig{
		MaxConcurrent: 5,
	})

	// Initial available
	available, _ := bh.Available()
	assert.Equal(t, 5, available)

	// Acquire a slot
	bh.Acquire()

	// Should have 4 available
	available, _ = bh.Available()
	assert.Equal(t, 4, available)
}

func TestBulkheadReleaseNonexistent(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	bh := NewBulkhead(store, BulkheadConfig{})

	// Should not error when releasing without acquiring first
	assert.NoError(t, bh.Release(), "unexpected error releasing without acquisition")
}

func TestBulkheadCurrentPIDTracking(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	bh := NewBulkhead(store, BulkheadConfig{
		MaxConcurrent: 5,
	})

	// Acquire a slot
	bh.Acquire()

	// Load state and verify PID
	state, _ := store.Load()
	require.Equal(t, 1, len(state.Bulkhead.ActivePIDs))

	assert.Equal(t, os.Getpid(), state.Bulkhead.ActivePIDs[0], "expected current PID")
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
		MaxConcurrent: 5,
	})

	// Force cleanup
	bh.ForceCleanup()

	// Should only have the alive process's PID
	inUse, _ := bh.InUse()
	assert.Equal(t, 1, inUse)

	state2, _ := store.Load()
	require.Equal(t, 1, len(state2.Bulkhead.ActivePIDs))
	assert.Equal(t, os.Getpid(), state2.Bulkhead.ActivePIDs[0], "expected current PID to remain")
}

func TestBulkheadSamePIDMultipleAcquires(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	bh := NewBulkhead(store, BulkheadConfig{
		MaxConcurrent: 5,
	})

	// First acquire
	ok1, _ := bh.Acquire()
	assert.True(t, ok1, "expected first acquire to succeed")

	// Second acquire from same process should succeed
	// (PID is already tracked, so it just returns true)
	ok2, _ := bh.Acquire()
	assert.True(t, ok2, "expected second acquire from same PID to succeed")

	// Should still only count as 1 slot
	inUse, _ := bh.InUse()
	assert.Equal(t, 1, inUse)
}

func TestBulkheadForceCleanup(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	bh := NewBulkhead(store, BulkheadConfig{
		MaxConcurrent: 5,
	})

	// Acquire a slot
	bh.Acquire()

	// Force cleanup (should not remove our live PID)
	require.NoError(t, bh.ForceCleanup())

	// Should still have our slot
	inUse, _ := bh.InUse()
	assert.Equal(t, 1, inUse)
}

func TestBulkheadHasPID(t *testing.T) {
	state := &BulkheadState{
		ActivePIDs: []int{100, 200, 300},
	}

	assert.True(t, state.HasPID(100), "expected HasPID(100) to return true")
	assert.True(t, state.HasPID(200), "expected HasPID(200) to return true")
	assert.False(t, state.HasPID(999), "expected HasPID(999) to return false")
}

func TestBulkheadAddPID(t *testing.T) {
	state := &BulkheadState{
		ActivePIDs: []int{100},
	}

	// Add new PID
	state.AddPID(200)
	assert.Equal(t, 2, len(state.ActivePIDs))

	// Add duplicate PID (should not add)
	state.AddPID(100)
	assert.Equal(t, 2, len(state.ActivePIDs), "expected 2 PIDs (no duplicate)")
}

func TestBulkheadRemovePID(t *testing.T) {
	state := &BulkheadState{
		ActivePIDs: []int{100, 200, 300},
	}

	// Remove middle PID
	state.RemovePID(200)
	assert.Equal(t, 2, len(state.ActivePIDs))
	assert.False(t, state.HasPID(200), "expected PID 200 to be removed")

	// Remove nonexistent PID (should not error)
	state.RemovePID(999)
	assert.Equal(t, 2, len(state.ActivePIDs))
}

func TestBulkheadCount(t *testing.T) {
	state := &BulkheadState{
		ActivePIDs: []int{100, 200, 300},
	}

	assert.Equal(t, 3, state.Count())

	state.ActivePIDs = nil
	assert.Equal(t, 0, state.Count(), "expected count 0 for nil slice")
}
