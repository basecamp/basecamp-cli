package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/names"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// --- Pure helpers -----------------------------------------------------------

func TestCommentTriggerID(t *testing.T) {
	// Bare numeric ID passes through untouched, with no URL account.
	id, acct, err := commentTriggerID("456")
	require.NoError(t, err)
	assert.Equal(t, "456", id)
	assert.Equal(t, "", acct)

	// Fragment URL yields the comment ID from the fragment (not the recording)
	// and surfaces the account the URL names.
	id, acct, err = commentTriggerID("https://3.basecamp.com/195539477/buckets/2085958499/todos/1069479351#__recording_1069479352")
	require.NoError(t, err)
	assert.Equal(t, "1069479352", id)
	assert.Equal(t, "195539477", acct)

	// Direct comment URL yields the comment ID and account.
	id, acct, err = commentTriggerID("https://3.basecamp.com/195539477/buckets/2085958499/comments/1069479352")
	require.NoError(t, err)
	assert.Equal(t, "1069479352", id)
	assert.Equal(t, "195539477", acct)
}

func TestCommentTriggerIDRejectsPlainRecordingURL(t *testing.T) {
	_, _, err := commentTriggerID("https://3.basecamp.com/195539477/buckets/2085958499/todos/1069479351")
	require.Error(t, err)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeUsage, outErr.Code)
	assert.Contains(t, outErr.Hint, "basecamp show")
}

func TestSortCommentsChronologically(t *testing.T) {
	base := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	in := []basecamp.Comment{
		{ID: 3, CreatedAt: base.Add(2 * time.Hour)},
		{ID: 1, CreatedAt: base},
		{ID: 5, CreatedAt: base.Add(time.Hour)}, // tie with ID 4 below
		{ID: 4, CreatedAt: base.Add(time.Hour)},
		{ID: 2, CreatedAt: base.Add(30 * time.Minute)},
	}
	out := sortCommentsChronologically(in)
	got := make([]int64, len(out))
	for i := range out {
		got[i] = out[i].ID
	}
	// created_at asc, then id asc for ties (4 before 5).
	assert.Equal(t, []int64{1, 2, 4, 5, 3}, got)
	// Original slice is untouched.
	assert.Equal(t, int64(3), in[0].ID)
}

func makeComments(n int) []basecamp.Comment {
	base := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	out := make([]basecamp.Comment, n)
	for i := range out {
		out[i] = basecamp.Comment{ID: int64(i + 1), CreatedAt: base.Add(time.Duration(i) * time.Minute)}
	}
	return out
}

func windowIDs(comments []basecamp.Comment) []int64 {
	ids := make([]int64, len(comments))
	for i := range comments {
		ids[i] = comments[i].ID
	}
	return ids
}

func TestSelectCommentWindowCentersOnFocus(t *testing.T) {
	comments := makeComments(100)
	// Focus at index 50 (ID 51), window 5 → 2 older, focus, 2 newer.
	sel, kind := selectCommentWindow(comments, 50, false, 5)
	assert.Equal(t, "focus_window", kind)
	assert.Equal(t, []int64{49, 50, 51, 52, 53}, windowIDs(sel))
}

func TestSelectCommentWindowEvenBiasesNewer(t *testing.T) {
	comments := makeComments(100)
	// Window 4 → before=(3)/2=1, after=2. 1 older, focus, 2 newer.
	sel, _ := selectCommentWindow(comments, 50, false, 4)
	assert.Equal(t, []int64{50, 51, 52, 53}, windowIDs(sel))
}

func TestSelectCommentWindowShiftsAtStartEdge(t *testing.T) {
	comments := makeComments(100)
	// Focus near the start: unused older capacity shifts to newer, still N total.
	sel, _ := selectCommentWindow(comments, 1, false, 5)
	require.Len(t, sel, 5)
	assert.Equal(t, []int64{1, 2, 3, 4, 5}, windowIDs(sel))
}

func TestSelectCommentWindowShiftsAtEndEdge(t *testing.T) {
	comments := makeComments(100)
	sel, _ := selectCommentWindow(comments, 99, false, 5)
	require.Len(t, sel, 5)
	assert.Equal(t, []int64{96, 97, 98, 99, 100}, windowIDs(sel))
}

func TestSelectCommentWindowSingle(t *testing.T) {
	comments := makeComments(100)
	sel, _ := selectCommentWindow(comments, 42, false, 1)
	assert.Equal(t, []int64{43}, windowIDs(sel))
}

func TestSelectCommentWindowFocusAbsentReturnsMostRecent(t *testing.T) {
	comments := makeComments(10)
	sel, kind := selectCommentWindow(comments, -1, false, 3)
	assert.Equal(t, "focus_window", kind)
	assert.Equal(t, []int64{8, 9, 10}, windowIDs(sel))
}

func TestSelectCommentWindowAll(t *testing.T) {
	comments := makeComments(7)
	sel, kind := selectCommentWindow(comments, 3, true, 3)
	assert.Equal(t, "all_fetched", kind)
	assert.Len(t, sel, 7)
}

