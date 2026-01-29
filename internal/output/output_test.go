package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/bcq/internal/observability"
)

// =============================================================================
// Exit Codes Tests
// =============================================================================

func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		code     string
		expected int
	}{
		{CodeUsage, ExitUsage},
		{CodeNotFound, ExitNotFound},
		{CodeAuth, ExitAuth},
		{CodeForbidden, ExitForbidden},
		{CodeRateLimit, ExitRateLimit},
		{CodeNetwork, ExitNetwork},
		{CodeAPI, ExitAPI},
		{CodeAmbiguous, ExitAmbiguous},
		{"unknown_code", ExitAPI}, // Unknown codes default to ExitAPI
		{"", ExitAPI},             // Empty code defaults to ExitAPI
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			result := ExitCodeFor(tt.code)
			assert.Equal(t, tt.expected, result, "ExitCodeFor(%q)", tt.code)
		})
	}
}

func TestExitCodeConstants(t *testing.T) {
	// Verify exit codes match expected values from bash implementation
	expected := map[int]int{
		ExitOK:        0,
		ExitUsage:     1,
		ExitNotFound:  2,
		ExitAuth:      3,
		ExitForbidden: 4,
		ExitRateLimit: 5,
		ExitNetwork:   6,
		ExitAPI:       7,
		ExitAmbiguous: 8,
	}

	for code, value := range expected {
		assert.Equal(t, value, code, "Exit code constant mismatch")
	}
}

func TestErrorCodeConstants(t *testing.T) {
	// Verify error code strings
	codes := []string{
		CodeUsage,
		CodeNotFound,
		CodeAuth,
		CodeForbidden,
		CodeRateLimit,
		CodeNetwork,
		CodeAPI,
		CodeAmbiguous,
	}

	for _, code := range codes {
		assert.NotEmpty(t, code, "Error code should not be empty")
	}
}

// =============================================================================
// Error Struct Tests
// =============================================================================

func TestErrorInterface(t *testing.T) {
	// Error with hint - includes hint in message
	errWithHint := &Error{
		Code:    CodeNotFound,
		Message: "resource not found",
		Hint:    "check the ID",
	}
	assert.Equal(t, "resource not found: check the ID", errWithHint.Error())

	// Error without hint - just message
	errNoHint := &Error{
		Code:    CodeNotFound,
		Message: "resource not found",
	}
	assert.Equal(t, "resource not found", errNoHint.Error())
}

func TestErrorUnwrap(t *testing.T) {
	cause := errors.New("underlying error")
	err := &Error{
		Code:    CodeAPI,
		Message: "api error",
		Cause:   cause,
	}

	unwrapped := err.Unwrap()
	assert.Equal(t, cause, unwrapped) //nolint:errorlint // testing Unwrap returns exact wrapped error
}

func TestErrorUnwrapNil(t *testing.T) {
	err := &Error{
		Code:    CodeAPI,
		Message: "api error",
	}

	assert.Nil(t, err.Unwrap(), "Unwrap() should return nil when Cause is nil")
}

func TestErrorExitCode(t *testing.T) {
	tests := []struct {
		code     string
		expected int
	}{
		{CodeUsage, ExitUsage},
		{CodeNotFound, ExitNotFound},
		{CodeAuth, ExitAuth},
		{CodeForbidden, ExitForbidden},
		{CodeRateLimit, ExitRateLimit},
		{CodeNetwork, ExitNetwork},
		{CodeAPI, ExitAPI},
		{CodeAmbiguous, ExitAmbiguous},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			err := &Error{Code: tt.code, Message: "test"}
			assert.Equal(t, tt.expected, err.ExitCode())
		})
	}
}

// =============================================================================
// Error Constructors Tests
// =============================================================================

func TestErrUsage(t *testing.T) {
	err := ErrUsage("invalid argument")

	assert.Equal(t, CodeUsage, err.Code)
	assert.Equal(t, "invalid argument", err.Message)
	assert.Equal(t, ExitUsage, err.ExitCode())
}

func TestErrUsageHint(t *testing.T) {
	err := ErrUsageHint("invalid argument", "try --help")

	assert.Equal(t, CodeUsage, err.Code)
	assert.Equal(t, "invalid argument", err.Message)
	assert.Equal(t, "try --help", err.Hint)
}

func TestErrNotFound(t *testing.T) {
	err := ErrNotFound("project", "123")

	assert.Equal(t, CodeNotFound, err.Code)
	assert.Equal(t, "project not found: 123", err.Message)
	assert.Equal(t, ExitNotFound, err.ExitCode())
}

func TestErrNotFoundHint(t *testing.T) {
	err := ErrNotFoundHint("project", "123", "check project ID")

	assert.Equal(t, CodeNotFound, err.Code)
	assert.Equal(t, "check project ID", err.Hint)
}

