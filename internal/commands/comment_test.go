package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/names"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// TestCommentsGroupAcceptsInFlag tests the 'comments' group accepts --in.
func TestCommentsGroupAcceptsInFlag(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCommentsCmd()

	err := executeCommand(cmd, app, "list", "--in", "456", "789")

	// Should not be "unknown flag" or "unknown shorthand"
	require.NotNil(t, err)
	assert.NotContains(t, err.Error(), "unknown flag")
	assert.NotContains(t, err.Error(), "unknown shorthand")
}

func TestCommentsCreateReadsDashContentFromStdin(t *testing.T) {
	transport := &mockCommentWriteTransport{}
	app, _ := setupCommentsWriteTestApp(t, transport)

	cmd := newCommentsCreateCmd()
	cmd.SetIn(strings.NewReader("Hello from stdin\n\n**works**\n"))

	err := executeCommand(cmd, app, "789", "-")
	require.NoError(t, err)
	require.Len(t, transport.capturedBodies, 1)

	var body map[string]string
	require.NoError(t, json.Unmarshal(transport.capturedBodies[0], &body))
	assert.Contains(t, body["content"], "Hello from stdin")
	assert.Contains(t, body["content"], "<strong>works</strong>")
	assert.NotEqual(t, "<p>-</p>", body["content"])
}

func TestCommentsUpdateReadsDashContentFromStdin(t *testing.T) {
	transport := &mockCommentWriteTransport{}
	app, _ := setupCommentsWriteTestApp(t, transport)

	cmd := newCommentsUpdateCmd()
	cmd.SetIn(strings.NewReader("Updated from stdin\n"))

	err := executeCommand(cmd, app, "1234", "-")
	require.NoError(t, err)
	require.Len(t, transport.capturedBodies, 1)

	var body map[string]string
	require.NoError(t, json.Unmarshal(transport.capturedBodies[0], &body))
	assert.Equal(t, "<p>Updated from stdin</p>", body["content"])
}

func TestCommentsUpdateRejectsEmptyDashContent(t *testing.T) {
	transport := &mockCommentWriteTransport{}
	app, _ := setupCommentsWriteTestApp(t, transport)
	app.Flags.JSON = true

	cmd := newCommentsUpdateCmd()
	cmd.SetIn(strings.NewReader("  \n"))

	err := executeCommand(cmd, app, "1234", "-")
	require.Error(t, err)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeUsage, outErr.Code)
	assert.Equal(t, "stdin for <content> is empty", outErr.Message)
	assert.Empty(t, transport.capturedBodies)
}

// A pipe without "-" is no longer consumed implicitly: the error teaches the
// explicit placeholder instead, and nothing reaches the server.
func TestCommentsCreateBarePipeErrorsWithDashHint(t *testing.T) {
	transport := &mockCommentWriteTransport{}
	app, _ := setupCommentsWriteTestApp(t, transport)
	app.Flags.JSON = true

	cmd := NewCommentsCmd()
	cmd.SetIn(strings.NewReader("hello from stdin"))
	err := executeCommand(cmd, app, "create", "123")
	require.Error(t, err)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeUsage, outErr.Code)
	assert.Contains(t, outErr.Hint, `"-"`)
	assert.Empty(t, transport.capturedBodies)
}

func TestCommentsCreatePrefersPositionalContentOverStdin(t *testing.T) {
	transport := &mockCommentWriteTransport{}
	app, _ := setupCommentsWriteTestApp(t, transport)

	cmd := NewCommentsCmd()
	cmd.SetIn(strings.NewReader("ignored stdin"))
	err := executeCommand(cmd, app, "create", "123", "hello from args")
	require.NoError(t, err)
	require.Len(t, transport.capturedBodies, 1)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBodies[0], &body))
	assert.Equal(t, "<p>hello from args</p>", body["content"])
}

func TestCommentsCreateMissingContentReturnsUsageBeforeAccountResolution(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.AccountID = ""
	app.Flags.JSON = true

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Skip("dev null not available")
	}

	t.Cleanup(func() {
		devNull.Close()
	})

	cmd := NewCommentsCmd()
	cmd.SetIn(devNull)
	err = executeCommand(cmd, app, "create", "123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "<content> required")
	assert.NotContains(t, err.Error(), "account")
}

