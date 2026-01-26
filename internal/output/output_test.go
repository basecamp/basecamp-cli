package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
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
			if result != tt.expected {
				t.Errorf("ExitCodeFor(%q) = %d, want %d", tt.code, result, tt.expected)
			}
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
		if code != value {
			t.Errorf("Exit code constant mismatch: got %d, want %d", code, value)
		}
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
		if code == "" {
			t.Error("Error code should not be empty")
		}
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
	expected := "resource not found: check the ID"
	if errWithHint.Error() != expected {
		t.Errorf("Error() = %q, want %q", errWithHint.Error(), expected)
	}

	// Error without hint - just message
	errNoHint := &Error{
		Code:    CodeNotFound,
		Message: "resource not found",
	}
	if errNoHint.Error() != "resource not found" {
		t.Errorf("Error() = %q, want %q", errNoHint.Error(), "resource not found")
	}
}

func TestErrorUnwrap(t *testing.T) {
	cause := errors.New("underlying error")
	err := &Error{
		Code:    CodeAPI,
		Message: "api error",
		Cause:   cause,
	}

	unwrapped := err.Unwrap()
	if unwrapped != cause {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, cause)
	}
}

func TestErrorUnwrapNil(t *testing.T) {
	err := &Error{
		Code:    CodeAPI,
		Message: "api error",
	}

	if err.Unwrap() != nil {
		t.Error("Unwrap() should return nil when Cause is nil")
	}
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
			if err.ExitCode() != tt.expected {
				t.Errorf("ExitCode() = %d, want %d", err.ExitCode(), tt.expected)
			}
		})
	}
}

// =============================================================================
// Error Constructors Tests
// =============================================================================

func TestErrUsage(t *testing.T) {
	err := ErrUsage("invalid argument")

	if err.Code != CodeUsage {
		t.Errorf("Code = %q, want %q", err.Code, CodeUsage)
	}
	if err.Message != "invalid argument" {
		t.Errorf("Message = %q, want %q", err.Message, "invalid argument")
	}
	if err.ExitCode() != ExitUsage {
		t.Errorf("ExitCode() = %d, want %d", err.ExitCode(), ExitUsage)
	}
}

func TestErrUsageHint(t *testing.T) {
	err := ErrUsageHint("invalid argument", "try --help")

	if err.Code != CodeUsage {
		t.Errorf("Code = %q, want %q", err.Code, CodeUsage)
	}
	if err.Message != "invalid argument" {
		t.Errorf("Message = %q, want %q", err.Message, "invalid argument")
	}
	if err.Hint != "try --help" {
		t.Errorf("Hint = %q, want %q", err.Hint, "try --help")
	}
}

func TestErrNotFound(t *testing.T) {
	err := ErrNotFound("project", "123")

	if err.Code != CodeNotFound {
		t.Errorf("Code = %q, want %q", err.Code, CodeNotFound)
	}
	if err.Message != "project not found: 123" {
		t.Errorf("Message = %q, want %q", err.Message, "project not found: 123")
	}
	if err.ExitCode() != ExitNotFound {
		t.Errorf("ExitCode() = %d, want %d", err.ExitCode(), ExitNotFound)
	}
}

func TestErrNotFoundHint(t *testing.T) {
	err := ErrNotFoundHint("project", "123", "check project ID")

	if err.Code != CodeNotFound {
		t.Errorf("Code = %q, want %q", err.Code, CodeNotFound)
	}
	if err.Hint != "check project ID" {
		t.Errorf("Hint = %q, want %q", err.Hint, "check project ID")
	}
}

func TestErrAuth(t *testing.T) {
	err := ErrAuth("not authenticated")

	if err.Code != CodeAuth {
		t.Errorf("Code = %q, want %q", err.Code, CodeAuth)
	}
	if err.Hint == "" {
		t.Error("Hint should contain login instruction")
	}
	if err.ExitCode() != ExitAuth {
		t.Errorf("ExitCode() = %d, want %d", err.ExitCode(), ExitAuth)
	}
}