func TestErrAuth(t *testing.T) {
	err := ErrAuth("not authenticated")

	assert.Equal(t, CodeAuth, err.Code)
	assert.NotEmpty(t, err.Hint, "Hint should contain login instruction")
	assert.Equal(t, ExitAuth, err.ExitCode())
}

func TestErrForbidden(t *testing.T) {
	err := ErrForbidden("access denied")

	assert.Equal(t, CodeForbidden, err.Code)
	assert.Equal(t, 403, err.HTTPStatus)
	assert.Equal(t, ExitForbidden, err.ExitCode())
}

func TestErrForbiddenScope(t *testing.T) {
	err := ErrForbiddenScope()

	assert.Equal(t, CodeForbidden, err.Code)
	assert.Equal(t, 403, err.HTTPStatus)
	assert.NotEmpty(t, err.Hint, "Hint should not be empty for scope error")
}

func TestErrRateLimit(t *testing.T) {
	err := ErrRateLimit(60)

	assert.Equal(t, CodeRateLimit, err.Code)
	assert.Equal(t, 429, err.HTTPStatus)
	assert.True(t, err.Retryable, "RateLimit error should be retryable")
	assert.NotEmpty(t, err.Hint, "Hint should contain retry time")
	assert.Equal(t, ExitRateLimit, err.ExitCode())
}

func TestErrRateLimitZero(t *testing.T) {
	err := ErrRateLimit(0)

	assert.Equal(t, "Try again later", err.Hint)
}

func TestErrNetwork(t *testing.T) {
	cause := errors.New("connection refused")
	err := ErrNetwork(cause)

	assert.Equal(t, CodeNetwork, err.Code)
	assert.True(t, err.Retryable, "Network error should be retryable")
	assert.Equal(t, cause, err.Cause) //nolint:errorlint // testing Cause field is exact wrapped error
	assert.Equal(t, "connection refused", err.Hint)
	assert.Equal(t, ExitNetwork, err.ExitCode())
}

func TestErrAPI(t *testing.T) {
	err := ErrAPI(500, "server error")

	assert.Equal(t, CodeAPI, err.Code)
	assert.Equal(t, 500, err.HTTPStatus)
	assert.Equal(t, "server error", err.Message)
	assert.Equal(t, ExitAPI, err.ExitCode())
}

func TestErrAmbiguous(t *testing.T) {
	matches := []string{"Project A", "Project B", "Project Alpha"}
	err := ErrAmbiguous("multiple matches", matches)

	assert.Equal(t, CodeAmbiguous, err.Code)
	assert.NotEmpty(t, err.Hint, "Hint should contain matches")
	assert.Equal(t, ExitAmbiguous, err.ExitCode())
}

// =============================================================================
// AsError Tests
// =============================================================================

func TestAsErrorWithOutputError(t *testing.T) {
	original := &Error{
		Code:    CodeNotFound,
		Message: "not found",
		Hint:    "try again",
	}

	result := AsError(original)
	assert.Equal(t, original, result, "AsError should return same *Error unchanged")
}

func TestAsErrorWithStandardError(t *testing.T) {
	original := errors.New("something went wrong")

	result := AsError(original)
	assert.Equal(t, CodeAPI, result.Code)
	assert.Equal(t, "something went wrong", result.Message)
	assert.Equal(t, original, result.Cause) //nolint:errorlint // testing Cause field is exact original error
}

func TestAsErrorWithWrappedOutputError(t *testing.T) {
	original := &Error{
		Code:    CodeAuth,
		Message: "auth required",
	}
	wrapped := errors.Join(errors.New("wrapper"), original)

	result := AsError(wrapped)
	assert.Equal(t, CodeAuth, result.Code)
}

// Note: AsError(nil) panics because it calls err.Error() on nil.
// This is expected behavior - callers should not pass nil to AsError.

// =============================================================================
// Envelope/Response Tests
// =============================================================================

func TestResponseJSON(t *testing.T) {
	resp := &Response{
		OK:      true,
		Data:    map[string]string{"name": "Test Project"},
		Summary: "Found 1 project",
	}

	data, err := json.Marshal(resp)
	require.NoError(t, err, "Failed to marshal")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded), "Failed to unmarshal")

	assert.Equal(t, true, decoded["ok"])
	assert.Equal(t, "Found 1 project", decoded["summary"])
}

func TestErrorResponseJSON(t *testing.T) {
	resp := &ErrorResponse{
		OK:    false,
		Error: "not found",
		Code:  CodeNotFound,
		Hint:  "check the ID",
	}

	data, err := json.Marshal(resp)
	require.NoError(t, err, "Failed to marshal")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded), "Failed to unmarshal")

	assert.Equal(t, false, decoded["ok"])
	assert.Equal(t, "not found", decoded["error"])
	assert.Equal(t, CodeNotFound, decoded["code"])
}