func TestReadStdinContentUnreadableStdinIsUsageError(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, r.Close())
	require.NoError(t, w.Close())

	cmd := newCommentsCreateCmd()
	cmd.SetIn(r)
	content, err := readStdinContent(cmd, "<content>")
	require.Error(t, err)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeUsage, outErr.Code)
	assert.Empty(t, content)
}

func setupCommentsWriteTestApp(t *testing.T, transport http.RoundTripper) (*appctx.App, *bytes.Buffer) {
	t.Helper()

	app, buf := setupTestApp(t)
	sdkClient := basecamp.NewClient(&basecamp.Config{BaseURL: "https://3.basecampapi.com"}, &testTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	app.SDK = sdkClient
	app.Names = names.NewResolver(sdkClient, app.Auth, app.Config.AccountID)
	return app, buf
}

type mockCommentWriteTransport struct {
	capturedBodies [][]byte
}

func (t *mockCommentWriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		t.capturedBodies = append(t.capturedBodies, body)
	}

	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	status := http.StatusOK
	if req.Method == http.MethodPost {
		status = http.StatusCreated
	}

	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(`{"id":1234,"content":"ok","status":"active"}`)),
		Header:     header,
	}, nil
}

// --- Iteration 2: comments show reply-atom enrichment -----------------------

