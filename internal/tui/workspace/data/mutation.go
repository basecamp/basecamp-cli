package data

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Mutation describes an optimistic mutation lifecycle.
// Go equivalent of iOS's Mutation protocol / Android's Mutation<T, A>.
type Mutation[T any] interface {
	// ApplyLocally modifies the current state optimistically.
	ApplyLocally(current T) T

	// ApplyRemotely performs the server-side operation.
	ApplyRemotely(ctx context.Context) error

	// IsReflectedIn returns true when the remote data already contains
	// this mutation's effect (for pending-mutation pruning on re-fetch).
	IsReflectedIn(remote T) bool
}

// MutationErrorMsg is sent when a mutation's remote apply fails.
type MutationErrorMsg struct {
	Key string
	Err error
}

type pendingMutation[T any] struct {
	id       uint64
	mutation Mutation[T]
}

// MutatingPool extends Pool with optimistic mutation support.
// Go equivalent of iOS BaseConcurrentDataStore / Android BaseConcurrentRepository.
type MutatingPool[T any] struct {
	*Pool[T]
	pendingMutations []pendingMutation[T]
	lastRemoteData   *T // last known remote state before local mutations
	hasRemoteData    bool
	mutSeq           uint64 // monotonic mutation ID
}

// NewMutatingPool creates a MutatingPool with the given key, config, and fetch function.
func NewMutatingPool[T any](key string, config PoolConfig, fetchFn FetchFunc[T]) *MutatingPool[T] {
	return &MutatingPool[T]{
		Pool: NewPool[T](key, config, fetchFn),
	}
}

// Apply executes an optimistic mutation:
//  1. Applies locally to the snapshot (immediate, synchronous)
//  2. Returns a Cmd that applies remotely, then re-fetches and reconciles
//
// The caller should read pool.Get() after calling Apply to get the
// optimistic data for immediate rendering.
func (mp *MutatingPool[T]) Apply(ctx context.Context, mutation Mutation[T]) tea.Cmd {
	mp.mu.Lock()
	// If this is the first mutation and data exists from Pool.Fetch or Set,
	// capture it as the remote baseline for rollback.
	if !mp.hasRemoteData && mp.snapshot.HasData {
		cp := mp.snapshot.Data
		mp.lastRemoteData = &cp
		mp.hasRemoteData = true
	}

	gen := mp.generation
	mp.mutSeq++
	mid := mp.mutSeq
	mp.pendingMutations = append(mp.pendingMutations, pendingMutation[T]{
		id:       mid,
		mutation: mutation,
	})
	if mp.snapshot.HasData {
		mp.snapshot.Data = mutation.ApplyLocally(mp.snapshot.Data)
		mp.snapshot.State = StateFresh
		mp.snapshot.FetchedAt = time.Now()
		mp.snapshot.Err = nil
		mp.version++
	}
	key := mp.key
	fetchFn := mp.fetchFn
	mp.mu.Unlock()

	return func() tea.Msg {
		if err := mutation.ApplyRemotely(ctx); err != nil {
			mp.rollback(gen, mid)
			return MutationErrorMsg{Key: key, Err: err}
		}

		remoteData, err := fetchFn(ctx)
		if err != nil {
			// Remote apply succeeded but re-fetch failed — data is
			// optimistically correct, just not reconciled yet.
			return PoolUpdatedMsg{Key: key}
		}

		mp.reconcile(gen, remoteData)
		return PoolUpdatedMsg{Key: key}
	}
}

// Fetch overrides Pool.Fetch to reconcile pending mutations after
// a successful fetch rather than overwriting them.
func (mp *MutatingPool[T]) Fetch(ctx context.Context) tea.Cmd {
	mp.mu.Lock()
	if mp.fetching {
		mp.mu.Unlock()
		return nil
	}
	mp.fetching = true
	gen := mp.generation
	if mp.snapshot.HasData {
		mp.snapshot.State = StateLoading
	}
	mp.mu.Unlock()

	return func() tea.Msg {
		data, err := mp.fetchFn(ctx)

		mp.mu.Lock()
		mp.fetching = false

		if mp.generation != gen {
			mp.mu.Unlock()
			return nil
		}

		if err != nil {
			mp.snapshot.State = StateError
			mp.snapshot.Err = err
			mp.mu.Unlock()
			return PoolUpdatedMsg{Key: mp.key}
		}
		mp.mu.Unlock()

		mp.reconcile(gen, data)
		return PoolUpdatedMsg{Key: mp.key}
	}
}

// FetchIfStale overrides Pool.FetchIfStale to route through MutatingPool.Fetch.
func (mp *MutatingPool[T]) FetchIfStale(ctx context.Context) tea.Cmd {
	if mp.isFreshOrFetching() {
		return nil
	}
	return mp.Fetch(ctx)
}

// Clear overrides Pool.Clear to also reset mutation state.
func (mp *MutatingPool[T]) Clear() {
	mp.mu.Lock()
	m := mp.metrics
	mp.clearLocked()
	mp.pendingMutations = nil
	mp.lastRemoteData = nil
	mp.hasRemoteData = false
	mp.mu.Unlock()
	// Unregister outside pool lock to avoid lock-order inversion.
	if m != nil {
		m.UnregisterPool(mp.key)
	}
}

// reconcile rebuilds local state from remote data, re-applying any
// pending mutations not yet reflected in the server response.
// Mirrors iOS sync(remoteState:withLocalState:).
func (mp *MutatingPool[T]) reconcile(gen uint64, remoteData T) {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	// Discard if pool was cleared/reset since the fetch started.
	if mp.generation != gen {
		return
	}

	cp := remoteData
	mp.lastRemoteData = &cp
	mp.hasRemoteData = true

	// Prune mutations already reflected in remote state.
	remaining := mp.pendingMutations[:0]
	for _, pm := range mp.pendingMutations {
		if !pm.mutation.IsReflectedIn(remoteData) {
			remaining = append(remaining, pm)
		}
	}
	mp.pendingMutations = remaining

	// Rebuild: start from remote, re-apply pending.
	data := remoteData
	for _, pm := range mp.pendingMutations {
		data = pm.mutation.ApplyLocally(data)
	}

	mp.snapshot.Data = data
	mp.snapshot.State = StateFresh
	mp.snapshot.FetchedAt = time.Now()
	mp.snapshot.HasData = true
	mp.snapshot.Err = nil
	mp.version++
}

// rollback removes a failed mutation and restores from last remote state.
func (mp *MutatingPool[T]) rollback(gen uint64, mutationID uint64) {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	// Discard if pool was cleared/reset since the mutation started.
	if mp.generation != gen {
		return
	}

	remaining := mp.pendingMutations[:0]
	for _, pm := range mp.pendingMutations {
		if pm.id != mutationID {
			remaining = append(remaining, pm)
		}
	}
	mp.pendingMutations = remaining

	if mp.hasRemoteData {
		data := *mp.lastRemoteData
		for _, pm := range mp.pendingMutations {
			data = pm.mutation.ApplyLocally(data)
		}
		mp.snapshot.Data = data
		mp.snapshot.State = StateFresh
		mp.snapshot.FetchedAt = time.Now()
		mp.snapshot.Err = nil
		mp.version++
	}
}
