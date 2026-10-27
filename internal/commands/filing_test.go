package commands

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// runTestFiling runs f against transport, which answers its POST with filing
// 70 and plays back the filing's statuses (see mockFilingTransport).
func runTestFiling(t *testing.T, transport *mockFilingTransport, f recordingFiling) filingRun {
	t.Helper()
	shortFilingWait(t)
	app, buf := newTestAppWithTransport(t, transport)
	cmd := &cobra.Command{RunE: func(cmd *cobra.Command, _ []string) error {
		return runFiling(cmd, appctx.FromContext(cmd.Context()), f)
	}}
	var run filingRun
	run.err = executeCommand(cmd, app)
	if run.err == nil {
		require.NoError(t, json.Unmarshal(buf.Bytes(), &run.envelope))
	}
	return run
}

func TestFilingADockToolNamesItsProjectAndReadsBackTheNewRecording(t *testing.T) {
	transport := &mockFilingTransport{statuses: []string{"processing", "completed"}}
	var readID int64
	run := runTestFiling(t, transport, recordingFiling{
		kind:        basecamp.FilingCopy,
		sourceID:    456,
		noun:        "message board",
		toProjectID: 999,
		destination: "project 999",
		read: func(_ context.Context, id int64) (filedRecording, error) {
			readID = id
			return filedRecording{
				result: map[string]any{"id": id},
				id:     id,
				bucket: &basecamp.Bucket{ID: 999, Name: "Dest"},
			}, nil
		},
	})
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/99999/recordings/456/copies"}, transport.posts)
	assert.Equal(t, float64(999), transport.body["destination_project_id"])
	assert.NotContains(t, transport.body, "filing_destination_id")
	assert.Equal(t, int64(4560), readID, "the new recording is read by the id the filing returned")
	assert.Equal(t, "Copied message board #456 to Dest as message board #4560", run.envelope.Summary)
}

func TestFilingWhoseNewRecordingCannotBeReadNamesItsNewID(t *testing.T) {
	transport := &mockFilingTransport{statuses: []string{"completed"}}
	run := runTestFiling(t, transport, recordingFiling{
		kind:          basecamp.FilingMove,
		sourceID:      456,
		noun:          "to-do",
		destinationID: 900,
		destination:   "to-do list 900 (Ideas)",
		showCmd:       "basecamp todos show",
		read: func(context.Context, int64) (filedRecording, error) {
			return filedRecording{}, errors.New("connection reset by peer")
		},
	})

	var e *output.Error
	require.ErrorAs(t, run.err, &e)
	assert.Equal(t, output.CodeAPI, e.Code)
	assert.Equal(t, "Moved to-do #456 as to-do #4560, but could not read the new to-do", e.Message)
	assert.Equal(t, "Show it with: basecamp todos show 4560", e.Hint)
	assert.Len(t, transport.posts, 1, "the move is not sent again")
}
