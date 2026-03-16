package resolve

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// mockDockTransport returns canned project JSON for dock tool resolution tests.
type mockDockTransport struct {
	projectJSON string
}

func (t *mockDockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Path, "/projects/") {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(t.projectJSON)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	}
	return nil, errors.New("unexpected request: " + req.URL.Path)
}

type testTokenProvider struct{}

func (testTokenProvider) AccessToken(_ context.Context) (string, error) {
	return "test-token", nil
}

func newTestResolver(transport http.RoundTripper, flags *Flags) *Resolver {
	sdk := basecamp.NewClient(&basecamp.Config{}, testTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	cfg := &config.Config{AccountID: "99999"}
	if flags == nil {
		flags = &Flags{JSON: true} // non-interactive by default
	}
	return New(sdk, nil, cfg, WithFlags(flags))
}

func TestDockToolSingleAutoSelects(t *testing.T) {
	transport := &mockDockTransport{
		projectJSON: `{"id": 1, "dock": [{"name": "todoset", "id": 100, "enabled": true}]}`,
	}
	r := newTestResolver(transport, nil)

	result, err := r.Todoset(context.Background(), "1", "")
	require.NoError(t, err)
	assert.Equal(t, "100", result.ToolID)
	assert.Equal(t, SourceDefault, result.Source)
}

func TestDockToolExplicitIDBypassesFetch(t *testing.T) {
	transport := &mockDockTransport{
		projectJSON: `{"id": 1, "dock": []}`, // would fail if fetched
	}
	r := newTestResolver(transport, nil)

	result, err := r.Todoset(context.Background(), "1", "42")
	require.NoError(t, err)
	assert.Equal(t, "42", result.ToolID)
	assert.Equal(t, SourceFlag, result.Source)
}

func TestDockToolMultiNonInteractiveError(t *testing.T) {
	transport := &mockDockTransport{
		projectJSON: `{"id": 1, "dock": [
			{"name": "todoset", "id": 100, "title": "Engineering", "enabled": true},
			{"name": "todoset", "id": 200, "title": "Design", "enabled": true}
		]}`,
	}
	// JSON mode → non-interactive
	r := newTestResolver(transport, &Flags{JSON: true})

	_, err := r.Todoset(context.Background(), "1", "")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Equal(t, "Multiple todosets found", e.Message)
	assert.Contains(t, e.Hint, "Specify one with --todoset <id>:")
	assert.Contains(t, e.Hint, "  100  Engineering")
	assert.Contains(t, e.Hint, "  200  Design")
}

func TestDockToolMultiInboxPluralizesCorrectly(t *testing.T) {
	transport := &mockDockTransport{
		projectJSON: `{"id": 1, "dock": [
			{"name": "inbox", "id": 300, "title": "Support", "enabled": true},
			{"name": "inbox", "id": 400, "title": "Billing", "enabled": true}
		]}`,
	}
	// JSON mode → non-interactive
	r := newTestResolver(transport, &Flags{JSON: true})

	_, err := r.Inbox(context.Background(), "1", "")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Equal(t, "Multiple inboxes found", e.Message)
	assert.Contains(t, e.Hint, "Specify one with --inbox <id>:")
	assert.Contains(t, e.Hint, "  300  Support")
	assert.Contains(t, e.Hint, "  400  Billing")
}

func TestDockToolNoneFound(t *testing.T) {
	transport := &mockDockTransport{
		projectJSON: `{"id": 1, "dock": []}`,
	}
	r := newTestResolver(transport, nil)

	_, err := r.Todoset(context.Background(), "1", "")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeNotFound, e.Code)
}

func TestDockToolDisabledNotReturned(t *testing.T) {
	transport := &mockDockTransport{
		projectJSON: `{"id": 1, "dock": [
			{"name": "todoset", "id": 100, "enabled": false},
			{"name": "todoset", "id": 200, "enabled": true}
		]}`,
	}
	r := newTestResolver(transport, nil)

	result, err := r.Todoset(context.Background(), "1", "")
	require.NoError(t, err)
	assert.Equal(t, "200", result.ToolID)
	assert.Equal(t, SourceDefault, result.Source)
}

func TestDockToolDisabledOnlyShowsDisabledError(t *testing.T) {
	transport := &mockDockTransport{
		projectJSON: `{"id": 1, "dock": [{"name": "todoset", "id": 100, "enabled": false}]}`,
	}
	r := newTestResolver(transport, nil)

	_, err := r.Todoset(context.Background(), "1", "")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Contains(t, e.Hint, "disabled for this project")
}

// todolistResolverTransport handles both project dock and todolist list calls.
type todolistResolverTransport struct {
	projectJSON   string
	todolistsJSON string
}

func (t *todolistResolverTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := http.Header{"Content-Type": []string{"application/json"}}

	if strings.Contains(req.URL.Path, "/projects/") && !strings.Contains(req.URL.Path, "todolists") {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(t.projectJSON)),
			Header:     header,
		}, nil
	}
	if strings.Contains(req.URL.Path, "/todolists.json") {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(t.todolistsJSON)),
			Header:     header,
		}, nil
	}
	return nil, errors.New("unexpected request: " + req.URL.Path)
}

func TestTodolistResolutionUsesTodosetResolver(t *testing.T) {
	// Multi-todoset project: Todolist() → fetchTodolists() → getTodosetID()
	// should fail with ambiguous error, not silently pick the first.
	transport := &todolistResolverTransport{
		projectJSON: `{"id": 1, "dock": [
			{"name": "todoset", "id": 100, "title": "Engineering", "enabled": true},
			{"name": "todoset", "id": 200, "title": "Design", "enabled": true}
		]}`,
		todolistsJSON: `[{"id": 10, "name": "Sprint 1"}]`,
	}
	r := newTestResolver(transport, &Flags{JSON: true})

	_, err := r.Todolist(context.Background(), "1", "")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Contains(t, e.Hint, "--todoset <id>")
}

func TestTodolistResolutionSingleTodosetWorks(t *testing.T) {
	// Single todoset: should auto-resolve through to todolists
	transport := &todolistResolverTransport{
		projectJSON:   `{"id": 1, "dock": [{"name": "todoset", "id": 100, "enabled": true}]}`,
		todolistsJSON: `[{"id": 10, "name": "Sprint 1"}]`,
	}
	r := newTestResolver(transport, &Flags{JSON: true})

	result, err := r.Todolist(context.Background(), "1", "")
	require.NoError(t, err)
	assert.Equal(t, "10", result.Value)
	assert.Equal(t, SourceDefault, result.Source)
}
