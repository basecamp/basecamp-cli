package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// hillCreateVersionRoute answers the progress-update POST with the fixture
// bc3's 201 resembles.
func hillCreateVersionRoute() stubRoute {
	return stubRoute{method: http.MethodPost, path: hillVersionsPath, status: http.StatusCreated, body: hillVersionFixture}
}

// hillTodolistsRoute serves the todoset's todolists, which name resolution
// reads to turn a --dot name into an ID.
func hillTodolistsRoute() stubRoute {
	return stubRoute{method: http.MethodGet, path: "/99999/todosets/12345/todolists.json", status: http.StatusOK,
		body: `[{"id":111,"name":"Background and research"},{"id":222,"name":"Design = layout"}]`}
}

func hillPeopleRoute() stubRoute {
	return stubRoute{method: http.MethodGet, path: "/99999/people.json", status: http.StatusOK,
		body: `[{"id":7,"name":"Victor Cooper","email_address":"victor@example.com"},` +
			`{"id":8,"name":"Annie Bryan","email_address":"annie@example.com"}]`}
}

// postedHillBody returns the JSON body of the progress-update POST, failing
// the test when none was sent.
func postedHillBody(t *testing.T, transport *recordingTransport) map[string]any {
	t.Helper()
	for _, call := range transport.recorded() {
		if call.Method == http.MethodPost && call.Path == hillVersionsPath {
			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(call.Body), &body), call.Body)
			return body
		}
	}
	t.Fatalf("no POST to %s; requests: %+v", hillVersionsPath, transport.recorded())
	return nil
}

// hillErrText is an error's message and, for a usage error, its hint.
func hillErrText(err error) string {
	var e *output.Error
	if errors.As(err, &e) {
		return e.Message + "\n" + e.Hint
	}
	return err.Error()
}

func requireNoHillPost(t *testing.T, transport *recordingTransport) {
	t.Helper()
	for _, call := range transport.recorded() {
		assert.False(t, call.Method == http.MethodPost, "unexpected POST %s", call.Path)
	}
}

func TestHillchartsUpdateMovesDotsByIDAndName(t *testing.T) {
	app, transport, out := setupHillUpdatesApp(t, hillTodolistsRoute(), hillCreateVersionRoute())

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "update",
		"--todoset", "12345", "--dot", "222=100", "--dot", "Background and research=40"))

	body := postedHillBody(t, transport)
	assert.Equal(t, []any{
		map[string]any{"id": float64(222), "position": float64(100)},
		map[string]any{"id": float64(111), "position": float64(40)},
	}, body["dots"])
	assert.NotContains(t, body, "description")
	assert.NotContains(t, body, "subscriptions", "nobody is notified unless named")

	data, summary := hillEnvelope(t, out)
	assert.Equal(t, "Posted progress update #501: 2 dot(s) on the hill", summary)
	assert.Contains(t, string(data), `"id": 501`)
}

// A todolist name can hold "=": the position is after the last one.
func TestHillchartsUpdateSplitsDotAtTheLastEquals(t *testing.T) {
	app, transport, _ := setupHillUpdatesApp(t, hillTodolistsRoute(), hillCreateVersionRoute())

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "update",
		"--todoset", "12345", "--dot", "Design = layout=55"))

	assert.Equal(t, []any{map[string]any{"id": float64(222), "position": float64(55)}},
		postedHillBody(t, transport)["dots"])
}

func TestHillchartsUpdateConvertsTheNoteFromMarkdown(t *testing.T) {
	app, transport, _ := setupHillUpdatesApp(t, hillCreateVersionRoute())

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "update",
		"--todoset", "12345", "--note", "Research is **done**"))

	body := postedHillBody(t, transport)
	desc, _ := body["description"].(string)
	assert.Contains(t, desc, "<strong>done</strong>")
	assert.NotContains(t, body, "dots", "lists left out keep their position, so none are sent")
}

func TestHillchartsUpdateSendsHTMLAsWrittenWithFormatHTML(t *testing.T) {
	app, transport, _ := setupHillUpdatesApp(t, hillCreateVersionRoute())

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "update",
		"--todoset", "12345", "--note", "<div>**as is**</div>", "--format", "html"))

	assert.Equal(t, "<div>**as is**</div>", postedHillBody(t, transport)["description"])
}

func TestHillchartsUpdateReadsTheNoteFromStdin(t *testing.T) {
	app, transport, _ := setupHillUpdatesApp(t, hillCreateVersionRoute())

	cmd := NewHillchartsCmd()
	cmd.SetArgs([]string{"update", "--todoset", "12345", "--note", "-"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetIn(strings.NewReader("Building starts *Monday*\n"))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())

	desc, _ := postedHillBody(t, transport)["description"].(string)
	assert.Contains(t, desc, "<em>Monday</em>")
}

func TestHillchartsUpdateNotifiesNamedPeople(t *testing.T) {
	app, transport, _ := setupHillUpdatesApp(t, hillPeopleRoute(), hillCreateVersionRoute())

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "update",
		"--todoset", "12345", "--dot", "111=60", "--notify", "Annie Bryan, 7"))

	assert.Equal(t, []any{float64(8), float64(7)}, postedHillBody(t, transport)["subscriptions"])
}

