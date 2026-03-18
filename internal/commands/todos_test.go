package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// noNetworkTransport is an http.RoundTripper that fails immediately.
// Used in tests to prevent real network calls without waiting for timeouts.
type todosNoNetworkTransport struct{}

func (todosNoNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network disabled in tests")
}

// todosTestTokenProvider is a mock token provider for tests.
type todosTestTokenProvider struct{}

func (t *todosTestTokenProvider) AccessToken(_ context.Context) (string, error) {
	return "test-token", nil
}

// setupTodosTestApp creates a minimal test app context for todos tests.
func setupTodosTestApp(t *testing.T) (*appctx.App, *bytes.Buffer) {
	t.Helper()

	// Disable keyring access during tests
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
	}

	// Create SDK client with mock token provider and no-network transport
	// The transport prevents real HTTP calls - fails instantly instead of timing out
	authMgr := auth.NewManager(cfg, nil)
	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &todosTestTokenProvider{},
		basecamp.WithTransport(todosNoNetworkTransport{}),
		basecamp.WithMaxRetries(0), // Disable retries for instant failure
	)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}
	return app, buf
}

// executeTodosCommand executes a cobra command with the given args.
func executeTodosCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)

	// Suppress output during tests
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	return cmd.Execute()
}

// TestTodosShowsHelp tests that help is shown when called without subcommand.
func TestTodosShowsHelp(t *testing.T) {
	app, _ := setupTodosTestApp(t)

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app)
	assert.NoError(t, err)
}

// TestTodosListRequiresProject tests that todos list requires --project.
func TestTodosListRequiresProject(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	// No project in config

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "list")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "Project ID required", e.Message)
}

// TestTodosCreateRequiresContent tests that todos create requires content.
func TestTodosCreateShowsHelpWithoutContent(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"
	app.Config.TodolistID = "456"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "create")
	require.NoError(t, err, "expected help output, not an error")
}

// TestTodosShowShowsHelpWithoutID tests that todos show shows help when no ID given.
func TestTodosShowShowsHelpWithoutID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "show")
	require.NoError(t, err, "expected help output, not an error")
}

// TestTodosCompleteShowsHelpWithoutID tests that todos complete shows help when no ID given.
func TestTodosCompleteShowsHelpWithoutID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "complete")
	require.NoError(t, err, "expected help output, not an error")
}

// TestTodosUncompleteShowsHelpWithoutID tests that todos uncomplete shows help when no ID given.
func TestTodosUncompleteShowsHelpWithoutID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "uncomplete")
	require.NoError(t, err, "expected help output, not an error")
}

// TestTodosPositionShowsHelpWithoutID tests that todos position shows help when no ID given.
func TestTodosPositionShowsHelpWithoutID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "position")
	require.NoError(t, err, "expected help output, not an error")
}

// TestTodosPositionRequiresPosition tests that todos position requires --to.
func TestTodosPositionRequiresPosition(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "position", "456")
	require.Error(t, err)

	assert.Equal(t, "--to is required (1 = top)", err.Error())
}

// TestTodoShortcutRequiresContent tests that todo shortcut requires content.
func TestTodoShortcutShowsHelpWithoutContent(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"
	app.Config.TodolistID = "456"

	cmd := NewTodoCmd()

	err := executeTodosCommand(cmd, app)
	require.NoError(t, err, "expected help output, not an error")
}

// TestTodoShortcutRequiresProject tests that todo shortcut requires project.
func TestTodoShortcutRequiresProject(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	// No project in config

	cmd := NewTodoCmd()

	err := executeTodosCommand(cmd, app, "Test todo")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "Project ID required", e.Message)
}

// TestDoneShowsHelpWithoutID tests that done command shows help when no ID given.
func TestDoneShowsHelpWithoutID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewDoneCmd()

	err := executeTodosCommand(cmd, app)
	require.NoError(t, err, "expected help output, not an error")
}

// TestReopenShowsHelpWithoutID tests that reopen command shows help when no ID given.
func TestReopenShowsHelpWithoutID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewReopenCmd()

	err := executeTodosCommand(cmd, app)
	require.NoError(t, err, "expected help output, not an error")
}