func TestBuildCommentsMetaTotalCountZeroIsUnknown(t *testing.T) {
	m := buildCommentsMeta(5, 5, "all_fetched", true, basecamp.ListMeta{TotalCount: 0})
	assert.Nil(t, m["total"])
	assert.Equal(t, false, m["total_known"])
	assert.Equal(t, true, m["fetch_complete"])
	assert.Equal(t, false, m["api_truncated"])
	assert.Equal(t, "active", m["scope"])
	assert.Equal(t, "created_at_asc_id_asc", m["order"])
}

func TestBuildCommentsMetaTruncated(t *testing.T) {
	m := buildCommentsMeta(100, 41, "focus_window", false, basecamp.ListMeta{TotalCount: 250, Truncated: true})
	assert.Equal(t, 250, m["total"])
	assert.Equal(t, true, m["total_known"])
	assert.Equal(t, false, m["fetch_complete"])
	assert.Equal(t, true, m["api_truncated"])
	assert.Equal(t, false, m["focus_in_active_comments"])
}

// --- Author mention ---------------------------------------------------------

func TestBuildFocusAuthorEmbeddedSGID(t *testing.T) {
	author := buildFocusAuthor(&basecamp.Person{ID: 7, Name: "Jane Doe", AttachableSGID: "SGID123"})
	mention := author["mention"].(map[string]any)
	assert.Equal(t, "[@Jane Doe](mention:SGID123)", mention["syntax"])
	assert.Equal(t, "embedded_sgid", mention["resolution"])
	assert.Equal(t, false, mention["requires_lookup"])
}

func TestBuildFocusAuthorPersonLookup(t *testing.T) {
	author := buildFocusAuthor(&basecamp.Person{ID: 42, Name: "Jane Doe"})
	mention := author["mention"].(map[string]any)
	assert.Equal(t, "[@Jane Doe](person:42)", mention["syntax"])
	assert.Equal(t, "person_lookup", mention["resolution"])
	assert.Equal(t, true, mention["requires_lookup"])
}

func TestBuildFocusAuthorUnavailable(t *testing.T) {
	// No SGID and no positive ID (system actor).
	author := buildFocusAuthor(&basecamp.Person{ID: 0, Name: "System"})
	mention := author["mention"].(map[string]any)
	assert.Nil(t, mention["syntax"])
	assert.Equal(t, "unavailable", mention["resolution"])
}

func TestBuildFocusAuthorNilCreator(t *testing.T) {
	author := buildFocusAuthor(nil)
	assert.Nil(t, author["id"])
	assert.Equal(t, "", author["name"])
	mention := author["mention"].(map[string]any)
	assert.Equal(t, "unavailable", mention["resolution"])
}

func TestBuildFocusAuthorHostileNameEscaped(t *testing.T) {
	// Newlines, ANSI, and markdown metachars must not survive into the syntax.
	hostile := "Jane\n\x1b[31m](evil)[Doe"
	author := buildFocusAuthor(&basecamp.Person{ID: 7, Name: hostile, AttachableSGID: "SGID"})
	syntax := author["mention"].(map[string]any)["syntax"].(string)
	assert.NotContains(t, syntax, "\n")
	assert.NotContains(t, syntax, "\x1b")
	// The link-breaking brackets/parens are backslash-escaped.
	assert.NotContains(t, syntax, "](evil)")
	assert.True(t, strings.HasPrefix(syntax, "[@"))
	assert.True(t, strings.HasSuffix(syntax, "](mention:SGID)"))
}

func TestBuildFocusAuthorEmptySanitizedNameUnavailable(t *testing.T) {
	// A name that sanitizes to empty (only controls) yields no mention.
	author := buildFocusAuthor(&basecamp.Person{ID: 7, Name: "\x1b\x1b", AttachableSGID: "SGID"})
	mention := author["mention"].(map[string]any)
	assert.Nil(t, mention["syntax"])
	assert.Equal(t, "unavailable", mention["resolution"])
}

func TestFocusMentionRoundTripsThroughResolveMentions(t *testing.T) {
	cases := map[string]string{
		"normal name":  "Jane Doe",
		"hostile name": "Jane\n\x1b[31m](evil)[Doe",
	}
	for label, name := range cases {
		t.Run(label, func(t *testing.T) {
			author := buildFocusAuthor(&basecamp.Person{ID: 7, Name: name, AttachableSGID: "BAh7CEkiCG"})
			syntax := author["mention"].(map[string]any)["syntax"].(string)

			// Embed the produced syntax exactly as a reply body would, then run
			// it through the same mention pipeline `comments create` uses. The
			// embedded SGID must resolve to a mention attachment with zero
			// lookups — even when the display label came from a hostile name.
			html := richtext.MarkdownToHTML(syntax)
			result, err := resolveMentions(context.Background(), nil, html)
			require.NoError(t, err)
			assert.Contains(t, result.HTML, "BAh7CEkiCG")
			assert.Contains(t, result.HTML, "application/vnd.basecamp.mention")
		})
	}
}

