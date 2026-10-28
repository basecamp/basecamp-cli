package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const timesheetEntryFixture = `{
  "id": 1069480283,
  "status": "active",
  "type": "Timesheet::Entry",
  "title": "Timesheet entry",
  "date": "2026-10-26",
  "hours": "1.5",
  "description": "Review",
  "created_at": "2026-10-26T10:00:00Z",
  "updated_at": "2026-10-26T10:00:00Z",
  "parent": {"id": 789, "title": "Plan the offsite", "type": "Todo"},
  "bucket": {"id": 89, "name": "Offsite", "type": "Project"},
  "creator": {"id": 7, "name": "Victor Cooper"},
  "person": {"id": 7, "name": "Victor Cooper"}
}`

func timesheetEntryPath(id int64) string {
	return fmt.Sprintf("/99999/timesheet_entries/%d", id)
}

func timesheetEntriesPath(recordingID int64) string {
	return fmt.Sprintf("/99999/recordings/%d/timesheet/entries.json", recordingID)
}

func runTimesheet(t *testing.T, routes []stubRoute, args ...string) (*recordingTransport, string, json.RawMessage, error) {
	t.Helper()
	app, transport, out := setupPersonalFeedApp(t, routes...)
	err := executeRecordingCommand(NewTimesheetCmd(), app, args...)
	if err != nil {
		return transport, "", nil, err
	}
	data, summary := subtaskEnvelope(t, out)
	return transport, summary, data, nil
}

func TestTimesheetEntryShowGetsTheEntry(t *testing.T) {
	transport, summary, data, err := runTimesheet(t,
		[]stubRoute{{method: http.MethodGet, path: timesheetEntryPath(1069480283), status: http.StatusOK, body: timesheetEntryFixture}},
		"entry", "show", "1069480283")
	require.NoError(t, err)

	call := transport.last(t)
	assert.Equal(t, http.MethodGet, call.Method)
	assert.Equal(t, timesheetEntryPath(1069480283), call.Path)
	assert.Equal(t, "Timesheet entry #1069480283: 1.5h on 2026-10-26 for Plan the offsite", summary)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(data, &entry))
	assert.Equal(t, "1.5", entry["hours"])
}

// An entry's own URL names it, in its app form and both API forms.
func TestTimesheetEntryShowAcceptsTheEntryURL(t *testing.T) {
	for _, url := range []string{
		"https://3.basecamp.com/99999/projects/89/timesheet/entries/1069480283",
		"https://3.basecampapi.com/99999/projects/89/timesheet/entries/1069480283.json",
		"https://3.basecampapi.com/99999/timesheet_entries/1069480283.json",
	} {
		transport, _, _, err := runTimesheet(t,
			[]stubRoute{{method: http.MethodGet, path: timesheetEntryPath(1069480283), status: http.StatusOK, body: timesheetEntryFixture}},
			"entry", "show", url)
		require.NoError(t, err, url)
		assert.Equal(t, timesheetEntryPath(1069480283), transport.last(t).Path, url)
	}
}

func TestTimesheetEntryCreatePostsDateHoursDescriptionAndPerson(t *testing.T) {
	transport, summary, _, err := runTimesheet(t,
		[]stubRoute{{method: http.MethodPost, path: timesheetEntriesPath(789), status: http.StatusCreated, body: timesheetEntryFixture}},
		"entry", "create", "789", "--hours", "1:30", "--date", "2026-10-26", "--description", "Review", "--person", "7")
	require.NoError(t, err)

	call := transport.last(t)
	assert.Equal(t, http.MethodPost, call.Method)
	assert.Equal(t, timesheetEntriesPath(789), call.Path)
	assert.Equal(t, map[string]any{
		"date": "2026-10-26", "hours": "1:30", "description": "Review", "person_id": float64(7),
	}, decodeSubtaskBody(t, call))
	assert.Equal(t, "Logged timesheet entry #1069480283: 1.5h on 2026-10-26 for Plan the offsite", summary)
}

// --date defaults to today, and unset optional fields are not sent.
func TestTimesheetEntryCreateDefaultsToTodayAndOmitsUnset(t *testing.T) {
	transport, _, _, err := runTimesheet(t,
		[]stubRoute{{method: http.MethodPost, path: timesheetEntriesPath(789), status: http.StatusCreated, body: timesheetEntryFixture}},
		"entry", "create", "https://3.basecamp.com/99999/buckets/89/todos/789", "--hours", "2")
	require.NoError(t, err)

	assert.Equal(t, map[string]any{
		"date": time.Now().Format("2006-01-02"), "hours": "2",
	}, decodeSubtaskBody(t, transport.last(t)))
}

