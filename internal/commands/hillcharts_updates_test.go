package commands

import (
	"bytes"
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

const (
	hillVersionsPath = "/99999/todosets/12345/hills/versions.json"
	hillVersionPath  = "/99999/todosets/12345/hills/versions/501" // the SDK sends no .json here
)

// hillVersionFixture is a progress update as bc3 serves it, with one dot whose
// todolist has since been deleted (id null).
const hillVersionFixture = `{
	"id": 501,
	"status": "active",
	"title": "Hill Chart: Progress update",
	"type": "Hill::Version",
	"created_at": "2026-10-01T15:10:47Z",
	"updated_at": "2026-10-01T15:10:47Z",
	"app_url": "https://3.basecamp.com/99999/buckets/123/todosets/12345/hills/versions#__recording_501",
	"comments_count": 2,
	"creator": {"id": 7, "name": "Victor Cooper"},
	"bucket": {"id": 123, "name": "Test Project", "type": "Project"},
	"description": "<div>Research is <strong>wrapping up</strong>.</div>",
	"description_attachments": [],
	"dots": [
		{"id": 111, "label": "Background and research", "color": "blue", "position": 40},
		{"id": null, "label": "Gone list", "color": "red", "position": 90}
	]
}`

func hillVersionsRoute(body string) stubRoute {
	return stubRoute{method: http.MethodGet, path: hillVersionsPath, status: http.StatusOK, body: body}
}

func hillVersionRoute() stubRoute {
	return stubRoute{method: http.MethodGet, path: hillVersionPath, status: http.StatusOK, body: hillVersionFixture}
}

// hillProjectRoute serves project 123 with its todoset in the dock, which is
// how a project-scoped call finds the todoset.
func hillProjectRoute() stubRoute {
	return stubRoute{method: http.MethodGet, path: "/99999/projects/123.json", status: http.StatusOK,
		body: `{"id":123,"name":"Test Project","dock":[{"name":"todoset","id":12345,"title":"To-dos","enabled":true}]}`}
}

func setupHillUpdatesApp(t *testing.T, routes ...stubRoute) (*appctx.App, *recordingTransport, *bytes.Buffer) {
	t.Helper()
	app, transport := setupRecordingTestApp(t, routes...)
	out := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: out})
	return app, transport, out
}

