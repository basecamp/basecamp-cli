package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

func TestResolveReportsScheduleWindow(t *testing.T) {
	now := time.Date(2026, time.March, 25, 9, 30, 0, 0, time.UTC)

	tests := []struct {
		name      string
		startDate string
		endDate   string
		wantStart string
		wantEnd   string
	}{
		{
			name:      "defaults to today plus 30 days",
			wantStart: "2026-03-25",
			wantEnd:   "2026-04-24",
		},
		{
			name:      "default end is anchored to resolved start",
			startDate: "next month",
			wantStart: "2026-04-25",
			wantEnd:   "2026-05-25",
		},
		{
			name:      "explicit absolute start gets 30 day default end",
			startDate: "2026-04-10",
			wantStart: "2026-04-10",
			wantEnd:   "2026-05-10",
		},
		{
			name:      "explicit end is preserved",
			startDate: "next month",
			endDate:   "2026-05-01",
			wantStart: "2026-04-25",
			wantEnd:   "2026-05-01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStart, gotEnd := resolveReportsScheduleWindow(tt.startDate, tt.endDate, now)
			assert.Equal(t, tt.wantStart, gotStart)
			assert.Equal(t, tt.wantEnd, gotEnd)
		})
	}
}

const overdueReportPage = `{"under_a_week_late":[{"id":1,"content":"One"}],"over_a_week_late":[],` +
	`"over_a_month_late":[{"id":2,"content":"Two"}],"over_three_months_late":[]}`

type overdueReportEnvelope struct {
	Summary     string `json:"summary"`
	Breadcrumbs []struct {
		Action string `json:"action"`
		Cmd    string `json:"cmd"`
	} `json:"breadcrumbs"`
}

func runReportsOverdue(t *testing.T, header http.Header, args ...string) (overdueReportEnvelope, recordedCall) {
	t.Helper()
	app, transport := setupRecordingTestApp(t, stubRoute{
		method: http.MethodGet,
		path:   "/99999/reports/todos/overdue.json",
		status: http.StatusOK,
		body:   overdueReportPage,
		header: header,
	})
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})
	app.Flags.Hints = true

	require.NoError(t, executeRecordingCommand(NewReportsCmd(), app, append([]string{"overdue"}, args...)...))

	var envelope overdueReportEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	return envelope, transport.last(t)
}

func overdueBreadcrumb(envelope overdueReportEnvelope, action string) string {
	for _, bc := range envelope.Breadcrumbs {
		if bc.Action == action {
			return bc.Cmd
		}
	}
	return ""
}

func TestReportsOverduePageWithMoreNamesTheNextPage(t *testing.T) {
	envelope, request := runReportsOverdue(t, http.Header{
		"Link":          []string{`<https://3.basecampapi.com/99999/reports/todos/overdue.json?page=2>; rel="next"`},
		"X-Total-Count": []string{"21"},
	})

	assert.Empty(t, request.Query)
	assert.Equal(t, "2 of 21 overdue todos (1 <1 week, 1 >1 month)", envelope.Summary)
	assert.Equal(t, "basecamp reports overdue --page 2", overdueBreadcrumb(envelope, "next"))
}

func TestReportsOverdueLaterPageNamesThePageAfterIt(t *testing.T) {
	envelope, request := runReportsOverdue(t, http.Header{
		"Link":          []string{`<https://3.basecampapi.com/99999/reports/todos/overdue.json?page=3>; rel="next"`},
		"X-Total-Count": []string{"95"},
	}, "--page", "2")

	assert.Equal(t, "page=2", request.Query)
	assert.Equal(t, "2 of 95 overdue todos (1 <1 week, 1 >1 month)", envelope.Summary)
	assert.Equal(t, "basecamp reports overdue --page 3", overdueBreadcrumb(envelope, "next"))
}

func TestReportsOverdueWholeReportOnOnePageNamesNoNextPage(t *testing.T) {
	envelope, _ := runReportsOverdue(t, nil)

	assert.Equal(t, "2 overdue todos (1 <1 week, 1 >1 month)", envelope.Summary)
	assert.Empty(t, overdueBreadcrumb(envelope, "next"))
}
