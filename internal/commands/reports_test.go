package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockReportsAssignmentsTransport struct {
	capturedPath string
	query        string
}

func (t *mockReportsAssignmentsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.capturedPath = req.URL.Path
	t.query = req.URL.RawQuery

	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	switch req.URL.Path {
	case "/99999/my/assignments.json":
		return &http.Response{
			StatusCode: 200,
			Header:     header,
			Body: io.NopCloser(strings.NewReader(`{
				"priorities": [{"id":1,"content":"Priority","bucket":{"id":123,"name":"Project"}}],
				"non_priorities": [{"id":2,"content":"Normal","bucket":{"id":123,"name":"Project"}}]
			}`)),
		}, nil
	case "/99999/my/assignments/completed.json":
		return &http.Response{
			StatusCode: 200,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`[{"id":3,"content":"Done"}]`)),
		}, nil
	case "/99999/my/assignments/due.json":
		return &http.Response{
			StatusCode: 200,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`[{"id":4,"content":"Due soon"}]`)),
		}, nil
	default:
		return &http.Response{
			StatusCode: 404,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"error":"not found"}`)),
		}, nil
	}
}

func TestReportsMine(t *testing.T) {
	transport := &mockReportsAssignmentsTransport{}
	app, buf := newTestAppWithTransport(t, transport)

	cmd := NewReportsCmd()
	err := executeCommand(cmd, app, "mine")
	require.NoError(t, err)

	assert.Equal(t, "/99999/my/assignments.json", transport.capturedPath)

	var envelope struct {
		Data struct {
			Priorities []map[string]any `json:"priorities"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	assert.Len(t, envelope.Data.Priorities, 1)
}

func TestReportsCompleted(t *testing.T) {
	transport := &mockReportsAssignmentsTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewReportsCmd()
	err := executeCommand(cmd, app, "completed")
	require.NoError(t, err)

	assert.Equal(t, "/99999/my/assignments/completed.json", transport.capturedPath)
}

func TestReportsDue(t *testing.T) {
	transport := &mockReportsAssignmentsTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewReportsCmd()
	err := executeCommand(cmd, app, "due", "--scope", "due_today")
	require.NoError(t, err)

	assert.Equal(t, "/99999/my/assignments/due.json", transport.capturedPath)
	assert.Equal(t, "scope=due_today", transport.query)
}

func TestReportsDueRejectsBadScope(t *testing.T) {
	app, _ := setupTestApp(t)

	cmd := NewReportsCmd()
	err := executeCommand(cmd, app, "due", "--scope", "tomorrowish")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--scope must be overdue")
}
