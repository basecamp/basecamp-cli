package data

import (
	"context"
	"fmt"
	"sync"
)

// Realm manages a group of pools with a shared lifecycle.
// Pools belong to realms. Realm teardown cancels all owned pools'
// in-flight fetches and clears their data.
//
// Three realms mirror the mobile architecture:
//   - Global: app lifetime (identity, account list)
//   - Account: active account session (projects, hey, assignments)
//   - Project: active project context (todos, campfire, messages)
type Realm struct {
	mu     sync.RWMutex
	name   string
	ctx    context.Context
	cancel context.CancelFunc
	pools  map[string]Pooler
}

// NewRealm creates a realm with a cancellable context derived from parent.
func NewRealm(name string, parent context.Context) *Realm { //nolint:revive // context-as-argument: name is the primary differentiator
	ctx, cancel := context.WithCancel(parent)
	return &Realm{
		name:   name,
		ctx:    ctx,
		cancel: cancel,
		pools:  make(map[string]Pooler),
	}
}

// Name returns the realm's identifier.
func (r *Realm) Name() string { return r.name }

// Context returns the realm's context. Canceled on teardown.
// Pass this to pool fetch functions so they abort when the realm dies.
func (r *Realm) Context() context.Context { return r.ctx }

// Register adds a pool to this realm for lifecycle management.
func (r *Realm) Register(key string, p Pooler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pools[key] = p
}

// Pool returns a registered pool by key, or nil if not found.
func (r *Realm) Pool(key string) Pooler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pools[key]
}

// Teardown cancels the realm's context and clears all pools.
// After teardown, the realm should not be reused.
func (r *Realm) Teardown() {
	r.cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.pools {
		p.Clear()
	}
	r.pools = make(map[string]Pooler)
}

// Invalidate marks all pools in this realm as stale.
func (r *Realm) Invalidate() {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.pools {
		p.Invalidate()
	}
}

// RealmPool retrieves or creates a typed pool within a realm.
// The type parameter P must implement Pooler (satisfied by *Pool[T],
// *MutatingPool[T], and *KeyedPool[K, T]).
//
// Each key maps to exactly one concrete type — callers must be
// consistent. The Hub's typed accessors enforce this.
func RealmPool[P Pooler](r *Realm, key string, create func() P) P {
	r.mu.RLock()
	if p, ok := r.pools[key]; ok {
		r.mu.RUnlock()
		typed, ok := p.(P)
		if !ok {
			panic(fmt.Sprintf("realm %q: pool %q has type %T, want %T", r.name, key, p, *new(P)))
		}
		return typed
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.pools[key]; ok {
		typed, ok := p.(P)
		if !ok {
			panic(fmt.Sprintf("realm %q: pool %q has type %T, want %T", r.name, key, p, *new(P)))
		}
		return typed
	}
	pool := create()
	r.pools[key] = pool
	return pool
}
