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
type messagesNoNetworkTransport struct{}

func (messagesNoNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network disabled in tests")
}

// messagesTestTokenProvider is a mock token provider for tests.
type messagesTestTokenProvider struct{}

func (t *messagesTestTokenProvider) AccessToken(_ context.Context) (string, error) {
	return "test-token", nil
}

// setupMessagesTestApp creates a minimal test app context for messages tests.
func setupMessagesTestApp(t *testing.T) (*appctx.App, *bytes.Buffer) {
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
	sdkClient := basecamp.NewClient(sdkCfg, &messagesTestTokenProvider{},
		basecamp.WithTransport(messagesNoNetworkTransport{}),
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
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "No project specified", e.Message)
}

// TestMessagesListRequiresProject tests that messages list requires --project.
func TestMessagesListRequiresProject(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	// No project in config

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "list")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "No project specified", e.Message)
}

// TestMessagesCreateRequiresSubject tests that messages create requires --subject.
func TestMessagesCreateRequiresSubject(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "create")
	require.Error(t, err)

	// Cobra validates required flags
	assert.Equal(t, `required flag(s) "subject" not set`, err.Error())
}

// TestMessagesShowRequiresID tests that messages show requires an ID argument.
func TestMessagesShowRequiresID(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "show")
	require.Error(t, err)

	// Cobra validates required args
	assert.Equal(t, "accepts 1 arg(s), received 0", err.Error())
}

// TestMessagesPinRequiresID tests that messages pin requires an ID argument.
func TestMessagesPinRequiresID(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "pin")
	require.Error(t, err)

	assert.Equal(t, "accepts 1 arg(s), received 0", err.Error())
}

// TestMessagesUnpinRequiresID tests that messages unpin requires an ID argument.
func TestMessagesUnpinRequiresID(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "unpin")
	require.Error(t, err)

	assert.Equal(t, "accepts 1 arg(s), received 0", err.Error())
}

// TestMessagesUpdateRequiresID tests that messages update requires an ID argument.
func TestMessagesUpdateRequiresID(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "update")
	require.Error(t, err)

	assert.Equal(t, "accepts 1 arg(s), received 0", err.Error())
}

// TestMessagesUpdateRequiresContent tests that messages update requires --subject or --content.
func TestMessagesUpdateRequiresContent(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "update", "456")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "at least one of --subject or --content is required", e.Message)
}

// TestMessageShortcutRequiresSubject tests that message command requires --subject.
func TestMessageShortcutRequiresSubject(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessageCmd()

	err := executeMessagesCommand(cmd, app)
	require.Error(t, err)

	// Cobra validates required flags
	assert.Equal(t, `required flag(s) "subject" not set`, err.Error())
}

// TestMessageShortcutRequiresProject tests that message command requires --project.
func TestMessageShortcutRequiresProject(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	// No project in config

	cmd := NewMessageCmd()

	// Need to set subject to bypass that validation
	err := executeMessagesCommand(cmd, app, "--subject", "Test")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "No project specified", e.Message)
}

// TestMessagesHasMessageBoardFlag tests that --message-board flag is available.
func TestMessagesHasMessageBoardFlag(t *testing.T) {
	cmd := NewMessagesCmd()

	flag := cmd.PersistentFlags().Lookup("message-board")
	require.NotNil(t, flag, "expected --message-board flag to exist")

	assert.Equal(t, "Message board ID (required if project has multiple)", flag.Usage)
}

// TestMessageShortcutHasMessageBoardFlag tests that message shortcut has --message-board flag.
func TestMessageShortcutHasMessageBoardFlag(t *testing.T) {
	cmd := NewMessageCmd()

	flag := cmd.Flags().Lookup("message-board")
	require.NotNil(t, flag, "expected --message-board flag to exist")

	assert.Equal(t, "Message board ID (required if project has multiple)", flag.Usage)
}

// TestMessagesSubcommands tests that all expected subcommands exist.
func TestMessagesSubcommands(t *testing.T) {
	cmd := NewMessagesCmd()

	expected := []string{"list", "show", "create", "update", "pin", "unpin"}
	for _, name := range expected {
		sub, _, err := cmd.Find([]string{name})
		require.NoError(t, err, "expected subcommand %q to exist", name)
		require.NotNil(t, sub, "expected subcommand %q to exist", name)
	}
}

// TestMessagesAliases tests that messages has the expected aliases.
func TestMessagesAliases(t *testing.T) {
	cmd := NewMessagesCmd()

	require.Len(t, cmd.Aliases, 1)
	assert.Equal(t, "msgs", cmd.Aliases[0])
}
