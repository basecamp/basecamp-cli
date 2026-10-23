package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// The series is entry 789. Its occurrence on 2026-10-22 is recording 1001,
// and comment 555 sits on that occurrence.
const (
	occurrenceURL     = "https://3.basecamp.com/99999/buckets/123/schedule_entries/789/occurrences/20261022"
	occurrenceEditURL = occurrenceURL + "/edit"
	entryURL          = "https://3.basecamp.com/99999/buckets/123/schedule_entries/789"
)

const (
	occurrenceEntryJSON = `{"id": 789, "type": "Schedule::Entry", "summary": "Standup", "all_day": false,
		"starts_at": "2026-10-20T15:00:00Z", "ends_at": "2026-10-20T16:00:00Z",
		"description": "<div>Agenda <bc-attachment url=\"https://3.basecampapi.com/99999/blobs/agenda/download/agenda.pdf\" filename=\"agenda.pdf\"></bc-attachment></div>",
		"bucket": {"id": 123}}`
	occurrenceJSON = `{"id": 1001, "type": "Schedule::Entry::Occurrence", "summary": "Standup",
		"starts_at": "2026-10-22T15:00:00Z", "ends_at": "2026-10-22T16:00:00Z",
		"description": "<div>Agenda <bc-attachment url=\"https://3.basecampapi.com/99999/blobs/agenda/download/agenda.pdf\" filename=\"agenda.pdf\"></bc-attachment></div>",
		"comments_count": 1, "parent": {"id": 789, "type": "Schedule::Entry"}, "bucket": {"id": 123}}`
	occurrenceCommentJSON = `{"id": 555, "type": "Comment",
		"content": "<div>Notes <bc-attachment url=\"https://3.basecampapi.com/99999/blobs/notes/download/notes.txt\" filename=\"notes.txt\"></bc-attachment></div>",
		"parent": {"id": 1001, "type": "Schedule::Entry::Occurrence"}, "bucket": {"id": 123}}`
)

// occurrenceAPI answers the requests these commands make with the shape the
// SDK expects, and 404s anything else, so a command only succeeds when it
// asked for the right thing.
type occurrenceAPI struct {
	mu       sync.Mutex
	requests []string
}

func (a *occurrenceAPI) RoundTrip(req *http.Request) (*http.Response, error) {
	a.mu.Lock()
	a.requests = append(a.requests, req.Method+" "+req.URL.Path)
	a.mu.Unlock()

	path := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/99999"), ".json")
	status, body := occurrenceAPIResponse(req.Method, path)
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	if path == "/recordings/1001/comments" {
		header.Set("X-Total-Count", "1")
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
		Request:    req,
	}, nil
}

func (a *occurrenceAPI) got() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.requests...)
}

func occurrenceAPIResponse(method, path string) (int, string) {
	switch method + " " + path {
	case "GET /schedule_entries/789", "PUT /schedule_entries/789", "GET /recordings/789":
		return http.StatusOK, occurrenceEntryJSON
	case "GET /schedule_entries/789/occurrences/20261022":
		return http.StatusOK, occurrenceJSON
	case "PUT /recordings/789/status/trashed", "PUT /recordings/789/status/archived":
		return http.StatusNoContent, ""
	case "POST /recordings/789/comments", "POST /recordings/1001/comments":
		return http.StatusCreated, `{"id": 556, "content": "<div>hello</div>"}`
	case "GET /recordings/789/comments":
		return http.StatusOK, `[]`
	case "GET /recordings/1001/comments":
		return http.StatusOK, `[{"id": 555, "content": "<div>Notes</div>"}]`
	case "POST /recordings/789/boosts":
		return http.StatusCreated, `{"id": 31, "content": "yes"}`
	case "POST /recordings/789/subscription":
		return http.StatusOK, `{"subscribed": true, "count": 1, "subscribers": []}`
	case "GET /comments/555", "PUT /comments/555":
		return http.StatusOK, occurrenceCommentJSON
	case "GET /blobs/notes/download/notes.txt", "GET /blobs/agenda/download/agenda.pdf":
		return http.StatusOK, "file body"
	}
	return http.StatusNotFound, `{"error": "not found"}`
}

func runOccurrenceCommand(t *testing.T, cmd *cobra.Command, args ...string) ([]string, *bytes.Buffer, error) {
	t.Helper()
	api := &occurrenceAPI{}
	app, out := setupMessagesMockApp(t, api)
	app.Flags.Hints = true
	err := executeMessagesCommand(cmd, app, args...)
	return api.got(), out, err
}

