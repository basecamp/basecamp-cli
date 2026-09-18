package connector

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A route in a Prepare backoff was visible in the connector's log and nowhere
// else, and nobody reads a connector's stdout at three in the morning. The
// wait goes into the ledger, which is what `connect status` and `connect
// doctor` read from another process.
func TestAWaitingRouteIsRecordedWhereAnOperatorCanReadIt(t *testing.T) {
	h := newWorktreeHarness(t)
	h.wt = h.worktrees(fakeGit(t, `case "$*" in *"worktree add"*) echo "fatal: no space left" >&2; exit 128;; esac`))
	clock := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	h.wt.now = func() time.Time { return clock }
	route := filepath.Join(h.repo, "app")
	ctx := context.Background()

	_, err := h.wt.Prepare(ctx, route, 90)
	require.Error(t, err)

	waits, err := h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	require.Len(t, waits, 1, "a failed Prepare leaves a wait a person can read")
	assert.Equal(t, route, waits[0].Route)
	assert.Equal(t, 1, waits[0].Failures)
	assert.Equal(t, clock, waits[0].FirstAt.UTC())
	assert.Equal(t, clock.Add(PrepareBackoff), waits[0].Until.UTC(), "and when it will be tried again")
	assert.Contains(t, waits[0].Reason, "no space left", "and what the last failure was")
	assert.True(t, waits[0].Waiting(clock))

	// A second failure updates the count and the next attempt, and keeps the
	// hour the route started failing: that is what tells a six-hour wait from
	// one that began a minute ago.
	clock = clock.Add(PrepareBackoff)
	_, err = h.wt.Prepare(ctx, route, 91)
	require.Error(t, err)
	waits, err = h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	require.Len(t, waits, 1)
	assert.Equal(t, 2, waits[0].Failures)
	assert.Equal(t, time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC), waits[0].FirstAt.UTC(), "the wait started when it started")
	assert.Equal(t, clock, waits[0].LastAt.UTC())

	// A worktree is the only proof the route works, and it is what clears the
	// record — not a count, not a timer.
	clock = clock.Add(PrepareBackoff * 2)
	h.wt.git = "git"
	_, err = h.wt.Prepare(ctx, route, 92)
	require.NoError(t, err)
	waits, err = h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	assert.Empty(t, waits, "a route that took a worktree is not waiting for one")
}

// The classification #753 established is proof-based: the connector refuses
// only what it can prove. "It has failed a lot" is not proof, so a route that
// has reached the cap over and over is still tried again, and is still a wait
// rather than a block.
func TestALongWaitIsNeverEscalatedIntoARefusal(t *testing.T) {
	h := newWorktreeHarness(t)
	h.wt = h.worktrees(fakeGit(t, `case "$*" in *"worktree add"*) exit 128;; esac`))
	clock := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	h.wt.now = func() time.Time { return clock }
	route := filepath.Join(h.repo, "app")
	ctx := context.Background()

	for range 40 {
		_, err := h.wt.Prepare(ctx, route, 95)
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrPrepareBackoff, "each attempt after the wait is a real attempt")
		clock = clock.Add(PrepareBackoffMax)
	}

	waits, err := h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	require.Len(t, waits, 1)
	assert.Equal(t, 40, waits[0].Failures, "the count is reported")
	assert.Equal(t, PrepareBackoffMax, waits[0].Until.Sub(waits[0].LastAt), "and the wait is still the capped wait")
	assert.Empty(t, h.wt.RoutesWaiting(), "the route is startable again, as it was at the first failure")

	// Nothing about the route was recorded as blocked or refused: the only
	// row is the wait, and a worktree still clears it.
	h.wt.git = "git"
	_, err = h.wt.Prepare(ctx, route, 96)
	require.NoError(t, err, "a route that has failed forty times is still tried, and still works when it works")
}

