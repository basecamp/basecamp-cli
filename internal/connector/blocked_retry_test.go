package connector

import (
	"context"
	"path/filepath"
	"strconv"
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

// A row the schedule has finished with stays blocked forever, and the sweep
// must not pay for it every minute for the life of the connector. It carries
// no next_retry_at once its window has passed, so the query does not read it
// at all — and it is not on the schedule, so it holds no claim either.
func TestARecordTheScheduleIsFinishedWithLeavesTheSweepEntirely(t *testing.T) {
	ledger, clock := retryLedger(t)
	blockRecord(t, ledger, 1, adapterBucketID, admission.ReasonReadFailed)
	blockRecord(t, ledger, 2, adapterBucketID, admission.ReasonReadFailed)

	// One is asked with room left in its window and stays on the schedule.
	// The other is asked on the window's edge, so its next attempt would fall
	// outside it and there is no next attempt.
	clock.Advance(admission.BlockedRetryWindow - 2*admission.BlockedRetryInterval)
	reblock(t, ledger, 2, admission.ReasonReadFailed)
	clock.Advance(2 * admission.BlockedRetryInterval)
	reblock(t, ledger, 1, admission.ReasonReadFailed)

	assert.Nil(t, getRecord(t, ledger, 1).Decision.NextRetryAt, "its window has passed")
	assert.NotNil(t, getRecord(t, ledger, 2).Decision.NextRetryAt)

	scope := BlockedRetryScope{Now: clock.Now(), Limit: 10}
	assert.Equal(t, []int64{2}, dueIDs(t, ledger, scope))
	scheduled, err := ledger.ScheduledBlockedIDs(context.Background(), scope)
	require.NoError(t, err)
	assert.Equal(t, []int64{2}, scheduled, "nothing keeps a claim for a record it can never offer")
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

// Copilot on #770, the high finding, and it is reachable. blocked_at is
// preserved across every blocked-to-blocked verdict, so a record that spent
// a week as the unbounded config_unreadable and then blocks on a transient
// read failure is measured against a week-old clock. read_failed promises 24
// hours of retries and gets none: it is scheduled against a window that
// belongs to the reason it is no longer blocked on.
//
// That is the retry mechanism stranding the work it exists to recover,
// through a different door than the out_of_scope one the card named.
func TestAReasonThatChangesGetsItsOwnWindow(t *testing.T) {
	ledger, clock := retryLedger(t)
	blockRecord(t, ledger, 1, adapterBucketID, admission.ReasonConfigUnreadable)

	// A week of an unreadable connect.json, asked every ten minutes, which
	// the unbounded window is there to allow.
	clock.Advance(7 * 24 * time.Hour)
	assert.Equal(t, []int64{1}, dueIDs(t, ledger, BlockedRetryScope{Now: clock.Now(), Limit: 10}))

	// The file is repaired, the record is decided again, and this time the
	// recording's read fails. A fresh 24 hours of read_failed retries is what
	// that reason promises.
	reblock(t, ledger, 1, admission.ReasonReadFailed)
	clock.Advance(admission.BlockedRetryInterval)
	assert.Equal(t, []int64{1}, dueIDs(t, ledger, BlockedRetryScope{Now: clock.Now(), Limit: 10}),
		"read_failed's window starts when read_failed does, not when the record first blocked")
}

// resetRunState says nothing a previous Run decided may leak into the next.
// A claim is a decision about a record, and a Run that was canceled between
// the offer and the verdict leaves the record blocked at the revision it was
// claimed at — so a claim that survived the Run would suppress that record's
// retry for the life of the process.
func TestAClaimDoesNotOutliveItsRun(t *testing.T) {
	intake, ledger, queue, clock := retryIntake(t)
	ctx := context.Background()
	blockRecord(t, ledger, 1, adapterBucketID, admission.ReasonReadFailed)
	clock.Advance(admission.BlockedRetryInterval)

	intake.sweepBlockedRetries(ctx)
	require.Equal(t, 1, queue.Depth())
	assert.Equal(t, int64(1), takeID(t, queue))

	// The run ended with the id taken from the queue and no verdict written:
	// the record is still blocked, still at the revision it was claimed at.
	intake.resetRunState()
	intake.sweepBlockedRetries(ctx)
	require.Equal(t, 1, queue.Depth(), "the next run offers it again; the revision guard makes a duplicate harmless")
	assert.Equal(t, int64(1), takeID(t, queue))
}

// Copilot on #770: the claim map held one entry per record ever retried and
// dropped none, so a connector that runs for months kept a copy of every
// event id an outage ever blocked. The bound it needs is the live one — a
// claim is worth keeping only while the record it names is still on the
// schedule.
func TestClaimsAreBoundedByTheRecordsStillOnTheSchedule(t *testing.T) {
	intake, ledger, queue, clock := retryIntake(t)
	ctx := context.Background()
	const records = 60
	for id := int64(1); id <= records; id++ {
		blockRecord(t, ledger, id, adapterBucketID, admission.ReasonReadFailed)
	}
	clock.Advance(admission.BlockedRetryInterval)
	intake.sweepBlockedRetries(ctx)
	require.Equal(t, records, queue.Depth())
	for range records {
		takeID(t, queue)
	}
	assert.Equal(t, records, intake.claimCount(), "every record on the schedule is claimed")

	// Every one of them is decided and leaves blocked. Their claims name
	// records no sweep will ever return again.
	for id := int64(1); id <= records; id++ {
		_, err := ledger.Admission().Commit(ctx, admittedVerdict(id, getRecord(t, ledger, id).Revision, "recording:"+strconv.FormatInt(id, 10)))
		require.NoError(t, err)
	}
	intake.sweepBlockedRetries(ctx)
	assert.Zero(t, intake.claimCount(), "a claim outlives neither its record's block nor its window")

	// And it keeps working after the prune: a record that blocks again is
	// claimed again.
	blockRecord(t, ledger, records+1, adapterBucketID, admission.ReasonReadFailed)
	clock.Advance(admission.BlockedRetryInterval)
	intake.sweepBlockedRetries(ctx)
	require.Equal(t, 1, queue.Depth())
	assert.Equal(t, 1, intake.claimCount())
}

// migrationsBeforeBlockedRetrySchedule is the last migration a ledger without
// retry_since and next_retry_at had applied. Migration 14 adds them.
const migrationsBeforeBlockedRetrySchedule = 13

// A ledger written before the schedule existed carries blocked records whose
// retry nothing ever computed. The upgrade owes them the attempt they were
// always promised, so the backfill puts them on the schedule due now and the
// ordinary interval takes over from their next verdict. A blocked reason the
// schedule does not run is left alone.
func TestTheUpgradePutsAlreadyBlockedRecordsOnTheSchedule(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "connector.db")
	old := applyMigrationsThrough(t, path, migrationsBeforeBlockedRetrySchedule)
	for _, row := range []struct {
		id     int64
		reason string
	}{{1, string(admission.ReasonReadFailed)}, {2, string(admission.ReasonNoRoute)}} {
		_, err := old.ExecContext(ctx, `
INSERT INTO events (id, state, reason, lane, event_type, kind, action, bucket_id, creator_id,
                    recording_id, created_at, seen_at, updated_at, decided_at, blocked_at)
VALUES (?, 'blocked', ?, 'poll', 'comment.created', 'comment_created', 'created', ?, ?, ?,
        '2026-09-18T12:00:00.000000000Z', '2026-09-18T12:00:00.000000000Z',
        '2026-09-18T12:00:00.000000000Z', '2026-09-18T12:00:00.000000000Z',
        '2026-09-18T12:00:00.000000000Z')`,
			row.id, row.reason, adapterBucketID, adapterOperatorID, 10304028972)
		require.NoError(t, err)
	}
	require.NoError(t, old.Close())

	ledger := openUpgraded(t, path)
	assert.Equal(t, []int64{1}, dueIDs(t, ledger, BlockedRetryScope{
		Now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC), Limit: 10,
	}), "read_failed is owed the attempt nothing ever ran; no_route still waits for the operator")
	assert.Nil(t, getRecord(t, ledger, 2).Decision.NextRetryAt)
}
