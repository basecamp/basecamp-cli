package connector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// A3: a missing position does not mean a new filter. The package confirms a
// page before it saves the position, so a failed save leaves this filter's
// poll-served id recorded with no position. A restart re-enters at that id,
// not at another filter set's larger one.
func TestARestartWithoutAPositionPrefersThisFilterSetsOwnServedID(t *testing.T) {
	ledger := newTestLedger(t)
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, ledger.NotePollServed(ctx, intake.CheckpointKey(), 1000))
	other := intake.CheckpointKey()
	other.FilterKey = "srv2-0000000000000000"
	require.NoError(t, ledger.NotePollServed(ctx, other, 2000))

	minter.ScriptTicket(ticket())
	polls.ScriptPage(eventfeed.PollPage{Position: "p1"})

	done := runInBackground(ctx, t, intake)
	subscribedConn(t, transport)
	require.Eventually(t, func() bool { return polls.CallCount() > 0 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "1000", polls.Calls()[0].Cursor.Since,
		"another filter set's id is past events this one never served")

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}

// E2: a safe re-entry that is itself refused ends the run. Reconnecting again
// would mint, dial and poll in a tight loop against the API.
func TestARefusedReentryEndsTheRunInsteadOfLooping(t *testing.T) {
	ledger := newTestLedger(t)
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, ledger.Save(ctx, intake.CheckpointKey(), "refused"))
	require.NoError(t, ledger.NotePollServed(ctx, intake.CheckpointKey(), 1000))

	for range 50 {
		minter.ScriptTicket(ticket())
		polls.ScriptError(&eventfeed.PollError{Kind: eventfeed.PollPositionInvalid})
	}

	done := runInBackground(ctx, t, intake)
	answered := 0
	var err error
	require.Eventually(t, func() bool {
		if conns := transport.Conns(); len(conns) > answered {
			answerSubscription(conns[len(conns)-1])
			answered = len(conns)
		}
		select {
		case err = <-done:
			return true
		default:
			return false
		}
	}, 5*time.Second, 10*time.Millisecond, "a refused re-entry must end the run")
	assert.Error(t, err)
	assert.LessOrEqual(t, minter.Calls(), 3, "one refusal, one re-entry, one refusal of that: then stop")
}

// C2: a loss whose repair can never finish — every pass ends in a failure no
// retry fixes — is still closed once its window has passed, so status shows its
// ids as unrecovered rather than open forever.
func TestALossPastItsWindowClosesEvenWhenItsWalkCannotFinish(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, clock.at.Add(-24*time.Hour), 10*time.Minute, eventfeed.Filters{})
	require.NoError(t, err)

	polls := &scriptedPolls{errs: []error{&eventfeed.PollError{Kind: eventfeed.PollFilterInvalid, Err: errors.New("x")}}}
	walker, _ := newTestWalker(t, ledger, polls, clock)
	require.NoError(t, walker.reconcile(ctx, loss))

	open, err := ledger.OpenLosses(ctx)
	require.NoError(t, err)
	assert.Empty(t, open)
	unrecovered, err := ledger.UnrecoveredIDs(ctx)
	require.NoError(t, err)
	assert.Equal(t, []int64{17099838509}, unrecovered)
}
