package mcpserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/basecamp/mcp/mcptest"
)

// The composite actions are basecamp-sdk compositions, so these tests drive
// them the way a client does — over the MCP wire, against an httptest
// upstream standing in for Basecamp — and assert on the requests the
// composition actually makes. The chat line is the case worth the most
// care: its read needs a Campfire id the pointer does not carry, so the
// helper discovers one, and the three ways that can end (found, every
// candidate said 404, a candidate failed) must stay apart.

// personSGID builds an attachable_sgid naming a person, in the JSON
// envelope Rails' message serializer emits. The signature is not verified
// by the mention helpers (only Basecamp can verify one), so a placeholder
// stands in for it and no real signed id is checked in.
func personSGID(personID int64) string {
	envelope := fmt.Sprintf(`{"_rails":{"data":"gid://bc3/Person/%d","pur":"attachable"}}`, personID)
	return base64.StdEncoding.EncodeToString([]byte(envelope)) + "--0000000000000000000000000000000000000000"
}

// upstreamLog records the requests a composition made, in order.
type upstreamLog struct {
	mu    sync.Mutex
	paths []string
}

func (l *upstreamLog) record(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paths = append(l.paths, path)
}

func (l *upstreamLog) seen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.paths...)
}

// compositeSession stands up the server against an upstream whose routes
// are keyed by path prefix. A request no route claims fails the test — a
// composition that reads more than it should is a finding, not a pass.
func compositeSession(t *testing.T, routes map[string]http.HandlerFunc) (*mcp.ClientSession, *upstreamLog) {
	t.Helper()
	log := &upstreamLog{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r.URL.Path)
		for prefix, handler := range routes {
			if strings.HasPrefix(r.URL.Path, prefix) {
				handler(w, r)
				return
			}
		}
		t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"unexpected"}`))
	}))
	t.Cleanup(upstream.Close)

	srv, err := New(newTestAPI(upstream), Config{})
	require.NoError(t, err)
	return mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler))), log
}

func summarize(t *testing.T, session *mcp.ClientSession, params map[string]any) (string, bool) {
	t.Helper()
	return mcptest.CallText(t, session, "basecamp_recordings", map[string]any{
		"action": "summarize",
		"params": params,
	})
}

func jsonBody(t *testing.T, text string) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &decoded), "result is not JSON: %s", text)
	return decoded
}

func serveJSON(t *testing.T, status int, body string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// --- the projection -------------------------------------------------------

func TestSummarizeProjectsACommentFromItsEventPointer(t *testing.T) {
	const comment = `{
	  "id": 3001,
	  "status": "active",
	  "type": "Comment",
	  "title": "Re: Kickoff",
	  "app_url": "https://3.basecamp.com/999/buckets/77/comments/3001",
	  "content": "<div>Ship it <bc-attachment sgid=\"SGID\"></bc-attachment></div>",
	  "updated_at": "2026-09-16T09:00:00Z",
	  "bucket": {"id": 77, "name": "Connector", "type": "Project"},
	  "creator": {"id": 42, "name": "Ann"},
	  "parent": {"id": 2001, "title": "Kickoff", "type": "Message"}
	}`

	session, log := compositeSession(t, map[string]http.HandlerFunc{
		"/999/comments/": serveJSON(t, http.StatusOK, strings.Replace(comment, "SGID", personSGID(1049715915), 1)),
	})

	text, isError := summarize(t, session, map[string]any{
		"bucket_id":    77,
		"recording_id": 3001,
		"event_type":   "comment.created",
	})
	require.False(t, isError, text)

	summary := jsonBody(t, text)
	assert.Equal(t, "Comment", summary["type"])
	assert.Equal(t, "Re: Kickoff", summary["title"])
	assert.Equal(t, float64(3001), summary["id"])
	assert.Equal(t, []any{float64(1049715915)}, summary["mentioned_person_ids"])
	assert.Contains(t, summary["content"], "Ship it")
	assert.Len(t, log.seen(), 1, "the projection is one typed read")
}

func TestSummarizeRefusesAPointerThatNamesNoType(t *testing.T) {
	session, log := compositeSession(t, nil)

	text, isError := summarize(t, session, map[string]any{"bucket_id": 77, "recording_id": 3001})
	assert.True(t, isError)
	assert.Contains(t, text, "event_type or recording_type")
	assert.Empty(t, log.seen(), "a pointer that cannot be routed costs no request")
}

func TestSummarizeRefusesAnIDPastTheExactJSONRange(t *testing.T) {
	session, log := compositeSession(t, nil)

	text, isError := summarize(t, session, map[string]any{
		"bucket_id":    77,
		"recording_id": 1e17,
		"event_type":   "comment.created",
	})
	assert.True(t, isError)
	assert.Contains(t, text, "recording_id")
	assert.Empty(t, log.seen(), "an id that cannot be carried exactly is never sent")
}

func TestSummarizeRefusesAnEventTypeThatNamesNoRecording(t *testing.T) {
	session, log := compositeSession(t, nil)

	text, isError := summarize(t, session, map[string]any{
		"bucket_id":    77,
		"recording_id": 3001,
		"event_type":   "boost.created",
	})
	require.True(t, isError)
	assert.Equal(t, "no_recording_type", jsonBody(t, text)["error"].(map[string]any)["type"])
	assert.Empty(t, log.seen())
}

// --- the chat line --------------------------------------------------------

const chatLine = `{
  "id": 5005,
  "status": "active",
  "type": "Chat::Lines::RichText",
  "title": "Chat line",
  "app_url": "https://3.basecamp.com/999/buckets/77/chats/900/lines/5005",
  "content": "<div>deploy when green</div>",
  "updated_at": "2026-09-16T09:30:00Z",
  "bucket": {"id": 77, "name": "Connector", "type": "Project"},
  "creator": {"id": 42, "name": "Ann"}
}`

func TestSummarizeFindsAChatLineThroughTheProjectDock(t *testing.T) {
	session, log := compositeSession(t, map[string]http.HandlerFunc{
		"/999/projects/": serveJSON(t, http.StatusOK, `{
		  "id": 77, "status": "active", "name": "Connector",
		  "dock": [{"id": 900, "title": "Campfire", "name": "chat", "enabled": true}]
		}`),
		"/999/chats/900/lines/5005": serveJSON(t, http.StatusOK, chatLine),
	})

	text, isError := summarize(t, session, map[string]any{
		"bucket_id":    77,
		"recording_id": 5005,
		"event_type":   "chat.line.created",
	})
	require.False(t, isError, text)

	summary := jsonBody(t, text)
	assert.Equal(t, "Chat::Lines::RichText", summary["type"])
	assert.Equal(t, float64(900), summary["campfire_id"], "a chat line carries the Campfire it was found under")
	assert.Contains(t, summary["content"], "deploy when green")
	assert.Equal(t, []string{"/999/projects/77", "/999/chats/900/lines/5005"}, log.seen(),
		"the dock answers first, so a project's line costs one project read and one line read")
}

func TestSummarizeFallsBackToTheAccountCampfireListing(t *testing.T) {
	session, log := compositeSession(t, map[string]http.HandlerFunc{
		// Not a project: the dock source has nothing to offer.
		"/999/projects/": serveJSON(t, http.StatusNotFound, `{"error":"not found"}`),
		"/999/chats.json": serveJSON(t, http.StatusOK, `[
		  {"id": 800, "title": "Elsewhere", "bucket": {"id": 12, "name": "Other", "type": "Project"}},
		  {"id": 901, "title": "Ping", "bucket": {"id": 77, "name": "Connector", "type": "Project"}},
		  {"id": 900, "title": "Campfire", "bucket": {"id": 77, "name": "Connector", "type": "Project"}}
		]`),
		"/999/chats/901/lines/5005": serveJSON(t, http.StatusNotFound, `{"error":"not found"}`),
		"/999/chats/900/lines/5005": serveJSON(t, http.StatusOK, chatLine),
	})

	text, isError := summarize(t, session, map[string]any{
		"bucket_id":      77,
		"recording_id":   5005,
		"recording_type": "Chat::Lines::RichText",
	})
	require.False(t, isError, text)

	summary := jsonBody(t, text)
	assert.Equal(t, float64(900), summary["campfire_id"])
	assert.NotContains(t, log.seen(), "/999/chats/800/lines/5005",
		"the listing is filtered to the bucket, so another project's Campfire is never tried")
}

func TestSummarizeReportsUnresolvedOnlyWhenEveryCandidateSaid404(t *testing.T) {
	session, log := compositeSession(t, map[string]http.HandlerFunc{
		"/999/projects/": serveJSON(t, http.StatusOK, `{
		  "id": 77, "status": "active", "name": "Connector",
		  "dock": [{"id": 900, "title": "Campfire", "name": "chat", "enabled": true}]
		}`),
		"/999/chats.json":           serveJSON(t, http.StatusOK, `[]`),
		"/999/chats/900/lines/5005": serveJSON(t, http.StatusNotFound, `{"error":"not found"}`),
	})

	text, isError := summarize(t, session, map[string]any{
		"bucket_id":    77,
		"recording_id": 5005,
		"event_type":   "chat.line.created",
	})
	require.True(t, isError, text)

	failure := jsonBody(t, text)["error"].(map[string]any)
	assert.Equal(t, "recording_unresolved", failure["type"],
		"every visible Campfire answered 404, which is its own verdict")
	assert.Equal(t, []any{float64(900)}, failure["campfire_ids"])
	assert.Equal(t, float64(5005), failure["recording_id"])
	assert.Contains(t, log.seen(), "/999/chats.json", "the second source is consulted before concluding")
}

func TestSummarizeKeepsAFailedCandidateReadApartFromUnresolved(t *testing.T) {
	session, _ := compositeSession(t, map[string]http.HandlerFunc{
		"/999/projects/": serveJSON(t, http.StatusOK, `{
		  "id": 77, "status": "active", "name": "Connector",
		  "dock": [{"id": 900, "title": "Campfire", "name": "chat", "enabled": true}]
		}`),
		"/999/chats/900/lines/5005": serveJSON(t, http.StatusForbidden, `{"error":"forbidden"}`),
	})

	text, isError := summarize(t, session, map[string]any{
		"bucket_id":    77,
		"recording_id": 5005,
		"event_type":   "chat.line.created",
	})
	require.True(t, isError, text)
	assert.NotContains(t, text, "recording_unresolved",
		"a candidate that failed is that read's error, never 'under no Campfire you can see'")
	assert.NotContains(t, text, "campfire_discovery_incomplete")
}

func TestSummarizeRefusesARecordingFromAnotherBucket(t *testing.T) {
	session, _ := compositeSession(t, map[string]http.HandlerFunc{
		"/999/comments/": serveJSON(t, http.StatusOK, `{
		  "id": 3001, "status": "active", "type": "Comment", "title": "Re: Kickoff",
		  "content": "<div>hi</div>", "updated_at": "2026-09-16T09:00:00Z",
		  "bucket": {"id": 4242, "name": "Somewhere else", "type": "Project"}
		}`),
	})

	text, isError := summarize(t, session, map[string]any{
		"bucket_id":    77,
		"recording_id": 3001,
		"event_type":   "comment.created",
	})
	require.True(t, isError, text)

	failure := jsonBody(t, text)["error"].(map[string]any)
	assert.Equal(t, "bucket_mismatch", failure["type"])
	assert.Equal(t, float64(77), failure["bucket_id"])
	assert.Equal(t, float64(4242), failure["found_in_bucket_id"])
}

// --- mentions on create_comment ------------------------------------------

func createComment(t *testing.T, session *mcp.ClientSession, params map[string]any) (string, bool) {
	t.Helper()
	return mcptest.CallText(t, session, "basecamp_messages", map[string]any{
		"action": "create_comment",
		"params": params,
	})
}

func TestCreateCommentExpandsMentionsIntoAttachmentMarkup(t *testing.T) {
	const personID = 1049715915
	var posted map[string]any

	session, log := compositeSession(t, map[string]http.HandlerFunc{
		"/999/people/": serveJSON(t, http.StatusOK, fmt.Sprintf(
			`{"id": %d, "name": "Ann", "attachable_sgid": %q}`, personID, personSGID(personID))),
		"/999/recordings/2001/comments.json": func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &posted))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id": 3002, "type": "Comment", "status": "active"}`))
		},
	})

	text, isError := createComment(t, session, map[string]any{
		"recordingId": 2001,
		"content":     "<div>on it</div>",
		"mentions":    []any{float64(personID)},
	})
	require.False(t, isError, text)

	require.NotNil(t, posted)
	content, _ := posted["content"].(string)
	assert.Contains(t, content, `<bc-attachment sgid="`+personSGID(personID)+`">`,
		"the mention is posted as the markup Basecamp reads")
	assert.Contains(t, content, "on it", "the caller's own content survives")
	assert.NotContains(t, posted, "mentions", "the synthetic parameter never reaches Basecamp")

	seen := log.seen()
	assert.Equal(t, fmt.Sprintf("/999/people/%d", personID), seen[0],
		"the person is read for the attachable_sgid before anything is posted")
	assert.Equal(t, "/999/recordings/2001/comments.json", seen[len(seen)-1])
}

