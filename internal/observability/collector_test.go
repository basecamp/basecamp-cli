package observability

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

func TestSessionCollector_RecordRequest(t *testing.T) {
	c := NewSessionCollector()

	c.RecordRequest(RequestMetrics{
		Method:     "GET",
		URL:        "/todos",
		StatusCode: 200,
		Duration:   50 * time.Millisecond,
		FromCache:  false,
	})

	c.RecordRequest(RequestMetrics{
		Method:     "GET",
		URL:        "/projects",
		StatusCode: 200,
		Duration:   10 * time.Millisecond,
		FromCache:  true,
	})

	summary := c.Summary()
	if summary.TotalRequests != 2 {
		t.Errorf("expected 2 total requests, got %d", summary.TotalRequests)
	}
	if summary.CacheHits != 1 {
		t.Errorf("expected 1 cache hit, got %d", summary.CacheHits)
	}
	if summary.CacheMisses != 1 {
		t.Errorf("expected 1 cache miss, got %d", summary.CacheMisses)
	}
}

func TestSessionCollector_RecordRequestFromSDK(t *testing.T) {
	c := NewSessionCollector()

	info := basecamp.RequestInfo{
		Method:  "POST",
		URL:     "/todos/123/complete",
		Attempt: 1,
	}
	result := basecamp.RequestResult{
		StatusCode: 204,
		Duration:   45 * time.Millisecond,
		FromCache:  false,
	}

	c.RecordRequestFromSDK(info, result)

	summary := c.Summary()
	if summary.TotalRequests != 1 {
		t.Errorf("expected 1 request, got %d", summary.TotalRequests)
	}
	if summary.TotalLatency != 45*time.Millisecond {
		t.Errorf("expected 45ms latency, got %v", summary.TotalLatency)
	}
}

func TestSessionCollector_RecordOperation(t *testing.T) {
	c := NewSessionCollector()

	c.RecordOperation(OperationMetrics{
		Service:   "Todos",
		Operation: "Complete",
		Duration:  100 * time.Millisecond,
		Error:     nil,
	})

	c.RecordOperation(OperationMetrics{
		Service:   "Todos",
		Operation: "List",
		Duration:  200 * time.Millisecond,
		Error:     errors.New("network error"),
	})

	summary := c.Summary()
	if summary.TotalOperations != 2 {
		t.Errorf("expected 2 total operations, got %d", summary.TotalOperations)
	}
	if summary.FailedOps != 1 {
		t.Errorf("expected 1 failed op, got %d", summary.FailedOps)
	}
}

func TestSessionCollector_RecordOperationFromSDK(t *testing.T) {
	c := NewSessionCollector()

	op := basecamp.OperationInfo{
		Service:      "Todos",
		Operation:    "Complete",
		ResourceType: "todo",
		IsMutation:   true,
		BucketID:     123,
		ResourceID:   456,
	}

	c.RecordOperationFromSDK(op, nil, 50*time.Millisecond)

	summary := c.Summary()
	if summary.TotalOperations != 1 {
		t.Errorf("expected 1 operation, got %d", summary.TotalOperations)
	}
	if summary.FailedOps != 0 {
		t.Errorf("expected 0 failed ops, got %d", summary.FailedOps)
	}
}

func TestSessionCollector_RecordRetry(t *testing.T) {
	c := NewSessionCollector()

	c.RecordRetry(RetryMetrics{
		Method:  "GET",
		URL:     "/todos",
		Attempt: 2,
		Error:   errors.New("connection reset"),
	})

	summary := c.Summary()
	if summary.TotalRetries != 1 {
		t.Errorf("expected 1 retry in summary, got %d", summary.TotalRetries)
	}
}

func TestSessionCollector_Reset(t *testing.T) {
	c := NewSessionCollector()

	c.RecordRequest(RequestMetrics{Method: "GET", URL: "/test"})
	c.RecordOperation(OperationMetrics{Service: "Test", Operation: "Op"})
	c.RecordRetry(RetryMetrics{Method: "GET", URL: "/test", Attempt: 2})

	c.Reset()

	summary := c.Summary()
	if summary.TotalRequests != 0 {
		t.Errorf("expected 0 requests after reset, got %d", summary.TotalRequests)
	}
	if summary.TotalOperations != 0 {
		t.Errorf("expected 0 operations after reset, got %d", summary.TotalOperations)
	}
	if summary.TotalRetries != 0 {
		t.Errorf("expected 0 retries after reset, got %d", summary.TotalRetries)
	}
}

func TestSessionCollector_Concurrent(t *testing.T) {
	c := NewSessionCollector()
	var wg sync.WaitGroup

	// Simulate concurrent access
	for i := 0; i < 100; i++ {
		wg.Add(3)
		go func(n int) {
			defer wg.Done()
			c.RecordRequest(RequestMetrics{
				Method: "GET",
				URL:    "/test",
			})
		}(i)
		go func(n int) {
			defer wg.Done()
			c.RecordOperation(OperationMetrics{
				Service:   "Test",
				Operation: "Op",
			})
		}(i)
		go func(n int) {
			defer wg.Done()
			c.RecordRetry(RetryMetrics{
				Method:  "GET",
				URL:     "/test",
				Attempt: 2,
			})
		}(i)
	}

	wg.Wait()

	summary := c.Summary()
	if summary.TotalRequests != 100 {
		t.Errorf("expected 100 requests, got %d", summary.TotalRequests)
	}
	if summary.TotalOperations != 100 {
		t.Errorf("expected 100 operations, got %d", summary.TotalOperations)
	}
	if summary.TotalRetries != 100 {
		t.Errorf("expected 100 retries, got %d", summary.TotalRetries)
	}
}

func TestSessionCollector_Summary_Latency(t *testing.T) {
	c := NewSessionCollector()

	c.RecordRequest(RequestMetrics{Duration: 50 * time.Millisecond})
	c.RecordRequest(RequestMetrics{Duration: 100 * time.Millisecond})
	c.RecordRequest(RequestMetrics{Duration: 150 * time.Millisecond})

	summary := c.Summary()
	expectedLatency := 300 * time.Millisecond
	if summary.TotalLatency != expectedLatency {
		t.Errorf("expected total latency %v, got %v", expectedLatency, summary.TotalLatency)
	}
}
