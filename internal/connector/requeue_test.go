package connector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("stdout is gone") }

// B2: the ledger row is written before the pointer line and the hand-off, so
// that a crash cannot lose the pointer. The cost is that a failure after the
// commit leaves an event the ledger's dedupe will suppress on every retry.
// Nothing may be left in that state: a record still seen is queued again on
// the next start.
func TestAnEventWhoseHandOffFailedIsQueuedOnTheNextStart(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()

	// A first run whose stdout is gone: the row commits, the pointer line
	// fails, and the id never reaches the queue.
	first, _, firstQueue := newTestIntakeOn(t, ledger, failingWriter{})
	require.Error(t, first.ingest(ctx, testEvent(17099838500), LanePoll))
	require.Zero(t, firstQueue.Depth())

	record, ok, err := ledger.Get(ctx, 17099838500)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, StateSeen, record.State)

	// The next start hands it over.
	second, _, secondQueue := newTestIntakeOn(t, ledger, nil)
	require.NoError(t, second.requeueSeen(ctx))
	assert.Equal(t, 1, secondQueue.Depth())
	id, err := secondQueue.Take(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(17099838500), id)
}

// Records a later stage has already taken are not queued again.
func TestOnlyRecordsStillSeenAreQueuedOnAStart(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	for _, id := range []int64{1, 2, 3} {
		_, err := ledger.RecordSeen(ctx, testEvent(id), LanePoll)
		require.NoError(t, err)
	}
	require.NoError(t, ledger.SetState(ctx, 2, StateAdmitted, ""))
	require.NoError(t, ledger.SetState(ctx, 3, StateDiscarded, "untrusted_author"))

	intake, _, queue := newTestIntakeOn(t, ledger, nil)
	require.NoError(t, intake.requeueSeen(ctx))
	assert.Equal(t, 1, queue.Depth())
	id, err := queue.Take(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), id)
}

// The hand-off waits for room rather than dropping, and a shutdown mid-requeue
// leaves the rest for the next start.
func TestRequeueingStopsOnShutdownAndLeavesTheRest(t *testing.T) {
	ledger := newTestLedger(t)
	ctx, cancel := context.WithCancel(context.Background())
	for _, id := range []int64{1, 2, 3} {
		_, err := ledger.RecordSeen(ctx, testEvent(id), LanePoll)
		require.NoError(t, err)
	}
	intake, _, queue := newTestIntakeOn(t, ledger, nil)
	small, err := NewQueue(1, 1)
	require.NoError(t, err)
	intake.queue = small
	_ = queue

	done := make(chan error, 1)
	go func() { done <- intake.requeueSeen(ctx) }()
	require.Eventually(t, small.Paused, 2*time.Second, time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("requeueing should stop on shutdown")
	}

	remaining, err := ledger.RecordsInState(context.Background(), StateSeen, 10)
	require.NoError(t, err)
	assert.Len(t, remaining, 3, "nothing is consumed by being queued; the next start sees them all")
}
