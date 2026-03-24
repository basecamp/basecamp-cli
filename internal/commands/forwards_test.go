package commands

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockForwardsSortTransport struct {
	query string
}

func (t *mockForwardsSortTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.query = req.URL.RawQuery

	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	switch req.URL.Path {
	case "/99999/projects.json":
		return &http.Response{
			StatusCode: 200,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`[{"id":123,"name":"Project"}]`)),
		}, nil
	case "/99999/projects/123.json":
		return &http.Response{
			StatusCode: 200,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"id":123,"dock":[{"name":"inbox","id":456,"title":"Inbox","enabled":true}]}`)),
		}, nil
	case "/99999/inboxes/456/forwards.json":
		return &http.Response{
			StatusCode: 200,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`[]`)),
		}, nil
	default:
		return &http.Response{
			StatusCode: 404,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"error":"not found"}`)),
		}, nil
	}
}

func TestForwardsListSort(t *testing.T) {
	transport := &mockForwardsSortTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewForwardsCmd()
	err := executeCommand(cmd, app, "list", "--in", "123", "--sort", "updated", "--reverse")
	require.NoError(t, err)

	assert.Equal(t, "direction=desc&sort=updated_at", transport.query)
}

func TestForwardsListRejectsBadSort(t *testing.T) {
	app, _ := setupTestApp(t)

	cmd := NewForwardsCmd()
	err := executeCommand(cmd, app, "list", "--sort", "subject")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--sort must be created or updated")
}