// TestTodosSubcommands tests that all expected subcommands exist.
func TestTodosSubcommands(t *testing.T) {
	cmd := NewTodosCmd()

	expected := []string{"list", "show", "create", "update", "complete", "uncomplete", "position"}
	for _, name := range expected {
		sub, _, err := cmd.Find([]string{name})
		require.NoError(t, err, "expected subcommand %q to exist", name)
		require.NotNil(t, sub, "expected subcommand %q to exist", name)
	}
}

// TestTodosHasListFlag tests that -l/--list flag is available.
func TestTodosHasListFlag(t *testing.T) {
	cmd := NewTodosCmd()

	// The -l/--list flag should exist
	flag := cmd.Flags().Lookup("list")
	if flag == nil {
		// Try persistent flags
		flag = cmd.PersistentFlags().Lookup("list")
	}
	// If not on root, check a subcommand
	if flag == nil {
		listCmd, _, _ := cmd.Find([]string{"list"})
		if listCmd != nil {
			flag = listCmd.Flags().Lookup("list")
		}
	}
	require.NotNil(t, flag, "expected --list flag to exist")
}

// TestTodosHasAssigneeFlag tests that --assignee flag is available.
func TestTodosHasAssigneeFlag(t *testing.T) {
	cmd := NewTodosCmd()

	// Check list subcommand for assignee flag
	listCmd, _, _ := cmd.Find([]string{"list"})
	require.NotNil(t, listCmd, "expected list subcommand to exist")

	flag := listCmd.Flags().Lookup("assignee")
	require.NotNil(t, flag, "expected --assignee flag on list subcommand")
}

// Note: Invalid assignee format testing requires API mocking because
// assignee validation happens after authentication checks.
// This is tested in the Bash integration tests (test/errors.bats).

// mockTodoCreateTransport handles resolver API calls and captures the create request.
type mockTodoCreateTransport struct {
	capturedBody []byte
}

func (t *mockTodoCreateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	// Handle resolver calls with mock responses
	if req.Method == "GET" {
		var body string
		if strings.Contains(req.URL.Path, "/projects.json") {
			// Projects list - return array
			body = `[{"id": 123, "name": "Test Project"}]`
		} else if strings.Contains(req.URL.Path, "/projects/") {
			// Single project lookup - return project with todoset in dock
			body = `{"id": 123, "dock": [{"name": "todoset", "id": 789, "enabled": true}]}`
		} else if strings.Contains(req.URL.Path, "/todolists.json") {
			// Todolists lookup - return list containing our todolist
			body = `[{"id": 456, "name": "Test List"}]`
		} else {
			body = `{}`
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	// Capture POST request body (the create call)
	if req.Method == "POST" {
		if req.Body != nil {
			body, _ := io.ReadAll(req.Body)
			t.capturedBody = body
			req.Body.Close()
		}
		// Return a mock todo response
		mockResp := `{"id": 999, "title": "Test", "status": "active"}`
		return &http.Response{
			StatusCode: 201,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     header,
		}, nil
	}

	return nil, errors.New("unexpected request")
}

// TestTodosCreateContentIsPlainText verifies that todo content is sent as plain text,
// not wrapped in HTML tags. The Basecamp API expects plain text for the todo "content"
// field (which is the todo title), not HTML.
func TestTodosCreateContentIsPlainText(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockTodoCreateTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID:  "99999",
		ProjectID:  "123",
		TodolistID: "456",
	}

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &todosTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	authMgr := auth.NewManager(cfg, nil)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}

	cmd := NewTodosCmd()
	plainTextContent := "Fix the authentication bug"

	err := executeTodosCommand(cmd, app, "create", plainTextContent)
	require.NoError(t, err, "command should succeed with mock transport")
	require.NotEmpty(t, transport.capturedBody, "expected request body to be captured")

	var requestBody map[string]any
	err = json.Unmarshal(transport.capturedBody, &requestBody)
	require.NoError(t, err, "expected valid JSON in request body")

	content, ok := requestBody["content"].(string)
	require.True(t, ok, "expected 'content' field in request body")

	// The content should be exactly what was passed in - plain text, no HTML wrapping
	assert.Equal(t, plainTextContent, content,
		"Todo content should be plain text, not HTML-wrapped")
}

func TestTodosListAssigneeWithoutProjectErrors(t *testing.T) {
	app, _ := setupTodosTestApp(t)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "list", "--assignee", "me")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "--assignee requires a project")
	assert.Contains(t, e.Hint, "reports assigned")
}