func hillEnvelope(t *testing.T, out *bytes.Buffer) (data json.RawMessage, summary string) {
	t.Helper()
	var envelope struct {
		Data    json.RawMessage `json:"data"`
		Summary string          `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope), out.String())
	return envelope.Data, envelope.Summary
}

func requireUsageError(t *testing.T, err error, contains string) {
	t.Helper()
	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Contains(t, e.Message+"\n"+e.Hint, contains)
}

// --- updates (list) ---

func TestHillchartsUpdatesListsByTodoset(t *testing.T) {
	app, transport, out := setupHillUpdatesApp(t, hillVersionsRoute("["+hillVersionFixture+"]"))

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "--todoset", "12345"))

	calls := transport.recorded()
	require.Len(t, calls, 1, "only the versions listing is requested")
	assert.Equal(t, http.MethodGet, calls[0].Method)
	assert.Equal(t, hillVersionsPath, calls[0].Path)

	data, summary := hillEnvelope(t, out)
	var versions []map[string]any
	require.NoError(t, json.Unmarshal(data, &versions))
	require.Len(t, versions, 1)
	assert.EqualValues(t, 501, versions[0]["id"])
	assert.Equal(t, "1 progress update(s)", summary)
}

func TestHillchartsUpdatesListSubcommandMatchesBare(t *testing.T) {
	app, transport, _ := setupHillUpdatesApp(t, hillVersionsRoute("[]"))

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "list", "--todoset", "12345"))

	assert.Equal(t, hillVersionsPath, transport.last(t).Path)
}

func TestHillchartsUpdatesFindsTheTodosetFromTheProject(t *testing.T) {
	app, transport, _ := setupHillUpdatesApp(t, projectsRoute(), hillProjectRoute(), hillVersionsRoute("[]"))

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "--in", "123"))

	assert.Equal(t, hillVersionsPath, transport.last(t).Path)
}

func TestHillchartsUpdatesEmptyIsAnArray(t *testing.T) {
	app, _, out := setupHillUpdatesApp(t, hillVersionsRoute("[]"))
	app.Flags.Hints = true

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "--todoset", "12345"))

	data, _ := hillEnvelope(t, out)
	assert.JSONEq(t, "[]", string(data))
}

func TestHillchartsUpdatesPageSelectsOnePage(t *testing.T) {
	app, transport, _ := setupHillUpdatesApp(t, hillVersionsRoute("[]"))

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "--todoset", "12345", "--page", "2"))

	assert.Equal(t, []string{"page=2"}, transport.queriesFor(hillVersionsPath))
}

func TestHillchartsUpdatesLimitTrims(t *testing.T) {
	two := "[" + hillVersionFixture + "," + strings.Replace(hillVersionFixture, `"id": 501`, `"id": 502`, 1) + "]"
	app, _, out := setupHillUpdatesApp(t, hillVersionsRoute(two))

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "--todoset", "12345", "--limit", "1"))

	data, _ := hillEnvelope(t, out)
	var versions []map[string]any
	require.NoError(t, json.Unmarshal(data, &versions))
	require.Len(t, versions, 1)
	assert.EqualValues(t, 501, versions[0]["id"])
}

func TestHillchartsUpdatesRejectsBadPaginationBeforeAnyRequest(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--all", "--limit", "5"}, "--all and --limit are mutually exclusive"},
		{[]string{"--page", "2", "--all"}, "--page cannot be combined"},
		{[]string{"--page", "2", "--limit", "5"}, "--page cannot be combined"},
		{[]string{"--page", "0"}, "--page must be 1 or greater"},
		{[]string{"--limit", "-1"}, "--limit must be zero or positive"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			app, transport, _ := setupHillUpdatesApp(t)
			args := append([]string{"updates", "--todoset", "12345"}, tc.args...)
			requireUsageError(t, executeHillchartsCommand(NewHillchartsCmd(), app, args...), tc.want)
			assert.Empty(t, transport.recorded())
		})
	}
}

// An ID passed to the listing is pointed at show rather than ignored.
func TestHillchartsUpdatesWithAnArgumentPointsAtShow(t *testing.T) {
	app, transport, _ := setupHillUpdatesApp(t)

	err := executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "501", "--todoset", "12345")

	requireUsageError(t, err, "basecamp hillcharts updates show <id|url>")
	assert.Empty(t, transport.recorded())
}

// --- updates show ---

func TestHillchartsUpdatesShowByIDAndTodoset(t *testing.T) {
	app, transport, out := setupHillUpdatesApp(t, hillVersionRoute())

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "show", "501", "--todoset", "12345"))

	assert.Equal(t, hillVersionPath, transport.last(t).Path)

	data, summary := hillEnvelope(t, out)
	var version map[string]any
	require.NoError(t, json.Unmarshal(data, &version))
	assert.EqualValues(t, 501, version["id"])
	assert.Equal(t, "Progress update #501 by Victor Cooper: 2 dot(s)", summary)

	// A deleted todolist's dot keeps its place with a null id.
	dots, ok := version["dots"].([]any)
	require.True(t, ok)
	require.Len(t, dots, 2)
	gone := dots[1].(map[string]any)
	assert.Contains(t, gone, "id")
	assert.Nil(t, gone["id"])
	assert.Equal(t, "Gone list", gone["label"])
}

func TestHillchartsUpdatesShowByIDAndProject(t *testing.T) {
	app, transport, _ := setupHillUpdatesApp(t, projectsRoute(), hillProjectRoute(), hillVersionRoute())

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "show", "501", "--in", "123"))

	assert.Equal(t, hillVersionPath, transport.last(t).Path)
}

// A pasted URL names its own todoset, so no project or --todoset is needed,
// and nothing else is requested.
func TestHillchartsUpdatesShowByURL(t *testing.T) {
	for name, url := range map[string]string{
		"web app_url":             "https://3.basecamp.com/99999/buckets/123/todosets/12345/hills/versions#__recording_501",
		"API url":                 "https://3.basecampapi.com/99999/buckets/123/todosets/12345/hills/versions/501.json",
		"web path, no .json":      "https://3.basecamp.com/99999/buckets/123/todosets/12345/hills/versions/501",
		"account-level API url":   "https://3.basecampapi.com/99999/todosets/12345/hills/versions/501.json",
		"account-level, no .json": "https://3.basecampapi.com/99999/todosets/12345/hills/versions/501",
	} {
		t.Run(name, func(t *testing.T) {
			app, transport, _ := setupHillUpdatesApp(t, hillVersionRoute())

			require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "show", url))

			calls := transport.recorded()
			require.Len(t, calls, 1)
			assert.Equal(t, hillVersionPath, calls[0].Path)
		})
	}
}

func TestHillchartsUpdatesShowURLAgreesWithFlags(t *testing.T) {
	url := "https://3.basecamp.com/99999/buckets/123/todosets/12345/hills/versions#__recording_501"

	t.Run("matching --todoset", func(t *testing.T) {
		app, transport, _ := setupHillUpdatesApp(t, hillVersionRoute())
		require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "show", url, "--todoset", "12345"))
		assert.Equal(t, hillVersionPath, transport.last(t).Path)
	})

	t.Run("other --todoset", func(t *testing.T) {
		app, transport, _ := setupHillUpdatesApp(t)
		err := executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "show", url, "--todoset", "777")
		requireUsageError(t, err, "--todoset 777 does not match the URL's todoset 12345")
		assert.Empty(t, transport.recorded())
	})

	t.Run("other project", func(t *testing.T) {
		app, transport, _ := setupHillUpdatesApp(t, stubRoute{method: http.MethodGet, path: "/99999/projects.json",
			status: http.StatusOK, body: `[{"id":123,"name":"Test Project"},{"id":456,"name":"Other"}]`})
		err := executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "show", url, "--in", "456")
		requireUsageError(t, err, "the URL is in project 123, not 456")
		for _, call := range transport.recorded() {
			assert.NotContains(t, call.Path, "/hills/", "no progress update is read for a contradicted URL")
		}
	})
}

// The account-level API URL names no project, so --in is checked against the
// todoset's own bucket.
func TestHillchartsUpdatesShowAccountLevelURLChecksProjectThroughTodoset(t *testing.T) {
	url := "https://3.basecampapi.com/99999/todosets/12345/hills/versions/501.json"
	todoset := func(bucket string) stubRoute {
		return stubRoute{method: http.MethodGet, path: "/99999/todosets/12345", status: http.StatusOK,
			body: `{"id":12345,"todolists_count":2,"bucket":{"id":` + bucket + `,"name":"P"}}`}
	}

	t.Run("its project", func(t *testing.T) {
		app, transport, _ := setupHillUpdatesApp(t, projectsRoute(), todoset("123"), hillVersionRoute())
		require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "show", url, "--in", "123"))
		assert.Equal(t, hillVersionPath, transport.last(t).Path)
	})

	t.Run("another project", func(t *testing.T) {
		app, transport, _ := setupHillUpdatesApp(t, projectsRoute(), todoset("456"))
		err := executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "show", url, "--in", "123")
		requireUsageError(t, err, "belongs to project 456")
		for _, call := range transport.recorded() {
			assert.NotContains(t, call.Path, "/hills/")
		}
	})
}

// An account adopted from a pasted URL is carried into the follow-up
// commands, which would otherwise run against no account (or another one).
func TestHillchartsUpdatesShowBreadcrumbsCarryAnAdoptedAccount(t *testing.T) {
	app, _, out := setupHillUpdatesApp(t, hillVersionRoute())
	app.Config.AccountID = ""
	app.Flags.Hints = true

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "show",
		"https://3.basecamp.com/99999/buckets/123/todosets/12345/hills/versions#__recording_501"))

	var envelope struct {
		Breadcrumbs []struct {
			Cmd string `json:"cmd"`
		} `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope), out.String())
	require.NotEmpty(t, envelope.Breadcrumbs)
	for _, b := range envelope.Breadcrumbs {
		assert.Contains(t, b.Cmd, "--account 99999", b.Cmd)
	}
}

