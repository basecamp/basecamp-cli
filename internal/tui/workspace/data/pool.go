package data

import (
	"context"
	"log"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
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
	SetTerminalFocused(focused bool)
}

// Pool is a typed, self-refreshing data source.
// Go equivalent of iOS RemoteReadService / Android BaseApiRepository.
// One Pool per logical data set (projects, hey-activity, campfire-lines).
//
// The Pool does not subscribe or push — it's a typed cache with fetch
// capabilities. TEA's polling mechanism (PollMsg -> view calls FetchIfStale)
// drives the refresh cycle.
type Pool[T any] struct {
	mu              sync.RWMutex
	key             string
	snapshot        Snapshot[T]
	config          PoolConfig
	fetchFn         FetchFunc[T]
	version         uint64 // incremented on every data change
	generation      uint64 // incremented on Clear, used to discard stale fetches
	fetching        bool
	pushMode        bool // when true, extend poll intervals (SSE connected)
	missCount       int
	focused         bool
	terminalFocused bool // false when the terminal window has lost OS focus
	metrics         *PoolMetrics
	cache           *PoolCache
	cachedFetchedAt time.Time // real FetchedAt from disk cache (for accurate age display)

	// Cumulative counters for observability (separate from missCount backoff state).
	cumulativeHits   int
	cumulativeMisses int
}

// NewPool creates a Pool with the given key, config, and fetch function.
func NewPool[T any](key string, config PoolConfig, fetchFn FetchFunc[T]) *Pool[T] {
	return &Pool[T]{
		key:             key,
		config:          config,
		fetchFn:         fetchFn,
		focused:         true,
		terminalFocused: true,
	}
}

// Key returns the pool's identifier.
func (p *Pool[T]) Key() string { return p.key }

// SetCache sets the disk cache for this pool.
// If cached data exists, seeds the snapshot as Stale for SWR boot.
// FetchedAt is set to now (not the original fetch time) so the data
// falls within the stale window and isn't immediately expired by Get().
func (p *Pool[T]) SetCache(c *PoolCache) {
	if c == nil {
		return
	}
	// Load from disk outside the lock to avoid blocking readers during I/O.
	var cachedData T
	fetchedAt, ok := c.Load(p.key, &cachedData)

	p.mu.Lock()
	p.cache = c
	m := p.metrics
	seeded := false
	// Only seed from cache if pool has no data yet.
	if ok && !p.snapshot.HasData {
		p.snapshot.Data = cachedData
		p.snapshot.State = StateStale
		p.snapshot.FetchedAt = time.Now() // anchor TTL to now so data isn't immediately expired
		p.snapshot.HasData = true
		p.cachedFetchedAt = fetchedAt // preserve real time for age display
		p.version++
		seeded = true
	}
	key := p.key
	p.mu.Unlock()
	if seeded && m != nil {
		m.Record(PoolEvent{Timestamp: time.Now(), PoolKey: key, EventType: CacheSeeded})
	}
}

