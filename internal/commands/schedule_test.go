package commands

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// TestScheduleCreateHasSubscribeFlags tests that schedule create has --subscribe and --no-subscribe flags.
func TestScheduleCreateHasSubscribeFlags(t *testing.T) {
	cmd := NewScheduleCmd()

	createCmd, _, err := cmd.Find([]string{"create"})
	require.NoError(t, err)

	flag := createCmd.Flags().Lookup("subscribe")
	require.NotNil(t, flag, "expected --subscribe flag on schedule create")

	flag = createCmd.Flags().Lookup("no-subscribe")
	require.NotNil(t, flag, "expected --no-subscribe flag on schedule create")
}

// TestScheduleCreateSubscribeEmptyIsError tests that --subscribe "" is rejected on schedule create.
func TestScheduleCreateSubscribeEmptyIsError(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewScheduleCmd()

	err := executeMessagesCommand(cmd, app, "create", "Standup",
		"--starts-at", "2026-03-04T09:00:00Z",
		"--ends-at", "2026-03-04T09:30:00Z",
		"--subscribe", "")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "at least one person")
}

// TestScheduleCreateSubscribeMutualExclusion tests that --subscribe and --no-subscribe are mutually exclusive.
func TestScheduleCreateSubscribeMutualExclusion(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewScheduleCmd()

	err := executeMessagesCommand(cmd, app, "create", "Standup",
		"--starts-at", "2026-03-04T09:00:00Z",
		"--ends-at", "2026-03-04T09:30:00Z",
		"--subscribe", "me", "--no-subscribe")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "mutually exclusive")
}

// =============================================================================
// Schedule Description HTML Conversion Tests
// =============================================================================

// mockScheduleCreateTransport handles resolver and dock API calls, and captures the POST/PUT body.
type mockScheduleCreateTransport struct {
	capturedBody []byte
}

func (t *mockScheduleCreateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "GET" {
		var body string
		if strings.Contains(req.URL.Path, "/projects.json") {
			body = `[{"id": 123, "name": "Test Project"}]`
		} else if strings.Contains(req.URL.Path, "/projects/") {
			body = `{"id": 123, "dock": [{"name": "schedule", "id": 777, "enabled": true}]}`
		} else {
			body = `{}`
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	if req.Method == "POST" || req.Method == "PUT" {
		if req.Body != nil {
			body, _ := io.ReadAll(req.Body)
			t.capturedBody = body
			req.Body.Close()
		}
		mockResp := `{"id": 999, "summary": "Event", "starts_at": "2026-03-04T09:00:00Z", "ends_at": "2026-03-04T09:30:00Z"}`
		status := 201
		if req.Method == "PUT" {
			status = 200
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     header,
		}, nil
	}

	return nil, errors.New("unexpected request")
}

func TestScheduleCreateDescriptionIsHTML(t *testing.T) {
	transport := &mockScheduleCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewScheduleCmd()
	err := executeMessagesCommand(cmd, app, "create", "Event",
		"--starts-at", "2026-03-04T09:00:00Z",
		"--ends-at", "2026-03-04T09:30:00Z",
		"--description", "**details**")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	desc, ok := body["description"].(string)
	require.True(t, ok)
	assert.Contains(t, desc, "<strong>details</strong>")
}

func TestScheduleUpdateDescriptionIsHTML(t *testing.T) {
	transport := &mockScheduleCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewScheduleCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--description", "**details**")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	desc, ok := body["description"].(string)
	require.True(t, ok)
	assert.Contains(t, desc, "<strong>details</strong>")
}

func TestScheduleCreateLocalImageErrors(t *testing.T) {
	transport := &mockScheduleCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewScheduleCmd()
	err := executeMessagesCommand(cmd, app, "create", "Event",
		"--starts-at", "2026-03-04T09:00:00Z",
		"--ends-at", "2026-03-04T09:30:00Z",
		"--description", "![alt](./missing.png)")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing.png")
}

func TestScheduleUpdateLocalImageErrors(t *testing.T) {
	transport := &mockScheduleCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewScheduleCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--description", "![alt](./missing.png)")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing.png")
}

// =============================================================================
// Schedule Show Occurrence URL Tests
// =============================================================================

// mockScheduleShowTransport tracks GET requests to verify occurrence routing.
type mockScheduleShowTransport struct {
	requests []string
}

func (t *mockScheduleShowTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, req.URL.Path)

	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	body := `{"id": 789, "summary": "Recurring Event", "starts_at": "2025-12-29T09:00:00Z", "ends_at": "2025-12-29T10:00:00Z"}`
	if strings.Contains(req.URL.Path, "/projects.json") {
		body = `[{"id": 456, "name": "Test Project"}]`
	} else if strings.Contains(req.URL.Path, "/projects/") {
		body = `{"id": 456, "dock": [{"name": "schedule", "id": 777, "enabled": true}]}`
	}

	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}, nil
}

func TestScheduleShowOccurrenceURLExtractsDate(t *testing.T) {
	transport := &mockScheduleShowTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewScheduleCmd()
	err := executeMessagesCommand(cmd, app, "show",
		"https://3.basecamp.com/99999/buckets/456/schedule_entries/789/occurrences/20251229")
	require.NoError(t, err)

	// Verify the occurrence endpoint was called, not the plain entry endpoint
	var hitOccurrence bool
	for _, path := range transport.requests {
		if strings.Contains(path, "/schedule_entries/789/occurrences/20251229") {
			hitOccurrence = true
		}
	}
	assert.True(t, hitOccurrence,
		"occurrence URL should hit the occurrence endpoint; got requests: %v", transport.requests)
}

func TestScheduleShowOccurrenceURLDateFlagTakesPrecedence(t *testing.T) {
	transport := &mockScheduleShowTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewScheduleCmd()
	// --date flag should override the date from the URL
	err := executeMessagesCommand(cmd, app, "show",
		"https://3.basecamp.com/99999/buckets/456/schedule_entries/789/occurrences/20251229",
		"--date", "20260101")
	require.NoError(t, err)

	var hitFlagDate bool
	for _, path := range transport.requests {
		if strings.Contains(path, "/occurrences/20260101") {
			hitFlagDate = true
		}
	}
	assert.True(t, hitFlagDate,
		"--date flag should take precedence over URL date; got requests: %v", transport.requests)
}

func TestScheduleShowPlainEntryURLNoOccurrence(t *testing.T) {
	transport := &mockScheduleShowTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewScheduleCmd()
	err := executeMessagesCommand(cmd, app, "show",
		"https://3.basecamp.com/99999/buckets/456/schedule_entries/789")
	require.NoError(t, err)

	var hitPlainEntry bool
	for _, path := range transport.requests {
		if strings.Contains(path, "/schedule_entries/789") && !strings.Contains(path, "/occurrences/") {
			hitPlainEntry = true
		}
	}
	assert.True(t, hitPlainEntry,
		"plain entry URL should not hit the occurrence endpoint; got requests: %v", transport.requests)
}
