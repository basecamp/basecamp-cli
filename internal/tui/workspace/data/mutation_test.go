package data

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testItem is a simple test domain type.
type testItem struct {
	ID        int
	Completed bool
}

// completeMutation marks an item as completed.
type completeMutation struct {
	itemID    int
	remoteErr error // if set, ApplyRemotely fails
}

func (m completeMutation) ApplyLocally(items []testItem) []testItem {
	out := make([]testItem, len(items))
	copy(out, items)
	for i := range out {
		if out[i].ID == m.itemID {
			out[i].Completed = true
		}
	}
	return out
}

func (m completeMutation) ApplyRemotely(ctx context.Context) error {
	return m.remoteErr
}

func (m completeMutation) IsReflectedIn(remote []testItem) bool {
	for _, item := range remote {
		if item.ID == m.itemID && item.Completed {
			return true
		}
	}
	return false
}

func newTestMutatingPool() *MutatingPool[[]testItem] {
	items := []testItem{{ID: 1}, {ID: 2}, {ID: 3}}
	return NewMutatingPool("items", PoolConfig{}, func(ctx context.Context) ([]testItem, error) {
		// Simulate server returning the base state.
		return items, nil
	})
}

func TestMutatingPoolApplyOptimistic(t *testing.T) {
	mp := newTestMutatingPool()
	mp.Pool.Fetch(context.Background())()

	// Verify pre-state.
	data := mp.Get().Data
	require.False(t, data[0].Completed)

	// Apply mutation — reads optimistic state synchronously.
	cmd := mp.Apply(context.Background(), completeMutation{itemID: 1})
	require.NotNil(t, cmd)

	optimistic := mp.Get().Data
	assert.True(t, optimistic[0].Completed, "item 1 should be optimistically completed")
	assert.False(t, optimistic[1].Completed)

	// Let the Cmd complete (remote + reconcile).
	cmd()

	final := mp.Get().Data
	assert.True(t, final[0].Completed)
}

func TestMutatingPoolApplyRemoteFailure(t *testing.T) {
	mp := newTestMutatingPool()
	mp.Pool.Fetch(context.Background())()

	// Apply a mutation that will fail remotely.
	cmd := mp.Apply(context.Background(), completeMutation{
		itemID:    1,
		remoteErr: errors.New("server error"),
	})

	// Optimistic update applied.
	assert.True(t, mp.Get().Data[0].Completed)

	// Cmd runs remote, fails, triggers rollback.
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	require.True(t, ok)
	require.Len(t, batch, 2)

	errMsg, ok := batch[0]().(MutationErrorMsg)
	require.True(t, ok)
	assert.Equal(t, "items", errMsg.Key)
	assert.EqualError(t, errMsg.Err, "server error")

	poolMsg, ok := batch[1]().(PoolUpdatedMsg)
	require.True(t, ok)
	assert.Equal(t, "items", poolMsg.Key)

	// Rolled back to pre-mutation state.
	assert.False(t, mp.Get().Data[0].Completed, "should have rolled back")
}

func TestMutatingPoolReconcilePrunesMutation(t *testing.T) {
	fetchCount := 0
	mp := NewMutatingPool("rc", PoolConfig{}, func(ctx context.Context) ([]testItem, error) {
		fetchCount++
		if fetchCount == 1 {
			return []testItem{{ID: 1}, {ID: 2}}, nil
		}
		// Second fetch: server now has item 1 completed.
		return []testItem{{ID: 1, Completed: true}, {ID: 2}}, nil
	})
	mp.Pool.Fetch(context.Background())()

	// Apply mutation — will be reflected in next fetch.
	cmd := mp.Apply(context.Background(), completeMutation{itemID: 1})
	cmd() // remote succeeds, re-fetch returns completed item

	// After reconcile, pending mutations should be empty
	// (the mutation is reflected in remote state).
	mp.mu.RLock()
	pendingCount := len(mp.pendingMutations)
	mp.mu.RUnlock()
	assert.Equal(t, 0, pendingCount)

	assert.True(t, mp.Get().Data[0].Completed)
}