type occurrenceCase struct {
	name string
	cmd  func() *cobra.Command
	args string // %s is the target
}

func (c occurrenceCase) run(t *testing.T, target string) ([]string, error) {
	t.Helper()
	requests, _, err := runOccurrenceCommand(t, c.cmd(), strings.Fields(fmt.Sprintf(c.args, target))...)
	return requests, err
}

// Between them, these commands reach the entry through each shared extractor.
var refusedOccurrenceCases = []occurrenceCase{
	{"schedule update", NewScheduleCmd, "update %s --summary X"},
	{"recordings trash", NewRecordingsCmd, "trash %s"},
	{"recordings archive", NewRecordingsCmd, "archive %s"},
	{"comments create", NewCommentsCmd, "create %s hello"},
	{"comments list", NewCommentsCmd, "list %s"},
	{"boost create", NewBoostsCmd, "create %s yes"},
	{"subscriptions subscribe", NewSubscriptionsCmd, "subscribe %s"},
	{"attachments list as a todo", NewAttachmentsCmd, "list %s --type todo"},
}

func TestOccurrenceURLIsRefused(t *testing.T) {
	for _, c := range refusedOccurrenceCases {
		for _, target := range []string{occurrenceURL, occurrenceEditURL} {
			t.Run(c.name+" "+target, func(t *testing.T) {
				requests, err := c.run(t, target)

				var outErr *output.Error
				require.True(t, errors.As(err, &outErr), "want a usage error, got %v", err)
				assert.Equal(t, output.CodeUsage, outErr.Code)
				assert.Contains(t, outErr.Message, "occurrence")
				assert.Contains(t, outErr.Hint, "789", "the hint names the event's ID")
				assert.Empty(t, requests)
			})
		}
	}
}

func TestEntryURLAndIDStillReachTheEntry(t *testing.T) {
	cases := []struct {
		occurrenceCase
		want   []string
		wantID []string // when the bare ID asks differently from the URL
	}{
		{refusedOccurrenceCases[0], []string{"GET /99999/schedule_entries/789", "PUT /99999/schedule_entries/789"}, nil},
		{refusedOccurrenceCases[1], []string{"PUT /99999/recordings/789/status/trashed.json"}, nil},
		{refusedOccurrenceCases[2], []string{"PUT /99999/recordings/789/status/archived.json"}, nil},
		{refusedOccurrenceCases[3], []string{"POST /99999/recordings/789/comments.json"}, nil},
		{refusedOccurrenceCases[4], []string{"GET /99999/recordings/789/comments.json"}, nil},
		{refusedOccurrenceCases[5], []string{"POST /99999/recordings/789/boosts.json"}, nil},
		{refusedOccurrenceCases[6], []string{"POST /99999/recordings/789/subscription.json"}, nil},
		{
			occurrenceCase{"attachments list", NewAttachmentsCmd, "list %s"},
			[]string{"GET /99999/schedule_entries/789.json"},
			[]string{"GET /99999/recordings/789.json", "GET /99999/schedule_entries/789.json"},
		},
	}
	for _, c := range cases {
		for _, target := range []string{entryURL, "789"} {
			t.Run(c.name+" "+target, func(t *testing.T) {
				requests, err := c.run(t, target)

				require.NoError(t, err, "requests: %v", requests)
				want := c.want
				if target == "789" && c.wantID != nil {
					want = c.wantID
				}
				assert.Equal(t, want, requests)
			})
		}
	}
}

func TestCommentFragmentOnOccurrenceURLNamesTheComment(t *testing.T) {
	commentURL := occurrenceURL + "#__recording_555"
	cases := []struct {
		name string
		cmd  func() *cobra.Command
		args []string
		want []string
	}{
		{"comments update", NewCommentsCmd, []string{"update", commentURL, "edited"},
			[]string{"PUT /99999/comments/555"}},
		{"attachments list", NewAttachmentsCmd, []string{"list", commentURL},
			[]string{"GET /99999/comments/555.json"}},
		{"attachments list --type comment", NewAttachmentsCmd, []string{"list", commentURL, "--type", "comment"},
			[]string{"GET /99999/comments/555.json"}},
		{"attachments download", NewAttachmentsCmd, []string{"download", commentURL, "--out", t.TempDir()},
			[]string{"GET /99999/comments/555.json", "GET /99999/blobs/notes/download/notes.txt"}},
		{"attachments download --type comment", NewAttachmentsCmd, []string{"download", commentURL, "--type", "comment", "--out", t.TempDir()},
			[]string{"GET /99999/comments/555.json", "GET /99999/blobs/notes/download/notes.txt"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			requests, _, err := runOccurrenceCommand(t, c.cmd(), c.args...)

			require.NoError(t, err, "requests: %v", requests)
			assert.Equal(t, c.want, requests)
		})
	}
}