func TestTodosListOverdueWithoutProjectErrors(t *testing.T) {
	app, _ := setupTodosTestApp(t)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "list", "--overdue")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "--overdue requires a project")
	assert.Contains(t, e.Hint, "reports overdue")
}

func TestTodosListAssigneeWithConfigDefaultProceeds(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "list", "--assignee", "me")
	require.Error(t, err)

	// Should proceed past the guard and fail on network (not the project error)
	var e *output.Error
	if errors.As(err, &e) {
		assert.NotContains(t, e.Message, "--assignee requires a project")
	}
}

func TestTodosListAssigneeWithFlagProceeds(t *testing.T) {
	app, _ := setupTodosTestApp(t)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "list", "--assignee", "me", "--in", "123")
	require.Error(t, err)

	// Should proceed past the guard and fail on project fetch (network disabled)
	var e *output.Error
	if errors.As(err, &e) {
		assert.NotContains(t, e.Message, "--assignee requires a project")
	}
}

func TestTodosSweepWithoutProjectErrors(t *testing.T) {
	app, _ := setupTodosTestApp(t)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "sweep", "--assignee", "me", "--comment", "test")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Sweep requires a project")
}

func TestTodosSweepOverdueWithoutProjectErrors(t *testing.T) {
	app, _ := setupTodosTestApp(t)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "sweep", "--overdue", "--complete")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Sweep requires a project")
}

// multiTodosetTransport returns a project with multiple todosets in its dock.
type multiTodosetTransport struct{}

func (multiTodosetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "GET" {
		var body string
		if strings.Contains(req.URL.Path, "/projects.json") {
			body = `[{"id": 123, "name": "Test"}]`
		} else if strings.Contains(req.URL.Path, "/projects/") {
			body = `{"id": 123, "dock": [
				{"name": "todoset", "id": 100, "title": "Engineering", "enabled": true},
				{"name": "todoset", "id": 200, "title": "Design", "enabled": true}
			]}`
		} else {
			body = `{}`
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}
	return nil, errors.New("unexpected request")
}

func setupMultiTodosetApp(t *testing.T) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &todosTestTokenProvider{},
		basecamp.WithTransport(multiTodosetTransport{}),
		basecamp.WithMaxRetries(1),
	)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	return &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}
}

func TestTodosListMultiTodosetAmbiguousError(t *testing.T) {
	app := setupMultiTodosetApp(t)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "list")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Contains(t, e.Hint, "Specify one with --todoset <id>:")
	assert.Contains(t, e.Hint, "  100  Engineering")
	assert.Contains(t, e.Hint, "  200  Design")
}

func TestTodosListMultiTodosetExplicitFlagWorks(t *testing.T) {
	app := setupMultiTodosetApp(t)

	cmd := NewTodosCmd()
	// --todoset 100 should bypass ambiguity — will proceed to fetch todolists
	// which the transport doesn't handle, so it'll fail with a different error
	err := executeTodosCommand(cmd, app, "list", "--todoset", "100")
	// Should NOT be an ambiguous error
	if err != nil {
		var e *output.Error
		if errors.As(err, &e) {
			assert.NotEqual(t, output.CodeAmbiguous, e.Code,
				"--todoset should bypass multi-todoset ambiguity")
		}
	}
}

func TestTodolistsListMultiTodosetAmbiguousError(t *testing.T) {
	app := setupMultiTodosetApp(t)

	cmd := NewTodolistsCmd()
	err := executeTodosCommand(cmd, app, "list")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Contains(t, e.Hint, "--todoset <id>")
}

func TestTodolistsCreateMultiTodosetAmbiguousError(t *testing.T) {
	app := setupMultiTodosetApp(t)

	cmd := NewTodolistsCmd()
	err := executeTodosCommand(cmd, app, "create", "Test List")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Contains(t, e.Hint, "--todoset <id>")
}

// todos404Transport returns HTTP 404 for all requests (no network delay).
type todos404Transport struct{}

func (todos404Transport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Body:       io.NopCloser(strings.NewReader(`{"error":"not found"}`)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func setupTodos404App(t *testing.T) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	cfg := &config.Config{AccountID: "99999"}
	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &todosTestTokenProvider{},
		basecamp.WithTransport(todos404Transport{}),
	)

	return &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, authMgr, cfg.AccountID),
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: &bytes.Buffer{},
		}),
	}
}