// runCommentsShow executes `comments show` against a tracking transport and
// returns stdout/stderr plus the run error, mirroring runThreadCmd.
func runCommentsShow(t *testing.T, transport *showTrackingTransport, format output.Format, args ...string) (string, string, error) {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	app := showTestAppWithOutput(t, transport, format, stdout, stderr)
	app.Flags.Hints = true
	cmd := NewCommentsCmd()
	full := append([]string{"show"}, args...)
	cmd.SetArgs(full)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func showCommentResponder(commentJSON string) func(path string) (int, string, http.Header) {
	return func(_ string) (int, string, http.Header) {
		return 200, commentJSON, nil
	}
}

func TestCommentsShowEnrichesReplyAtoms(t *testing.T) {
	comment := map[string]any{
		"id": 456, "type": "Comment", "content": "<p>hi</p>",
		"created_at": "2026-07-20T10:05:00Z",
		"parent":     map[string]any{"id": 123, "type": "Todo", "url": "https://x/y"},
		"creator":    map[string]any{"id": 7, "name": "Jane Doe", "attachable_sgid": "SGID123"},
	}
	cb, _ := json.Marshal(comment)
	transport := &showTrackingTransport{responderWithHeaders: showCommentResponder(string(cb))}

	stdout, _, err := runCommentsShow(t, transport, output.FormatJSON, "456")
	require.NoError(t, err)

	var env threadEnvelope
	require.NoError(t, json.Unmarshal([]byte(stdout), &env))

	// reply_target.recording_id == parent.id — the smoke invariant.
	replyTarget := env.Data["reply_target"].(map[string]any)
	parent := env.Data["parent"].(map[string]any)
	assert.EqualValues(t, parent["id"], replyTarget["recording_id"])
	assert.EqualValues(t, 123, replyTarget["recording_id"])

	// Paste-ready mention rides in machine .data.
	mention := env.Data["mention"].(map[string]any)
	assert.Equal(t, "[@Jane Doe](mention:SGID123)", mention["syntax"])

	// Exactly one request — no parent or list fetch.
	assert.Len(t, transport.getRequests(), 1)

	// The reply breadcrumb is the human-output affordance: update first, reply
	// last, targeting the parent recording.
	require.Len(t, env.Breadcrumbs, 2)
	assert.Equal(t, "update", env.Breadcrumbs[0].Action)
	assert.Equal(t, "reply", env.Breadcrumbs[1].Action)
	assert.Equal(t, "basecamp comments create 123 <text>", env.Breadcrumbs[1].Cmd)
}

func TestCommentsShowNilParentOmitsReplyTarget(t *testing.T) {
	comment := map[string]any{
		"id": 456, "type": "Comment", "content": "<p>hi</p>",
		"created_at": "2026-07-20T10:05:00Z",
		"creator":    map[string]any{"id": 7, "name": "Jane Doe"},
	}
	cb, _ := json.Marshal(comment)
	transport := &showTrackingTransport{responderWithHeaders: showCommentResponder(string(cb))}

	stdout, _, err := runCommentsShow(t, transport, output.FormatJSON, "456")
	require.NoError(t, err)

	var env threadEnvelope
	require.NoError(t, json.Unmarshal([]byte(stdout), &env))

	_, hasReplyTarget := env.Data["reply_target"]
	assert.False(t, hasReplyTarget, "no reply_target without a parent")
	for _, b := range env.Breadcrumbs {
		assert.NotEqual(t, "reply", b.Action, "no reply breadcrumb without a parent")
	}
	// Mention still resolves (person lookup) — enrichment is graceful, not fatal.
	mention := env.Data["mention"].(map[string]any)
	assert.Equal(t, "person_lookup", mention["resolution"])
}

func TestCommentsShowSharedResolverSafety(t *testing.T) {
	comment := `{"id":456,"type":"Comment","content":"<p>hi</p>","parent":{"id":123,"type":"Todo"},"creator":{"id":7,"name":"Jane"}}`

	t.Run("plain recording URL rejected, zero requests", func(t *testing.T) {
		transport := &showTrackingTransport{responderWithHeaders: showCommentResponder(comment)}
		_, _, err := runCommentsShow(t, transport, output.FormatJSON, "https://3.basecamp.com/99999/buckets/1/todos/123")
		require.Error(t, err)
		var outErr *output.Error
		require.True(t, errors.As(err, &outErr))
		assert.Equal(t, output.CodeUsage, outErr.Code)
		assert.Empty(t, transport.getRequests())
	})

	t.Run("account mismatch rejected, zero requests", func(t *testing.T) {
		transport := &showTrackingTransport{responderWithHeaders: showCommentResponder(comment)}
		url := "https://3.basecamp.com/88888/buckets/1/todos/123#__recording_456"
		_, _, err := runCommentsShow(t, transport, output.FormatJSON, url)
		require.Error(t, err)
		var outErr *output.Error
		require.True(t, errors.As(err, &outErr))
		assert.Equal(t, output.CodeUsage, outErr.Code)
		assert.Contains(t, outErr.Message, "does not match")
		assert.Empty(t, transport.getRequests())
	})

	t.Run("non-positive id rejected, zero requests", func(t *testing.T) {
		transport := &showTrackingTransport{responderWithHeaders: showCommentResponder(comment)}
		_, _, err := runCommentsShow(t, transport, output.FormatJSON, "0")
		require.Error(t, err)
		var outErr *output.Error
		require.True(t, errors.As(err, &outErr))
		assert.Equal(t, output.CodeUsage, outErr.Code)
		assert.Empty(t, transport.getRequests())
	})

	t.Run("three valid forms resolve the same comment", func(t *testing.T) {
		forms := []string{
			"456",
			"https://3.basecamp.com/99999/buckets/1/todos/123#__recording_456",
			"https://3.basecamp.com/99999/buckets/1/comments/456",
		}
		for _, form := range forms {
			transport := &showTrackingTransport{responderWithHeaders: showCommentResponder(comment)}
			stdout, _, err := runCommentsShow(t, transport, output.FormatJSON, form)
			require.NoError(t, err, form)
			var env threadEnvelope
			require.NoError(t, json.Unmarshal([]byte(stdout), &env))
			assert.EqualValues(t, 456, env.Data["id"])
			assert.Len(t, transport.getRequests(), 1, form)
		}
	})
}

// runCommentsShowAccount runs `comments show` with an explicit configured account
// (use "" to simulate an unconfigured run).
func runCommentsShowAccount(t *testing.T, transport *showTrackingTransport, accountID string, args ...string) (string, error) {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	app := showTestAppWithOutput(t, transport, output.FormatJSON, stdout, stderr)
	app.Flags.Hints = true
	app.Config.AccountID = accountID
	app.Names.SetAccountID(accountID)
	cmd := NewCommentsCmd()
	full := append([]string{"show"}, args...)
	cmd.SetArgs(full)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	return stdout.String(), err
}

func TestCommentsShowRejectsUntrustedHostNoAPICall(t *testing.T) {
	comment := `{"id":456,"type":"Comment","parent":{"id":123,"type":"Todo"},"creator":{"id":7,"name":"Jane"}}`
	cases := map[string]string{
		"direct comment URL": "https://evil.example/99999/buckets/1/comments/456",
		"fragment URL":       "https://evil.example/99999/buckets/1/todos/123#__recording_456",
	}
	for name, url := range cases {
		t.Run(name, func(t *testing.T) {
			transport := &showTrackingTransport{responderWithHeaders: showCommentResponder(comment)}
			_, _, err := runCommentsShow(t, transport, output.FormatJSON, url)
			require.Error(t, err)
			var outErr *output.Error
			require.True(t, errors.As(err, &outErr))
			assert.Equal(t, output.CodeUsage, outErr.Code)
			assert.Contains(t, outErr.Message, "untrusted host")
			assert.Empty(t, transport.getRequests())
		})
	}
}

func TestCommentsShowInvalidIDBeforeAccountResolution(t *testing.T) {
	comment := `{"id":456,"type":"Comment","parent":{"id":123,"type":"Todo"},"creator":{"id":7,"name":"Jane"}}`
	transport := &showTrackingTransport{responderWithHeaders: showCommentResponder(comment)}
	stdout, err := runCommentsShowAccount(t, transport, "", "0")
	require.Error(t, err)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeUsage, outErr.Code)
	assert.Equal(t, "Invalid comment ID", outErr.Message)
	assert.NotContains(t, stdout, "account is required")
	assert.Empty(t, transport.getRequests())
}

