package commands

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

const (
	messageCategoriesPath = "/99999/buckets/123/categories.json"
	messageCreatePath     = "/99999/message_boards/777/messages.json"
	messageUpdatePath     = "/99999/messages/789"
)

func messageCategoriesRoute(body string) stubRoute {
	return stubRoute{method: http.MethodGet, path: messageCategoriesPath, status: http.StatusOK, body: body}
}

const defaultMessageCategories = `[{"id":7,"name":"Announcement","icon":"📢"},{"id":8,"name":"FYI","icon":"✨"}]`

func setupMessageCreateCategoryApp(t *testing.T, categories string) (*appctx.App, *recordingTransport) {
	t.Helper()
	app, transport := setupRecordingTestApp(t,
		projectsRoute(),
		stubRoute{method: http.MethodGet, path: "/99999/projects/123.json", status: http.StatusOK, body: `{"id":123,"dock":[{"name":"message_board","id":777,"enabled":true}]}`},
		messageCategoriesRoute(categories),
		stubRoute{method: http.MethodPost, path: messageCreatePath, status: http.StatusCreated, body: `{"id":999,"subject":"Hello","status":"active"}`},
	)
	app.Config.ProjectID = "123"
	return app, transport
}

func setupMessageUpdateCategoryApp(t *testing.T, categories string) (*appctx.App, *recordingTransport) {
	t.Helper()
	return setupRecordingTestApp(t,
		projectsRoute(),
		stubRoute{method: http.MethodGet, path: messageUpdatePath, status: http.StatusOK, body: `{"id":789,"subject":"Hello","bucket":{"id":123,"name":"Test Project","type":"Project"}}`},
		messageCategoriesRoute(categories),
		stubRoute{method: http.MethodPut, path: messageUpdatePath, status: http.StatusOK, body: `{"id":789,"subject":"Hello","status":"active"}`},
	)
}

// sentMessageBody decodes the body of the single write the transport saw.
func sentMessageBody(t *testing.T, transport *recordingTransport, method, path string) map[string]any {
	t.Helper()
	var writes []recordedCall
	for _, call := range transport.recorded() {
		if call.Method == method && call.Path == path {
			writes = append(writes, call)
		}
	}
	require.Len(t, writes, 1, "expected exactly one %s %s", method, path)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(writes[0].Body), &body))
	return body
}

func requireNoMessageWrite(t *testing.T, transport *recordingTransport) {
	t.Helper()
	for _, call := range transport.recorded() {
		assert.NotContains(t, []string{http.MethodPost, http.MethodPut}, call.Method,
			"no write expected, saw %s %s", call.Method, call.Path)
	}
}

func TestMessagesCreateCategoryByID(t *testing.T) {
	app, transport := setupMessageCreateCategoryApp(t, defaultMessageCategories)

	require.NoError(t, executeRecordingCommand(NewMessagesCmd(), app, "create", "Hello", "--category", "42"))

	body := sentMessageBody(t, transport, http.MethodPost, messageCreatePath)
	assert.Equal(t, float64(42), body["category_id"])
	assert.Empty(t, messagesRequestsTo(transport, messageCategoriesPath), "a numeric category needs no lookup")
}

func TestMessagesCreateCategoryByName(t *testing.T) {
	app, transport := setupMessageCreateCategoryApp(t, defaultMessageCategories)

	require.NoError(t, executeRecordingCommand(NewMessagesCmd(), app, "create", "Hello", "--category", "announcement"))

	body := sentMessageBody(t, transport, http.MethodPost, messageCreatePath)
	assert.Equal(t, float64(7), body["category_id"])
}

func TestMessagesCreateWithoutCategoryOmitsIt(t *testing.T) {
	app, transport := setupMessageCreateCategoryApp(t, defaultMessageCategories)

	require.NoError(t, executeRecordingCommand(NewMessagesCmd(), app, "create", "Hello"))

	body := sentMessageBody(t, transport, http.MethodPost, messageCreatePath)
	_, ok := body["category_id"]
	assert.False(t, ok)
	assert.Empty(t, messagesRequestsTo(transport, messageCategoriesPath))
}

