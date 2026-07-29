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
	sdkCfg := &basecamp.Config{BaseURL: "https://3.basecampapi.com"}
	sdkClient := basecamp.NewClient(sdkCfg, &messagesTestTokenProvider{},
		basecamp.WithTransport(messagesNoNetworkTransport{}),
		basecamp.WithMaxRetries(1), // Disable retries for instant failure
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

// TestMessagesCreateRequiresProject tests that a project-only command still
// asks for a project when none is in scope. messages list no longer does —
// it lists account-wide instead.
func TestMessagesCreateRequiresProject(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	// No project in config

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "create", "Title", "Body")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "Project ID required", e.Message)
}

// TestMessagesCreateShowsHelpWithoutTitle tests that help is shown when title is missing.
func TestMessagesCreateShowsHelpWithoutTitle(t *testing.T) {
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

// TestMessagesUpdateRequiresID tests that messages update errors when no ID is given.
func TestMessagesUpdateRequiresID(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "update")
	assert.Error(t, err)
}

// TestMessagesUpdateShowsHelpWithoutContent tests that messages update requires --title or --body.
func TestMessagesUpdateShowsHelpWithoutContent(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "update", "456")
	assert.NoError(t, err)
}

// TestMessagesPublishRequiresID tests that messages publish requires an ID argument.
func TestMessagesPublishRequiresID(t *testing.T) {
	app, _ := setupMessagesTestApp(t)

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "publish")
	require.Error(t, err)

	assert.Equal(t, "accepts 1 arg(s), received 0", err.Error())
}

// TestMessagesPublishInvalidID tests that messages publish rejects non-numeric IDs.
func TestMessagesPublishInvalidID(t *testing.T) {
	app, _ := setupMessagesTestApp(t)

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "publish", "not-a-number")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "Invalid message ID", e.Message)
}

// TestMessagesPublishSendsActiveStatus verifies publish sends an update with status "active".
func TestMessagesPublishSendsActiveStatus(t *testing.T) {
	transport := &mockMessageUpdateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "publish", "789")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody, "expected request body to be captured")

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	assert.Equal(t, "active", body["status"])
}

// TestMessagesHasMessageBoardFlag tests that --message-board flag is available.
func TestMessagesHasMessageBoardFlag(t *testing.T) {
	cmd := NewMessagesCmd()

	flag := cmd.PersistentFlags().Lookup("message-board")
	require.NotNil(t, flag, "expected --message-board flag to exist")

	assert.Equal(t, "Message board ID (required if project has multiple)", flag.Usage)
}

// TestMessagesSubcommands tests that all expected subcommands exist.
func TestMessagesSubcommands(t *testing.T) {
	cmd := NewMessagesCmd()

	expected := []string{"list", "show", "create", "update", "publish", "pin", "unpin"}
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

// TestMessagesCreateSubscribeMutualExclusion tests that --subscribe and --no-subscribe are mutually exclusive.
func TestMessagesCreateSubscribeMutualExclusion(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "create", "Test", "--subscribe", "me", "--no-subscribe")
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

	err := executeMessagesCommand(cmd, app, "create", "Test", "--subscribe", "")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "at least one person")
}

// TestMessagesCreateHasVisibleToClientsFlag tests that messages create has the --visible-to-clients flag.
func TestMessagesCreateHasVisibleToClientsFlag(t *testing.T) {
	cmd := NewMessagesCmd()
	createCmd, _, err := cmd.Find([]string{"create"})
	require.NoError(t, err)

	flag := createCmd.Flags().Lookup("visible-to-clients")
	require.NotNil(t, flag, "expected --visible-to-clients flag on messages create")
}

// TestMessagesCreateDefaultOmitsVisibleToClients verifies that without the flag,
// visible_to_clients is omitted so the server applies its own default.
func TestMessagesCreateDefaultOmitsVisibleToClients(t *testing.T) {
	transport := &mockMessageCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "create", "Normal post")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))

	_, ok := body["visible_to_clients"]
	assert.False(t, ok, "expected visible_to_clients to be omitted when flag is not set")
}

// TestMessagesCreateVisibleToClientsTrue verifies --visible-to-clients sends true.
func TestMessagesCreateVisibleToClientsTrue(t *testing.T) {
	transport := &mockMessageCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "create", "Client post", "--visible-to-clients")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))

	assert.Equal(t, true, body["visible_to_clients"])
}