func TestCreateCommentPostsNothingWhenAMentionCannotBeRead(t *testing.T) {
	session, log := compositeSession(t, map[string]http.HandlerFunc{
		"/999/people/": serveJSON(t, http.StatusNotFound, `{"error":"not found"}`),
		"/999/recordings/2001/comments.json": func(w http.ResponseWriter, _ *http.Request) {
			t.Error("a comment must not be posted when a mention cannot be resolved")
			w.WriteHeader(http.StatusCreated)
		},
	})

	text, isError := createComment(t, session, map[string]any{
		"recordingId": 2001,
		"content":     "<div>on it</div>",
		"mentions":    []any{float64(1049715915)},
	})
	assert.True(t, isError, text)
	assert.NotContains(t, log.seen(), "/999/recordings/2001/comments.json")
}

func TestCreateCommentWithoutMentionsIsUntouched(t *testing.T) {
	var posted map[string]any

	session, log := compositeSession(t, map[string]http.HandlerFunc{
		"/999/recordings/2001/comments.json": func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &posted))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id": 3002, "type": "Comment", "status": "active"}`))
		},
	})

	text, isError := createComment(t, session, map[string]any{
		"recordingId": 2001,
		"content":     "<div>plain</div>",
	})
	require.False(t, isError, text)
	assert.Equal(t, "<div>plain</div>", posted["content"])
	assert.Len(t, log.seen(), 1, "no mentions, no person reads")
}

func TestCreateCommentRefusesAMalformedMentionsParameter(t *testing.T) {
	session, log := compositeSession(t, nil)

	text, isError := createComment(t, session, map[string]any{
		"recordingId": 2001,
		"content":     "<div>hi</div>",
		"mentions":    "1049715915",
	})
	assert.True(t, isError, text)
	assert.Contains(t, text, "mentions")
	assert.Empty(t, log.seen())
}

// --- read-only mode -------------------------------------------------------

func TestReadOnlyModeKeepsSummarizeAndDropsCreateComment(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(upstream.Close)

	srv, err := New(newTestAPI(upstream), Config{ReadOnly: true})
	require.NoError(t, err)
	session := mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))

	tools := mcptest.ListTools(t, session)
	require.Contains(t, tools, "basecamp_recordings", "a read-only composite survives read-only mode")
	assert.Contains(t, tools["basecamp_recordings"].Description, "summarize")

	text, isError := createComment(t, session, map[string]any{"recordingId": 2001, "content": "<div>no</div>"})
	assert.True(t, isError, text)
}
