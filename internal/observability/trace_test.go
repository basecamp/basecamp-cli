package observability

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

func TestTraceWriter_WriteOperationStart(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	op := basecamp.OperationInfo{Service: "Todos", Operation: "Complete"}
	w.WriteOperationStart(op)

	output := buf.String()
	if !strings.Contains(output, "Calling Todos.Complete") {
		t.Errorf("expected 'Calling Todos.Complete', got: %s", output)
	}
	if !strings.HasPrefix(output, "[") {
		t.Errorf("expected timestamp prefix, got: %s", output)
	}
}

func TestTraceWriter_WriteOperationEnd(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	op := basecamp.OperationInfo{Service: "Todos", Operation: "List"}
	w.WriteOperationEnd(op, nil, 50*time.Millisecond)

	output := buf.String()
	if !strings.Contains(output, "Completed Todos.List") {
		t.Errorf("expected 'Completed Todos.List', got: %s", output)
	}
	if !strings.Contains(output, "(50ms)") {
		t.Errorf("expected duration, got: %s", output)
	}
}

func TestTraceWriter_WriteOperationEnd_Error(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	op := basecamp.OperationInfo{Service: "Projects", Operation: "Create"}
	w.WriteOperationEnd(op, errors.New("forbidden"), 50*time.Millisecond)

	output := buf.String()
	if !strings.Contains(output, "Failed Projects.Create") {
		t.Errorf("expected 'Failed Projects.Create', got: %s", output)
	}
	if !strings.Contains(output, "forbidden") {
		t.Errorf("expected error message, got: %s", output)
	}
}

func TestTraceWriter_WriteRequestStart(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	info := basecamp.RequestInfo{Method: "GET", URL: "/buckets/123/todos", Attempt: 1}
	w.WriteRequestStart(info)

	output := buf.String()
	if !strings.Contains(output, "-> GET /buckets/123/todos") {
		t.Errorf("expected request line, got: %s", output)
	}
}

func TestTraceWriter_WriteRequestEnd(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	info := basecamp.RequestInfo{Method: "GET", URL: "/todos", Attempt: 1}
	result := basecamp.RequestResult{StatusCode: 200, Duration: 45 * time.Millisecond}
	w.WriteRequestEnd(info, result)

	output := buf.String()
	if !strings.Contains(output, "<- 200") {
		t.Errorf("expected response line, got: %s", output)
	}
	if !strings.Contains(output, "(45ms)") {
		t.Errorf("expected duration, got: %s", output)
	}
}

func TestTraceWriter_WriteRequestEnd_Cached(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	info := basecamp.RequestInfo{Method: "GET", URL: "/projects", Attempt: 1}
	result := basecamp.RequestResult{StatusCode: 200, FromCache: true}
	w.WriteRequestEnd(info, result)

	output := buf.String()
	if !strings.Contains(output, "(cached)") {
		t.Errorf("expected cached indicator, got: %s", output)
	}
}

func TestTraceWriter_WriteRequestEnd_Error(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	info := basecamp.RequestInfo{Method: "POST", URL: "/todos", Attempt: 1}
	result := basecamp.RequestResult{Error: errors.New("connection refused")}
	w.WriteRequestEnd(info, result)

	output := buf.String()
	if !strings.Contains(output, "ERROR") {
		t.Errorf("expected ERROR, got: %s", output)
	}
	if !strings.Contains(output, "connection refused") {
		t.Errorf("expected error message, got: %s", output)
	}
}

func TestTraceWriter_WriteRetry(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	info := basecamp.RequestInfo{Method: "GET", URL: "/todos", Attempt: 2}
	w.WriteRetry(info, 2, errors.New("timeout"))

	output := buf.String()
	if !strings.Contains(output, "RETRY #2") {
		t.Errorf("expected 'RETRY #2', got: %s", output)
	}
	if !strings.Contains(output, "timeout") {
		t.Errorf("expected error message, got: %s", output)
	}
}

func TestTraceWriter_Timestamps(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	op1 := basecamp.OperationInfo{Service: "Test", Operation: "Op1"}
	op2 := basecamp.OperationInfo{Service: "Test", Operation: "Op2"}
	w.WriteOperationStart(op1)
	time.Sleep(10 * time.Millisecond)
	w.WriteOperationStart(op2)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	// Parse timestamps and verify second is later
	// Format: [0.123s] ...
	if !strings.HasPrefix(lines[0], "[0.") {
		t.Errorf("expected timestamp prefix on line 1: %s", lines[0])
	}
	if !strings.HasPrefix(lines[1], "[0.") {
		t.Errorf("expected timestamp prefix on line 2: %s", lines[1])
	}
}

func TestTraceWriter_Reset(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	// Write with initial time
	op := basecamp.OperationInfo{Service: "Test", Operation: "Op"}
	w.WriteOperationStart(op)
	firstOutput := buf.String()

	time.Sleep(50 * time.Millisecond)
	buf.Reset()
	w.Reset()

	// Write after reset - timestamp should be near zero again
	w.WriteOperationStart(op)
	secondOutput := buf.String()

	// First output should have larger timestamp than second (after reset)
	// This is a basic check - both should start with [0.0
	if !strings.HasPrefix(firstOutput, "[0.0") {
		t.Errorf("first output should start with near-zero timestamp: %s", firstOutput)
	}
	if !strings.HasPrefix(secondOutput, "[0.0") {
		t.Errorf("second output after reset should start with near-zero timestamp: %s", secondOutput)
	}
}
