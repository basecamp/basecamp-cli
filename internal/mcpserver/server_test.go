package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/mcp/mcptest"
)

type testTokenProvider struct{}

func (testTokenProvider) AccessToken(context.Context) (string, error) {
	return "test-token", nil
}

// newTestAPI returns an account-scoped basecamp-sdk client — the same kind
// the CLI hands the server — pointed at upstream.
func newTestAPI(upstream *httptest.Server) *basecamp.AccountClient {
	client := basecamp.NewClient(&basecamp.Config{BaseURL: upstream.URL}, testTokenProvider{},
		basecamp.WithMaxRetries(1))
	return client.ForAccount("999")
}

func TestServerListsDomainTools(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(upstream.Close)

	srv, err := New(newTestAPI(upstream), Config{})
	require.NoError(t, err)
	session := mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))

	tools := mcptest.ListTools(t, session)
	assert.Len(t, tools, 15)
	require.Contains(t, tools, "basecamp_projects")
	projects := tools["basecamp_projects"]
	assert.Contains(t, projects.Description, "list_projects")
	assert.Contains(t, projects.Description, "create_project")
}

func TestServerDescribeServesSchemas(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(upstream.Close)

	srv, err := New(newTestAPI(upstream), Config{})
	require.NoError(t, err)
	session := mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))

	text, isError := mcptest.CallText(t, session, "basecamp_projects", map[string]any{
		"action": "describe",
		"params": map[string]any{"action": "get_project"},
	})
	require.False(t, isError, "describe failed: %s", text)
	assert.Contains(t, text, "projectId")
	assert.NotContains(t, text, "accountId", "describe must not advertise the account parameter the CLI supplies")
}

func TestServerDispatchesThroughSDKClient(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/999/projects.json", r.URL.Path)
		require.Equal(t, "archived", r.URL.Query().Get("status"))
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1,"name":"Making a Podcast"}]`))
	}))
	t.Cleanup(upstream.Close)

	srv, err := New(newTestAPI(upstream), Config{})
	require.NoError(t, err)
	session := mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))

	text, isError := mcptest.CallText(t, session, "basecamp_projects", map[string]any{
		"action": "list_projects",
		"params": map[string]any{"status": "archived"},
	})
	require.False(t, isError, "list_projects failed: %s", text)
	var projects []struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &projects))
	require.Len(t, projects, 1)
	assert.Equal(t, "Making a Podcast", projects[0].Name)
}

func TestServerWrapsBodyAndCreates(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/999/todolists/2/todos.json", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "Buy milk", body["content"])
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":3,"content":"Buy milk"}`))
	}))
	t.Cleanup(upstream.Close)

	srv, err := New(newTestAPI(upstream), Config{})
	require.NoError(t, err)
	session := mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))

	text, isError := mcptest.CallText(t, session, "basecamp_todos", map[string]any{
		"action": "create_todo",
		"params": map[string]any{"todolistId": "2", "content": "Buy milk"},
	})
	require.False(t, isError, "create_todo failed: %s", text)
	assert.Contains(t, text, `"id":3`)
}

func TestServerSurfacesEmptyBodyStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/999/todos/2/completion.json", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	srv, err := New(newTestAPI(upstream), Config{})
	require.NoError(t, err)
	session := mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))

	text, isError := mcptest.CallText(t, session, "basecamp_todos", map[string]any{
		"action": "complete_todo",
		"params": map[string]any{"todoId": "2"},
	})
	require.False(t, isError, "complete_todo failed: %s", text)
	assert.Contains(t, text, `"status": 204`)
}