// --- Integration ------------------------------------------------------------

// threadFixture builds a focus comment, its parent recording, and a list of
// active comments, and returns a responder for showTrackingTransport keyed by
// request path.
type threadFixture struct {
	focusJSON     string
	parentJSON    string
	listJSON      string
	listHeaders   map[string]string
	parentStatus  int
	commentStatus int
}

func (f threadFixture) responder() func(path string) (int, string, http.Header) {
	return func(path string) (int, string, http.Header) {
		switch {
		case strings.Contains(path, "/recordings/") && strings.HasSuffix(path, "/comments.json"):
			h := http.Header{}
			for k, v := range f.listHeaders {
				h.Set(k, v)
			}
			return 200, f.listJSON, h
		case strings.Contains(path, "/comments/"):
			status := f.commentStatus
			if status == 0 {
				status = 200
			}
			return status, f.focusJSON, nil
		default:
			// Any other path is treated as the parent recording fetch.
			status := f.parentStatus
			if status == 0 {
				status = 200
			}
			return status, f.parentJSON, nil
		}
	}
}

func runThreadCmd(t *testing.T, transport *showTrackingTransport, format output.Format, args ...string) (string, string, error) {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	app := showTestAppWithOutput(t, transport, format, stdout, stderr)
	app.Flags.Hints = true // root enables hints/breadcrumbs by default
	cmd := NewCommentsCmd()
	full := append([]string{"thread"}, args...)
	cmd.SetArgs(full)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

// runThreadCmdMaxPages runs the thread command with an SDK client capped at
// maxPages, so a next-page Link header on a list response makes the SDK report
// truncation deterministically.
func runThreadCmdMaxPages(t *testing.T, transport *showTrackingTransport, format output.Format, maxPages int, args ...string) (string, string, error) {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	app := showTestAppWithOutput(t, transport, format, stdout, stderr)
	app.Flags.Hints = true
	app.SDK = basecamp.NewClient(&basecamp.Config{BaseURL: "https://3.basecampapi.com"}, &showTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
		basecamp.WithMaxPages(maxPages),
	)
	app.Names = names.NewResolver(app.SDK, app.Auth, app.Config.AccountID)
	cmd := NewCommentsCmd()
	full := append([]string{"thread"}, args...)
	cmd.SetArgs(full)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func defaultThreadFixture() threadFixture {
	focus := map[string]any{
		"id":         456,
		"type":       "Comment",
		"content":    "<p>focus body</p>",
		"created_at": "2026-07-20T10:05:00Z",
		"parent":     map[string]any{"id": 123, "type": "Todo", "url": "https://3.basecampapi.com/99999/buckets/1/messages/999.json"},
		"creator":    map[string]any{"id": 7, "name": "Jane Doe", "attachable_sgid": "SGID123"},
	}
	parent := map[string]any{"id": 123, "type": "Todo", "title": "Parent todo", "content": "<p>todo body</p>"}
	// Out-of-order list including the focus (456).
	list := []map[string]any{
		{"id": 458, "content": "<p>c3</p>", "created_at": "2026-07-20T10:10:00Z", "creator": map[string]any{"name": "Bob"}},
		{"id": 456, "content": "<p>focus body</p>", "created_at": "2026-07-20T10:05:00Z", "creator": map[string]any{"name": "Jane Doe"}},
		{"id": 457, "content": "<p>c2</p>", "created_at": "2026-07-20T10:07:00Z", "creator": map[string]any{"name": "Bob"}},
		{"id": 455, "content": "<p>c0</p>", "created_at": "2026-07-20T10:00:00Z", "creator": map[string]any{"name": "Amy"}},
	}
	fb, _ := json.Marshal(focus)
	pb, _ := json.Marshal(parent)
	lb, _ := json.Marshal(list)
	return threadFixture{focusJSON: string(fb), parentJSON: string(pb), listJSON: string(lb)}
}

type threadEnvelope struct {
	Summary     string              `json:"summary"`
	Notice      string              `json:"notice"`
	Breadcrumbs []output.Breadcrumb `json:"breadcrumbs"`
	Data        map[string]any      `json:"data"`
}

func decodeThread(t *testing.T, stdout string) threadEnvelope {
	t.Helper()
	var env threadEnvelope
	require.NoError(t, json.Unmarshal([]byte(stdout), &env))
	return env
}

func TestThreadResolvesAndBuildsContract(t *testing.T) {
	fx := defaultThreadFixture()
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	stdout, _, err := runThreadCmd(t, transport, output.FormatJSON, "456")
	require.NoError(t, err)

	env := decodeThread(t, stdout)
	// reply_target routes to the parent recording, never the comment.
	replyTarget := env.Data["reply_target"].(map[string]any)
	assert.EqualValues(t, 123, replyTarget["recording_id"])
	assert.Equal(t, true, env.Data["recording_full"])

	recording := env.Data["recording"].(map[string]any)
	assert.EqualValues(t, 123, recording["id"])

	// reply_target.recording_id == recording.id (the smoke invariant).
	assert.EqualValues(t, recording["id"], replyTarget["recording_id"])

	focus := env.Data["focus"].(map[string]any)
	assert.EqualValues(t, 456, focus["comment_id"])

	// Comments sorted created_at asc, then id asc: 455, 456, 457, 458.
	comments := env.Data["comments"].([]any)
	require.Len(t, comments, 4)
	ids := make([]int, 0, len(comments))
	for _, c := range comments {
		ids = append(ids, int(c.(map[string]any)["id"].(float64)))
	}
	assert.Equal(t, []int{455, 456, 457, 458}, ids)

	meta := env.Data["comments_meta"].(map[string]any)
	assert.Equal(t, true, meta["focus_in_active_comments"])

	// The reply breadcrumb targets the parent recording.
	require.NotEmpty(t, env.Breadcrumbs)
	last := env.Breadcrumbs[len(env.Breadcrumbs)-1]
	assert.Equal(t, "reply", last.Action)
	assert.Contains(t, last.Cmd, "comments create 123")
}

func TestThreadFetchesParentViaTypeEndpointNotURL(t *testing.T) {
	fx := defaultThreadFixture()
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	_, _, err := runThreadCmd(t, transport, output.FormatJSON, "456")
	require.NoError(t, err)

	// The parent was fetched from the type endpoint (/todos/123.json), derived
	// from Parent.Type+ID — never from the decoy URL (which named messages/999).
	paths := transport.getRequests()
	assert.Contains(t, paths, "/99999/todos/123.json")
	for _, p := range paths {
		assert.NotContains(t, p, "messages/999")
	}
}

func TestThreadThreeTriggerFormsResolveSameComment(t *testing.T) {
	forms := []string{
		"456",
		"https://3.basecamp.com/99999/buckets/1/todos/123#__recording_456",
		"https://3.basecamp.com/99999/buckets/1/comments/456",
	}
	for _, form := range forms {
		t.Run(form, func(t *testing.T) {
			fx := defaultThreadFixture()
			transport := &showTrackingTransport{responderWithHeaders: fx.responder()}
			stdout, _, err := runThreadCmd(t, transport, output.FormatJSON, form)
			require.NoError(t, err)
			env := decodeThread(t, stdout)
			assert.EqualValues(t, 456, env.Data["focus"].(map[string]any)["comment_id"])
		})
	}
}

func TestThreadPreservesLargeIDsWithUseNumber(t *testing.T) {
	fx := defaultThreadFixture()
	// Parent ID > 2^53 must survive the decode without float rounding.
	bigID := int64(9007199254740993)
	focus := map[string]any{
		"id":         456,
		"type":       "Comment",
		"content":    "<p>x</p>",
		"created_at": "2026-07-20T10:05:00Z",
		"parent":     map[string]any{"id": bigID, "type": "Todo", "url": "https://x/y"},
		"creator":    map[string]any{"id": 7, "name": "Jane", "attachable_sgid": "S"},
	}
	parent := map[string]any{"id": bigID, "type": "Todo", "title": "Big"}
	fb, _ := json.Marshal(focus)
	pb, _ := json.Marshal(parent)
	fx.focusJSON = string(fb)
	fx.parentJSON = string(pb)
	fx.listJSON = "[]"
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	stdout, _, err := runThreadCmd(t, transport, output.FormatJSON, "456")
	require.NoError(t, err)
	// The exact ID appears in the reply-target path and JSON (no rounding).
	assert.Contains(t, transport.getRequests(), fmt.Sprintf("/99999/todos/%d.json", bigID))
	assert.Contains(t, stdout, fmt.Sprintf("%d", bigID))
}

func TestThreadNilParentHardFailsBeforeList(t *testing.T) {
	fx := defaultThreadFixture()
	focus := map[string]any{"id": 456, "type": "Comment", "content": "x", "created_at": "2026-07-20T10:05:00Z",
		"creator": map[string]any{"id": 7, "name": "Jane"}}
	fb, _ := json.Marshal(focus)
	fx.focusJSON = string(fb)
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	_, _, err := runThreadCmd(t, transport, output.FormatJSON, "456")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no parent recording")

	// No list or parent fetch happened — only the trigger Get.
	for _, p := range transport.getRequests() {
		assert.NotContains(t, p, "/recordings/")
		assert.NotContains(t, p, "/todos/")
	}
}

func TestThreadMappedParentFetchFailureHardErrors(t *testing.T) {
	fx := defaultThreadFixture()
	fx.parentStatus = 500
	fx.parentJSON = `{"error":"boom"}`
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	_, _, err := runThreadCmd(t, transport, output.FormatJSON, "456")
	require.Error(t, err)
}

func TestThreadRejectsURLAccountMismatchNoAPICall(t *testing.T) {
	fx := defaultThreadFixture()
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	// Configured account is 99999 (showTestApp); the URL names 88888.
	url := "https://3.basecamp.com/88888/buckets/1/todos/123#__recording_456"
	_, _, err := runThreadCmd(t, transport, output.FormatJSON, url)
	require.Error(t, err)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeUsage, outErr.Code)
	assert.Contains(t, outErr.Message, "does not match")
	// The mismatch is caught before any request — no Get, no List.
	assert.Empty(t, transport.getRequests())
}

func TestThreadMappedParent204HardErrors(t *testing.T) {
	fx := defaultThreadFixture()
	fx.parentStatus = 204
	fx.parentJSON = ""
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	_, _, err := runThreadCmd(t, transport, output.FormatJSON, "456")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no content")
}

func TestThreadMappedParentDecodeFailureHardErrors(t *testing.T) {
	fx := defaultThreadFixture()
	fx.parentJSON = `{not valid json`
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	_, _, err := runThreadCmd(t, transport, output.FormatJSON, "456")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

func TestThreadTruncationEmitsNotice(t *testing.T) {
	fx := defaultThreadFixture()
	// A same-origin next-page Link on the list response, combined with a
	// MaxPages cap of 1, makes the SDK report Meta.Truncated.
	fx.listHeaders = map[string]string{
		"Link": `<https://3.basecampapi.com/99999/recordings/123/comments.json?page=2>; rel="next"`,
	}
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	stdout, _, err := runThreadCmdMaxPages(t, transport, output.FormatJSON, 1, "456")
	require.NoError(t, err)

	env := decodeThread(t, stdout)
	assert.NotEmpty(t, env.Notice)
	assert.Contains(t, env.Notice, "truncated")
	meta := env.Data["comments_meta"].(map[string]any)
	assert.Equal(t, false, meta["fetch_complete"])
	assert.Equal(t, true, meta["api_truncated"])
}

func TestThreadStyledSanitizesHostileMultilineAuthor(t *testing.T) {
	fx := defaultThreadFixture()
	focus := map[string]any{
		"id": 456, "type": "Comment", "content": "<p>body</p>",
		"created_at": "2026-07-20T10:05:00Z",
		"parent":     map[string]any{"id": 123, "type": "Todo", "url": "https://x/y"},
		"creator":    map[string]any{"id": 7, "name": "Evil\nInjected Line", "attachable_sgid": "S"},
	}
	fb, _ := json.Marshal(focus)
	fx.focusJSON = string(fb)

	for _, format := range []output.Format{output.FormatStyled, output.FormatMarkdown} {
		transport := &showTrackingTransport{responderWithHeaders: fx.responder()}
		stdout, _, err := runThreadCmd(t, transport, format, "456")
		require.NoError(t, err)
		// The Focus Author detail row stays on one line — the injected newline
		// must not split the name across rows in either human format.
		assert.Contains(t, stdout, "Evil Injected Line")
		assert.NotContains(t, stdout, "Evil\nInjected Line")
	}
}

func TestThreadUnmappedParentTypeReturnsRef(t *testing.T) {
	fx := defaultThreadFixture()
	focus := map[string]any{
		"id": 456, "type": "Comment", "content": "x", "created_at": "2026-07-20T10:05:00Z",
		"parent":  map[string]any{"id": 123, "type": "Some::New::Type", "url": "https://x/y", "title": "Ref title"},
		"creator": map[string]any{"id": 7, "name": "Jane"},
	}
	fb, _ := json.Marshal(focus)
	fx.focusJSON = string(fb)
	fx.listJSON = "[]"
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	stdout, _, err := runThreadCmd(t, transport, output.FormatJSON, "456")
	require.NoError(t, err)
	env := decodeThread(t, stdout)
	assert.Equal(t, false, env.Data["recording_full"])
	ref := env.Data["recording_ref"].(map[string]any)
	assert.Equal(t, "Some::New::Type", ref["type"])
	assert.Equal(t, "Ref title", ref["title"])
	// reply_target still routes to the parent ID.
	assert.EqualValues(t, 123, env.Data["reply_target"].(map[string]any)["recording_id"])
}

func TestThreadWindowAndAllMutuallyExclusive(t *testing.T) {
	fx := defaultThreadFixture()
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}
	_, _, err := runThreadCmd(t, transport, output.FormatJSON, "456", "--all", "--window", "5")
	require.Error(t, err)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeUsage, outErr.Code)
}

