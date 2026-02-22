package observability

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
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
	assert.Equal(t, 2, summary.TotalRequests)
	assert.Equal(t, 1, summary.CacheHits)
	assert.Equal(t, 1, summary.CacheMisses)
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
	assert.Equal(t, 1, summary.TotalRequests)
	assert.Equal(t, 45*time.Millisecond, summary.TotalLatency)
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
	assert.Equal(t, 2, summary.TotalOperations)
	assert.Equal(t, 1, summary.FailedOps)
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
	assert.Equal(t, 1, summary.TotalOperations)
	assert.Equal(t, 0, summary.FailedOps)
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
	assert.Equal(t, 1, summary.TotalRetries)
}

func TestSessionCollector_Reset(t *testing.T) {
	c := NewSessionCollector()

	c.RecordRequest(RequestMetrics{Method: "GET", URL: "/test"})
	c.RecordOperation(OperationMetrics{Service: "Test", Operation: "Op"})
	c.RecordRetry(RetryMetrics{Method: "GET", URL: "/test", Attempt: 2})

	c.Reset()

	summary := c.Summary()
	assert.Equal(t, 0, summary.TotalRequests)
	assert.Equal(t, 0, summary.TotalOperations)
	assert.Equal(t, 0, summary.TotalRetries)
}

func TestSessionCollector_Concurrent(t *testing.T) {
	c := NewSessionCollector()
	var wg sync.WaitGroup

	// Simulate concurrent access
	for i := range 100 {
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
	assert.Equal(t, 100, summary.TotalRequests)
	assert.Equal(t, 100, summary.TotalOperations)
	assert.Equal(t, 100, summary.TotalRetries)
}

func TestSessionCollector_Summary_Latency(t *testing.T) {
	c := NewSessionCollector()

	c.RecordRequest(RequestMetrics{Duration: 50 * time.Millisecond})
	c.RecordRequest(RequestMetrics{Duration: 100 * time.Millisecond})
	c.RecordRequest(RequestMetrics{Duration: 150 * time.Millisecond})

	summary := c.Summary()
	assert.Equal(t, 300*time.Millisecond, summary.TotalLatency)
}