// SetMetrics sets the metrics collector for this pool.
func (p *Pool[T]) SetMetrics(m *PoolMetrics) {
	p.mu.Lock()
	p.metrics = m
	p.mu.Unlock()
	// Register outside pool lock to avoid lock-order inversion.
	if m != nil {
		m.RegisterPool(p.key, p.Status)
	}
}

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
		p.mu.RLock()
		m := p.metrics
		fetchKey := p.key
		p.mu.RUnlock()

		if m != nil {
			m.Record(PoolEvent{Timestamp: time.Now(), PoolKey: fetchKey, EventType: FetchStart})
		}
		start := time.Now()
		data, err := p.fetchFn(ctx)
		elapsed := time.Since(start)

		if m != nil {
			ev := PoolEvent{Timestamp: time.Now(), PoolKey: fetchKey, Duration: elapsed}
			if err != nil {
				ev.EventType = FetchError
				ev.Detail = err.Error()
			} else {
				ev.EventType = FetchComplete
			}
			m.Record(ev)
		}

		p.mu.Lock()
		p.fetching = false

		// Discard result if pool was cleared while fetching.
		if p.generation != gen {
			p.mu.Unlock()
			return nil
		}

		var cacheData any
		var cacheFetchedAt time.Time
		var cacheRef *PoolCache
		var cacheKey string

		if err != nil {
			p.snapshot.State = StateError
			p.snapshot.Err = err
		} else {
			p.snapshot.Data = data
			p.snapshot.State = StateFresh
			p.snapshot.FetchedAt = time.Now()
			p.snapshot.HasData = true
			p.snapshot.Err = nil
			p.cachedFetchedAt = time.Time{} // real fetch replaces cache-seeded data
			p.version++
			if p.cache != nil {
				cacheRef = p.cache
				cacheKey = p.key
				cacheData = data
				cacheFetchedAt = p.snapshot.FetchedAt
			}
		}
		p.mu.Unlock()

		// Disk I/O outside the lock to avoid blocking readers.
		if cacheRef != nil {
			if err := cacheRef.Save(cacheKey, cacheData, cacheFetchedAt); err != nil {
				log.Printf("pool cache save %s: %v", cacheKey, err)
			}
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
	invalidated := p.snapshot.HasData && p.snapshot.State == StateFresh
	if invalidated {
		p.snapshot.State = StateStale
	}
	m := p.metrics
	key := p.key
	p.mu.Unlock()
	if invalidated && m != nil {
		m.Record(PoolEvent{Timestamp: time.Now(), PoolKey: key, EventType: PoolInvalidated})
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
	p.cachedFetchedAt = time.Time{}
	p.version++
}

// Clear resets the pool to its initial empty state.
func (p *Pool[T]) Clear() {
	p.mu.Lock()
	m := p.metrics
	p.clearLocked()
	p.mu.Unlock()
	// Unregister outside pool lock to avoid lock-order inversion.
	if m != nil {
		m.UnregisterPool(p.key)
	}
}

func (p *Pool[T]) clearLocked() {
	var zero T
	p.snapshot = Snapshot[T]{Data: zero}
	p.cachedFetchedAt = time.Time{}
	p.version++
	p.generation++
	p.fetching = false
}

// SetPollConfig replaces the pool's timing configuration.
// Does NOT bump generation or return a Cmd — timer invalidation is the
// caller's responsibility (bump view-side pollGen, re-arm schedulePoll).
func (p *Pool[T]) SetPollConfig(cfg PoolConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config = cfg
	p.missCount = 0 // reset backoff so new config takes effect immediately
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
	m := p.metrics
	key := p.key
	p.missCount = 0
	p.cumulativeHits++
	p.mu.Unlock()
	if m != nil {
		m.Record(PoolEvent{Timestamp: time.Now(), PoolKey: key, EventType: CacheHit})
	}
}

// RecordMiss increments the miss counter for adaptive backoff.
func (p *Pool[T]) RecordMiss() {
	p.mu.Lock()
	m := p.metrics
	key := p.key
	p.missCount++
	p.cumulativeMisses++
	p.mu.Unlock()
	if m != nil {
		m.Record(PoolEvent{Timestamp: time.Now(), PoolKey: key, EventType: CacheMiss})
	}
}

// SetTerminalFocused marks whether the terminal window has OS focus.
// When false, poll intervals are extended 4× to reduce background load.
func (p *Pool[T]) SetTerminalFocused(focused bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.terminalFocused = focused
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

// Status returns a live status snapshot for the metrics panel.
func (p *Pool[T]) Status() PoolStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var fetchCount, errorCount int
	var avgLatency time.Duration
	if p.metrics != nil {
		p.metrics.mu.RLock()
		if s, ok := p.metrics.stats[p.key]; ok {
			fetchCount = s.FetchCount
			errorCount = s.ErrorCount
			if s.FetchCount > 0 {
				avgLatency = time.Duration(s.TotalTimeMs/int64(s.FetchCount)) * time.Millisecond
			}
		}
		p.metrics.mu.RUnlock()
	}

	return PoolStatus{
		Key:             p.key,
		State:           p.snapshot.State,
		Fetching:        p.fetching,
		FetchedAt:       p.snapshot.FetchedAt,
		CachedFetchedAt: p.cachedFetchedAt,
		PollInterval:    p.pollInterval(),
		HitCount:        p.cumulativeHits,
		MissCount:       p.cumulativeMisses,
		FetchCount:      fetchCount,
		ErrorCount:      errorCount,
		AvgLatency:      avgLatency,
	}
}

func (p *Pool[T]) pollInterval() time.Duration {
	if p.config.PollBase == 0 {
		return 0
	}
	base := p.config.PollBase
	if !p.focused && p.config.PollBg > 0 {
		base = p.config.PollBg
	}
	if !p.terminalFocused {
		base *= 4
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