func TestBreadcrumb(t *testing.T) {
	bc := Breadcrumb{
		Action:      "show",
		Cmd:         "bcq projects show 123",
		Description: "View project details",
	}

	data, err := json.Marshal(bc)
	require.NoError(t, err, "Failed to marshal")

	var decoded map[string]string
	require.NoError(t, json.Unmarshal(data, &decoded), "Failed to unmarshal")

	assert.Equal(t, "show", decoded["action"])
	assert.Equal(t, "bcq projects show 123", decoded["cmd"])
}

// =============================================================================
// Writer Tests
// =============================================================================

func TestWriterOK(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatJSON,
		Writer: &buf,
	})

	data := map[string]string{"id": "123", "name": "Test"}
	err := w.OK(data, WithSummary("test summary"))
	require.NoError(t, err, "OK() failed")

	var resp Response
	require.NoError(t, json.Unmarshal(buf.Bytes(), &resp), "Failed to unmarshal output")

	assert.True(t, resp.OK)
	assert.Equal(t, "test summary", resp.Summary)
}

func TestWriterErr(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatJSON,
		Writer: &buf,
	})

	err := w.Err(ErrNotFound("project", "123"))
	require.NoError(t, err, "Err() failed")

	var resp ErrorResponse
	require.NoError(t, json.Unmarshal(buf.Bytes(), &resp), "Failed to unmarshal output")

	assert.False(t, resp.OK)
	assert.Equal(t, CodeNotFound, resp.Code)
}

func TestWriterQuietFormat(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatQuiet,
		Writer: &buf,
	})

	data := map[string]string{"id": "123", "name": "Test"}
	err := w.OK(data, WithSummary("ignored"))
	require.NoError(t, err, "OK() failed")

	// Quiet format should output just the data, not the envelope
	var decoded map[string]string
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded), "Failed to unmarshal output")

	assert.Equal(t, "123", decoded["id"])
	// Should not have envelope fields
	_, exists := decoded["ok"]
	assert.False(t, exists, "Quiet format should not include envelope ok field")
}

func TestWriterQuietFormatString(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatQuiet,
		Writer: &buf,
	})

	// Quiet mode outputs JSON (preserves --agent contract)
	err := w.OK("my-auth-token-value")
	require.NoError(t, err, "OK() failed")

	// Should output JSON-encoded string
	output := buf.String()
	assert.Equal(t, "\"my-auth-token-value\"\n", output)
}

func TestWriterIDsFormat(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatIDs,
		Writer: &buf,
	})

	data := []map[string]any{
		{"id": 123, "name": "Project A"},
		{"id": 456, "name": "Project B"},
	}
	err := w.OK(data)
	require.NoError(t, err, "OK() failed")

	output := buf.String()
	assert.Equal(t, "123\n456\n", output)
}

func TestWriterCountFormat(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatCount,
		Writer: &buf,
	})

	data := []map[string]any{
		{"id": 1},
		{"id": 2},
		{"id": 3},
	}
	err := w.OK(data)
	require.NoError(t, err, "OK() failed")

	output := buf.String()
	assert.Equal(t, "3\n", output)
}

func TestWriterCountFormatSingleItem(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatCount,
		Writer: &buf,
	})

	data := map[string]any{"id": 1, "name": "Single"}
	err := w.OK(data)
	require.NoError(t, err, "OK() failed")

	output := buf.String()
	assert.Equal(t, "1\n", output)
}

func TestDefaultOptions(t *testing.T) {
	opts := DefaultOptions()

	assert.Equal(t, FormatAuto, opts.Format)
	assert.NotNil(t, opts.Writer, "Default Writer should not be nil")
}

func TestNewWithNilWriter(t *testing.T) {
	w := New(Options{
		Format: FormatJSON,
		Writer: nil,
	})

	// Should default to os.Stdout
	assert.NotNil(t, w.opts.Writer, "Writer should default to os.Stdout, not nil")
}

// =============================================================================
// Response Options Tests
// =============================================================================

func TestWithSummary(t *testing.T) {
	resp := &Response{}
	WithSummary("test summary")(resp)

	assert.Equal(t, "test summary", resp.Summary)
}

func TestWithBreadcrumbs(t *testing.T) {
	resp := &Response{}
	bc1 := Breadcrumb{Action: "list", Cmd: "bcq list", Description: "List items"}
	bc2 := Breadcrumb{Action: "show", Cmd: "bcq show 1", Description: "Show item"}

	WithBreadcrumbs(bc1, bc2)(resp)

	require.Len(t, resp.Breadcrumbs, 2)
	assert.Equal(t, "list", resp.Breadcrumbs[0].Action)
}

func TestWithBreadcrumbsAppend(t *testing.T) {
	resp := &Response{
		Breadcrumbs: []Breadcrumb{{Action: "initial"}},
	}
	bc := Breadcrumb{Action: "added"}

	WithBreadcrumbs(bc)(resp)

	assert.Len(t, resp.Breadcrumbs, 2)
}

