package commands

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/auth"
	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/names"
	"github.com/basecamp/bcq/internal/output"
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
	t.Setenv("BCQ_NO_KEYRING", "1")

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

// TestTodosRequiresProject tests that No project specified for todos.
func TestTodosRequiresProject(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	// No project in config

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app)
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "No project specified", e.Message)
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
	assert.Equal(t, "No project specified", e.Message)
}

// TestTodosCreateRequiresContent tests that todos create requires content.
func TestTodosCreateRequiresContent(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"
	app.Config.TodolistID = "456"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "create")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "Todo content required", e.Message)
}

// TestTodosShowRequiresID tests that todos show requires an ID argument.
func TestTodosShowRequiresID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "show")
	require.Error(t, err)

	assert.Equal(t, "accepts 1 arg(s), received 0", err.Error())
}

// TestTodosCompleteRequiresID tests that todos complete requires an ID argument.
func TestTodosCompleteRequiresID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "complete")
	require.Error(t, err)

	// Cobra validates required args
	assert.Equal(t, "requires at least 1 arg(s), only received 0", err.Error())
}

// TestTodosUncompleteRequiresID tests that todos uncomplete requires an ID argument.
func TestTodosUncompleteRequiresID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "uncomplete")
	require.Error(t, err)

	// Cobra validates required args
	assert.Equal(t, "requires at least 1 arg(s), only received 0", err.Error())
}

// TestTodosPositionRequiresID tests that todos position requires an ID argument.
func TestTodosPositionRequiresID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "position", "--to", "1")
	require.Error(t, err)

	// Cobra validates required args
	assert.Equal(t, "accepts 1 arg(s), received 0", err.Error())
}

// TestTodosPositionRequiresPosition tests that todos position requires --to.
func TestTodosPositionRequiresPosition(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "position", "456")
	require.Error(t, err)

	// Cobra validates required flags
	assert.Equal(t, `required flag(s) "to" not set`, err.Error())
}

// TestTodoShortcutRequiresContent tests that todo shortcut requires content.
func TestTodoShortcutRequiresContent(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"
	app.Config.TodolistID = "456"

	cmd := NewTodoCmd()

	err := executeTodosCommand(cmd, app)
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "Todo content required", e.Message)
}

// TestTodoShortcutRequiresProject tests that todo shortcut requires project.
func TestTodoShortcutRequiresProject(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	// No project in config

	cmd := NewTodoCmd()

	err := executeTodosCommand(cmd, app, "--content", "Test todo")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "No project specified", e.Message)
}

// TestDoneRequiresID tests that done command requires an ID.
func TestDoneRequiresID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewDoneCmd()

	err := executeTodosCommand(cmd, app)
	require.Error(t, err)

	// Cobra validates required args
	assert.Equal(t, "requires at least 1 arg(s), only received 0", err.Error())
}

// TestReopenRequiresID tests that reopen command requires an ID.
func TestReopenRequiresID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewReopenCmd()

	err := executeTodosCommand(cmd, app)
	require.Error(t, err)

	// Cobra validates required args
	assert.Equal(t, "requires at least 1 arg(s), only received 0", err.Error())
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
