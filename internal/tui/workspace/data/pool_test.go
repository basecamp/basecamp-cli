package data

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPoolGetEmpty(t *testing.T) {
	p := NewPool("test", PoolConfig{}, func(ctx context.Context) (int, error) {
		return 0, nil
	})
	snap := p.Get()
	assert.Equal(t, StateEmpty, snap.State)
	assert.False(t, snap.HasData)
}

func TestPoolFetchSuccess(t *testing.T) {
	p := NewPool("items", PoolConfig{FreshTTL: time.Minute}, func(ctx context.Context) ([]string, error) {
		return []string{"a", "b"}, nil
	})

	cmd := p.Fetch(context.Background())
	require.NotNil(t, cmd)

	msg := cmd()
	assert.Equal(t, PoolUpdatedMsg{Key: "items"}, msg)

	snap := p.Get()
	assert.Equal(t, StateFresh, snap.State)
	assert.True(t, snap.HasData)
	assert.Equal(t, []string{"a", "b"}, snap.Data)
	assert.Equal(t, uint64(1), p.Version())
}

func TestPoolFetchError(t *testing.T) {
	fetchErr := errors.New("network down")
	p := NewPool("items", PoolConfig{}, func(ctx context.Context) (int, error) {
		return 0, fetchErr
	})

	msg := p.Fetch(context.Background())()
	assert.Equal(t, PoolUpdatedMsg{Key: "items"}, msg)

	snap := p.Get()
	assert.Equal(t, StateError, snap.State)
	assert.False(t, snap.HasData)
	assert.Equal(t, fetchErr, snap.Err)
}

func TestPoolFetchErrorPreservesExistingData(t *testing.T) {
	calls := 0
	p := NewPool("items", PoolConfig{}, func(ctx context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "good", nil
		}
		return "", errors.New("fail")
	})

	// First fetch: success
	p.Fetch(context.Background())()
	assert.Equal(t, "good", p.Get().Data)

	// Second fetch: error — data preserved
	p.Fetch(context.Background())()
	snap := p.Get()
	assert.Equal(t, StateError, snap.State)
	assert.True(t, snap.HasData)
	assert.Equal(t, "good", snap.Data)
}

func TestPoolFetchDedup(t *testing.T) {
	var count atomic.Int32
	started := make(chan struct{})
	proceed := make(chan struct{})

	p := NewPool("slow", PoolConfig{}, func(ctx context.Context) (int, error) {
		count.Add(1)
		close(started)
		<-proceed
		return 42, nil
	})

	// Start first fetch.
	cmd1 := p.Fetch(context.Background())
	require.NotNil(t, cmd1)

	// Run cmd1 in background, wait for it to enter fetchFn.
	go cmd1()
	<-started

	// Second Fetch while first is in-flight returns nil (deduped).
	cmd2 := p.Fetch(context.Background())
	assert.Nil(t, cmd2)

	close(proceed)
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, int32(1), count.Load())
}

func TestPoolFetchIfStale(t *testing.T) {
	p := NewPool("ttl", PoolConfig{FreshTTL: 50 * time.Millisecond}, func(ctx context.Context) (int, error) {
		return 1, nil
	})

	// Empty pool: should fetch.
	cmd := p.FetchIfStale(context.Background())
	require.NotNil(t, cmd)
	cmd()

	// Fresh: should not fetch.
	assert.Nil(t, p.FetchIfStale(context.Background()))

	// Wait for TTL to expire.
	time.Sleep(60 * time.Millisecond)
	cmd = p.FetchIfStale(context.Background())
	assert.NotNil(t, cmd)
}

func TestPoolFetchIfStaleNoTTL(t *testing.T) {
	p := NewPool("no-ttl", PoolConfig{}, func(ctx context.Context) (int, error) {
		return 1, nil
	})
	p.Fetch(context.Background())()

	// FreshTTL == 0 means "no expiry" — data stays fresh.
	assert.Nil(t, p.FetchIfStale(context.Background()))
}