type occurrenceEnvelope struct {
	Data        json.RawMessage     `json:"data"`
	Summary     string              `json:"summary"`
	Breadcrumbs []output.Breadcrumb `json:"breadcrumbs"`
}

func decodeOccurrenceEnvelope(t *testing.T, out *bytes.Buffer) occurrenceEnvelope {
	t.Helper()
	var env occurrenceEnvelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &env), out.String())
	return env
}

func (env occurrenceEnvelope) cmds() []string {
	cmds := make([]string, 0, len(env.Breadcrumbs))
	for _, b := range env.Breadcrumbs {
		cmds = append(cmds, b.Cmd)
	}
	return cmds
}

// A read of an occurrence goes through the occurrence, and anything a
// breadcrumb suggests writing goes to the occurrence's own recording.
func TestAttachmentsListOccurrenceURL(t *testing.T) {
	requests, out, err := runOccurrenceCommand(t, NewAttachmentsCmd(), "list", occurrenceURL)

	require.NoError(t, err, "requests: %v", requests)
	assert.Equal(t, []string{"GET /99999/schedule_entries/789/occurrences/20261022.json"}, requests)
	env := decodeOccurrenceEnvelope(t, out)
	assert.Contains(t, string(env.Data), "agenda.pdf")
	assert.Equal(t, []string{
		"basecamp show " + occurrenceURL,
		"basecamp comments create 1001 <text>",
		"basecamp attachments download " + occurrenceURL,
	}, env.cmds())
}

func TestAttachmentsDownloadOccurrenceURL(t *testing.T) {
	requests, _, err := runOccurrenceCommand(t, NewAttachmentsCmd(), "download", occurrenceEditURL, "--out", t.TempDir())

	require.NoError(t, err, "requests: %v", requests)
	assert.Equal(t, []string{
		"GET /99999/schedule_entries/789/occurrences/20261022.json",
		"GET /99999/blobs/agenda/download/agenda.pdf",
	}, requests)
}

func TestScheduleShowOccurrenceURLReadsItsComments(t *testing.T) {
	for _, target := range []string{occurrenceURL, occurrenceEditURL} {
		t.Run(target, func(t *testing.T) {
			requests, out, err := runOccurrenceCommand(t, NewScheduleCmd(), "show", target, "--comments")

			require.NoError(t, err, "requests: %v", requests)
			assert.Equal(t, []string{
				"GET /99999/schedule_entries/789/occurrences/20261022",
				"GET /99999/recordings/1001/comments.json",
			}, requests)
			env := decodeOccurrenceEnvelope(t, out)
			assert.Equal(t, []string{
				"basecamp comments create 1001 <text>",
				"basecamp schedule entries --project 123",
				"basecamp comments list --all 1001",
			}, env.cmds())
		})
	}
}

func TestShowOccurrenceURLReadsItsComments(t *testing.T) {
	for _, target := range []string{occurrenceURL, occurrenceEditURL} {
		t.Run(target, func(t *testing.T) {
			requests, out, err := runOccurrenceCommand(t, NewShowCmd(), target)

			require.NoError(t, err, "requests: %v", requests)
			assert.Equal(t, []string{
				"GET /99999/schedule_entries/789/occurrences/20261022.json",
				"GET /99999/recordings/1001/comments.json",
			}, requests)
			env := decodeOccurrenceEnvelope(t, out)
			assert.Contains(t, env.Summary, "#1001")
			assert.Contains(t, env.cmds(), "basecamp comments create 1001 <text>")
			assert.Contains(t, env.cmds(), "basecamp comments list --all 1001")
			for _, cmd := range env.cmds() {
				assert.NotContains(t, cmd, "comments create 789", "a breadcrumb must not write to the series")
			}
		})
	}
}
