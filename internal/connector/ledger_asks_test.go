package connector

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

const askRecordingID int64 = 10304028972

// boostVerdict is an admitted boost on askRecordingID; operator says it is
// the operator's, which is when admission names the ask it would answer.
func boostVerdict(id int64, operator bool) admission.Verdict {
	v := admittedVerdict(id, 0, "recording:10304028989")
	v.EventType, v.Trigger, v.Acknowledge = "boost.created", admission.TriggerBoosted, false
	v.Snapshot.Content = "yes, go"
	if operator {
		v.AskRecordingID = askRecordingID
	}
	return v
}

func commitBoost(t *testing.T, ledger *Ledger, id int64, operator bool) {
	t.Helper()
	seenRecord(t, ledger, id)
	_, err := ledger.Admission().Commit(context.Background(), boostVerdict(id, operator))
	require.NoError(t, err)
}

func goes(t *testing.T, ledger *Ledger, id int64) bool {
	t.Helper()
	g, err := ledger.isGo(context.Background(), id)
	require.NoError(t, err)
	return g
}

// One ask, one go: the operator's first boost on a declared ask takes it,
// and every boost after it is an ordinary reply — the operator's second, and
// one removed and given again, which Basecamp delivers as a new event.
func TestOneAskGetsOneGo(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	declared, err := ledger.DeclareAsk(ctx, askRecordingID, "local:agent")
	require.NoError(t, err)
	assert.False(t, declared.Already)

	commitBoost(t, ledger, 1, true)
	commitBoost(t, ledger, 2, true)
	commitBoost(t, ledger, 3, true)

	assert.True(t, goes(t, ledger, 1), "the first operator boost is the go")
	assert.False(t, goes(t, ledger, 2), "a second boost is a reply")
	assert.False(t, goes(t, ledger, 3), "a boost given again is a reply")

	again, err := ledger.DeclareAsk(ctx, askRecordingID, "local:agent")
	require.NoError(t, err)
	assert.True(t, again.Already)
	assert.Equal(t, int64(1), again.GoEventID, "declaring it again does not reopen the ask")
	commitBoost(t, ledger, 4, true)
	assert.False(t, goes(t, ledger, 4))
}

// The event that took the go keeps it when it is decided again.
func TestTheGoIsTheEventsToKeep(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	_, err := ledger.DeclareAsk(ctx, askRecordingID, "local:agent")
	require.NoError(t, err)
	commitBoost(t, ledger, 1, true)

	tx, err := ledger.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	again, err := takeGo(ctx, tx, askRecordingID, 1)
	require.NoError(t, err)
	assert.True(t, again, "the same event takes the same go")
	other, err := takeGo(ctx, tx, askRecordingID, 2)
	require.NoError(t, err)
	assert.False(t, other)
}

func TestNoGoWithoutADeclaredAskOrTheOperator(t *testing.T) {
	t.Run("a boost on a post nobody declared an ask", func(t *testing.T) {
		ledger := newTestLedger(t)
		commitBoost(t, ledger, 1, true)
		assert.False(t, goes(t, ledger, 1))
		// Declared after the boost was decided: not retroactive.
		_, err := ledger.DeclareAsk(context.Background(), askRecordingID, "local:agent")
		require.NoError(t, err)
		assert.False(t, goes(t, ledger, 1))
	})

	t.Run("someone else's boost leaves the ask open for the operator's", func(t *testing.T) {
		ledger := newTestLedger(t)
		_, err := ledger.DeclareAsk(context.Background(), askRecordingID, "local:agent")
		require.NoError(t, err)
		commitBoost(t, ledger, 1, false)
		commitBoost(t, ledger, 2, true)
		assert.False(t, goes(t, ledger, 1))
		assert.True(t, goes(t, ledger, 2))
	})

	t.Run("an ask names a recording and who declared it", func(t *testing.T) {
		ledger := newTestLedger(t)
		_, err := ledger.DeclareAsk(context.Background(), 0, "local:agent")
		require.Error(t, err)
		_, err = ledger.DeclareAsk(context.Background(), askRecordingID, " ")
		require.Error(t, err)
	})
}

// The session tells the go from a thumbs-up by the line alone.
func TestTheGoIsHandedOffAsSuch(t *testing.T) {
	ledger := newTestLedger(t)
	_, err := ledger.DeclareAsk(context.Background(), askRecordingID, "local:agent")
	require.NoError(t, err)
	commitBoost(t, ledger, 1, true)
	commitBoost(t, ledger, 2, true)
	admitOn(t, ledger, 3, "recording:3")

	var out bytes.Buffer
	opts := handoffOptions(ledger, &out)
	for range 3 {
		require.NoError(t, handOffReady(context.Background(), opts))
	}
	lines := handedOffLines(t, &out)
	require.Len(t, lines, 3)
	byID := map[int64]HandoffLine{}
	for _, l := range lines {
		byID[l.EventID] = l
	}
	assert.True(t, byID[1].Go)
	assert.Equal(t, "boosted", byID[1].Trigger)
	assert.Equal(t, askRecordingID, byID[1].Recording.RecordingID, "the ask is the line's recording")
	assert.Equal(t, "yes, go", byID[1].Content)
	assert.False(t, byID[2].Go, "the second boost is a reply")
	assert.False(t, byID[3].Go, "a mention is never a go")
}

// Only a boosted record's line can say go, whatever the asks table holds.
func TestOnlyABoostedLineSaysGo(t *testing.T) {
	line, err := handoffLine(func() Record {
		ledger := newTestLedger(t)
		admitOn(t, ledger, 1, "recording:1")
		return getRecord(t, ledger, 1)
	}(), 999, true)
	require.NoError(t, err)
	assert.False(t, line.Go)
}

// A go taken stays taken, even when its record is closed unwritten: on
// restart that looks the same as a line printed just before a crash, and
// giving the go back would let the next boost approve twice.
func TestAGoClosedUnwrittenIsNotGivenBack(t *testing.T) {
	ledger := newTestLedger(t)
	_, err := ledger.DeclareAsk(context.Background(), askRecordingID, "local:agent")
	require.NoError(t, err)
	commitBoost(t, ledger, 1, true)

	var out bytes.Buffer
	opts := handoffOptions(ledger, &out)
	opts.Started = opts.Started.Add(time.Hour)
	require.NoError(t, handOffReady(context.Background(), opts))
	assert.Empty(t, out.String())
	assert.Equal(t, ReasonBeforeThisRun, getRecord(t, ledger, 1).Reason)

	commitBoost(t, ledger, 2, true)
	assert.True(t, goes(t, ledger, 1), "the go stays with the event that took it")
	assert.False(t, goes(t, ledger, 2), "the next boost is a reply, never a second go")
}
