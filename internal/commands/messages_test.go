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
	t.Setenv("BASECAMP_NO_KEYRING", "1")

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

// TestMessagesShowsHelp tests that help is shown when called without subcommand.
func TestMessagesShowsHelp(t *testing.T) {
	app, _ := setupMessagesTestApp(t)

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app)
	assert.NoError(t, err)
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
	assert.Equal(t, "Project ID required", e.Message)
}

// TestMessagesCreateShowsHelpWithoutSubject tests that help is shown when --subject is missing.
func TestMessagesCreateShowsHelpWithoutSubject(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "create")
	assert.NoError(t, err)
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
func TestMessagesUpdateShowsHelpWithoutContent(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "update", "456")
	assert.NoError(t, err)
}

// TestMessageShortcutShowsHelpWithoutSubject tests that help is shown when --subject is missing.
func TestMessageShortcutShowsHelpWithoutSubject(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessageCmd()

	err := executeMessagesCommand(cmd, app)
	assert.NoError(t, err)
}

// TestMessageShortcutRequiresProject tests that message command requires --project.
func TestMessageShortcutRequiresProject(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	// No project in config

	cmd := NewMessageCmd()

	// Need to set title to bypass that validation
	err := executeMessagesCommand(cmd, app, "Test")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "Project ID required", e.Message)
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

// TestMessagesCreateHasSubscribeFlags tests that messages create has --subscribe and --no-subscribe flags.
func TestMessagesCreateHasSubscribeFlags(t *testing.T) {
	cmd := NewMessagesCmd()
	createCmd, _, err := cmd.Find([]string{"create"})
	require.NoError(t, err)

	flag := createCmd.Flags().Lookup("subscribe")
	require.NotNil(t, flag, "expected --subscribe flag on messages create")

	flag = createCmd.Flags().Lookup("no-subscribe")
	require.NotNil(t, flag, "expected --no-subscribe flag on messages create")
}

// TestMessageShortcutHasSubscribeFlags tests that message shortcut has --subscribe and --no-subscribe flags.
func TestMessageShortcutHasSubscribeFlags(t *testing.T) {
	cmd := NewMessageCmd()

	flag := cmd.Flags().Lookup("subscribe")
	require.NotNil(t, flag, "expected --subscribe flag on message")

	flag = cmd.Flags().Lookup("no-subscribe")
	require.NotNil(t, flag, "expected --no-subscribe flag on message")
}

// TestMessagesCreateSubscribeMutualExclusion tests that --subscribe and --no-subscribe are mutually exclusive.
func TestMessagesCreateSubscribeMutualExclusion(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "create", "--subject", "Test", "--subscribe", "me", "--no-subscribe")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "mutually exclusive")
}

// TestMessageShortcutSubscribeMutualExclusion tests mutual exclusion on the message shortcut.
func TestMessageShortcutSubscribeMutualExclusion(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessageCmd()

	err := executeMessagesCommand(cmd, app, "Test", "--subscribe", "me", "--no-subscribe")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "mutually exclusive")
}

// TestMessagesCreateSubscribeEmptyIsError tests that --subscribe "" is rejected.
func TestMessagesCreateSubscribeEmptyIsError(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "create", "--subject", "Test", "--subscribe", "")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "at least one person")
}

// TestMessageShortcutSubscribeEmptyIsError tests that --subscribe "" is rejected on the shortcut.
func TestMessageShortcutSubscribeEmptyIsError(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessageCmd()

	err := executeMessagesCommand(cmd, app, "Test", "--subscribe", "")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "at least one person")
}

// mockMessageCreateTransport handles resolver and dock API calls, and captures the POST body.
type mockMessageCreateTransport struct {
	capturedBody []byte
}

func (t *mockMessageCreateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "GET" {
		var body string
		if strings.Contains(req.URL.Path, "/projects.json") {
			body = `[{"id": 123, "name": "Test Project"}]`
		} else if strings.Contains(req.URL.Path, "/projects/") {
			// Return project with message_board in dock
			body = `{"id": 123, "dock": [{"name": "message_board", "id": 777}]}`
		} else {
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
			t.capturedBody = body
			req.Body.Close()
		}
		mockResp := `{"id": 999, "subject": "Test", "status": "active"}`
		return &http.Response{
			StatusCode: 201,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     header,
		}, nil
	}

	return nil, errors.New("unexpected request")
}

func setupMessagesMockApp(t *testing.T, transport http.RoundTripper) (*appctx.App, *bytes.Buffer) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &messagesTestTokenProvider{},
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
	return app, buf
}

// TestMessagesCreateNoSubscribeSendsEmptyList verifies --no-subscribe sends an empty subscriptions array.
func TestMessagesCreateNoSubscribeSendsEmptyList(t *testing.T) {
	transport := &mockMessageCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "create", "--subject", "Bot log", "--no-subscribe")
	require.NoError(t, err, "command should succeed with mock transport")
	require.NotEmpty(t, transport.capturedBody, "expected request body to be captured")

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	subs, ok := body["subscriptions"]
	require.True(t, ok, "expected subscriptions field in request body")

	subsList, ok := subs.([]any)
	require.True(t, ok, "expected subscriptions to be an array")
	assert.Empty(t, subsList, "expected empty subscriptions array for --no-subscribe")
}

// TestMessagesCreateDefaultOmitsSubscriptions verifies that without flags, subscriptions is omitted.
func TestMessagesCreateDefaultOmitsSubscriptions(t *testing.T) {
	transport := &mockMessageCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "create", "--subject", "Normal post")
	require.NoError(t, err, "command should succeed with mock transport")
	require.NotEmpty(t, transport.capturedBody, "expected request body to be captured")

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	_, ok := body["subscriptions"]
	assert.False(t, ok, "expected subscriptions to be omitted when neither flag is set")
}
