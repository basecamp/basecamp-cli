package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// filingTransport answers GET /filings/4 with status and body.
type filingTransport struct {
	status int
	body   string
}

func (f filingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	status, body := f.status, f.body
	if strings.TrimSuffix(req.URL.Path, ".json") != "/99999/filings/4" {
		status, body = http.StatusNotFound, `{"error":"not found"}`
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
}

type filingsShown struct {
	Data struct {
		Status               string `json:"status"`
		Type                 string `json:"type"`
		SourceRecordingID    int64  `json:"source_recording_id"`
		DestinationRecording *struct {
			ID     int64  `json:"id"`
			Type   string `json:"type"`
			AppURL string `json:"app_url"`
		} `json:"destination_recording"`
	} `json:"data"`
	Summary     string `json:"summary"`
	Notice      string `json:"notice"`
	Breadcrumbs []struct {
		Cmd string `json:"cmd"`
	} `json:"breadcrumbs"`
}

func showFiling(t *testing.T, status int, body string) (filingsShown, error) {
	t.Helper()
	app, buf := newTestAppWithTransport(t, filingTransport{status: status, body: body})
	app.Flags.Hints = true
	var shown filingsShown
	err := executeCommand(NewFilingsCmd(), app, "show", "4")
	if err == nil {
		require.NoError(t, json.Unmarshal(buf.Bytes(), &shown))
	}
	return shown, err
}

func TestFilingsShowPendingSaysNotToRunItAgain(t *testing.T) {
	shown, err := showFiling(t, 200, `{"id":4,"status":"processing","type":"Copy","source_recording_id":456}`)
	require.NoError(t, err)
	assert.Equal(t, "processing", shown.Data.Status)
	assert.Equal(t, int64(456), shown.Data.SourceRecordingID)
	assert.Equal(t, "The copy of #456 is processing", shown.Summary)
	assert.Equal(t, "Do not copy #456 again while this runs.", shown.Notice)
	require.Len(t, shown.Breadcrumbs, 1)
	assert.Equal(t, "basecamp filings show 4", shown.Breadcrumbs[0].Cmd)
}

func TestFilingsShowCompletedNamesTheRecordItMade(t *testing.T) {
	for _, tc := range []struct {
		name, dest, want string
	}{
		{"a moved message", `{"id":4560,"type":"Message","app_url":"https://3.basecamp.com/99999/buckets/999/messages/4560"}`, "basecamp messages show 4560"},
		{"a moved column", `{"id":4560,"type":"Kanban::Column","app_url":"https://3.basecamp.com/99999/buckets/999/card_tables/columns/4560"}`, "basecamp cards column show 4560"},
		{"a moved dock tool", `{"id":4560,"type":"Message::Board","app_url":"https://3.basecamp.com/99999/buckets/999/message_boards/4560"}`, "basecamp show https://3.basecamp.com/99999/buckets/999/message_boards/4560"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shown, err := showFiling(t, 200, `{"id":4,"status":"completed","type":"Move","source_recording_id":456,"destination_recording":`+tc.dest+`}`)
			require.NoError(t, err)
			require.NotNil(t, shown.Data.DestinationRecording)
			assert.Equal(t, int64(4560), shown.Data.DestinationRecording.ID)
			assert.NotEmpty(t, shown.Data.DestinationRecording.AppURL)
			assert.Contains(t, shown.Summary, "Moved #456 as")
			assert.Contains(t, shown.Summary, "#4560")
			require.Len(t, shown.Breadcrumbs, 1)
			assert.Equal(t, tc.want, shown.Breadcrumbs[0].Cmd)
		})
	}
}

// Basecamp's docs say a move is safe to retry but say nothing of what a failed
// copy leaves behind, so only a failed move reads as safe to run again.
func TestFilingsShowFailedIsSafeToRetryOnlyForAMove(t *testing.T) {
	moved, err := showFiling(t, 200, `{"id":4,"status":"failed","type":"Move","source_recording_id":456}`)
	require.NoError(t, err)
	assert.Equal(t, "Basecamp could not move #456", moved.Summary)
	assert.Contains(t, moved.Notice, "safe to try again")

	copied, err := showFiling(t, 200, `{"id":4,"status":"failed","type":"Copy","source_recording_id":456}`)
	require.NoError(t, err)
	assert.Equal(t, "Basecamp could not copy #456", copied.Summary)
	assert.NotContains(t, copied.Notice, "safe")
	assert.Contains(t, copied.Notice, "Look in the destination before copying again")
}

func TestFilingsShowNotFoundSaysWhoCanReadIt(t *testing.T) {
	_, err := showFiling(t, 404, `{"error":"not found"}`)
	e := output.AsError(err)
	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Contains(t, e.Hint, "Only the person who started a move or copy can read its filing")
}

func TestFilingsShowRefusesANonNumericID(t *testing.T) {
	app, _ := newTestAppWithTransport(t, filingTransport{})
	err := executeCommand(NewFilingsCmd(), app, "show", "abc")
	assert.Equal(t, output.CodeUsage, output.AsError(err).Code)
}