// "me" is resolved to the caller's person id before the entry is created.
func TestTimesheetEntryCreateResolvesPersonMe(t *testing.T) {
	transport, _, _, err := runTimesheet(t,
		[]stubRoute{
			{method: http.MethodGet, path: "/99999/my/profile.json", status: http.StatusOK, body: `{"id": 42, "name": "Me"}`},
			{method: http.MethodPost, path: timesheetEntriesPath(789), status: http.StatusCreated, body: timesheetEntryFixture},
		},
		"entry", "create", "789", "--hours", "1", "--person", "me")
	require.NoError(t, err)

	assert.Equal(t, float64(42), decodeSubtaskBody(t, transport.last(t))["person_id"])
}

func TestTimesheetEntryCreateRefusesBadInput(t *testing.T) {
	for _, args := range [][]string{
		{"entry", "create", "789"},
		{"entry", "create", "789", "--hours", ""},
		{"entry", "create", "789", "--hours", "an hour"},
		{"entry", "create", "789", "--hours", "1:75"},
		{"entry", "create", "789", "--hours", "1", "--date", "someday"},
		{"entry", "create", "789", "--hours", "1", "--date", "2026-13-40"},
		{"entry", "create", "789", "--hours", "1", "--person", ""},
		{"entry", "create", "not-an-id", "--hours", "1"},
		// A project's timesheet URL carries the project id, not the
		// timesheet recording's.
		{"entry", "create", "https://3.basecamp.com/99999/projects/89/timesheet", "--hours", "1"},
		// Neither does a project's URL, or a listing's.
		{"entry", "create", "https://3.basecamp.com/99999/projects/89", "--hours", "1"},
		{"entry", "create", "https://3.basecamp.com/99999/buckets/89/todolists", "--hours", "1"},
		// A person's URL parses to an id, but not a recording's.
		{"entry", "create", "https://3.basecamp.com/99999/people/789", "--hours", "1"},
		// Without a scheme there is no host to trust.
		{"entry", "create", "//evil.example/11111/buckets/89/todos/789", "--hours", "1"},
		{"entry", "create", "3.basecamp.com/99999/buckets/89/todos/789", "--hours", "1"},
	} {
		transport, _, _, err := runTimesheet(t, nil, args...)
		requireBookmarksUsageError(t, err)
		assert.Empty(t, transport.recorded(), "no request for %v", args)
	}
}

func TestTimesheetEntryUpdateSendsOnlyWhatChanged(t *testing.T) {
	transport, summary, _, err := runTimesheet(t,
		[]stubRoute{{method: http.MethodPut, path: timesheetEntryPath(1069480283), status: http.StatusOK, body: timesheetEntryFixture}},
		"entry", "update", "1069480283", "--hours", "2.5")
	require.NoError(t, err)

	call := transport.last(t)
	assert.Equal(t, http.MethodPut, call.Method)
	assert.Equal(t, timesheetEntryPath(1069480283), call.Path)
	assert.Equal(t, map[string]any{"hours": "2.5"}, decodeSubtaskBody(t, call))
	assert.Equal(t, "Updated timesheet entry #1069480283: 1.5h on 2026-10-26 for Plan the offsite", summary)
}

func TestTimesheetEntryUpdateSendsEveryField(t *testing.T) {
	transport, _, _, err := runTimesheet(t,
		[]stubRoute{{method: http.MethodPut, path: timesheetEntryPath(1069480283), status: http.StatusOK, body: timesheetEntryFixture}},
		"entry", "update", "1069480283", "--hours", "3", "--date", "2026-10-25", "--description", "Call", "--person", "8")
	require.NoError(t, err)

	assert.Equal(t, map[string]any{
		"date": "2026-10-25", "hours": "3", "description": "Call", "person_id": float64(8),
	}, decodeSubtaskBody(t, transport.last(t)))
}