func TestThreadFocusAbsentReportsFact(t *testing.T) {
	fx := defaultThreadFixture()
	// List without the focus (456).
	list := []map[string]any{
		{"id": 455, "content": "<p>c0</p>", "created_at": "2026-07-20T10:00:00Z", "creator": map[string]any{"name": "Amy"}},
		{"id": 457, "content": "<p>c2</p>", "created_at": "2026-07-20T10:07:00Z", "creator": map[string]any{"name": "Bob"}},
	}
	lb, _ := json.Marshal(list)
	fx.listJSON = string(lb)
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	stdout, _, err := runThreadCmd(t, transport, output.FormatJSON, "456")
	require.NoError(t, err)
	env := decodeThread(t, stdout)
	meta := env.Data["comments_meta"].(map[string]any)
	assert.Equal(t, false, meta["focus_in_active_comments"])
	assert.Equal(t, "focus_window", meta["selection"])
}

func TestThreadStyledRendersRecordingFocusAndComments(t *testing.T) {
	fx := defaultThreadFixture()
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}
	stdout, _, err := runThreadCmd(t, transport, output.FormatStyled, "456")
	require.NoError(t, err)

	// Sections render in the fixed order: Recording → Focus → Comments → reply.
	iRecording := strings.Index(stdout, "Recording")
	iFocus := strings.Index(stdout, "Focus")
	iComments := strings.Index(stdout, "Comments:")
	iReply := strings.Index(stdout, "comments create 123")
	require.Greater(t, iRecording, -1)
	require.Greater(t, iFocus, iRecording)
	require.Greater(t, iComments, iFocus)
	require.Greater(t, iReply, iComments)
}