func TestHillchartsUpdateResolvesTheTodosetFromTheProject(t *testing.T) {
	app, transport, out := setupHillUpdatesApp(t, projectsRoute(), hillProjectRoute(), hillCreateVersionRoute())
	app.Flags.Hints = true

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "update",
		"--in", "123", "--dot", "111=60"))

	postedHillBody(t, transport)
	var envelope struct {
		Breadcrumbs []struct {
			Cmd string `json:"cmd"`
		} `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope), out.String())
	cmds := make([]string, 0, len(envelope.Breadcrumbs))
	for _, b := range envelope.Breadcrumbs {
		cmds = append(cmds, b.Cmd)
	}
	assert.Contains(t, cmds, "basecamp hillcharts show --in 123")
	assert.Contains(t, cmds, "basecamp hillcharts updates --in 123")
	assert.Contains(t, cmds, "basecamp comments list 501")
}

// Every mistake the flags alone reveal is refused before any request: no
// account lookup, no name resolution, and above all no POST.
func TestHillchartsUpdateRefusesBadFlagsBeforeAnyRequest(t *testing.T) {
	cases := map[string]struct {
		args []string
		want string
	}{
		"nothing to post":    {[]string{"update", "--todoset", "12345"}, "No update fields specified"},
		"no position":        {[]string{"update", "--todoset", "12345", "--dot", "Design"}, "needs a position"},
		"no todolist":        {[]string{"update", "--todoset", "12345", "--dot", "=50"}, "names no todolist"},
		"position too high":  {[]string{"update", "--todoset", "12345", "--dot", "111=101"}, "0 to 100"},
		"position negative":  {[]string{"update", "--todoset", "12345", "--dot", "111=-1"}, "0 to 100"},
		"position not whole": {[]string{"update", "--todoset", "12345", "--dot", "111=50.5"}, "0 to 100"},
		"position empty":     {[]string{"update", "--todoset", "12345", "--dot", "111="}, "0 to 100"},
		"empty notify":       {[]string{"update", "--todoset", "12345", "--dot", "111=5", "--notify", " , "}, "at least one person"},
		"empty note alone":   {[]string{"update", "--todoset", "12345", "--note", "  "}, "nothing to post"},
		"positional arg":     {[]string{"update", "--todoset", "12345", "111=5"}, "unknown command"},
		"todoset not an id":  {[]string{"update", "--todoset", "bogus", "--note", "-"}, "Invalid todoset ID: bogus"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			app, transport, _ := setupHillUpdatesApp(t)
			app.Flags.JSON = true // with a terminal, "nothing to post" shows help instead
			err := executeHillchartsCommand(NewHillchartsCmd(), app, tc.args...)
			require.Error(t, err)
			assert.Contains(t, hillErrText(err), tc.want)
			assert.Empty(t, transport.recorded())
		})
	}
}

func TestHillchartsUpdateRefusesTwoDotsForOneTodolist(t *testing.T) {
	app, transport, _ := setupHillUpdatesApp(t, hillTodolistsRoute())

	err := executeHillchartsCommand(NewHillchartsCmd(), app, "update",
		"--todoset", "12345", "--dot", "111=10", "--dot", "Background and research=20")
	requireUsageError(t, err, "move the same todolist (111)")
	requireNoHillPost(t, transport)
}

// A refusal from bc3 (an untracked todolist, say) reaches the caller with the
// server's message, and is not reported as success.
func TestHillchartsUpdateSurfacesTheServersRefusal(t *testing.T) {
	app, _, out := setupHillUpdatesApp(t, stubRoute{method: http.MethodPost, path: hillVersionsPath,
		status: http.StatusUnprocessableEntity, body: `{"error":"To-do list 333 isn't tracked on this hill chart"}`})

	err := executeHillchartsCommand(NewHillchartsCmd(), app, "update", "--todoset", "12345", "--dot", "333=10")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "isn't tracked")
	assert.Empty(t, out.String())
}

// --notify alone records the chart as it stands and tells the people named.
func TestHillchartsUpdateNotifyAlonePosts(t *testing.T) {
	app, transport, _ := setupHillUpdatesApp(t, hillPeopleRoute(), hillCreateVersionRoute())

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "update",
		"--todoset", "12345", "--notify", "7"))

	body := postedHillBody(t, transport)
	assert.Equal(t, []any{float64(7)}, body["subscriptions"])
	assert.NotContains(t, body, "dots")
	assert.NotContains(t, body, "description")
}