func TestDoneAllFailReturnsError(t *testing.T) {
	app := setupTodos404App(t)

	cmd := NewDoneCmd()
	err := executeTodosCommand(cmd, app, "123", "456")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "123")
	assert.Contains(t, err.Error(), "456")

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "expected *output.Error")
	assert.Equal(t, 404, outErr.HTTPStatus)
}

func TestReopenAllFailReturnsError(t *testing.T) {
	app := setupTodos404App(t)

	cmd := NewReopenCmd()
	err := executeTodosCommand(cmd, app, "123", "456")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "123")
	assert.Contains(t, err.Error(), "456")

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "expected *output.Error")
	assert.Equal(t, 404, outErr.HTTPStatus)
}

func TestDoneParseFailReturnsUsageError(t *testing.T) {
	app, _ := setupTodosTestApp(t)

	cmd := NewDoneCmd()
	// Non-numeric IDs trigger parse failures, not API errors
	err := executeTodosCommand(cmd, app, "abc", "def")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid todo ID(s)")
	assert.Contains(t, err.Error(), "abc")
	assert.Contains(t, err.Error(), "def")
}

// scopedTodosetTransport serves a multi-todoset project where each todoset has
// different todolists, and can create todos on a specific todolist.
type scopedTodosetTransport struct {
	createdOnTodolist int64 // records which todolist got the create call
}

func (s *scopedTodosetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	path := req.URL.Path
	status := 200

	var body string
	switch {
	case req.Method == "POST" && strings.Contains(path, "/todolists/"):
		// Create todo — extract todolist ID from path
		parts := strings.Split(path, "/")
		for i, p := range parts {
			if p == "todolists" && i+1 < len(parts) {
				if id, err := json.Number(parts[i+1]).Int64(); err == nil {
					s.createdOnTodolist = id
				}
			}
		}
		status = 201
		body = `{"id": 999, "content": "test", "status": "active", "completed": false}`
	case strings.Contains(path, "/projects.json"):
		body = `[{"id": 123, "name": "Test"}]`
	case strings.Contains(path, "/projects/"):
		body = `{"id": 123, "dock": [
			{"name": "todoset", "id": 100, "title": "Engineering", "enabled": true},
			{"name": "todoset", "id": 200, "title": "Design", "enabled": true}
		]}`
	case strings.Contains(path, "/todosets/100/todolists"):
		body = `[{"id": 10, "name": "Sprint 1"}, {"id": 11, "name": "Backlog"}]`
	case strings.Contains(path, "/todosets/200/todolists"):
		body = `[{"id": 20, "name": "UI Tasks"}, {"id": 21, "name": "Backlog"}]`
	default:
		body = `{}`
	}

	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}, nil
}

func setupScopedTodosetApp(t *testing.T, transport *scopedTodosetTransport) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &todosTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	return &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}
}

func TestTodoCreateWithTodosetScopesListResolution(t *testing.T) {
	// "Sprint 1" only exists in todoset 100. With --todoset 100, it should resolve.
	transport := &scopedTodosetTransport{}
	app := setupScopedTodosetApp(t, transport)

	cmd := NewTodoCmd()
	err := executeTodosCommand(cmd, app, "test todo", "--list", "Sprint 1", "--todoset", "100")
	require.NoError(t, err)
	assert.Equal(t, int64(10), transport.createdOnTodolist)
}

func TestTodoCreateWithTodosetRejectsWrongList(t *testing.T) {
	// "Sprint 1" is in todoset 100, not 200. With --todoset 200, it should fail.
	transport := &scopedTodosetTransport{}
	app := setupScopedTodosetApp(t, transport)

	cmd := NewTodoCmd()
	err := executeTodosCommand(cmd, app, "test todo", "--list", "Sprint 1", "--todoset", "200")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Sprint 1")
}

func TestTodoCreateWithTodosetDisambiguatesDuplicateNames(t *testing.T) {
	// "Backlog" exists in both todosets (id 11 in 100, id 21 in 200).
	// --todoset 200 should resolve to id 21.
	transport := &scopedTodosetTransport{}
	app := setupScopedTodosetApp(t, transport)

	cmd := NewTodoCmd()
	err := executeTodosCommand(cmd, app, "test todo", "--list", "Backlog", "--todoset", "200")
	require.NoError(t, err)
	assert.Equal(t, int64(21), transport.createdOnTodolist)
}