func TestTimesheetEntryUpdateRefusesBadInput(t *testing.T) {
	for _, args := range [][]string{
		{"entry", "update", "1069480283"},
		{"entry", "update", "1069480283", "--hours", "x"},
		{"entry", "update", "1069480283", "--date", "someday"},
		{"entry", "update", "1069480283", "--description", ""},
		{"entry", "update", "1069480283", "--person", ""},
		{"entry", "update", "not-an-id", "--hours", "1"},
	} {
		transport, _, _, err := runTimesheet(t, nil, args...)
		requireBookmarksUsageError(t, err)
		assert.Empty(t, transport.recorded(), "no request for %v", args)
	}
}

func TestTimesheetEntryDeleteDestroysTheEntry(t *testing.T) {
	transport, summary, data, err := runTimesheet(t,
		[]stubRoute{{method: http.MethodDelete, path: timesheetEntryPath(1069480283), status: http.StatusNoContent}},
		"entry", "delete", "1069480283", "--force")
	require.NoError(t, err)

	call := transport.last(t)
	assert.Equal(t, http.MethodDelete, call.Method)
	assert.Equal(t, timesheetEntryPath(1069480283), call.Path)
	assert.Equal(t, "Deleted timesheet entry #1069480283", summary)
	assert.JSONEq(t, `{"id": 1069480283, "deleted": true}`, string(data))
}

// A permanent delete in machine-output mode has nobody to confirm it, so it
// needs --force and is refused before any request without it.
func TestTimesheetEntryDeleteNeedsForceWhenNothingCanConfirm(t *testing.T) {
	transport, _, _, err := runTimesheet(t, nil, "entry", "delete", "1069480283")
	outErr := requireBookmarksUsageError(t, err)
	assert.Contains(t, outErr.Message, "--force")
	assert.Empty(t, transport.recorded())
}

// A URL that is not an entry's own names something else — most often the item
// the time is logged against — and must never be read as an entry id, least
// of all by delete.
func TestTimesheetEntryVerbsRefuseOtherURLs(t *testing.T) {
	for _, url := range []string{
		"https://3.basecamp.com/99999/buckets/89/todos/789",
		"https://3.basecamp.com/99999/projects/89/timesheet",
		"https://example.com/not/basecamp",
	} {
		for _, verb := range [][]string{{"show"}, {"update", "--hours", "1"}, {"delete", "--force"}} {
			args := append([]string{"entry", verb[0], url}, verb[1:]...)
			transport, _, _, err := runTimesheet(t, nil, args...)
			requireBookmarksUsageError(t, err)
			assert.Empty(t, transport.recorded(), "no request for %v", args)
		}
	}
}

func TestTimesheetEntryVerbsRejectANonID(t *testing.T) {
	for _, args := range [][]string{
		{"entry", "show", "not-an-id"},
		{"entry", "show", "0"},
		{"entry", "delete", "12abc", "--force"},
	} {
		transport, _, _, err := runTimesheet(t, nil, args...)
		requireBookmarksUsageError(t, err)
		assert.Empty(t, transport.recorded(), "no request for %v", args)
	}
}

// A refusal from Basecamp (403 when the caller may not remove the entry) is
// returned as an error, not reported as a delete.
func TestTimesheetEntryDeleteReportsARefusal(t *testing.T) {
	_, _, _, err := runTimesheet(t,
		[]stubRoute{{method: http.MethodDelete, path: timesheetEntryPath(1069480283), status: http.StatusForbidden, body: `{"error":"forbidden"}`}},
		"entry", "delete", "1069480283", "--force")
	require.Error(t, err)
}

