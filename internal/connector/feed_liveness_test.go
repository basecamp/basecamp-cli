package connector

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed/feedtest"
)

// walk feeds one repair walk's findings into l and returns its verdict.
func walk(l *liveness, polled, live int) bool {
	l.walkStarted()
	for range polled {
		l.note(LanePoll)
	}
	for range live {
		l.note(LaneLive)
	}
	return l.walkDone()
}

// connectedLiveness is a liveness past its connection's catch-up walk.
func connectedLiveness() *liveness {
	l := &liveness{}
	l.connected()
	walk(l, 50, 0)
	return l
}

func TestLivenessIgnoresTheCatchUpWalk(t *testing.T) {
	l := &liveness{}
	l.connected()
	// What happened while no socket was open arrives by poll, and says
	// nothing about the socket.
	assert.False(t, walk(l, 100, 0))
	assert.Empty(t, l.window)
}

func TestLivenessReplacesASocketThePollKeepsOvertaking(t *testing.T) {
	l := connectedLiveness()
	assert.False(t, walk(l, 1, 0))
	assert.False(t, walk(l, 1, 0))
	assert.True(t, walk(l, 1, 0), "three walks each finding what the socket never sent")
	assert.Equal(t, 1, l.strikes)
}

func TestLivenessReplacesAPartlyDeafSocket(t *testing.T) {
	// The failover lost about three quarters of Cable's subscriptions: some
	// events still came live, most came by poll.
	l := connectedLiveness()
	assert.False(t, walk(l, 4, 1))
	assert.False(t, walk(l, 3, 1))
	assert.True(t, walk(l, 4, 1))
}

func TestLivenessLeavesAHealthySocketAlone(t *testing.T) {
	cases := map[string][][2]int{
		// Poll and live overlap: dedupe means the poll finds nothing new.
		"steady":   {{0, 5}, {0, 3}, {0, 7}, {0, 2}},
		"quiet":    {{0, 0}, {0, 0}, {0, 0}, {0, 0}},
		"one drop": {{1, 5}, {0, 4}, {0, 6}},
		// Two dropped push frames on a quiet account are below the minimum.
		"two drops": {{1, 0}, {1, 0}, {0, 0}},
		// One burst between two polls is one walk, not a pattern.
		"one burst":               {{0, 0}, {6, 0}, {0, 0}},
		"few events, mostly live": {{1, 2}, {1, 2}, {1, 2}},
	}
	for name, walks := range cases {
		t.Run(name, func(t *testing.T) {
			l := connectedLiveness()
			for i, w := range walks {
				assert.False(t, walk(l, w[0], w[1]), "walk %d", i)
			}
		})
	}
}

func TestLivenessIgnoresLossRepairs(t *testing.T) {
	l := connectedLiveness()
	for range 3 {
		l.walkStarted()
		for range 5 {
			l.note(LaneRepair)
		}
		l.note(LanePoll)
		// Counted as neither lane: as live they would mask a deaf socket, as
		// polled they would invent one.
		assert.Equal(t, 0, l.live)
		assert.Equal(t, 1, l.polled)
		if len(l.window) < 2 {
			assert.False(t, l.walkDone())
		}
	}
	assert.Equal(t, []walkSample{{polled: 1}, {polled: 1}}, l.window)
	// The poll evidence beside them still counts.
	assert.True(t, l.walkDone())
}

// A stalled hand-off or a buffer overflow lets the poll reach events the
// socket did send: that walk is no evidence, and the span starts again.
func TestLivenessDiscountsAWalkTheSocketCouldNotWin(t *testing.T) {
	l := connectedLiveness()
	assert.False(t, walk(l, 3, 1))
	l.taint()
	assert.False(t, walk(l, 3, 1), "the tainted walk")
	assert.False(t, walk(l, 3, 1), "the walk after it, which finds what the first could not see")
	assert.Empty(t, l.window)
	assert.False(t, walk(l, 3, 1))
	assert.False(t, walk(l, 0, 0))
	// The same three samples untainted are a verdict, so the taint is what
	// held the ones above back.
	assert.True(t, walk(l, 3, 1))
}

func TestLivenessDiscountsAWalkIntakeWasBusyFor(t *testing.T) {
	l := connectedLiveness()
	assert.False(t, walk(l, 1, 0))
	// Many quick hand-offs add up: none is long, the total is.
	for range 20 {
		l.busy(livenessBusy / 10)
	}
	assert.False(t, walk(l, 1, 0))
	assert.False(t, walk(l, 1, 0))
	assert.Empty(t, l.window, "a busy walk and the one after it are dropped")

	// Busy time under the bound is ordinary, and is not carried over.
	l = connectedLiveness()
	for range 3 {
		l.busy(livenessBusy * 9 / 10)
		if len(l.window) < 2 {
			assert.False(t, walk(l, 1, 0))
		}
	}
	assert.True(t, walk(l, 1, 0))
}

