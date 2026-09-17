package connector

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// The filter set is the checkpoint's identity. A caller that keeps its slice
// and mutates it would change the filters intake subscribes, records and
// walks with, while the checkpoint key stays frozen on what it was built from.
func TestTheFilterSetIsCopiedAtConstruction(t *testing.T) {
	ledger := newTestLedger(t)
	queue, err := NewQueue(1, 2)
	require.NoError(t, err)
	types := []string{"comment.created"}
	buckets := []int64{48699913}
	intake, err := New(Options{
		Origin:            "https://3.basecampapi.com",
		AccountID:         "2914079",
		ConsumerNamespace: "connector-test",
		Filters:           eventfeed.Filters{Types: types, Buckets: buckets},
		Ledger:            ledger,
		Queue:             queue,
		Minter:            stubMinter{},
		Polls:             &scriptedPolls{},
	})
	require.NoError(t, err)

	types[0] = "card.created"
	buckets[0] = 999

	assert.Equal(t, []string{"comment.created"}, intake.opts.Filters.Types)
	assert.Equal(t, []int64{48699913}, intake.opts.Filters.Buckets)
	assert.Equal(t, eventfeed.Filters{Types: []string{"comment.created"}, Buckets: []int64{48699913}}.FilterKey(),
		intake.CheckpointKey().FilterKey)
}

// A peer's disconnect reason is text the other end chose. It reaches an
// operator's terminal through the log.
func TestALoggedDisconnectReasonCarriesNoTerminalControls(t *testing.T) {
	var logs bytes.Buffer
	intake, _, _ := newTestIntake(t, nil, nil)
	intake.log = slog.New(slog.NewJSONHandler(&logs, nil))

	observer := intake.observer(context.Background())
	require.NotNil(t, observer.Disconnected)
	observer.Disconnected("stale"+csi+"31m"+esc+"[2J", nil)

	assert.NotContains(t, logs.String(), csi)
	assert.NotContains(t, logs.String(), esc)
	assert.Contains(t, logs.String(), "stale")
}

// A re-entry that saved a checkpoint has made progress, even if the page it
// saved carried no event. A later refusal of that saved position is an
// ordinary refusal, not the re-entry being refused.
func TestAReentryThatCheckpointedAnEmptyPageIsNotStillPending(t *testing.T) {
	ledger := newTestLedger(t)
	// A short repair cadence so the position saved from the empty page is
	// re-polled, and refused, while this connection is streaming.
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{RepairInterval: 50 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, ledger.Save(ctx, intake.CheckpointKey(), "refused"))
	require.NoError(t, ledger.NotePollServed(ctx, intake.CheckpointKey(), 1000))

	for range 6 {
		minter.ScriptTicket(ticket())
	}
	// The stored position is refused; the re-entry's first page is empty but
	// checkpoints; that saved position is then refused in turn.
	polls.ScriptError(&eventfeed.PollError{Kind: eventfeed.PollPositionInvalid})
	polls.ScriptPage(eventfeed.PollPage{Position: "saved-from-an-empty-page"})
	polls.ScriptError(&eventfeed.PollError{Kind: eventfeed.PollPositionInvalid})
	polls.ScriptPage(eventfeed.PollPage{Events: []eventfeed.Event{testEvent(17099838600)}, Position: "p3"})
	for range 4 {
		polls.ScriptPage(eventfeed.PollPage{Position: "p4"})
	}

	done := runInBackground(ctx, t, intake)
	answered := 0
	require.Eventually(t, func() bool {
		if conns := transport.Conns(); len(conns) > answered {
			answerSubscription(conns[len(conns)-1])
			answered = len(conns)
		}
		_, ok, err := ledger.Get(ctx, 17099838600)
		return err == nil && ok
	}, 5*time.Second, 10*time.Millisecond, "the run aborted instead of re-entering after a checkpointed empty page")

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}

// countingPolls reports the highest number of polls in flight at once.
type countingPolls struct {
	inFlight atomic.Int32
	peak     atomic.Int32
	calls    atomic.Int32
}

