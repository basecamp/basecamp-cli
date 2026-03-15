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
