package data

import (
	"sync"
	"time"
)

// PoolEventType classifies pool fetch events.
type PoolEventType int

const (
	FetchStart PoolEventType = iota
	FetchComplete
	FetchError
)

// PoolEvent records a single pool fetch event.
type PoolEvent struct {
	Timestamp time.Time
	PoolKey   string
	EventType PoolEventType
	Duration  time.Duration
	DataSize  int
}

// PoolStats holds aggregate statistics for a single pool.
type PoolStats struct {
	FetchCount  int
	ErrorCount  int
	TotalTimeMs int64
	LastFetch   time.Time
}

// NavigationEvent records a view navigation with data quality.
type NavigationEvent struct {
	Timestamp time.Time
	ViewTitle string
	PoolKey   string
	Quality   float64 // 1.0=Fresh, 0.5=Stale, 0.0=Empty
}

// MetricsSummary provides a point-in-time snapshot of pool health.
type MetricsSummary struct {
	ActivePools int
	P50Latency  time.Duration
	ErrorRate   float64
	Apdex       float64
}

// PoolStatus is a live status snapshot from a registered pool.
type PoolStatus struct {
	Key          string
	State        SnapshotState
	FetchedAt    time.Time
	PollInterval time.Duration
	HitCount     int
	MissCount    int
	FetchCount   int
	ErrorCount   int
	AvgLatency   time.Duration
}

// PoolMetrics collects pool fetch telemetry for status bar display.
type PoolMetrics struct {
	mu     sync.RWMutex
	events []PoolEvent // ring buffer, last 100
	stats  map[string]*PoolStats
	navLog []NavigationEvent // last 20 navigations

	reporters map[string]func() PoolStatus // registered pool reporters
}

// NewPoolMetrics creates an empty metrics collector.
func NewPoolMetrics() *PoolMetrics {
	return &PoolMetrics{
		stats:     make(map[string]*PoolStats),
		reporters: make(map[string]func() PoolStatus),
	}
}

const maxEvents = 100
const maxNavLog = 20

// Record adds a pool event to the ring buffer and updates stats.
func (m *PoolMetrics) Record(e PoolEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.events) >= maxEvents {
		m.events = m.events[1:]
	}
	m.events = append(m.events, e)

	if e.EventType == FetchComplete || e.EventType == FetchError {
		s, ok := m.stats[e.PoolKey]
		if !ok {
			s = &PoolStats{}
			m.stats[e.PoolKey] = s
		}
		s.FetchCount++
		s.TotalTimeMs += e.Duration.Milliseconds()
		s.LastFetch = e.Timestamp
		if e.EventType == FetchError {
			s.ErrorCount++
		}
	}
}

// RecordNavigation logs a view navigation with data quality.
func (m *PoolMetrics) RecordNavigation(e NavigationEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.navLog) >= maxNavLog {
		m.navLog = m.navLog[1:]
	}
	m.navLog = append(m.navLog, e)
}

// Summary returns aggregate metrics for status bar display.
func (m *PoolMetrics) Summary() MetricsSummary {
	m.mu.RLock()
	defer m.mu.RUnlock()

	summary := MetricsSummary{
		ActivePools: len(m.stats),
	}

	// Compute p50 latency from recent FetchComplete events
	var latencies []time.Duration
	var errors int
	var total int
	for i := len(m.events) - 1; i >= 0 && len(latencies) < 50; i-- {
		e := m.events[i]
		switch e.EventType {
		case FetchComplete:
			latencies = append(latencies, e.Duration)
			total++
		case FetchError:
			errors++
			total++
		default:
			// FetchStart events are not counted toward latency/error metrics
		}
	}

	if len(latencies) > 0 {
		sortDurations(latencies)
		summary.P50Latency = latencies[len(latencies)/2]
	}
	if total > 0 {
		summary.ErrorRate = float64(errors) / float64(total)
	}

	summary.Apdex = m.apdex()
	return summary
}

// Apdex returns the navigation quality score (0.0-1.0).
// Fresh = satisfied (1.0), Stale = tolerating (0.5), Empty = frustrated (0.0).
func (m *PoolMetrics) Apdex() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.apdex()
}

func (m *PoolMetrics) apdex() float64 {
	if len(m.navLog) == 0 {
		return 1.0
	}
	var sum float64
	for _, n := range m.navLog {
		sum += n.Quality
	}
	return sum / float64(len(m.navLog))
}

// RegisterPool adds a live status reporter for a pool.
func (m *PoolMetrics) RegisterPool(key string, reporter func() PoolStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reporters[key] = reporter
}

// UnregisterPool removes a pool's status reporter.
func (m *PoolMetrics) UnregisterPool(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.reporters, key)
}

// PoolStatsList returns live status from all registered pools.
// Copies the reporter slice under lock, then invokes reporters
// without holding the metrics lock to avoid lock-order inversion
// with pool mutexes.
func (m *PoolMetrics) PoolStatsList() []PoolStatus {
	m.mu.RLock()
	reporters := make([]func() PoolStatus, 0, len(m.reporters))
	for _, r := range m.reporters {
		reporters = append(reporters, r)
	}
	m.mu.RUnlock()

	statuses := make([]PoolStatus, 0, len(reporters))
	for _, r := range reporters {
		ps := r()
		if ps.FetchedAt.IsZero() {
			continue // registered but never fetched
		}
		statuses = append(statuses, ps)
	}
	sortPoolStatuses(statuses)
	return statuses
}

// sortPoolStatuses sorts by Key for stable table ordering.
func sortPoolStatuses(s []PoolStatus) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Key < s[j-1].Key; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// sortDurations sorts a slice of durations in ascending order.
func sortDurations(d []time.Duration) {
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && d[j] < d[j-1]; j-- {
			d[j], d[j-1] = d[j-1], d[j]
		}
	}
}
