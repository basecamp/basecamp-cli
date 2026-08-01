package commands

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

const (
	assignmentsPrioritiesPath   = "/99999/my/priorities.json"
	assignmentsPriorityMovePath = "/99999/my/priority_moves.json"
)

func assignmentsPriorityPath(id int64) string {
	return fmt.Sprintf("/99999/my/priorities/%d", id)
}

func noContentRoute(method, path string) stubRoute {
	return stubRoute{method: method, path: path, status: http.StatusNoContent, body: ""}
}

func TestAssignmentsPrioritizePosts(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, noContentRoute(http.MethodPost, assignmentsPrioritiesPath))

	require.NoError(t, executeRecordingCommand(NewAssignmentsCmd(), app, "prioritize", "42"))

	call := transport.last(t)
	assert.Equal(t, http.MethodPost, call.Method)
	assert.Equal(t, assignmentsPrioritiesPath, call.Path)
	assert.Contains(t, call.Body, "42")
}

func TestAssignmentsDeprioritizeDeletes(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, noContentRoute(http.MethodDelete, assignmentsPriorityPath(42)))

	require.NoError(t, executeRecordingCommand(NewAssignmentsCmd(), app, "deprioritize", "42"))

	call := transport.last(t)
	assert.Equal(t, http.MethodDelete, call.Method)
	assert.Equal(t, assignmentsPriorityPath(42), call.Path)
}

func TestAssignmentsReorderSendsThePosition(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, noContentRoute(http.MethodPost, assignmentsPriorityMovePath))

	require.NoError(t, executeRecordingCommand(NewAssignmentsCmd(), app, "reorder", "42", "--position", "3"))

	call := transport.last(t)
	assert.Equal(t, http.MethodPost, call.Method)
	assert.Equal(t, assignmentsPriorityMovePath, call.Path)
	assert.Contains(t, call.Body, "3")
}

// Positions are 1-based, and a bad one is refused rather than clamped: serving
// a different position than the one asked for would move the item somewhere the
// caller did not choose.
func TestAssignmentsReorderRejectsBadPositions(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing --position", []string{"reorder", "42"}},
		{"zero", []string{"reorder", "42", "--position", "0"}},
		{"negative", []string{"reorder", "42", "--position=-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, transport, _ := setupPersonalFeedApp(t, noContentRoute(http.MethodPost, assignmentsPriorityMovePath))

			err := executeRecordingCommand(NewAssignmentsCmd(), app, tc.args...)

			outErr := requireBookmarksUsageError(t, err)
			assert.Contains(t, outErr.Message, "--position")
			assert.Empty(t, transport.recorded(), "a rejected move must not reach the server")
		})
	}
}

func TestAssignmentsPriorityVerbsRejectANonID(t *testing.T) {
	for _, args := range [][]string{
		{"prioritize", "not-an-id"},
		{"deprioritize", "not-an-id"},
		{"reorder", "not-an-id", "--position", "1"},
	} {
		t.Run(args[0], func(t *testing.T) {
			app, transport, _ := setupPersonalFeedApp(t)

			err := executeRecordingCommand(NewAssignmentsCmd(), app, args...)

			outErr := requireBookmarksUsageError(t, err)
			assert.Contains(t, outErr.Hint, "recording id")
			assert.Empty(t, transport.recorded())
		})
	}
}

// priority_recording_id is what reorder and deprioritize need to address a
// prioritized card-table step, and this listing is the only place it exists —
// it appears in no URL and no other command's output. A row that drops it
// leaves those verbs with no way to name their target, and the failure is
// silent: the server answers 204 either way.
func TestFlattenAssignmentsSurfacesPriorityRecordingID(t *testing.T) {
	priorityID := int64(9001)
	rows := flattenAssignments(&basecamp.MyAssignmentsResult{
		Priorities: []basecamp.MyAssignment{{
			ID:                  777,
			Content:             "Card with a prioritized step",
			Type:                "Kanban::Card",
			Bucket:              basecamp.MyAssignmentBucket{ID: 977190, Name: "JD test proj"},
			PriorityRecordingID: &priorityID,
		}},
		NonPriorities: []basecamp.MyAssignment{{
			ID:      888,
			Content: "Not in Up Next",
			Type:    "Todo",
			Bucket:  basecamp.MyAssignmentBucket{ID: 977190, Name: "JD test proj"},
		}},
	})

	require.Len(t, rows, 2)

	assert.Equal(t, int64(777), rows[0]["id"], "the entry id is the card's")
	assert.Equal(t, priorityID, rows[0]["priority_recording_id"], "the step is addressed by this instead")
	assert.Equal(t, true, rows[0]["up_next"])
	assert.Equal(t, "JD test proj", rows[0]["project"])

	assert.NotContains(t, rows[1], "priority_recording_id",
		"an unprioritized entry has no priority_recording_id yet")
	assert.Equal(t, false, rows[1]["up_next"])
}
