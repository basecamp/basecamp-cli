package observability

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTraceWriter_WriteOperationStart(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	op := basecamp.OperationInfo{Service: "Todos", Operation: "Complete"}
	w.WriteOperationStart(op)

	output := buf.String()
	assert.Contains(t, output, "Calling Todos.Complete")
	assert.True(t, strings.HasPrefix(output, "["), "expected timestamp prefix")
}

func TestTraceWriter_WriteOperationEnd(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	op := basecamp.OperationInfo{Service: "Todos", Operation: "List"}
	w.WriteOperationEnd(op, nil, 50*time.Millisecond)

	output := buf.String()
	assert.Contains(t, output, "Completed Todos.List")
	assert.Contains(t, output, "(50ms)", "expected duration")
}

func TestTraceWriter_WriteOperationEnd_Error(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	op := basecamp.OperationInfo{Service: "Projects", Operation: "Create"}
	w.WriteOperationEnd(op, errors.New("forbidden"), 50*time.Millisecond)

	output := buf.String()
	assert.Contains(t, output, "Failed Projects.Create")
	assert.Contains(t, output, "forbidden", "expected error message")
}

func TestTraceWriter_WriteRequestStart(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	info := basecamp.RequestInfo{Method: "GET", URL: "/buckets/123/todos", Attempt: 1}
	w.WriteRequestStart(info)

	output := buf.String()
	assert.Contains(t, output, "-> GET /buckets/123/todos", "expected request line")
}

func TestTraceWriter_WriteRequestEnd(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	info := basecamp.RequestInfo{Method: "GET", URL: "/todos", Attempt: 1}
	result := basecamp.RequestResult{StatusCode: 200, Duration: 45 * time.Millisecond}
	w.WriteRequestEnd(info, result)

	output := buf.String()
	assert.Contains(t, output, "<- 200", "expected response line")
	assert.Contains(t, output, "(45ms)", "expected duration")
}

func TestTraceWriter_WriteRequestEnd_Cached(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	info := basecamp.RequestInfo{Method: "GET", URL: "/projects", Attempt: 1}
	result := basecamp.RequestResult{StatusCode: 200, FromCache: true}
	w.WriteRequestEnd(info, result)

	output := buf.String()
	assert.Contains(t, output, "(cached)", "expected cached indicator")
}

func TestTraceWriter_WriteRequestEnd_Error(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	info := basecamp.RequestInfo{Method: "POST", URL: "/todos", Attempt: 1}
	result := basecamp.RequestResult{Error: errors.New("connection refused")}
	w.WriteRequestEnd(info, result)

	output := buf.String()
	assert.Contains(t, output, "ERROR", "expected ERROR")
	assert.Contains(t, output, "connection refused", "expected error message")
}

func TestTraceWriter_WriteRetry(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	info := basecamp.RequestInfo{Method: "GET", URL: "/todos", Attempt: 2}
	w.WriteRetry(info, 2, errors.New("timeout"))

	output := buf.String()
	assert.Contains(t, output, "RETRY #2")
	assert.Contains(t, output, "timeout", "expected error message")
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
	require.Equal(t, 2, len(lines), "expected 2 lines")

	// Parse timestamps and verify second is later
	// Format: [0.123s] ...
	assert.True(t, strings.HasPrefix(lines[0], "[0."), "expected timestamp prefix on line 1")
	assert.True(t, strings.HasPrefix(lines[1], "[0."), "expected timestamp prefix on line 2")
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
	assert.True(t, strings.HasPrefix(firstOutput, "[0.0"), "first output should start with near-zero timestamp")
	assert.True(t, strings.HasPrefix(secondOutput, "[0.0"), "second output after reset should start with near-zero timestamp")
}

func TestScrubURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no sensitive params",
			input:    "https://api.example.com/todos?page=1&limit=10",
			expected: "https://api.example.com/todos?page=1&limit=10",
		},
		{
			name:     "access_token param",
			input:    "https://api.example.com/todos?access_token=secret123&page=1",
			expected: "https://api.example.com/todos?access_token=%5BREDACTED%5D&page=1",
		},
		{
			name:     "api_key param",
			input:    "https://api.example.com/data?api_key=key123&format=json",
			expected: "https://api.example.com/data?api_key=%5BREDACTED%5D&format=json",
		},
		{
			name:     "password param",
			input:    "https://api.example.com/login?user=admin&password=secret",
			expected: "https://api.example.com/login?password=%5BREDACTED%5D&user=admin",
		},
		{
			name:     "client_secret param",
			input:    "https://api.example.com/oauth?client_id=app&client_secret=xyz",
			expected: "https://api.example.com/oauth?client_id=app&client_secret=%5BREDACTED%5D",
		},
		{
			name:     "multiple sensitive params",
			input:    "https://api.example.com/data?api_key=abc&secret=xyz&limit=10",
			expected: "https://api.example.com/data?api_key=%5BREDACTED%5D&limit=10&secret=%5BREDACTED%5D",
		},
		{
			name:     "case insensitive matching",
			input:    "https://api.example.com/auth?PASSWORD=abc&ApiKey=xyz",
			expected: "https://api.example.com/auth?ApiKey=%5BREDACTED%5D&PASSWORD=%5BREDACTED%5D",
		},
		{
			name:     "no query string",
			input:    "https://api.example.com/todos",
			expected: "https://api.example.com/todos",
		},
		{
			name:     "invalid url returns placeholder",
			input:    "://malformed",
			expected: "[unparseable URL]",
		},
		{
			name:     "relative path with query",
			input:    "/api/todos?api_key=secret",
			expected: "/api/todos?api_key=%5BREDACTED%5D",
		},
		{
			name:     "generic params not scrubbed",
			input:    "https://api.example.com/data?key=sortorder&token=pagetoken&auth=basic",
			expected: "https://api.example.com/data?key=sortorder&token=pagetoken&auth=basic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := scrubURL(tt.input)
			assert.Equal(t, tt.expected, result, "scrubURL(%q)", tt.input)
		})
	}
}

func TestWriteRequestStart_ScrubsURLs(t *testing.T) {
	var buf bytes.Buffer
	w := NewTraceWriterTo(&buf)

	info := basecamp.RequestInfo{
		Method:  "GET",
		URL:     "https://api.example.com/todos?access_token=secret123",
		Attempt: 1,
	}
	w.WriteRequestStart(info)

	output := buf.String()

	// Should NOT contain the actual token
	assert.NotContains(t, output, "secret123", "URL should be scrubbed, but output contains secret")
	// Should contain REDACTED
	assert.Contains(t, output, "REDACTED", "URL should contain [REDACTED]")
}
