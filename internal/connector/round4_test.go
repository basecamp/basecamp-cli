package connector

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// headPolls models a feed where one event was committed after the stored
// position. The stored position is refused; an entry at the present serves
// nothing; any entry in served history serves the event.
type headPolls struct {
	mu      sync.Mutex
	cursors []eventfeed.Cursor
}

func (h *headPolls) Poll(_ context.Context, cursor eventfeed.Cursor, _ eventfeed.Filters) (eventfeed.PollPage, error) {
	h.mu.Lock()
	h.cursors = append(h.cursors, cursor)
	h.mu.Unlock()
	switch {
	case cursor.Position == "checkpoint-from-empty-pages":
		return eventfeed.PollPage{}, &eventfeed.PollError{Kind: eventfeed.PollPositionInvalid}
	case cursor.Since == "now" || (cursor.Since == "" && cursor.Position == "" && cursor.PageURL == ""):
		return eventfeed.PollPage{Position: "head"}, nil
	default:
		return eventfeed.PollPage{Events: []eventfeed.Event{testEvent(17099838600)}, Position: "after-the-event"}, nil
	}
}

// A position saved from empty pages leaves this filter set's poll-served id at
// zero. If the server then refuses it, the safe re-entry is the beginning of
// served history — not another filter set's id, and not the present.
func TestARefusedPositionWithNoPollServedIDReentersAtTheBeginningNotThePresent(t *testing.T) {
	for _, lineage := range []int64{0, 17099838700} {
		t.Run("lineage "+strconv.FormatInt(lineage, 10), func(t *testing.T) {
			ledger := newTestLedger(t)
			polls := &headPolls{}
			queue, err := NewQueue(DefaultBacklogWarn, DefaultBacklogPause)
			require.NoError(t, err)
			intake, transport, minter, _ := newFeedIntake(t, ledger, Options{})
			intake.opts.PollsFor = func() eventfeed.PollSource { return polls }
			intake.queue = queue
			for range 4 {
				minter.ScriptTicket(ticket())
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			require.NoError(t, ledger.Save(ctx, intake.CheckpointKey(), "checkpoint-from-empty-pages"))
			if lineage > 0 {
				other := intake.CheckpointKey()
				other.FilterKey = "srv2-0000000000000000"
				require.NoError(t, ledger.NotePollServed(ctx, other, lineage))
			}

			done := runInBackground(ctx, t, intake)
			subscribedConn(t, transport)

			answered := 1
			require.Eventually(t, func() bool {
				if conns := transport.Conns(); len(conns) > answered {
					answerSubscription(conns[len(conns)-1])
					answered = len(conns)
				}
				_, ok, err := ledger.Get(ctx, 17099838600)
				return err == nil && ok
			}, 3*time.Second, 10*time.Millisecond, "the event committed after the refused checkpoint is lost")

			cancel()
			awaitReturn(t, done, "Run should return on shutdown")
		})
	}
}

// The ledger's lifecycle is closed; a state outside it, or a reason on a state
// that takes none, would be a durable row no recovery scan can find.
func TestSetStateRefusesAnythingOutsideTheLifecycle(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	_, err := ledger.RecordSeen(ctx, testEvent(1), LanePoll)
	require.NoError(t, err)

	assert.Error(t, ledger.SetState(ctx, 1, RecordState("sen"), ""))
	assert.Error(t, ledger.SetState(ctx, 1, StateAdmitted, "because"))
	assert.Error(t, ledger.SetState(ctx, 1, StateBlocked, ""), "a blocked record says why")
	assert.NoError(t, ledger.SetState(ctx, 1, StateBlocked, "read_failed"))
	assert.NoError(t, ledger.SetState(ctx, 1, StateAdmitted, ""))
}

// noncePolls answers every request with the epoch's 410, and each answer's
// resume differs only by a nonce — a server that signs its resume URLs.
type noncePolls struct {
	mu    sync.Mutex
	calls int
}

func (n *noncePolls) Poll(context.Context, eventfeed.Cursor, eventfeed.Filters) (eventfeed.PollPage, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls++
	if n.calls > 500 {
		return eventfeed.PollPage{}, &eventfeed.PollError{Kind: eventfeed.PollUnrecoverable}
	}
	return eventfeed.PollPage{}, &eventfeed.PollError{Kind: eventfeed.PollGone, EpochAfterID: 150,
		ResumeURL: "https://3.basecampapi.com/2914079/events.json?since=150&nonce=" + strconv.Itoa(n.calls)}
}

// C3: a pass is bounded whatever URLs the server chooses.
func TestARepairPassIsBoundedWhenEveryResumeURLDiffers(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	loss, err := ledger.RecordLoss(ctx, []int64{100, 200}, clock.at, time.Minute, eventfeed.Filters{})
	require.NoError(t, err)

	polls := &noncePolls{}
	walker, _ := newTestWalker(t, ledger, polls, clock)
	require.NoError(t, walker.reconcile(ctx, loss))
	assert.LessOrEqual(t, polls.calls, 10, "two passes, each a handful of polls, not a hot loop")
}

// A ledger read that failed because the connection was canceled — by a
// reconnect — is not a reason to end the run.
func TestACanceledReadDuringAReconnectIsNotAnAbort(t *testing.T) {
	intake, _, _ := newTestIntake(t, nil, nil)
	intake.positions = canceledPositions{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	intake.onPositionRejected(ctx)
	assert.NoError(t, intake.abortErr)
}

type canceledPositions struct{}

func (canceledPositions) LastPollServedID(ctx context.Context, _ eventfeed.CheckpointKey) (int64, error) {
	return 0, ctx.Err()
}

func (canceledPositions) LineagePollServedID(ctx context.Context, _ eventfeed.CheckpointKey) (int64, error) {
	return 0, ctx.Err()
}

// Queue callbacks may observe the queue. None of them runs while the queue
// holds a lock its own operations need.
func TestQueueCallbacksMayTouchTheQueue(t *testing.T) {
	queue, err := NewQueue(1, 4)
	require.NoError(t, err)
	var depths []int
	// No timeout: a callback that takes from the queue must actually get the
	// id the offer is delivering, not time out and call that success.
	queue.OnWarn = func(int) {
		id, err := queue.Take(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, int64(1), id)
		depths = append(depths, queue.Depth())
	}

	done := make(chan error, 1)
	go func() { done <- queue.Offer(context.Background(), 1) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("a callback that takes from the queue deadlocked the offer")
	}
}