func TestWithContext(t *testing.T) {
	resp := &Response{}

	WithContext("project_id", 123)(resp)
	WithContext("user", "alice")(resp)

	assert.Equal(t, 123, resp.Context["project_id"])
	assert.Equal(t, "alice", resp.Context["user"])
}

func TestWithMeta(t *testing.T) {
	resp := &Response{}

	WithMeta("page", 1)(resp)
	WithMeta("total", 100)(resp)

	assert.Equal(t, 1, resp.Meta["page"])
	assert.Equal(t, 100, resp.Meta["total"])
}

func TestWithStats(t *testing.T) {
	startTime := time.Now().Add(-1 * time.Second)
	endTime := time.Now()

	metrics := &observability.SessionMetrics{
		StartTime:       startTime,
		EndTime:         endTime,
		TotalRequests:   10,
		CacheHits:       4,
		CacheMisses:     6,
		TotalOperations: 5,
		FailedOps:       1,
		TotalLatency:    500 * time.Millisecond,
	}

	resp := &Response{}
	WithStats(metrics)(resp)

	require.NotNil(t, resp.Meta, "Meta should be initialized")

	stats, ok := resp.Meta["stats"].(map[string]any)
	require.True(t, ok, "Meta[stats] should be map[string]any, got %T", resp.Meta["stats"])

	assert.Equal(t, 10, stats["requests"])
	assert.Equal(t, 4, stats["cache_hits"])
	assert.Equal(t, 5, stats["operations"])
	assert.Equal(t, 1, stats["failed"])
	assert.Equal(t, int64(500), stats["latency_ms"])

	// cache_rate should be 40% (4 hits out of 10 requests)
	cacheRate, ok := stats["cache_rate"].(float64)
	require.True(t, ok, "cache_rate should be float64, got %T", stats["cache_rate"])
	assert.Equal(t, 40.0, cacheRate)
}

func TestWithStatsNil(t *testing.T) {
	resp := &Response{}
	WithStats(nil)(resp)

	// Should not create Meta if metrics is nil
	assert.Nil(t, resp.Meta, "Meta should remain nil when metrics is nil")
}

func TestWithStatsZeroRequests(t *testing.T) {
	metrics := &observability.SessionMetrics{
		TotalRequests: 0,
		CacheHits:     0,
	}

	resp := &Response{}
	WithStats(metrics)(resp)

	stats := resp.Meta["stats"].(map[string]any)
	cacheRate := stats["cache_rate"].(float64)

	// cache_rate should be 0 when no requests
	assert.Equal(t, 0.0, cacheRate)
}

// =============================================================================
// normalizeData Tests
// =============================================================================

func TestNormalizeDataWithSlice(t *testing.T) {
	data := []map[string]any{
		{"id": 1, "name": "A"},
		{"id": 2, "name": "B"},
	}

	result := normalizeData(data)
	slice, ok := result.([]map[string]any)
	require.True(t, ok, "Expected []map[string]any, got %T", result)
	assert.Len(t, slice, 2)
}

func TestNormalizeDataWithMap(t *testing.T) {
	data := map[string]any{"id": 1, "name": "A"}

	result := normalizeData(data)
	m, ok := result.(map[string]any)
	require.True(t, ok, "Expected map[string]any, got %T", result)
	assert.Equal(t, 1, m["id"])
}

func TestNormalizeDataWithJSONRawMessage(t *testing.T) {
	raw := json.RawMessage(`[{"id": 1}, {"id": 2}]`)

	result := normalizeData(raw)
	slice, ok := result.([]map[string]any)
	require.True(t, ok, "Expected []map[string]any, got %T", result)
	assert.Len(t, slice, 2)
}

func TestNormalizeDataWithStruct(t *testing.T) {
	type Item struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	data := Item{ID: 1, Name: "Test"}

	result := normalizeData(data)
	m, ok := result.(map[string]any)
	require.True(t, ok, "Expected map[string]any, got %T", result)
	assert.Equal(t, float64(1), m["id"]) // JSON unmarshals numbers as float64
}

func TestNormalizeDataWithNil(t *testing.T) {
	result := normalizeData(nil)
	assert.Nil(t, result)
}

// =============================================================================
// formatCell Tests
// =============================================================================

func TestFormatCellWithScalarArray(t *testing.T) {
	// Test string arrays (e.g., tags)
	tags := []any{"frontend", "bug", "urgent"}
	result := formatCell(tags)
	assert.Equal(t, "frontend, bug, urgent", result)

	// Test number arrays
	numbers := []any{float64(1), float64(2), float64(3)}
	result = formatCell(numbers)
	assert.Equal(t, "1, 2, 3", result)

	// Test mixed arrays
	mixed := []any{"a", float64(1), "b"}
	result = formatCell(mixed)
	assert.Equal(t, "a, 1, b", result)

	// Test empty array
	empty := []any{}
	result = formatCell(empty)
	assert.Equal(t, "", result)
}