// With the account inherited from configuration, follow-ups stay bare.
func TestHillchartsUpdatesBreadcrumbsInheritAConfiguredAccount(t *testing.T) {
	app, _, out := setupHillUpdatesApp(t, hillVersionsRoute("[]"))
	app.Flags.Hints = true

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "--todoset", "12345"))

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
	assert.Contains(t, cmds, "basecamp hillcharts updates show <id> --todoset 12345")
	for _, c := range cmds {
		assert.NotContains(t, c, "--account", c)
	}
}

func TestHillchartsUpdatesShowRefusesURLsBeforeAnyRequest(t *testing.T) {
	for _, tc := range []struct {
		name, arg, want string
	}{
		{"untrusted host", "https://3.basecamp.example.com/99999/buckets/123/todosets/12345/hills/versions#__recording_501", "untrusted host"},
		{"other account", "https://3.basecamp.com/11111/buckets/123/todosets/12345/hills/versions#__recording_501", "does not match the configured account"},
		{"the listing itself", "https://3.basecamp.com/99999/buckets/123/todosets/12345/hills/versions", "the list of progress updates"},
		{"a todolist", "https://3.basecamp.com/99999/buckets/123/todolists/111", "does not name a hill chart progress update"},
		{"the hill chart", "https://3.basecamp.com/99999/buckets/123/todosets/12345/hill", "does not name a hill chart progress update"},
		{"path hidden in a query", "https://3.basecamp.com/99999/buckets/123/todolists/111?x=/99999/buckets/123/todosets/12345/hills/versions/501", "does not name a hill chart progress update"},
		{"comment fragment on another page", "https://3.basecamp.com/99999/buckets/123/todolists/111#__recording_501", "does not name a hill chart progress update"},
		{"not a number", "abc", "does not name a hill chart progress update"},
		{"zero", "0", "not a progress update ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, transport, _ := setupHillUpdatesApp(t)
			err := executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "show", tc.arg, "--todoset", "12345")
			requireUsageError(t, err, tc.want)
			assert.Empty(t, transport.recorded())
		})
	}
}