func TestMutatingPoolReconcileReappliesPending(t *testing.T) {
	fetchCount := 0
	mp := NewMutatingPool("rc2", PoolConfig{}, func(ctx context.Context) ([]testItem, error) {
		fetchCount++
		// Server never reflects item 2 completion (simulates pending).
		return []testItem{{ID: 1, Completed: true}, {ID: 2}}, nil
	})
	mp.Pool.Fetch(context.Background())()

	// Apply mutation for item 2. Server won't reflect this.
	cmd := mp.Apply(context.Background(), completeMutation{itemID: 2})
	cmd()

	// Mutation for item 2 should still be pending.
	mp.mu.RLock()
	pendingCount := len(mp.pendingMutations)
	mp.mu.RUnlock()
	assert.Equal(t, 1, pendingCount)

	// But the data should still show item 2 as completed (re-applied).
	data := mp.Get().Data
	assert.True(t, data[0].Completed) // from server
	assert.True(t, data[1].Completed) // re-applied pending mutation
}

func TestMutatingPoolFetchReconciles(t *testing.T) {
	fetchCount := 0
	mp := NewMutatingPool("fetch-rc", PoolConfig{}, func(ctx context.Context) ([]testItem, error) {
		fetchCount++
		if fetchCount <= 2 {
			return []testItem{{ID: 1}, {ID: 2}}, nil
		}
		// Third fetch: server reflects the mutation.
		return []testItem{{ID: 1, Completed: true}, {ID: 2}}, nil
	})
	mp.Pool.Fetch(context.Background())()

	// Apply mutation (succeeds remotely, but re-fetch inside Apply
	// returns the old state because fetchCount==2).
	cmd := mp.Apply(context.Background(), completeMutation{itemID: 1})
	cmd()

	// Pending mutation should still exist (not reflected in fetch #2).
	mp.mu.RLock()
	pendingBefore := len(mp.pendingMutations)
	mp.mu.RUnlock()
	assert.Equal(t, 1, pendingBefore)

	// Now do a regular Fetch (MutatingPool.Fetch, which reconciles).
	mp.Fetch(context.Background())()

	// After fetch #3, the mutation is reflected — pending should be empty.
	mp.mu.RLock()
	pendingAfter := len(mp.pendingMutations)
	mp.mu.RUnlock()
	assert.Equal(t, 0, pendingAfter)
}

func TestMutatingPoolClearResetsMutationState(t *testing.T) {
	mp := newTestMutatingPool()
	mp.Pool.Fetch(context.Background())()
	mp.Apply(context.Background(), completeMutation{itemID: 1})

	mp.Clear()

	assert.False(t, mp.Get().HasData)
	mp.mu.RLock()
	assert.Empty(t, mp.pendingMutations)
	assert.False(t, mp.hasRemoteData)
	mp.mu.RUnlock()
}

func TestMutatingPoolApplyResetsErr(t *testing.T) {
	fetchCount := 0
	mp := NewMutatingPool("err-reset", PoolConfig{}, func(ctx context.Context) ([]testItem, error) {
		fetchCount++
		if fetchCount == 1 {
			return []testItem{{ID: 1}}, nil
		}
		return nil, errors.New("fetch failed")
	})
	// First fetch succeeds.
	mp.Pool.Fetch(context.Background())()
	// Second fetch fails — snapshot now carries an error.
	mp.Pool.Fetch(context.Background())()
	assert.Equal(t, StateError, mp.Get().State)
	assert.Error(t, mp.Get().Err)

	// Optimistic mutation should clear the error.
	mp.Apply(context.Background(), completeMutation{itemID: 1})
	snap := mp.Get()
	assert.Equal(t, StateFresh, snap.State)
	assert.NoError(t, snap.Err, "optimistic apply should clear Err")
}

