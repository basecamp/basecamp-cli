// Package observability provides metrics collection and tracing for CLI operations.
package observability

import (
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
// It is safe for concurrent use.
type SessionCollector struct {
	mu sync.Mutex

	startTime  time.Time
	requests   []RequestMetrics
	operations []OperationMetrics
	retries    []RetryMetrics
}

// NewSessionCollector creates a new SessionCollector.
func NewSessionCollector() *SessionCollector {
	return &SessionCollector{
		startTime:  time.Now(),
		requests:   make([]RequestMetrics, 0),
		operations: make([]OperationMetrics, 0),
		retries:    make([]RetryMetrics, 0),
	}
}

// RecordRequest records metrics for an HTTP request.
func (c *SessionCollector) RecordRequest(m RequestMetrics) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, m)
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
	c.operations = append(c.operations, m)
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
func (c *SessionCollector) RecordRetry(m RetryMetrics) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.retries = append(c.retries, m)
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

	metrics := SessionMetrics{
		StartTime:       c.startTime,
		EndTime:         time.Now(),
		TotalRequests:   len(c.requests),
		TotalOperations: len(c.operations),
		TotalRetries:    len(c.retries),
	}

	for _, r := range c.requests {
		metrics.TotalLatency += r.Duration
		if r.FromCache {
			metrics.CacheHits++
		} else {
			metrics.CacheMisses++
		}
	}

	for _, op := range c.operations {
		if op.Error != nil {
			metrics.FailedOps++
		}
	}

	return metrics
}

// Requests returns a copy of all recorded request metrics.
func (c *SessionCollector) Requests() []RequestMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()

	result := make([]RequestMetrics, len(c.requests))
	copy(result, c.requests)
	return result
}

// Operations returns a copy of all recorded operation metrics.
func (c *SessionCollector) Operations() []OperationMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()

	result := make([]OperationMetrics, len(c.operations))
	copy(result, c.operations)
	return result
}

// Retries returns a copy of all recorded retry events.
func (c *SessionCollector) Retries() []RetryMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()

	result := make([]RetryMetrics, len(c.retries))
	copy(result, c.retries)
	return result
}

// Reset clears all collected metrics and resets the start time.
func (c *SessionCollector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.startTime = time.Now()
	c.requests = c.requests[:0]
	c.operations = c.operations[:0]
	c.retries = c.retries[:0]
}
