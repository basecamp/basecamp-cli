package resilience

import (
	"os"
	"time"
)

// Bulkhead implements the bulkhead pattern with cross-process persistence.
// It uses PID-based tracking to limit concurrent operations across CLI invocations.
type Bulkhead struct {
	config BulkheadConfig
	store  *Store
}

// NewBulkhead creates a new bulkhead with the given config.
func NewBulkhead(store *Store, config BulkheadConfig) *Bulkhead {
	// Apply defaults for zero values
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = 10
	}

	return &Bulkhead{
		config: config,
		store:  store,
	}
}

// now returns the current time.
func (b *Bulkhead) now() time.Time {
	return time.Now()
}

// cleanupStaleSlots removes PIDs from dead processes.
func (b *Bulkhead) cleanupStaleSlots(state *BulkheadState) {
	alive := make([]int, 0, len(state.ActivePIDs))

	for _, pid := range state.ActivePIDs {
		if isProcessAlive(pid) {
			alive = append(alive, pid)
		}
	}

	state.ActivePIDs = alive
}

// Acquire tries to acquire a slot in the bulkhead.
// Returns true if the slot was acquired, false if the bulkhead is full.
func (b *Bulkhead) Acquire() (bool, error) {
	var acquired bool

	err := b.store.Update(func(state *State) error {
		bhState := &state.Bulkhead

		// Cleanup stale slots from dead processes
		b.cleanupStaleSlots(bhState)

		// Check if we already have a slot
		pid := os.Getpid()
		if bhState.HasPID(pid) {
			acquired = true
			return nil
		}

		// Check if we have room
		if bhState.Count() >= b.config.MaxConcurrent {
			acquired = false
			return nil
		}

		// Add our PID
		bhState.AddPID(pid)
		state.UpdatedAt = b.now()
		acquired = true
		return nil
	})

	if err != nil {
		// On error, allow the request (fail open)
		return true, nil //nolint:nilerr // Intentional fail-open: allow request when state cannot be loaded
	}

	return acquired, nil
}

// Release releases the slot held by this process.
func (b *Bulkhead) Release() error {
	return b.store.Update(func(state *State) error {
		pid := os.Getpid()
		state.Bulkhead.RemovePID(pid)
		state.UpdatedAt = b.now()
		return nil
	})
}

// Available returns the number of available slots.
// Returns a value in [0, MaxConcurrent] even if Count exceeds MaxConcurrent
// (possible under fail-open or config changes).
func (b *Bulkhead) Available() (int, error) {
	state, err := b.store.Load()
	if err != nil {
		return b.config.MaxConcurrent, err
	}

	bhState := state.Bulkhead
	b.cleanupStaleSlots(&bhState)
	available := b.config.MaxConcurrent - bhState.Count()
	if available < 0 {
		available = 0
	}
	return available, nil
}

// InUse returns the number of slots currently in use.
func (b *Bulkhead) InUse() (int, error) {
	state, err := b.store.Load()
	if err != nil {
		return 0, err
	}

	bhState := state.Bulkhead
	b.cleanupStaleSlots(&bhState)
	return bhState.Count(), nil
}

// Reset clears all slots (useful for cleanup or testing).
func (b *Bulkhead) Reset() error {
	return b.store.Update(func(state *State) error {
		state.Bulkhead = BulkheadState{ActivePIDs: []int{}}
		state.UpdatedAt = b.now()
		return nil
	})
}

// ForceCleanup performs a cleanup of stale slots without acquiring.
func (b *Bulkhead) ForceCleanup() error {
	return b.store.Update(func(state *State) error {
		b.cleanupStaleSlots(&state.Bulkhead)
		state.UpdatedAt = b.now()
		return nil
	})
}