func TestMutatingPoolRollbackResetsErr(t *testing.T) {
	fetchCount := 0
	mp := NewMutatingPool("rb-err", PoolConfig{}, func(ctx context.Context) ([]testItem, error) {
		fetchCount++
		if fetchCount == 1 {
			return []testItem{{ID: 1}}, nil
		}
		return nil, errors.New("fetch failed")
	})
	// Fetch success, then fetch error.
	mp.Pool.Fetch(context.Background())()
	mp.Pool.Fetch(context.Background())()
	assert.Error(t, mp.Get().Err)

	// Apply a mutation that fails remotely — rollback triggers.
	cmd := mp.Apply(context.Background(), completeMutation{
		itemID:    1,
		remoteErr: errors.New("remote fail"),
	})
	// Optimistic apply clears the error.
	assert.NoError(t, mp.Get().Err)

	// Now the rollback runs.
	cmd()
	snap := mp.Get()
	assert.Equal(t, StateFresh, snap.State)
	assert.NoError(t, snap.Err, "rollback should clear Err")
}

func TestMutatingPoolClearVsInflightReconcile(t *testing.T) {
	proceed := make(chan struct{})
	mp := NewMutatingPool("clear-rc", PoolConfig{}, func(ctx context.Context) ([]testItem, error) {
		<-proceed
		return []testItem{{ID: 1, Completed: true}}, nil
	})
	// Bootstrap data via Set so we can mutate it.
	mp.Set([]testItem{{ID: 1}})

	cmd := mp.Apply(context.Background(), completeMutation{itemID: 1})
	require.NotNil(t, cmd)

	// Clear the pool before the remote Cmd completes.
	mp.Clear()
	mp.Set([]testItem{{ID: 99}})

	// Now let the fetch inside Apply proceed.
	close(proceed)
	cmd()

	// The reconcile should have been discarded (generation mismatch).
	// Pool should still have the data from Set, not the reconciled data.
	snap := mp.Get()
	require.True(t, snap.HasData)
	require.Len(t, snap.Data, 1)
	assert.Equal(t, 99, snap.Data[0].ID)
}

func TestMutatingPoolClearVsInflightRollback(t *testing.T) {
	mp := NewMutatingPool("clear-rb", PoolConfig{}, func(ctx context.Context) ([]testItem, error) {
		return []testItem{{ID: 1}}, nil
	})
	mp.Set([]testItem{{ID: 1}})

	cmd := mp.Apply(context.Background(), completeMutation{
		itemID:    1,
		remoteErr: errors.New("fail"),
	})

	// Clear before the Cmd runs.
	mp.Clear()
	mp.Set([]testItem{{ID: 77}})

	// Cmd runs — remote fails, rollback fires but generation mismatches.
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	require.True(t, ok)
	require.Len(t, batch, 2)

	errMsg, ok := batch[0]().(MutationErrorMsg)
	require.True(t, ok)
	assert.EqualError(t, errMsg.Err, "fail")

	// Pool should still have the data from Set, not the rolled-back data.
	snap := mp.Get()
	require.True(t, snap.HasData)
	require.Len(t, snap.Data, 1)
	assert.Equal(t, 77, snap.Data[0].ID)
}

func TestMutatingPoolMultipleMutations(t *testing.T) {
	mp := NewMutatingPool("multi", PoolConfig{}, func(ctx context.Context) ([]testItem, error) {
		return []testItem{{ID: 1}, {ID: 2}, {ID: 3}}, nil
	})
	mp.Pool.Fetch(context.Background())()

	// Apply two mutations.
	cmd1 := mp.Apply(context.Background(), completeMutation{itemID: 1})
	cmd2 := mp.Apply(context.Background(), completeMutation{itemID: 3})

	data := mp.Get().Data
	assert.True(t, data[0].Completed)
	assert.False(t, data[1].Completed)
	assert.True(t, data[2].Completed)

	// Let both complete.
	cmd1()
	cmd2()
}
