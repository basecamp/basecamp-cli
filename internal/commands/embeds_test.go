package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

const (
	embedVideoURL = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	embedChartURL = "https://dash.37signals.com/widgets/abc"
	embedCardURL  = "https://3.basecamp.com/99999/buckets/123/card_tables/cards/789"
)

type embedReply struct {
	status int
	body   string
}

type embedTransport struct {
	mu      sync.Mutex
	hang    bool
	creates map[string]embedReply
	gets    map[string][]embedReply
	calls   []string
	posted  []byte
}

func (t *embedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	respond := func(reply embedReply) (*http.Response, error) {
		return &http.Response{
			StatusCode: reply.status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(reply.body)),
			Request:    req,
		}, nil
	}

	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body.Close()
	}

	switch {
	case req.Method == http.MethodPost && req.URL.Path == "/99999/embeds.json" && t.hang:
		t.calls = append(t.calls, "create (hung)")
		t.mu.Unlock()
		<-req.Context().Done()
		t.mu.Lock()
		return nil, req.Context().Err()
	case req.Method == http.MethodPost && req.URL.Path == "/99999/embeds.json":
		var params struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(body, &params)
		t.calls = append(t.calls, "create "+params.URL)
		if reply, ok := t.creates[params.URL]; ok {
			return respond(reply)
		}
		return respond(embedReply{http.StatusUnprocessableEntity, `{"error":"Basecamp can't embed this URL. Link to it instead."}`})
	case req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, "/99999/embeds/"):
		sgid := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/99999/embeds/"), ".json")
		t.calls = append(t.calls, "get "+sgid)
		replies := t.gets[sgid]
		if len(replies) == 0 {
			return respond(embedReply{http.StatusNotFound, `{"error":"not found"}`})
		}
		reply := replies[0]
		if len(replies) > 1 {
			t.gets[sgid] = replies[1:]
		}
		return respond(reply)
	case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/comments.json"):
		t.calls = append(t.calls, "comment")
		t.posted = body
		return respond(embedReply{http.StatusCreated, `{"id": 999, "content": "ok", "created_at": "2024-01-01T00:00:00Z"}`})
	default:
		return respond(embedReply{http.StatusNotFound, `{"error":"not found"}`})
	}
}

func (t *embedTransport) postedContent(tb testing.TB) string {
	tb.Helper()
	t.mu.Lock()
	defer t.mu.Unlock()
	require.NotEmpty(tb, t.posted, "nothing was posted")
	var body map[string]any
	require.NoError(tb, json.Unmarshal(t.posted, &body))
	content, _ := body["content"].(string)
	return content
}

func (t *embedTransport) recordedCalls() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.calls...)
}

func embedJSON(url, status, sgid string) string {
	embed := map[string]any{"url": url, "status": status, "sgid": sgid}
	if status == "active" {
		embed["type"] = "video"
		embed["content_type"] = "embed/youtube-video"
	}
	b, _ := json.Marshal(embed)
	return string(b)
}

func fastEmbedPolling(t *testing.T, wait time.Duration) {
	t.Helper()
	interval, deadline := embedPollInterval, embedWait
	embedPollInterval, embedWait = time.Millisecond, wait
	t.Cleanup(func() { embedPollInterval, embedWait = interval, deadline })
}

func runEmbedCommand(cmd *cobra.Command, app *appctx.App, args ...string) (string, error) {
	cmd.SetArgs(args)
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)
	err := cmd.Execute()
	return stderr.String(), err
}

func TestCommentsCreateEmbedsAURLAloneOnItsLine(t *testing.T) {
	fastEmbedPolling(t, time.Second)
	transport := &embedTransport{
		creates: map[string]embedReply{embedVideoURL: {http.StatusCreated, embedJSON(embedVideoURL, "pending", "sgid-video")}},
		gets: map[string][]embedReply{"sgid-video": {
			{http.StatusOK, embedJSON(embedVideoURL, "pending", "sgid-video")},
			{http.StatusOK, embedJSON(embedVideoURL, "active", "sgid-video")},
		}},
	}
	app, _ := newTestAppWithTransport(t, transport)

	stderr, err := runEmbedCommand(NewCommentsCmd(), app, "create", embedCardURL, "Watch this:\n"+embedVideoURL)
	require.NoError(t, err)

	assert.Equal(t, "<p>Watch this:</p>\n"+`<bc-attachment sgid="sgid-video" content-type="embed/youtube-video"></bc-attachment>`,
		transport.postedContent(t))
	assert.Equal(t, []string{"create " + embedVideoURL, "get sgid-video", "get sgid-video", "comment"}, transport.recordedCalls())
	assert.Empty(t, stderr)
}