func TestFormatCellWithMapArray(t *testing.T) {
	// Test maps with name key (assignees)
	assignees := []any{
		map[string]any{"id": float64(1), "name": "Alice"},
		map[string]any{"id": float64(2), "name": "Bob"},
	}
	result := formatCell(assignees)
	assert.Equal(t, "Alice, Bob", result)

	// Test maps with title key (no name)
	items := []any{
		map[string]any{"id": float64(1), "title": "Task A"},
		map[string]any{"id": float64(2), "title": "Task B"},
	}
	result = formatCell(items)
	assert.Equal(t, "Task A, Task B", result)

	// Test maps with only id (fallback)
	attachments := []any{
		map[string]any{"id": float64(100)},
		map[string]any{"id": float64(200)},
	}
	result = formatCell(attachments)
	assert.Equal(t, "100, 200", result)
}

// =============================================================================
// Markdown Format Tests
// =============================================================================

func TestWriterMarkdownFormatError(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatMarkdown,
		Writer: &buf,
	})

	err := w.Err(ErrNotFound("project", "123"))
	require.NoError(t, err, "Err() failed")

	output := buf.String()
	// Should NOT be JSON
	assert.NotContains(t, output, `"ok":`)
	// Should contain styled error message
	assert.Contains(t, output, "Error:")
	assert.Contains(t, output, "project not found")
}

func TestWriterMarkdownFormatList(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatMarkdown,
		Writer: &buf,
	})

	data := []map[string]any{
		{"id": 1, "name": "Project A", "status": "active"},
		{"id": 2, "name": "Project B", "status": "archived"},
	}
	err := w.OK(data, WithSummary("2 projects"))
	require.NoError(t, err, "OK() failed")

	output := buf.String()
	// Should NOT be JSON
	assert.NotContains(t, output, `"ok":`)
	// Should contain summary
	assert.Contains(t, output, "2 projects")
	// Should contain data
	assert.Contains(t, output, "Project A")
}

func TestWriterMarkdownFormatObject(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatMarkdown,
		Writer: &buf,
	})

	data := map[string]any{
		"id":        123,
		"name":      "Test Todo",
		"completed": false,
	}
	err := w.OK(data)
	require.NoError(t, err, "OK() failed")

	output := buf.String()
	// Should NOT be JSON
	assert.NotContains(t, output, `"ok":`)
	// Should contain key-value pairs (keys are now title-cased via formatHeader)
	assert.Contains(t, output, "Id")
	assert.Contains(t, output, "123")
	assert.Contains(t, output, "Completed")
	assert.Contains(t, output, "no")
}

func TestWriterMarkdownFormatBreadcrumbs(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatMarkdown,
		Writer: &buf,
	})

	data := map[string]any{"id": 1}
	err := w.OK(data, WithBreadcrumbs(
		Breadcrumb{Action: "show", Cmd: "bcq show 1", Description: "View details"},
	))
	require.NoError(t, err, "OK() failed")

	output := buf.String()
	// Should contain breadcrumb (literal Markdown uses "### Next" heading)
	assert.Contains(t, output, "Next")
	assert.Contains(t, output, "bcq show 1")
}

func TestWriterMarkdownNoANSIWhenNotTTY(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatMarkdown,
		Writer: &buf, // bytes.Buffer is not a TTY
	})

	err := w.Err(ErrNotFound("project", "123"))
	require.NoError(t, err, "Err() failed")

	output := buf.String()
	// Should NOT contain ANSI escape codes when not a TTY
	assert.NotContains(t, output, "\x1b[")
	// Should still contain the error message
	assert.Contains(t, output, "Error:")
}

func TestWriterStyledEmitsANSI(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatStyled,
		Writer: &buf, // bytes.Buffer is not a TTY, but FormatStyled forces ANSI
	})

	err := w.Err(ErrNotFound("project", "123"))
	require.NoError(t, err, "Err() failed")

	output := buf.String()
	// SHOULD contain ANSI escape codes when FormatStyled is used
	assert.Contains(t, output, "\x1b[")
	// Should still contain the error message
	assert.Contains(t, output, "Error:")
}

func TestWriterMarkdownOutputsLiteralMarkdown(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatMarkdown,
		Writer: &buf,
	})

	err := w.Err(ErrNotFound("project", "123"))
	require.NoError(t, err, "Err() failed")

	output := buf.String()
	// Should NOT contain ANSI escape codes
	assert.NotContains(t, output, "\x1b[")
	// Should contain Markdown syntax
	assert.Contains(t, output, "**Error:**")
}

