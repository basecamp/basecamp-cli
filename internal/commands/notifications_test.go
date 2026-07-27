package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
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

// notifications read resolves IDs from the notification feed, not the
// dedicated Bubble Ups endpoint, so bubbleups must not advertise a read
// breadcrumb — it would fail for any bubble-up not on the feed's first page.
func TestNotificationsBubbleupsOffersNoReadBreadcrumb(t *testing.T) {
	app, _ := setupRecordingTestApp(t, stubRoute{
		method: http.MethodGet,
		path:   "/99999/my/readings/bubble_ups.json",
		status: http.StatusOK,
		body:   `[{"id":1,"title":"Bubbled up","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]`,
	})
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})
	app.Flags.Hints = true

	err := executeRecordingCommand(NewNotificationsCmd(), app, "bubbleups")
	require.NoError(t, err)

	var envelope struct {
		Breadcrumbs []struct {
			Action string `json:"action"`
		} `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	require.NotEmpty(t, envelope.Breadcrumbs)

	for _, bc := range envelope.Breadcrumbs {
		assert.NotEqual(t, "read", bc.Action,
			"read cannot resolve IDs from the dedicated bubble-ups endpoint")
	}
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

func TestNotificationsListLimitBubbleUpsNextBreadcrumbKeepsFlag(t *testing.T) {
	app, _ := setupRecordingTestApp(t, stubRoute{
		method: http.MethodGet,
		path:   "/99999/my/readings.json",
		status: http.StatusOK,
		body: `{"unreads":[{"id":1,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}],` +
			`"reads":[],"bubble_ups":[{"id":2,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}],` +
			`"bubble_ups_count":5,"scheduled_bubble_ups_count":3}`,
	})
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})
	app.Flags.Hints = true

	err := executeRecordingCommand(NewNotificationsCmd(), app, "list", "--limit-bubble-ups")
	require.NoError(t, err)

	var envelope struct {
		Summary     string `json:"summary"`
		Breadcrumbs []struct {
			Action string `json:"action"`
			Cmd    string `json:"cmd"`
		} `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))

	// The headline total must come from the uncapped count fields
	// (1 unread + 5 bubble-ups + 3 scheduled), not the capped arrays.
	assert.Contains(t, envelope.Summary, "9 notification(s)")
	assert.Contains(t, envelope.Summary, "1 of 5 bubble-up(s)")

	var nextCmd string
	for _, bc := range envelope.Breadcrumbs {
		if bc.Action == "next" {
			nextCmd = bc.Cmd
		}
	}
	assert.Contains(t, nextCmd, "--limit-bubble-ups",
		"next-page breadcrumb must preserve the cap")
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