func TestLivenessBacksOffWhileResubscribingDoesNotHelp(t *testing.T) {
	l := connectedLiveness()
	for range 3 {
		walk(l, 1, 0)
	}
	require.Equal(t, 1, l.strikes)

	// Cable down for everyone: the next connection is just as deaf, and the
	// next verdict waits twice as long.
	l.connected()
	walk(l, 20, 0)
	for i := range 5 {
		assert.False(t, walk(l, 1, 0), "walk %d of 6", i+1)
	}
	assert.True(t, walk(l, 1, 0))
	assert.Equal(t, 2, l.strikes)
	assert.Equal(t, 12, l.span())

	// The doubling stops at the cap.
	l.strikes = 40
	assert.Equal(t, livenessWalks<<livenessMaxBackoff, l.span())
}

func TestLivenessForgivesOnceLiveDeliveryReturns(t *testing.T) {
	l := connectedLiveness()
	for range 3 {
		walk(l, 1, 0)
	}
	require.Equal(t, 1, l.strikes)
	l.connected()
	walk(l, 20, 0)
	for range 6 {
		walk(l, 0, 4)
	}
	assert.Zero(t, l.strikes)
	assert.Equal(t, livenessWalks, l.span())
}

// handPolls serves one walk per call. Each call after the first, the
// catch-up, takes the next id from serve; it can wait for the walk before it
// to be done, since a call starts only once the previous walk has drained.
type handPolls struct {
	mu    sync.Mutex
	calls int
	serve func(call int) []int64
}

func newHandPolls(serve func(call int) []int64) *handPolls {
	return &handPolls{serve: serve}
}

func (p *handPolls) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *handPolls) Poll(ctx context.Context, _ eventfeed.Cursor, _ eventfeed.Filters) (eventfeed.PollPage, error) {
	if err := ctx.Err(); err != nil {
		return eventfeed.PollPage{}, err
	}
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	page := eventfeed.PollPage{Position: fmt.Sprintf("p%d", call)}
	if call > 1 {
		for _, id := range p.serve(call) {
			page.Events = append(page.Events, testEvent(id))
		}
	}
	return page, nil
}

// awaitCall waits for poll call n to start, which means every walk before it
// has been sampled.
func (p *handPolls) awaitCall(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return p.count() >= n }, 5*time.Second, time.Millisecond, "poll call %d never started", n)
}

type feedRun struct {
	ledger    *Ledger
	transport *feedtest.Transport
	conn      *feedtest.Conn
	ident     string
	logs      *safeBuffer
	pings     atomic.Int64
}

// startFeed runs intake over a socket that answers keep-alives every 5ms,
// with the repair poll at 50ms rather than 60s.
func startFeed(t *testing.T, polls eventfeed.PollSource) *feedRun {
	t.Helper()
	queue, err := NewQueue(DefaultBacklogWarn, DefaultBacklogPause)
	require.NoError(t, err)
	return startFeedOn(t, polls, queue)
}