func TestThreadMarkdownRenders(t *testing.T) {
	fx := defaultThreadFixture()
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}
	stdout, _, err := runThreadCmd(t, transport, output.FormatMarkdown, "456")
	require.NoError(t, err)
	assert.Contains(t, stdout, "## Comments")
	assert.Contains(t, stdout, "Focus")
}

func TestThreadSanitizesHostileHTMLBody(t *testing.T) {
	fx := defaultThreadFixture()
	focus := map[string]any{
		"id": 456, "type": "Comment",
		"content":    "<script>alert(1)</script><p>hi\x1b[31mred</p>",
		"created_at": "2026-07-20T10:05:00Z",
		"parent":     map[string]any{"id": 123, "type": "Todo", "url": "https://x/y"},
		"creator":    map[string]any{"id": 7, "name": "Jane", "attachable_sgid": "S"},
	}
	fb, _ := json.Marshal(focus)
	fx.focusJSON = string(fb)
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	stdout, _, err := runThreadCmd(t, transport, output.FormatStyled, "456")
	require.NoError(t, err)
	// No raw escape byte and no live script tag reaches the terminal sink.
	assert.NotContains(t, stdout, "\x1b[31m")
	assert.NotContains(t, stdout, "<script>")
}

// --- Iteration 2: trust-contract completeness -------------------------------