func TestServerSurfacesPagination(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Link", `<http://`+r.Host+`/999/projects.json?page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	t.Cleanup(upstream.Close)

	srv, err := New(newTestAPI(upstream), Config{})
	require.NoError(t, err)
	session := mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))

	text, isError := mcptest.CallText(t, session, "basecamp_projects", map[string]any{
		"action": "list_projects",
		"params": map[string]any{},
	})
	require.False(t, isError, "list_projects failed: %s", text)
	var wrapped struct {
		NextPage int             `json:"next_page"`
		Results  json.RawMessage `json:"results"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &wrapped))
	assert.Equal(t, 2, wrapped.NextPage, "next_page is a number, matching the page parameter's integer schema")
	assert.JSONEq(t, `[{"id":1}]`, string(wrapped.Results))
}

// TestServerAcceptsSynthesizedPageParam drives the pagination round trip
// through list_webhooks, one of the operations the model marks paginated
// without declaring a page parameter: the next_page a listing returns must
// be acceptable as the follow-up call's page parameter.
func TestServerAcceptsSynthesizedPageParam(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/999/buckets/1/webhooks.json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"id":2}]`))
			return
		}
		w.Header().Set("Link", `<http://`+r.Host+`/999/buckets/1/webhooks.json?page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	t.Cleanup(upstream.Close)

	srv, err := New(newTestAPI(upstream), Config{})
	require.NoError(t, err)
	session := mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))

	text, isError := mcptest.CallText(t, session, "basecamp_automation", map[string]any{
		"action": "list_webhooks",
		"params": map[string]any{"bucketId": "1"},
	})
	require.False(t, isError, "list_webhooks failed: %s", text)
	var wrapped struct {
		NextPage int `json:"next_page"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &wrapped))
	require.Equal(t, 2, wrapped.NextPage)

	text, isError = mcptest.CallText(t, session, "basecamp_automation", map[string]any{
		"action": "list_webhooks",
		"params": map[string]any{"bucketId": "1", "page": wrapped.NextPage},
	})
	require.False(t, isError, "passing next_page back must dispatch, got: %s", text)
	assert.JSONEq(t, `[{"id":2}]`, text)
}

func TestServerSurfacesAPIErrorsInBand(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Record not found"}`, http.StatusNotFound)
	}))
	t.Cleanup(upstream.Close)

	srv, err := New(newTestAPI(upstream), Config{})
	require.NoError(t, err)
	session := mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))

	text, isError := mcptest.CallText(t, session, "basecamp_projects", map[string]any{
		"action": "get_project",
		"params": map[string]any{"projectId": "12345"},
	})
	assert.True(t, isError, "a 404 must come back in-band, got: %s", text)
	assert.NotEmpty(t, text)
}

func TestServerReadOnlyDropsWrites(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(upstream.Close)

	srv, err := New(newTestAPI(upstream), Config{ReadOnly: true})
	require.NoError(t, err)
	session := mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))

	tools := mcptest.ListTools(t, session)
	require.Contains(t, tools, "basecamp_projects")
	projects := tools["basecamp_projects"]
	assert.Contains(t, projects.Description, "list_projects")
	assert.NotContains(t, projects.Description, "create_project")
	require.NotNil(t, projects.Annotations)
	assert.True(t, projects.Annotations.ReadOnlyHint)

	text, isError := mcptest.CallText(t, session, "basecamp_projects", map[string]any{
		"action": "create_project",
		"params": map[string]any{"name": "Nope"},
	})
	assert.True(t, isError, "write dispatch must be refused in read-only mode, got: %s", text)
}

func TestServerDomainNarrowing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(upstream.Close)

	srv, err := New(newTestAPI(upstream), Config{Domains: []string{"projects", "todos"}})
	require.NoError(t, err)
	session := mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))

	tools := mcptest.ListTools(t, session)
	assert.Len(t, tools, 2)
	assert.Contains(t, tools, "basecamp_projects")
	assert.Contains(t, tools, "basecamp_todos")
}

func TestServerUnknownDomainFailsClosed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(upstream.Close)

	_, err := New(newTestAPI(upstream), Config{Domains: []string{"projects", "nonsense"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown domain "nonsense"`)
}

func TestServerRequiresAPI(t *testing.T) {
	_, err := New(nil, Config{})
	assert.Error(t, err)
}