func TestTodosCreateWithTodosetScopesListResolution(t *testing.T) {
	// Same as TestTodoCreateWithTodosetScopesListResolution but via "todos create"
	transport := &scopedTodosetTransport{}
	app := setupScopedTodosetApp(t, transport)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "create", "test todo", "--list", "UI Tasks", "--todoset", "200")
	require.NoError(t, err)
	assert.Equal(t, int64(20), transport.createdOnTodolist)
}

func TestTodoCreateWithConfigTodolistAndTodosetScopes(t *testing.T) {
	// Config has todolist_id set to a name. --todoset should still scope resolution.
	transport := &scopedTodosetTransport{}
	app := setupScopedTodosetApp(t, transport)
	app.Config.TodolistID = "Backlog" // name, not numeric

	cmd := NewTodoCmd()
	err := executeTodosCommand(cmd, app, "test todo", "--todoset", "100")
	require.NoError(t, err)
	assert.Equal(t, int64(11), transport.createdOnTodolist,
		"config todolist_id 'Backlog' + --todoset 100 should resolve to todolist 11")
}

// paginatedTodosetTransport serves todolists across two pages to verify
// that todoset-scoped resolution follows pagination Link headers.
type paginatedTodosetTransport struct {
	createdOnTodolist int64
}

func (p *paginatedTodosetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	path := req.URL.Path
	query := req.URL.Query()
	status := 200

	var body string
	switch {
	case req.Method == "POST" && strings.Contains(path, "/todolists/"):
		parts := strings.Split(path, "/")
		for i, part := range parts {
			if part == "todolists" && i+1 < len(parts) {
				if id, err := json.Number(parts[i+1]).Int64(); err == nil {
					p.createdOnTodolist = id
				}
			}
		}
		status = 201
		body = `{"id": 999, "content": "test", "status": "active", "completed": false}`
	case strings.Contains(path, "/projects/"):
		body = `{"id": 123, "dock": [
			{"name": "todoset", "id": 300, "title": "Big Team", "enabled": true}
		]}`
	case strings.Contains(path, "/todosets/300/todolists"):
		if query.Get("page") == "2" {
			body = `[{"id": 32, "name": "Deep Backlog"}]`
		} else {
			// Page 1: Link header points to page 2 (same origin)
			page2URL := req.URL.Scheme + "://" + req.URL.Host + path + "?page=2"
			header.Set("Link", `<`+page2URL+`>; rel="next"`)
			body = `[{"id": 30, "name": "Sprint 1"}, {"id": 31, "name": "Sprint 2"}]`
		}
	default:
		body = `[]`
	}

	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
		Request:    req,
	}, nil
}

func TestTodoScopedResolutionPaginates(t *testing.T) {
	// "Deep Backlog" is on page 2; verifies GetAll follows Link headers.
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &paginatedTodosetTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}
	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{
		BaseURL: "https://3.basecampapi.com",
	}, &todosTestTokenProvider{},
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
			Writer: buf,
		}),
	}

	cmd := NewTodoCmd()
	err := executeTodosCommand(cmd, app, "test todo", "--in", "123", "--todoset", "300", "--list", "Deep Backlog")
	require.NoError(t, err)
	assert.Equal(t, int64(32), transport.createdOnTodolist,
		"should resolve 'Deep Backlog' from page 2 of todoset 300")
}

// ---------------------------------------------------------------------------
// Todolist group integration tests
// ---------------------------------------------------------------------------

// groupTodoTransport serves a todolist (ID 500) with interleaved direct
// todos and a group:
//
//	Position 1: direct todo (ID 1)
//	Position 2: group (ID 600) containing todo (ID 2)
//	Position 3: direct todo (ID 3)
//
// It also supports a cross-list aggregation path via todoset 900 containing
// todolist 500.
type groupTodoTransport struct{}