// A pasted URL acts only on the account it names, on a Basecamp host: a
// look-alike host, or a URL from another account than the configured one, is
// refused before any request, on every verb.
func TestTimesheetEntryVerbsHoldAURLToItsHostAndAccount(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"show untrusted host", []string{"entry", "show", "https://evil.example/99999/timesheet_entries/1069480283.json"}},
		{"update untrusted host", []string{"entry", "update", "https://evil.example/99999/projects/89/timesheet/entries/1069480283", "--hours", "1"}},
		{"delete untrusted host", []string{"entry", "delete", "https://evil.example/99999/timesheet_entries/1069480283", "--force"}},
		{"create untrusted host", []string{"entry", "create", "https://evil.example/99999/buckets/89/todos/789", "--hours", "1"}},
		{"show other account", []string{"entry", "show", "https://3.basecamp.com/11111/projects/89/timesheet/entries/1069480283"}},
		{"update other account", []string{"entry", "update", "https://3.basecampapi.com/11111/timesheet_entries/1069480283.json", "--hours", "1"}},
		{"delete other account", []string{"entry", "delete", "https://3.basecamp.com/11111/projects/89/timesheet/entries/1069480283", "--force"}},
		{"create other account", []string{"entry", "create", "https://3.basecamp.com/11111/buckets/89/todos/789", "--hours", "1"}},
		{"show scheme-less", []string{"entry", "show", "//evil.example/11111/timesheet_entries/1069480283"}},
		{"delete scheme-less", []string{"entry", "delete", "3.basecamp.com/99999/timesheet_entries/1069480283", "--force"}},
		// A path written after a fragment or query is not the URL's path.
		{"update path in fragment", []string{"entry", "update", "https://3.basecamp.com#x/99999/timesheet_entries/1069480283", "--hours", "1"}},
		{"delete path in query", []string{"entry", "delete", "https://3.basecamp.com/?x=/99999/timesheet_entries/1069480283", "--force"}},
		{"create path in fragment", []string{"entry", "create", "https://3.basecamp.com#x/99999/buckets/89/todos/789", "--hours", "1"}},
		{"create userinfo", []string{"entry", "create", "https://evil.example@3.basecamp.com/99999/buckets/89/todos/789", "--hours", "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport, _, _, err := runTimesheet(t, nil, tc.args...)
			requireBookmarksUsageError(t, err)
			assert.Empty(t, transport.recorded())
		})
	}
}

// The delete breadcrumb is something its reader can run: machine output cannot
// answer a confirmation, so the JSON a test renders carries --force.
func TestTimesheetEntryDeleteBreadcrumbCarriesForceForMachineOutput(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t,
		stubRoute{method: http.MethodGet, path: timesheetEntryPath(1069480283), status: http.StatusOK, body: timesheetEntryFixture})
	app.Flags.Hints = true // breadcrumbs are stripped from the envelope without this
	require.NoError(t, executeRecordingCommand(NewTimesheetCmd(), app, "entry", "show", "1069480283"))

	var envelope struct {
		Breadcrumbs []struct {
			Action string `json:"action"`
			Cmd    string `json:"cmd"`
		} `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope), out.String())
	var deleteCmd string
	for _, b := range envelope.Breadcrumbs {
		if b.Action == "delete" {
			deleteCmd = b.Cmd
		}
	}
	assert.Equal(t, "basecamp timesheet entry delete 1069480283 --force", deleteCmd)
}

// Account ids compare as numbers, so a zero-padded URL account is the
// configured one.
func TestTimesheetEntryAcceptsAZeroPaddedAccountInAURL(t *testing.T) {
	transport, _, _, err := runTimesheet(t,
		[]stubRoute{{method: http.MethodGet, path: timesheetEntryPath(1069480283), status: http.StatusOK, body: timesheetEntryFixture}},
		"entry", "show", "https://3.basecamp.com/099999/projects/89/timesheet/entries/1069480283")
	require.NoError(t, err)
	assert.Equal(t, timesheetEntryPath(1069480283), transport.last(t).Path)
}

// An account adopted from a pasted URL lasts only for this run, so the
// follow-ups name it; otherwise a pasted update or delete would act in
// whatever account the next run resolves.
func TestTimesheetEntryBreadcrumbsCarryAnAccountAdoptedFromTheURL(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t,
		stubRoute{method: http.MethodGet, path: timesheetEntryPath(1069480283), status: http.StatusOK, body: timesheetEntryFixture})
	app.Config.AccountID = ""
	app.Flags.Hints = true
	require.NoError(t, executeRecordingCommand(NewTimesheetCmd(), app, "entry", "show",
		"https://3.basecamp.com/99999/projects/89/timesheet/entries/1069480283"))

	var envelope struct {
		Breadcrumbs []struct {
			Action string `json:"action"`
			Cmd    string `json:"cmd"`
		} `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope), out.String())
	got := map[string]string{}
	for _, b := range envelope.Breadcrumbs {
		got[b.Action] = b.Cmd
	}
	assert.Equal(t, "basecamp timesheet entry update 1069480283 --hours <hours> --account 99999", got["update"])
	assert.Equal(t, "basecamp timesheet entry delete 1069480283 --account 99999 --force", got["delete"])
	assert.Equal(t, "basecamp timesheet item 789 --account 99999", got["item"])
}
