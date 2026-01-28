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

	requests := c.Requests()
	if len(requests) != 2 {
		t.Errorf("expected 2 requests, got %d", len(requests))
	}

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

	requests := c.Requests()
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Method != "POST" {
		t.Errorf("expected POST, got %s", requests[0].Method)
	}
	if requests[0].StatusCode != 204 {
		t.Errorf("expected 204, got %d", requests[0].StatusCode)
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

	ops := c.Operations()
	if len(ops) != 2 {
		t.Errorf("expected 2 operations, got %d", len(ops))
	}

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

	ops := c.Operations()
	if len(ops) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(ops))
	}
	if ops[0].Service != "Todos" {
		t.Errorf("expected Todos, got %s", ops[0].Service)
	}
	if ops[0].IsMutation != true {
		t.Error("expected IsMutation=true")
	}
	if ops[0].BucketID != 123 {
		t.Errorf("expected BucketID=123, got %d", ops[0].BucketID)
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

	retries := c.Retries()
	if len(retries) != 1 {
		t.Fatalf("expected 1 retry, got %d", len(retries))
	}
	if retries[0].Attempt != 2 {
		t.Errorf("expected attempt 2, got %d", retries[0].Attempt)
	}

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

	if len(c.Requests()) != 0 {
		t.Error("expected 0 requests after reset")
	}
	if len(c.Operations()) != 0 {
		t.Error("expected 0 operations after reset")
	}
	if len(c.Retries()) != 0 {
		t.Error("expected 0 retries after reset")
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
