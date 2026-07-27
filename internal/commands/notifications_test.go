package commands

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotificationsBubbleupsHitsDedicatedEndpoint(t *testing.T) {
	app, transport := setupRecordingTestApp(t, stubRoute{
		method: http.MethodGet,
		path:   "/99999/my/readings/bubble_ups.json",
		status: http.StatusOK,
		body:   `[{"id":1,"title":"Bubbled up","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]`,
	})

	err := executeRecordingCommand(NewNotificationsCmd(), app, "bubbleups")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodGet, last.Method)
	assert.Equal(t, "/99999/my/readings/bubble_ups.json", last.Path)
}

func TestNotificationsListLimitBubbleUpsSendsQueryParam(t *testing.T) {
	app, transport := setupRecordingTestApp(t, stubRoute{
		method: http.MethodGet,
		path:   "/99999/my/readings.json",
		status: http.StatusOK,
		body:   `{"unreads":[],"reads":[],"bubble_ups_count":5,"scheduled_bubble_ups_count":3}`,
	})

	err := executeRecordingCommand(NewNotificationsCmd(), app, "list", "--limit-bubble-ups")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodGet, last.Method)
	assert.Equal(t, "/99999/my/readings.json", last.Path)
	assert.Contains(t, last.Query, "limit_bubble_ups=true")
}

func TestNotificationsListWithoutLimitOmitsQueryParam(t *testing.T) {
	app, transport := setupRecordingTestApp(t, stubRoute{
		method: http.MethodGet,
		path:   "/99999/my/readings.json",
		status: http.StatusOK,
		body:   `{"unreads":[],"reads":[]}`,
	})

	err := executeRecordingCommand(NewNotificationsCmd(), app, "list")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, "/99999/my/readings.json", last.Path)
	assert.NotContains(t, last.Query, "limit_bubble_ups")
}