func TestCommentsShowAdoptsURLAccountWhenUnconfigured(t *testing.T) {
	comment := `{"id":456,"type":"Comment","parent":{"id":123,"type":"Todo"},"creator":{"id":7,"name":"Jane"}}`
	transport := &showTrackingTransport{responderWithHeaders: showCommentResponder(comment)}
	url := "https://3.basecamp.com/77777/buckets/1/comments/456"
	stdout, err := runCommentsShowAccount(t, transport, "", url)
	require.NoError(t, err)

	reqs := transport.getRequests()
	require.Len(t, reqs, 1, "one Get, no extra calls")
	assert.Contains(t, reqs[0], "/77777/", "the fetch must target the URL's account")

	// The reply contract stays runnable in a fresh process: reply_target names
	// the adopted account, and the reply breadcrumb spells out --account.
	var env threadEnvelope
	require.NoError(t, json.Unmarshal([]byte(stdout), &env))
	assert.Equal(t, "77777", env.Data["reply_target"].(map[string]any)["account_id"])
	var replyCmd string
	for _, b := range env.Breadcrumbs {
		if b.Action == "reply" {
			replyCmd = b.Cmd
		}
	}
	assert.Contains(t, replyCmd, "--account 77777")
}

func TestCommentsShowRejectsZeroComment(t *testing.T) {
	// An all-zero comment ({}) must fail as empty_response, not a bogus success
	// masked by the non-empty enrichment mention map.
	transport := &showTrackingTransport{responderWithHeaders: showCommentResponder(`{}`)}
	_, _, err := runCommentsShow(t, transport, output.FormatJSON, "456")
	require.Error(t, err)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, "empty_response", outErr.Code)
}

// --- Iteration 2: account provenance in reply breadcrumbs -------------------

func TestHasPersistentAccount(t *testing.T) {
	mk := func(id, src string) *config.Config {
		c := &config.Config{AccountID: id, Sources: map[string]string{}}
		if src != "" {
			c.Sources["account_id"] = src
		}
		return c
	}
	// No account → not persistent.
	assert.False(t, hasPersistentAccount(mk("", "")))
	// Process-local sources → not persistent (follow-ups must echo --account).
	assert.False(t, hasPersistentAccount(mk("99999", string(config.SourceFlag))))
	assert.False(t, hasPersistentAccount(mk("99999", string(config.SourcePrompt))))
	// On-disk / exported-env sources → persistent (follow-ups inherit it).
	assert.True(t, hasPersistentAccount(mk("99999", string(config.SourceGlobal))))
	assert.True(t, hasPersistentAccount(mk("99999", string(config.SourceLocal))))
	assert.True(t, hasPersistentAccount(mk("99999", string(config.SourceEnv))))
}