func (groupTodoTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	path := req.URL.Path
	var body string

	switch {
	// Project resolution
	case strings.Contains(path, "/projects.json"):
		body = `[{"id": 123, "name": "Test"}]`
	case strings.Contains(path, "/projects/"):
		body = `{"id": 123, "dock": [{"name": "todoset", "id": 900, "enabled": true}]}`

	// Todolist resolution — todoset lists
	case strings.Contains(path, "/todosets/900/todolists"):
		body = `[{"id": 500, "name": "Sprint"}]`

	// Groups for todolist 500
	case strings.Contains(path, "/todolists/500/groups.json"):
		body = `[{"id": 600, "position": 2, "name": "Group A"}]`

	// Todos in group 600
	case strings.Contains(path, "/todolists/600/todos.json"):
		body = `[{"id": 2, "title": "Group todo", "position": 1, "status": "active"}]`

	// Direct todos in todolist 500
	case strings.Contains(path, "/todolists/500/todos.json"):
		body = `[{"id": 1, "title": "First", "position": 1, "status": "active"},` +
			`{"id": 3, "title": "Third", "position": 3, "status": "active"}]`

	// No groups on group sublists
	case strings.Contains(path, "/todolists/600/groups.json"):
		body = `[]`

	default:
		body = `{}`
	}

	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}, nil
}

// groupErrorTransport is like groupTodoTransport but returns HTTP 403 for
// the /groups.json endpoint.
type groupErrorTransport struct{}

func (groupErrorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	path := req.URL.Path

	if strings.Contains(path, "/groups.json") {
		return &http.Response{
			StatusCode: 403,
			Body:       io.NopCloser(strings.NewReader(`{"error":"forbidden"}`)),
			Header:     header,
		}, nil
	}

	// Delegate everything else to the happy-path transport.
	return (groupTodoTransport{}).RoundTrip(req)
}

func setupGroupTodoApp(t *testing.T, transport http.RoundTripper) (*appctx.App, *bytes.Buffer) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &todosTestTokenProvider{},
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
			Writer: buf,
		}),
	}
	return app, buf
}

func TestTodosListInListMergesGroupTodosByPosition(t *testing.T) {
	app, buf := setupGroupTodoApp(t, groupTodoTransport{})

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "list", "--list", "500")
	require.NoError(t, err)

	var resp struct {
		Data []struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &resp))
	require.Len(t, resp.Data, 3, "expected 3 todos (2 direct + 1 from group)")

	// Position-ordered: direct(pos1) → group(pos2, containing todo 2) → direct(pos3)
	assert.Equal(t, int64(1), resp.Data[0].ID)
	assert.Equal(t, int64(2), resp.Data[1].ID)
	assert.Equal(t, int64(3), resp.Data[2].ID)
}

func TestTodosListAllIncludesGroupTodos(t *testing.T) {
	app, buf := setupGroupTodoApp(t, groupTodoTransport{})

	cmd := NewTodosCmd()
	// No --list: cross-list aggregation path
	err := executeTodosCommand(cmd, app, "list")
	require.NoError(t, err)

	var resp struct {
		Data []struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &resp))
	require.Len(t, resp.Data, 3, "expected 3 todos including group todo")
}

func TestTodosListInListGroupErrorFails(t *testing.T) {
	app, _ := setupGroupTodoApp(t, groupErrorTransport{})

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "list", "--list", "500")
	require.Error(t, err, "group fetch error should propagate in single-list mode")
}

func TestTodosListAllGroupErrorSkipped(t *testing.T) {
	app, buf := setupGroupTodoApp(t, groupErrorTransport{})

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "list")
	require.NoError(t, err, "group error should be skipped in cross-list mode")

	var resp struct {
		Data []struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &resp))
	assert.Len(t, resp.Data, 2, "should contain only direct todos when groups fail")
}

