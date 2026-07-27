package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	assert.Equal(t, "<content> required", outErr.Message)
	assert.Empty(t, transport.capturedBodies)
}

func TestCommentsCreateReadsContentFromStdin(t *testing.T) {
	transport := &mockCommentWriteTransport{}
	app, _ := setupCommentsWriteTestApp(t, transport)

	cmd := NewCommentsCmd()
	cmd.SetIn(strings.NewReader("hello from stdin"))
	err := executeCommand(cmd, app, "create", "123")
	require.NoError(t, err)
	require.Len(t, transport.capturedBodies, 1)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBodies[0], &body))
	assert.Equal(t, "<p>hello from stdin</p>", body["content"])
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

func TestReadPipedStdinIgnoresUnreadableStdin(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, r.Close())
	require.NoError(t, w.Close())

	cmd := newCommentsCreateCmd()
	cmd.SetIn(r)
	content, hasPipedStdin, err := readPipedStdin(cmd)
	require.NoError(t, err)
	assert.Empty(t, content)
	assert.False(t, hasPipedStdin)
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