// A route proved unusable is refused, with a reason on the record and a reply
// on the recording that asked. It is not also a wait: two answers to one
// question, one of them saying the connector will try again, is worse than
// either alone.
func TestARouteProvedUnusableIsNotAlsoRecordedAsWaiting(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()
	outside := filepath.Join(filepath.Dir(h.repo), "no-repository-here")
	require.NoError(t, os.MkdirAll(outside, 0o700))

	_, err := h.wt.Prepare(ctx, outside, 100)
	require.ErrorIs(t, err, ErrRouteUnusable)
	waits, err := h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	assert.Empty(t, waits, "a refusal is the answer; nothing says it will be tried again")

	// And a route that was waiting, then proved unusable, stops saying it is
	// waiting: the refusal replaces the wait rather than sitting beside it.
	route := filepath.Join(h.repo, "app")
	broken := h.worktrees(fakeGit(t, `case "$*" in *"worktree add"*) exit 128;; esac`))
	_, err = broken.Prepare(ctx, route, 101)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRouteUnusable)
	waits, err = h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	require.Len(t, waits, 1)

	require.NoError(t, os.RemoveAll(filepath.Join(h.repo, ".git")))
	_, err = h.wt.Prepare(ctx, route, 102)
	require.ErrorIs(t, err, ErrRouteUnusable)
	waits, err = h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	assert.Empty(t, waits, "the route is refused now, not waiting")
}

// reportStranded's shape: a condition that persists keeps saying so, every
// ten minutes, rather than being mentioned once at the failure.
func TestTheDispatcherSaysWaitingRoutesOutLoudOnASchedule(t *testing.T) {
	var logs bytes.Buffer
	h := newDispatchHarness(t, newFakeDriver(), func(o *DispatcherOptions) {
		o.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	})
	ctx := context.Background()
	now := time.Now()
	require.NoError(t, h.ledger.RecordRouteWait(ctx, RouteWait{
		Route: "/work/broken", Failures: 7, Reason: "fatal: not a git repository",
		FirstAt: now.Add(-6 * time.Hour), LastAt: now.Add(-time.Minute), Until: now.Add(PrepareBackoffMax),
	}))

	h.d.reviewWaitingRoutes(ctx)
	assert.Equal(t, 1, strings.Count(logs.String(), `"route":"/work/broken"`))
	assert.Contains(t, logs.String(), `"failures":7`)
	assert.Contains(t, logs.String(), "fatal: not a git repository")

	h.d.reviewWaitingRoutes(ctx)
	assert.Equal(t, 1, strings.Count(logs.String(), `"route":"/work/broken"`), "not on every tick")

	h.d.waitingAt = time.Time{}
	h.d.reviewWaitingRoutes(ctx)
	assert.Equal(t, 2, strings.Count(logs.String(), `"route":"/work/broken"`), "and again ten minutes later")

	// A route whose wait has elapsed is one the next record will try, so it is
	// not reported as waiting — and a route nothing is routed to any more does
	// not warn for as long as the connector runs.
	logs.Reset()
	require.NoError(t, h.ledger.RecordRouteWait(ctx, RouteWait{
		Route: "/work/broken", Failures: 8, FirstAt: now.Add(-6 * time.Hour), LastAt: now.Add(-time.Hour), Until: now.Add(-time.Minute),
	}))
	h.d.waitingAt = time.Time{}
	h.d.reviewWaitingRoutes(ctx)
	assert.NotContains(t, logs.String(), "/work/broken")
}

