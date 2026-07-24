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
)

// mockTodolistCreateTransport resolves the todoset via the project dock and
// captures the POST body sent to create a todolist.
type mockTodolistCreateTransport struct {
	capturedBody []byte
}

func (t *mockTodolistCreateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "GET" {
		var body string
		if strings.Contains(req.URL.Path, "/projects.json") {
			body = `[{"id": 123, "name": "Test Project"}]`
		} else if strings.Contains(req.URL.Path, "/projects/") {
			body = `{"id": 123, "dock": [{"name": "todoset", "id": 777, "enabled": true}]}`
		} else {
			body = `{}`
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	if req.Method == "POST" && strings.Contains(req.URL.Path, "/todolists.json") {
		if req.Body != nil {
			body, _ := io.ReadAll(req.Body)
			t.capturedBody = body
			req.Body.Close()
		}
		return &http.Response{
			StatusCode: 201,
			Body:       io.NopCloser(strings.NewReader(`{"id": 999, "name": "My list"}`)),
			Header:     header,
		}, nil
	}

	return nil, errors.New("unexpected request")
}

func TestTodolistsCreateHasVisibleToClientsFlag(t *testing.T) {
	cmd := NewTodolistsCmd()
	createCmd, _, err := cmd.Find([]string{"create"})
	require.NoError(t, err)

	flag := createCmd.Flags().Lookup("visible-to-clients")
	require.NotNil(t, flag, "expected --visible-to-clients flag on todolists create")
}

func TestTodolistsCreateDefaultOmitsVisibleToClients(t *testing.T) {
	transport := &mockTodolistCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewTodolistsCmd()
	err := executeMessagesCommand(cmd, app, "create", "My list")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))

	_, ok := body["visible_to_clients"]
	assert.False(t, ok, "expected visible_to_clients to be omitted when flag is not set")
}

func TestTodolistsCreateVisibleToClientsTrue(t *testing.T) {
	transport := &mockTodolistCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewTodolistsCmd()
	err := executeMessagesCommand(cmd, app, "create", "My list", "--visible-to-clients")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))

	assert.Equal(t, true, body["visible_to_clients"])
}

func TestTodolistsCreateVisibleToClientsFalse(t *testing.T) {
	transport := &mockTodolistCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewTodolistsCmd()
	err := executeMessagesCommand(cmd, app, "create", "My list", "--visible-to-clients=false")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))

	val, ok := body["visible_to_clients"]
	require.True(t, ok, "expected visible_to_clients present for explicit --visible-to-clients=false")
	assert.Equal(t, false, val)
}
