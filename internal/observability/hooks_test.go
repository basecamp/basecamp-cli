package observability

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

func TestCLIHooks_SetLevel(t *testing.T) {
	h := NewCLIHooks(0, nil, nil)

	if h.Level() != 0 {
		t.Errorf("expected level 0, got %d", h.Level())
	}

	h.SetLevel(2)
	if h.Level() != 2 {
		t.Errorf("expected level 2, got %d", h.Level())
	}
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
	if buf.Len() != 0 {
		t.Errorf("expected no output at level 0, got: %s", buf.String())
	}

	// But metrics should still be collected
	if len(collector.Operations()) != 1 {
		t.Errorf("expected 1 operation recorded, got %d", len(collector.Operations()))
	}
	if len(collector.Requests()) != 1 {
		t.Errorf("expected 1 request recorded, got %d", len(collector.Requests()))
	}
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
	if !strings.Contains(output, "Calling Todos.Complete") {
		t.Errorf("expected operation start, got: %s", output)
	}
	if !strings.Contains(output, "Completed Todos.Complete") {
		t.Errorf("expected operation end, got: %s", output)
	}

	// Should NOT show request details at level 1
	if strings.Contains(output, "POST") {
		t.Errorf("unexpected request output at level 1: %s", output)
	}
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
	if !strings.Contains(output, "Calling Todos.Complete") {
		t.Errorf("expected operation start, got: %s", output)
	}
	if !strings.Contains(output, "-> POST /todos/123/complete") {
		t.Errorf("expected request start, got: %s", output)
	}
	if !strings.Contains(output, "<- 204") {
		t.Errorf("expected request complete, got: %s", output)
	}
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
	if !strings.Contains(output, "Failed Todos.Complete") {
		t.Errorf("expected failure message, got: %s", output)
	}
	if !strings.Contains(output, "permission denied") {
		t.Errorf("expected error message, got: %s", output)
	}

	// Collector should record the error
	ops := collector.Operations()
	if len(ops) != 1 || ops[0].Error == nil {
		t.Error("expected operation with error to be recorded")
	}
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

	if !strings.Contains(output, "(cached)") {
		t.Errorf("expected cached indicator, got: %s", output)
	}

	// Collector should record cache hit
	summary := collector.Summary()
	if summary.CacheHits != 1 {
		t.Errorf("expected 1 cache hit, got %d", summary.CacheHits)
	}
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

	if !strings.Contains(output, "RETRY #2") {
		t.Errorf("expected retry message, got: %s", output)
	}
	if !strings.Contains(output, "connection reset") {
		t.Errorf("expected error message, got: %s", output)
	}

	// Collector should record retry
	if len(collector.Retries()) != 1 {
		t.Errorf("expected 1 retry recorded, got %d", len(collector.Retries()))
	}
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
	if buf.Len() == 0 {
		t.Error("expected output even with nil collector")
	}
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
	if len(collector.Operations()) != 1 {
		t.Error("expected 1 operation collected")
	}
	if len(collector.Requests()) != 1 {
		t.Error("expected 1 request collected")
	}
}