// TestMessagesCreateVisibleToClientsFalse verifies --visible-to-clients=false
// sends an explicit false rather than dropping the field.
func TestMessagesCreateVisibleToClientsFalse(t *testing.T) {
	transport := &mockMessageCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewMessagesCmd()

	err := executeMessagesCommand(cmd, app, "create", "Team post", "--visible-to-clients=false")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))

	val, ok := body["visible_to_clients"]
	require.True(t, ok, "expected visible_to_clients present for explicit --visible-to-clients=false")
	assert.Equal(t, false, val)
}

// mockMessageUpdateTransport handles PUT requests and captures the body.
type mockMessageUpdateTransport struct {
	capturedBody []byte
}

func (t *mockMessageUpdateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method == "PUT" {
		if req.Body != nil {
			body, _ := io.ReadAll(req.Body)
			t.capturedBody = body
			req.Body.Close()
		}
		mockResp := `{"id": 789, "subject": "Draft", "status": "active"}`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     header,
		}, nil
	}

	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Header:     header,
	}, nil
}

// TestMessagesListBreadcrumbs tests the messagesListBreadcrumbs helper.
func TestMessagesListBreadcrumbs(t *testing.T) {
	breadcrumbs := messagesListBreadcrumbs("456")

	require.Len(t, breadcrumbs, 3)
	assert.Equal(t, "archived", breadcrumbs[2].Action)
	assert.Contains(t, breadcrumbs[2].Cmd, "recordings messages --status archived --in 456")
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
			body = `{"id": 123, "dock": [{"name": "message_board", "id": 777, "enabled": true}]}`
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

	sdkCfg := &basecamp.Config{BaseURL: "https://3.basecampapi.com"}
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

	err := executeMessagesCommand(cmd, app, "create", "Bot log", "--no-subscribe")
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

	err := executeMessagesCommand(cmd, app, "create", "Normal post")
	require.NoError(t, err, "command should succeed with mock transport")
	require.NotEmpty(t, transport.capturedBody, "expected request body to be captured")

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	_, ok := body["subscriptions"]
	assert.False(t, ok, "expected subscriptions to be omitted when neither flag is set")
}

// mockMessageListTransport handles the resolution chain and returns a truncated
// messages list (fewer messages than TotalCount) to exercise the truncation notice path.
type mockMessageListTransport struct{}

func (mockMessageListTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if req.Method != "GET" {
		return nil, errors.New("unexpected method: " + req.Method)
	}

	var body string
	switch {
	case strings.Contains(req.URL.Path, "/projects.json"):
		body = `[{"id": 123, "name": "Test Project"}]`
	case strings.Contains(req.URL.Path, "/projects/"):
		body = `{"id": 123, "dock": [{"name": "message_board", "id": 777, "enabled": true}]}`
	case strings.Contains(req.URL.Path, "/messages.json"):
		// Return 2 messages but signal 50 total via header
		header.Set("X-Total-Count", "50")
		body = `[{"id": 1, "subject": "Msg 1"}, {"id": 2, "subject": "Msg 2"}]`
	default:
		body = `{}`
	}

	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}, nil
}

// TestMessagesListAgentModeTruncationSilent verifies that truncation notices
// do not leak to stderr in quiet/agent mode. Only diagnostic warnings
// (e.g. unresolved mentions) should appear on stderr.
func TestMessagesListAgentModeTruncationSilent(t *testing.T) {
	transport := mockMessageListTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	// Override output to FormatQuiet with separate stdout/stderr buffers
	var stdout, stderr bytes.Buffer
	app.Output = output.New(output.Options{
		Format:    output.FormatQuiet,
		Writer:    &stdout,
		ErrWriter: &stderr,
	})

	cmd := NewMessagesCmd()
	err := executeMessagesCommand(cmd, app, "list", "--in", "123")
	require.NoError(t, err)

	// stdout should contain data
	assert.NotEmpty(t, stdout.String())

	// stderr must be empty — truncation notice should NOT leak
	assert.Empty(t, stderr.String(),
		"truncation notices should not appear on stderr in quiet mode")
}

// --- account-wide listing ---

const messagesAccountWidePath = "/99999/messages.json"

