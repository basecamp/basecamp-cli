package commands

// Integration coverage for the "-" (stdin) tier-1 patterns: one command per
// resolver shape, driven through a real Execute with a mock transport.

import (
	"bytes"
	"encoding/json"
	"errors"
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

// Pattern 2: exact positional — messages create <title> [body].
func TestMessagesCreateBodyDashReadsStdin(t *testing.T) {
	transport := &mockMessageCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewMessagesCmd()
	cmd.SetIn(strings.NewReader("Body **from stdin**\n"))

	err := executeMessagesCommand(cmd, app, "create", "Title", "-")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	assert.Equal(t, "Title", body["subject"])
	content, _ := body["content"].(string)
	assert.Contains(t, content, "<strong>from stdin</strong>")
}

// Pattern 3: content flag — api post --data -.
func TestAPIPostDataDashReadsStdin(t *testing.T) {
	transport := &mockCommentWriteTransport{}
	app, _ := setupCommentsWriteTestApp(t, transport)

	cmd := NewAPICmd()
	cmd.SetIn(strings.NewReader(`{"content":"from stdin"}` + "\n"))

	err := executeCommand(cmd, app, "post", "/buckets/1/todos.json", "--data", "-")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBodies)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBodies[0], &body))
	assert.Equal(t, "from stdin", body["content"])
}

// Pattern 1: join-all positionals — todos create <content>.
func TestTodosCreateDashReadsStdin(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockTodoCreateTransport{}
	cfg := &config.Config{AccountID: "99999", ProjectID: "123", TodolistID: "456"}
	sdkClient := basecamp.NewClient(&basecamp.Config{BaseURL: "https://3.basecampapi.com"}, &todosTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	authMgr := auth.NewManager(cfg, nil)
	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, authMgr, cfg.AccountID),
		Output: output.New(output.Options{Format: output.FormatJSON, Writer: &bytes.Buffer{}}),
	}

	cmd := NewTodosCmd()
	cmd.SetIn(strings.NewReader("Call the vendor back\n"))

	err := executeTodosCommand(cmd, app, "create", "-")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	assert.Equal(t, "Call the vendor back", body["content"],
		"the trailing newline must be trimmed from a piped title")
}

// Boost content from stdin: the trailing newline is trimmed before the 16-rune
// limit is applied, so a printf'd emoji doesn't burn a rune.
func TestBoostCreateDashReadsStdin(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockBoostTransport{}
	app, _ := newBoostTestApp(transport)

	cmd := NewBoostsCmd()
	cmd.SetIn(strings.NewReader("🎉\n"))

	err := executeBoostCommand(cmd, app, "create", "456", "-")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	assert.Equal(t, "🎉", body["content"])
}

func TestBoostCreateDashOverLimitStillRejected(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	transport := &mockBoostTransport{}
	app, _ := newBoostTestApp(transport)

	cmd := NewBoostsCmd()
	cmd.SetIn(strings.NewReader("seventeen chars!!\n"))

	err := executeBoostCommand(cmd, app, "create", "456", "-")
	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Boost content too long")
}

// Pattern 3 on an update: todos update --description -.
func TestTodosUpdateDescriptionDashReadsStdin(t *testing.T) {
	transport := &mockCommentWriteTransport{}
	app, _ := setupCommentsWriteTestApp(t, transport)

	cmd := NewTodosCmd()
	cmd.SetIn(strings.NewReader("New **details**\n"))

	err := executeCommand(cmd, app, "update", "789", "--description", "-")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBodies)

	found := false
	for _, captured := range transport.capturedBodies {
		var body map[string]any
		if json.Unmarshal(captured, &body) == nil {
			if desc, _ := body["description"].(string); strings.Contains(desc, "<strong>details</strong>") {
				found = true
			}
		}
	}
	assert.True(t, found, "the piped description should reach the wire as HTML")
}

// countingTransport records whether any request escaped the command.
type countingTransport struct{ calls int }

func (t *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return nil, errors.New("network disabled in tests")
}

// trackingReader records whether stdin was ever read.
type trackingReader struct {
	r    *strings.Reader
	read bool
}

func (t *trackingReader) Read(p []byte) (int, error) {
	t.read = true
	return t.r.Read(p)
}

func setupTransportTestApp(t *testing.T, transport http.RoundTripper) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	cfg := &config.Config{AccountID: "99999", ProjectID: "123"}
	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{BaseURL: "https://3.basecampapi.com"}, &testTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	return &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, authMgr, cfg.AccountID),
		Output: output.New(output.Options{Format: output.FormatJSON, Writer: &bytes.Buffer{}}),
	}
}

