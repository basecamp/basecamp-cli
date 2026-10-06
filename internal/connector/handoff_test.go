package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/ndjson"
)

func handoffOptions(ledger *Ledger, out *bytes.Buffer) HandoffOptions {
	return HandoffOptions{
		Ledger: ledger,
		Served: func() (map[int64]admission.Project, error) {
			return map[int64]admission.Project{adapterBucketID: {}}, nil
		},
		AgentID: 999,
		Lines:   ndjson.NewWriter(out),
		// testEvent's requests were made at 10:00 that day.
		Started: time.Date(2026, 9, 16, 10, 0, 30, 0, time.UTC),
	}
}

func handedOffLines(t *testing.T, out *bytes.Buffer) []HandoffLine {
	t.Helper()
	var lines []HandoffLine
	for raw := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		if raw == "" {
			continue
		}
		var l HandoffLine
		require.NoError(t, json.Unmarshal([]byte(raw), &l))
		lines = append(lines, l)
	}
	return lines
}

// Every trusted request is written once, as the session reads it, and the
// connector is done with it. Two on one conversation are both handed off,
// oldest first: nothing waits for a worker.
func TestEachTrustedRequestIsHandedOffOnce(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	admitOn(t, ledger, 2, "recording:1")
	var out bytes.Buffer
	opts := handoffOptions(ledger, &out)

	require.NoError(t, handOffReady(context.Background(), opts))
	require.NoError(t, handOffReady(context.Background(), opts))

	lines := handedOffLines(t, &out)
	require.Len(t, lines, 2)
	first := lines[0]
	assert.Equal(t, "request", first.Type)
	assert.Equal(t, int64(1), first.EventID)
	assert.Equal(t, "mentioned", first.Trigger)
	assert.True(t, first.Acknowledge)
	assert.Equal(t, "<div>please look</div>", first.Content)
	assert.Equal(t, "Comment", first.Recording.Type)
	assert.Equal(t, adapterBucketID, first.Recording.BucketID)
	assert.Equal(t, "https://app.basecamp.com/2914079/buckets/48699913/recordings/10304028972", first.Recording.URL)
	assert.Equal(t, int64(10304028989), first.ReplyTo.RecordingID)
	assert.Equal(t, int64(2), lines[1].EventID)

	for _, id := range []int64{1, 2} {
		r := getRecord(t, ledger, id)
		assert.Equal(t, StateDiscarded, r.State)
		assert.Equal(t, ReasonHandedOff, r.Reason)
	}
}

// A request made before this run started is not run late: nobody was there
// to take it.
func TestARequestFromBeforeTheRunIsNotHandedOff(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	var out bytes.Buffer
	opts := handoffOptions(ledger, &out)
	opts.Started = opts.Started.Add(time.Hour)

	require.NoError(t, handOffReady(context.Background(), opts))

	assert.Empty(t, out.String())
	r := getRecord(t, ledger, 1)
	assert.Equal(t, StateDiscarded, r.State)
	assert.Equal(t, ReasonBeforeThisRun, r.Reason)
}

// Only the projects the agent serves, narrowed to the run's --project.
func TestOnlyServedProjectsAreHandedOff(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	var out bytes.Buffer
	opts := handoffOptions(ledger, &out)
	opts.Buckets = []int64{adapterBucketID + 1}

	require.NoError(t, handOffReady(context.Background(), opts))

	assert.Empty(t, out.String())
	assert.Equal(t, StateAdmitted, getRecord(t, ledger, 1).State, "left for a run that serves it")

	// And a project connect.json no longer serves, with no --project.
	opts = handoffOptions(ledger, &out)
	opts.Served = func() (map[int64]admission.Project, error) {
		return map[int64]admission.Project{adapterBucketID + 1: {}}, nil
	}
	require.NoError(t, handOffReady(context.Background(), opts))
	assert.Empty(t, out.String())
	assert.Equal(t, StateAdmitted, getRecord(t, ledger, 1).State)
}

