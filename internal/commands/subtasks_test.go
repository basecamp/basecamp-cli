package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const subtaskFixture = `{
  "id": 456,
  "status": "active",
  "title": "Book the venue",
  "type": "Kanban::Step",
  "position": 1,
  "completed": false,
  "due_on": null,
  "parent": {"id": 123, "title": "Plan the offsite", "type": "Todo"},
  "bucket": {"id": 89, "name": "Offsite", "type": "Project"},
  "assignees": [],
  "completion_url": "https://3.basecampapi.com/99999/subtasks/456/completion.json"
}`

func subtasksListPath(parentID int64) string {
	return fmt.Sprintf("/99999/recordings/%d/subtasks.json", parentID)
}

func subtaskPath(id int64) string {
	return fmt.Sprintf("/99999/subtasks/%d", id)
}

func subtaskRoute(method, path string, status int, body string) stubRoute {
	return stubRoute{method: method, path: path, status: status, body: body}
}

// subtaskEnvelope reads the success envelope a subtasks verb emits.
func subtaskEnvelope(t *testing.T, out *bytes.Buffer) (data json.RawMessage, summary string) {
	t.Helper()
	var envelope struct {
		Data    json.RawMessage `json:"data"`
		Summary string          `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope), out.String())
	return envelope.Data, envelope.Summary
}

func decodeSubtaskBody(t *testing.T, call recordedCall) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(call.Body), &body), call.Body)
	return body
}

func TestSubtasksListReadsTheParentsSubtasks(t *testing.T) {
	app, transport, out := setupPersonalFeedApp(t,
		subtaskRoute(http.MethodGet, subtasksListPath(123), http.StatusOK, "["+subtaskFixture+"]"))

	require.NoError(t, executeRecordingCommand(NewSubtasksCmd(), app, "list", "123"))

	call := transport.last(t)
	assert.Equal(t, http.MethodGet, call.Method)
	assert.Equal(t, subtasksListPath(123), call.Path)

	data, summary := subtaskEnvelope(t, out)
	var subtasks []map[string]any
	require.NoError(t, json.Unmarshal(data, &subtasks))
	require.Len(t, subtasks, 1)
	assert.Equal(t, "Book the venue", subtasks[0]["title"])
	assert.Equal(t, "1 subtasks on #123", summary)
}

// A to-do or card URL names the parent by its path, not by a fragment.
func TestSubtasksListAcceptsAParentURL(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t,
		subtaskRoute(http.MethodGet, subtasksListPath(123), http.StatusOK, "[]"))

	require.NoError(t, executeRecordingCommand(NewSubtasksCmd(), app, "list",
		"https://3.basecamp.com/99999/buckets/89/card_tables/cards/123"))

	assert.Equal(t, subtasksListPath(123), transport.last(t).Path)
}

// An empty list is an empty array, not null.
func TestSubtasksListEmptyIsAnArray(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t,
		subtaskRoute(http.MethodGet, subtasksListPath(123), http.StatusOK, "[]"))

	require.NoError(t, executeRecordingCommand(NewSubtasksCmd(), app, "list", "123"))

	data, _ := subtaskEnvelope(t, out)
	assert.JSONEq(t, "[]", string(data))
}

func TestSubtasksListPageSelectsOnePage(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t,
		subtaskRoute(http.MethodGet, subtasksListPath(123), http.StatusOK, "[]"))

	require.NoError(t, executeRecordingCommand(NewSubtasksCmd(), app, "list", "123", "--page", "2"))

	assert.Equal(t, "page=2", transport.last(t).Query)
}

func TestSubtasksListRejectsConflictingPagination(t *testing.T) {
	for _, args := range [][]string{
		{"list", "123", "--all", "--limit", "5"},
		{"list", "123", "--page", "2", "--all"},
		{"list", "123", "--page", "0"},
		{"list", "123", "--limit", "-1"},
	} {
		app, transport, _ := setupPersonalFeedApp(t)

		err := executeRecordingCommand(NewSubtasksCmd(), app, args...)
		requireBookmarksUsageError(t, err)
		assert.Empty(t, transport.recorded(), "no request for %v", args)
	}
}

func TestSubtasksShowGetsTheSubtask(t *testing.T) {
	app, transport, out := setupPersonalFeedApp(t,
		subtaskRoute(http.MethodGet, subtaskPath(456), http.StatusOK, subtaskFixture))

	require.NoError(t, executeRecordingCommand(NewSubtasksCmd(), app, "show", "456"))

	assert.Equal(t, subtaskPath(456), transport.last(t).Path)
	_, summary := subtaskEnvelope(t, out)
	assert.Equal(t, "Subtask #456: Book the venue", summary)
}

// A subtask's app URL is its parent's with a #__recording_<id> fragment; the
// fragment names the subtask.
func TestSubtasksShowAcceptsTheSubtaskURL(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t,
		subtaskRoute(http.MethodGet, subtaskPath(456), http.StatusOK, subtaskFixture))

	require.NoError(t, executeRecordingCommand(NewSubtasksCmd(), app, "show",
		"https://3.basecamp.com/99999/buckets/89/todos/123#__recording_456"))

	assert.Equal(t, subtaskPath(456), transport.last(t).Path)
}

func TestSubtasksCreatePostsTitleDueAndAssignees(t *testing.T) {
	app, transport, out := setupPersonalFeedApp(t,
		subtaskRoute(http.MethodPost, subtasksListPath(123), http.StatusCreated, subtaskFixture))

	require.NoError(t, executeRecordingCommand(NewSubtasksCmd(), app, "create", "123", "Book the venue",
		"--due", "2026-10-02", "--assignees", "7,8"))

	call := transport.last(t)
	assert.Equal(t, http.MethodPost, call.Method)
	assert.Equal(t, subtasksListPath(123), call.Path)

	body := decodeSubtaskBody(t, call)
	assert.Equal(t, "Book the venue", body["title"])
	assert.Equal(t, "2026-10-02", body["due_on"])
	assert.Equal(t, []any{float64(7), float64(8)}, body["assignee_ids"])

	_, summary := subtaskEnvelope(t, out)
	assert.Equal(t, "Created subtask #456: Book the venue", summary)
}

func TestSubtasksCreateOmitsUnsetFields(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t,
		subtaskRoute(http.MethodPost, subtasksListPath(123), http.StatusCreated, subtaskFixture))

	require.NoError(t, executeRecordingCommand(NewSubtasksCmd(), app, "create", "123", "Book the venue"))

	body := decodeSubtaskBody(t, transport.last(t))
	assert.Equal(t, map[string]any{"title": "Book the venue"}, body)
}

func TestSubtasksCreateRejectsAnEmptyTitle(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t)

	err := executeRecordingCommand(NewSubtasksCmd(), app, "create", "123", "")
	requireBookmarksUsageError(t, err)
	assert.Empty(t, transport.recorded())
}

func TestSubtasksUpdateSendsOnlyWhatChanged(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t,
		subtaskRoute(http.MethodPut, subtaskPath(456), http.StatusOK, subtaskFixture))

	require.NoError(t, executeRecordingCommand(NewSubtasksCmd(), app, "update", "456", "Book the bigger venue"))

	call := transport.last(t)
	assert.Equal(t, http.MethodPut, call.Method)
	assert.Equal(t, subtaskPath(456), call.Path)
	assert.Equal(t, map[string]any{"title": "Book the bigger venue"}, decodeSubtaskBody(t, call))
}

// --no-due and --no-assignees clear: an empty due_on and an empty assignee
// list are sent, not omitted.
func TestSubtasksUpdateClearsDueAndAssignees(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t,
		subtaskRoute(http.MethodPut, subtaskPath(456), http.StatusOK, subtaskFixture))

	require.NoError(t, executeRecordingCommand(NewSubtasksCmd(), app, "update", "456", "--no-due", "--no-assignees"))

	assert.Equal(t, map[string]any{"due_on": "", "assignee_ids": []any{}}, decodeSubtaskBody(t, transport.last(t)))
}

func TestSubtasksUpdateRejectsContradictions(t *testing.T) {
	for _, args := range [][]string{
		{"update", "456", "--due", "tomorrow", "--no-due"},
		{"update", "456", "--assignees", "7", "--no-assignees"},
		{"update", "456", "A title", "--title", "Another"},
		{"update", "456", "--title", ""},
	} {
		app, transport, _ := setupPersonalFeedApp(t)

		err := executeRecordingCommand(NewSubtasksCmd(), app, args...)
		requireBookmarksUsageError(t, err)
		assert.Empty(t, transport.recorded(), "no request for %v", args)
	}
}

func TestSubtasksCompleteAndUncomplete(t *testing.T) {
	completion := fmt.Sprintf("/99999/subtasks/%d/completion.json", 456)
	for _, tc := range []struct {
		verb    string
		method  string
		summary string
	}{
		{"complete", http.MethodPost, "Completed subtask #456"},
		{"uncomplete", http.MethodDelete, "Uncompleted subtask #456"},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			app, transport, out := setupPersonalFeedApp(t,
				subtaskRoute(tc.method, completion, http.StatusNoContent, ""))

			require.NoError(t, executeRecordingCommand(NewSubtasksCmd(), app, tc.verb, "456"))

			call := transport.last(t)
			assert.Equal(t, tc.method, call.Method)
			assert.Equal(t, completion, call.Path)

			data, summary := subtaskEnvelope(t, out)
			assert.Equal(t, tc.summary, summary)
			var payload map[string]any
			require.NoError(t, json.Unmarshal(data, &payload))
			assert.Equal(t, tc.verb == "complete", payload["completed"])
		})
	}
}

func TestSubtasksMoveSendsTheOneBasedPosition(t *testing.T) {
	position := fmt.Sprintf("/99999/subtasks/%d/position.json", 456)
	app, transport, _ := setupPersonalFeedApp(t,
		subtaskRoute(http.MethodPut, position, http.StatusNoContent, ""))

	require.NoError(t, executeRecordingCommand(NewSubtasksCmd(), app, "move", "456", "--position", "1"))

	call := transport.last(t)
	assert.Equal(t, http.MethodPut, call.Method)
	assert.Equal(t, position, call.Path)
	assert.Equal(t, map[string]any{"position": float64(1)}, decodeSubtaskBody(t, call))
}

// Positions are 1-based, so 0 (or no --position at all) is a local usage
// error rather than a request the SDK would refuse.
func TestSubtasksMoveRequiresAPositivePosition(t *testing.T) {
	for _, args := range [][]string{
		{"move", "456"},
		{"move", "456", "--position", "0"},
	} {
		app, transport, _ := setupPersonalFeedApp(t)

		err := executeRecordingCommand(NewSubtasksCmd(), app, args...)
		requireBookmarksUsageError(t, err)
		assert.Empty(t, transport.recorded())
	}
}

func TestSubtasksDeleteDeletesTheSubtask(t *testing.T) {
	app, transport, out := setupPersonalFeedApp(t,
		subtaskRoute(http.MethodDelete, subtaskPath(456), http.StatusNoContent, ""))

	require.NoError(t, executeRecordingCommand(NewSubtasksCmd(), app, "delete", "456", "--force"))

	call := transport.last(t)
	assert.Equal(t, http.MethodDelete, call.Method)
	assert.Equal(t, subtaskPath(456), call.Path)
	_, summary := subtaskEnvelope(t, out)
	assert.Equal(t, "Deleted subtask #456", summary)
}

func TestSubtasksVerbsRejectANonID(t *testing.T) {
	for _, args := range [][]string{
		{"list", "not-an-id"},
		{"show", "not-an-id"},
		{"create", "not-an-id", "Title"},
		{"update", "not-an-id", "Title"},
		{"complete", "not-an-id"},
		{"uncomplete", "0"},
		{"move", "not-an-id", "--position", "1"},
		{"delete", "not-an-id"},
	} {
		app, transport, _ := setupPersonalFeedApp(t)

		err := executeRecordingCommand(NewSubtasksCmd(), app, args...)
		requireBookmarksUsageError(t, err)
		assert.Empty(t, transport.recorded(), "no request for %v", args)
	}
}

// A permanent delete in machine-output mode has nobody to confirm it, so it
// needs --force and is refused before any request without it.
func TestSubtasksDeleteNeedsForceWhenNothingCanConfirm(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t)

	err := executeRecordingCommand(NewSubtasksCmd(), app, "delete", "456")
	outErr := requireBookmarksUsageError(t, err)
	assert.Contains(t, outErr.Message, "--force")
	assert.Empty(t, transport.recorded())
}

// A to-do or card URL without a #__recording_ fragment names the parent, not a
// subtask, so it must not be read as a subtask id — least of all by delete.
func TestSubtasksPerSubtaskVerbsRefuseAParentURL(t *testing.T) {
	parentURL := "https://3.basecamp.com/99999/buckets/89/todos/123"
	for _, args := range [][]string{
		{"show", parentURL},
		{"complete", parentURL},
		{"delete", parentURL, "--force"},
		{"show", "https://example.com/not/basecamp"},
		// Collection URLs carry the parent's id, not a subtask's.
		{"delete", "https://3.basecampapi.com/99999/recordings/123/subtasks.json", "--force"},
		{"delete", "https://3.basecampapi.com/99999/buckets/89/card_tables/cards/123/steps.json", "--force"},
		// A fragment names a subtask only on a to-do or card, and only as #__recording_<id>.
		{"show", "https://3.basecamp.com/99999/buckets/89/messages/123#__recording_456"},
		{"show", "https://3.basecamp.com/99999/buckets/89/todos/123#456"},
		{"show", "https://3.basecampapi.com/99999/subtasks/789#__recording_456"},
	} {
		app, transport, _ := setupPersonalFeedApp(t)

		err := executeRecordingCommand(NewSubtasksCmd(), app, args...)
		requireBookmarksUsageError(t, err)
		assert.Empty(t, transport.recorded(), "no request for %v", args)
	}
}

// The API URL a subtask payload carries names the subtask in its path.
func TestSubtasksShowAcceptsTheSubtaskAPIURL(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t,
		subtaskRoute(http.MethodGet, subtaskPath(456), http.StatusOK, subtaskFixture))

	require.NoError(t, executeRecordingCommand(NewSubtasksCmd(), app, "show",
		"https://3.basecampapi.com/99999/buckets/89/subtasks/456.json"))

	assert.Equal(t, subtaskPath(456), transport.last(t).Path)
}

// A card's URL carries its subtasks' fragments the same way a to-do's does.
func TestSubtasksShowAcceptsACardSubtaskURL(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t,
		subtaskRoute(http.MethodGet, subtaskPath(456), http.StatusOK, subtaskFixture))

	require.NoError(t, executeRecordingCommand(NewSubtasksCmd(), app, "show",
		"https://3.basecamp.com/99999/buckets/89/card_tables/cards/123#__recording_456"))

	assert.Equal(t, subtaskPath(456), transport.last(t).Path)
}

// stepsJSON renders n subtasks with ids from 1, the first `done` completed.
func stepsJSON(n, done int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf(`{"id":%d,"title":"Step %d","type":"Kanban::Step","position":%d,"completed":%t}`,
			i+1, i+1, i+1, i < done)
	}
	return "[" + strings.Join(items, ",") + "]"
}

// parentJSON is a card or to-do embedding `inline` steps. A negative count
// omits subtasks_count, as a server predating the field would.
func parentJSON(id int64, typ string, inline, count int) string {
	countField := ""
	if count >= 0 {
		countField = fmt.Sprintf(`"subtasks_count":%d,`, count)
	}
	return fmt.Sprintf(`{"id":%d,"title":"Big checklist","type":%q,%s"bucket":{"id":123,"name":"Test Project","type":"Project"},"steps":%s}`,
		id, typ, countField, stepsJSON(inline, 0))
}

func cardGetRoute(id int64, body string) stubRoute {
	return stubRoute{method: http.MethodGet, path: fmt.Sprintf("/99999/card_tables/cards/%d", id), status: http.StatusOK, body: body}
}

func todoGetRoute(id int64, body string) stubRoute {
	return stubRoute{method: http.MethodGet, path: fmt.Sprintf("/99999/todos/%d", id), status: http.StatusOK, body: body}
}

func decodeSteps(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	var steps []map[string]any
	require.NoError(t, json.Unmarshal(raw, &steps), string(raw))
	return steps
}

// A card embeds at most 100 steps; when subtasks_count says there are more,
// `cards steps` reads the whole list instead of reporting the first 100.
func TestCardsStepsReadsEveryStepPastTheEmbedCap(t *testing.T) {
	app, transport, out := setupPersonalFeedApp(t,
		projectsRoute(),
		cardGetRoute(777, parentJSON(777, "Kanban::Card", 100, 135)),
		subtaskRoute(http.MethodGet, subtasksListPath(777), http.StatusOK, stepsJSON(135, 0)))

	require.NoError(t, executeRecordingCommand(NewCardsCmd(), app, "steps", "777", "--in", "123"))

	data, summary := subtaskEnvelope(t, out)
	steps := decodeSteps(t, data)
	require.Equal(t, 135, len(steps))
	assert.Equal(t, "Step 135", steps[134]["title"])
	assert.Equal(t, "135 steps on card #777", summary)
	assert.Len(t, transport.queriesFor(subtasksListPath(777)), 1)
}

// With no subtasks_count to compare against, exactly 100 embedded steps may be
// a cut-off list, so read the whole list.
func TestCardsStepsReadsEveryStepWhenTheCountIsMissingAndTheEmbedIsFull(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t,
		projectsRoute(),
		cardGetRoute(777, parentJSON(777, "Kanban::Card", 100, -1)),
		subtaskRoute(http.MethodGet, subtasksListPath(777), http.StatusOK, stepsJSON(135, 0)))

	require.NoError(t, executeRecordingCommand(NewCardsCmd(), app, "steps", "777", "--in", "123"))

	data, summary := subtaskEnvelope(t, out)
	assert.Equal(t, 135, len(decodeSteps(t, data)))
	assert.Equal(t, "135 steps on card #777", summary)
}

// A complete embed costs no extra request.
func TestCardsStepsTrustsACompleteEmbed(t *testing.T) {
	app, transport, out := setupPersonalFeedApp(t,
		projectsRoute(),
		cardGetRoute(777, parentJSON(777, "Kanban::Card", 3, 3)))

	require.NoError(t, executeRecordingCommand(NewCardsCmd(), app, "steps", "777", "--in", "123"))

	data, summary := subtaskEnvelope(t, out)
	assert.Equal(t, 3, len(decodeSteps(t, data)))
	assert.Equal(t, "3 steps on card #777", summary)
	assert.Empty(t, transport.queriesFor(subtasksListPath(777)))
}

func TestCardsShowCarriesEveryStepPastTheEmbedCap(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t,
		cardGetRoute(777, parentJSON(777, "Kanban::Card", 100, 135)),
		subtaskRoute(http.MethodGet, subtasksListPath(777), http.StatusOK, stepsJSON(135, 0)))

	require.NoError(t, executeRecordingCommand(NewCardsCmd(), app, "show", "777"))

	data, _ := subtaskEnvelope(t, out)
	var card struct {
		Steps         json.RawMessage `json:"steps"`
		SubtasksCount int             `json:"subtasks_count"`
	}
	require.NoError(t, json.Unmarshal(data, &card))
	assert.Equal(t, 135, len(decodeSteps(t, card.Steps)))
	assert.Equal(t, 135, card.SubtasksCount)
}

func TestTodosShowCarriesEveryStepPastTheEmbedCap(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t,
		todoGetRoute(888, parentJSON(888, "Todo", 100, 135)),
		subtaskRoute(http.MethodGet, subtasksListPath(888), http.StatusOK, stepsJSON(135, 0)))

	require.NoError(t, executeRecordingCommand(NewTodosCmd(), app, "show", "888"))

	data, _ := subtaskEnvelope(t, out)
	var todo struct {
		Steps json.RawMessage `json:"steps"`
	}
	require.NoError(t, json.Unmarshal(data, &todo))
	assert.Equal(t, 135, len(decodeSteps(t, todo.Steps)))
}

func TestTodosShowTrustsACompleteEmbed(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t,
		todoGetRoute(888, parentJSON(888, "Todo", 2, 2)))

	require.NoError(t, executeRecordingCommand(NewTodosCmd(), app, "show", "888"))

	assert.Empty(t, transport.queriesFor(subtasksListPath(888)))
}

// With no count, a full embed is only possibly cut off; when the full list
// can't be read, the embed stands rather than failing the command.
func TestCardsStepsKeepsTheEmbedWhenAGuessedReadFails(t *testing.T) {
	app, transport, out := setupPersonalFeedApp(t,
		projectsRoute(),
		cardGetRoute(777, parentJSON(777, "Kanban::Card", 100, -1)))

	require.NoError(t, executeRecordingCommand(NewCardsCmd(), app, "steps", "777", "--in", "123"))

	data, summary := subtaskEnvelope(t, out)
	assert.Equal(t, 100, len(decodeSteps(t, data)))
	assert.Equal(t, "100 steps on card #777", summary)
	assert.Len(t, transport.queriesFor(subtasksListPath(777)), 1)
}

// A known cut-off that can't be completed is an error, not a short list.
func TestCardsStepsFailsWhenAKnownCutOffCannotBeRead(t *testing.T) {
	app, _, _ := setupPersonalFeedApp(t,
		projectsRoute(),
		cardGetRoute(777, parentJSON(777, "Kanban::Card", 100, 135)))

	require.Error(t, executeRecordingCommand(NewCardsCmd(), app, "steps", "777", "--in", "123"))
}

func TestShowCarriesEveryStepPastTheEmbedCap(t *testing.T) {
	for _, tc := range []struct {
		kind, path, typ string
		id              int64
	}{
		{"card", "/99999/card_tables/cards/777.json", "Kanban::Card", 777},
		{"todo", "/99999/todos/888.json", "Todo", 888},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			app, _, out := setupPersonalFeedApp(t,
				stubRoute{method: http.MethodGet, path: tc.path, status: http.StatusOK, body: parentJSON(tc.id, tc.typ, 100, 135)},
				subtaskRoute(http.MethodGet, subtasksListPath(tc.id), http.StatusOK, stepsJSON(135, 0)))

			require.NoError(t, executeRecordingCommand(NewShowCmd(), app, tc.kind, fmt.Sprint(tc.id)))

			data, _ := subtaskEnvelope(t, out)
			var parent struct {
				Steps json.RawMessage `json:"steps"`
			}
			require.NoError(t, json.Unmarshal(data, &parent))
			steps := decodeSteps(t, parent.Steps)
			require.Equal(t, 135, len(steps))
			assert.Equal(t, "Step 135", steps[134]["title"])
		})
	}
}

func TestShowTrustsACompleteEmbed(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t,
		stubRoute{method: http.MethodGet, path: "/99999/card_tables/cards/777.json", status: http.StatusOK, body: parentJSON(777, "Kanban::Card", 3, 3)})

	require.NoError(t, executeRecordingCommand(NewShowCmd(), app, "card", "777"))

	assert.Empty(t, transport.queriesFor(subtasksListPath(777)))
}
