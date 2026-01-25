package commands

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/api"
	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/auth"
	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/names"
	"github.com/basecamp/bcq/internal/output"
)

// setupMessagesTestApp creates a minimal test app context for messages tests.
func setupMessagesTestApp(t *testing.T) (*appctx.App, *bytes.Buffer) {
	t.Helper()

	// Disable keyring access during tests
	t.Setenv("BCQ_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
	}

	authMgr := auth.NewManager(cfg, nil)
	apiClient := api.NewClient(cfg, authMgr)
	nameResolver := names.NewResolver(apiClient, authMgr)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		API:    apiClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}
	return app, buf
}

// executeMessagesCommand executes a cobra command with the given args.
func executeMessagesCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)

	// Suppress output during tests
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	return cmd.Execute()
}

// TestMessagesRequiresProject tests that No project specified for messages.
func TestMessagesRequiresProject(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	// No project in config

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app)
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

// TestMessagesListRequiresProject tests that messages list requires --project.
func TestMessagesListRequiresProject(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	// No project in config

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "list")
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

// TestMessagesCreateRequiresSubject tests that messages create requires --subject.
func TestMessagesCreateRequiresSubject(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "create")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates required flags
	errStr := err.Error()
	if errStr != `required flag(s) "subject" not set` {
		t.Errorf("expected required flag error for --subject, got %q", errStr)
	}
}

// TestMessagesShowRequiresID tests that messages show requires an ID argument.
func TestMessagesShowRequiresID(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "show")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates required args
	errStr := err.Error()
	if errStr != "accepts 1 arg(s), received 0" {
		t.Errorf("expected args error, got %q", errStr)
	}
}

// TestMessagesPinRequiresID tests that messages pin requires an ID argument.
func TestMessagesPinRequiresID(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "pin")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errStr := err.Error()
	if errStr != "accepts 1 arg(s), received 0" {
		t.Errorf("expected args error, got %q", errStr)
	}
}

// TestMessagesUnpinRequiresID tests that messages unpin requires an ID argument.
func TestMessagesUnpinRequiresID(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "unpin")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errStr := err.Error()
	if errStr != "accepts 1 arg(s), received 0" {
		t.Errorf("expected args error, got %q", errStr)
	}
}

// TestMessagesUpdateRequiresID tests that messages update requires an ID argument.
func TestMessagesUpdateRequiresID(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "update")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errStr := err.Error()
	if errStr != "accepts 1 arg(s), received 0" {
		t.Errorf("expected args error, got %q", errStr)
	}
}

// TestMessagesUpdateRequiresContent tests that messages update requires --subject or --content.
func TestMessagesUpdateRequiresContent(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "update", "456")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if e, ok := err.(*output.Error); ok {
		if e.Message != "at least one of --subject or --content is required" {
			t.Errorf("expected 'at least one of --subject or --content is required', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// TestMessageShortcutRequiresSubject tests that message command requires --subject.
func TestMessageShortcutRequiresSubject(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessageCmd()

	err := executeMessagesCommand(cmd, app)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Cobra validates required flags
	errStr := err.Error()
	if errStr != `required flag(s) "subject" not set` {
		t.Errorf("expected required flag error for --subject, got %q", errStr)
	}
}

// TestMessageShortcutRequiresProject tests that message command requires --project.
func TestMessageShortcutRequiresProject(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	// No project in config

	cmd := NewMessageCmd()

	// Need to set subject to bypass that validation
	err := executeMessagesCommand(cmd, app, "--subject", "Test")
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

// TestMessagesHasMessageBoardFlag tests that --message-board flag is available.
func TestMessagesHasMessageBoardFlag(t *testing.T) {
	cmd := NewMessagesCmd()

	flag := cmd.PersistentFlags().Lookup("message-board")
	if flag == nil {
		t.Fatal("expected --message-board flag to exist")
	}

	if flag.Usage != "Message board ID (required if project has multiple)" {
		t.Errorf("unexpected flag usage: %q", flag.Usage)
	}
}

// TestMessageShortcutHasMessageBoardFlag tests that message shortcut has --message-board flag.
func TestMessageShortcutHasMessageBoardFlag(t *testing.T) {
	cmd := NewMessageCmd()

	flag := cmd.Flags().Lookup("message-board")
	if flag == nil {
		t.Fatal("expected --message-board flag to exist")
	}

	if flag.Usage != "Message board ID (required if project has multiple)" {
		t.Errorf("unexpected flag usage: %q", flag.Usage)
	}
}

// TestMessagesSubcommands tests that all expected subcommands exist.
func TestMessagesSubcommands(t *testing.T) {
	cmd := NewMessagesCmd()

	expected := []string{"list", "show", "create", "update", "pin", "unpin"}
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

// TestMessagesAliases tests that messages has the expected aliases.
func TestMessagesAliases(t *testing.T) {
	cmd := NewMessagesCmd()

	if len(cmd.Aliases) != 1 || cmd.Aliases[0] != "msgs" {
		t.Errorf("expected alias 'msgs', got %v", cmd.Aliases)
	}
}