func TestCommentsCreateEmbedsEveryURLBeforeWaitingOnAny(t *testing.T) {
	fastEmbedPolling(t, time.Second)
	transport := &embedTransport{
		creates: map[string]embedReply{
			embedChartURL: {http.StatusCreated, embedJSON(embedChartURL, "pending", "sgid-chart")},
			embedVideoURL: {http.StatusOK, embedJSON(embedVideoURL, "active", "sgid-video")},
		},
		gets: map[string][]embedReply{"sgid-chart": {{http.StatusOK, embedJSON(embedChartURL, "active", "sgid-chart")}}},
	}
	app, _ := newTestAppWithTransport(t, transport)

	_, err := runEmbedCommand(NewCommentsCmd(), app, "create", embedCardURL, embedChartURL+"\n"+embedVideoURL+"\n\n"+embedChartURL)
	require.NoError(t, err)

	assert.Equal(t, []string{"create " + embedChartURL, "create " + embedVideoURL, "get sgid-chart", "comment"}, transport.recordedCalls())
	assert.Equal(t, 2, strings.Count(transport.postedContent(t), `sgid="sgid-chart"`))
	assert.Contains(t, transport.postedContent(t), `sgid="sgid-video"`)
}

func TestCommentsCreateLeavesAURLBasecampCanNotEmbedAsALinkQuietly(t *testing.T) {
	transport := &embedTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	stderr, err := runEmbedCommand(NewCommentsCmd(), app, "create", embedCardURL, "https://github.com/basecamp/bc3/pull/1")
	require.NoError(t, err)

	assert.Equal(t, "<p>https://github.com/basecamp/bc3/pull/1</p>", transport.postedContent(t))
	assert.Empty(t, stderr)
}

func TestCommentsCreateLeavesALinkWhenTheEmbedFails(t *testing.T) {
	for name, tc := range map[string]struct {
		create  embedReply
		gets    []embedReply
		warning string
	}{
		"rate limited": {
			create:  embedReply{http.StatusTooManyRequests, `{"error":"Too many new embeds. Try again later."}`},
			warning: "couldn't embed " + embedVideoURL + ", so it stays a link",
		},
		"refused to agents": {
			create:  embedReply{http.StatusForbidden, `{"error":"Basecamp doesn't let agents do this.","reason":"agent_not_permitted"}`},
			warning: "couldn't embed " + embedVideoURL + ", so it stays a link",
		},
		"the provider has nothing": {
			create:  embedReply{http.StatusCreated, embedJSON(embedVideoURL, "pending", "sgid-video")},
			gets:    []embedReply{{http.StatusOK, embedJSON(embedVideoURL, "not_found", "sgid-video")}},
			warning: "Basecamp found nothing to embed at " + embedVideoURL + ", so it stays a link.",
		},
		"the provider is slow": {
			create:  embedReply{http.StatusCreated, embedJSON(embedVideoURL, "pending", "sgid-video")},
			gets:    []embedReply{{http.StatusOK, embedJSON(embedVideoURL, "pending", "sgid-video")}},
			warning: embedVideoURL + " took too long to answer, so it stays a link.",
		},
		"the embed vanishes": {
			create:  embedReply{http.StatusCreated, embedJSON(embedVideoURL, "pending", "sgid-video")},
			gets:    []embedReply{{http.StatusNotFound, `{"error":"not found"}`}},
			warning: "couldn't embed " + embedVideoURL + ", so it stays a link",
		},
	} {
		t.Run(name, func(t *testing.T) {
			fastEmbedPolling(t, 20*time.Millisecond)
			transport := &embedTransport{
				creates: map[string]embedReply{embedVideoURL: tc.create},
				gets:    map[string][]embedReply{"sgid-video": tc.gets},
			}
			app, _ := newTestAppWithTransport(t, transport)

			stderr, err := runEmbedCommand(NewCommentsCmd(), app, "create", embedCardURL, embedVideoURL)
			require.NoError(t, err)

			assert.Equal(t, "<p>"+embedVideoURL+"</p>", transport.postedContent(t))
			assert.Contains(t, stderr, tc.warning)
		})
	}
}

