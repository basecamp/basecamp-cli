package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bubbleUpRecordingPath(id int64) string {
	return fmt.Sprintf("/99999/recordings/%d/bubble_up.json", id)
}

func bubbleUpRoute(id int64, method string) stubRoute {
	return stubRoute{
		method: method,
		path:   bubbleUpRecordingPath(id),
		status: http.StatusNoContent,
		body:   "",
	}
}

// Default add bubbles up now: bc3 requires a value for `at`, so the command
// sends "now" rather than omitting it.
func TestBubbleUpAddSendsNowByDefault(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, bubbleUpRoute(42, http.MethodPost))

	require.NoError(t, executeRecordingCommand(NewBubbleUpCmd(), app, "add", "42"))

	call := transport.last(t)
	assert.Equal(t, http.MethodPost, call.Method)
	assert.Equal(t, bubbleUpRecordingPath(42), call.Path)

	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(call.Body), &body))
	assert.Equal(t, "now", body["at"])
}

// --at schedules: the keyword reaches the wire verbatim.
func TestBubbleUpAddSchedulesWithAt(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, bubbleUpRoute(42, http.MethodPost))

	require.NoError(t, executeRecordingCommand(NewBubbleUpCmd(), app, "add", "42", "--at", "tomorrow"))

	call := transport.last(t)
	assert.Equal(t, http.MethodPost, call.Method)

	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(call.Body), &body))
	assert.Equal(t, "tomorrow", body["at"])
}

func TestBubbleUpAddAcceptsAURL(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, bubbleUpRoute(42, http.MethodPost))

	require.NoError(t, executeRecordingCommand(NewBubbleUpCmd(), app, "add",
		"https://3.basecamp.com/99999/buckets/89/todos/42"))

	assert.Equal(t, bubbleUpRecordingPath(42), transport.last(t).Path)
}

// bubbleUpData reads the single-object success envelope the add/remove verbs emit.
func bubbleUpData(t *testing.T, out *bytes.Buffer) map[string]any {
	t.Helper()
	var envelope struct {
		Data    map[string]any `json:"data"`
		Summary string         `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	return map[string]any{"data": envelope.Data, "summary": envelope.Summary}
}

// A scheduled add reports bubbled_up:false (only in the scheduled set) and a
// scheduling summary.
func TestBubbleUpAddScheduledReportsNotYetBubbled(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t, bubbleUpRoute(42, http.MethodPost))

	require.NoError(t, executeRecordingCommand(NewBubbleUpCmd(), app, "add", "42", "--at", "tomorrow"))

	env := bubbleUpData(t, out)
	data := env["data"].(map[string]any)
	assert.Equal(t, false, data["bubbled_up"])
	assert.Equal(t, "tomorrow", data["at"])
	assert.Equal(t, "Scheduled recording 42 to bubble up tomorrow", env["summary"])
}

// An explicit --at now is immediate, matching the omitted default: bubbled_up:true
// and a "Bubbled up" summary, not a scheduling one.
func TestBubbleUpAddExplicitNowReportsImmediate(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t, bubbleUpRoute(42, http.MethodPost))

	require.NoError(t, executeRecordingCommand(NewBubbleUpCmd(), app, "add", "42", "--at", "now"))

	env := bubbleUpData(t, out)
	data := env["data"].(map[string]any)
	assert.Equal(t, true, data["bubbled_up"])
	assert.Equal(t, "Bubbled up recording 42", env["summary"])
}

// A malformed --at is a local usage error before any request is issued.
func TestBubbleUpAddRejectsInvalidAt(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t)

	err := executeRecordingCommand(NewBubbleUpCmd(), app, "add", "42", "--at", "tomorow")
	requireBookmarksUsageError(t, err)
	assert.Empty(t, transport.recorded(), "no request should be made for an invalid --at")
}

// A non-positive id is rejected as a usage error, not sent to the API.
func TestBubbleUpAddRejectsNonPositiveID(t *testing.T) {
	app, _, _ := setupPersonalFeedApp(t)

	err := executeRecordingCommand(NewBubbleUpCmd(), app, "add", "0")
	requireBookmarksUsageError(t, err)
}

func TestBubbleUpRemoveDeletesTheBubbleUp(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, bubbleUpRoute(42, http.MethodDelete))

	require.NoError(t, executeRecordingCommand(NewBubbleUpCmd(), app, "remove", "42"))

	call := transport.last(t)
	assert.Equal(t, http.MethodDelete, call.Method)
	assert.Equal(t, bubbleUpRecordingPath(42), call.Path)
}

func TestBubbleUpVerbsRejectANonID(t *testing.T) {
	app, _, _ := setupPersonalFeedApp(t)

	for _, verb := range []string{"add", "remove"} {
		err := executeRecordingCommand(NewBubbleUpCmd(), app, verb, "not-an-id")
		requireBookmarksUsageError(t, err)
	}
}