// messagesFeedRoute serves the account-wide feed with n message recordings,
// titled by the given subjects (cycled) so ordering is assertable.
func messagesFeedRoute(subjects ...string) stubRoute {
	items := make([]string, 0, len(subjects))
	for i, subject := range subjects {
		items = append(items, fmt.Sprintf(
			`{"id":%d,"type":"Message","title":%q,"subject":%q,"bucket":{"id":%d,"name":"Project %d","type":"Project"}}`,
			i+1, subject, subject, 100+i, i))
	}
	return stubRoute{
		method: http.MethodGet,
		path:   messagesAccountWidePath,
		status: http.StatusOK,
		body:   "[" + strings.Join(items, ",") + "]",
	}
}

// messagesFeedRouteOfSize serves n identical message recordings.
func messagesFeedRouteOfSize(n int) stubRoute {
	subjects := make([]string, n)
	for i := range subjects {
		subjects[i] = fmt.Sprintf("Message %d", i)
	}
	return messagesFeedRoute(subjects...)
}

// runMessagesListJSON runs messages list and returns the decoded envelope data.
func runMessagesListJSON(t *testing.T, app *appctx.App, args ...string) []map[string]any {
	t.Helper()

	var buf bytes.Buffer
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: &buf})

	require.NoError(t, executeRecordingCommand(NewMessagesCmd(), app, append([]string{"list"}, args...)...))

	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	return envelope.Data
}

// messagesRequestsTo returns the requests the transport saw for the given path.
func messagesRequestsTo(transport *recordingTransport, path string) []recordedCall {
	var matched []recordedCall
	for _, call := range transport.recorded() {
		if call.Path == path {
			matched = append(matched, call)
		}
	}
	return matched
}

// requireMessagesUsageError asserts the command failed with a usage error whose
// message contains want.
func requireMessagesUsageError(t *testing.T, err error, want string) {
	t.Helper()

	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, want)
}

// TestMessagesListProjectScopedUnchanged verifies a configured project still
// reaches the project's message board, not the account-wide feed.
func TestMessagesListProjectScopedUnchanged(t *testing.T) {
	app, transport := setupRecordingTestApp(t,
		projectsRoute(),
		stubRoute{http.MethodGet, "/99999/projects/123.json", http.StatusOK,
			`{"id":123,"dock":[{"name":"message_board","id":777,"enabled":true}]}`},
		stubRoute{http.MethodGet, "/99999/message_boards/777/messages.json", http.StatusOK,
			`[{"id":1,"subject":"Board message"}]`},
	)
	app.Config.ProjectID = "123"

	require.NoError(t, executeRecordingCommand(NewMessagesCmd(), app, "list"))

	assert.Equal(t, "/99999/message_boards/777/messages.json", transport.last(t).Path)
	assert.Empty(t, messagesRequestsTo(transport, messagesAccountWidePath))
}

// TestMessagesListAccountWideWithoutAnyProject verifies that with no project
// anywhere the list goes account-wide instead of prompting for one (I7).
func TestMessagesListAccountWideWithoutAnyProject(t *testing.T) {
	app, transport := setupRecordingTestApp(t, messagesFeedRouteOfSize(messagesAccountWideLimit))

	data := runMessagesListJSON(t, app)

	require.Len(t, data, messagesAccountWideLimit)
	require.Len(t, messagesRequestsTo(transport, messagesAccountWidePath), 1)
	assert.Empty(t, messagesRequestsTo(transport, "/99999/projects.json"),
		"account-wide listing should not resolve a project")
}

// TestMessagesListAllProjectsOverridesConfiguredProject verifies --all-projects
// wins over ambient config rather than being ignored.
func TestMessagesListAllProjectsOverridesConfiguredProject(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), messagesFeedRouteOfSize(messagesAccountWideLimit))
	app.Config.ProjectID = "123"

	data := runMessagesListJSON(t, app, "--all-projects")

	require.Len(t, data, messagesAccountWideLimit)
	require.Len(t, messagesRequestsTo(transport, messagesAccountWidePath), 1)
	assert.Empty(t, messagesRequestsTo(transport, "/99999/projects/123.json"),
		"--all-projects should override the configured project")
}

// requireMessagesAllProjectsConflict runs messages list and asserts the
// explicit-project conflict was caught.
func requireMessagesAllProjectsConflict(t *testing.T, rootProject string, args ...string) {
	t.Helper()

	app, _ := setupRecordingTestApp(t, projectsRoute(), messagesFeedRoute("Kickoff"))
	app.Flags.Project = rootProject

	err := executeRecordingCommand(NewMessagesCmd(), app, args...)
	requireMessagesUsageError(t, err, "--all-projects conflicts with --project/--in")
}

