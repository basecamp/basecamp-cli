package observability

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
)

func TestCLIHooks_SetLevel(t *testing.T) {
	h := NewCLIHooks(0, nil, nil)

	assert.Equal(t, 0, h.Level(), "expected level 0")

	h.SetLevel(2)
	assert.Equal(t, 2, h.Level(), "expected level 2")
}

func TestCLIHooks_Level0_Silent(t *testing.T) {
	var buf bytes.Buffer
	writer := NewTraceWriterTo(&buf)
	collector := NewSessionCollector()
	h := NewCLIHooks(0, collector, writer)

	ctx := context.Background()
	op := basecamp.OperationInfo{Service: "Todos", Operation: "Complete"}
	ctx = h.OnOperationStart(ctx, op)
	h.OnOperationEnd(ctx, op, nil, 50*time.Millisecond)

	info := basecamp.RequestInfo{Method: "POST", URL: "/todos/123/complete", Attempt: 1}
	result := basecamp.RequestResult{StatusCode: 204, Duration: 45 * time.Millisecond}
	ctx = h.OnRequestStart(ctx, info)
	h.OnRequestEnd(ctx, info, result)

	// Level 0 should produce no output
	assert.Equal(t, 0, buf.Len(), "expected no output at level 0")

	// But metrics should still be collected
	summary := collector.Summary()
	assert.Equal(t, 1, summary.TotalOperations, "expected 1 operation recorded")
	assert.Equal(t, 1, summary.TotalRequests, "expected 1 request recorded")
}

func TestCLIHooks_Level1_OperationsOnly(t *testing.T) {
	var buf bytes.Buffer
	writer := NewTraceWriterTo(&buf)
	h := NewCLIHooks(1, nil, writer)

	ctx := context.Background()
	op := basecamp.OperationInfo{Service: "Todos", Operation: "Complete"}
	ctx = h.OnOperationStart(ctx, op)
	h.OnOperationEnd(ctx, op, nil, 50*time.Millisecond)

	info := basecamp.RequestInfo{Method: "POST", URL: "/todos/123/complete", Attempt: 1}
	result := basecamp.RequestResult{StatusCode: 204, Duration: 45 * time.Millisecond}
	ctx = h.OnRequestStart(ctx, info)
	h.OnRequestEnd(ctx, info, result)

	output := buf.String()

	// Should show operation start/end
	assert.True(t, strings.Contains(output, "Calling Todos.Complete"), "expected operation start")
	assert.True(t, strings.Contains(output, "Completed Todos.Complete"), "expected operation end")

	// Should NOT show request details at level 1
	assert.False(t, strings.Contains(output, "POST"), "unexpected request output at level 1")
}

func TestCLIHooks_Level2_OperationsAndRequests(t *testing.T) {
	var buf bytes.Buffer
	writer := NewTraceWriterTo(&buf)
	h := NewCLIHooks(2, nil, writer)

	ctx := context.Background()
	op := basecamp.OperationInfo{Service: "Todos", Operation: "Complete"}
	ctx = h.OnOperationStart(ctx, op)

	info := basecamp.RequestInfo{Method: "POST", URL: "/todos/123/complete", Attempt: 1}
	result := basecamp.RequestResult{StatusCode: 204, Duration: 45 * time.Millisecond}
	reqCtx := h.OnRequestStart(ctx, info)
	h.OnRequestEnd(reqCtx, info, result)

	h.OnOperationEnd(ctx, op, nil, 50*time.Millisecond)

	output := buf.String()

	// Should show both operation and request details
	assert.True(t, strings.Contains(output, "Calling Todos.Complete"), "expected operation start")
	assert.True(t, strings.Contains(output, "-> POST /todos/123/complete"), "expected request start")
	assert.True(t, strings.Contains(output, "<- 204"), "expected request complete")
}