// The line names the project and, when they wrote the recording, the person
// who asked: the session needn't ask Basecamp as the agent.
func TestTheLineNamesTheProjectAndTheRequester(t *testing.T) {
	ledger := newTestLedger(t)
	seenRecord(t, ledger, 1)
	v := admittedVerdict(1, 0, "recording:1")
	v.Snapshot.ProjectName = "Bring your agents to Basecamp"
	v.Snapshot.RequesterName = "Rob Zolkos"
	_, err := ledger.Admission().Commit(context.Background(), v)
	require.NoError(t, err)
	var out bytes.Buffer

	require.NoError(t, handOffReady(context.Background(), handoffOptions(ledger, &out)))

	lines := handedOffLines(t, &out)
	require.Len(t, lines, 1)
	assert.Equal(t, "Bring your agents to Basecamp", lines[0].Recording.ProjectName)
	assert.Equal(t, "Rob Zolkos", lines[0].RequesterName)
}

// A record with nothing to hand off is closed under its own reason: status
// never claims a delivery that didn't happen.
func TestARecordWithNothingToHandOffIsNotCalledHandedOff(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	_, err := ledger.db.ExecContext(context.Background(), `UPDATE events SET snapshot = '{not json' WHERE id = 1`)
	require.NoError(t, err)
	var out bytes.Buffer

	require.NoError(t, handOffReady(context.Background(), handoffOptions(ledger, &out)))

	assert.Empty(t, out.String())
	r := getRecord(t, ledger, 1)
	assert.Equal(t, StateDiscarded, r.State)
	assert.Equal(t, ReasonUnreadable, r.Reason)
}

// Control sequences in Basecamp's text never reach the line.
func TestTheLineCarriesNoTerminalControlSequences(t *testing.T) {
	ledger := newTestLedger(t)
	seenRecord(t, ledger, 1)
	v := admittedVerdict(1, 0, "recording:1")
	v.Snapshot.Content = "<p>fix it\u009b2J\x1b[31m now</p>"
	v.Snapshot.Title = "A \x1b]0;title\x07card"
	_, err := ledger.Admission().Commit(context.Background(), v)
	require.NoError(t, err)
	var out bytes.Buffer

	require.NoError(t, handOffReady(context.Background(), handoffOptions(ledger, &out)))

	lines := handedOffLines(t, &out)
	require.Len(t, lines, 1)
	for _, s := range []string{lines[0].Content, lines[0].Recording.Title} {
		assert.NotContains(t, s, "\x1b")
		assert.NotContains(t, s, "\u009b")
	}
	assert.Contains(t, lines[0].Content, "fix it")
}

// A task the worker connector left open, by a crash, doesn't keep requests on
// its conversation from being handed off, a follow-up joined to it included
// (Copilot on #814).
func TestATaskLeftOpenByTheWorkerConnectorBlocksNothing(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	launch(t, ledger, 1) // the old dispatcher started a task, then crashed
	admitOn(t, ledger, 2, "recording:1")
	var out bytes.Buffer

	require.NoError(t, handOffReady(context.Background(), handoffOptions(ledger, &out)))

	lines := handedOffLines(t, &out)
	require.Len(t, lines, 1)
	assert.Equal(t, int64(2), lines[0].EventID)
}

// Every line says whose request it is, in the words the local connector uses:
// "operator" or "participant". A record admitted before admission settled a
// role, or carrying a role this build doesn't know, is handed off as a
// participant's: the session's participant rules are the ones that cannot
// lend anyone an operator's standing.
func TestTheLineSaysWhoseRequestItIs(t *testing.T) {
	for name, tc := range map[string]struct {
		role admission.Role
		want string
	}{
		"an operator's":                {admission.RoleOperator, "operator"},
		"a participant's":              {admission.RoleParticipant, "participant"},
		"one admitted before roles":    {"", "participant"},
		"one with a role nobody knows": {"owner", "participant"},
	} {
		t.Run(name, func(t *testing.T) {
			ledger := newTestLedger(t)
			seenRecord(t, ledger, 1)
			v := admittedVerdict(1, 0, "recording:1")
			v.Snapshot.Role = tc.role
			_, err := ledger.Admission().Commit(context.Background(), v)
			require.NoError(t, err)
			var out bytes.Buffer

			require.NoError(t, handOffReady(context.Background(), handoffOptions(ledger, &out)))

			lines := handedOffLines(t, &out)
			require.Len(t, lines, 1)
			assert.Equal(t, tc.want, lines[0].Role)
			assert.Contains(t, out.String(), `"role":"`+tc.want+`"`, "under the local connector's key")
		})
	}
}
