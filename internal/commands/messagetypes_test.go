package commands

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

const messageTypeJSON = `{"id":456,"name":"Announcement","icon":"📣","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`

// Message types are bucket-scoped: every operation must forward the resolved
// project as the bucket in the URL. Collection paths carry .json; member
// paths do not (pinned by SDK message_types_test.go).

func TestMessagetypesListHitsBucketScopedPath(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodGet,
		path:   "/99999/buckets/123/categories.json",
		status: http.StatusOK,
		body:   "[" + messageTypeJSON + "]",
	})

	err := executeRecordingCommand(NewMessagetypesCmd(), app, "list", "-p", "123")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodGet, last.Method)
	assert.Equal(t, "/99999/buckets/123/categories.json", last.Path)
}

func TestMessagetypesShowHitsBucketScopedPath(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodGet,
		path:   "/99999/buckets/123/categories/456",
		status: http.StatusOK,
		body:   messageTypeJSON,
	})

	err := executeRecordingCommand(NewMessagetypesCmd(), app, "show", "456", "-p", "123")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodGet, last.Method)
	assert.Equal(t, "/99999/buckets/123/categories/456", last.Path)
}

func TestMessagetypesCreateHitsBucketScopedPath(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodPost,
		path:   "/99999/buckets/123/categories.json",
		status: http.StatusCreated,
		body:   messageTypeJSON,
	})

	err := executeRecordingCommand(NewMessagetypesCmd(), app,
		"create", "Announcement", "--icon", "📣", "-p", "123")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodPost, last.Method)
	assert.Equal(t, "/99999/buckets/123/categories.json", last.Path)
}

func TestMessagetypesUpdateHitsBucketScopedPath(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodPut,
		path:   "/99999/buckets/123/categories/456",
		status: http.StatusOK,
		body:   messageTypeJSON,
	})

	err := executeRecordingCommand(NewMessagetypesCmd(), app,
		"update", "456", "--name", "Heads-up", "-p", "123")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodPut, last.Method)
	assert.Equal(t, "/99999/buckets/123/categories/456", last.Path)
}

func TestMessagetypesDeleteHitsBucketScopedPath(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodDelete,
		path:   "/99999/buckets/123/categories/456",
		status: http.StatusNoContent,
		body:   "",
	})

	err := executeRecordingCommand(NewMessagetypesCmd(), app, "delete", "456", "-p", "123")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodDelete, last.Method)
	assert.Equal(t, "/99999/buckets/123/categories/456", last.Path)
}

// Validation must run before project resolution: a usage error should surface
// without any network traffic.

func TestMessagetypesCreateIconValidationPrecedesResolution(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute())

	err := executeRecordingCommand(NewMessagetypesCmd(), app, "create", "Announcement", "-p", "123")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "--icon is required", e.Message)
	assert.Empty(t, transport.recorded(), "validation should fail before project resolution")
}

func TestMessagetypesUpdateNoChangesPrecedesResolution(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	app, transport := setupRecordingTestApp(t, projectsRoute())

	err := executeRecordingCommand(NewMessagetypesCmd(), app, "update", "456", "-p", "123")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "No update fields specified", e.Message)
	assert.Empty(t, transport.recorded(), "no-changes check should fail before project resolution")
}

func TestMessagetypesShowInvalidIDPrecedesResolution(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute())

	err := executeRecordingCommand(NewMessagetypesCmd(), app, "show", "abc", "-p", "123")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "Invalid message type ID", e.Message)
	assert.Empty(t, transport.recorded(), "ID validation should fail before project resolution")
}