func (c *countingPolls) Poll(context.Context, eventfeed.Cursor, eventfeed.Filters) (eventfeed.PollPage, error) {
	n := c.inFlight.Add(1)
	for {
		peak := c.peak.Load()
		if n <= peak || c.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	defer c.inFlight.Add(-1)
	c.calls.Add(1)
	time.Sleep(5 * time.Millisecond)
	return eventfeed.PollPage{Position: "p"}, nil
}

// C3: many open losses are repaired within a bound. One goroutine and one poll
// source per loss would turn an overloaded feed into an API storm.
func TestRepairsRunWithinABound(t *testing.T) {
	polls := &countingPolls{}
	intake, ledger, _ := newTestIntake(t, polls, nil)
	intake.opts.RepairInterval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := range 25 {
		// A window already closed, so each loss is one final pass and done.
		_, err := ledger.RecordLoss(ctx, []int64{int64(1000 + i)}, intake.now().Add(-time.Hour), time.Minute, eventfeed.Filters{})
		require.NoError(t, err)
	}
	require.NoError(t, intake.resumeReconciliation(ctx))

	require.Eventually(t, func() bool { return polls.calls.Load() >= 10 }, 5*time.Second, 5*time.Millisecond)
	assert.LessOrEqual(t, int(polls.peak.Load()), maxConcurrentRepairs, "repairs are bounded")

	cancel()
	intake.repairs.Wait()
}

// The ledger is a file. SQLite's in-memory URI would accept every write and
// lose it on close, which is the one thing the ledger exists not to do.
func TestAnInMemoryLedgerIsRefused(t *testing.T) {
	for _, path := range []string{":memory:", "file::memory:"} {
		_, err := OpenLedger(path)
		require.Error(t, err, path)
		assert.Contains(t, strings.ToLower(err.Error()), "memory")
	}
	// A real path containing the word is still a file.
	ledger, err := OpenLedger(filepath.Join(t.TempDir(), "state", "memory.db"))
	require.NoError(t, err)
	require.NoError(t, ledger.Close())
}

// Each Run gets its own repair pool. A pool left over from a finished run has
// no workers, so a second Run would queue losses nobody walks — silently.
func TestASecondRunRepairsToo(t *testing.T) {
	polls := &countingPolls{}
	intake, ledger, _ := newTestIntake(t, polls, nil)
	intake.opts.RepairInterval = time.Hour
	intake.opts.Minter = stubMinter{}

	record := func() {
		_, err := ledger.RecordLoss(context.Background(), []int64{17099838509}, intake.now().Add(-time.Hour), time.Minute, eventfeed.Filters{})
		require.NoError(t, err)
	}

	first, cancelFirst := context.WithCancel(context.Background())
	record()
	require.NoError(t, intake.resumeReconciliation(first))
	require.Eventually(t, func() bool { return polls.calls.Load() >= 1 }, 5*time.Second, 5*time.Millisecond)
	cancelFirst()
	intake.repairs.Wait()
	intake.releaseRepairWorkers()
	after := polls.calls.Load()

	second, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	record()
	require.NoError(t, intake.resumeReconciliation(second))
	require.Eventually(t, func() bool { return polls.calls.Load() > after }, 5*time.Second, 5*time.Millisecond,
		"the second run's repairs are queued to a pool with no workers")
}

// Run is reusable, so its per-run state starts clean. A checkpoint saved by an
// earlier run would otherwise clear this run's --since before it has saved
// anything of its own, and an earlier abort would end it before it began.
func TestASecondRunStartsWithCleanRunState(t *testing.T) {
	ledger := newTestLedger(t)
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{SinceEventID: 17099838000})
	intake.checkpointed = true
	intake.abortErr = errors.New("an earlier run's fatal")
	intake.hasReentry = true
	intake.replaying = true
	intake.enteredByReentry = true

	minter.ScriptTicket(ticket())
	polls.ScriptPage(eventfeed.PollPage{Position: "p1"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(ctx, t, intake)
	subscribedConn(t, transport)

	require.Eventually(t, func() bool { return polls.CallCount() > 0 }, 5*time.Second, 10*time.Millisecond,
		"an earlier run's abort must not end this one")
	assert.Equal(t, "17099838000", polls.Calls()[0].Cursor.Since)

	// checkpointed is legitimately true again by now, this run having saved
	// its own page; what must not come back is the rest.
	intake.mu.Lock()
	clean := intake.abortErr == nil && !intake.hasReentry && !intake.replaying
	intake.mu.Unlock()
	assert.True(t, clean, "per-run state starts clean")

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}
