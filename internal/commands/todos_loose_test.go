package commands

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	looseProjectPath  = "/99999/projects/123.json"
	looseTodosetPath  = "/99999/buckets/123/todosets/300/todos.json"
	looseTodolistPath = "/99999/todolists/30/todos.json"
)

// looseDockRoute serves the project dock ensureTodoset reads to find the
// to-do set. --loose needs no todolist, so this is the only lookup it makes.
func looseDockRoute() stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   looseProjectPath,
		status: http.StatusOK,
		body: `{"id": 123, "name": "Test Project", "dock": [
			{"name": "todoset", "id": 300, "title": "To-dos", "enabled": true}
		]}`,
	}
}

func looseCreateRoute(path string) stubRoute {
	return stubRoute{
		method: http.MethodPost,
		path:   path,
		status: http.StatusCreated,
		body:   `{"id": 999, "content": "Call the vendor back", "status": "active", "completed": false}`,
	}
}

// --loose creates directly on the to-do set. It resolves a todoset and never
// touches todolist resolution, so no todolist request may appear.
func TestTodosCreateLooseCreatesOnTheTodoset(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t,
		projectsRoute(),
		looseDockRoute(),
		looseCreateRoute(looseTodosetPath),
	)

	require.NoError(t, executeRecordingCommand(NewTodosCmd(), app,
		"create", "Call the vendor back", "--in", "123", "--loose"))

	call := transport.last(t)
	assert.Equal(t, http.MethodPost, call.Method)
	assert.Equal(t, looseTodosetPath, call.Path)
	assert.Contains(t, call.Body, "Call the vendor back")

	for _, recorded := range transport.recorded() {
		assert.NotContains(t, recorded.Path, "/todolists/",
			"--loose must not resolve or create through a todolist")
	}
}

// --list and --loose ask for opposite things: one names a list, the other says
// there is none. Rejecting beats silently honoring whichever is checked first.
func TestTodosCreateLooseRejectsList(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t,
		projectsRoute(),
		looseDockRoute(),
		looseCreateRoute(looseTodosetPath),
	)

	err := executeRecordingCommand(NewTodosCmd(), app,
		"create", "Call the vendor back", "--in", "123", "--loose", "--list", "30")

	outErr := requireBookmarksUsageError(t, err)
	assert.Contains(t, outErr.Message, "--loose")
	assert.Contains(t, outErr.Message, "--list")

	for _, recorded := range transport.recorded() {
		assert.NotEqual(t, http.MethodPost, recorded.Method,
			"a rejected create must not reach the server")
	}
}

// Without --loose the create path is unchanged.
func TestTodosCreateWithoutLooseStillUsesTheTodolist(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t,
		projectsRoute(),
		looseDockRoute(),
		// Only the non-loose path needs this: resolving --list walks the
		// todoset's lists, which is exactly the work --loose skips.
		stubRoute{
			method: http.MethodGet,
			path:   "/99999/todosets/300/todolists.json",
			status: http.StatusOK,
			body:   `[{"id": 30, "name": "Sprint 1"}]`,
		},
		looseCreateRoute(looseTodolistPath),
	)

	require.NoError(t, executeRecordingCommand(NewTodosCmd(), app,
		"create", "Call the vendor back", "--in", "123", "--list", "30"))

	call := transport.last(t)
	assert.Equal(t, looseTodolistPath, call.Path)
}