// TestMessagesListAllProjectsConflictsWithExplicitProject verifies the conflict
// is caught however the explicit project arrives — by each alias, on either side
// of the subcommand, and from the root-level flag that never sets Changed.
func TestMessagesListAllProjectsConflictsWithExplicitProject(t *testing.T) {
	requireMessagesAllProjectsConflict(t, "", "list", "--all-projects", "--project", "123")
	requireMessagesAllProjectsConflict(t, "", "list", "--all-projects", "-p", "123")
	requireMessagesAllProjectsConflict(t, "", "list", "--all-projects", "--in", "123")
	requireMessagesAllProjectsConflict(t, "", "--in", "123", "list", "--all-projects")
	requireMessagesAllProjectsConflict(t, "123", "list", "--all-projects")
}

// TestMessagesListAccountWideRejectsMessageBoard verifies the scope-child flag
// is rejected rather than ignored, on both sides of the subcommand.
func TestMessagesListAccountWideRejectsMessageBoard(t *testing.T) {
	for _, args := range [][]string{
		{"list", "--message-board", "777"},
		{"--message-board", "777", "list"},
		{"list", "--all-projects", "--message-board", "777"},
	} {
		app, _ := setupRecordingTestApp(t, messagesFeedRoute("Kickoff"))

		err := executeRecordingCommand(NewMessagesCmd(), app, args...)
		requireMessagesUsageError(t, err, "--message-board applies to a single project")
	}
}

// TestMessagesListAccountWideRejectsBadPaging verifies the pagination inputs the
// feed cannot express are errors, not silent no-ops.
func TestMessagesListAccountWideRejectsBadPaging(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--page", "0"}, "--page must be a positive page number"},
		{[]string{"--page", "-1"}, "--page must be a positive page number"},
		{[]string{"--limit", "-5"}, "--limit must be a positive number of messages"},
		{[]string{"--reverse"}, "--reverse requires --sort"},
	}

	for _, tc := range cases {
		app, _ := setupRecordingTestApp(t, messagesFeedRoute("Kickoff"))

		err := executeRecordingCommand(NewMessagesCmd(), app, append([]string{"list"}, tc.args...)...)
		requireMessagesUsageError(t, err, tc.want)
	}
}

// TestMessagesListAccountWideAcceptsAnyPositivePage verifies the project path's
// "only --page 1" rule does not reach the account-wide branch (I1).
func TestMessagesListAccountWideAcceptsAnyPositivePage(t *testing.T) {
	app, transport := setupRecordingTestApp(t, messagesFeedRoute("Kickoff"))

	data := runMessagesListJSON(t, app, "--page", "7")

	require.Len(t, data, 1)
	calls := messagesRequestsTo(transport, messagesAccountWidePath)
	require.Len(t, calls, 1)
	assert.Contains(t, calls[0].Query, "page=7")
}

// TestMessagesListProjectScopedStillRejectsPageTwo verifies the project path
// keeps its own page rule.
func TestMessagesListProjectScopedStillRejectsPageTwo(t *testing.T) {
	app, _ := setupRecordingTestApp(t, projectsRoute())
	app.Config.ProjectID = "123"

	err := executeRecordingCommand(NewMessagesCmd(), app, "list", "--page", "2")
	requireMessagesUsageError(t, err, "only --page 1 is supported")
}

// TestMessagesListAccountWideAllFollowsEveryPage verifies --all maps to page 0,
// which the SDK follows across every page.
func TestMessagesListAccountWideAllFollowsEveryPage(t *testing.T) {
	app, transport := setupRecordingTestApp(t, messagesFeedRouteOfSize(3))

	data := runMessagesListJSON(t, app, "--all")

	require.Len(t, data, 3)
	calls := messagesRequestsTo(transport, messagesAccountWidePath)
	require.Len(t, calls, 1)
	assert.NotContains(t, calls[0].Query, "page=")
}

// TestMessagesListAccountWideDefaultCapsAt100 verifies the documented default
// cap survives the move account-wide, and that the cap is collected a page at a
// time rather than by crawling the account.
func TestMessagesListAccountWideDefaultCapsAt100(t *testing.T) {
	app, transport := setupRecordingTestApp(t, messagesFeedRouteOfSize(60))

	data := runMessagesListJSON(t, app)

	assert.Len(t, data, messagesAccountWideLimit)
	calls := messagesRequestsTo(transport, messagesAccountWidePath)
	require.Len(t, calls, 2, "expected the cap to be filled from two pages")
	assert.Contains(t, calls[0].Query, "page=1")
	assert.Contains(t, calls[1].Query, "page=2")
}

