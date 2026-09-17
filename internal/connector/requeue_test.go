package connector

import (
	"context"
	"errors"
	"sync"
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

// The restart is not the only answer. A repair walk is the case that makes it
// urgent: the commit resolves the loss's missing id, so the reconciliation
// that follows sees nothing missing and closes, and a connector that runs for
// weeks would never mention the event again. Every commit goes through one
// handover path, and a failure on it is remembered and retried in process.
func TestAnEventWhoseHandOffFailedIsSweptWithoutARestart(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	intake, _, queue := newTestIntakeOn(t, ledger, failingWriter{})

	require.Error(t, intake.ingest(ctx, testEvent(17099838500), LaneRepair))
	require.Zero(t, queue.Depth(), "the id never reached the queue")

	// The ledger's dedupe now suppresses the event on every later delivery of
	// itself, so nothing but the sweep can hand it over.
	fresh, err := ledger.RecordSeen(ctx, testEvent(17099838500), LaneRepair)
	require.NoError(t, err)
	require.False(t, fresh)

	intake.sweepStranded(ctx)

	require.Equal(t, 1, queue.Depth())
	id, err := queue.Take(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(17099838500), id)

	// Exactly once: a second sweep has nothing left to offer.
	intake.sweepStranded(ctx)
	assert.Zero(t, queue.Depth())
}

// A sweep that cannot hand an id over leaves it for the next one, and for the
// next start after that.
func TestASweepThatCannotHandOverKeepsTheStrandedID(t *testing.T) {
	ctx := context.Background()
	intake, _, _ := newTestIntakeOn(t, newTestLedger(t), failingWriter{})
	full, err := NewQueue(1, 1)
	require.NoError(t, err)
	intake.queue = full

	require.Error(t, intake.ingest(ctx, testEvent(42), LaneLive))
	require.NoError(t, full.Offer(ctx, 99)) // no room for anything else

	stopped, cancel := context.WithCancel(ctx)
	cancel()
	intake.sweepStranded(stopped)
	assert.Equal(t, 1, full.Depth(), "the sweep waited for room and gave up, keeping the id")

	waiting, err := full.Take(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(99), waiting)

	intake.sweepStranded(ctx)
	bounded, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	id, err := full.Take(bounded)
	require.NoError(t, err, "the id the earlier sweep kept should be handed over now")
	assert.Equal(t, int64(42), id)
}

// The start's re-queue offers what is in seen and then forgets the ids it
// carried in — but only those. A repair walk is already running by then, and
// it serves OLD ids, which the paging may already have passed: one stranded
// mid-pass must not be forgotten by a clear that assumed it had offered
// everything.
func TestTheStartOnlyForgetsTheStrandedIDsItCarriedIn(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	_, err := ledger.RecordSeen(ctx, testEvent(1), LanePoll)
	require.NoError(t, err)

	intake, _, _ := newTestIntakeOn(t, ledger, nil)
	queue, err := NewQueue(1, 10)
	require.NoError(t, err)
	intake.queue = queue
	var once sync.Once
	// Stranded while the re-queue is paging: a repair walk's hand-off failing
	// on an id the paging has already gone past.
	queue.OnWarn = func(int) { once.Do(func() { intake.strand(999) }) }

	require.NoError(t, intake.requeueSeen(ctx))
	require.Equal(t, 1, queue.Depth())

	intake.sweepStranded(ctx)

	require.Equal(t, 2, queue.Depth(), "the id stranded mid-pass was never offered")
	first, err := queue.Take(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), first)
	second, err := queue.Take(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(999), second)
}
