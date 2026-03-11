package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	expected := []string{"list", "show", "create", "complete", "uncomplete", "position"}
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
	assert.Contains(t, e.Hint, "--todoset <id>")
	assert.Contains(t, e.Hint, "Engineering (ID: 100)")
	assert.Contains(t, e.Hint, "Design (ID: 200)")
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
