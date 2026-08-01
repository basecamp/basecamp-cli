package commands

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
)

// Account-wide --assignee and --due.
//
// These are the feature the v0.12.0 signature change exists for. Two properties
// matter and are asserted separately: the filters must reach the wire as real
// query parameters, and, served identical pages, they must not change the shape
// of the bounded walk.
//
// The second is deliberately narrower than "the request count is the same".
// Against a real account it is not: the cap counts items, so a narrower filter
// can return fewer per page and need another page to reach it — a production
// soak measured 2 requests for 100 unfiltered todos against 3 for --assignee's
// 21. What is pinned here is the algorithm, on a same-fixture comparison.

const (
	openTodosPath = "/99999/todos/open.json"
	openCardsPath = "/99999/cards/open.json"
)

func cardsAccountWideFilterRoute(path string) stubRoute {
	body := `[{"bucket":{"id":977190,"name":"JD test proj","type":"Project"},"cards":[{"id":1,"title":"A card"}]}]`
	return stubRoute{
		method: http.MethodGet,
		path:   path,
		status: http.StatusOK,
		body:   body,
		pages:  []string{body},
	}
}

// queryValues parses one recorded query string.
func queryValues(t *testing.T, raw string) url.Values {
	t.Helper()
	values, err := url.ParseQuery(raw)
	require.NoError(t, err)
	return values
}

func TestTodosListAccountWideSendsAssigneeIDs(t *testing.T) {
	app, transport := setupRecordingTestApp(t,
		accountWideTodosRoute(openTodosPath, todosGroupsBody(1)))

	require.NoError(t, executeRecordingCommand(NewTodosCmd(), app,
		"list", "--assignee", "42", "--assignee", "43"))

	queries := transport.queriesFor(openTodosPath)
	require.NotEmpty(t, queries)
	values := queryValues(t, queries[0])
	assert.Equal(t, []string{"42", "43"}, values["assignee_ids[]"],
		"both people must reach the wire — --assignee is repeatable")
}

// A single value may itself be comma-separated, matching how the other
// people-taking flags already behave.
func TestTodosListAccountWideAcceptsCommaSeparatedAssignees(t *testing.T) {
	app, transport := setupRecordingTestApp(t,
		accountWideTodosRoute(openTodosPath, todosGroupsBody(1)))

	require.NoError(t, executeRecordingCommand(NewTodosCmd(), app, "list", "--assignee", "42,43"))

	values := queryValues(t, transport.queriesFor(openTodosPath)[0])
	assert.Equal(t, []string{"42", "43"}, values["assignee_ids[]"])
}

func TestTodosListAccountWideSendsDue(t *testing.T) {
	for _, due := range []string{"with", "without", "overdue"} {
		t.Run(due, func(t *testing.T) {
			app, transport := setupRecordingTestApp(t,
				accountWideTodosRoute(openTodosPath, todosGroupsBody(1)))

			require.NoError(t, executeRecordingCommand(NewTodosCmd(), app, "list", "--due", due))

			values := queryValues(t, transport.queriesFor(openTodosPath)[0])
			assert.Equal(t, due, values.Get("due"))
		})
	}
}

func TestCardsListAccountWideSendsFilters(t *testing.T) {
	app, transport := setupRecordingTestApp(t, cardsAccountWideFilterRoute(openCardsPath))

	require.NoError(t, executeRecordingCommand(NewCardsCmd(), app,
		"list", "--all-projects", "--assignee", "42", "--due", "with"))

	queries := transport.queriesFor(openCardsPath)
	require.NotEmpty(t, queries)
	values := queryValues(t, queries[0])
	assert.Equal(t, []string{"42"}, values["assignee_ids[]"])
	assert.Equal(t, "with", values.Get("due"))
}

// An unfiltered call must stay byte-identical to what it was before the filters
// existed: no filter passed means no filter parameter on the wire.
func TestAccountWideListingsOmitFilterParamsWhenUnused(t *testing.T) {
	t.Run("todos", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t,
			accountWideTodosRoute(openTodosPath, todosGroupsBody(1)))

		require.NoError(t, executeRecordingCommand(NewTodosCmd(), app, "list"))

		for _, q := range transport.queriesFor(openTodosPath) {
			assert.NotContains(t, q, "assignee_ids")
			assert.NotContains(t, q, "due=")
		}
	})

	t.Run("cards", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, cardsAccountWideFilterRoute(openCardsPath))

		require.NoError(t, executeRecordingCommand(NewCardsCmd(), app, "list", "--all-projects"))

		for _, q := range transport.queriesFor(openCardsPath) {
			assert.NotContains(t, q, "assignee_ids")
			assert.NotContains(t, q, "due=")
		}
	})
}

