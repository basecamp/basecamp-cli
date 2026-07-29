package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// The root --todolist is a global, so it reaches every account-wide listing
// whether or not the command has any notion of a todolist. I3 lists it among
// the scope-child flags that must be rejected by name rather than dropped.
func TestAccountWideListingsRejectRootTodolist(t *testing.T) {
	cases := []struct {
		name string
		cmd  func() *cobra.Command
		args []string
	}{
		{"messages", NewMessagesCmd, []string{"list"}},
		{"comments", NewCommentsCmd, []string{"list"}},
		{"boost", NewBoostsCmd, []string{"list"}},
		{"forwards", NewForwardsCmd, []string{"list"}},
		{"checkins", NewCheckinsCmd, []string{"answers"}},
		{"files", NewFilesCmd, []string{"list"}},
		{"cards", NewCardsCmd, []string{"list"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, transport := setupRecordingTestApp(t)
			app.Flags.Todolist = "456"

			err := executeRecordingCommand(tc.cmd(), app, tc.args...)
			require.Error(t, err)

			var e *output.Error
			require.True(t, errors.As(err, &e), "expected *output.Error, got %T", err)
			assert.Contains(t, e.Message, "--todolist")
			assert.Empty(t, transport.recorded(), "must reject before any request")
		})
	}
}

// The grouped aggregates nest their items inside project groups. --count and
// --ids read the display rows so they report todos rather than projects, and
// --md renders rows at all instead of a heading with no table.
func TestAccountWideGroupedListingsFeedEveryOutputMode(t *testing.T) {
	body := `[{"bucket":{"id":1,"name":"Alpha"},"todos":[{"id":11,"title":"A1"},{"id":12,"title":"A2"}]},
	          {"bucket":{"id":2,"name":"Beta"},"todos":[{"id":21,"title":"B1"}]}]`

	run := func(t *testing.T, format output.Format) string {
		t.Helper()
		app, _ := setupRecordingTestApp(t, stubRoute{
			method: http.MethodGet, path: "/99999/todos/open.json", status: http.StatusOK, body: body,
		})
		buf := &bytes.Buffer{}
		app.Output = output.New(output.Options{Format: format, Writer: buf})
		require.NoError(t, executeRecordingCommand(newTodosListCmd(), app, "--all-projects", "--page", "1"))
		return buf.String()
	}

	assert.Equal(t, "3\n", run(t, output.FormatCount), "counts todos, not project groups")
	assert.Equal(t, "11\n12\n21\n", run(t, output.FormatIDs))

	md := run(t, output.FormatMarkdown)
	assert.Contains(t, md, "| Alpha |")
	assert.Contains(t, md, "A1")

	// --json keeps the grouping the SDK returned.
	app, _ := setupRecordingTestApp(t, stubRoute{
		method: http.MethodGet, path: "/99999/todos/open.json", status: http.StatusOK, body: body,
	})
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})
	require.NoError(t, executeRecordingCommand(newTodosListCmd(), app, "--all-projects", "--page", "1"))

	var envelope struct {
		Data []struct {
			Bucket struct {
				Name string `json:"name"`
			} `json:"bucket"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	require.Len(t, envelope.Data, 2)
	assert.Equal(t, "Alpha", envelope.Data[0].Bucket.Name)
}

// An account-wide row the user cannot attribute to a project is not actionable,
// so the recording feeds carry the bucket name into their display rows.
func TestAccountWideRecordingFeedsCarryProject(t *testing.T) {
	body := `[{"id":7,"title":"Message","subject":"Ship it","type":"Message",
	           "bucket":{"id":1,"name":"Alpha"},"created_at":"2026-01-01T00:00:00Z"}]`

	app, _ := setupRecordingTestApp(t, stubRoute{
		method: http.MethodGet, path: "/99999/messages.json", status: http.StatusOK, body: body,
	})
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatMarkdown, Writer: buf})
	require.NoError(t, executeRecordingCommand(NewMessagesCmd(), app, "list", "--all-projects", "--page", "1"))

	out := buf.String()
	assert.Contains(t, out, "Alpha", "styled/markdown rows must name the project")
	assert.Contains(t, out, "Ship it", "subject wins over the generic recording title")
	assert.True(t, strings.Contains(out, "| Project |") || strings.Contains(out, "Project"),
		"expected a project column, got:\n%s", out)
}

// The flat overdue aggregates return items from every project with the project
// in a nested bucket, which both generic renderers skip by name. Without
// display rows, two otherwise identical overdue todos cannot be told apart.
func TestAccountWideOverdueListingsNameTheirProject(t *testing.T) {
	t.Run("todos", func(t *testing.T) {
		body := `[{"id":11,"title":"Ship it","due_on":"2020-01-01","bucket":{"id":1,"name":"Alpha"}}]`
		app, _ := setupRecordingTestApp(t, stubRoute{
			method: http.MethodGet, path: "/99999/todos/overdue.json", status: http.StatusOK, body: body,
		})
		buf := &bytes.Buffer{}
		app.Output = output.New(output.Options{Format: output.FormatMarkdown, Writer: buf})
		require.NoError(t, executeRecordingCommand(newTodosListCmd(), app, "--all-projects", "--overdue"))
		assert.Contains(t, buf.String(), "Alpha")
		assert.Contains(t, buf.String(), "Ship it")
	})

	t.Run("cards", func(t *testing.T) {
		body := `[{"id":21,"title":"Fix it","due_on":"2020-01-01","bucket":{"id":2,"name":"Beta"}}]`
		app, _ := setupRecordingTestApp(t, stubRoute{
			method: http.MethodGet, path: "/99999/cards/overdue.json", status: http.StatusOK, body: body,
		})
		buf := &bytes.Buffer{}
		app.Output = output.New(output.Options{Format: output.FormatMarkdown, Writer: buf})
		require.NoError(t, executeRecordingCommand(NewCardsCmd(), app, "list", "--all-projects", "--overdue"))
		assert.Contains(t, buf.String(), "Beta")
		assert.Contains(t, buf.String(), "Fix it")
	})
}