// Regression: proves that poll-driven FetchIfStale actually triggers a second
// fetch after TTL expiry — caught the campfire "forever fresh" bug where
// FreshTTL was 0 but the view polled with FetchIfStale.
func TestPoolPollTriggersRefetchAfterTTL(t *testing.T) {
	var fetchCount atomic.Int32
	p := NewPool("poll-ttl", PoolConfig{
		FreshTTL: 20 * time.Millisecond,
		PollBase: 30 * time.Millisecond,
	}, func(ctx context.Context) (int, error) {
		fetchCount.Add(1)
		return int(fetchCount.Load()), nil
	})

	// Initial fetch.
	cmd := p.Fetch(context.Background())
	require.NotNil(t, cmd)
	cmd()
	assert.Equal(t, int32(1), fetchCount.Load())
	assert.Equal(t, 1, p.Get().Data)

	// Immediately after: FetchIfStale returns nil (data is fresh).
	assert.Nil(t, p.FetchIfStale(context.Background()))

	// Wait for FreshTTL to expire, simulating a poll tick.
	time.Sleep(25 * time.Millisecond)

	// Now FetchIfStale should trigger a real fetch.
	cmd = p.FetchIfStale(context.Background())
	require.NotNil(t, cmd, "FetchIfStale must trigger after FreshTTL expiry")
	cmd()
	assert.Equal(t, int32(2), fetchCount.Load())
	assert.Equal(t, 2, p.Get().Data)
}

func TestPoolInvalidate(t *testing.T) {
	p := NewPool("inv", PoolConfig{}, func(ctx context.Context) (int, error) {
		return 1, nil
	})
	p.Fetch(context.Background())()
	assert.Equal(t, StateFresh, p.Get().State)

	p.Invalidate()
	assert.Equal(t, StateStale, p.Get().State)

	// FetchIfStale should now return a Cmd.
	assert.NotNil(t, p.FetchIfStale(context.Background()))
}

func TestPoolSet(t *testing.T) {
	p := NewPool[string]("direct", PoolConfig{}, nil)
	p.Set("prefetched")

	snap := p.Get()
	assert.Equal(t, StateFresh, snap.State)
	assert.True(t, snap.HasData)
	assert.Equal(t, "prefetched", snap.Data)
	assert.Equal(t, uint64(1), p.Version())
}

func TestPoolClear(t *testing.T) {
	p := NewPool("clr", PoolConfig{}, func(ctx context.Context) (int, error) {
		return 42, nil
	})
	p.Fetch(context.Background())()
	assert.True(t, p.Get().HasData)

	p.Clear()
	snap := p.Get()
	assert.Equal(t, StateEmpty, snap.State)
	assert.False(t, snap.HasData)
}

func TestPoolClearDiscardsInFlightFetch(t *testing.T) {
	proceed := make(chan struct{})
	p := NewPool("gen", PoolConfig{}, func(ctx context.Context) (int, error) {
		<-proceed
		return 99, nil
	})

	cmd := p.Fetch(context.Background())
	require.NotNil(t, cmd)

	// Clear before the fetch completes — generation changes.
	p.Clear()
	p.Set(1)

	close(proceed)
	msg := cmd()

	// The stale fetch result should be discarded (nil message).
	assert.Nil(t, msg)
	// Pool still has the data from Set.
	assert.Equal(t, 1, p.Get().Data)
}

func TestPoolTTLBasedStateTransition(t *testing.T) {
	p := NewPool("ttl", PoolConfig{FreshTTL: 20 * time.Millisecond}, func(ctx context.Context) (int, error) {
		return 1, nil
	})
	p.Fetch(context.Background())()

	// Immediately fresh.
	assert.Equal(t, StateFresh, p.Get().State)

	// After TTL, Get() returns stale.
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, StateStale, p.Get().State)
}

