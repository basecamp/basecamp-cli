package connector

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

func newTestLedger(t *testing.T) *Ledger {
	t.Helper()
	ledger, err := OpenLedger(filepath.Join(t.TempDir(), "connector.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ledger.Close() })
	return ledger
}

func testEvent(id int64) eventfeed.Event {
	return eventfeed.Event{
		ID:          id,
		Kind:        "comment_created",
		EventType:   "comment.created",
		Action:      "created",
		CreatedAt:   time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC),
		BucketID:    48699913,
		CreatorID:   26909558,
		RecordingID: 10304028972,
	}
}

func testKey() eventfeed.CheckpointKey {
	return eventfeed.CheckpointKey{
		Origin:            "https://3.basecampapi.com",
		AccountID:         "2914079",
		ConsumerNamespace: "connector-test",
		FilterKey:         "srv2-9f2ab04e5c11d3a7",
	}
}

func TestRecordSeenDedupesAcrossLanes(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()

	fresh, err := ledger.RecordSeen(ctx, testEvent(17099838500), LaneLive)
	require.NoError(t, err)
	assert.True(t, fresh)

	// The poll lane serving what the live lane already delivered is the
	// ordinary case, not an anomaly.
	fresh, err = ledger.RecordSeen(ctx, testEvent(17099838500), LanePoll)
	require.NoError(t, err)
	assert.False(t, fresh)

	record, ok, err := ledger.Get(ctx, 17099838500)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, LaneLive, record.Lane, "the first lane to serve it stands")
	assert.Equal(t, StateSeen, record.State)
}

func TestRecordSeenKeepsDetailsVerbatimAndNilWhenAbsent(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()

	withDetails := testEvent(1)
	withDetails.EventType = "card.moved"
	withDetails.Details = json.RawMessage(`{"column_id":10287321848,"previous_column_id":null}`)
	_, err := ledger.RecordSeen(ctx, withDetails, LanePoll)
	require.NoError(t, err)

	_, err = ledger.RecordSeen(ctx, testEvent(2), LanePoll)
	require.NoError(t, err)

	moved, _, err := ledger.Get(ctx, 1)
	require.NoError(t, err)
	assert.JSONEq(t, `{"column_id":10287321848,"previous_column_id":null}`, string(moved.Details))

	plain, _, err := ledger.Get(ctx, 2)
	require.NoError(t, err)
	assert.Nil(t, plain.Details, "an event that publishes no details must not be stored with an empty one")
}

func TestDroppedContentLeavesATombstoneThatStillDedupes(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()

	_, err := ledger.RecordSeen(ctx, testEvent(42), LanePoll)
	require.NoError(t, err)
	require.NoError(t, ledger.SetState(ctx, 42, StateDiscarded, "untrusted_author"))

	dropped, err := ledger.DropContent(ctx, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, dropped)

	record, ok, err := ledger.Get(ctx, 42)
	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, record.ContentDropped)
	assert.Empty(t, record.EventType, "the payload goes")
	assert.Equal(t, StateDiscarded, record.State, "the outcome stays")

	// The whole point of keeping the tombstone: an explicit replay of old
	// history can never turn a finished event back into a new task.
	fresh, err := ledger.RecordSeen(ctx, testEvent(42), LanePoll)
	require.NoError(t, err)
	assert.False(t, fresh)
}

func TestDropContentLeavesNonTerminalRecordsAlone(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()

	_, err := ledger.RecordSeen(ctx, testEvent(7), LanePoll)
	require.NoError(t, err)
	require.NoError(t, ledger.SetState(ctx, 7, StateBlocked, "read_failed"))

	dropped, err := ledger.DropContent(ctx, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Zero(t, dropped)

	record, _, err := ledger.Get(ctx, 7)
	require.NoError(t, err)
	assert.Equal(t, "comment.created", record.EventType,
		"a blocked record's pointer is the only copy of what intake was told")
}

func TestCheckpointRoundTrip(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	key := testKey()

	_, ok, err := ledger.Load(ctx, key)
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, ledger.Save(ctx, key, "opaque-position-1"))
	position, ok, err := ledger.Load(ctx, key)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "opaque-position-1", position)
}

func TestForgetPositionKeepsTheLastPollServedID(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	key := testKey()

	require.NoError(t, ledger.Save(ctx, key, "opaque-position-1"))
	require.NoError(t, ledger.NotePollServed(ctx, key, 17099838600))
	require.NoError(t, ledger.ForgetPosition(ctx, key))

	_, ok, err := ledger.Load(ctx, key)
	require.NoError(t, err)
	assert.False(t, ok, "a dropped position must not read back as a present empty one")

	served, err := ledger.LastPollServedID(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, int64(17099838600), served,
		"the 409 invalidates the position, not the id the poll lane had reached")
}

func TestNotePollServedOnlyMovesForward(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	key := testKey()

	require.NoError(t, ledger.NotePollServed(ctx, key, 500))
	require.NoError(t, ledger.NotePollServed(ctx, key, 400))

	served, err := ledger.LastPollServedID(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, int64(500), served)
}

func TestCheckpointsAreKeyedByFilterDigest(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()

	narrow := testKey()
	wide := testKey()
	wide.FilterKey = "srv2-0000000000000000"

	require.NoError(t, ledger.Save(ctx, narrow, "narrow-position"))
	_, ok, err := ledger.Load(ctx, wide)
	require.NoError(t, err)
	assert.False(t, ok, "a filter change re-enters under its own lineage")
}