func TestTodosListInListLimitCapsFlattened(t *testing.T) {
	app, buf := setupGroupTodoApp(t, groupTodoTransport{})

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "list", "--list", "500", "--limit", "2")
	require.NoError(t, err)

	var resp struct {
		Data []struct {
			ID int64 `json:"id"`
		} `json:"data"`
		Notice string `json:"notice"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &resp))
	assert.Len(t, resp.Data, 2, "should cap to --limit 2")
	assert.NotEmpty(t, resp.Notice, "should include truncation notice")
}

func TestTodosListInListPageOneIsNoOp(t *testing.T) {
	app, buf := setupGroupTodoApp(t, groupTodoTransport{})

	cmd := NewTodosCmd()
	// --page 1 is the only valid value (2+ rejected by runTodosList) and is
	// the SDK default, so it succeeds even with grouped todolists.
	err := executeTodosCommand(cmd, app, "list", "--list", "500", "--page", "1")
	require.NoError(t, err)

	var resp struct {
		Data []struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &resp))
	assert.Len(t, resp.Data, 3, "should return all todos including group todos")
}

func TestTodosListInListLimitPreservedCrossList(t *testing.T) {
	app, buf := setupGroupTodoApp(t, groupTodoTransport{})

	cmd := NewTodosCmd()
	// Cross-list with --limit 1 should cap per-list results. Todolist 500
	// has groups, so the helper fetches all 3 todos for position merge then
	// caps to 1 before returning.
	err := executeTodosCommand(cmd, app, "list", "--limit", "1")
	require.NoError(t, err)

	var resp struct {
		Data []struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &resp))
	assert.Equal(t, 1, len(resp.Data))
}

// =============================================================================
// --status incomplete alias tests
// =============================================================================

// statusCapturingTransport serves a todolist with mixed-completion todos and
// captures the status query parameter sent to the todos.json endpoint.
type statusCapturingTransport struct {
	capturedStatus string
}

func (s *statusCapturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	path := req.URL.Path
	var body string

	switch {
	case strings.Contains(path, "/projects.json"):
		body = `[{"id": 123, "name": "Test"}]`
	case strings.Contains(path, "/projects/"):
		body = `{"id": 123, "dock": [{"name": "todoset", "id": 900, "enabled": true}]}`
	case strings.Contains(path, "/todosets/900/todolists"):
		body = `[{"id": 500, "name": "Sprint"}]`
	case strings.Contains(path, "/groups.json"):
		body = `[]`
	case strings.Contains(path, "/todos.json"):
		s.capturedStatus = req.URL.Query().Get("status")
		body = `[` +
			`{"id": 1, "content": "Open task", "position": 1, "status": "active", "completed": false},` +
			`{"id": 2, "content": "Done task", "position": 2, "status": "active", "completed": true}]`
	default:
		body = `{}`
	}

	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}, nil
}

func setupStatusTestApp(t *testing.T, transport *statusCapturingTransport) (*appctx.App, *bytes.Buffer) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &todosTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	return &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}, buf
}

func TestTodosListStatusIncomplete_SingleList_NormalizesToPending(t *testing.T) {
	transport := &statusCapturingTransport{}
	app, _ := setupStatusTestApp(t, transport)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "list", "--list", "500", "--status", "incomplete")
	require.NoError(t, err)

	assert.Equal(t, "pending", transport.capturedStatus,
		"--status incomplete should be normalized to 'pending' before reaching the SDK")
}

func TestTodosListStatusPending_SingleList_PassedThrough(t *testing.T) {
	transport := &statusCapturingTransport{}
	app, _ := setupStatusTestApp(t, transport)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "list", "--list", "500", "--status", "pending")
	require.NoError(t, err)

	assert.Equal(t, "pending", transport.capturedStatus,
		"--status pending should pass through unchanged")
}

func TestTodosListStatusIncomplete_CrossList_FiltersClientSide(t *testing.T) {
	transport := &statusCapturingTransport{}
	app, buf := setupStatusTestApp(t, transport)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "list", "--status", "incomplete")
	require.NoError(t, err)

	// Cross-list path fetches todos without a status filter and filters client-side.
	assert.Empty(t, transport.capturedStatus,
		"cross-list path should not send status to the API")

	var resp struct {
		Data []struct {
			ID        int64 `json:"id"`
			Completed bool  `json:"completed"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &resp))
	require.Len(t, resp.Data, 1, "should filter out completed todos")
	assert.Equal(t, int64(1), resp.Data[0].ID)
	assert.False(t, resp.Data[0].Completed)
}

// =============================================================================
// Sweep Comment HTML Conversion Tests
// =============================================================================

// mockSweepTransport serves a todolist with one overdue todo, captures comment POST bodies,
// and handles todo completion.
type mockSweepTransport struct {
	capturedCommentBody []byte
}

