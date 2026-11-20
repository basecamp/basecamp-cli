package commands

import (
	"net/http"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

func TestAccountWideListsReadEveryPage(t *testing.T) {
	cases := []struct {
		name  string
		cmd   func() *cobra.Command
		args  []string
		path  string
		pages []string
		ids   func(records any) []any
		also  []stubRoute
	}{
		{
			name: "overdue todos", cmd: NewTodosCmd, args: []string{"list", "--overdue"},
			path:  "/99999/todos/overdue.json",
			pages: []string{`[{"id":1,"title":"Alpha","due_on":"2020-01-01"}]`, `[{"id":2,"title":"Bravo","due_on":"2020-02-01"}]`},
			ids:   listIDs,
		},
		{
			name: "overdue cards", cmd: NewCardsCmd, args: []string{"list", "--all-projects", "--overdue"},
			path:  "/99999/cards/overdue.json",
			pages: []string{`[{"id":1,"title":"Alpha","due_on":"2020-01-01"}]`, `[{"id":2,"title":"Bravo","due_on":"2020-02-01"}]`},
			ids:   listIDs,
		},
		{
			name: "completed assignments", cmd: NewAssignmentsCmd, args: []string{"completed"},
			path:  "/99999/my/assignments/completed.json",
			pages: []string{`[{"id":1,"content":"Alpha"}]`, `[{"id":2,"content":"Bravo"}]`},
			ids:   listIDs,
		},
		{
			name: "timesheet report", cmd: NewTimesheetCmd, args: []string{"report"},
			path:  "/99999/reports/timesheet.json",
			pages: []string{`[{"id":1,"hours":"1.0"}]`, `[{"id":2,"hours":"2.0"}]`},
			ids:   listIDs,
		},
		{
			name: "assigned todos", cmd: NewReportsCmd, args: []string{"assigned", "42"},
			path: "/99999/reports/todos/assigned/42",
			pages: []string{
				`{"person":{"id":42,"name":"Ann"},"grouped_by":"","todos":[{"id":1,"title":"Alpha"}]}`,
				`{"person":{"id":42,"name":"Ann"},"grouped_by":"","todos":[{"id":2,"title":"Bravo"}]}`,
			},
			ids: func(report any) []any { return listIDs(report.(map[string]any)["todos"]) },
			also: []stubRoute{{method: http.MethodGet, path: "/99999/people.json", status: http.StatusOK,
				body: `[{"id":42,"name":"Ann"}]`}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			listRoute := stubRoute{method: http.MethodGet, path: tc.path, status: http.StatusOK, pages: tc.pages, linked: true}
			app, transport, buf := setupAccountWideTodosApp(t, output.FormatJSON, append(tc.also, listRoute)...)

			require.NoError(t, executeRecordingCommand(tc.cmd(), app, tc.args...))

			assert.Equal(t, []string{"", "page=2"}, transport.queriesFor(tc.path))
			assert.Equal(t, []any{float64(1), float64(2)}, tc.ids(decodeTodosEnvelope(t, buf)["data"]))
		})
	}
}

func listIDs(records any) []any {
	list := records.([]any)
	ids := make([]any, 0, len(list))
	for _, record := range list {
		ids = append(ids, record.(map[string]any)["id"])
	}
	return ids
}
