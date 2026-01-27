package commands

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/auth"
	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/names"
	"github.com/basecamp/bcq/internal/output"
)

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

	authMgr := auth.NewManager(cfg, nil)
	sdkCfg := &basecamp.Config{
		AccountID: cfg.AccountID,
	}
	sdkClient := basecamp.NewClient(sdkCfg, &todosTestTokenProvider{})
	nameResolver := names.NewResolver(sdkClient, authMgr)

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
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if e, ok := err.(*output.Error); ok {
		if e.Message != "No project specified" {
			t.Errorf("expected 'No project specified', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestTodosListRequiresProject tests that todos list requires --project.
func TestTodosListRequiresProject(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	// No project in config

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "list")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if e, ok := err.(*output.Error); ok {
		if e.Message != "No project specified" {
			t.Errorf("expected 'No project specified', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestTodosCreateRequiresContent tests that todos create requires content.
func TestTodosCreateRequiresContent(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"
	app.Config.TodolistID = "456"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "create")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if e, ok := err.(*output.Error); ok {
		if e.Message != "Todo content required" {
			t.Errorf("expected 'Todo content required', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestTodosShowRequiresID tests that todos show requires an ID argument.
func TestTodosShowRequiresID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "show")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errStr := err.Error()
	if errStr != "accepts 1 arg(s), received 0" {
		t.Errorf("expected args error, got %q", errStr)
	}
}

// TestTodosCompleteRequiresID tests that todos complete requires an ID argument.
func TestTodosCompleteRequiresID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "complete")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates required args
	errStr := err.Error()
	if errStr != "requires at least 1 arg(s), only received 0" {
		t.Errorf("expected args error, got %q", errStr)
	}
}

// TestTodosUncompleteRequiresID tests that todos uncomplete requires an ID argument.
func TestTodosUncompleteRequiresID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "uncomplete")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates required args
	errStr := err.Error()
	if errStr != "requires at least 1 arg(s), only received 0" {
		t.Errorf("expected args error, got %q", errStr)
	}
}

// TestTodosPositionRequiresID tests that todos position requires an ID argument.
func TestTodosPositionRequiresID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "position", "--to", "1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates required args
	errStr := err.Error()
	if errStr != "accepts 1 arg(s), received 0" {
		t.Errorf("expected args error, got %q", errStr)
	}
}

// TestTodosPositionRequiresPosition tests that todos position requires --to.
func TestTodosPositionRequiresPosition(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewTodosCmd()

	err := executeTodosCommand(cmd, app, "position", "456")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates required flags
	errStr := err.Error()
	if errStr != `required flag(s) "to" not set` {
		t.Errorf("expected required flag error for --to, got %q", errStr)
	}
}

// TestTodoShortcutRequiresContent tests that todo shortcut requires content.
func TestTodoShortcutRequiresContent(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"
	app.Config.TodolistID = "456"

	cmd := NewTodoCmd()

	err := executeTodosCommand(cmd, app)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if e, ok := err.(*output.Error); ok {
		if e.Message != "Todo content required" {
			t.Errorf("expected 'Todo content required', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestTodoShortcutRequiresProject tests that todo shortcut requires project.
func TestTodoShortcutRequiresProject(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	// No project in config

	cmd := NewTodoCmd()

	err := executeTodosCommand(cmd, app, "--content", "Test todo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if e, ok := err.(*output.Error); ok {
		if e.Message != "No project specified" {
			t.Errorf("expected 'No project specified', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestDoneRequiresID tests that done command requires an ID.
func TestDoneRequiresID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewDoneCmd()

	err := executeTodosCommand(cmd, app)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates required args
	errStr := err.Error()
	if errStr != "requires at least 1 arg(s), only received 0" {
		t.Errorf("expected args error, got %q", errStr)
	}
}

// TestReopenRequiresID tests that reopen command requires an ID.
func TestReopenRequiresID(t *testing.T) {
	app, _ := setupTodosTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewReopenCmd()

	err := executeTodosCommand(cmd, app)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates required args
	errStr := err.Error()
	if errStr != "requires at least 1 arg(s), only received 0" {
		t.Errorf("expected args error, got %q", errStr)
	}
}

// TestTodosSubcommands tests that all expected subcommands exist.
func TestTodosSubcommands(t *testing.T) {
	cmd := NewTodosCmd()

	expected := []string{"list", "show", "create", "complete", "uncomplete", "position"}
	for _, name := range expected {
		sub, _, err := cmd.Find([]string{name})
		if err != nil {
			t.Errorf("expected subcommand %q to exist, got error: %v", name, err)
		}
		if sub == nil {
			t.Errorf("expected subcommand %q to exist, got nil", name)
		}
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
	if flag == nil {
		t.Fatal("expected --list flag to exist")
	}
}

// TestTodosHasAssigneeFlag tests that --assignee flag is available.
func TestTodosHasAssigneeFlag(t *testing.T) {
	cmd := NewTodosCmd()

	// Check list subcommand for assignee flag
	listCmd, _, _ := cmd.Find([]string{"list"})
	if listCmd == nil {
		t.Fatal("expected list subcommand to exist")
	}

	flag := listCmd.Flags().Lookup("assignee")
	if flag == nil {
		t.Fatal("expected --assignee flag on list subcommand")
	}
}

// Note: Invalid assignee format testing requires API mocking because
// assignee validation happens after authentication checks.
// This is tested in the Bash integration tests (test/errors.bats).
