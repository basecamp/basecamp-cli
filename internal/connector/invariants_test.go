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

// H1, repair side: the walk's logs render a failure's kind, never its text.
func TestInvariantH1RepairLogsRenderNoFailureText(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, clock.at, time.Minute, eventfeed.Filters{})
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
	loss, err := ledger.RecordLoss(ctx, []int64{100, 200}, clock.at, 10*time.Minute, eventfeed.Filters{})
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

// E3: the listing the lister gives is what decides a change. A bucket learned
// from an arriving event is provisional and never counts as one, and it is
// revoked when a listing omits it.
func TestInvariantE3MembershipComparison(t *testing.T) {
	intake, _, _ := newTestIntake(t, nil, nil)
	intake.opts.Membership = &flakyMembership{buckets: []int64{1, 2}}

	require.False(t, intake.adoptListing([]int64{1, 2}), "the first listing is a baseline")
	intake.noteBucket(9)
	assert.False(t, intake.adoptListing([]int64{1, 2}), "a learned bucket the listing omits is not a change")

	intake.mu.Lock()
	held := intake.learned[9]
	intake.mu.Unlock()
	assert.False(t, held, "and that listing revoked it")

	assert.True(t, intake.adoptListing([]int64{1}), "a listed bucket dropping off is a change")
	assert.True(t, intake.adoptListing([]int64{1, 2}), "and its return is a change")
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

// E4: terminal means terminal. A completed or discarded record that could move
// back into the working states could be dispatched a second time — and once
// DropContent has taken its payload, requeued as work with nothing in it.
func TestInvariantE4TerminalRecordsHaveNoWayBack(t *testing.T) {
	active := []RecordState{StateSeen, StateAdmitted, StateQueued, StateBlocked, StateDispatched}
	for _, terminal := range []RecordState{StateCompleted, StateDiscarded} {
		for _, target := range append(active, terminalPeer(terminal)) {
			t.Run(string(terminal)+" to "+string(target), func(t *testing.T) {
				ledger := newTestLedger(t)
				ctx := context.Background()
				require.NoError(t, reachTerminal(t, ledger, 1, terminal))

				reason := ""
				if target == StateBlocked || target == StateDiscarded {
					reason = "a reason"
				}
				err := ledger.SetState(ctx, 1, target, reason)

				require.Error(t, err)
				assert.ErrorIs(t, err, ErrNotATransition)
				record, ok, getErr := ledger.Get(ctx, 1)
				require.NoError(t, getErr)
				require.True(t, ok)
				assert.Equal(t, terminal, record.State, "the record stayed where it was")
			})
		}
	}
}

// Writing the state a record already has is a repeat, not a move: a retry
// after a crash is not an error, and the record does not leave its state.
func TestInvariantE4TerminalRecordsTolerateARepeat(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	require.NoError(t, reachTerminal(t, ledger, 1, StateCompleted))

	require.NoError(t, ledger.SetState(ctx, 1, StateCompleted, ""))

	record, ok, err := ledger.Get(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, StateCompleted, record.State)
}

// The refusal is the database's too, so anything that ever writes to this file
// meets it — not only this package's own SetState.
func TestInvariantE4TheDatabaseRefusesLeavingATerminalState(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	require.NoError(t, reachTerminal(t, ledger, 1, StateDiscarded))

	_, err := ledger.db.ExecContext(ctx, `UPDATE events SET state = 'seen' WHERE id = 1`)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal record")
}

// A record that does not exist is told apart from a transition that is not
// allowed: one is a missing row, the other a refusal.
func TestInvariantE4AMissingRecordIsNotARefusedTransition(t *testing.T) {
	ledger := newTestLedger(t)

	err := ledger.SetState(context.Background(), 404, StateAdmitted, "")

	assert.ErrorIs(t, err, ErrNoSuchRecord)
	assert.NotErrorIs(t, err, ErrNotATransition)
}

// terminalPeer is the other terminal state, so the pairs cover completed to
// discarded and back.
func terminalPeer(state RecordState) RecordState {
	if state == StateCompleted {
		return StateDiscarded
	}
	return StateCompleted
}

// reachTerminal walks a fresh record to a terminal state along the lifecycle's
// own edges.
func reachTerminal(t *testing.T, ledger *Ledger, id int64, terminal RecordState) error {
	t.Helper()
	ctx := context.Background()
	if _, err := ledger.RecordSeen(ctx, testEvent(id), LanePoll); err != nil {
		return err
	}
	if terminal == StateDiscarded {
		return ledger.SetState(ctx, id, StateDiscarded, "untrusted_author")
	}
	dispatchForTest(t, ledger, id)
	return ledger.SetState(ctx, id, StateCompleted, "")
}

// A2: a cursor older than the feed's current epoch is never usable. A 410 with
// an epoch says the history below it is gone for good, so the position the
// walk was holding is not merely superseded — it is refused by the server
// forever, and a pass that ends for some other reason must not hand it back.
func TestInvariantA2APreEpochCursorIsNeverUsedAgain(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	// The missing id is above the epoch, so the walk still has something to
	// serve after the fence and follows the resume.
	loss, err := ledger.RecordLoss(ctx, []int64{17099838700}, clock.at, time.Hour, eventfeed.Filters{})
	require.NoError(t, err)
	require.NoError(t, ledger.SaveRepairCursor(ctx, loss.ID, "PRE-EPOCH-POSITION"))
	loss.RepairCursor = "PRE-EPOCH-POSITION"

	const epoch = int64(17099838600)
	polls := &scriptedPolls{errs: []error{
		// The stored cursor is below the epoch: the fence.
		&eventfeed.PollError{
			Kind:         eventfeed.PollGone,
			EpochAfterID: epoch,
			ResumeURL:    "https://3.basecampapi.com/2914079/events.json?since=17099838600",
			Err:          errors.New("gone"),
		},
		// The resume then fails transiently — a connection reset, not a
		// verdict — and the pass ends.
		errors.New("connection reset"),
	}}
	walker, _ := newTestWalker(t, ledger, polls, clock)

	cursor, err := walker.walk(ctx, &loss)
	require.NoError(t, err)

	assert.Empty(t, cursor, "the pass must not carry the refused position back to its caller")
	assert.Empty(t, loss.RepairCursor)
	stored, err := ledger.OpenLosses(ctx)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Empty(t, stored[0].RepairCursor, "the pre-epoch cursor is gone from the ledger too")
	assert.Equal(t, epoch, stored[0].RepairSince)

	// The next pass enters at the epoch, never at the refused position.
	polls.errs = nil
	_, err = walker.walk(ctx, &stored[0])
	require.NoError(t, err)
	entered := polls.cursors[len(polls.cursors)-1]
	assert.Equal(t, "17099838600", entered.Since)
	// The first poll of the first pass is where the refusal was discovered.
	// Nothing after it may name that position again.
	for _, seen := range polls.cursors[1:] {
		assert.NotEqual(t, "PRE-EPOCH-POSITION", seen.Position, "no pass may re-enter at the refused position")
	}
}

// The lifecycle carries the edges the later cards actually commit, so neither
// has to write state around SetState to make its own contract work.
func TestInvariantE4TheLifecycleCarriesTheEdgesLaterCardsCommit(t *testing.T) {
	// Admission commits an admitted verdict AS queued when the conversation
	// is live, so the record goes from seen to queued in one write.
	t.Run("seen to queued", func(t *testing.T) {
		ledger := newTestLedger(t)
		ctx := context.Background()
		_, err := ledger.RecordSeen(ctx, testEvent(1), LanePoll)
		require.NoError(t, err)

		require.NoError(t, ledger.SetState(ctx, 1, StateQueued, ""))

		record, ok, err := ledger.Get(ctx, 1)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, StateQueued, record.State)
	})

	// A dispatched record never handed to a worker returns to admitted when
	// its task is superseded.
	t.Run("dispatched back to admitted", func(t *testing.T) {
		ledger := newTestLedger(t)
		ctx := context.Background()
		_, err := ledger.RecordSeen(ctx, testEvent(1), LanePoll)
		require.NoError(t, err)
		grant := dispatchForTest(t, ledger, 1)

		require.NoError(t, ledger.SupersedeTask(ctx, grant.ID))

		record, ok, err := ledger.Get(ctx, 1)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, StateAdmitted, record.State)
	})
}

// updated_at is the retention clock. Writing the state a record already has is
// a repeat, and a repeat must not restart the window on a finished record.
func TestInvariantE4ARepeatDoesNotRestartRetention(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	ledger.now = func() time.Time { return at }
	require.NoError(t, reachTerminal(t, ledger, 1, StateCompleted))

	ledger.now = func() time.Time { return at.Add(90 * 24 * time.Hour) }
	require.NoError(t, ledger.SetState(ctx, 1, StateCompleted, ""))

	dropped, err := ledger.DropContent(ctx, at.Add(time.Hour), at.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, dropped, "the repeat must not have pushed the record's retention forward")
}