func TestReplyAccountArg(t *testing.T) {
	assert.Equal(t, "", replyAccountArg(true, "99999"))                  // persistent → no echo
	assert.Equal(t, "", replyAccountArg(false, ""))                      // nothing to echo
	assert.Equal(t, " --account 99999", replyAccountArg(false, "99999")) // process-local → echo
}

func TestCommentsShowFlagAccountEchoedInBreadcrumb(t *testing.T) {
	// An account supplied solely via the process-local --account flag is not
	// persistent, so the reply breadcrumb must spell out --account.
	comment := `{"id":456,"type":"Comment","parent":{"id":123,"type":"Todo"},"creator":{"id":7,"name":"Jane"}}`
	transport := &showTrackingTransport{responderWithHeaders: showCommentResponder(comment)}
	stdout := &bytes.Buffer{}
	app := showTestAppWithOutput(t, transport, output.FormatJSON, stdout, &bytes.Buffer{})
	app.Flags.Hints = true
	app.Config.Sources = map[string]string{"account_id": string(config.SourceFlag)}

	cmd := NewCommentsCmd()
	cmd.SetArgs([]string{"show", "456"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())

	var env threadEnvelope
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &env))
	var replyCmd string
	for _, b := range env.Breadcrumbs {
		if b.Action == "reply" {
			replyCmd = b.Cmd
		}
	}
	assert.Contains(t, replyCmd, "--account 99999")
}

// --- Account-wide comment listing -------------------------------------------

// accountWideCommentsRoute serves n comments on the account-wide feed. The stub
// matches on path only, so every page of a walk sees the same body — enough to
// prove the walk and the truncation without pretending to be a real cursor.
func accountWideCommentsRoute(n int) stubRoute {
	items := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		items = append(items, fmt.Sprintf(
			`{"id":%d,"type":"Comment","title":"Re: thing %d","content":"<p>c%d</p>",`+
				`"bucket":{"id":7,"name":"Test Project","type":"Project"}}`, 1000+i, i, i))
	}
	return stubRoute{
		method: http.MethodGet,
		path:   "/99999/comments.json",
		status: http.StatusOK,
		body:   "[" + strings.Join(items, ",") + "]",
	}
}

func runCommentsListCmd(t *testing.T, app *appctx.App, args ...string) error {
	t.Helper()
	return executeRecordingCommand(NewCommentsCmd(), app, append([]string{"list"}, args...)...)
}

type commentsListEnvelope struct {
	Data    []map[string]any `json:"data"`
	Summary string           `json:"summary"`
	Notice  string           `json:"notice"`
}

// runCommentsListJSON runs `comments list` and returns the JSON envelope.
func runCommentsListJSON(t *testing.T, app *appctx.App, args ...string) commentsListEnvelope {
	t.Helper()
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})
	require.NoError(t, runCommentsListCmd(t, app, args...))

	var env commentsListEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	return env
}

// assertCommentsListUsage asserts the invocation is rejected as an actionable
// usage error before any request goes out.
func assertCommentsListUsage(t *testing.T, app *appctx.App, transport *recordingTransport, wantMsg string, args ...string) {
	t.Helper()
	outErr := assertCommentsListUsageCode(t, app, transport, wantMsg, args...)
	assert.NotEmpty(t, outErr.Hint, "a rejected flag must say what to do instead")
}

// assertCommentsListUsageCode is assertCommentsListUsage without the hint
// requirement, for the terse pre-existing usage errors.
func assertCommentsListUsageCode(t *testing.T, app *appctx.App, transport *recordingTransport, wantMsg string, args ...string) *output.Error {
	t.Helper()
	err := runCommentsListCmd(t, app, args...)
	require.Error(t, err)

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeUsage, outErr.Code)
	assert.Contains(t, outErr.Message, wantMsg)
	assert.Empty(t, transport.recorded(), "a rejected invocation must not reach the API")
	return outErr
}