func TestHillchartsUpdatesShowNeedsAnArgument(t *testing.T) {
	app, transport, _ := setupHillUpdatesApp(t)

	err := executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "show", "--todoset", "12345")

	require.Error(t, err)
	assert.Empty(t, transport.recorded())
}

func TestHillchartsUpdatesShowNotFound(t *testing.T) {
	app, _, _ := setupHillUpdatesApp(t)

	err := executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "show", "501", "--todoset", "12345")

	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeNotFound, e.Code)
}

// --- human output ---

func TestHillchartsUpdatesShowMarkdownListsDots(t *testing.T) {
	app, _, out := setupHillUpdatesApp(t, hillVersionRoute())
	app.Output = output.New(output.Options{Format: output.FormatMarkdown, Writer: out})

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "show", "501", "--todoset", "12345"))

	text := out.String()
	assert.Contains(t, text, "Research is **wrapping up**.")
	assert.Contains(t, text, "-  40/100  Background and research")
	assert.Contains(t, text, "-  90/100  Gone list")
	assert.Contains(t, text, "Victor Cooper")
	assert.NotContains(t, text, "<nil>")
}

func TestHillchartsUpdatesListMarkdownLeadsWithAuthor(t *testing.T) {
	app, _, out := setupHillUpdatesApp(t, hillVersionsRoute("["+hillVersionFixture+"]"))
	app.Output = output.New(output.Options{Format: output.FormatMarkdown, Writer: out})

	require.NoError(t, executeHillchartsCommand(NewHillchartsCmd(), app, "updates", "--todoset", "12345"))

	text := out.String()
	assert.Contains(t, text, "| Id | Creator | Created | Description |")
	assert.Contains(t, text, "| 501 | Victor Cooper |")
	assert.NotContains(t, text, "Hill Chart: Progress update", "the constant title is not a column")
}
