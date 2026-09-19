package connector

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// blockRecord records a pointer in bucket and blocks it for reason, at the
// ledger's current clock.
func blockRecord(t *testing.T, ledger *Ledger, id, bucket int64, reason admission.Reason) {
	t.Helper()
	ctx := context.Background()
	ev := testEvent(id)
	ev.BucketID = bucket
	_, err := ledger.RecordSeen(ctx, ev, LanePoll)
	require.NoError(t, err)
	_, err = ledger.Admission().Commit(ctx, blockedVerdict(id, getRecord(t, ledger, id).Revision, reason))
	require.NoError(t, err)
	require.Equal(t, StateBlocked, getRecord(t, ledger, id).State)
}

// reblock is the verdict a retry writes when the record blocks again: it
// bumps the revision and moves decided_at to the ledger's clock, leaving
// blocked_at where it was.
func reblock(t *testing.T, ledger *Ledger, id int64, reason admission.Reason) {
	t.Helper()
	_, err := ledger.Admission().Commit(context.Background(),
		blockedVerdict(id, getRecord(t, ledger, id).Revision, reason))
	require.NoError(t, err)
}

// dueIDs is the ids the query calls due, in the order it returned them.
func dueIDs(t *testing.T, ledger *Ledger, scope BlockedRetryScope) []int64 {
	t.Helper()
	records, err := ledger.DueBlockedRetries(context.Background(), scope)
	require.NoError(t, err)
	ids := make([]int64, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
}

// retryLedger is a ledger on a clock the test moves.
func retryLedger(t *testing.T) (*Ledger, *obClock) {
	t.Helper()
	ledger := newTestLedger(t)
	clock := &obClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	ledger.now = clock.Now
	return ledger, clock
}

// The requirement the previous attempt was taken out over: out_of_scope is a
// terminal discard, and the projects a run leaves out are another run's to
// dispatch. A sweep that offered one would cause the permanent loss it exists
// to prevent.
func TestDueBlockedRetriesKeepsToTheRunsProjectScope(t *testing.T) {
	ledger, clock := retryLedger(t)
	blockRecord(t, ledger, 1, adapterBucketID, admission.ReasonReadFailed)
	blockRecord(t, ledger, 2, adapterBucketID+1, admission.ReasonReadFailed)
	clock.Advance(admission.BlockedRetryInterval)

	assert.Equal(t, []int64{1}, dueIDs(t, ledger, BlockedRetryScope{
		Buckets: []int64{adapterBucketID}, Now: clock.Now(), Limit: 10,
	}), "a run restricted to one project never offers the other's record")

	assert.Equal(t, []int64{1, 2}, dueIDs(t, ledger, BlockedRetryScope{
		Now: clock.Now(), Limit: 10,
	}), "no --project is every project the agent sees, as it is for admission's own gate")

	assert.Equal(t, []int64{1, 2}, dueIDs(t, ledger, BlockedRetryScope{
		Buckets: []int64{adapterBucketID + 1, adapterBucketID, adapterBucketID}, Now: clock.Now(), Limit: 10,
	}), "the scope is a set, whatever order or repeats it arrives in")
}

// Which reasons automatic re-decision is turned on for. The three left out
// wait for a decision, not for time: no_route for the operator serving the
// project, bucket_mismatch and unroutable for facts about the pointer that no
// later read can change.
func TestDueBlockedRetriesRunsOnlyTheReasonsTheScheduleNames(t *testing.T) {
	ledger, clock := retryLedger(t)
	timed := []admission.Reason{
		admission.ReasonReadFailed, admission.ReasonReadUnresolved,
		admission.ReasonDeltaUnverified, admission.ReasonTrustUnverified,
		admission.ReasonConfigUnreadable,
	}
	untimed := []admission.Reason{
		admission.ReasonNoRoute, admission.ReasonBucketMismatch, admission.ReasonUnroutable,
	}
	want := make([]int64, 0, len(timed))
	id := int64(1)
	for _, reason := range timed {
		blockRecord(t, ledger, id, adapterBucketID, reason)
		want = append(want, id)
		id++
	}
	for _, reason := range untimed {
		blockRecord(t, ledger, id, adapterBucketID, reason)
		id++
	}
	clock.Advance(admission.BlockedRetryInterval)

	assert.Equal(t, want, dueIDs(t, ledger, BlockedRetryScope{Now: clock.Now(), Limit: 10}))
}

// The schedule is admission.NextBlockedRetry's, read back off the columns the
// verdict wrote — not a second copy of it in SQL.
func TestDueBlockedRetriesWaitsOutTheIntervalAndTheWindow(t *testing.T) {
	t.Run("nothing before the interval is up", func(t *testing.T) {
		ledger, clock := retryLedger(t)
		blockRecord(t, ledger, 1, adapterBucketID, admission.ReasonReadFailed)

		clock.Advance(admission.BlockedRetryInterval - time.Minute)
		assert.Empty(t, dueIDs(t, ledger, BlockedRetryScope{Now: clock.Now(), Limit: 10}))
		clock.Advance(time.Minute)
		assert.Equal(t, []int64{1}, dueIDs(t, ledger, BlockedRetryScope{Now: clock.Now(), Limit: 10}))
	})

	t.Run("a throttle is never asked before the server's deadline", func(t *testing.T) {
		ledger, clock := retryLedger(t)
		blockedAt := clock.Now()
		ctx := context.Background()
		_, err := ledger.RecordSeen(ctx, testEvent(1), LanePoll)
		require.NoError(t, err)
		v := blockedVerdict(1, 0, admission.ReasonThrottled)
		v.RetryAt = blockedAt.Add(45 * time.Minute)
		_, err = ledger.Admission().Commit(ctx, v)
		require.NoError(t, err)

		clock.Advance(20 * time.Minute)
		assert.Empty(t, dueIDs(t, ledger, BlockedRetryScope{Now: clock.Now(), Limit: 10}),
			"the interval passed, the server's deadline did not")
		clock.Advance(25 * time.Minute)
		assert.Equal(t, []int64{1}, dueIDs(t, ledger, BlockedRetryScope{Now: clock.Now(), Limit: 10}))
	})

	t.Run("a day of a failing server hands the record to a person", func(t *testing.T) {
		ledger, clock := retryLedger(t)
		blockRecord(t, ledger, 1, adapterBucketID, admission.ReasonReadFailed)
		blockRecord(t, ledger, 2, adapterBucketID, admission.ReasonConfigUnreadable)

		// The schedule has been running all day: the last attempt is on the
		// window's edge, and the next one would fall outside it.
		clock.Advance(admission.BlockedRetryWindow)
		reblock(t, ledger, 1, admission.ReasonReadFailed)
		reblock(t, ledger, 2, admission.ReasonConfigUnreadable)

		clock.Advance(admission.BlockedRetryInterval)
		assert.Equal(t, []int64{2}, dueIDs(t, ledger, BlockedRetryScope{Now: clock.Now(), Limit: 10}),
			"a day of a failing server is a server that is not coming back; an unreadable connect.json has no window")

		clock.Advance(30 * 24 * time.Hour)
		assert.Equal(t, []int64{2}, dueIDs(t, ledger, BlockedRetryScope{Now: clock.Now(), Limit: 10}),
			"a month later it is still asking")
	})

	t.Run("a retry that came due while nothing was running is late, not excused", func(t *testing.T) {
		ledger, clock := retryLedger(t)
		blockRecord(t, ledger, 1, adapterBucketID, admission.ReasonReadFailed)

		// The connector was down for a week. The attempt the record never
		// got is owed to it: the window bounds how long the schedule keeps
		// asking, not how long an answer stays worth having.
		clock.Advance(7 * 24 * time.Hour)
		assert.Equal(t, []int64{1}, dueIDs(t, ledger, BlockedRetryScope{Now: clock.Now(), Limit: 10}))

		reblock(t, ledger, 1, admission.ReasonReadFailed)
		clock.Advance(admission.BlockedRetryInterval)
		assert.Empty(t, dueIDs(t, ledger, BlockedRetryScope{Now: clock.Now(), Limit: 10}),
			"it has had its catch-up; the window is long past and a person owns it now")
	})
}

// A row the schedule has finished with stays blocked forever. Read under a
// plain LIMIT it would hold a place in every sweep's window for good, and the
// due rows behind it would never be reached. The query pages past them
// instead.
func TestDueBlockedRetriesIsNotStarvedByTheRecordsItSkips(t *testing.T) {
	ledger, clock := retryLedger(t)
	finished := int64(dueBlockedBatch + 10)
	for id := int64(1); id <= finished; id++ {
		blockRecord(t, ledger, id, adapterBucketID, admission.ReasonReadFailed)
	}
	// A page and more of rows the schedule is done with: blocked a day ago,
	// last asked on the window's edge.
	clock.Advance(admission.BlockedRetryWindow)
	for id := int64(1); id <= finished; id++ {
		reblock(t, ledger, id, admission.ReasonReadFailed)
	}
	blockRecord(t, ledger, finished+1, adapterBucketID, admission.ReasonReadFailed)
	clock.Advance(admission.BlockedRetryInterval)

	assert.Equal(t, []int64{finished + 1}, dueIDs(t, ledger, BlockedRetryScope{Now: clock.Now(), Limit: 10}),
		"the due row is found behind a page of rows the schedule is done with")
}

// retryIntake is an intake and ledger on one clock the test moves, with
// nothing else offering anything to the queue.
func retryIntake(t *testing.T) (*Intake, *Ledger, *Queue, *obClock) {
	t.Helper()
	intake, ledger, queue := newTestIntake(t, nil, nil)
	clock := &obClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	intake.now, ledger.now = clock.Now, clock.Now
	return intake, ledger, queue, clock
}

func takeID(t *testing.T, queue *Queue) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, err := queue.Take(ctx)
	require.NoError(t, err)
	return id
}