// assertCommentsNoProjectLookup proves ensureProject never ran: it resolves through the
// project list, so a single /projects.json fetch would give it away.
func assertCommentsNoProjectLookup(t *testing.T, transport *recordingTransport) {
	t.Helper()
	for _, req := range transport.recorded() {
		assert.NotContains(t, req.Path, "/projects.json", "account-wide listing must not resolve a project")
	}
}

// TestCommentsListItemScopedUnchanged pins the item-scoped path: an ID still
// reaches the per-recording endpoint, and it still permits only page 1.
func TestCommentsListItemScopedUnchanged(t *testing.T) {
	itemRoute := stubRoute{
		method: http.MethodGet,
		path:   "/99999/recordings/789/comments.json",
		status: http.StatusOK,
		body:   `[{"id":1,"content":"<p>hi</p>"}]`,
	}

	t.Run("bare ID", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, itemRoute)
		require.NoError(t, runCommentsListCmd(t, app, "789"))
		assert.Equal(t, "/99999/recordings/789/comments.json", transport.last(t).Path)
	})

	t.Run("a configured project does not divert it", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, itemRoute, projectsRoute())
		app.Config.ProjectID = "123"
		require.NoError(t, runCommentsListCmd(t, app, "789"))
		assert.Equal(t, "/99999/recordings/789/comments.json", transport.last(t).Path)
	})

	t.Run("still only page 1", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, itemRoute)
		assertCommentsListUsageCode(t, app, transport, "only --page 1 is supported", "789", "--page", "2")
	})
}

// TestCommentsListReachesAccountWide covers the three dispatch rows that end
// account-wide: nothing in scope, --all-projects over a configured project, and
// a configured project alone — which cannot scope a per-item listing and is
// therefore ignored rather than turned into an error.
func TestCommentsListReachesAccountWide(t *testing.T) {
	t.Run("no project anywhere", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, accountWideCommentsRoute(120))
		require.NoError(t, runCommentsListCmd(t, app))
		assert.Equal(t, "/99999/comments.json", transport.last(t).Path)
		assertCommentsNoProjectLookup(t, transport)
	})

	t.Run("--all-projects overrides a configured project", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, accountWideCommentsRoute(120), projectsRoute())
		app.Config.ProjectID = "123"
		require.NoError(t, runCommentsListCmd(t, app, "--all-projects"))
		assert.Equal(t, "/99999/comments.json", transport.last(t).Path)
		assertCommentsNoProjectLookup(t, transport)
	})

	t.Run("a configured project alone is ignored", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, accountWideCommentsRoute(120), projectsRoute())
		app.Config.ProjectID = "123"
		app.Config.TodolistID = "456"
		require.NoError(t, runCommentsListCmd(t, app))
		assert.Equal(t, "/99999/comments.json", transport.last(t).Path)
		assertCommentsNoProjectLookup(t, transport)
	})
}

// TestCommentsListRejectsUnhonorableScopeFlags covers every way a scope that
// cannot narrow an account-wide comment feed can arrive: after the group noun,
// by the --in alias, at the root, and as a todolist.
func TestCommentsListRejectsUnhonorableScopeFlags(t *testing.T) {
	const cannotScope = "cannot scope a comment listing"

	t.Run("--project without an ID asks for an ID", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, projectsRoute())
		assertCommentsListUsage(t, app, transport, cannotScope, "--project", "123")
	})

	t.Run("-p without an ID asks for an ID", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, projectsRoute())
		assertCommentsListUsage(t, app, transport, cannotScope, "-p", "123")
	})

	t.Run("--in alias without an ID asks for an ID", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, projectsRoute())
		assertCommentsListUsage(t, app, transport, cannotScope, "--in", "123")
	})

	t.Run("root-level --project without an ID asks for an ID", func(t *testing.T) {
		// The root form never sets cmd.Flags().Changed — it lands here instead.
		app, transport := setupRecordingTestApp(t, projectsRoute())
		app.Flags.Project = "123"
		assertCommentsListUsage(t, app, transport, cannotScope)
	})

	t.Run("--all-projects conflicts with an explicit project", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, projectsRoute())
		assertCommentsListUsage(t, app, transport,
			"--all-projects cannot be combined with --project", "--all-projects", "--project", "123")
	})

	t.Run("--all-projects conflicts with the root-level project", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, projectsRoute())
		app.Flags.Project = "123"
		assertCommentsListUsage(t, app, transport,
			"--all-projects cannot be combined with --project", "--all-projects")
	})

	t.Run("--all-projects conflicts with an item ID", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t)
		assertCommentsListUsage(t, app, transport,
			"--all-projects cannot be combined with an item ID", "--all-projects", "789")
	})

	t.Run("root-level --todolist is rejected by name", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t)
		app.Flags.Todolist = "456"
		assertCommentsListUsage(t, app, transport, "--todolist cannot scope an account-wide comment listing")
	})

	t.Run("root-level --todolist is rejected under --all-projects too", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t)
		app.Flags.Todolist = "456"
		assertCommentsListUsage(t, app, transport,
			"--todolist cannot scope an account-wide comment listing", "--all-projects")
	})
}