func TestErrForbidden(t *testing.T) {
	err := ErrForbidden("access denied")

	if err.Code != CodeForbidden {
		t.Errorf("Code = %q, want %q", err.Code, CodeForbidden)
	}
	if err.HTTPStatus != 403 {
		t.Errorf("HTTPStatus = %d, want %d", err.HTTPStatus, 403)
	}
	if err.ExitCode() != ExitForbidden {
		t.Errorf("ExitCode() = %d, want %d", err.ExitCode(), ExitForbidden)
	}
}

func TestErrForbiddenScope(t *testing.T) {
	err := ErrForbiddenScope()

	if err.Code != CodeForbidden {
		t.Errorf("Code = %q, want %q", err.Code, CodeForbidden)
	}
	if err.HTTPStatus != 403 {
		t.Errorf("HTTPStatus = %d, want %d", err.HTTPStatus, 403)
	}
	if err.Hint == "" {
		t.Error("Hint should not be empty for scope error")
	}
}

func TestErrRateLimit(t *testing.T) {
	err := ErrRateLimit(60)

	if err.Code != CodeRateLimit {
		t.Errorf("Code = %q, want %q", err.Code, CodeRateLimit)
	}
	if err.HTTPStatus != 429 {
		t.Errorf("HTTPStatus = %d, want %d", err.HTTPStatus, 429)
	}
	if !err.Retryable {
		t.Error("RateLimit error should be retryable")
	}
	if err.Hint == "" {
		t.Error("Hint should contain retry time")
	}
	if err.ExitCode() != ExitRateLimit {
		t.Errorf("ExitCode() = %d, want %d", err.ExitCode(), ExitRateLimit)
	}
}

func TestErrRateLimitZero(t *testing.T) {
	err := ErrRateLimit(0)

	if err.Hint != "Try again later" {
		t.Errorf("Hint = %q, want %q for zero retry", err.Hint, "Try again later")
	}
}

func TestErrNetwork(t *testing.T) {
	cause := errors.New("connection refused")
	err := ErrNetwork(cause)

	if err.Code != CodeNetwork {
		t.Errorf("Code = %q, want %q", err.Code, CodeNetwork)
	}
	if !err.Retryable {
		t.Error("Network error should be retryable")
	}
	if err.Cause != cause {
		t.Error("Cause should be set")
	}
	if err.Hint != "connection refused" {
		t.Errorf("Hint = %q, want %q", err.Hint, "connection refused")
	}
	if err.ExitCode() != ExitNetwork {
		t.Errorf("ExitCode() = %d, want %d", err.ExitCode(), ExitNetwork)
	}
}

func TestErrAPI(t *testing.T) {
	err := ErrAPI(500, "server error")

	if err.Code != CodeAPI {
		t.Errorf("Code = %q, want %q", err.Code, CodeAPI)
	}
	if err.HTTPStatus != 500 {
		t.Errorf("HTTPStatus = %d, want %d", err.HTTPStatus, 500)
	}
	if err.Message != "server error" {
		t.Errorf("Message = %q, want %q", err.Message, "server error")
	}
	if err.ExitCode() != ExitAPI {
		t.Errorf("ExitCode() = %d, want %d", err.ExitCode(), ExitAPI)
	}
}

func TestErrAmbiguous(t *testing.T) {
	matches := []string{"Project A", "Project B", "Project Alpha"}
	err := ErrAmbiguous("multiple matches", matches)

	if err.Code != CodeAmbiguous {
		t.Errorf("Code = %q, want %q", err.Code, CodeAmbiguous)
	}
	if err.Hint == "" {
		t.Error("Hint should contain matches")
	}
	if err.ExitCode() != ExitAmbiguous {
		t.Errorf("ExitCode() = %d, want %d", err.ExitCode(), ExitAmbiguous)
	}
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
	if result != original {
		t.Error("AsError should return same *Error unchanged")
	}
}

