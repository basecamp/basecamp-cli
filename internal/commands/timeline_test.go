package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// progressTransport records the query of each progress.json request.
type progressTransport struct {
	queries []url.Values
}

func (p *progressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := http.Header{"Content-Type": []string{"application/json"}}
	if req.URL.Path != "/99999/reports/progress.json" {
		return &http.Response{StatusCode: http.StatusNotFound, Header: header, Body: io.NopCloser(strings.NewReader(`{}`)), Request: req}, nil
	}
	p.queries = append(p.queries, req.URL.Query())
	return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`[]`)), Request: req}, nil
}

func firstBreadcrumb(t *testing.T, buf *bytes.Buffer) string {
	t.Helper()
	var envelope struct {
		Breadcrumbs []struct {
			Cmd string `json:"cmd"`
		} `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	require.NotEmpty(t, envelope.Breadcrumbs)
	return envelope.Breadcrumbs[0].Cmd
}

func TestTimelineDateReadsThatDay(t *testing.T) {
	transport := &progressTransport{}
	app, buf := newTestAppWithTransport(t, transport)
	app.Flags.Hints = true

	require.NoError(t, executeCommand(NewTimelineCmd(), app, "--date", "2026-08-30"))

	require.Len(t, transport.queries, 1)
	assert.Equal(t, "2026-08-30", transport.queries[0].Get("date"))
	assert.Equal(t, "basecamp timeline --date 2026-08-29", firstBreadcrumb(t, buf))
}

func TestTimelineWithoutDateReadsToday(t *testing.T) {
	transport := &progressTransport{}
	app, buf := newTestAppWithTransport(t, transport)
	app.Flags.Hints = true

	require.NoError(t, executeCommand(NewTimelineCmd(), app))

	require.Len(t, transport.queries, 1)
	assert.False(t, transport.queries[0].Has("date"))
	// Undated, the day is Basecamp's today, which this machine's clock can't
	// name, so the breadcrumb can't name the day before it either.
	assert.Equal(t, "basecamp timeline --date <YYYY-MM-DD>", firstBreadcrumb(t, buf))
}

func TestTimelineDateRefusals(t *testing.T) {
	for name, args := range map[string][]string{
		"not a date":  {"--date", "someday"},
		"in future":   {"--date", "tomorrow"},
		"for project": {"--date", "2026-08-30", "--in", "123"},
		"for person":  {"--date", "2026-08-30", "me"},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &progressTransport{}
			app, _ := newTestAppWithTransport(t, transport)

			err := executeCommand(NewTimelineCmd(), app, args...)

			var e *output.Error
			require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
			assert.Equal(t, output.CodeUsage, e.Code)
			assert.Empty(t, transport.queries)
		})
	}
}
