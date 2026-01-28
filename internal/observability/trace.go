package observability

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// TraceWriter outputs human-readable trace information to stderr.
// It formats output with timestamps relative to session start.
type TraceWriter struct {
	mu        sync.Mutex
	writer    io.Writer
	startTime time.Time
}

// NewTraceWriter creates a new TraceWriter that writes to stderr.
func NewTraceWriter() *TraceWriter {
	return &TraceWriter{
		writer:    os.Stderr,
		startTime: time.Now(),
	}
}

// NewTraceWriterTo creates a new TraceWriter that writes to the given writer.
func NewTraceWriterTo(w io.Writer) *TraceWriter {
	return &TraceWriter{
		writer:    w,
		startTime: time.Now(),
	}
}

// WriteOperationStart writes an operation start trace line.
// Format: [0.234s] Calling Todos.Complete
func (t *TraceWriter) WriteOperationStart(op basecamp.OperationInfo) {
	t.mu.Lock()
	defer t.mu.Unlock()

	elapsed := time.Since(t.startTime).Seconds()
	fmt.Fprintf(t.writer, "[%.3fs] Calling %s.%s\n", elapsed, op.Service, op.Operation)
}

// WriteOperationEnd writes an operation completion trace line.
// Format: [0.234s] Completed Todos.Complete (234ms)
func (t *TraceWriter) WriteOperationEnd(op basecamp.OperationInfo, err error, duration time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	elapsed := time.Since(t.startTime).Seconds()

	if err != nil {
		fmt.Fprintf(t.writer, "[%.3fs] Failed %s.%s: %v\n", elapsed, op.Service, op.Operation, err)
	} else {
		fmt.Fprintf(t.writer, "[%.3fs] Completed %s.%s (%dms)\n", elapsed, op.Service, op.Operation, duration.Milliseconds())
	}
}

// WriteRequestStart writes a request start trace line.
// Format: [0.234s]   -> GET /buckets/123/todos
func (t *TraceWriter) WriteRequestStart(info basecamp.RequestInfo) {
	t.mu.Lock()
	defer t.mu.Unlock()

	elapsed := time.Since(t.startTime).Seconds()
	fmt.Fprintf(t.writer, "[%.3fs]   -> %s %s\n", elapsed, info.Method, info.URL)
}

// WriteRequestEnd writes a request completion trace line.
// Format: [0.234s]   <- 200 (45ms) or [0.234s]   <- 200 (cached)
func (t *TraceWriter) WriteRequestEnd(info basecamp.RequestInfo, result basecamp.RequestResult) {
	t.mu.Lock()
	defer t.mu.Unlock()

	elapsed := time.Since(t.startTime).Seconds()

	if result.Error != nil {
		fmt.Fprintf(t.writer, "[%.3fs]   <- ERROR: %v\n", elapsed, result.Error)
		return
	}

	if result.FromCache {
		fmt.Fprintf(t.writer, "[%.3fs]   <- %d (cached)\n", elapsed, result.StatusCode)
	} else {
		fmt.Fprintf(t.writer, "[%.3fs]   <- %d (%dms)\n", elapsed, result.StatusCode, result.Duration.Milliseconds())
	}
}

// WriteRetry writes a retry trace line.
// Format: [0.234s]   RETRY #2: connection reset
func (t *TraceWriter) WriteRetry(info basecamp.RequestInfo, attempt int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	elapsed := time.Since(t.startTime).Seconds()
	fmt.Fprintf(t.writer, "[%.3fs]   RETRY #%d: %v\n", elapsed, attempt, err)
}

// Reset resets the start time for relative timestamps.
func (t *TraceWriter) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.startTime = time.Now()
}