func TestCLIHooks_OperationError(t *testing.T) {
	var buf bytes.Buffer
	writer := NewTraceWriterTo(&buf)
	collector := NewSessionCollector()
	h := NewCLIHooks(1, collector, writer)

	ctx := context.Background()
	op := basecamp.OperationInfo{Service: "Todos", Operation: "Complete"}
	ctx = h.OnOperationStart(ctx, op)
	err := errors.New("permission denied")
	h.OnOperationEnd(ctx, op, err, 50*time.Millisecond)

	output := buf.String()

	// Should show failed with error
	assert.True(t, strings.Contains(output, "Failed Todos.Complete"), "expected failure message")
	assert.True(t, strings.Contains(output, "permission denied"), "expected error message")

	// Collector should record the error
	summary := collector.Summary()
	assert.Equal(t, 1, summary.TotalOperations, "expected 1 operation")
	assert.Equal(t, 1, summary.FailedOps, "expected 1 failure")
}

func TestCLIHooks_CachedRequest(t *testing.T) {
	var buf bytes.Buffer
	writer := NewTraceWriterTo(&buf)
	collector := NewSessionCollector()
	h := NewCLIHooks(2, collector, writer)

	ctx := context.Background()
	info := basecamp.RequestInfo{Method: "GET", URL: "/projects", Attempt: 1}
	result := basecamp.RequestResult{StatusCode: 200, FromCache: true}
	ctx = h.OnRequestStart(ctx, info)
	h.OnRequestEnd(ctx, info, result)

	output := buf.String()

	assert.True(t, strings.Contains(output, "(cached)"), "expected cached indicator")

	// Collector should record cache hit
	summary := collector.Summary()
	assert.Equal(t, 1, summary.CacheHits, "expected 1 cache hit")
}

func TestCLIHooks_Retry(t *testing.T) {
	var buf bytes.Buffer
	writer := NewTraceWriterTo(&buf)
	collector := NewSessionCollector()
	h := NewCLIHooks(2, collector, writer)

	ctx := context.Background()
	info := basecamp.RequestInfo{Method: "GET", URL: "/todos", Attempt: 2}
	err := errors.New("connection reset")
	h.OnRetry(ctx, info, 2, err)

	output := buf.String()

	assert.True(t, strings.Contains(output, "RETRY #2"), "expected retry message")
	assert.True(t, strings.Contains(output, "connection reset"), "expected error message")

	// Collector should record retry
	summary := collector.Summary()
	assert.Equal(t, 1, summary.TotalRetries, "expected 1 retry recorded")
}

func TestCLIHooks_ImplementsInterface(t *testing.T) {
	// Compile-time check that CLIHooks implements basecamp.Hooks
	var _ basecamp.Hooks = (*CLIHooks)(nil)
}

func TestCLIHooks_NilCollector(t *testing.T) {
	var buf bytes.Buffer
	writer := NewTraceWriterTo(&buf)
	h := NewCLIHooks(2, nil, writer) // nil collector

	ctx := context.Background()
	op := basecamp.OperationInfo{Service: "Todos", Operation: "List"}
	ctx = h.OnOperationStart(ctx, op)
	h.OnOperationEnd(ctx, op, nil, 50*time.Millisecond)

	info := basecamp.RequestInfo{Method: "GET", URL: "/todos", Attempt: 1}
	result := basecamp.RequestResult{StatusCode: 200, Duration: 45 * time.Millisecond}
	ctx = h.OnRequestStart(ctx, info)
	h.OnRequestEnd(ctx, info, result)

	// Should not panic and should still produce output
	assert.True(t, buf.Len() > 0, "expected output even with nil collector")
}

func TestCLIHooks_NilWriter(t *testing.T) {
	collector := NewSessionCollector()
	h := NewCLIHooks(2, collector, nil) // nil writer

	ctx := context.Background()
	op := basecamp.OperationInfo{Service: "Todos", Operation: "List"}
	ctx = h.OnOperationStart(ctx, op)
	h.OnOperationEnd(ctx, op, nil, 50*time.Millisecond)

	info := basecamp.RequestInfo{Method: "GET", URL: "/todos", Attempt: 1}
	result := basecamp.RequestResult{StatusCode: 200, Duration: 45 * time.Millisecond}
	ctx = h.OnRequestStart(ctx, info)
	h.OnRequestEnd(ctx, info, result)

	// Should not panic and should still collect metrics
	summary := collector.Summary()
	assert.Equal(t, 1, summary.TotalOperations, "expected 1 operation collected")
	assert.Equal(t, 1, summary.TotalRequests, "expected 1 request collected")
}