// A connector that restarts begins its own tally at one. Writing that over a
// row at fourteen would report one failure under a timestamp six hours old:
// a smaller, more reassuring number than the truth, which is the failure mode
// worth avoiding — silence sends a person looking, a confident wrong number
// stops them.
func TestAFailureCountSurvivesARestart(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	first := time.Date(2026, 9, 18, 6, 0, 0, 0, time.UTC)
	require.NoError(t, l.RecordRouteWait(ctx, RouteWait{
		Route: "/work/app", Failures: 14, FirstAt: first, LastAt: first.Add(5 * time.Hour), Until: first.Add(6 * time.Hour),
	}))

	// The restarted connector's first failure on the same route: count 1, and
	// a FirstAt of its own.
	restarted := first.Add(6 * time.Hour)
	require.NoError(t, l.RecordRouteWait(ctx, RouteWait{
		Route: "/work/app", Failures: 1, FirstAt: restarted, LastAt: restarted, Until: restarted.Add(PrepareBackoff),
	}))

	waits, err := l.RouteWaits(ctx)
	require.NoError(t, err)
	require.Len(t, waits, 1)
	assert.Equal(t, 15, waits[0].Failures, "the count goes up, never back to the new process's tally")
	assert.Equal(t, first, waits[0].FirstAt.UTC(), "and it is still counted from when the route started failing")
}

// A wait belongs to a route connect.json names. Once it names that route no
// longer, nothing is waiting on it, and a row that outlives its condition is
// worse than no row.
func TestAWaitOnARouteNoLongerRoutedIsDropped(t *testing.T) {
	var logs bytes.Buffer
	h := newDispatchHarness(t, newFakeDriver(), func(o *DispatcherOptions) {
		o.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	})
	ctx := context.Background()
	now := time.Now()
	for _, route := range []string{testRoute, "/work/was-routed-here"} {
		require.NoError(t, h.ledger.RecordRouteWait(ctx, RouteWait{
			Route: route, Failures: 3, Reason: "fatal: cannot chdir",
			FirstAt: now.Add(-time.Hour), LastAt: now, Until: now.Add(PrepareBackoffMax),
		}))
	}

	h.d.reviewWaitingRoutes(ctx)
	waits, err := h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	require.Len(t, waits, 1, "only the route connect.json still names is still waiting")
	assert.Equal(t, testRoute, waits[0].Route)
	assert.NotContains(t, logs.String(), "/work/was-routed-here")
}

// Worktrees turned off: no route is waiting for a worktree that is never
// made, and Prepare in off mode never reaches the clear on its success path.
func TestTurningWorktreesOffDropsTheRecordedWaits(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()
	now := time.Now()
	require.NoError(t, h.ledger.RecordRouteWait(ctx, RouteWait{
		Route: filepath.Join(h.repo, "app"), Failures: 3, FirstAt: now.Add(-time.Hour), LastAt: now, Until: now.Add(PrepareBackoffMax),
	}))

	off, err := NewWorktrees(WorktreesOptions{Ledger: h.ledger, Root: h.root, Lookup: h.lookup, Off: true})
	require.NoError(t, err)
	require.NoError(t, off.Recover(ctx))

	waits, err := h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	assert.Empty(t, waits, "nothing waits for a worktree that is never made")
}

// A connector at its concurrency has the most to say about a broken route and
// the least attention to spare. The report must not sit behind the capacity
// return, where long-running tasks would silence it for as long as they run.
func TestWaitingRoutesAreReportedWhenEveryWorkerSlotIsTaken(t *testing.T) {
	var logs bytes.Buffer
	h := newDispatchHarness(t, newFakeDriver(), func(o *DispatcherOptions) {
		o.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	})
	ctx := context.Background()
	now := time.Now()
	require.NoError(t, h.ledger.RecordRouteWait(ctx, RouteWait{
		Route: testRoute, Failures: 4, Reason: "fatal: cannot chdir",
		FirstAt: now.Add(-time.Hour), LastAt: now, Until: now.Add(PrepareBackoffMax),
	}))

	h.d.mu.Lock()
	h.d.held = h.d.opts.Concurrency
	h.d.mu.Unlock()
	require.Equal(t, 0, h.d.free(), "every slot is taken")

	require.NoError(t, h.d.dispatchReady(ctx))
	assert.Contains(t, logs.String(), `"route":"`+testRoute+`"`)
}