// A server-side filter must not deepen the walk. Served the same page contents,
// a filtered listing walks exactly as far as an unfiltered one — the filter
// changes what the server selects, not how the client paginates.
//
// This is a same-fixture comparison on purpose. Against a real account the
// counts can differ by a page, because the cap counts items and a narrower
// filter returns fewer per page; that is the bounded walk working. What this
// pins is that filtering does not change the walk's shape.
func TestAccountWideFiltersDoNotDeepenTheWalk(t *testing.T) {
	countRequests := func(t *testing.T, args ...string) int {
		t.Helper()
		app, transport := setupRecordingTestApp(t,
			accountWideTodosRoute(openTodosPath, todosGroupsBody(1)))
		require.NoError(t, executeRecordingCommand(NewTodosCmd(), app, args...))
		return len(transport.queriesFor(openTodosPath))
	}

	unfiltered := countRequests(t, "list")
	filtered := countRequests(t, "list", "--assignee", "42", "--due", "with")

	assert.Equal(t, unfiltered, filtered,
		"given identical page contents, filtering must not change the walk")
}

// --assignee intersected with --unassigned is necessarily empty: the server
// builds the unassigned selector over a relation the assignee filter has
// already narrowed, so nothing can satisfy both. Returning zero rows would look
// like a real answer, so the combination is refused before any request.
func TestAccountWideRejectsAssigneeWithUnassigned(t *testing.T) {
	t.Run("todos", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t,
			accountWideTodosRoute("/99999/todos/unassigned.json", todosGroupsBody(1)))

		err := executeRecordingCommand(NewTodosCmd(), app, "list", "--unassigned", "--assignee", "42")

		outErr := requireBookmarksUsageError(t, err)
		assert.Contains(t, outErr.Message, "--assignee and --unassigned")
		assert.Empty(t, transport.recorded(), "an impossible query must not be issued")
	})

	t.Run("cards", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t,
			cardsAccountWideFilterRoute("/99999/cards/unassigned.json"))

		err := executeRecordingCommand(NewCardsCmd(), app,
			"list", "--all-projects", "--unassigned", "--assignee", "42")

		outErr := requireBookmarksUsageError(t, err)
		assert.Contains(t, outErr.Message, "--assignee and --unassigned")
		assert.Empty(t, transport.recorded(), "an impossible query must not be issued")
	})
}

// --due names the same axis as the dedicated due-date selectors, each of which
// picks its own endpoint. Combining them asks two endpoints for one answer.
func TestAccountWideRejectsDueWithDueDateSelectors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"todos --due with --overdue", []string{"list", "--overdue", "--due", "with"}},
		{"todos --due with --no-due-date", []string{"list", "--no-due-date", "--due", "with"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, transport := setupRecordingTestApp(t,
				accountWideTodosRoute("/99999/todos/overdue.json", todosGroupsBody(1)),
				accountWideTodosRoute("/99999/todos/without_due_date.json", todosGroupsBody(1)))

			err := executeRecordingCommand(NewTodosCmd(), app, tc.args...)

			outErr := requireBookmarksUsageError(t, err)
			assert.Contains(t, outErr.Message, "--due and")
			assert.Empty(t, transport.recorded())
		})
	}
}

func TestAccountWideRejectsUnknownDueToken(t *testing.T) {
	app, transport := setupRecordingTestApp(t,
		accountWideTodosRoute(openTodosPath, todosGroupsBody(1)))

	err := executeRecordingCommand(NewTodosCmd(), app, "list", "--due", "tomorrow")

	outErr := requireBookmarksUsageError(t, err)
	assert.Contains(t, outErr.Message, "tomorrow")
	assert.Contains(t, outErr.Hint, "with, without, overdue",
		"the hint must name the tokens, since these are categories rather than dates")
	assert.Empty(t, transport.recorded())
}