// attachmentFocusFixture builds a focus whose Get carries a content attachment
// but whose parent's active-comments list omits the focus entirely — proving the
// attachment data rides on the Get, not the list.
func attachmentFocusFixture(t *testing.T) threadFixture {
	t.Helper()
	fx := defaultThreadFixture()
	focus := map[string]any{
		"id": 456, "type": "Comment", "content": "<p>see attached</p>",
		"created_at": "2026-07-20T10:05:00Z",
		"parent":     map[string]any{"id": 123, "type": "Todo", "url": "https://x/y"},
		"creator":    map[string]any{"id": 7, "name": "Jane Doe", "attachable_sgid": "SGID123"},
		"content_attachments": []map[string]any{
			{"id": 999, "sgid": "AT", "filename": "spec.pdf", "content_type": "application/pdf", "byte_size": 2048, "download_url": "https://x/dl"},
		},
	}
	// List that does NOT contain the focus (456).
	list := []map[string]any{
		{"id": 455, "content": "<p>c0</p>", "created_at": "2026-07-20T10:00:00Z", "creator": map[string]any{"name": "Amy"}},
		{"id": 457, "content": "<p>c2</p>", "created_at": "2026-07-20T10:07:00Z", "creator": map[string]any{"name": "Bob"}},
	}
	fb, _ := json.Marshal(focus)
	lb, _ := json.Marshal(list)
	fx.focusJSON = string(fb)
	fx.listJSON = string(lb)
	return fx
}

func TestThreadSurfacesFocusAttachmentEvenWhenAbsentFromList(t *testing.T) {
	fx := attachmentFocusFixture(t)
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	stdout, _, err := runThreadCmd(t, transport, output.FormatJSON, "456")
	require.NoError(t, err)
	env := decodeThread(t, stdout)

	// The attachment survives from the Get though the list omits the focus.
	focusOut := env.Data["focus"].(map[string]any)
	atts, ok := focusOut["content_attachments"].([]any)
	require.True(t, ok, "focus.content_attachments should be a non-nil array")
	require.Len(t, atts, 1)
	assert.Equal(t, "spec.pdf", atts[0].(map[string]any)["filename"])

	// A type-safe download breadcrumb fires even though the focus is absent.
	dlIdx, replyIdx := -1, -1
	for i, b := range env.Breadcrumbs {
		if b.Action == "download" {
			dlIdx = i
			assert.Equal(t, "basecamp attachments download 456 --type comment", b.Cmd)
		}
		if b.Action == "reply" {
			replyIdx = i
		}
	}
	require.GreaterOrEqual(t, dlIdx, 0, "download breadcrumb should be present")
	require.GreaterOrEqual(t, replyIdx, 0, "reply breadcrumb should be present")
	assert.Less(t, dlIdx, replyIdx, "download breadcrumb must come before reply (reply last)")
}

