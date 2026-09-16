package connector

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// Round 2 of review. Each of these failed on 893c0d6.

func TestARefusedPositionPrefersThisFilterSetsOwnPollServedID(t *testing.T) {
	ledger := newTestLedger(t)
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	key := intake.CheckpointKey()
	require.NoError(t, ledger.Save(ctx, key, "position-the-server-will-refuse"))
	require.NoError(t, ledger.NotePollServed(ctx, key, 1000))
	other := key
	other.FilterKey = "srv2-0000000000000000"
	require.NoError(t, ledger.NotePollServed(ctx, other, 2000))

	minter.ScriptTicket(ticket())
	minter.ScriptTicket(ticket())
	polls.ScriptError(&eventfeed.PollError{Kind: eventfeed.PollPositionInvalid})
	polls.ScriptPage(eventfeed.PollPage{Position: "p2"})
	polls.ScriptPage(eventfeed.PollPage{Position: "p3"})

	done := runInBackground(ctx, t, intake)
	subscribedConn(t, transport)

	answered := 1
	var reentry eventfeed.Cursor
	require.Eventually(t, func() bool {
		if conns := transport.Conns(); len(conns) > answered {
			answerSubscription(conns[len(conns)-1])
			answered = len(conns)
		}
		calls := polls.Calls()
		if len(calls) < 2 {
			return false
		}
		reentry = calls[1].Cursor
		return true
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "1000", reentry.Since,
		"another filter set's id is past events this one never served; re-entering there skips them")

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}

// A reconnect that lands while intake is waiting for queue room must not
// strand the event it was handing over: it is already seen, so no re-served
// page would ever queue it again.
func TestAReconnectDuringABackloggedHandOffStrandsNothing(t *testing.T) {
	ledger := newTestLedger(t)
	queue, err := NewQueue(1, 1)
	require.NoError(t, err)
	membership := &flakyMembership{buckets: []int64{48699913}}
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{
		Membership:         membership,
		MembershipInterval: 50 * time.Millisecond,
	})
	intake.queue = queue
	intake.opts.Queue = queue
	for range 5 {
		minter.ScriptTicket(ticket())
	}
	page := eventfeed.PollPage{Events: []eventfeed.Event{testEvent(500), testEvent(501)}, Position: "p1"}
	for range 5 {
		polls.ScriptPage(page)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(ctx, t, intake)
	subscribedConn(t, transport)

	require.Eventually(t, func() bool { return queue.Paused() }, 5*time.Second, 5*time.Millisecond)
	membership.mu.Lock()
	membership.buckets = []int64{48699913, 1}
	membership.mu.Unlock()

	// Nothing is taken until the reconnect has been asked for, so the hand-off
	// is still waiting when it lands.
	require.Eventually(t, func() bool {
		return len(transport.Dials()) >= 2 || len(intake.reconnect) == 1
	}, 5*time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond)

	answered := 1
	var got []int64
	require.Eventually(t, func() bool {
		if conns := transport.Conns(); len(conns) > answered {
			answerSubscription(conns[len(conns)-1])
			answered = len(conns)
		}
		ctxTake, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer stop()
		if id, err := queue.Take(ctxTake); err == nil {
			got = append(got, id)
		}
		return len(got) >= 2
	}, 5*time.Second, 10*time.Millisecond, "every seen event must reach the queue")
	assert.Equal(t, []int64{500, 501}, got)

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}

// A project the membership list never names — archived, or past the lister's
// page — must cost one reconnect, not one per event.
func TestAnUnlistedProjectCostsOneReconnectNotOnePerEvent(t *testing.T) {
	ledger := newTestLedger(t)
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{
		Membership:         &flakyMembership{buckets: []int64{1}},
		MembershipInterval: time.Hour,
	})
	for range 20 {
		minter.ScriptTicket(ticket())
	}
	var events []eventfeed.Event
	for id := int64(600); id < 605; id++ {
		e := testEvent(id)
		e.BucketID = 777
		events = append(events, e)
	}
	for range 20 {
		polls.ScriptPage(eventfeed.PollPage{Events: events, Position: "p"})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(ctx, t, intake)
	subscribedConn(t, transport)

	answered := 1
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if conns := transport.Conns(); len(conns) > answered {
			answerSubscription(conns[len(conns)-1])
			answered = len(conns)
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.LessOrEqual(t, len(transport.Dials()), 2, "five events from one unlisted project are one reconnect")

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}

func TestSinceAppliesToTheFirstConnectionOnly(t *testing.T) {
	ledger := newTestLedger(t)
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{
		SinceEventID:       17099838000,
		Membership:         &flakyMembership{buckets: []int64{1}},
		MembershipInterval: time.Hour,
	})
	minter.ScriptTicket(ticket())
	minter.ScriptTicket(ticket())
	e := testEvent(17099838001)
	e.BucketID = 777 // unlisted: forces one reconnect
	polls.ScriptPage(eventfeed.PollPage{Events: []eventfeed.Event{e}, Position: "after-since"})
	polls.ScriptPage(eventfeed.PollPage{Position: "second"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(ctx, t, intake)
	subscribedConn(t, transport)

	answered := 1
	require.Eventually(t, func() bool {
		if conns := transport.Conns(); len(conns) > answered {
			answerSubscription(conns[len(conns)-1])
			answered = len(conns)
		}
		return polls.CallCount() >= 2
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "17099838000", polls.Calls()[0].Cursor.Since)
	assert.NotEqual(t, "17099838000", polls.Calls()[1].Cursor.Since,
		"a reconnect resumes from what the run stored, not from --since again")

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}

func TestMembershipWatcherStopsWhenStopped(t *testing.T) {
	var reads atomic.Int32
	intake, _, _ := newTestIntake(t, nil, nil)
	intake.opts.Membership = countingMembership{&reads}
	intake.opts.MembershipInterval = time.Millisecond

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	await := intake.watchMembership(ctx)
	require.Eventually(t, func() bool { return reads.Load() > 0 }, time.Second, time.Millisecond)
	stop()
	await()
	after := reads.Load()
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, after, reads.Load(), "a stopped watcher reads nothing more")
}

type countingMembership struct{ n *atomic.Int32 }

func (c countingMembership) Buckets(context.Context) ([]int64, error) {
	c.n.Add(1)
	return nil, nil
}

// The seam requires connector cancellation to pass through unchanged, or a
// shutdown enters transport-retry handling.
func TestCallerCancellationPassesThroughTheAdapterUnchanged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	adapter := newTestAdapter(t, &fakeFeedClient{err: context.Canceled})
	_, err := adapter.Poll(ctx, eventfeed.Cursor{}, eventfeed.Filters{})
	var pollErr *eventfeed.PollError
	assert.False(t, errors.As(err, &pollErr), "a canceled poll is not a transport failure")
	assert.ErrorIs(t, err, context.Canceled)

	_, err = adapter.MintStreamTicket(ctx)
	var mintErr *eventfeed.MintError
	assert.False(t, errors.As(err, &mintErr), "a canceled mint is not a transport failure")
	assert.ErrorIs(t, err, context.Canceled)

	// A client-owned timeout with the caller's context still live stays
	// transient.
	adapter = newTestAdapter(t, &fakeFeedClient{err: context.DeadlineExceeded})
	_, err = adapter.Poll(context.Background(), eventfeed.Cursor{}, eventfeed.Filters{})
	require.ErrorAs(t, err, &pollErr)
	assert.Equal(t, eventfeed.PollTransient, pollErr.Kind)
}

func TestQueuePauseTracksEveryBlockedOffer(t *testing.T) {
	queue, err := NewQueue(1, 1)
	require.NoError(t, err)
	var pauses, resumes atomic.Int32
	queue.OnPause = func(int) { pauses.Add(1) }
	queue.OnResume = func(int) { resumes.Add(1) }
	ctx := context.Background()
	require.NoError(t, queue.Offer(ctx, 1))

	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- queue.Offer(ctx, 2) }()
	go func() { second <- queue.Offer(ctx, 3) }()
	require.Eventually(t, func() bool { return pauses.Load() >= 1 && len(queue.ids) == 1 }, time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond)

	_, err = queue.Take(ctx)
	require.NoError(t, err)
	select {
	case <-first:
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("one offer should have resumed")
	}
	assert.True(t, queue.Paused(), "the other offer is still waiting, so the feed is still paused")
	assert.Zero(t, resumes.Load(), "resume fires when the last waiter stops waiting")

	_, err = queue.Take(ctx)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !queue.Paused() }, time.Second, time.Millisecond)
	assert.Equal(t, int32(1), pauses.Load())
	assert.Equal(t, int32(1), resumes.Load())
}

// The ledger holds feed positions — signed tokens — and account event
// metadata. It is private to the user or it is not opened.
func TestLedgerIsCreatedPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	dir := filepath.Join(t.TempDir(), "state")
	ledger, err := OpenLedger(filepath.Join(dir, "connector.db"))
	require.NoError(t, err)
	defer ledger.Close()
	require.NoError(t, ledger.Save(context.Background(), testKey(), "p"))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	for _, name := range []string{"connector.db", "connector.db-wal", "connector.db-shm"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), name)
	}
}

func TestLedgerRefusesALooseDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	dir := filepath.Join(t.TempDir(), "shared")
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0o755))
	_, err := OpenLedger(filepath.Join(dir, "connector.db"))
	assert.Error(t, err, "a directory other users can read exposes the ledger's sidecars")
}

func TestLedgerRefusesALooseFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	dir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.Chmod(dir, 0o700))
	path := filepath.Join(dir, "connector.db")
	require.NoError(t, os.WriteFile(path, nil, 0o644))
	require.NoError(t, os.Chmod(path, 0o644))
	_, err := OpenLedger(path)
	assert.Error(t, err)
}
