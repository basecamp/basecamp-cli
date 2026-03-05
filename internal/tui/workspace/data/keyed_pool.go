package data

import "sync"

// KeyedPool manages sub-collection pools keyed by a parent ID.
// For data keyed by a parent: todos by todolist, campfire lines by campfire,
// comments by recording.
type KeyedPool[K comparable, T any] struct {
	mu              sync.RWMutex
	pools           map[K]*Pool[T]
	factory         func(key K) *Pool[T]
	terminalFocused bool // persisted so new sub-pools inherit the state
}

// NewKeyedPool creates a KeyedPool with the given factory for creating
// new pools on demand. All pools share the factory's config but have
// independent fetch state.
func NewKeyedPool[K comparable, T any](factory func(key K) *Pool[T]) *KeyedPool[K, T] {
	return &KeyedPool[K, T]{
		pools:           make(map[K]*Pool[T]),
		factory:         factory,
		terminalFocused: true,
	}
}

// Get returns the Pool for the given key, creating one if it doesn't exist.
func (kp *KeyedPool[K, T]) Get(key K) *Pool[T] {
	kp.mu.RLock()
	if p, ok := kp.pools[key]; ok {
		kp.mu.RUnlock()
		return p
	}
	kp.mu.RUnlock()

	kp.mu.Lock()
	defer kp.mu.Unlock()
	if p, ok := kp.pools[key]; ok {
		return p
	}
	p := kp.factory(key)
	if !kp.terminalFocused {
		p.SetTerminalFocused(false)
	}
	kp.pools[key] = p
	return p
}

// Has returns true if a pool exists for the given key.
func (kp *KeyedPool[K, T]) Has(key K) bool {
	kp.mu.RLock()
	defer kp.mu.RUnlock()
	_, ok := kp.pools[key]
	return ok
}

// Invalidate marks all sub-pools as stale.
func (kp *KeyedPool[K, T]) Invalidate() {
	kp.mu.RLock()
	defer kp.mu.RUnlock()
	for _, p := range kp.pools {
		p.Invalidate()
	}
}

// SetTerminalFocused fans out terminal focus state to all sub-pools.
func (kp *KeyedPool[K, T]) SetTerminalFocused(focused bool) {
	kp.mu.Lock()
	defer kp.mu.Unlock()
	kp.terminalFocused = focused
	for _, p := range kp.pools {
		p.SetTerminalFocused(focused)
	}
}

// Clear removes all sub-pools and their data.
func (kp *KeyedPool[K, T]) Clear() {
	kp.mu.Lock()
	defer kp.mu.Unlock()
	for _, p := range kp.pools {
		p.Clear()
	}
	kp.pools = make(map[K]*Pool[T])
}