// Both flags are account-wide only on cards, and --due is account-wide only on
// todos too. A project in scope makes them unanswerable, so they are refused by
// name rather than ignored.
func TestProjectScopedRejectsAccountWideOnlyFilters(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cmd     func() *cobra.Command
		args    []string
		wantMsg string
	}{
		{
			name:    "cards --assignee",
			cmd:     NewCardsCmd,
			args:    []string{"list", "--in", "123", "--assignee", "42"},
			wantMsg: "--assignee filters the account-wide card listing only",
		},
		{
			name:    "cards --due",
			cmd:     NewCardsCmd,
			args:    []string{"list", "--in", "123", "--due", "with"},
			wantMsg: "--due filters the account-wide card listing only",
		},
		{
			name:    "todos --due",
			cmd:     NewTodosCmd,
			args:    []string{"list", "--in", "123", "--due", "with"},
			wantMsg: "--due filters the account-wide listing only",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, transport := setupRecordingTestApp(t, projectsRoute())

			err := executeRecordingCommand(tc.cmd(), app, tc.args...)

			outErr := requireBookmarksUsageError(t, err)
			assert.Contains(t, outErr.Message, tc.wantMsg)

			for _, call := range transport.recorded() {
				assert.NotContains(t, strings.Join([]string{call.Path, call.Query}, "?"), "assignee_ids")
			}
		})
	}
}

// `--due=` is a request that names nothing, and every other check in this file
// tests the flag's *value* — which makes an explicit empty string
// indistinguishable from never passing the flag at all. Left alone it slipped
// past the project-scoped guard, built no account-wide filter, and returned a
// full unfiltered listing to a caller who believed they had narrowed it.
//
// Both nouns and both scopes, because the two scopes reject on different paths:
// account-wide never had a guard, and the project-scoped one keys off a
// non-empty value.
func TestEmptyTaskFilterValuesAreRefusedBeforeAnyRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  func() *cobra.Command
		args []string
		want string
	}{
		{"todos account-wide --due=", NewTodosCmd, []string{"list", "--all-projects", "--due", ""}, "--due needs a value"},
		{"todos project-scoped --due=", NewTodosCmd, []string{"list", "--in", "123", "--due", ""}, "--due needs a value"},
		{"cards account-wide --due=", NewCardsCmd, []string{"list", "--all-projects", "--due", ""}, "--due needs a value"},
		{"cards project-scoped --due=", NewCardsCmd, []string{"list", "--in", "123", "--due", ""}, "--due needs a value"},
		{"todos --assignee=", NewTodosCmd, []string{"list", "--all-projects", "--assignee", ""}, "--assignee needs a value"},
		{"cards --assignee=", NewCardsCmd, []string{"list", "--all-projects", "--assignee", ""}, "--assignee needs a value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, transport := setupRecordingTestApp(t,
				accountWideTodosRoute(openTodosPath, todosGroupsBody(1)))

			err := executeRecordingCommand(tc.cmd(), app, tc.args...)

			outErr := requireBookmarksUsageError(t, err)
			assert.Contains(t, outErr.Message, tc.want)
			assert.Empty(t, transport.recorded(),
				"an empty filter value must be refused before the account is resolved")
		})
	}
}

// setupNoAccountTestApp is setupRecordingTestApp with no configured account, so
// anything reaching ensureAccount fails with "--account is required".
func setupNoAccountTestApp(t *testing.T, routes ...stubRoute) (*appctx.App, *recordingTransport) {
	t.Helper()
	app, transport := setupRecordingTestApp(t, routes...)
	app.Config.AccountID = ""
	return app, transport
}

// A filter usage error that needs no account must not demand one.
//
// The token and emptiness checks depend on neither the account nor the scope,
// but they used to run inside the account-wide path — after ensureAccount. With
// nothing configured, `todos list --due tomorrow` therefore hit account
// resolution first: an interactive session got the account picker, a
// noninteractive one got "--account is required", and the actual mistake was
// never reported. Asserting on the message is the point; asserting on the
// absence of requests alone would have passed before the fix too.
func TestTaskFilterUsageErrorsPrecedeAccountResolution(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  func() *cobra.Command
		args []string
		want string
	}{
		{"todos unknown --due token", NewTodosCmd, []string{"list", "--due", "tomorrow"}, "tomorrow"},
		{"cards unknown --due token", NewCardsCmd, []string{"list", "--due", "tomorrow"}, "tomorrow"},
		{"todos empty --due", NewTodosCmd, []string{"list", "--due", ""}, "--due needs a value"},
		{"cards empty --assignee", NewCardsCmd, []string{"list", "--assignee", ""}, "--assignee needs a value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, transport := setupNoAccountTestApp(t,
				accountWideTodosRoute(openTodosPath, todosGroupsBody(1)))

			err := executeRecordingCommand(tc.cmd(), app, tc.args...)

			outErr := requireBookmarksUsageError(t, err)
			assert.Contains(t, outErr.Message, tc.want)
			assert.NotContains(t, outErr.Message, "account is required",
				"the filter mistake must be reported, not masked by account resolution")
			assert.Empty(t, transport.recorded())
		})
	}
}