func TestThreadNoAttachmentBreadcrumbWhenNone(t *testing.T) {
	fx := defaultThreadFixture() // no content_attachments on the focus
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	stdout, _, err := runThreadCmd(t, transport, output.FormatJSON, "456")
	require.NoError(t, err)
	env := decodeThread(t, stdout)

	for _, b := range env.Breadcrumbs {
		assert.NotEqual(t, "download", b.Action, "no download breadcrumb without attachments")
	}
	// Reply is still the last breadcrumb.
	last := env.Breadcrumbs[len(env.Breadcrumbs)-1]
	assert.Equal(t, "reply", last.Action)
}

func TestThreadFocusAbsentNoticeWindowSelectionAware(t *testing.T) {
	fx := attachmentFocusFixture(t) // list omits focus; 2 fetched
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	stdout, _, err := runThreadCmd(t, transport, output.FormatJSON, "456")
	require.NoError(t, err)
	env := decodeThread(t, stdout)

	assert.Contains(t, env.Notice, "not present in the fetched active discussion")
	assert.Contains(t, env.Notice, "most recent 2 fetched comments")
	assert.NotContains(t, env.Notice, "all 2 fetched")
}

func TestThreadFocusAbsentNoticeAllSelectionAware(t *testing.T) {
	fx := attachmentFocusFixture(t)
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	stdout, _, err := runThreadCmd(t, transport, output.FormatJSON, "456", "--all")
	require.NoError(t, err)
	env := decodeThread(t, stdout)

	assert.Contains(t, env.Notice, "not present in the fetched active discussion")
	assert.Contains(t, env.Notice, "all 2 fetched comments")
	assert.NotContains(t, env.Notice, "most recent")
}

func TestThreadFocusAbsentNoticeComposesWithTruncation(t *testing.T) {
	fx := attachmentFocusFixture(t)
	fx.listHeaders = map[string]string{
		"Link": `<https://3.basecampapi.com/99999/recordings/123/comments.json?page=2>; rel="next"`,
	}
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	stdout, _, err := runThreadCmdMaxPages(t, transport, output.FormatJSON, 1, "456")
	require.NoError(t, err)
	env := decodeThread(t, stdout)

	// Both fragments compose into one combined notice.
	assert.Contains(t, env.Notice, "most recent")
	assert.Contains(t, env.Notice, "truncated")
}

func TestThreadSummaryMatrix(t *testing.T) {
	trigger := &basecamp.Comment{ID: 123}
	author := map[string]any{"name": "Jane"}
	cases := []struct {
		name              string
		returned, fetched int
		selection         string
		fetchComplete     bool
		focusPresent      bool
		want              string
	}{
		{"all complete", 50, 50, "all_fetched", true, true,
			"Comment #123 by Jane — all 50 active comments"},
		{"all truncated", 40, 40, "all_fetched", false, true,
			"Comment #123 by Jane — all 40 fetched active comments; fetch incomplete"},
		{"window focus present complete", 41, 50, "focus_window", true, true,
			"Comment #123 by Jane — showing 41 of 50 active comments around the focus"},
		{"window focus present truncated", 41, 60, "focus_window", false, true,
			"Comment #123 by Jane — showing 41 around the focus from 60 fetched active comments; more exist on server"},
		{"window focus absent complete", 41, 50, "focus_window", true, false,
			"Comment #123 by Jane — showing the 41 most recent of 50 active comments"},
		{"window focus absent truncated", 41, 60, "focus_window", false, false,
			"Comment #123 by Jane — showing the 41 most recent within the fetched subset (60 fetched)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := threadSummary(trigger, author, c.returned, c.fetched, c.selection, c.fetchComplete, c.focusPresent)
			assert.Equal(t, c.want, got)
			assert.NotContains(t, got, "in discussion")
			if !c.focusPresent && c.selection != "all_fetched" {
				assert.NotContains(t, got, "around the focus")
			}
		})
	}
}

func TestThreadSummary41Of50Integration(t *testing.T) {
	fx := defaultThreadFixture()
	base := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	list := make([]map[string]any, 0, 50)
	for i := 0; i < 50; i++ {
		id := 1000 + i
		if i == 25 {
			id = 456 // focus present in the fetched set
		}
		list = append(list, map[string]any{
			"id":         id,
			"content":    "<p>c</p>",
			"created_at": base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			"creator":    map[string]any{"name": "Bob"},
		})
	}
	lb, _ := json.Marshal(list)
	fx.listJSON = string(lb)
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	stdout, _, err := runThreadCmd(t, transport, output.FormatJSON, "456")
	require.NoError(t, err)
	env := decodeThread(t, stdout)
	assert.Contains(t, env.Summary, "41 of 50")
	assert.NotContains(t, env.Summary, "in discussion")
}

