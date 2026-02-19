package data

import (
	"context"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// PoolUpdatedMsg is sent when a pool's snapshot changes.
// Views match on Key to identify which pool updated, then read
// typed data via the pool's Get() method.
type PoolUpdatedMsg struct {
	Key string
}

// FetchFunc retrieves data for a pool.
type FetchFunc[T any] func(ctx context.Context) (T, error)

// PoolConfig configures a Pool's timing behavior.
type PoolConfig struct {
	FreshTTL time.Duration // how long data is "fresh" (0 = no expiry)
	StaleTTL time.Duration // how long stale data is served during revalidation
	PollBase time.Duration // base polling interval when focused (0 = no auto-poll)
	PollBg   time.Duration // background polling interval when blurred
	PollMax  time.Duration // max interval after consecutive misses
}

// Pooler is the non-generic interface for pool lifecycle management.
// Realm uses this to manage pools of different types uniformly.
type Pooler interface {
	Invalidate()
	Clear()
}

// Pool is a typed, self-refreshing data source.
// Go equivalent of iOS RemoteReadService / Android BaseApiRepository.
// One Pool per logical data set (projects, hey-activity, campfire-lines).
//
// The Pool does not subscribe or push — it's a typed cache with fetch
// capabilities. TEA's polling mechanism (PollMsg -> view calls FetchIfStale)
// drives the refresh cycle.
type Pool[T any] struct {
	mu         sync.RWMutex
	key        string
	snapshot   Snapshot[T]
	config     PoolConfig
	fetchFn    FetchFunc[T]
	version    uint64 // incremented on every data change
	generation uint64 // incremented on Clear, used to discard stale fetches
	fetching   bool
	pushMode   bool // when true, extend poll intervals (SSE connected)
	missCount  int
	focused    bool
}

// NewPool creates a Pool with the given key, config, and fetch function.
func NewPool[T any](key string, config PoolConfig, fetchFn FetchFunc[T]) *Pool[T] {
	return &Pool[T]{
		key:     key,
		config:  config,
		fetchFn: fetchFn,
		focused: true,
	}
}

// Key returns the pool's identifier.
func (p *Pool[T]) Key() string { return p.key }

// Get returns the current snapshot. Never blocks.
// Recalculates state based on TTL — a snapshot stored as Fresh may
// be returned as Stale if FreshTTL has elapsed, or expired (HasData=false)
// if StaleTTL has also elapsed.
func (p *Pool[T]) Get() Snapshot[T] {
	p.mu.RLock()
	defer p.mu.RUnlock()
	snap := p.snapshot
	if snap.HasData && p.config.FreshTTL > 0 {
		age := time.Since(snap.FetchedAt)
		if age >= p.config.FreshTTL {
			if p.config.StaleTTL > 0 && age >= p.config.FreshTTL+p.config.StaleTTL {
				// Data has expired — no longer usable even as stale.
				var zero T
				snap.Data = zero
				snap.HasData = false
				snap.State = StateEmpty
			} else if snap.State == StateFresh {
				snap.State = StateStale
			}
		}
	}
	return snap
}

// Version returns the current data version.
func (p *Pool[T]) Version() uint64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.version
}

// Fetch returns a Cmd that fetches fresh data and emits PoolUpdatedMsg.
// Concurrent fetches are deduped — returns nil if a fetch is in progress.
func (p *Pool[T]) Fetch(ctx context.Context) tea.Cmd {
	p.mu.Lock()
	if p.fetching {
		p.mu.Unlock()
		return nil
	}
	p.fetching = true
	gen := p.generation
	if p.snapshot.HasData {
		p.snapshot.State = StateLoading
	}
	p.mu.Unlock()

	return func() tea.Msg {
		data, err := p.fetchFn(ctx)

		p.mu.Lock()
		defer p.mu.Unlock()
		p.fetching = false

		// Discard result if pool was cleared while fetching.
		if p.generation != gen {
			return nil
		}
		if err != nil {
			p.snapshot.State = StateError
			p.snapshot.Err = err
		} else {
			p.snapshot.Data = data
			p.snapshot.State = StateFresh
			p.snapshot.FetchedAt = time.Now()
			p.snapshot.HasData = true
			p.snapshot.Err = nil
			p.version++
		}
		return PoolUpdatedMsg{Key: p.key}
	}
}

// FetchIfStale returns a Fetch Cmd if data is stale or empty, nil if fresh.
func (p *Pool[T]) FetchIfStale(ctx context.Context) tea.Cmd {
	if p.isFreshOrFetching() {
		return nil
	}
	return p.Fetch(ctx)
}

// isFreshOrFetching returns true if the data is fresh or a fetch is in progress.
func (p *Pool[T]) isFreshOrFetching() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.fetching {
		return true
	}
	if !p.snapshot.HasData {
		return false
	}
	if p.snapshot.State == StateError || p.snapshot.State == StateStale {
		return false
	}
	if p.snapshot.State != StateFresh {
		return false
	}
	return p.config.FreshTTL == 0 || time.Since(p.snapshot.FetchedAt) < p.config.FreshTTL
}

// Invalidate marks current data as stale. Next FetchIfStale will re-fetch.
func (p *Pool[T]) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.snapshot.HasData && p.snapshot.State == StateFresh {
		p.snapshot.State = StateStale
	}
}

// Set writes data directly into the pool (for prefetch / dual-write patterns).
func (p *Pool[T]) Set(data T) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshot.Data = data
	p.snapshot.State = StateFresh
	p.snapshot.FetchedAt = time.Now()
	p.snapshot.HasData = true
	p.snapshot.Err = nil
	p.version++
}

// Clear resets the pool to its initial empty state.
func (p *Pool[T]) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clearLocked()
}

func (p *Pool[T]) clearLocked() {
	var zero T
	p.snapshot = Snapshot[T]{Data: zero}
	p.version++
	p.generation++
	p.fetching = false
}

// SetPushMode enables/disables push mode (SSE connected).
// In push mode, poll intervals are extended significantly.
func (p *Pool[T]) SetPushMode(enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pushMode = enabled
}

// RecordHit resets the miss counter (new data arrived).
func (p *Pool[T]) RecordHit() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.missCount = 0
}

// RecordMiss increments the miss counter for adaptive backoff.
func (p *Pool[T]) RecordMiss() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.missCount++
}

// SetFocused marks whether the view consuming this pool has focus.
func (p *Pool[T]) SetFocused(focused bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.focused = focused
	if focused {
		p.missCount = 0
	}
}

// PollInterval returns the current recommended polling interval,
// accounting for focus state, push mode, and miss backoff.
func (p *Pool[T]) PollInterval() time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pollInterval()
}

func (p *Pool[T]) pollInterval() time.Duration {
	if p.config.PollBase == 0 {
		return 0
	}
	base := p.config.PollBase
	if !p.focused && p.config.PollBg > 0 {
		base = p.config.PollBg
	}
	if p.pushMode {
		base *= 10
	}
	interval := base
	for range p.missCount {
		interval *= 2
		if p.config.PollMax > 0 && interval >= p.config.PollMax {
			return p.config.PollMax
		}
	}
	return interval
}
