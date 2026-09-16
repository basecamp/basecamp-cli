package connector

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// One test per invariant the design review found unenforced or untested. The
// invariant names match the review's list (A positions, C repair, D
// classification, E lifecycle, F queue, G lock, H confidentiality).

// D2 / A3: a followed URL carries exactly its cursor. One with neither since
// nor position would silently enter at the present.
func TestInvariantD2ACursorlessContinuationIsRefused(t *testing.T) {
	client := &fakeFeedClient{}
	_, err := newTestAdapter(t, client).Poll(context.Background(),
		eventfeed.Cursor{PageURL: "https://3.basecampapi.com/2914079/events.json?types=comment.created"},
		eventfeed.Filters{})

	var pollErr *eventfeed.PollError
	require.ErrorAs(t, err, &pollErr)
	assert.NotEqual(t, eventfeed.PollTransient, pollErr.Kind)
	assert.Zero(t, client.calls, "a URL with no cursor is an entry at the present, and is never sent")
}

// H1: nothing server-chosen — a redirect target, a URL carrying a position —
// is rendered into an error the adapter returns.
func TestInvariantH1AdapterErrorsRenderNoServerChosenURL(t *testing.T) {
	leaky := errors.New(`Get "https://3.basecampapi.com/2914079/events.json?position=SECRET-POSITION": connection reset`)

	_, err := newTestAdapter(t, &fakeFeedClient{err: leaky}).Poll(context.Background(), eventfeed.Cursor{}, eventfeed.Filters{})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SECRET-POSITION")

	_, err = newTestAdapter(t, &fakeFeedClient{err: leaky}).MintStreamTicket(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SECRET-POSITION")

	redirect := errors.Join(ErrRedirectRefused, errors.New(`Get "https://evil.example.com/steal?leak=SECRET-TARGET"`))
	_, err = newTestAdapter(t, &fakeFeedClient{err: redirect}).Poll(context.Background(), eventfeed.Cursor{}, eventfeed.Filters{})
	var pollErr *eventfeed.PollError
	require.ErrorAs(t, err, &pollErr)
	assert.Equal(t, eventfeed.PollRedirectRefused, pollErr.Kind)
	assert.NotContains(t, err.Error(), "SECRET-TARGET")

	_, err = newTestAdapter(t, &fakeFeedClient{err: redirect}).MintStreamTicket(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SECRET-TARGET")
}

// H1, repair side: the walk's logs render a failure's kind, never its text.
func TestInvariantH1RepairLogsRenderNoFailureText(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, clock.at, time.Minute)
	require.NoError(t, err)

	var logs bytes.Buffer
	polls := &scriptedPolls{errs: []error{
		errors.New("position=SECRET-POSITION"),
		&eventfeed.PollError{Kind: eventfeed.PollUnrecoverable, Err: errors.New("next=SECRET-NEXT")},
	}}
	walker, _ := newTestWalker(t, ledger, polls, clock)
	walker.log = slog.New(slog.NewTextHandler(&logs, nil))
	require.NoError(t, walker.reconcile(ctx, loss))

	assert.NotContains(t, logs.String(), "SECRET")
}

type failingPositions struct{}

func (failingPositions) LastPollServedID(context.Context, eventfeed.CheckpointKey) (int64, error) {
	return 0, errors.New("disk I/O error")
}

func (failingPositions) LineagePollServedID(context.Context, eventfeed.CheckpointKey) (int64, error) {
	return 0, errors.New("disk I/O error")
}

// A3: when the safe re-entry after a refused position cannot be read, the
// connection ends rather than letting the package enter at the present.
func TestInvariantA3AnUnreadableReentryEndsTheFeedInsteadOfEnteringAtThePresent(t *testing.T) {
	ledger := newTestLedger(t)
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, ledger.Save(ctx, intake.CheckpointKey(), "position-the-server-will-refuse"))
	intake.positions = failingPositions{}

	minter.ScriptTicket(ticket())
	polls.ScriptError(&eventfeed.PollError{Kind: eventfeed.PollPositionInvalid})
	polls.ScriptPage(eventfeed.PollPage{Position: "present-entry"})

	done := runInBackground(ctx, t, intake)
	subscribedConn(t, transport)

	err := awaitReturn(t, done, "a feed whose safe re-entry cannot be read must end")
	assert.Error(t, err)
	for _, call := range polls.Calls() {
		assert.NotEqual(t, "now", call.Cursor.Since, "no poll may enter at the present")
	}
}

// G1: one lock per canonical account and agent.
func TestInvariantG1OneLockPerCanonicalAccount(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireInstanceLock(dir, "2914079", 52007412, time.Now())
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Release() })

	_, err = AcquireInstanceLock(dir, "02914079", 52007412, time.Now())
	assert.Error(t, err, "02914079 is the same account and must meet the same lock")
	_, err = AcquireInstanceLock(dir, "0", 52007412, time.Now())
	assert.Error(t, err)
}