// =============================================================================
// Format Constants Tests
// =============================================================================

func TestFormatConstants(t *testing.T) {
	// Verify format constants have distinct values
	formats := map[Format]string{
		FormatAuto:     "auto",
		FormatJSON:     "json",
		FormatMarkdown: "markdown",
		FormatStyled:   "styled",
		FormatQuiet:    "quiet",
		FormatIDs:      "ids",
		FormatCount:    "count",
	}

	seen := make(map[Format]bool)
	for format := range formats {
		assert.False(t, seen[format], "Duplicate format value: %d", format)
		seen[format] = true
	}
}

func TestEffectiveFormat(t *testing.T) {
	tests := []struct {
		name     string
		format   Format
		expected Format
	}{
		{"JSON stays JSON", FormatJSON, FormatJSON},
		{"Markdown stays Markdown", FormatMarkdown, FormatMarkdown},
		{"Styled stays Styled", FormatStyled, FormatStyled},
		{"Quiet stays Quiet", FormatQuiet, FormatQuiet},
		{"IDs stays IDs", FormatIDs, FormatIDs},
		{"Count stays Count", FormatCount, FormatCount},
		// FormatAuto resolves to FormatJSON when writer is not a TTY (bytes.Buffer)
		{"Auto resolves to JSON for non-TTY", FormatAuto, FormatJSON},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := New(Options{
				Format: tt.format,
				Writer: &buf,
			})

			got := w.EffectiveFormat()
			if got != tt.expected {
				t.Errorf("EffectiveFormat() = %d, want %d", got, tt.expected)
			}
		})
	}
}

// =============================================================================
// Edge Case Tests
// =============================================================================

func TestWriterIDsFormatWithSingleItem(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatIDs,
		Writer: &buf,
	})

	data := map[string]any{"id": 999, "name": "Single"}
	err := w.OK(data)
	require.NoError(t, err, "OK() failed")

	output := buf.String()
	assert.Equal(t, "999\n", output)
}

func TestWriterIDsFormatWithNoID(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatIDs,
		Writer: &buf,
	})

	data := []map[string]any{
		{"name": "No ID"},
	}
	err := w.OK(data)
	require.NoError(t, err, "OK() failed")

	output := buf.String()
	assert.Equal(t, "", output)
}

func TestErrorWithHTTPStatus(t *testing.T) {
	testCases := []struct {
		name           string
		err            *Error
		expectedStatus int
	}{
		{"forbidden", ErrForbidden("x"), 403},
		{"forbidden scope", ErrForbiddenScope(), 403},
		{"rate limit", ErrRateLimit(60), 429},
		{"api error", ErrAPI(500, "x"), 500},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expectedStatus, tc.err.HTTPStatus)
		})
	}
}

func TestErrorRetryable(t *testing.T) {
	retryable := []struct {
		name string
		err  *Error
	}{
		{"rate limit", ErrRateLimit(60)},
		{"network", ErrNetwork(errors.New("connection failed"))},
	}

	for _, tc := range retryable {
		t.Run(tc.name+" is retryable", func(t *testing.T) {
			assert.True(t, tc.err.Retryable, "Expected error to be retryable")
		})
	}

	nonRetryable := []struct {
		name string
		err  *Error
	}{
		{"not found", ErrNotFound("x", "y")},
		{"auth", ErrAuth("x")},
		{"forbidden", ErrForbidden("x")},
		{"usage", ErrUsage("x")},
		{"ambiguous", ErrAmbiguous("x", nil)},
	}

	for _, tc := range nonRetryable {
		t.Run(tc.name+" is not retryable", func(t *testing.T) {
			assert.False(t, tc.err.Retryable, "Expected error not to be retryable")
		})
	}
}

// =============================================================================
// formatDateValue Tests
// =============================================================================

func TestFormatDateValue(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		value    any
		expected string
	}{
		// Non-date columns should pass through to formatCell
		{"non-date column string", "name", "Test Project", "Test Project"},
		{"non-date column number", "id", float64(123), "123"},
		{"non-date column bool", "completed", true, "yes"},

		// Date-only format (YYYY-MM-DD)
		{"date-only format", "due_on", "2024-03-15", "Mar 15, 2024"},
		{"due_date format", "due_date", "2024-12-25", "Dec 25, 2024"},

		// Empty or nil values
		{"empty string", "created_at", "", ""},
		{"nil value", "updated_at", nil, ""},

		// Non-string date column
		{"non-string date value", "created_at", float64(12345), "12345"},

		// Invalid date format
		{"invalid date format", "created_at", "not-a-date", "not-a-date"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatDateValue(tt.key, tt.value)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormatDateValueRelativeTimes(t *testing.T) {
	// Test relative time formatting with dynamically generated timestamps
	now := time.Now()

	tests := []struct {
		name     string
		offset   time.Duration
		contains string
	}{
		{"just now", -30 * time.Second, "just now"},
		{"minutes ago", -5 * time.Minute, "minutes ago"},
		{"1 hour ago", -1 * time.Hour, "1 hour ago"},
		{"hours ago", -3 * time.Hour, "hours ago"},
		{"yesterday", -25 * time.Hour, "yesterday"},
		{"days ago", -3 * 24 * time.Hour, "days ago"},
		{"old date formatted", -30 * 24 * time.Hour, "2"}, // Will contain year like "2025" or "2026"
		{"future date formatted", 24 * time.Hour, "2"},    // Future dates show formatted date
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			timestamp := now.Add(tt.offset).Format(time.RFC3339)
			result := formatDateValue("created_at", timestamp)

			assert.Contains(t, result, tt.contains)
		})
	}
}