func TestAsErrorWithStandardError(t *testing.T) {
	original := errors.New("something went wrong")

	result := AsError(original)
	if result.Code != CodeAPI {
		t.Errorf("Code = %q, want %q", result.Code, CodeAPI)
	}
	if result.Message != "something went wrong" {
		t.Errorf("Message = %q, want %q", result.Message, "something went wrong")
	}
	if result.Cause != original {
		t.Error("Cause should be original error")
	}
}

func TestAsErrorWithWrappedOutputError(t *testing.T) {
	original := &Error{
		Code:    CodeAuth,
		Message: "auth required",
	}
	wrapped := errors.Join(errors.New("wrapper"), original)

	result := AsError(wrapped)
	if result.Code != CodeAuth {
		t.Errorf("Code = %q, want %q", result.Code, CodeAuth)
	}
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
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded["ok"] != true {
		t.Error("ok field should be true")
	}
	if decoded["summary"] != "Found 1 project" {
		t.Errorf("summary = %q, want %q", decoded["summary"], "Found 1 project")
	}
}

func TestErrorResponseJSON(t *testing.T) {
	resp := &ErrorResponse{
		OK:    false,
		Error: "not found",
		Code:  CodeNotFound,
		Hint:  "check the ID",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded["ok"] != false {
		t.Error("ok field should be false")
	}
	if decoded["error"] != "not found" {
		t.Errorf("error = %q, want %q", decoded["error"], "not found")
	}
	if decoded["code"] != CodeNotFound {
		t.Errorf("code = %q, want %q", decoded["code"], CodeNotFound)
	}
}

func TestBreadcrumb(t *testing.T) {
	bc := Breadcrumb{
		Action:      "show",
		Cmd:         "bcq projects show 123",
		Description: "View project details",
	}

	data, err := json.Marshal(bc)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded map[string]string
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded["action"] != "show" {
		t.Errorf("action = %q, want %q", decoded["action"], "show")
	}
	if decoded["cmd"] != "bcq projects show 123" {
		t.Errorf("cmd = %q, want %q", decoded["cmd"], "bcq projects show 123")
	}
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
	if err != nil {
		t.Fatalf("OK() failed: %v", err)
	}

	var resp Response
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to unmarshal output: %v", err)
	}

	if !resp.OK {
		t.Error("OK field should be true")
	}
	if resp.Summary != "test summary" {
		t.Errorf("Summary = %q, want %q", resp.Summary, "test summary")
	}
}

func TestWriterErr(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatJSON,
		Writer: &buf,
	})

	err := w.Err(ErrNotFound("project", "123"))
	if err != nil {
		t.Fatalf("Err() failed: %v", err)
	}

	var resp ErrorResponse
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to unmarshal output: %v", err)
	}

	if resp.OK {
		t.Error("OK field should be false")
	}
	if resp.Code != CodeNotFound {
		t.Errorf("Code = %q, want %q", resp.Code, CodeNotFound)
	}
}

func TestWriterQuietFormat(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatQuiet,
		Writer: &buf,
	})

	data := map[string]string{"id": "123", "name": "Test"}
	err := w.OK(data, WithSummary("ignored"))
	if err != nil {
		t.Fatalf("OK() failed: %v", err)
	}

	// Quiet format should output just the data, not the envelope
	var decoded map[string]string
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("Failed to unmarshal output: %v", err)
	}

	if decoded["id"] != "123" {
		t.Errorf("id = %q, want %q", decoded["id"], "123")
	}
	// Should not have envelope fields
	if _, exists := decoded["ok"]; exists {
		t.Error("Quiet format should not include envelope ok field")
	}
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
	if err != nil {
		t.Fatalf("OK() failed: %v", err)
	}

	output := buf.String()
	if output != "123\n456\n" {
		t.Errorf("IDs output = %q, want %q", output, "123\n456\n")
	}
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
	if err != nil {
		t.Fatalf("OK() failed: %v", err)
	}

	output := buf.String()
	if output != "3\n" {
		t.Errorf("Count output = %q, want %q", output, "3\n")
	}
}