// The sweep itself: a due blocked record is offered, and offered once. Queue
// .Offer does not deduplicate, so without the claim the same rows would be
// offered again on every tick while the rows behind them in the window
// starve, and a second copy could decide a record the moment the first
// re-blocked it.
func TestTheSweepOffersADueBlockedRecordOncePerRevision(t *testing.T) {
	intake, ledger, queue, clock := retryIntake(t)
	ctx := context.Background()
	blockRecord(t, ledger, 1, adapterBucketID, admission.ReasonReadFailed)

	intake.sweepBlockedRetries(ctx)
	assert.Zero(t, queue.Depth(), "not due yet")

	clock.Advance(admission.BlockedRetryInterval)
	intake.sweepBlockedRetries(ctx)
	require.Equal(t, 1, queue.Depth())
	assert.Equal(t, int64(1), takeID(t, queue))

	// The record is still blocked and still due; the claim is what stops it
	// being offered a second time.
	intake.sweepBlockedRetries(ctx)
	clock.Advance(admission.BlockedRetryInterval)
	intake.sweepBlockedRetries(ctx)
	assert.Zero(t, queue.Depth(), "claimed at this revision, and nothing has decided it since")

	// Admission re-decides it and it blocks again. That bumps the revision
	// and moves decided_at, so the claim is retired and the next retry is an
	// interval away — never sooner.
	_, err := ledger.Admission().Commit(ctx, blockedVerdict(1, getRecord(t, ledger, 1).Revision, admission.ReasonReadFailed))
	require.NoError(t, err)
	intake.sweepBlockedRetries(ctx)
	assert.Zero(t, queue.Depth(), "re-blocked just now: the interval has not passed")

	clock.Advance(admission.BlockedRetryInterval)
	intake.sweepBlockedRetries(ctx)
	require.Equal(t, 1, queue.Depth())
	assert.Equal(t, int64(1), takeID(t, queue))
}

