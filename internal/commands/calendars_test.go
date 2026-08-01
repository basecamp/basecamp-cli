package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func calendarPath(id int64) string {
	return fmt.Sprintf("/99999/calendars/%d", id)
}

func calendarRoute(method string, id int64, color string) stubRoute {
	return stubRoute{
		method: method,
		path:   calendarPath(id),
		status: http.StatusOK,
		body: fmt.Sprintf(`{
			"id": %d, "type": "Calendar", "name": "Team calendar", "color": %q,
			"created_at": "2026-07-01T10:00:00.000Z",
			"updated_at": "2026-07-01T10:00:00.000Z",
			"url": "", "app_url": "", "schedule_url": ""
		}`, id, color),
	}
}

func TestCalendarsShowFetchesTheCalendar(t *testing.T) {
	app, transport, out := setupPersonalFeedApp(t, calendarRoute(http.MethodGet, 12345, "blue"))

	require.NoError(t, executeRecordingCommand(NewCalendarsCmd(), app, "show", "12345"))

	assert.Equal(t, calendarPath(12345), transport.last(t).Path)

	var envelope struct {
		Summary string `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	assert.Equal(t, "Team calendar (blue)", envelope.Summary)
}

// There is no index endpoint, so pasting a URL is the realistic way to name a
// calendar — the discovery path the group depends on.
func TestCalendarsShowAcceptsAURL(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, calendarRoute(http.MethodGet, 12345, "blue"))

	require.NoError(t, executeRecordingCommand(NewCalendarsCmd(), app,
		"show", "https://3.basecamp.com/1234567/calendars/12345"))

	assert.Equal(t, calendarPath(12345), transport.last(t).Path)
}

func TestCalendarsUpdateSendsTheColor(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, calendarRoute(http.MethodPut, 12345, "aqua"))

	require.NoError(t, executeRecordingCommand(NewCalendarsCmd(), app, "update", "12345", "--color", "aqua"))

	call := transport.last(t)
	assert.Equal(t, http.MethodPut, call.Method)
	assert.Equal(t, calendarPath(12345), call.Path)
	assert.Contains(t, call.Body, `"color":"aqua"`)
}

// The SDK at v0.12.0 cannot carry the server's field message back: its error
// parser reads only error/error_description, so a 422 whose body names the
// color degrades to a bare "validation error". Validating here is what makes
// the failure actionable, so it must happen before any request is issued.
func TestCalendarsUpdateRejectsAnUnknownColorWithoutARequest(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, calendarRoute(http.MethodPut, 12345, "blue"))

	err := executeRecordingCommand(NewCalendarsCmd(), app, "update", "12345", "--color", "chartreuse")

	outErr := requireBookmarksUsageError(t, err)
	assert.Contains(t, outErr.Message, "chartreuse")
	assert.Contains(t, outErr.Hint, "blue", "the hint must name the alternatives")
	assert.Empty(t, transport.recorded(), "an invalid color must not reach the server")
}

func TestCalendarsUpdateRequiresAColor(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, calendarRoute(http.MethodPut, 12345, "blue"))

	err := executeRecordingCommand(NewCalendarsCmd(), app, "update", "12345")

	outErr := requireBookmarksUsageError(t, err)
	assert.Contains(t, outErr.Message, "--color")
	assert.Empty(t, transport.recorded())
}

func TestCalendarsAcceptsEveryDocumentedColor(t *testing.T) {
	for _, color := range calendarColors {
		t.Run(color, func(t *testing.T) {
			app, transport, _ := setupPersonalFeedApp(t, calendarRoute(http.MethodPut, 12345, color))

			require.NoError(t, executeRecordingCommand(NewCalendarsCmd(), app, "update", "12345", "--color", color))

			assert.NotEmpty(t, transport.recorded())
		})
	}
}

func TestCalendarsRejectANonID(t *testing.T) {
	for _, args := range [][]string{
		{"show", "not-an-id"},
		{"update", "not-an-id", "--color", "blue"},
	} {
		t.Run(args[0], func(t *testing.T) {
			app, transport, _ := setupPersonalFeedApp(t)

			err := executeRecordingCommand(NewCalendarsCmd(), app, args...)

			outErr := requireBookmarksUsageError(t, err)
			assert.Contains(t, outErr.Hint, "calendar id")
			assert.Empty(t, transport.recorded())
		})
	}
}