func TestWriterCountFormatSingleItem(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatCount,
		Writer: &buf,
	})

	data := map[string]any{"id": 1, "name": "Single"}
	err := w.OK(data)
	if err != nil {
		t.Fatalf("OK() failed: %v", err)
	}

	output := buf.String()
	if output != "1\n" {
		t.Errorf("Count output for single item = %q, want %q", output, "1\n")
	}
}

func TestDefaultOptions(t *testing.T) {
	opts := DefaultOptions()

	if opts.Format != FormatAuto {
		t.Errorf("Default Format = %d, want %d", opts.Format, FormatAuto)
	}
	if opts.Writer == nil {
		t.Error("Default Writer should not be nil")
	}
}

func TestNewWithNilWriter(t *testing.T) {
	w := New(Options{
		Format: FormatJSON,
		Writer: nil,
	})

	// Should default to os.Stdout
	if w.opts.Writer == nil {
		t.Error("Writer should default to os.Stdout, not nil")
	}
}

// =============================================================================
// Response Options Tests
// =============================================================================

func TestWithSummary(t *testing.T) {
	resp := &Response{}
	WithSummary("test summary")(resp)

	if resp.Summary != "test summary" {
		t.Errorf("Summary = %q, want %q", resp.Summary, "test summary")
	}
}

func TestWithBreadcrumbs(t *testing.T) {
	resp := &Response{}
	bc1 := Breadcrumb{Action: "list", Cmd: "bcq list", Description: "List items"}
	bc2 := Breadcrumb{Action: "show", Cmd: "bcq show 1", Description: "Show item"}

	WithBreadcrumbs(bc1, bc2)(resp)

	if len(resp.Breadcrumbs) != 2 {
		t.Errorf("Breadcrumbs count = %d, want %d", len(resp.Breadcrumbs), 2)
	}
	if resp.Breadcrumbs[0].Action != "list" {
		t.Errorf("First breadcrumb action = %q, want %q", resp.Breadcrumbs[0].Action, "list")
	}
}

func TestWithBreadcrumbsAppend(t *testing.T) {
	resp := &Response{
		Breadcrumbs: []Breadcrumb{{Action: "initial"}},
	}
	bc := Breadcrumb{Action: "added"}

	WithBreadcrumbs(bc)(resp)

	if len(resp.Breadcrumbs) != 2 {
		t.Errorf("Breadcrumbs count = %d, want %d", len(resp.Breadcrumbs), 2)
	}
}

func TestWithContext(t *testing.T) {
	resp := &Response{}

	WithContext("project_id", 123)(resp)
	WithContext("user", "alice")(resp)

	if resp.Context["project_id"] != 123 {
		t.Errorf("Context[project_id] = %v, want %d", resp.Context["project_id"], 123)
	}
	if resp.Context["user"] != "alice" {
		t.Errorf("Context[user] = %v, want %q", resp.Context["user"], "alice")
	}
}

func TestWithMeta(t *testing.T) {
	resp := &Response{}

	WithMeta("page", 1)(resp)
	WithMeta("total", 100)(resp)

	if resp.Meta["page"] != 1 {
		t.Errorf("Meta[page] = %v, want %d", resp.Meta["page"], 1)
	}
	if resp.Meta["total"] != 100 {
		t.Errorf("Meta[total] = %v, want %d", resp.Meta["total"], 100)
	}
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
	if !ok {
		t.Fatalf("Expected []map[string]any, got %T", result)
	}
	if len(slice) != 2 {
		t.Errorf("Length = %d, want %d", len(slice), 2)
	}
}

func TestNormalizeDataWithMap(t *testing.T) {
	data := map[string]any{"id": 1, "name": "A"}

	result := normalizeData(data)
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("Expected map[string]any, got %T", result)
	}
	if m["id"] != 1 {
		t.Errorf("id = %v, want %d", m["id"], 1)
	}
}

