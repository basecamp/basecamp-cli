package commands

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

var scheduleTimes = []string{"--starts-at", "2026-03-04T09:00:00Z", "--ends-at", "2026-03-04T09:30:00Z"}

// runScheduleWrite runs a schedule subcommand against the capturing mock and
// returns the body of its POST or PUT.
func runScheduleWrite(t *testing.T, args ...string) map[string]any {
	t.Helper()
	transport := &mockScheduleCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	require.NoError(t, executeMessagesCommand(NewScheduleCmd(), app, args...))
	require.NotEmpty(t, transport.capturedBody, "expected a write request")

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	return body
}

func scheduleCreateArgs(extra ...string) []string {
	return append(append([]string{"create", "Event"}, scheduleTimes...), extra...)
}

func TestScheduleCreateOmitsHighlightAndJoinURLByDefault(t *testing.T) {
	body := runScheduleWrite(t, scheduleCreateArgs()...)

	assert.NotContains(t, body, "highlighted")
	assert.NotContains(t, body, "url")
}

func TestScheduleCreateHighlight(t *testing.T) {
	body := runScheduleWrite(t, scheduleCreateArgs("--highlight")...)
	assert.Equal(t, true, body["highlighted"])

	body = runScheduleWrite(t, scheduleCreateArgs("--highlight=false")...)
	assert.Equal(t, false, body["highlighted"])
}

func TestScheduleCreateJoinURL(t *testing.T) {
	body := runScheduleWrite(t, scheduleCreateArgs("--join-url", "https://meet.example.com/abc")...)

	// The join link is spelled url on the way in; join_url is dropped by BC3.
	assert.Equal(t, "https://meet.example.com/abc", body["url"])
	assert.NotContains(t, body, "join_url")
}

func TestScheduleCreateEmptyJoinURLIsOmitted(t *testing.T) {
	body := runScheduleWrite(t, scheduleCreateArgs("--join-url", "")...)
	assert.NotContains(t, body, "url")
}

func TestScheduleUpdateLeavesHighlightAndJoinURLAlone(t *testing.T) {
	body := runScheduleWrite(t, "update", "999", "--summary", "Renamed", "--project", "123")

	// Omission is how BC3 preserves both on its full-replace PUT.
	assert.NotContains(t, body, "highlighted")
	assert.NotContains(t, body, "url")
	assert.Equal(t, "Renamed", body["summary"])
}

func TestScheduleUpdateHighlight(t *testing.T) {
	for _, tc := range []struct {
		flag string
		want bool
	}{
		{"--highlight", true},
		{"--highlight=false", false},
		{"--no-highlight", false},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			body := runScheduleWrite(t, "update", "999", tc.flag, "--project", "123")
			assert.Equal(t, tc.want, body["highlighted"])
			assert.NotContains(t, body, "url")
		})
	}
}

func TestScheduleUpdateJoinURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"set", []string{"--join-url", "https://meet.example.com/abc"}, "https://meet.example.com/abc"},
		{"empty clears", []string{"--join-url", ""}, ""},
		{"no-join-url clears", []string{"--no-join-url"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"update", "999", "--project", "123"}, tc.args...)
			body := runScheduleWrite(t, args...)

			got, ok := body["url"]
			require.True(t, ok, "expected url in the request")
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, body, "join_url")
			assert.NotContains(t, body, "highlighted")
		})
	}
}

func TestScheduleUpdateConflictingFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		msg  string
	}{
		{"highlight", []string{"--highlight", "--no-highlight"}, "--highlight and --no-highlight"},
		{"join url", []string{"--join-url", "https://meet.example.com/abc", "--no-join-url"}, "--join-url and --no-join-url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &mockScheduleCreateTransport{}
			app, _ := setupMessagesMockApp(t, transport)

			args := append([]string{"update", "999", "--project", "123"}, tc.args...)
			err := executeMessagesCommand(NewScheduleCmd(), app, args...)
			require.Error(t, err)

			var e *output.Error
			require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
			assert.Contains(t, e.Message, tc.msg)
			assert.Empty(t, transport.capturedBody, "nothing may be written")
		})
	}
}

// scheduleEntryFixtureTransport serves one highlighted entry with a join link.
// The response spells the link join_url, since url is the entry's API URL.
type scheduleEntryFixtureTransport struct{}

func (scheduleEntryFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	body := `{"id": 789, "summary": "Standup", "starts_at": "2026-03-04T09:00:00Z", "ends_at": "2026-03-04T09:15:00Z",
		"url": "https://3.basecampapi.com/99999/schedule_entries/789.json",
		"join_url": "https://meet.example.com/standup", "highlighted": true}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
}

func TestScheduleShowReportsHighlightAndJoinURL(t *testing.T) {
	app, buf := setupMessagesMockApp(t, scheduleEntryFixtureTransport{})
	require.NoError(t, executeMessagesCommand(NewScheduleCmd(), app, "show", "789", "--no-comments"))

	var resp struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &resp))
	assert.Equal(t, "https://meet.example.com/standup", resp.Data["join_url"])
	assert.Equal(t, true, resp.Data["highlighted"])
	assert.Equal(t, "https://3.basecampapi.com/99999/schedule_entries/789.json", resp.Data["url"])
}

func TestScheduleShowMarkdownShowsHighlightAndJoinURL(t *testing.T) {
	app, buf := setupMessagesMockApp(t, scheduleEntryFixtureTransport{})
	app.Output = output.New(output.Options{Format: output.FormatMarkdown, Writer: buf})
	require.NoError(t, executeMessagesCommand(NewScheduleCmd(), app, "show", "789", "--no-comments"))

	out := buf.String()
	assert.Contains(t, out, "https://meet.example.com/standup")
	assert.Regexp(t, `(?i)highlighted\W+(yes|true)`, out)
}
