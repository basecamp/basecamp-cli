package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

type scheduleDraftTransport struct {
	mockScheduleCreateTransport
	writes int
}

func (tr *scheduleDraftTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := tr.mockScheduleCreateTransport.RoundTrip(req)
	if err != nil || req.Method != http.MethodPost {
		return resp, err
	}
	tr.writes++
	var body map[string]any
	if err := json.Unmarshal(tr.capturedBody, &body); err != nil {
		return nil, err
	}
	status, _ := body["status"].(string)
	if status == "" {
		status = "active"
	}
	resp.Body.Close()
	resp.Body = io.NopCloser(strings.NewReader(fmt.Sprintf(`{"id":999,"status":%q,"summary":"Event","app_url":"https://3.basecamp.com/99999/buckets/123/schedule_entries/999","starts_at":"2026-03-04T09:00:00Z","ends_at":"2026-03-04T09:30:00Z"}`, status)))
	return resp, nil
}

func TestScheduleCreatePublicationState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		flags  []string
		status string
	}{
		{"published by default", nil, "active"},
		{"explicit draft", []string{"--draft"}, "drafted"},
		{"explicit false publishes", []string{"--draft=false"}, "active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &scheduleDraftTransport{}
			app, buf := newTestAppWithTransport(t, tr)
			app.Flags.Hints = true
			app.Config.ActiveProfile = "alternate"
			app.Config.Sources = map[string]string{"account_id": string(config.SourceFlag)}
			args := append([]string{"create", "Event", "--starts-at", "2026-03-04T09:00:00Z", "--ends-at", "2026-03-04T09:30:00Z"}, tc.flags...)
			require.NoError(t, executeRecordingCommand(NewScheduleCmd(), app, args...))
			require.Equal(t, 1, tr.writes)
			var body map[string]any
			require.NoError(t, json.Unmarshal(tr.capturedBody, &body))
			assert.Equal(t, tc.status, body["status"])
			var out struct {
				Data struct {
					ID     int64  `json:"id"`
					Status string `json:"status"`
					AppURL string `json:"app_url"`
				} `json:"data"`
				Summary     string `json:"summary"`
				Notice      string `json:"notice"`
				Breadcrumbs []struct {
					Cmd string `json:"cmd"`
				} `json:"breadcrumbs"`
			}
			require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
			assert.Equal(t, int64(999), out.Data.ID)
			assert.Equal(t, tc.status, out.Data.Status)
			assert.Equal(t, "https://3.basecamp.com/99999/buckets/123/schedule_entries/999", out.Data.AppURL)
			require.NotEmpty(t, out.Breadcrumbs)
			assert.Contains(t, out.Breadcrumbs[0].Cmd, "schedule show 999")
			assert.Contains(t, out.Breadcrumbs[0].Cmd, "--profile alternate --account 99999")
			if tc.status == "drafted" {
				assert.Contains(t, out.Summary, "draft")
				assert.Contains(t, out.Notice, "edit and publish")
				assert.Len(t, out.Breadcrumbs, 1, "ordinary listings omit drafts")
			} else {
				assert.NotContains(t, out.Summary, "draft")
			}
		})
	}
}

func TestScheduleCreateDraftDoesNotWarnInQuietMode(t *testing.T) {
	tr := &scheduleDraftTransport{}
	app, _ := newTestAppWithTransport(t, tr)
	var stdout, stderr bytes.Buffer
	app.Output = output.New(output.Options{Format: output.FormatQuiet, Writer: &stdout, ErrWriter: &stderr})
	require.NoError(t, executeRecordingCommand(NewScheduleCmd(), app, "create", "Event", "--starts-at", "2026-03-04T09:00:00Z", "--ends-at", "2026-03-04T09:30:00Z", "--draft"))
	assert.Empty(t, stderr.String())
	var entry struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &entry))
	assert.Equal(t, "drafted", entry.Status)
}