// TestCommentsListAccountWidePagination pins the row of the pagination contract
// this command owns: default cap 100, --all follows every page, and any
// positive --page is accepted where the item feed permits only page 1.
func TestCommentsListAccountWidePagination(t *testing.T) {
	t.Run("default caps at 100", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, accountWideCommentsRoute(120))
		env := runCommentsListJSON(t, app)
		assert.Len(t, env.Data, 100)
		assert.Equal(t, "100 comments across all projects", env.Summary)

		reqs := transport.recorded()
		require.Len(t, reqs, 1, "one full page already covers the cap")
		assert.Equal(t, "page=1", reqs[0].Query)
	})

	t.Run("--limit walks positive pages until it is met", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, accountWideCommentsRoute(3))
		env := runCommentsListJSON(t, app, "--limit", "5")
		assert.Len(t, env.Data, 5)
		assert.Equal(t, "5 comments across all projects", env.Summary)

		reqs := transport.recorded()
		require.Len(t, reqs, 2, "three per page, so five needs two pages")
		assert.Equal(t, "page=1", reqs[0].Query)
		assert.Equal(t, "page=2", reqs[1].Query)
	})

	t.Run("-n is the same flag", func(t *testing.T) {
		app, _ := setupRecordingTestApp(t, accountWideCommentsRoute(9))
		env := runCommentsListJSON(t, app, "-n", "4")
		assert.Len(t, env.Data, 4)
	})

	t.Run("--all follows every page", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, accountWideCommentsRoute(3))
		env := runCommentsListJSON(t, app, "--all")
		assert.Len(t, env.Data, 3)

		reqs := transport.recorded()
		require.Len(t, reqs, 1)
		assert.Empty(t, reqs[0].Query, "page 0 is spelled as no page parameter")
	})

	t.Run("any positive --page is accepted", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, accountWideCommentsRoute(3))
		env := runCommentsListJSON(t, app, "--page", "7")
		assert.Len(t, env.Data, 3)

		reqs := transport.recorded()
		require.Len(t, reqs, 1)
		assert.Equal(t, "page=7", reqs[0].Query)
	})

	t.Run("explicit --page 0 is rejected", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t)
		assertCommentsListUsageCode(t, app, transport, "--page must be a positive page number", "--page", "0")
	})

	t.Run("negative --page is rejected", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t)
		assertCommentsListUsageCode(t, app, transport, "--page must be a positive page number", "--page=-1")
	})

	t.Run("negative --limit is rejected", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t)
		assertCommentsListUsageCode(t, app, transport, "--limit must be a positive number", "--limit=-1")
	})
}

// TestCommentsListAccountWideRendersRecordingsAsIs proves the []Recording
// payload needs no flattening branch: the styled renderer takes it unchanged,
// exactly as `recordings list` already hands it over.
func TestCommentsListAccountWideRendersRecordingsAsIs(t *testing.T) {
	app, _ := setupRecordingTestApp(t, accountWideCommentsRoute(3))
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatStyled, Writer: buf})

	require.NoError(t, runCommentsListCmd(t, app))
	assert.Contains(t, buf.String(), "Re: thing 1")
}