func TestFormatDateValueColumnDetection(t *testing.T) {
	// Test that only _at, _on, _date suffixes are treated as date columns
	testCases := []struct {
		key       string
		isDateCol bool
	}{
		{"created_at", true},
		{"updated_at", true},
		{"due_on", true},
		{"starts_on", true},
		{"due_date", true},
		{"start_date", true},
		{"name", false},
		{"status", false},
		{"creator", false},
		{"content", false},
	}

	for _, tc := range testCases {
		t.Run(tc.key, func(t *testing.T) {
			// For date columns with valid date, should format
			// For non-date columns, should pass through unchanged
			testValue := "2024-06-15"
			result := formatDateValue(tc.key, testValue)

			if tc.isDateCol {
				// Date columns should format the date
				assert.NotEqual(t, testValue, result, "Date column %q should format the date", tc.key)
			} else {
				// Non-date columns should return value unchanged
				assert.Equal(t, testValue, result, "Non-date column %q should return value unchanged", tc.key)
			}
		})
	}
}

// =============================================================================
// formatHeader Tests
// =============================================================================

func TestFormatHeader(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"id", "Id"},
		{"name", "Name"},
		{"created_at", "Created"},
		{"updated_at", "Updated"},
		{"due_on", "Due"},
		{"due_date", "Due Date"},
		{"starts_on", "Starts"},
		{"project_id", "Project Id"},
		{"app_url", "App Url"},
		{"content", "Content"},
		{"some_long_field_name", "Some Long Field Name"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := formatHeader(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// =============================================================================
// renderObject Ordering Tests
// =============================================================================

func TestRenderObjectOrdering(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatStyled,
		Writer: &buf,
	})

	// Create data with fields that have different priorities
	data := map[string]any{
		"updated_at":  "2024-01-15T10:00:00Z", // priority 9
		"created_at":  "2024-01-10T10:00:00Z", // priority 8
		"description": "Test description",     // priority 7
		"status":      "active",               // priority 4
		"name":        "Test Item",            // priority 2
		"id":          float64(123),           // priority 1
	}

	err := w.OK(data)
	require.NoError(t, err, "OK() failed")

	output := buf.String()

	// Verify that id appears before name, name before status, etc.
	idPos := strings.Index(output, "Id")
	namePos := strings.Index(output, "Name")
	statusPos := strings.Index(output, "Status")
	descPos := strings.Index(output, "Description")
	createdPos := strings.Index(output, "Created")
	updatedPos := strings.Index(output, "Updated")

	assert.NotEqual(t, -1, idPos, "Output should contain 'Id'")
	assert.NotEqual(t, -1, namePos, "Output should contain 'Name'")

	// Verify ordering: id < name < status < description < created < updated
	assert.Less(t, idPos, namePos, "Id (priority 1) should appear before Name (priority 2)")
	assert.Less(t, namePos, statusPos, "Name (priority 2) should appear before Status (priority 4)")
	assert.Less(t, statusPos, descPos, "Status (priority 4) should appear before Description (priority 7)")
	assert.Less(t, descPos, createdPos, "Description (priority 7) should appear before Created (priority 8)")
	assert.Less(t, createdPos, updatedPos, "Created (priority 8) should appear before Updated (priority 9)")
}

// =============================================================================
// renderObject Header Humanization Tests
// =============================================================================

func TestRenderObjectHeaders(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatStyled,
		Writer: &buf,
	})

	data := map[string]any{
		"id":         float64(123),
		"created_at": "2024-01-10T10:00:00Z",
		"due_on":     "2024-02-01",
	}

	err := w.OK(data)
	require.NoError(t, err, "OK() failed")

	output := buf.String()

	// Should use humanized headers
	assert.Contains(t, output, "Id")
	assert.Contains(t, output, "Created")
	assert.NotContains(t, output, "created_at")
	assert.Contains(t, output, "Due")
	assert.NotContains(t, output, "due_on")
}

// =============================================================================
// skipObjectColumns Tests
// =============================================================================