func TestNormalizeDataWithJSONRawMessage(t *testing.T) {
	raw := json.RawMessage(`[{"id": 1}, {"id": 2}]`)

	result := normalizeData(raw)
	slice, ok := result.([]map[string]any)
	if !ok {
		t.Fatalf("Expected []map[string]any, got %T", result)
	}
	if len(slice) != 2 {
		t.Errorf("Length = %d, want %d", len(slice), 2)
	}
}

func TestNormalizeDataWithStruct(t *testing.T) {
	type Item struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	data := Item{ID: 1, Name: "Test"}

	result := normalizeData(data)
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("Expected map[string]any, got %T", result)
	}
	if m["id"] != float64(1) { // JSON unmarshals numbers as float64
		t.Errorf("id = %v, want %v", m["id"], float64(1))
	}
}

func TestNormalizeDataWithNil(t *testing.T) {
	result := normalizeData(nil)
	if result != nil {
		t.Errorf("Expected nil, got %v", result)
	}
}

// =============================================================================
// formatCell Tests
// =============================================================================

func TestFormatCellWithScalarArray(t *testing.T) {
	// Test string arrays (e.g., tags)
	tags := []any{"frontend", "bug", "urgent"}
	result := formatCell(tags)
	if result != "frontend, bug, urgent" {
		t.Errorf("formatCell(string array) = %q, want %q", result, "frontend, bug, urgent")
	}

	// Test number arrays
	numbers := []any{float64(1), float64(2), float64(3)}
	result = formatCell(numbers)
	if result != "1, 2, 3" {
		t.Errorf("formatCell(number array) = %q, want %q", result, "1, 2, 3")
	}

	// Test mixed arrays
	mixed := []any{"a", float64(1), "b"}
	result = formatCell(mixed)
	if result != "a, 1, b" {
		t.Errorf("formatCell(mixed array) = %q, want %q", result, "a, 1, b")
	}

	// Test empty array
	empty := []any{}
	result = formatCell(empty)
	if result != "" {
		t.Errorf("formatCell(empty array) = %q, want %q", result, "")
	}
}

func TestFormatCellWithMapArray(t *testing.T) {
	// Test maps with name key (assignees)
	assignees := []any{
		map[string]any{"id": float64(1), "name": "Alice"},
		map[string]any{"id": float64(2), "name": "Bob"},
	}
	result := formatCell(assignees)
	if result != "Alice, Bob" {
		t.Errorf("formatCell(assignees) = %q, want %q", result, "Alice, Bob")
	}

	// Test maps with title key (no name)
	items := []any{
		map[string]any{"id": float64(1), "title": "Task A"},
		map[string]any{"id": float64(2), "title": "Task B"},
	}
	result = formatCell(items)
	if result != "Task A, Task B" {
		t.Errorf("formatCell(items with title) = %q, want %q", result, "Task A, Task B")
	}

	// Test maps with only id (fallback)
	attachments := []any{
		map[string]any{"id": float64(100)},
		map[string]any{"id": float64(200)},
	}
	result = formatCell(attachments)
	if result != "100, 200" {
		t.Errorf("formatCell(attachments) = %q, want %q", result, "100, 200")
	}
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
	if err != nil {
		t.Fatalf("Err() failed: %v", err)
	}

	output := buf.String()
	// Should NOT be JSON
	if strings.Contains(output, `"ok":`) {
		t.Errorf("Markdown error output should not contain JSON, got: %s", output)
	}
	// Should contain styled error message
	if !strings.Contains(output, "Error:") {
		t.Errorf("Markdown error output should contain 'Error:', got: %s", output)
	}
	if !strings.Contains(output, "project not found") {
		t.Errorf("Markdown error output should contain error message, got: %s", output)
	}
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
	if err != nil {
		t.Fatalf("OK() failed: %v", err)
	}

	output := buf.String()
	// Should NOT be JSON
	if strings.Contains(output, `"ok":`) {
		t.Errorf("Markdown list output should not contain JSON, got: %s", output)
	}
	// Should contain summary
	if !strings.Contains(output, "2 projects") {
		t.Errorf("Markdown output should contain summary, got: %s", output)
	}
	// Should contain data
	if !strings.Contains(output, "Project A") {
		t.Errorf("Markdown output should contain data, got: %s", output)
	}
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
	if err != nil {
		t.Fatalf("OK() failed: %v", err)
	}

	output := buf.String()
	// Should NOT be JSON
	if strings.Contains(output, `"ok":`) {
		t.Errorf("Markdown object output should not contain JSON, got: %s", output)
	}
	// Should contain key-value pairs (keys are now title-cased via formatHeader)
	if !strings.Contains(output, "Id") || !strings.Contains(output, "123") {
		t.Errorf("Markdown output should contain Id: 123, got: %s", output)
	}
	if !strings.Contains(output, "Completed") || !strings.Contains(output, "no") {
		t.Errorf("Markdown output should contain Completed: no, got: %s", output)
	}
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
	if err != nil {
		t.Fatalf("OK() failed: %v", err)
	}

	output := buf.String()
	// Should contain breadcrumb (literal Markdown uses "### Next" heading)
	if !strings.Contains(output, "Next") {
		t.Errorf("Markdown output should contain 'Next', got: %s", output)
	}
	if !strings.Contains(output, "bcq show 1") {
		t.Errorf("Markdown output should contain breadcrumb command, got: %s", output)
	}
}

