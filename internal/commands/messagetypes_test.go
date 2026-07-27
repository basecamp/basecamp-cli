package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/names"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// recordedCall captures one request seen by the recording transport.
type recordedCall struct {
	Method string
	Path   string
	Query  string
	Body   string
}

// stubRoute matches a request by method + exact URL path and serves a canned
// response.
type stubRoute struct {
	method string
	path   string
	status int
	body   string
}

// recordingTransport is an http.RoundTripper that records every request and
// serves canned responses from its route table. Unmatched requests get an
// error response — a wrong route fails the command rather than passing
// silently.
type recordingTransport struct {
	mu       sync.Mutex
	requests []recordedCall
	routes   []stubRoute
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		body = string(b)
	}

	t.mu.Lock()
	t.requests = append(t.requests, recordedCall{
		Method: req.Method,
		Path:   req.URL.Path,
		Query:  req.URL.RawQuery,
		Body:   body,
	})
	t.mu.Unlock()

	for _, route := range t.routes {
		if route.method == req.Method && route.path == req.URL.Path {
			return &http.Response{
				StatusCode: route.status,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(route.body)),
				Request:    req,
			}, nil
		}
	}

	return &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(fmt.Sprintf(
			`{"error":"no stub route for %s %s"}`, req.Method, req.URL.Path))),
		Request: req,
	}, nil
}

// recorded returns a snapshot of the requests seen so far.
func (t *recordingTransport) recorded() []recordedCall {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]recordedCall(nil), t.requests...)
}

// last returns the most recent request, failing the test if none were made.
func (t *recordingTransport) last(tb testing.TB) recordedCall {
	tb.Helper()
	reqs := t.recorded()
	require.NotEmpty(tb, reqs, "expected at least one request")
	return reqs[len(reqs)-1]
}

// projectsRoute serves the project list that name resolution fetches first.
func projectsRoute() stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   "/99999/projects.json",
		status: http.StatusOK,
		body:   `[{"id":123,"name":"Test Project"}]`,
	}
}

// setupRecordingTestApp creates a test app whose SDK client talks to a
// recording stub transport seeded with the given routes.
func setupRecordingTestApp(t *testing.T, routes ...stubRoute) (*appctx.App, *recordingTransport) {
	t.Helper()

	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &recordingTransport{routes: routes}
	cfg := &config.Config{
		AccountID: "99999",
	}

	authMgr := auth.NewManager(cfg, nil)
	sdkCfg := &basecamp.Config{BaseURL: "https://3.basecampapi.com"}
	sdkClient := basecamp.NewClient(sdkCfg, &messagesTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: &bytes.Buffer{},
		}),
	}
	return app, transport
}

// executeRecordingCommand executes a cobra command against the test app.
func executeRecordingCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)

	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	return cmd.Execute()
}

const messageTypeJSON = `{"id":456,"name":"Announcement","icon":"📣","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`

// Message types are bucket-scoped: every operation must forward the resolved
// project as the bucket in the URL. Collection paths carry .json; member
// paths do not (pinned by SDK message_types_test.go).

func TestMessagetypesListHitsBucketScopedPath(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodGet,
		path:   "/99999/buckets/123/categories.json",
		status: http.StatusOK,
		body:   "[" + messageTypeJSON + "]",
	})

	err := executeRecordingCommand(NewMessagetypesCmd(), app, "list", "-p", "123")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodGet, last.Method)
	assert.Equal(t, "/99999/buckets/123/categories.json", last.Path)
}

func TestMessagetypesShowHitsBucketScopedPath(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodGet,
		path:   "/99999/buckets/123/categories/456",
		status: http.StatusOK,
		body:   messageTypeJSON,
	})

	err := executeRecordingCommand(NewMessagetypesCmd(), app, "show", "456", "-p", "123")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodGet, last.Method)
	assert.Equal(t, "/99999/buckets/123/categories/456", last.Path)
}

func TestMessagetypesCreateHitsBucketScopedPath(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodPost,
		path:   "/99999/buckets/123/categories.json",
		status: http.StatusCreated,
		body:   messageTypeJSON,
	})

	err := executeRecordingCommand(NewMessagetypesCmd(), app,
		"create", "Announcement", "--icon", "📣", "-p", "123")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodPost, last.Method)
	assert.Equal(t, "/99999/buckets/123/categories.json", last.Path)
}

func TestMessagetypesUpdateHitsBucketScopedPath(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodPut,
		path:   "/99999/buckets/123/categories/456",
		status: http.StatusOK,
		body:   messageTypeJSON,
	})

	err := executeRecordingCommand(NewMessagetypesCmd(), app,
		"update", "456", "--name", "Heads-up", "-p", "123")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodPut, last.Method)
	assert.Equal(t, "/99999/buckets/123/categories/456", last.Path)
}

func TestMessagetypesDeleteHitsBucketScopedPath(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodDelete,
		path:   "/99999/buckets/123/categories/456",
		status: http.StatusNoContent,
		body:   "",
	})

	err := executeRecordingCommand(NewMessagetypesCmd(), app, "delete", "456", "-p", "123")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodDelete, last.Method)
	assert.Equal(t, "/99999/buckets/123/categories/456", last.Path)
}

// Validation must run before project resolution: a usage error should surface
// without any network traffic.

func TestMessagetypesCreateIconValidationPrecedesResolution(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute())

	err := executeRecordingCommand(NewMessagetypesCmd(), app, "create", "Announcement", "-p", "123")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "--icon is required", e.Message)
	assert.Empty(t, transport.recorded(), "validation should fail before project resolution")
}

func TestMessagetypesUpdateNoChangesPrecedesResolution(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	app, transport := setupRecordingTestApp(t, projectsRoute())

	err := executeRecordingCommand(NewMessagetypesCmd(), app, "update", "456", "-p", "123")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "No update fields specified", e.Message)
	assert.Empty(t, transport.recorded(), "no-changes check should fail before project resolution")
}

func TestMessagetypesShowInvalidIDPrecedesResolution(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute())

	err := executeRecordingCommand(NewMessagetypesCmd(), app, "show", "abc", "-p", "123")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "Invalid message type ID", e.Message)
	assert.Empty(t, transport.recorded(), "ID validation should fail before project resolution")
}
