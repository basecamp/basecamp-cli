// Package observability provides metrics collection and tracing for CLI operations.
package observability

import (
	"fmt"
	"sync"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// RequestMetrics holds timing and status information for a single HTTP request.
type RequestMetrics struct {
	Method     string
	URL        string
	Attempt    int
	StatusCode int
	Duration   time.Duration
	FromCache  bool
	Retryable  bool
	Error      error
}

// OperationMetrics holds timing information for a high-level SDK operation.
type OperationMetrics struct {
	Service      string // e.g., "Todos", "Projects"
	Operation    string // e.g., "Complete", "List"
	ResourceType string // e.g., "todo", "project"
	IsMutation   bool
	BucketID     int64
	ResourceID   int64
	Duration     time.Duration
	Error        error
}

// RetryMetrics records a retry event.
type RetryMetrics struct {
	Method  string
	URL     string
	Attempt int
	Error   error
}

// SessionMetrics aggregates metrics for an entire CLI session.
type SessionMetrics struct {
	StartTime       time.Time
	EndTime         time.Time
	TotalRequests   int
	CacheHits       int
	CacheMisses     int
	TotalOperations int
	FailedOps       int
	TotalRetries    int
	TotalLatency    time.Duration
}

// SessionCollector accumulates metrics across a CLI session.
// It is safe for concurrent use and uses counters instead of unbounded slices.
type SessionCollector struct {
	mu sync.Mutex

	startTime       time.Time
	totalRequests   int
	cacheHits       int
	cacheMisses     int
	totalOperations int
	failedOps       int
	totalRetries    int
	totalLatency    time.Duration
}

// NewSessionCollector creates a new SessionCollector.
func NewSessionCollector() *SessionCollector {
	return &SessionCollector{
		startTime: time.Now(),
	}
}

// RecordRequest records metrics for an HTTP request.
func (c *SessionCollector) RecordRequest(m RequestMetrics) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totalRequests++
	c.totalLatency += m.Duration
	if m.FromCache {
		c.cacheHits++
	} else {
		c.cacheMisses++
	}
}

// RecordRequestFromSDK records metrics from SDK types.
func (c *SessionCollector) RecordRequestFromSDK(info basecamp.RequestInfo, result basecamp.RequestResult) {
	c.RecordRequest(RequestMetrics{
		Method:     info.Method,
		URL:        info.URL,
		Attempt:    info.Attempt,
		StatusCode: result.StatusCode,
		Duration:   result.Duration,
		FromCache:  result.FromCache,
		Retryable:  result.Retryable,
		Error:      result.Error,
	})
}

// RecordOperation records metrics for a high-level operation.
func (c *SessionCollector) RecordOperation(m OperationMetrics) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totalOperations++
	if m.Error != nil {
		c.failedOps++
	}
}

// RecordOperationFromSDK records metrics from SDK types.
func (c *SessionCollector) RecordOperationFromSDK(op basecamp.OperationInfo, err error, duration time.Duration) {
	c.RecordOperation(OperationMetrics{
		Service:      op.Service,
		Operation:    op.Operation,
		ResourceType: op.ResourceType,
		IsMutation:   op.IsMutation,
		BucketID:     op.BucketID,
		ResourceID:   op.ResourceID,
		Duration:     duration,
		Error:        err,
	})
}

// RecordRetry records a retry event.
func (c *SessionCollector) RecordRetry(_ RetryMetrics) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totalRetries++
}

// RecordRetryFromSDK records a retry event from SDK types.
func (c *SessionCollector) RecordRetryFromSDK(info basecamp.RequestInfo, attempt int, err error) {
	c.RecordRetry(RetryMetrics{
		Method:  info.Method,
		URL:     info.URL,
		Attempt: attempt,
		Error:   err,
	})
}

// Summary returns aggregated metrics for the session.
func (c *SessionCollector) Summary() SessionMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()

	return SessionMetrics{
		StartTime:       c.startTime,
		EndTime:         time.Now(),
		TotalRequests:   c.totalRequests,
		CacheHits:       c.cacheHits,
		CacheMisses:     c.cacheMisses,
		TotalOperations: c.totalOperations,
		FailedOps:       c.failedOps,
		TotalRetries:    c.totalRetries,
		TotalLatency:    c.totalLatency,
	}
}

// Reset clears all collected metrics and resets the start time.
func (c *SessionCollector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.startTime = time.Now()
	c.totalRequests = 0
	c.cacheHits = 0
	c.cacheMisses = 0
	c.totalOperations = 0
	c.failedOps = 0
	c.totalRetries = 0
	c.totalLatency = 0
}

// FormatParts returns the formatted parts for a stats summary line.
// The parts are: duration, requests, cached, retries, failed.
// Zero-value fields are omitted (except duration which is always included).
func (m *SessionMetrics) FormatParts() []string {
	var parts []string

	// Duration (always included)
	duration := m.EndTime.Sub(m.StartTime)
	if duration < time.Second {
		parts = append(parts, fmt.Sprintf("%dms", duration.Milliseconds()))
	} else {
		parts = append(parts, fmt.Sprintf("%.1fs", duration.Seconds()))
	}

	// Requests
	if m.TotalRequests > 0 {
		if m.TotalRequests == 1 {
			parts = append(parts, "1 request")
		} else {
			parts = append(parts, fmt.Sprintf("%d requests", m.TotalRequests))
		}
	}

	// Cache hits
	if m.CacheHits > 0 {
		rate := 0.0
		if m.TotalRequests > 0 {
			rate = float64(m.CacheHits) / float64(m.TotalRequests) * 100
		}
		parts = append(parts, fmt.Sprintf("%d cached (%.0f%%)", m.CacheHits, rate))
	}

	// Retries
	if m.TotalRetries > 0 {
		if m.TotalRetries == 1 {
			parts = append(parts, "1 retry")
		} else {
			parts = append(parts, fmt.Sprintf("%d retries", m.TotalRetries))
		}
	}

	// Failed ops
	if m.FailedOps > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", m.FailedOps))
	}

	return parts
}

// SessionMetricsFromMap creates a SessionMetrics from a stats map (as used in JSON output).
// This allows renderers to use the same formatting logic as direct SessionMetrics consumers.
func SessionMetricsFromMap(stats map[string]any) *SessionMetrics {
	m := &SessionMetrics{}

	// Helper to extract int from int, int64, or float64.
	// Direct map access yields int64 (from time.Duration.Milliseconds()),
	// while JSON round-tripping yields float64.
	getInt := func(key string) int {
		switch v := stats[key].(type) {
		case int:
			return v
		case int64:
			return int(v)
		case float64:
			return int(v)
		default:
			return 0
		}
	}

	m.TotalRequests = getInt("requests")
	m.CacheHits = getInt("cache_hits")
	m.TotalRetries = getInt("retries")
	m.FailedOps = getInt("failed")

	// Reconstruct duration from duration_ms
	if durationMS := getInt("duration_ms"); durationMS > 0 {
		m.EndTime = m.StartTime.Add(time.Duration(durationMS) * time.Millisecond)
	}

	return m
}