// The sweep passes the run's scope down, so the query cannot be asked for a
// record this connector must not decide.
func TestTheSweepNeverOffersARecordOutsideTheRunsScope(t *testing.T) {
	intake, ledger, queue, clock := retryIntake(t)
	intake.opts.Filters = eventfeed.Filters{Buckets: []int64{adapterBucketID}}
	blockRecord(t, ledger, 1, adapterBucketID+1, admission.ReasonReadFailed)
	clock.Advance(admission.BlockedRetryInterval)

	intake.sweepBlockedRetries(context.Background())
	assert.Zero(t, queue.Depth(), "offering it would discard it out_of_scope, terminally")
}

// The join, not the pieces. The previous attempt's end-to-end test called the
// query and the queue itself, so it stayed green with the periodic hook
// deleted. This one drives the real ticker and nothing else: delete the
// sweepBlockedRetries call from sweepLosses and this fails, while the test
// above it stays green.
func TestTheRepairSweepTickerRunsTheBlockedRetrySchedule(t *testing.T) {
	intake, ledger, queue, clock := retryIntake(t)
	intake.opts.RepairInterval = time.Hour
	intake.repairSweep = 10 * time.Millisecond
	blockRecord(t, ledger, 1, adapterBucketID, admission.ReasonReadFailed)
	clock.Advance(admission.BlockedRetryInterval)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	intake.startRepairWorkers(ctx)
	t.Cleanup(func() {
		cancel()
		intake.repairs.Wait()
		intake.releaseRepairWorkers()
	})

	assert.Equal(t, int64(1), takeID(t, queue),
		"the timer the connector actually runs is what re-offers a blocked record")
}
