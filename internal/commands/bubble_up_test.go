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

// A scheduled add reports the requested timing, not a resulting state: bc3
// returns a bare 204 and add is idempotent, so the command must not claim the
// bubble-up now sits in the scheduled set (it may have been a no-op over an
// existing immediate bubble-up). It echoes the requested `at` and no
// bubbled_up state assertion.
func TestBubbleUpAddScheduledReportsRequestedTiming(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t, bubbleUpRoute(42, http.MethodPost))

	require.NoError(t, executeRecordingCommand(NewBubbleUpCmd(), app, "add", "42", "--at", "tomorrow"))

	env := bubbleUpData(t, out)
	data := env["data"].(map[string]any)
	assert.NotContains(t, data, "bubbled_up", "add must not assert a resulting bubbled-up state")
	assert.Equal(t, "tomorrow", data["at"])
	assert.Equal(t, "Requested bubble-up for recording 42 at tomorrow", env["summary"])
}

// An immediate add (the omitted default, or an explicit --at now) reports the
// request without a resulting-state claim.
func TestBubbleUpAddImmediateReportsRequest(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t, bubbleUpRoute(42, http.MethodPost))

	require.NoError(t, executeRecordingCommand(NewBubbleUpCmd(), app, "add", "42", "--at", "now"))

	env := bubbleUpData(t, out)
	data := env["data"].(map[string]any)
	assert.NotContains(t, data, "bubbled_up", "add must not assert a resulting bubbled-up state")
	assert.Equal(t, "now", data["at"])
	assert.Equal(t, "Requested bubble-up for recording 42", env["summary"])
}

// A calendar date reaches the wire verbatim.
func TestBubbleUpAddAcceptsACalendarDate(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, bubbleUpRoute(42, http.MethodPost))

	require.NoError(t, executeRecordingCommand(NewBubbleUpCmd(), app, "add", "42", "--at", "2026-09-10"))

	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(transport.last(t).Body), &body))
	assert.Equal(t, "2026-09-10", body["at"])
}

// A malformed --at, and a timestamp bc3 would truncate to a date, are both local
// usage errors before any request is issued.
func TestBubbleUpAddRejectsInvalidAt(t *testing.T) {
	for _, bad := range []string{"tomorow", "2026-09-10T09:00:00Z", "20260910"} {
		app, transport, _ := setupPersonalFeedApp(t)

		err := executeRecordingCommand(NewBubbleUpCmd(), app, "add", "42", "--at", bad)
		requireBookmarksUsageError(t, err)
		assert.Empty(t, transport.recorded(), "no request should be made for --at %q", bad)
	}
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