func TestSkipObjectColumns(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatStyled,
		Writer: &buf,
	})

	// Include fields that should be skipped
	data := map[string]any{
		"id":               float64(123),
		"name":             "Test",
		"bucket":           map[string]any{"id": 1},    // should skip (nested)
		"creator":          map[string]any{"id": 2},    // should skip (nested + in skipObjectColumns)
		"url":              "https://example.com",      // should skip (in skipObjectColumns)
		"app_url":          "https://app.example.com",  // should skip (in skipObjectColumns)
		"type":             "Todo",                     // visible (not skipped)
		"bookmark_url":     "https://bookmark.example", // should skip (in skipObjectColumns)
		"subscription_url": "https://sub.example",      // should skip (in skipObjectColumns)
		"comments_count":   float64(5),                 // should skip (in skipObjectColumns)
		"comments_url":     "https://comments.example", // should skip (in skipObjectColumns)
		"position":         float64(1),                 // should skip (in skipObjectColumns)
		"inherits_status":  true,                       // should skip (in skipObjectColumns)
	}

	err := w.OK(data)
	require.NoError(t, err, "OK() failed")

	output := buf.String()

	// Should contain visible fields
	assert.Contains(t, output, "Id")
	assert.Contains(t, output, "Name")

	// Should NOT contain skipped fields
	skippedFields := []string{
		"bucket", "creator", "url", "app_url",
		"bookmark_url", "subscription_url", "comments_count",
		"comments_url", "position", "inherits_status",
	}
	for _, field := range skippedFields {
		// Check for both raw key and title-cased version
		assert.NotContains(t, strings.ToLower(output), field, "Output should NOT contain skipped field %q", field)
	}
}

func TestSkipObjectColumnsMap(t *testing.T) {
	// Verify the skipObjectColumns map contains expected fields
	expectedSkipped := []string{
		"bucket", "creator", "parent", "dock", "inherits_status",
		"url", "app_url", "bookmark_url", "subscription_url",
		"comments_count", "comments_url", "position",
		"attachable_sgid", "personable_type", "recording_type",
	}

	for _, field := range expectedSkipped {
		assert.True(t, skipObjectColumns[field], "skipObjectColumns should contain %q", field)
	}
}

// =============================================================================
// Stats Rendering Tests
// =============================================================================

func TestStyledOutputWithStats(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatStyled,
		Writer: &buf,
	})

	startTime := time.Now().Add(-250 * time.Millisecond)
	metrics := &observability.SessionMetrics{
		StartTime:       startTime,
		EndTime:         time.Now(),
		TotalRequests:   5,
		CacheHits:       2,
		CacheMisses:     3,
		TotalOperations: 3,
		FailedOps:       0,
		TotalLatency:    200 * time.Millisecond,
	}

	err := w.OK(map[string]any{"id": 123}, WithStats(metrics))
	require.NoError(t, err, "OK() failed")

	output := buf.String()

	// Should contain Stats line
	assert.Contains(t, output, "Stats:")
	// Should contain request count
	assert.Contains(t, output, "5 requests")
	// Should contain cache info
	assert.Contains(t, output, "cached")
}

func TestMarkdownOutputWithStats(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatMarkdown,
		Writer: &buf,
	})

	startTime := time.Now().Add(-500 * time.Millisecond)
	metrics := &observability.SessionMetrics{
		StartTime:       startTime,
		EndTime:         time.Now(),
		TotalRequests:   3,
		CacheHits:       1,
		CacheMisses:     2,
		TotalOperations: 2,
		FailedOps:       1,
		TotalLatency:    400 * time.Millisecond,
	}

	err := w.OK(map[string]any{"id": 456}, WithStats(metrics))
	require.NoError(t, err, "OK() failed")

	output := buf.String()

	// Should contain Stats line in markdown format
	assert.Contains(t, output, "*Stats:")
	// Should contain failed count
	assert.Contains(t, output, "1 failed")
}

func TestStyledOutputWithoutStats(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatStyled,
		Writer: &buf,
	})

	err := w.OK(map[string]any{"id": 789})
	require.NoError(t, err, "OK() failed")

	output := buf.String()

	// Should NOT contain Stats line when no stats provided
	assert.NotContains(t, output, "Stats:")
}

func TestStatsRenderingSingleRequest(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatStyled,
		Writer: &buf,
	})

	metrics := &observability.SessionMetrics{
		StartTime:     time.Now().Add(-100 * time.Millisecond),
		EndTime:       time.Now(),
		TotalRequests: 1,
		CacheHits:     0,
		CacheMisses:   1,
	}

	err := w.OK(map[string]any{"id": 1}, WithStats(metrics))
	require.NoError(t, err, "OK() failed")

	output := buf.String()

	// Should use singular "request" not "requests"
	assert.Contains(t, output, "1 request")
	assert.NotContains(t, output, "1 requests")
}
