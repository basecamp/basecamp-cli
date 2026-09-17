package connector

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// C2: a loss is always somewhere — repaired, scheduled for another attempt, or
// closed with a reason. Waiting on a server's own delay is not spending the
// window, so a throttle can neither condemn the ids nor run the clock out.
func TestAThrottleDoesNotSpendTheLossWindow(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	// A window with a minute left, and a server asking for fifteen.
	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, clock.at.Add(-9*time.Minute), 10*time.Minute, eventfeed.Filters{})
	require.NoError(t, err)
	before := loss.DeadlineAt

	// Two refusals, each longer than what is left of the window, then the
	// page. Only a window that does not spend on the server's own delays
	// still has attempts left by then.
	throttle := &eventfeed.PollError{Kind: eventfeed.PollThrottled, RetryAfter: 15 * time.Minute}
	polls := &scriptedPolls{
		errs:  []error{throttle, throttle},
		pages: []eventfeed.PollPage{{}, {}, {Events: []eventfeed.Event{testEvent(17099838509)}, Position: "p"}},
	}
	walker, _ := newTestWalker(t, ledger, polls, clock)
	walker.sleep = func(_ context.Context, d time.Duration) error {
		clock.at = clock.at.Add(d)
		return nil
	}
	require.NoError(t, walker.reconcile(ctx, loss))

	unrecovered, err := ledger.UnrecoveredIDs(ctx)
	require.NoError(t, err)
	assert.Empty(t, unrecovered, "the server refused the attempt; it says nothing about the ids")
	recovered, err := ledger.MissingIDs(ctx, loss.ID, LossRecovered)
	require.NoError(t, err)
	assert.Equal(t, []int64{17099838509}, recovered, "the retry after the wait found it")
	_ = before
}

// C2: a loss the repair queue had no room for is swept back in, without
// waiting for a restart.
func TestALossSkippedByAFullQueueIsSweptBackIn(t *testing.T) {
	polls := &countingPolls{}
	intake, ledger, _ := newTestIntake(t, polls, nil)
	intake.opts.RepairInterval = time.Hour
	intake.repairQueueSize = 1
	intake.repairSweep = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := range 6 {
		_, err := ledger.RecordLoss(ctx, []int64{int64(2000 + i)}, intake.now().Add(-time.Hour), time.Minute, eventfeed.Filters{})
		require.NoError(t, err)
	}
	require.NoError(t, intake.resumeReconciliation(ctx))

	require.Eventually(t, func() bool {
		open, err := ledger.OpenLosses(context.Background())
		return err == nil && len(open) == 0
	}, 10*time.Second, 20*time.Millisecond, "every open loss is eventually attempted, queue or no queue")

	cancel()
	intake.repairs.Wait()
}

// The same rule inside the window: the deadline moves out the moment the
// server asks for a wait, not only on the last attempt.
func TestAThrottleInsideTheWindowPostponesItImmediately(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, clock.at, time.Hour, eventfeed.Filters{})
	require.NoError(t, err)

	polls := &scriptedPolls{
		errs:  []error{&eventfeed.PollError{Kind: eventfeed.PollThrottled, RetryAfter: 15 * time.Minute}},
		pages: []eventfeed.PollPage{{}, {Events: []eventfeed.Event{testEvent(17099838509)}, Position: "p"}},
	}
	walker, _ := newTestWalker(t, ledger, polls, clock)
	var atWait time.Time
	walker.sleep = func(sleepCtx context.Context, d time.Duration) error {
		if atWait.IsZero() {
			open, err := ledger.OpenLosses(sleepCtx)
			require.NoError(t, err)
			require.Len(t, open, 1)
			atWait = open[0].DeadlineAt
		}
		clock.at = clock.at.Add(d)
		return nil
	}
	require.NoError(t, walker.reconcile(ctx, loss))

	require.False(t, atWait.IsZero(), "the walk waited")
	assert.Equal(t, loss.DeadlineAt.Add(15*time.Minute).UTC(), atWait.UTC(),
		"the window moved out by exactly what the server asked for")
}

// One throttle governs one pass. A later pass with a healthy server waits the
// ordinary cadence and does not push the window out again.
func TestOneThrottleDoesNotGovernEveryLaterPass(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, clock.at, time.Hour, eventfeed.Filters{})
	require.NoError(t, err)

	polls := &scriptedPolls{errs: []error{&eventfeed.PollError{Kind: eventfeed.PollThrottled, RetryAfter: 15 * time.Minute}}}
	walker, _ := newTestWalker(t, ledger, polls, clock)
	var waits []time.Duration
	walker.sleep = func(sleepCtx context.Context, d time.Duration) error {
		waits = append(waits, d)
		clock.at = clock.at.Add(d)
		if len(waits) >= 3 {
			return context.Canceled
		}
		return nil
	}
	require.ErrorIs(t, walker.reconcile(ctx, loss), context.Canceled)

	require.Len(t, waits, 3)
	assert.Equal(t, 15*time.Minute, waits[0], "the server asked for this one")
	assert.Equal(t, walker.interval, waits[1], "and said nothing about the next")
	assert.Equal(t, walker.interval, waits[2])

	open, err := ledger.OpenLosses(ctx)
	require.NoError(t, err)
	require.Len(t, open, 1)
	assert.Equal(t, loss.DeadlineAt.Add(15*time.Minute).UTC(), open[0].DeadlineAt.UTC(),
		"the window moved out once, for the one wait the server asked for")
}

// C2: a loss cannot live forever. However long a server keeps asking for
// patience, a loss closes at its absolute deadline with its ids reported.
func TestALossClosesAtItsAbsoluteDeadline(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, clock.at, 10*time.Minute, eventfeed.Filters{})
	require.NoError(t, err)

	walker, _ := newTestWalker(t, ledger, alwaysThrottling{}, clock)
	walker.sleep = func(_ context.Context, d time.Duration) error {
		clock.at = clock.at.Add(d)
		return nil
	}
	require.NoError(t, walker.reconcile(ctx, loss))

	open, err := ledger.OpenLosses(ctx)
	require.NoError(t, err)
	assert.Empty(t, open, "closed exactly once, at its absolute deadline")
	unrecovered, err := ledger.UnrecoveredIDs(ctx)
	require.NoError(t, err)
	assert.Equal(t, []int64{17099838509}, unrecovered, "and its ids are reported, not forgotten")
	assert.False(t, clock.at.Before(loss.DetectedAt.Add(maxLossLifetime)), "not before the absolute deadline")
}