// The <title> [body] creates bound their arity, so a stray trailing token is a
// usage error at Args-validation time — before "-" drains stdin and before any
// request is built. Without the bound the extra token was silently dropped
// *after* stdin had already been consumed.
func TestExactPositionalCreatesRejectExtraArgsBeforeConsumingStdin(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  func() *cobra.Command
		path []string
	}{
		{"messages", NewMessagesCmd, []string{"create"}},
		{"cards", NewCardsCmd, []string{"create"}},
		{"docs", NewDocsCmd, []string{"documents", "create"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &countingTransport{}
			app := setupTransportTestApp(t, transport)

			stdin := &trackingReader{r: strings.NewReader("body from stdin")}
			cmd := tc.cmd()
			InstallDashGuard(cmd)
			cmd.SetIn(stdin)

			args := append(append([]string{}, tc.path...), "Title", "-", "unexpected")
			err := executeCommand(cmd, app, args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "accepts at most 2 arg")
			assert.False(t, stdin.read, "stdin must not be consumed before arity validation")
			assert.Zero(t, transport.calls, "no request may be issued")
		})
	}
}

// A flag-borne "-" with nothing piped must suggest an escape that parses:
// "api post ... --data -", never a bare positional "-" (api post takes one).
func TestAPIPostDataDashOnTTYHintPreservesTheFlag(t *testing.T) {
	transport := &countingTransport{}
	app := setupTransportTestApp(t, transport)

	cmd := NewAPICmd()
	InstallDashGuard(cmd)
	devNullStdin(t, cmd)

	err := executeCommand(cmd, app, "post", "/buckets/1/todos.json", "--data", "-")
	outErr := requireUsageErr(t, err)
	assert.Contains(t, outErr.Hint, "api post ... --data -")
	assert.Zero(t, transport.calls)
}

// setupNoAccountApp builds an app with no account configured, so any command
// that reaches account resolution fails with "--account is required".
func setupNoAccountApp(t *testing.T, transport http.RoundTripper) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	cfg := &config.Config{}
	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{BaseURL: "https://3.basecampapi.com"}, &testTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	return &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, authMgr, cfg.AccountID),
		Output: output.New(output.Options{Format: output.FormatJSON, Writer: &bytes.Buffer{}}),
	}
}

// Every "-" must be diagnosed before account, project or network work, so the
// caller gets the stdin error the feature promises instead of "--account is
// required" — or, worse, a resolution round-trip for an invocation that was
// never going to run. Driven on a TTY stdin because that error is produced by
// the resolver itself, which pins where in the sequence it ran.
func TestStdinResolvesBeforeAccountAndProject(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  func() *cobra.Command
		args []string
	}{
		{"cards update --body", NewCardsCmd, []string{"update", "1", "--body", "-"}},
		{"docs create [content]", NewDocsCmd, []string{"documents", "create", "Title", "-"}},
		{"gauges create --description", NewGaugesCmd, []string{"create", "--position", "50", "--description", "-"}},
		{"gauges update --description", NewGaugesCmd, []string{"update", "1", "--description", "-"}},
		{"schedule create --description", NewScheduleCmd, []string{
			"create", "Title", "--starts-at", "2026-01-01T10:00:00Z", "--ends-at", "2026-01-01T11:00:00Z", "--description", "-",
		}},
		{"templates update --description", NewTemplatesCmd, []string{"update", "1", "--description", "-"}},
		{"templates construct --description", NewTemplatesCmd, []string{"construct", "1", "--name", "P", "--description", "-"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &countingTransport{}
			app := setupNoAccountApp(t, transport)

			cmd := tc.cmd()
			InstallDashGuard(cmd)
			devNullStdin(t, cmd)

			err := executeCommand(cmd, app, tc.args...)
			outErr := requireUsageErr(t, err)
			assert.Contains(t, outErr.Message, "nothing is piped",
				"expected the stdin error, got %q", outErr.Message)
			assert.NotContains(t, outErr.Message, "account")
			assert.Zero(t, transport.calls, "no request may be issued")
		})
	}
}

// Two explicit content sources used to resolve by silent precedence: the
// positional won and --content was dropped, so "--content -" left the pipe
// unread and posted the positional instead.
func TestChatRejectsPositionalAlongsideContentFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"post", []string{"post", "hello", "--content", "-"}},
		{"update", []string{"update", "123", "hello", "--content", "-"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &countingTransport{}
			app := setupTransportTestApp(t, transport)

			stdin := &trackingReader{r: strings.NewReader("from stdin")}
			cmd := NewChatCmd()
			InstallDashGuard(cmd)
			cmd.SetIn(stdin)

			err := executeCommand(cmd, app, tc.args...)
			outErr := requireUsageErr(t, err)
			assert.Contains(t, outErr.Message, "--content")
			assert.False(t, stdin.read, "the discarded source must not consume stdin")
			assert.Zero(t, transport.calls)
		})
	}
}