func TestWriterMarkdownNoANSIWhenNotTTY(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatMarkdown,
		Writer: &buf, // bytes.Buffer is not a TTY
	})

	err := w.Err(ErrNotFound("project", "123"))
	if err != nil {
		t.Fatalf("Err() failed: %v", err)
	}

	output := buf.String()
	// Should NOT contain ANSI escape codes when not a TTY
	if strings.Contains(output, "\x1b[") {
		t.Errorf("Markdown output should not contain ANSI codes when not TTY, got: %q", output)
	}
	// Should still contain the error message
	if !strings.Contains(output, "Error:") {
		t.Errorf("Markdown output should contain 'Error:', got: %s", output)
	}
}

func TestWriterStyledEmitsANSI(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatStyled,
		Writer: &buf, // bytes.Buffer is not a TTY, but FormatStyled forces ANSI
	})

	err := w.Err(ErrNotFound("project", "123"))
	if err != nil {
		t.Fatalf("Err() failed: %v", err)
	}

	output := buf.String()
	// SHOULD contain ANSI escape codes when FormatStyled is used
	if !strings.Contains(output, "\x1b[") {
		t.Errorf("Styled output should contain ANSI codes, got: %q", output)
	}
	// Should still contain the error message
	if !strings.Contains(output, "Error:") {
		t.Errorf("Styled output should contain 'Error:', got: %s", output)
	}
}

