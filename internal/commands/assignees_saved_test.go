package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// assigneeEchoTransport plays Basecamp for every command that sets assignees.
// Each record is #999. A write answers with the assignee_ids it was sent, or,
// when drop is set, with none of them: what Basecamp does with an ID it
// cannot assign, such as a card column's.
type assigneeEchoTransport struct {
	drop bool
}

func (m *assigneeEchoTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := http.Header{"Content-Type": []string{"application/json"}}
	respond := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
	}

	path := req.URL.Path
	switch req.Method {
	case http.MethodGet:
		switch {
		case strings.HasSuffix(path, "/projects.json"):
			return respond(200, `[{"id": 123, "name": "Test Project"}]`)
		case strings.HasSuffix(path, "/projects/123.json"):
			return respond(200, `{"id": 123, "dock": [{"name": "kanban_board", "id": 789, "title": "Card Table"}]}`)
		case strings.HasSuffix(path, "/people.json"):
			return respond(200, `[{"id": 42, "name": "Annie Bryan"}]`)
		}
		return respond(200, `{"id": 999, "title": "Thing", "content": "Thing", "assignees": []}`)
	case http.MethodPost, http.MethodPut:
		var body struct {
			AssigneeIDs []int64 `json:"assignee_ids"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		assignees := []string{}
		if !m.drop {
			for _, id := range body.AssigneeIDs {
				assignees = append(assignees, fmt.Sprintf(`{"id": %d, "name": "Person %d"}`, id, id))
			}
		}
		status := http.StatusOK
		if req.Method == http.MethodPost {
			status = http.StatusCreated
		}
		return respond(status, `{"id": 999, "title": "Thing", "content": "Thing", "assignees": [`+strings.Join(assignees, ",")+`]}`)
	}
	return nil, fmt.Errorf("unexpected %s %s", req.Method, path)
}

type assigneeWrite struct {
	name string
	cmd  func() *cobra.Command
	args []string
}

// Every command that sets assignees, given the ID of a card column.
var assigneeWrites = []assigneeWrite{
	{"cards create --to", NewCardsCmd, []string{"create", "Thing", "--column", "111", "--to", "822400739"}},
	{"cards update", NewCardsCmd, []string{"update", "999", "--assignee", "822400739"}},
	{"cards step create", NewCardsCmd, []string{"step", "create", "Thing", "--card", "999", "--assignees", "822400739"}},
	{"cards step update", NewCardsCmd, []string{"step", "update", "999", "--assignees", "822400739"}},
	{"todos create", NewTodosCmd, []string{"create", "Thing", "--list", "555", "--assignee", "822400739"}},
	{"todos update", NewTodosCmd, []string{"update", "999", "--assignee", "822400739"}},
	{"subtasks create", NewSubtasksCmd, []string{"create", "999", "Thing", "--assignees", "822400739"}},
	{"subtasks update", NewSubtasksCmd, []string{"update", "999", "--assignees", "822400739"}},
	{"assign", NewAssignCmd, []string{"999", "--to", "822400739"}},
	{"assign --card", NewAssignCmd, []string{"999", "--card", "--to", "822400739"}},
	{"assign --step", NewAssignCmd, []string{"999", "--step", "--to", "822400739"}},
}

func TestAssigneeWritesFailWhenBasecampDropsTheAssignee(t *testing.T) {
	for _, w := range assigneeWrites {
		t.Run(w.name, func(t *testing.T) {
			app := setupCardsMockApp(t, &assigneeEchoTransport{drop: true})

			err := executeCommand(w.cmd(), app, w.args...)

			var e *output.Error
			require.True(t, errors.As(err, &e), "want an output error, got %v", err)
			assert.Equal(t, output.CodeUsage, e.Code)
			assert.Contains(t, e.Message, "#999")
			assert.Contains(t, e.Message, "822400739")
		})
	}
}

func TestAssigneeWritesSucceedWhenBasecampKeepsTheAssignee(t *testing.T) {
	for _, w := range assigneeWrites {
		t.Run(w.name, func(t *testing.T) {
			app := setupCardsMockApp(t, &assigneeEchoTransport{})

			require.NoError(t, executeCommand(w.cmd(), app, w.args...))
		})
	}
}

func TestCheckAssigneesSavedNamesWhatTheRecordKept(t *testing.T) {
	err := checkAssigneesSaved(assigneeCard, 7, []int64{1, 2, 3}, []basecamp.Person{{ID: 2, Name: "Ann"}})

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "card #7")
	assert.Contains(t, e.Message, "1, 3")
	assert.Contains(t, e.Message, "Ann")
}
