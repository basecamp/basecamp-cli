package commands

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduleEntryHighlightAndJoinURL(t *testing.T) {
	for _, action := range []string{"create", "update"} {
		for _, tc := range []struct {
			name        string
			flags       []string
			highlighted any
			url         any
		}{
			{name: "omitted"},
			{name: "set", flags: []string{"--highlight", "--join-url", "https://meet.example.com/planning"}, highlighted: true, url: "https://meet.example.com/planning"},
			{name: "clear", flags: []string{"--highlight=false", "--join-url="}, highlighted: false, url: ""},
			{name: "highlight only", flags: []string{"--highlight=false"}, highlighted: false},
			{name: "link only", flags: []string{"--join-url="}, url: ""},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				tr := &mockScheduleCreateTransport{}
				app, _ := newTestAppWithTransport(t, tr)
				args := []string{"create", "Event", "--starts-at", "2026-03-04T09:00:00Z", "--ends-at", "2026-03-04T09:30:00Z"}
				if action == "update" {
					args = []string{"update", "999", "--summary", "Updated"}
				}
				require.NoError(t, executeRecordingCommand(NewScheduleCmd(), app, append(args, tc.flags...)...))
				var body map[string]any
				require.NoError(t, json.Unmarshal(tr.capturedBody, &body))
				for key, expected := range map[string]any{"highlighted": tc.highlighted, "url": tc.url} {
					if expected == nil {
						assert.NotContains(t, body, key)
					} else {
						assert.Equal(t, expected, body[key])
					}
				}
				assert.NotContains(t, body, "join_url")
				if action == "update" {
					assert.NotContains(t, body, "status")
					assert.Equal(t, "Updated", body["summary"])
				}
			})
		}
	}
}

func TestScheduleUpdateOptionsAlone(t *testing.T) {
	for _, flags := range [][]string{{"--highlight"}, {"--highlight=false"}, {"--join-url", "https://meet.example.com/room"}, {"--join-url="}} {
		tr := &mockScheduleCreateTransport{}
		app, _ := newTestAppWithTransport(t, tr)
		require.NoError(t, executeRecordingCommand(NewScheduleCmd(), app, append([]string{"update", "999"}, flags...)...))
		require.NotEmpty(t, tr.capturedBody)
	}
}