func TestWriterMarkdownOutputsLiteralMarkdown(t *testing.T) {
	var buf bytes.Buffer
	w := New(Options{
		Format: FormatMarkdown,
		Writer: &buf,
	})

	err := w.Err(ErrNotFound("project", "123"))
	if err != nil {
		t.Fatalf("Err() failed: %v", err)
	}

	output := buf.String()
	// Should NOT contain ANSI escape codes
	if strings.Contains(output, "\x1b[") {
		t.Errorf("Markdown output should NOT contain ANSI codes, got: %q", output)
	}
	// Should contain Markdown syntax
	if !strings.Contains(output, "**Error:**") {
		t.Errorf("Markdown output should contain '**Error:**', got: %s", output)
	}
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
		if seen[format] {
			t.Errorf("Duplicate format value: %d", format)
		}
		seen[format] = true
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
	if err != nil {
		t.Fatalf("OK() failed: %v", err)
	}

	output := buf.String()
	if output != "999\n" {
		t.Errorf("IDs output for single item = %q, want %q", output, "999\n")
	}
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
	if err != nil {
		t.Fatalf("OK() failed: %v", err)
	}

	output := buf.String()
	if output != "" {
		t.Errorf("IDs output for item without id = %q, want empty", output)
	}
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
			if tc.err.HTTPStatus != tc.expectedStatus {
				t.Errorf("HTTPStatus = %d, want %d", tc.err.HTTPStatus, tc.expectedStatus)
			}
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
			if !tc.err.Retryable {
				t.Error("Expected error to be retryable")
			}
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
			if tc.err.Retryable {
				t.Error("Expected error not to be retryable")
			}
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
			if result != tt.expected {
				t.Errorf("formatDateValue(%q, %v) = %q, want %q", tt.key, tt.value, result, tt.expected)
			}
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

			if !strings.Contains(result, tt.contains) {
				t.Errorf("formatDateValue(%q) = %q, expected to contain %q", timestamp, result, tt.contains)
			}
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
				if result == testValue {
					t.Errorf("Date column %q should format the date, got raw value", tc.key)
				}
			} else {
				// Non-date columns should return value unchanged
				if result != testValue {
					t.Errorf("Non-date column %q should return value unchanged, got %q", tc.key, result)
				}
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
			if result != tt.expected {
				t.Errorf("formatHeader(%q) = %q, want %q", tt.input, result, tt.expected)
			}
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
	if err != nil {
		t.Fatalf("OK() failed: %v", err)
	}

	output := buf.String()

	// Verify that id appears before name, name before status, etc.
	idPos := strings.Index(output, "Id")
	namePos := strings.Index(output, "Name")
	statusPos := strings.Index(output, "Status")
	descPos := strings.Index(output, "Description")
	createdPos := strings.Index(output, "Created")
	updatedPos := strings.Index(output, "Updated")

	if idPos == -1 {
		t.Error("Output should contain 'Id'")
	}
	if namePos == -1 {
		t.Error("Output should contain 'Name'")
	}

	// Verify ordering: id < name < status < description < created < updated
	if idPos > namePos {
		t.Errorf("Id (priority 1) should appear before Name (priority 2)")
	}
	if namePos > statusPos {
		t.Errorf("Name (priority 2) should appear before Status (priority 4)")
	}
	if statusPos > descPos {
		t.Errorf("Status (priority 4) should appear before Description (priority 7)")
	}
	if descPos > createdPos {
		t.Errorf("Description (priority 7) should appear before Created (priority 8)")
	}
	if createdPos > updatedPos {
		t.Errorf("Created (priority 8) should appear before Updated (priority 9)")
	}
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
	if err != nil {
		t.Fatalf("OK() failed: %v", err)
	}

	output := buf.String()

	// Should use humanized headers
	if !strings.Contains(output, "Id") {
		t.Error("Output should contain humanized 'Id' header")
	}
	if !strings.Contains(output, "Created") {
		t.Error("Output should contain humanized 'Created' header (not 'created_at')")
	}
	if strings.Contains(output, "created_at") {
		t.Error("Output should NOT contain raw 'created_at' key")
	}
	if !strings.Contains(output, "Due") {
		t.Error("Output should contain humanized 'Due' header (not 'due_on')")
	}
	if strings.Contains(output, "due_on") {
		t.Error("Output should NOT contain raw 'due_on' key")
	}
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
	if err != nil {
		t.Fatalf("OK() failed: %v", err)
	}

	output := buf.String()

	// Should contain visible fields
	if !strings.Contains(output, "Id") {
		t.Error("Output should contain 'Id'")
	}
	if !strings.Contains(output, "Name") {
		t.Error("Output should contain 'Name'")
	}

	// Should NOT contain skipped fields
	skippedFields := []string{
		"bucket", "creator", "url", "app_url",
		"bookmark_url", "subscription_url", "comments_count",
		"comments_url", "position", "inherits_status",
	}
	for _, field := range skippedFields {
		// Check for both raw key and title-cased version
		if strings.Contains(strings.ToLower(output), field) {
			t.Errorf("Output should NOT contain skipped field %q", field)
		}
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
		if !skipObjectColumns[field] {
			t.Errorf("skipObjectColumns should contain %q", field)
		}
	}
}