func TestMessagesCreateCategoryNotFound(t *testing.T) {
	app, transport := setupMessageCreateCategoryApp(t, defaultMessageCategories)

	err := executeRecordingCommand(NewMessagesCmd(), app, "create", "Hello", "--category", "Pitch")

	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Contains(t, e.Hint, "basecamp messagetypes list --in 123")
	assert.Contains(t, e.Hint, "Announcement")
	requireNoMessageWrite(t, transport)
}

func TestMessagesCreateCategoryAmbiguous(t *testing.T) {
	app, transport := setupMessageCreateCategoryApp(t,
		`[{"id":7,"name":"Update","icon":"📢"},{"id":8,"name":"UPDATE","icon":"✨"}]`)

	err := executeRecordingCommand(NewMessagesCmd(), app, "create", "Hello", "--category", "update")

	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Contains(t, e.Hint, "Update (7), UPDATE (8)")
	requireNoMessageWrite(t, transport)
}

func TestMessagesCreateCategoryExactMatchBeatsCaseInsensitive(t *testing.T) {
	app, transport := setupMessageCreateCategoryApp(t,
		`[{"id":7,"name":"Update","icon":"📢"},{"id":8,"name":"UPDATE","icon":"✨"}]`)

	require.NoError(t, executeRecordingCommand(NewMessagesCmd(), app, "create", "Hello", "--category", "UPDATE"))

	body := sentMessageBody(t, transport, http.MethodPost, messageCreatePath)
	assert.Equal(t, float64(8), body["category_id"])
}

func TestMessagesCreateCategoryRejectsBlankAndNonPositive(t *testing.T) {
	for _, value := range []string{"", "  ", "0", "-3", "9223372036854775808", "-9223372036854775809"} {
		t.Run(value, func(t *testing.T) {
			app, transport := setupMessageCreateCategoryApp(t, defaultMessageCategories)

			err := executeRecordingCommand(NewMessagesCmd(), app, "create", "Hello", "--category", value)

			requireMessagesUsageError(t, err, "--category")
			requireNoMessageWrite(t, transport)
		})
	}
}

func TestMessagesUpdateCategoryByID(t *testing.T) {
	app, transport := setupMessageUpdateCategoryApp(t, defaultMessageCategories)

	require.NoError(t, executeRecordingCommand(NewMessagesCmd(), app, "update", "789", "--category", "8"))

	body := sentMessageBody(t, transport, http.MethodPut, messageUpdatePath)
	assert.Equal(t, map[string]any{"category_id": float64(8)}, body)
	assert.Empty(t, messagesRequestsTo(transport, messageCategoriesPath), "a numeric category needs no lookup")
}

func TestMessagesUpdateCategoryByNameUsesMessageProject(t *testing.T) {
	app, transport := setupMessageUpdateCategoryApp(t, defaultMessageCategories)

	require.NoError(t, executeRecordingCommand(NewMessagesCmd(), app, "update", "789", "--category", "fyi"))

	body := sentMessageBody(t, transport, http.MethodPut, messageUpdatePath)
	assert.Equal(t, map[string]any{"category_id": float64(8)}, body)
	assert.Len(t, messagesRequestsTo(transport, messageCategoriesPath), 1)
}

func TestMessagesUpdateNoCategoryClears(t *testing.T) {
	app, transport := setupMessageUpdateCategoryApp(t, defaultMessageCategories)

	require.NoError(t, executeRecordingCommand(NewMessagesCmd(), app, "update", "789", "--no-category"))

	body := sentMessageBody(t, transport, http.MethodPut, messageUpdatePath)
	assert.Equal(t, map[string]any{"category_id": ""}, body)
}