func TestCommentsCreateKeepsPollingTheOthersWhileOneProviderIsSlow(t *testing.T) {
	fastEmbedPolling(t, 50*time.Millisecond)
	transport := &embedTransport{
		creates: map[string]embedReply{
			embedChartURL: {http.StatusOK, embedJSON(embedChartURL, "pending", "sgid-chart")},
			embedVideoURL: {http.StatusOK, embedJSON(embedVideoURL, "pending", "sgid-video")},
		},
		gets: map[string][]embedReply{
			"sgid-chart": {{http.StatusOK, embedJSON(embedChartURL, "pending", "sgid-chart")}},
			"sgid-video": {{http.StatusOK, embedJSON(embedVideoURL, "active", "sgid-video")}},
		},
	}
	app, _ := newTestAppWithTransport(t, transport)

	stderr, err := runEmbedCommand(NewCommentsCmd(), app, "create", embedCardURL, embedChartURL+"\n"+embedVideoURL)
	require.NoError(t, err)

	assert.Equal(t, "<p>"+embedChartURL+"</p>\n"+`<bc-attachment sgid="sgid-video" content-type="embed/youtube-video"></bc-attachment>`, transport.postedContent(t))
	assert.Contains(t, stderr, embedChartURL+" took too long to answer")
}

func TestCommentsCreateStopsAskingForEmbedsAfterAFailure(t *testing.T) {
	transport := &embedTransport{
		creates: map[string]embedReply{
			embedChartURL: {http.StatusInternalServerError, `{"error":"boom"}`},
			embedVideoURL: {http.StatusOK, embedJSON(embedVideoURL, "active", "sgid-video")},
		},
	}
	app, _ := newTestAppWithTransport(t, transport)

	stderr, err := runEmbedCommand(NewCommentsCmd(), app, "create", embedCardURL, embedChartURL+"\n"+embedVideoURL)
	require.NoError(t, err)

	assert.Equal(t, "<p>"+embedChartURL+"\n"+embedVideoURL+"</p>", transport.postedContent(t))
	assert.Equal(t, 1, strings.Count(strings.Join(transport.recordedCalls(), ","), "create "))
	assert.Contains(t, stderr, "the URL after it stays a link too")
}

func TestCommentsCreateStopsPollingAfterAFailure(t *testing.T) {
	fastEmbedPolling(t, time.Second)
	transport := &embedTransport{
		creates: map[string]embedReply{
			embedChartURL: {http.StatusOK, embedJSON(embedChartURL, "pending", "sgid-chart")},
			embedVideoURL: {http.StatusInternalServerError, `{"error":"boom"}`},
		},
		gets: map[string][]embedReply{"sgid-chart": {{http.StatusOK, embedJSON(embedChartURL, "active", "sgid-chart")}}},
	}
	app, _ := newTestAppWithTransport(t, transport)

	stderr, err := runEmbedCommand(NewCommentsCmd(), app, "create", embedCardURL, embedChartURL+"\n"+embedVideoURL)
	require.NoError(t, err)

	assert.Equal(t, []string{"create " + embedChartURL, "create " + embedVideoURL, "comment"}, transport.recordedCalls())
	assert.Equal(t, "<p>"+embedChartURL+"\n"+embedVideoURL+"</p>", transport.postedContent(t))
	assert.Contains(t, stderr, "couldn't embed "+embedChartURL)
}

func TestCommentsCreateWarnsOnOneCleanLine(t *testing.T) {
	transport := &embedTransport{
		creates: map[string]embedReply{embedVideoURL: {http.StatusInternalServerError, `{"error":"boom\u001b[31m\nWarning: forged"}`}},
	}
	app, _ := newTestAppWithTransport(t, transport)

	stderr, err := runEmbedCommand(NewCommentsCmd(), app, "create", embedCardURL, embedVideoURL)
	require.NoError(t, err)

	assert.NotContains(t, stderr, "\x1b")
	assert.Equal(t, 1, strings.Count(stderr, "\n"), stderr)
	assert.Contains(t, stderr, "couldn't embed "+embedVideoURL)
}

func TestCommentsCreatePostsWhenAnEmbedRequestHangs(t *testing.T) {
	fastEmbedPolling(t, 50*time.Millisecond)
	transport := &embedTransport{hang: true}
	app, _ := newTestAppWithTransport(t, transport)

	started := time.Now()
	stderr, err := runEmbedCommand(NewCommentsCmd(), app, "create", embedCardURL, embedVideoURL)
	require.NoError(t, err)

	assert.Less(t, time.Since(started), 5*time.Second)
	assert.Equal(t, "<p>"+embedVideoURL+"</p>", transport.postedContent(t))
	assert.Contains(t, stderr, "couldn't embed "+embedVideoURL)
}