func (t *mockSweepTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "GET" {
		var body string
		switch {
		case strings.Contains(req.URL.Path, "/projects.json"):
			body = `[{"id": 123, "name": "Test Project"}]`
		case strings.Contains(req.URL.Path, "/todos.json"):
			// Return one overdue active todo
			body = `[{"id": 555, "title": "Overdue Todo", "status": "active", "completed": false, "due_on": "2020-01-01"}]`
		case strings.Contains(req.URL.Path, "/todolists.json"):
			body = `[{"id": 456, "name": "Test List"}]`
		case strings.Contains(req.URL.Path, "/projects/"):
			body = `{"id": 123, "dock": [{"name": "todoset", "id": 789, "enabled": true}]}`
		default:
			body = `{}`
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	if req.Method == "POST" {
		if req.Body != nil {
			body, _ := io.ReadAll(req.Body)
			if strings.Contains(req.URL.Path, "/comments.json") {
				t.capturedCommentBody = body
			}
			req.Body.Close()
		}
		mockResp := `{"id": 999, "status": "active"}`
		return &http.Response{
			StatusCode: 201,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     header,
		}, nil
	}

	return nil, errors.New("unexpected request")
}

func TestSweepCommentContentIsHTML(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockSweepTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	sdkClient := basecamp.NewClient(&basecamp.Config{}, &todosTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	authMgr := auth.NewManager(cfg, nil)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "sweep", "--overdue", "--comment", "**done** here")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedCommentBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedCommentBody, &body)
	require.NoError(t, err)

	content, ok := body["content"].(string)
	require.True(t, ok)
	assert.Contains(t, content, "<strong>done</strong>")
}

func TestSweepCommentLocalImageErrors(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockSweepTransport{}
	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	sdkClient := basecamp.NewClient(&basecamp.Config{}, &todosTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	authMgr := auth.NewManager(cfg, nil)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "sweep", "--overdue", "--comment", "![alt](./missing.png)")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing.png")
}

// =============================================================================
// Todos Update Tests
// =============================================================================

// mockTodoUpdateTransport handles GET and PUT for todo update tests.
type mockTodoUpdateTransport struct {
	capturedBody []byte
}

func (t *mockTodoUpdateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "GET" {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     header,
		}, nil
	}

	if req.Method == "PUT" {
		if req.Body != nil {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, fmt.Errorf("reading request body: %w", err)
			}
			t.capturedBody = body
			req.Body.Close()
		}
		mockResp := `{"id": 999, "title": "Updated", "status": "active", "completed": false}`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     header,
		}, nil
	}

	return nil, errors.New("unexpected request")
}

func setupTodoUpdateApp(t *testing.T, transport http.RoundTripper) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	cfg := &config.Config{
		AccountID: "99999",
	}

	sdkClient := basecamp.NewClient(&basecamp.Config{}, &todosTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	authMgr := auth.NewManager(cfg, nil)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	return &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: &bytes.Buffer{},
		}),
	}
}

func TestTodosUpdateShowsHelpWithoutID(t *testing.T) {
	app, _ := setupTodosTestApp(t)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "update")
	require.NoError(t, err, "expected help output, not an error")
}

func TestTodosUpdateNoChangesShowsHelp(t *testing.T) {
	app, _ := setupTodosTestApp(t)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "update", "123")
	// In non-machine mode (no format set), noChanges shows help → no error
	assert.NoError(t, err)
}

func TestTodosUpdatePositionalTitle(t *testing.T) {
	transport := &mockTodoUpdateTransport{}
	app := setupTodoUpdateApp(t, transport)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "update", "999", "New title")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	content, ok := body["content"].(string)
	require.True(t, ok)
	assert.Equal(t, "New title", content)
}

func TestTodosUpdateFlagTitle(t *testing.T) {
	transport := &mockTodoUpdateTransport{}
	app := setupTodoUpdateApp(t, transport)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "update", "999", "--title", "Flag title")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	content, ok := body["content"].(string)
	require.True(t, ok)
	assert.Equal(t, "Flag title", content)
}

func TestTodosUpdatePositionalTitleTakesPrecedence(t *testing.T) {
	transport := &mockTodoUpdateTransport{}
	app := setupTodoUpdateApp(t, transport)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "update", "999", "Positional", "--title", "Flag")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	content, ok := body["content"].(string)
	require.True(t, ok)
	assert.Equal(t, "Positional", content)
}

func TestTodosUpdateDescriptionIsHTML(t *testing.T) {
	transport := &mockTodoUpdateTransport{}
	app := setupTodoUpdateApp(t, transport)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "update", "999", "--description", "**bold** text")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	desc, ok := body["description"].(string)
	require.True(t, ok)
	assert.Contains(t, desc, "<strong>bold</strong>")
}

func TestTodosUpdateLocalImageErrors(t *testing.T) {
	transport := &mockTodoUpdateTransport{}
	app := setupTodoUpdateApp(t, transport)

	cmd := NewTodosCmd()
	err := executeTodosCommand(cmd, app, "update", "999", "--description", "![alt](./missing.png)")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing.png")
}
