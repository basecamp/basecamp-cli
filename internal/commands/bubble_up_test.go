package commands

import (
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
		"https://3.basecamp.com/1234567/buckets/89/todos/42"))

	assert.Equal(t, bubbleUpRecordingPath(42), transport.last(t).Path)
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

	err := executeRecordingCommand(NewBubbleUpCmd(), app, "add", "not-an-id")
	requireBookmarksUsageError(t, err)
}
