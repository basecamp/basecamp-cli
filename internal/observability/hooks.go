package observability

import (
	"context"
	"sync"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// Verify CLIHooks implements basecamp.Hooks at compile time.
var _ basecamp.Hooks = (*CLIHooks)(nil)

// CLIHooks implements basecamp.Hooks for CLI observability.
// It supports configurable verbosity levels:
//   - 0: Silent (collect stats only, no output)
//   - 1: Operations only (log SDK operations)
//   - 2: Operations + requests (log both operations and HTTP requests)
type CLIHooks struct {
	mu        sync.Mutex
	level     int
	collector *SessionCollector
	writer    *TraceWriter
}

// NewCLIHooks creates a new CLIHooks with the given verbosity level.
// If collector is nil, metrics are not collected.
// If writer is nil, no trace output is produced.
func NewCLIHooks(level int, collector *SessionCollector, writer *TraceWriter) *CLIHooks {
	return &CLIHooks{
		level:     level,
		collector: collector,
		writer:    writer,
	}
}

// SetLevel changes the verbosity level at runtime.
func (h *CLIHooks) SetLevel(level int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.level = level
}

// Level returns the current verbosity level.
func (h *CLIHooks) Level() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.level
}

// OnOperationStart is called when a semantic SDK operation begins.
func (h *CLIHooks) OnOperationStart(ctx context.Context, op basecamp.OperationInfo) context.Context {
	h.mu.Lock()
	level := h.level
	writer := h.writer
	h.mu.Unlock()

	if level >= 1 && writer != nil {
		writer.WriteOperationStart(op)
	}

	return ctx
}

// OnOperationEnd is called when a semantic SDK operation completes.
func (h *CLIHooks) OnOperationEnd(ctx context.Context, op basecamp.OperationInfo, err error, duration time.Duration) {
	h.mu.Lock()
	level := h.level
	collector := h.collector
	writer := h.writer
	h.mu.Unlock()

	if collector != nil {
		collector.RecordOperationFromSDK(op, err, duration)
	}

	if level >= 1 && writer != nil {
		writer.WriteOperationEnd(op, err, duration)
	}
}

// OnRequestStart is called before an HTTP request is sent.
func (h *CLIHooks) OnRequestStart(ctx context.Context, info basecamp.RequestInfo) context.Context {
	h.mu.Lock()
	level := h.level
	writer := h.writer
	h.mu.Unlock()

	if level >= 2 && writer != nil {
		writer.WriteRequestStart(info)
	}

	return ctx
}

// OnRequestEnd is called after an HTTP request completes.
func (h *CLIHooks) OnRequestEnd(ctx context.Context, info basecamp.RequestInfo, result basecamp.RequestResult) {
	h.mu.Lock()
	collector := h.collector
	writer := h.writer
	level := h.level
	h.mu.Unlock()

	if collector != nil {
		collector.RecordRequestFromSDK(info, result)
	}

	if level >= 2 && writer != nil {
		writer.WriteRequestEnd(info, result)
	}
}

// OnRetry is called before a retry attempt.
func (h *CLIHooks) OnRetry(ctx context.Context, info basecamp.RequestInfo, attempt int, err error) {
	h.mu.Lock()
	collector := h.collector
	writer := h.writer
	level := h.level
	h.mu.Unlock()

	if collector != nil {
		collector.RecordRetryFromSDK(info, attempt, err)
	}

	if level >= 2 && writer != nil {
		writer.WriteRetry(info, attempt, err)
	}
}