// C2: a 410 on a pass that has not yet followed that 410's resume is
// followed, whatever the walk's entry happens to equal. Ids above the fence
// stay recoverable.
func TestInvariantC2A410OnAStoredPositionIsFollowedEvenAtTheFence(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	loss, err := ledger.RecordLoss(ctx, []int64{100, 200}, clock.at, 10*time.Minute)
	require.NoError(t, err)
	_, err = ledger.MarkUnrecoveredThrough(ctx, loss.ID, 150)
	require.NoError(t, err)
	require.NoError(t, ledger.SetRepairEntry(ctx, loss.ID, 150))
	require.NoError(t, ledger.SaveRepairCursor(ctx, loss.ID, "position-above-the-fence"))
	loss.RepairSince = 150
	loss.RepairCursor = "position-above-the-fence"

	polls := fencedPolls{resume: "https://3.basecampapi.com/2914079/events.json?since=150"}
	walker, _ := newTestWalker(t, ledger, polls, clock)
	require.NoError(t, walker.reconcile(ctx, loss))

	recovered, err := ledger.MissingIDs(ctx, loss.ID, LossRecovered)
	require.NoError(t, err)
	assert.Equal(t, []int64{200}, recovered)
}

// fencedPolls answers every stored position with the epoch's 410, and serves
// the event above the fence only from the resume.
type fencedPolls struct{ resume string }

func (f fencedPolls) Poll(_ context.Context, cursor eventfeed.Cursor, _ eventfeed.Filters) (eventfeed.PollPage, error) {
	if cursor.PageURL == f.resume || cursor.Since == "150" {
		return eventfeed.PollPage{Events: []eventfeed.Event{testEvent(200)}, Position: "above"}, nil
	}
	return eventfeed.PollPage{}, &eventfeed.PollError{Kind: eventfeed.PollGone, EpochAfterID: 150, ResumeURL: f.resume}
}

// F2: warning edges settle on the true state under concurrent offers and
// takes.
func TestInvariantF2WarningEdgesSettleOnTheTrueState(t *testing.T) {
	for range 50 {
		queue, err := NewQueue(1, 64)
		require.NoError(t, err)
		var warns, recovers atomic.Int32
		queue.OnWarn = func(int) { warns.Add(1) }
		queue.OnRecover = func(int) { recovers.Add(1) }

		ctx := context.Background()
		var wg sync.WaitGroup
		for g := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range 200 {
					require.NoError(t, queue.Offer(ctx, int64(g*1000+i)))
					_, err := queue.Take(ctx)
					require.NoError(t, err)
				}
			}()
		}
		wg.Wait()
		require.Zero(t, queue.Depth())
		require.Equal(t, warns.Load(), recovers.Load(), "every warning is answered once the backlog is gone")
	}
}

// F1: while intake is paused on a full queue, the page it is in the middle of
// is not checkpointed.
func TestInvariantF1APausedFeedDoesNotMoveTheCheckpoint(t *testing.T) {
	ledger := newTestLedger(t)
	queue, err := NewQueue(1, 1)
	require.NoError(t, err)
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{})
	intake.queue = queue
	intake.opts.Queue = queue
	minter.ScriptTicket(ticket())
	polls.ScriptPage(eventfeed.PollPage{Events: []eventfeed.Event{testEvent(500), testEvent(501)}, Position: "after-both"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(ctx, t, intake)
	subscribedConn(t, transport)

	require.Eventually(t, queue.Paused, 5*time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	_, ok, err := ledger.Load(ctx, intake.CheckpointKey())
	require.NoError(t, err)
	assert.False(t, ok, "a crash now must resume from before the page, not after it")

	_, err = queue.Take(ctx)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		position, ok, err := ledger.Load(ctx, intake.CheckpointKey())
		return err == nil && ok && position == "after-both"
	}, 5*time.Second, 5*time.Millisecond)

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}

// E2: a reconnect asked for between connections is answered by the next
// connection, not carried into it to turn its terminal error into a silent
// reconnect.
func TestInvariantE2AStaleReconnectDoesNotSwallowATerminalError(t *testing.T) {
	ledger := newTestLedger(t)
	intake, _, minter, _ := newFeedIntake(t, ledger, Options{})
	for range 4 {
		minter.ScriptError(&eventfeed.MintError{Kind: eventfeed.MintUnrecoverable, Err: errors.New("gone")})
	}
	// Asked for while no connection was running: the next connection IS the
	// answer to it.
	intake.requestReconnect()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := awaitReturn(t, runInBackground(ctx, t, intake), "the terminal error must end Run")
	assert.Error(t, err)
	assert.Equal(t, 1, minter.Calls(),
		"a latch carried into the connection turns its terminal error into a reconnect")
}

// E3: membership detection catches every change to the listed set, ignores
// learned buckets, and notices a learned bucket's revocation once the list
// has named it.
func TestInvariantE3MembershipComparison(t *testing.T) {
	intake, _, _ := newTestIntake(t, nil, nil)
	intake.snapshot = map[int64]bool{1: true, 2: true, 9: true}
	intake.learned = map[int64]bool{9: true}

	assert.False(t, intake.membershipChanged([]int64{1, 2}), "a learned bucket the list omits is not a change")
	assert.True(t, intake.membershipChanged([]int64{1}), "a listed bucket dropping off is a change")

	intake.snapshot = map[int64]bool{1: true, 2: true, 9: true}
	intake.learned = map[int64]bool{9: true}
	assert.False(t, intake.membershipChanged([]int64{1, 2, 9}), "the list catching up with a learned bucket is not a change")
	assert.True(t, intake.membershipChanged([]int64{1, 2}), "and its later revocation is")
}

// H2 availability: two processes opening one fresh ledger both get it.
func TestInvariantH2ConcurrentFreshOpensAllSucceed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "connector.db")
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ledger, err := OpenLedger(path)
			if err == nil {
				err = ledger.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
}