func TestCommentsCreateWithFormatHTMLEmbedsNothing(t *testing.T) {
	transport := &embedTransport{
		creates: map[string]embedReply{embedVideoURL: {http.StatusOK, embedJSON(embedVideoURL, "active", "sgid-video")}},
	}
	app, _ := newTestAppWithTransport(t, transport)

	html := "<p>" + embedVideoURL + "</p>\n" + embedVideoURL
	_, err := runEmbedCommand(NewCommentsCmd(), app, "create", embedCardURL, html, "--format", "html")
	require.NoError(t, err)

	assert.Equal(t, html, transport.postedContent(t))
	assert.Equal(t, []string{"comment"}, transport.recordedCalls())
}

func TestCommentsCreateKeepsATitledLink(t *testing.T) {
	transport := &embedTransport{
		creates: map[string]embedReply{embedVideoURL: {http.StatusOK, embedJSON(embedVideoURL, "active", "sgid-video")}},
	}
	app, _ := newTestAppWithTransport(t, transport)

	_, err := runEmbedCommand(NewCommentsCmd(), app, "create", embedCardURL, "[The video]("+embedVideoURL+")")
	require.NoError(t, err)

	assert.Equal(t, `<p><a href="`+embedVideoURL+`">The video</a></p>`, transport.postedContent(t))
	assert.Equal(t, []string{"comment"}, transport.recordedCalls())
}

func TestEmbedsCreateWaitsAndPrintsTheAttachment(t *testing.T) {
	fastEmbedPolling(t, time.Second)
	transport := &embedTransport{
		creates: map[string]embedReply{embedVideoURL: {http.StatusCreated, embedJSON(embedVideoURL, "pending", "sgid-video")}},
		gets:    map[string][]embedReply{"sgid-video": {{http.StatusOK, embedJSON(embedVideoURL, "active", "sgid-video")}}},
	}
	app, out := newTestAppWithTransport(t, transport)

	_, err := runEmbedCommand(NewEmbedsCmd(), app, "create", embedVideoURL)
	require.NoError(t, err)

	var envelope struct {
		Data struct {
			URL        string `json:"url"`
			Status     string `json:"status"`
			SGID       string `json:"sgid"`
			Attachment string `json:"attachment"`
		} `json:"data"`
		Summary string `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	assert.Equal(t, "active", envelope.Data.Status)
	assert.Equal(t, "sgid-video", envelope.Data.SGID)
	assert.Equal(t, `<bc-attachment sgid="sgid-video" content-type="embed/youtube-video"></bc-attachment>`, envelope.Data.Attachment)
	assert.Equal(t, "Embed ready for "+embedVideoURL, envelope.Summary)
}

func TestEmbedsCreateRefusesAURLBasecampCanNotEmbed(t *testing.T) {
	transport := &embedTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	_, err := runEmbedCommand(NewEmbedsCmd(), app, "create", "https://example.com")

	var outErr *output.Error
	require.ErrorAs(t, err, &outErr)
	assert.Equal(t, "Basecamp can't embed https://example.com", outErr.Message)
	assert.Equal(t, "Link to it instead", outErr.Hint)
}

func TestEmbedsCreateWithoutAURLAsksForNothing(t *testing.T) {
	transport := &embedTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	_, _ = runEmbedCommand(NewEmbedsCmd(), app, "create", " ")

	assert.Empty(t, transport.recordedCalls())
}

func TestEmbedsCreateLeavesNoAttachmentForAnEmbedThatIsNotReady(t *testing.T) {
	fastEmbedPolling(t, 5*time.Millisecond)
	transport := &embedTransport{
		creates: map[string]embedReply{embedVideoURL: {http.StatusCreated, embedJSON(embedVideoURL, "pending", "sgid-video")}},
		gets:    map[string][]embedReply{"sgid-video": {{http.StatusOK, embedJSON(embedVideoURL, "pending", "sgid-video")}}},
	}
	app, out := newTestAppWithTransport(t, transport)

	_, err := runEmbedCommand(NewEmbedsCmd(), app, "create", embedVideoURL)
	require.NoError(t, err)

	assert.NotContains(t, out.String(), `"attachment"`)
	assert.Contains(t, out.String(), "basecamp embeds show")
}

func TestEmbedsShow(t *testing.T) {
	transport := &embedTransport{
		gets: map[string][]embedReply{"sgid-video": {{http.StatusOK, embedJSON(embedVideoURL, "active", "sgid-video")}}},
	}
	app, out := newTestAppWithTransport(t, transport)

	_, err := runEmbedCommand(NewEmbedsCmd(), app, "show", "sgid-video")
	require.NoError(t, err)

	var envelope struct {
		Data struct {
			Attachment string `json:"attachment"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	assert.Equal(t, `<bc-attachment sgid="sgid-video" content-type="embed/youtube-video"></bc-attachment>`, envelope.Data.Attachment)
	assert.Equal(t, []string{"get sgid-video"}, transport.recordedCalls())
}