// A malformed target ID is knowable from the arguments alone, so it must be
// reported without first draining the pipe: reading blocks on the producer, and
// a blank pipe would answer "stdin is empty" instead of naming the bad ID.
func TestMalformedIDRejectedBeforeReadingStdin(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  func() *cobra.Command
		args []string
	}{
		{"cards update", NewCardsCmd, []string{"update", "nope", "--body", "-"}},
		{"cards column update", NewCardsCmd, []string{"column", "update", "nope", "--description", "-"}},
		{"gauges update", NewGaugesCmd, []string{"update", "nope", "--description", "-"}},
		{"templates update", NewTemplatesCmd, []string{"update", "nope", "--description", "-"}},
		{"templates construct", NewTemplatesCmd, []string{"construct", "nope", "--name", "P", "--description", "-"}},
		{"messages update", NewMessagesCmd, []string{"update", "nope", "--body", "-"}},
		{"todos update", NewTodosCmd, []string{"update", "nope", "--description", "-"}},
		{"todolists update", NewTodolistsCmd, []string{"update", "nope", "--description", "-"}},
		{"projects update", NewProjectsCmd, []string{"update", "nope", "--description", "-"}},
		{"comments update", NewCommentsCmd, []string{"update", "nope", "-"}},
		{"files update", NewFilesCmd, []string{"update", "nope", "--content", "-"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &countingTransport{}
			app := setupTransportTestApp(t, transport)

			stdin := &trackingReader{r: strings.NewReader("body from stdin")}
			cmd := tc.cmd()
			InstallDashGuard(cmd)
			cmd.SetIn(stdin)

			err := executeCommand(cmd, app, tc.args...)
			outErr := requireUsageErr(t, err)
			assert.Contains(t, strings.ToLower(outErr.Message), "invalid",
				"expected the malformed-ID error, got %q", outErr.Message)
			assert.False(t, stdin.read, "stdin must not be drained before the ID is validated")
			assert.Zero(t, transport.calls, "no request may be issued")
		})
	}
}

// An invocation that is already doomed by its flags or arguments must be
// rejected before the pipe is drained. Otherwise the caller waits on a producer
// whose output is discarded, an unbounded one buffers into memory, and a blank
// one answers "stdin is empty" instead of naming the real problem.
func TestDeterministicFailuresRejectedBeforeReadingStdin(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  func() *cobra.Command
		args []string
		want string
	}{
		{"api foreign host", NewAPICmd,
			[]string{"post", "https://evil.example/x", "--data", "-"}, "configured host"},
		{"chat post bad room", NewChatCmd,
			[]string{"post", "-", "--room", "nope"}, "Invalid chat room ID"},
		{"chat update bad content-type", NewChatCmd,
			[]string{"update", "1", "-", "--content-type", "bogus"}, "unsupported --content-type"},
		{"boost bad id", NewBoostsCmd,
			[]string{"create", "nope", "-"}, "Invalid ID"},
		{"boost bad event", NewBoostsCmd,
			[]string{"create", "1", "-", "--event", "nope"}, "Invalid event ID"},
		{"checkins bad question", NewCheckinsCmd,
			[]string{"answer", "create", "nope", "-"}, "Invalid question ID"},
		{"checkins bad answer", NewCheckinsCmd,
			[]string{"answer", "update", "nope", "-"}, "Invalid answer ID"},
		{"files bad type", NewFilesCmd,
			[]string{"update", "1", "--type", "nonsense", "--content", "-"}, "Invalid type"},
		{"todos sweep without a filter", NewTodosCmd,
			[]string{"sweep", "--comment", "-"}, "requires a filter"},
		{"todos loose with a list", NewTodosCmd,
			[]string{"create", "-", "--loose", "--list", "123"}, "cannot be combined with --list"},
		{"gauges custom notify", NewGaugesCmd,
			[]string{"create", "--position", "50", "--notify", "custom", "--description", "-"}, "--subscriptions required"},
		{"docs subscribe conflict", NewDocsCmd,
			[]string{"documents", "create", "Title", "-", "--subscribe", "me", "--no-subscribe"}, "mutually exclusive"},
		{"messages subscribe conflict", NewMessagesCmd,
			[]string{"create", "Title", "-", "--subscribe", "me", "--no-subscribe"}, "mutually exclusive"},
		{"schedule bad entry id", NewScheduleCmd,
			[]string{"update", "nope", "--description", "-"}, "Invalid schedule entry ID"},
		{"uploads unreadable file", NewUploadsCmd,
			[]string{"create", "/nope/missing.txt", "--description", "-"}, "missing.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &countingTransport{}
			app := setupTransportTestApp(t, transport)

			stdin := &trackingReader{r: strings.NewReader("body from stdin")}
			cmd := tc.cmd()
			InstallDashGuard(cmd)
			cmd.SetIn(stdin)

			err := executeCommand(cmd, app, tc.args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.False(t, stdin.read, "stdin must not be drained for an invocation that cannot succeed")
			assert.Zero(t, transport.calls, "no request may be issued")
		})
	}
}