func TestPoolPollInterval(t *testing.T) {
	p := NewPool[int]("poll", PoolConfig{
		PollBase: 10 * time.Second,
		PollBg:   30 * time.Second,
		PollMax:  2 * time.Minute,
	}, nil)

	// Focused, no misses.
	assert.Equal(t, 10*time.Second, p.PollInterval())

	// One miss: doubles.
	p.RecordMiss()
	assert.Equal(t, 20*time.Second, p.PollInterval())

	// Two misses: doubles again.
	p.RecordMiss()
	assert.Equal(t, 40*time.Second, p.PollInterval())

	// Hit resets.
	p.RecordHit()
	assert.Equal(t, 10*time.Second, p.PollInterval())

	// Blurred: uses PollBg.
	p.SetFocused(false)
	assert.Equal(t, 30*time.Second, p.PollInterval())

	// Push mode: 10x extension.
	p.SetFocused(true)
	p.SetPushMode(true)
	assert.Equal(t, 100*time.Second, p.PollInterval())
}

func TestPoolPollIntervalMaxCap(t *testing.T) {
	p := NewPool[int]("cap", PoolConfig{
		PollBase: time.Second,
		PollMax:  5 * time.Second,
	}, nil)

	for range 10 {
		p.RecordMiss()
	}
	assert.Equal(t, 5*time.Second, p.PollInterval())
}

func TestPoolPollIntervalZeroBase(t *testing.T) {
	p := NewPool[int]("no-poll", PoolConfig{}, nil)
	assert.Equal(t, time.Duration(0), p.PollInterval())
}

func TestPoolStaleTTLExpiry(t *testing.T) {
	p := NewPool("stale", PoolConfig{
		FreshTTL: 20 * time.Millisecond,
		StaleTTL: 30 * time.Millisecond,
	}, func(ctx context.Context) (string, error) {
		return "data", nil
	})
	p.Fetch(context.Background())()

	// Immediately fresh.
	snap := p.Get()
	assert.Equal(t, StateFresh, snap.State)
	assert.True(t, snap.HasData)

	// After FreshTTL but before StaleTTL: stale but usable.
	time.Sleep(25 * time.Millisecond)
	snap = p.Get()
	assert.Equal(t, StateStale, snap.State)
	assert.True(t, snap.HasData)
	assert.Equal(t, "data", snap.Data)

	// After FreshTTL + StaleTTL: expired, data gone.
	time.Sleep(30 * time.Millisecond)
	snap = p.Get()
	assert.Equal(t, StateEmpty, snap.State)
	assert.False(t, snap.HasData)
}

func TestPoolStaleTTLZeroMeansNoExpiry(t *testing.T) {
	p := NewPool("no-expiry", PoolConfig{
		FreshTTL: 10 * time.Millisecond,
		StaleTTL: 0, // zero = stale forever
	}, func(ctx context.Context) (string, error) {
		return "persistent", nil
	})
	p.Fetch(context.Background())()

	// Wait well past FreshTTL — should become stale but never expire.
	time.Sleep(50 * time.Millisecond)
	snap := p.Get()
	assert.Equal(t, StateStale, snap.State)
	assert.True(t, snap.HasData)
	assert.Equal(t, "persistent", snap.Data)
}

func TestPoolFetchSetsLoading(t *testing.T) {
	proceed := make(chan struct{})
	p := NewPool("load", PoolConfig{}, func(ctx context.Context) (int, error) {
		<-proceed
		return 1, nil
	})
	// Pre-set data so Loading state is observable.
	p.Set(0)

	cmd := p.Fetch(context.Background())
	require.NotNil(t, cmd)

	// After Fetch call but before Cmd completes, state should be Loading.
	assert.Equal(t, StateLoading, p.Get().State)
	assert.True(t, p.Get().HasData) // prior data preserved

	close(proceed)
	cmd()
	assert.Equal(t, StateFresh, p.Get().State)
}