func TestMessagesUpdateCategoryWithTitle(t *testing.T) {
	app, transport := setupMessageUpdateCategoryApp(t, defaultMessageCategories)

	require.NoError(t, executeRecordingCommand(NewMessagesCmd(), app, "update", "789", "--title", "New", "--category", "Announcement"))

	body := sentMessageBody(t, transport, http.MethodPut, messageUpdatePath)
	assert.Equal(t, "New", body["subject"])
	assert.Equal(t, float64(7), body["category_id"])
}

func TestMessagesUpdateCategoryConflictsWithNoCategory(t *testing.T) {
	app, transport := setupMessageUpdateCategoryApp(t, defaultMessageCategories)

	err := executeRecordingCommand(NewMessagesCmd(), app, "update", "789", "--category", "8", "--no-category")

	requireMessagesUsageError(t, err, "--no-category")
	assert.Empty(t, transport.recorded())
}

func TestMessagesUpdateCategoryNotFound(t *testing.T) {
	app, transport := setupMessageUpdateCategoryApp(t, defaultMessageCategories)

	err := executeRecordingCommand(NewMessagesCmd(), app, "update", "789", "--category", "Pitch")

	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeNotFound, e.Code)
	requireNoMessageWrite(t, transport)
}

func TestMessagesUpdateCategoryRejectsOutOfRangeID(t *testing.T) {
	for _, value := range []string{"0", "9223372036854775808", "-9223372036854775809"} {
		t.Run(value, func(t *testing.T) {
			app, transport := setupMessageUpdateCategoryApp(t, defaultMessageCategories)

			err := executeRecordingCommand(NewMessagesCmd(), app, "update", "789", "--category", value)

			requireMessagesUsageError(t, err, "--category")
			assert.Empty(t, transport.recorded())
		})
	}
}

// An explicit --message-board may belong to a different project than --in;
// the message lands on that board, so a category name is matched against the
// board's own project.
func TestMessagesCreateCategoryNameUsesExplicitBoardProject(t *testing.T) {
	app, transport := setupRecordingTestApp(t,
		projectsRoute(),
		stubRoute{method: http.MethodGet, path: "/99999/message_boards/555", status: http.StatusOK, body: `{"id":555,"bucket":{"id":456,"name":"Other Project","type":"Project"}}`},
		messageCategoriesRoute(defaultMessageCategories),
		stubRoute{method: http.MethodGet, path: "/99999/buckets/456/categories.json", status: http.StatusOK, body: `[{"id":70,"name":"Announcement","icon":"📢"}]`},
		stubRoute{method: http.MethodPost, path: "/99999/message_boards/555/messages.json", status: http.StatusCreated, body: `{"id":999,"subject":"Hello","status":"active"}`},
	)
	app.Config.ProjectID = "123"

	require.NoError(t, executeRecordingCommand(NewMessagesCmd(), app, "create", "Hello", "--message-board", "555", "--category", "Announcement"))

	body := sentMessageBody(t, transport, http.MethodPost, "/99999/message_boards/555/messages.json")
	assert.Equal(t, float64(70), body["category_id"])
	assert.Empty(t, messagesRequestsTo(transport, messageCategoriesPath), "the --in project's types are not the board's")
}

func TestMessagesCreateCategoryNameRefusesBoardOfUnknownProject(t *testing.T) {
	app, transport := setupRecordingTestApp(t,
		projectsRoute(),
		stubRoute{method: http.MethodGet, path: "/99999/message_boards/555", status: http.StatusOK, body: `{"id":555}`},
		messageCategoriesRoute(defaultMessageCategories),
		stubRoute{method: http.MethodPost, path: "/99999/message_boards/555/messages.json", status: http.StatusCreated, body: `{"id":999,"subject":"Hello","status":"active"}`},
	)
	app.Config.ProjectID = "123"

	err := executeRecordingCommand(NewMessagesCmd(), app, "create", "Hello", "--message-board", "555", "--category", "Announcement")

	requireMessagesUsageError(t, err, "Cannot tell which project")
	requireNoMessageWrite(t, transport)
	assert.Empty(t, messagesRequestsTo(transport, messageCategoriesPath))
}
