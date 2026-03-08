package observability

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// TraceCategories is a bitmask of trace event categories.
type TraceCategories int

const (
	TraceHTTP TraceCategories = 1 << iota
	TraceTUI
	TraceAll = TraceHTTP | TraceTUI
)

// Tracer writes structured JSON trace events to a file.
// All methods are nil-safe — a nil *Tracer is a no-op.
// The categories field uses atomic access so EnableCategories is safe
// to call concurrently with Log/Enabled from other goroutines.
type Tracer struct {
	logger *slog.Logger
	file   *os.File
	cats   atomic.Int32
	path   string
}

// NewTracer creates a Tracer that writes JSON events to path.
// Intermediate directories are created as needed.
func NewTracer(categories TraceCategories, path string) (*Tracer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("tracer: create dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // trace log, not world-readable
	if err != nil {
		return nil, fmt.Errorf("tracer: open file: %w", err)
	}
	handler := slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})
	t := &Tracer{
		logger: slog.New(handler),
		file:   f,
		path:   path,
	}
	t.cats.Store(int32(categories)) //nolint:gosec // bitmask values are bounded (max 3)
	return t, nil
}

// Close flushes and closes the trace file. Nil-safe.
func (t *Tracer) Close() error {
	if t == nil || t.file == nil {
		return nil
	}
	return t.file.Close()
}

// Path returns the trace file path. Nil-safe (returns "").
func (t *Tracer) Path() string {
	if t == nil {
		return ""
	}
	return t.path
}

// Enabled reports whether cat is active. Nil-safe.
func (t *Tracer) Enabled(cat TraceCategories) bool {
	return t != nil && TraceCategories(t.cats.Load())&cat != 0
}

// EnableCategories adds categories to the active set (bitwise OR).
// Nil-safe. Safe for concurrent use.
func (t *Tracer) EnableCategories(cats TraceCategories) {
	if t != nil {
		for {
			old := t.cats.Load()
			if t.cats.CompareAndSwap(old, old|int32(cats)) { //nolint:gosec // bitmask values are bounded (max 3)
				return
			}
		}
	}
}

// Log writes a structured trace event if cat is enabled. Nil-safe.
func (t *Tracer) Log(cat TraceCategories, msg string, args ...any) {
	if t == nil || TraceCategories(t.cats.Load())&cat == 0 {
		return
	}
	t.logger.Info(msg, args...)
}

// Logger returns the raw slog.Logger. Nil-safe (returns nil).
func (t *Tracer) Logger() *slog.Logger {
	if t == nil {
		return nil
	}
	return t.logger
}

// TracePath returns the trace file path for the current process.
// If cacheDir is non-empty it is used as the base; otherwise
// the platform's user cache directory is used (~/.cache on Linux,
// ~/Library/Caches on macOS).
func TracePath(cacheDir string) string {
	if cacheDir == "" {
		var err error
		cacheDir, err = os.UserCacheDir()
		if err != nil {
			cacheDir = os.TempDir()
		}
		cacheDir = filepath.Join(cacheDir, "basecamp")
	}
	return filepath.Join(cacheDir, fmt.Sprintf("trace-%d.log", os.Getpid()))
}

// ParseTraceEnv reads BASECAMP_TRACE (or BASECAMP_DEBUG for backward compat)
// and returns a configured Tracer, or nil when tracing is disabled.
//
// Values:
//
//	"http"       → TraceHTTP
//	"tui"        → TraceTUI
//	"all" or "1" → TraceAll
//	path (/, ., ~) → TraceAll + custom path
//
// Errors are reported to stderr and return nil (best-effort).
func ParseTraceEnv() *Tracer {
	return parseTraceEnv("")
}

// ParseTraceEnvWithCacheDir is like ParseTraceEnv but uses cacheDir for the
// default trace file location when no explicit path is given.
func ParseTraceEnvWithCacheDir(cacheDir string) *Tracer {
	return parseTraceEnv(cacheDir)
}

func parseTraceEnv(cacheDir string) *Tracer {
	val := os.Getenv("BASECAMP_TRACE")
	if val == "" {
		// Backward compat: BASECAMP_DEBUG → TraceHTTP, but only for values
		// the verbosity parser already accepts (numeric or "true").
		if dbg := os.Getenv("BASECAMP_DEBUG"); dbg == "true" || isPositiveInt(dbg) {
			val = "http"
		}
	}
	if val == "" {
		return nil
	}

	cats := TraceAll
	path := TracePath(cacheDir)

	switch strings.ToLower(val) {
	case "http":
		cats = TraceHTTP
	case "tui":
		cats = TraceTUI
	case "all", "1", "true":
		cats = TraceAll
	default:
		// Value starting with /, ., or ~ is a custom path
		if val[0] == '/' || val[0] == '.' || val[0] == '~' {
			path = expandHome(val)
		} else {
			fmt.Fprintf(os.Stderr, "Warning: unknown BASECAMP_TRACE value %q, ignoring\n", val)
			return nil
		}
	}

	t, err := NewTracer(cats, path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to start tracer: %v\n", err)
		return nil
	}
	return t
}

// isPositiveInt reports whether s is a non-empty string of digits representing
// a value > 0 (e.g. "1", "2"). Used to match the verbosity parser's acceptance
// of BASECAMP_DEBUG values.
func isPositiveInt(s string) bool {
	if s == "" || s == "0" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// expandHome replaces a leading ~ with the user's home directory.
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}