// TestMessagesListAccountWideLimitTruncates verifies --limit caps the feed.
func TestMessagesListAccountWideLimitTruncates(t *testing.T) {
	app, transport := setupRecordingTestApp(t, messagesFeedRouteOfSize(10))

	data := runMessagesListJSON(t, app, "--limit", "4")

	assert.Len(t, data, 4)
	assert.Len(t, messagesRequestsTo(transport, messagesAccountWidePath), 1)
}

// TestMessagesListAccountWideSortsBeforeTruncating verifies a sorted listing
// sees every page before the cap is applied — truncating first would sort only
// the surviving window.
func TestMessagesListAccountWideSortsBeforeTruncating(t *testing.T) {
	app, transport := setupRecordingTestApp(t, messagesFeedRoute("Charlie", "Alpha", "Bravo"))

	data := runMessagesListJSON(t, app, "--sort", "title", "--limit", "2")

	require.Len(t, data, 2)
	assert.Equal(t, "Alpha", data[0]["subject"])
	assert.Equal(t, "Bravo", data[1]["subject"])

	calls := messagesRequestsTo(transport, messagesAccountWidePath)
	require.Len(t, calls, 1)
	assert.NotContains(t, calls[0].Query, "page=",
		"a sorted listing must fetch every page before capping")
}

// TestMessagesListAccountWideReverseSorts verifies --reverse flips the order.
func TestMessagesListAccountWideReverseSorts(t *testing.T) {
	app, _ := setupRecordingTestApp(t, messagesFeedRoute("Charlie", "Alpha", "Bravo"))

	data := runMessagesListJSON(t, app, "--sort", "title", "--reverse")

	require.Len(t, data, 3)
	assert.Equal(t, "Charlie", data[0]["subject"])
	assert.Equal(t, "Alpha", data[2]["subject"])
}

// TestMessagesListAccountWideRejectsUnknownSortField verifies the sort field set
// is the same one the project path validates.
func TestMessagesListAccountWideRejectsUnknownSortField(t *testing.T) {
	app, _ := setupRecordingTestApp(t, messagesFeedRoute("Kickoff"))

	err := executeRecordingCommand(NewMessagesCmd(), app, "list", "--sort", "due")
	requireMessagesUsageError(t, err, "invalid sort field")
}

// TestMessagesListAccountWideStyledRendersFlatRows verifies []Recording needs no
// flattening: the styled renderer already gives it one row per message, the way
// recordings list does.
func TestMessagesListAccountWideStyledRendersFlatRows(t *testing.T) {
	app, _ := setupRecordingTestApp(t, messagesFeedRoute("Kickoff", "Retro"))

	var buf bytes.Buffer
	app.Output = output.New(output.Options{Format: output.FormatStyled, Writer: &buf})

	require.NoError(t, executeRecordingCommand(NewMessagesCmd(), app, "list", "--all"))

	rendered := buf.String()
	assert.Contains(t, rendered, "2 messages across all projects")
	assert.Contains(t, rendered, "Kickoff")
	assert.Contains(t, rendered, "Retro")
	assert.NotContains(t, rendered, "map[", "nested cells would mean the payload needs flattening")
}

// TestMessagesAccountWideTitlePrefersSubject verifies the message feed's own
// subject wins over the generic recording title.
func TestMessagesAccountWideTitlePrefersSubject(t *testing.T) {
	subject := "Kickoff"
	empty := ""

	assert.Equal(t, "Kickoff", messagesAccountWideTitle(basecamp.Recording{Title: "generic", Subject: &subject}))
	assert.Equal(t, "generic", messagesAccountWideTitle(basecamp.Recording{Title: "generic", Subject: &empty}))
	assert.Equal(t, "generic", messagesAccountWideTitle(basecamp.Recording{Title: "generic"}))
}

// TestMessagesListHasAllProjectsFlag verifies the flag is on the list command.
func TestMessagesListHasAllProjectsFlag(t *testing.T) {
	cmd := NewMessagesCmd()
	listCmd, _, err := cmd.Find([]string{"list"})
	require.NoError(t, err)

	require.NotNil(t, listCmd.Flags().Lookup("all-projects"))
}