func startFeedOn(t *testing.T, polls eventfeed.PollSource, queue *Queue) *feedRun {
	t.Helper()
	ledger := newTestLedger(t)
	transport := feedtest.NewTransport()
	minter := feedtest.NewMinter()
	for i := range 8 {
		minter.ScriptTicket(eventfeed.StreamTicket{Ticket: "t", ExpiresIn: 3600, URL: fmt.Sprintf("wss://cable.basecamp.com/cable?ticket=t%d", i)})
	}
	logs := &safeBuffer{}
	var pointers safeBuffer
	intake, err := New(Options{
		Origin:            "https://3.basecampapi.com",
		AccountID:         "2914079",
		ConsumerNamespace: "connector-test",
		Ledger:            ledger,
		Queue:             queue,
		Minter:            minter,
		Polls:             polls,
		Transport:         transport,
		Pointers:          &pointers,
		Logger:            slog.New(slog.NewTextHandler(logs, nil)),
		RepairInterval:    50 * time.Millisecond,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- intake.Run(ctx) }()
	conn, ident := subscribedConn(t, transport)
	run := &feedRun{ledger: ledger, transport: transport, conn: conn, ident: ident, logs: logs}
	go func() {
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for n := 0; ; n++ {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				conn.Serve(fmt.Appendf(nil, `{"type":"ping","message":%d}`, 1700000000+n))
				run.pings.Add(1)
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	return run
}

func (r *feedRun) lane(t *testing.T, id int64) (Lane, bool) {
	t.Helper()
	rec, ok, err := r.ledger.Get(context.Background(), id)
	require.NoError(t, err)
	return rec.Lane, ok
}

// The defect on card 10435111118: a socket that keeps answering keep-alives
// but delivers nothing, while every repair walk finds an event it never sent.
func TestIntakeResubscribesASocketThatOnlyAnswersKeepalives(t *testing.T) {
	const first = int64(17099838501)
	var (
		mu sync.Mutex
		fr *feedRun
		// dialsAtLastWalk is the dial count as the third repair walk polls:
		// the two before it have been sampled, and it has not.
		dialsAtLastWalk = -1
		pingsAtLastWalk int64
	)
	polls := newHandPolls(func(call int) []int64 {
		if call == livenessWalks+1 {
			mu.Lock()
			if fr != nil {
				dialsAtLastWalk = len(fr.transport.Dials())
				pingsAtLastWalk = fr.pings.Load()
			}
			mu.Unlock()
		}
		return []int64{first + int64(call-2)}
	})
	run := startFeed(t, polls)
	mu.Lock()
	fr = run
	mu.Unlock()

	require.Eventually(t, func() bool { return len(run.transport.Dials()) >= 2 }, 5*time.Second, 5*time.Millisecond,
		"the deaf socket is replaced:\n%s", run.logs.String())
	mu.Lock()
	assert.Equal(t, 1, dialsAtLastWalk, "not replaced before the third repair walk is sampled")
	assert.GreaterOrEqual(t, pingsAtLastWalk, int64(5), "keep-alives were flowing on the socket the whole time")
	mu.Unlock()
	// Call 1 is the catch-up and calls 2 to 4 the repair walks. The new
	// socket is never welcomed, so nothing polls after the verdict.
	assert.Equal(t, livenessWalks+1, polls.count(), "replaced on the third repair walk, not later")
	for i := range int64(livenessWalks) {
		lane, ok := run.lane(t, first+i)
		require.True(t, ok)
		assert.Equal(t, LanePoll, lane, "event %d arrived only by poll", i)
	}
	// The SDK's staleness window is 7.5s, longer than this test runs; the
	// throwaway proof on the card held a socket like this for 10s and
	// staleness never fired.
	assert.Contains(t, run.logs.String(), "connected but not delivering")
}

// The control: the same pings and the same poll, but the socket delivers each
// event and the poll serves it again a walk later, as in steady state.
func TestIntakeKeepsASocketThatDelivers(t *testing.T) {
	const first = int64(17099838601)
	var (
		mu      sync.Mutex
		pending = make([]int64, 0, 4*livenessWalks)
	)
	polls := newHandPolls(func(int) []int64 {
		mu.Lock()
		defer mu.Unlock()
		ids := pending
		pending = nil
		return ids
	})
	run := startFeed(t, polls)

	for i := range int64(4 * livenessWalks) {
		id := first + i
		run.conn.Serve(pushFrame(t, run.ident, testEvent(id)))
		require.Eventually(t, func() bool { _, ok := run.lane(t, id); return ok }, 5*time.Second, 5*time.Millisecond)
		mu.Lock()
		pending = append(pending, id)
		called := polls.count()
		mu.Unlock()
		// The next call serves it; the one after starts only once that walk
		// has been sampled.
		polls.awaitCall(t, called+2)
	}
	assert.Len(t, run.transport.Dials(), 1, "a delivering socket is kept")
	assert.NotContains(t, run.logs.String(), "not delivering")
	lane, _ := run.lane(t, first)
	assert.Equal(t, LaneLive, lane)
}

func newLivenessIntake(t *testing.T, queue *Queue) *Intake {
	t.Helper()
	var pointers safeBuffer
	intake, err := New(Options{
		Origin:            "https://3.basecampapi.com",
		AccountID:         "2914079",
		ConsumerNamespace: "connector-test",
		Ledger:            newTestLedger(t),
		Queue:             queue,
		Minter:            feedtest.NewMinter(),
		Polls:             feedtest.NewPolls(),
		Pointers:          &pointers,
		Logger:            slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	return intake
}

func (in *Intake) livenessTainted() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.live.tainted
}

// A hand-off the backlog held up taints the walk; one that went straight in
// does not.
func TestIntakeTaintsTheWalkAfterAStalledHandOff(t *testing.T) {
	saved := livenessBusy
	livenessBusy = 200 * time.Millisecond
	t.Cleanup(func() { livenessBusy = saved })

	queue, err := NewQueue(1, 1)
	require.NoError(t, err)
	in := newLivenessIntake(t, queue)
	ctx := context.Background()
	sample := func() bool {
		in.mu.Lock()
		defer in.mu.Unlock()
		in.live.walks = 2
		in.live.walkDone()
		return in.live.tainted > 0
	}

	require.NoError(t, in.ingestFeed(ctx, testEvent(17099838701)))
	assert.False(t, sample(), "an offer with room is not a stall")

	// The queue is full now: the next offer waits for a take, which comes
	// only once the offer is seen waiting and the bound has passed.
	go func() {
		for queue.Depth() < 2 {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(2 * livenessBusy)
		_, _ = queue.Take(ctx)
	}()
	require.NoError(t, in.ingestFeed(ctx, testEvent(17099838702)))
	assert.True(t, sample(), "a walk in which intake was held past the bound")
}

func TestIntakeTaintsTheWalkAfterABufferOverflow(t *testing.T) {
	queue, err := NewQueue(DefaultBacklogWarn, DefaultBacklogPause)
	require.NoError(t, err)
	in := newLivenessIntake(t, queue)
	in.observer(context.Background()).BufferOverflow(3)
	assert.Equal(t, livenessTaintWalks, in.livenessTainted())
}

// The same deaf socket, but admission is slow: each hand-off waits for room
// longer than the bound. No walk is evidence, so the socket is kept however
// long the poll wins. It runs through the feed loop, so it also says the
// loop is what times the hand-offs.
func TestIntakeHoldsTheVerdictWhileIntakeIsBusy(t *testing.T) {
	saved := livenessBusy
	livenessBusy = 100 * time.Millisecond
	t.Cleanup(func() { livenessBusy = saved })

	queue, err := NewQueue(1, 1)
	require.NoError(t, err)
	// One id sits in the queue; each new one waits there until this takes
	// the one ahead of it, well past the bound.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			for queue.Depth() < 2 {
				if ctx.Err() != nil {
					return
				}
				time.Sleep(time.Millisecond)
			}
			time.Sleep(2 * livenessBusy)
			if _, err := queue.Take(ctx); err != nil {
				return
			}
		}
	}()

	const first = int64(17099838801)
	polls := newHandPolls(func(call int) []int64 { return []int64{first + int64(call-2)} })
	run := startFeedOn(t, polls, queue)
	polls.awaitCall(t, 4*livenessWalks)
	assert.Len(t, run.transport.Dials(), 1)
	assert.NotContains(t, run.logs.String(), "not delivering")
}

// A reconnect's catch-up runs within the poll's delay of what tainted the walk
// before it, so it spends none of the taint: the first repair walk after it,
// which can see those events, is still discounted.
func TestLivenessKeepsATaintThroughACatchUp(t *testing.T) {
	l := connectedLiveness()
	l.taint()
	assert.False(t, walk(l, 0, 0), "the walk the overflow happened in")
	l.connected()
	assert.False(t, walk(l, 0, 0), "the catch-up")
	assert.Equal(t, 1, l.tainted)
	assert.False(t, walk(l, 2, 0), "the first repair walk finds what the overflow dropped")
	assert.Empty(t, l.window)
	assert.Zero(t, l.tainted)
}

// slowWriter holds every write while it is set.
type slowWriter struct {
	delay atomic.Int64
}

func (w *slowWriter) Write(p []byte) (int, error) {
	time.Sleep(time.Duration(w.delay.Load()))
	return len(p), nil
}

// Time the feed's goroutine spends in an observer callback counts, not only
// time in ingest: here a log line held up before the poll freezes its head.
func TestIntakeTaintsTheWalkAfterASlowCallback(t *testing.T) {
	saved := livenessBusy
	livenessBusy = 100 * time.Millisecond
	t.Cleanup(func() { livenessBusy = saved })

	queue, err := NewQueue(DefaultBacklogWarn, DefaultBacklogPause)
	require.NoError(t, err)
	in := newLivenessIntake(t, queue)
	logs := &slowWriter{}
	in.log = slog.New(slog.NewTextHandler(logs, nil))
	observer := in.observer(context.Background())

	in.mu.Lock()
	in.live.walks = 1
	in.mu.Unlock()
	observer.CatchUpStarted(eventfeed.Cursor{})
	observer.CaughtUp()
	assert.Zero(t, in.livenessTainted(), "callbacks that return at once are not a stall")

	logs.delay.Store(int64(2 * livenessBusy))
	observer.CatchUpStarted(eventfeed.Cursor{})
	logs.delay.Store(0)
	observer.CaughtUp()
	assert.Equal(t, livenessTaintWalks-1, in.livenessTainted(), "the walk is discounted, and so is the next")
}