func TestThreadRendersMentionInHumanOutput(t *testing.T) {
	for _, format := range []output.Format{output.FormatStyled, output.FormatMarkdown} {
		fx := defaultThreadFixture() // creator Jane Doe with attachable_sgid SGID123
		transport := &showTrackingTransport{responderWithHeaders: fx.responder()}
		stdout, _, err := runThreadCmd(t, transport, format, "456")
		require.NoError(t, err)
		assert.Contains(t, stdout, "[@Jane Doe](mention:SGID123)",
			"human format %v must render the paste-ready mention", format)
	}
}

func TestThreadParentNullBodyHardErrors(t *testing.T) {
	fx := defaultThreadFixture()
	fx.parentJSON = "null"
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}

	_, _, err := runThreadCmd(t, transport, output.FormatJSON, "456")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty body")
}

func TestThreadResolverRejectsBeforeAnyRequest(t *testing.T) {
	cases := []struct{ name, arg string }{
		{"plain recording URL", "https://3.basecamp.com/99999/buckets/1/todos/123"},
		{"non-positive id", "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := defaultThreadFixture()
			transport := &showTrackingTransport{responderWithHeaders: fx.responder()}
			_, _, err := runThreadCmd(t, transport, output.FormatJSON, c.arg)
			require.Error(t, err)
			var outErr *output.Error
			require.True(t, errors.As(err, &outErr))
			assert.Equal(t, output.CodeUsage, outErr.Code)
			assert.Empty(t, transport.getRequests(), "must reject before any API call")
		})
	}
}

// runThreadCmdAccount runs `thread` with an explicit configured account (use ""
// to simulate an unconfigured run), so URL-account adoption and pre-API ID
// validation can be exercised without a configured account masking them.
func runThreadCmdAccount(t *testing.T, transport *showTrackingTransport, accountID string, args ...string) (string, error) {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	app := showTestAppWithOutput(t, transport, output.FormatJSON, stdout, stderr)
	app.Flags.Hints = true
	app.Config.AccountID = accountID
	app.Names.SetAccountID(accountID)
	cmd := NewCommentsCmd()
	full := append([]string{"thread"}, args...)
	cmd.SetArgs(full)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	return stdout.String(), err
}

func TestThreadRejectsUntrustedHostNoAPICall(t *testing.T) {
	cases := map[string]string{
		"direct comment URL":  "https://evil.example/99999/buckets/1/comments/456",
		"fragment URL":        "https://evil.example/99999/buckets/1/todos/123#__recording_456",
		"api look-alike host": "https://3.basecampapi.evil.com/99999/buckets/1/comments/456",
	}
	for name, url := range cases {
		t.Run(name, func(t *testing.T) {
			fx := defaultThreadFixture()
			transport := &showTrackingTransport{responderWithHeaders: fx.responder()}
			_, _, err := runThreadCmd(t, transport, output.FormatJSON, url)
			require.Error(t, err)
			var outErr *output.Error
			require.True(t, errors.As(err, &outErr))
			assert.Equal(t, output.CodeUsage, outErr.Code)
			assert.Contains(t, outErr.Message, "untrusted host")
			assert.Empty(t, transport.getRequests(), "untrusted host must be rejected before any API call")
		})
	}
}

func TestThreadInvalidIDBeforeAccountResolution(t *testing.T) {
	// Empty configuration + non-positive id → Invalid comment ID, not
	// "--account is required", and zero requests (no account/content fetch).
	fx := defaultThreadFixture()
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}
	stdout, err := runThreadCmdAccount(t, transport, "", "0")
	require.Error(t, err)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeUsage, outErr.Code)
	assert.Equal(t, "Invalid comment ID", outErr.Message)
	assert.NotContains(t, stdout, "account is required")
	assert.Empty(t, transport.getRequests())
}

func TestThreadAdoptsURLAccountWhenUnconfigured(t *testing.T) {
	// Empty configuration + trusted URL → the fetch targets the URL's account,
	// not an interactively-selected one.
	fx := defaultThreadFixture()
	transport := &showTrackingTransport{responderWithHeaders: fx.responder()}
	url := "https://3.basecamp.com/77777/buckets/1/comments/456"
	stdout, err := runThreadCmdAccount(t, transport, "", url)
	require.NoError(t, err)

	reqs := transport.getRequests()
	require.NotEmpty(t, reqs)
	targetedURLAccount := false
	for _, p := range reqs {
		assert.Contains(t, p, "/77777/", "every request must target the URL's account")
		if strings.Contains(p, "/77777/") {
			targetedURLAccount = true
		}
	}
	assert.True(t, targetedURLAccount)

	env := decodeThread(t, stdout)
	assert.EqualValues(t, 456, env.Data["focus"].(map[string]any)["comment_id"])

	// The reply contract stays runnable in a fresh process: reply_target names
	// the adopted account, and the reply breadcrumb spells out --account.
	replyTarget := env.Data["reply_target"].(map[string]any)
	assert.Equal(t, "77777", replyTarget["account_id"])
	var replyCmd string
	for _, b := range env.Breadcrumbs {
		if b.Action == "reply" {
			replyCmd = b.Cmd
		}
	}
	assert.Contains(t, replyCmd, "--account 77777")
}
