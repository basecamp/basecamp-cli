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

type mockNotificationsTransport struct {
	capturedPath   string
	capturedMethod string
	capturedBody   []byte
}

func (t *mockNotificationsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.capturedPath = req.URL.Path
	t.capturedMethod = req.Method
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		t.capturedBody = body
		_ = req.Body.Close()
	}

	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/99999/my/readings.json":
		return &http.Response{
			StatusCode: 200,
			Header:     header,
			Body: io.NopCloser(strings.NewReader(`{
				"unreads": [{"id":1,"readable_sgid":"sgid-1","created_at":"2026-03-24T00:00:00Z","updated_at":"2026-03-24T00:00:00Z"}],
				"reads": [{"id":2,"readable_sgid":"sgid-2","created_at":"2026-03-24T00:00:00Z","updated_at":"2026-03-24T00:00:00Z"}],
				"memories": []
			}`)),
		}, nil
	case req.Method == http.MethodPut && req.URL.Path == "/99999/my/unreads.json":
		return &http.Response{
			StatusCode: 204,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	default:
		return &http.Response{
			StatusCode: 404,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"error":"not found"}`)),
		}, nil
	}
}

func TestNotificationsList(t *testing.T) {
	transport := &mockNotificationsTransport{}
	app, buf := newTestAppWithTransport(t, transport)

	cmd := NewNotificationsCmd()
	err := executeCommand(cmd, app, "list")
	require.NoError(t, err)

	assert.Equal(t, http.MethodGet, transport.capturedMethod)
	assert.Equal(t, "/99999/my/readings.json", transport.capturedPath)
	assert.Contains(t, buf.String(), `"readable_sgid": "sgid-1"`)
}

func TestNotificationsRead(t *testing.T) {
	transport := &mockNotificationsTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewNotificationsCmd()
	err := executeCommand(cmd, app, "read", "sgid-1", "sgid-2")
	require.NoError(t, err)

	assert.Equal(t, http.MethodPut, transport.capturedMethod)
	assert.Equal(t, "/99999/my/unreads.json", transport.capturedPath)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	assert.Equal(t, []any{"sgid-1", "sgid-2"}, body["readables"])
}
