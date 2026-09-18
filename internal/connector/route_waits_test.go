package connector

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// reviewWaits runs the tick's review over the snapshot dispatchReady would
// take, so a test exercises the same one-snapshot derivation production does.
func (h *dispatchHarness) reviewWaits(ctx context.Context) {
	routes, known := h.d.opts.Routes()
	h.d.reviewWaitingRoutes(ctx, routes, known)
}

// waitingWorktrees is a Worktrees over the harness's own ledger, so the
// dispatcher's review runs against the pair that really holds the waits: the
// rows and the backoffs armed from them.
func waitingWorktrees(t *testing.T, l *Ledger) *Worktrees {
	t.Helper()
	root := filepath.Join(t.TempDir(), "worktrees")
	require.NoError(t, os.MkdirAll(filepath.Dir(root), 0o700))
	wt, err := NewWorktrees(WorktreesOptions{Ledger: l, Root: root})
	require.NoError(t, err)
	return wt
}

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
	// connect.json still routes it: a wait is only reported for a route the
	// file names, which TestAWaitOnARouteNoLongerRoutedIsDropped holds.
	h.mu.Lock()
	h.routes[700] = admission.Route{Path: "/work/broken"}
	h.mu.Unlock()
	require.NoError(t, h.ledger.RecordRouteWait(ctx, RouteWait{
		Route: "/work/broken", Failures: 7, Reason: "fatal: not a git repository",
		FirstAt: now.Add(-6 * time.Hour), LastAt: now.Add(-time.Minute), Until: now.Add(PrepareBackoffMax),
	}))

	h.reviewWaits(ctx)
	assert.Equal(t, 1, strings.Count(logs.String(), `"route":"/work/broken"`))
	assert.Contains(t, logs.String(), `"failures":7`)
	assert.Contains(t, logs.String(), "fatal: not a git repository")

	h.reviewWaits(ctx)
	assert.Equal(t, 1, strings.Count(logs.String(), `"route":"/work/broken"`), "not on every tick")

	h.d.waitingAt = time.Time{}
	h.reviewWaits(ctx)
	assert.Equal(t, 2, strings.Count(logs.String(), `"route":"/work/broken"`), "and again ten minutes later")

	// A route whose wait has elapsed is one the next record will try, so it is
	// not reported as waiting — and a route nothing is routed to any more does
	// not warn for as long as the connector runs.
	logs.Reset()
	require.NoError(t, h.ledger.RecordRouteWait(ctx, RouteWait{
		Route: "/work/broken", Failures: 8, FirstAt: now.Add(-6 * time.Hour), LastAt: now.Add(-time.Hour), Until: now.Add(-time.Minute),
	}))
	h.d.waitingAt = time.Time{}
	h.reviewWaits(ctx)
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
	var wt *Worktrees
	h := newDispatchHarness(t, newFakeDriver(), func(o *DispatcherOptions) {
		o.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
		wt = waitingWorktrees(t, o.Ledger)
		o.Workspaces = wt
	})
	ctx := context.Background()
	now := time.Now()
	for _, route := range []string{testRoute, "/work/was-routed-here"} {
		require.NoError(t, h.ledger.RecordRouteWait(ctx, RouteWait{
			Route: route, Failures: 3, Reason: "fatal: cannot chdir",
			FirstAt: now.Add(-time.Hour), LastAt: now, Until: now.Add(PrepareBackoffMax),
		}))
	}
	// And the backoffs those rows armed, as a running connector would hold
	// them: the prune has to take both or the dispatcher goes on waiting on a
	// route with nothing in status to show for it.
	require.NoError(t, wt.Recover(ctx))
	require.ElementsMatch(t, []string{testRoute, "/work/was-routed-here"}, wt.RoutesWaiting())

	h.reviewWaits(ctx)
	waits, err := h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	require.Len(t, waits, 1, "only the route connect.json still names is still waiting")
	assert.Equal(t, testRoute, waits[0].Route)
	assert.Equal(t, []string{testRoute}, wt.RoutesWaiting(),
		"and the backoff goes with the row, so no route is held with nothing to show for it")
	assert.NotContains(t, logs.String(), "/work/was-routed-here")
}

// An absence nobody managed to observe is not an absence. connect.json that
// could not be read this once yields an empty route set, and pruning against
// it would erase every recorded failure permanently — losing the record of a
// route that has been failing all morning, which is the thing this table
// exists to keep.
func TestAnUnreadableConfigErasesNoRecordedWait(t *testing.T) {
	var logs bytes.Buffer
	h := newDispatchHarness(t, newFakeDriver(), func(o *DispatcherOptions) {
		o.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
		o.Workspaces = waitingWorktrees(t, o.Ledger)
	})
	ctx := context.Background()
	now := time.Now()
	require.NoError(t, h.ledger.RecordRouteWait(ctx, RouteWait{
		Route: "/work/failing-all-morning", Failures: 14, Reason: "fatal: cannot chdir",
		FirstAt: now.Add(-6 * time.Hour), LastAt: now, Until: now.Add(PrepareBackoffMax),
	}))

	h.mu.Lock()
	h.routes = map[int64]admission.Route{}
	h.routesUnknown = true
	h.mu.Unlock()

	h.reviewWaits(ctx)
	waits, err := h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	require.Len(t, waits, 1, "a file that could not be read says nothing about which routes exist")
	assert.Equal(t, 14, waits[0].Failures)
	assert.NotContains(t, logs.String(), "/work/failing-all-morning",
		"and naming a route as waiting claims it is this connector's, which an unread file does not establish")

	// A file that reads and routes nothing is a different fact, and does
	// prune: the emptiness was observed.
	h.mu.Lock()
	h.routesUnknown = false
	h.mu.Unlock()
	h.d.waitingAt = time.Time{}
	h.reviewWaits(ctx)
	waits, err = h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	assert.Empty(t, waits, "an empty set that was read is an empty set")
}

// A connect.json that reads but names another agent or account is the same
// case: it is not this connector's route set, so it neither deletes a wait
// nor makes one safe to call this connector's in the log. Re-setting the
// profile for another identity under a running connector stops the old one
// reporting routes that are no longer its own.
func TestAConfigNamingAnotherIdentityNeitherPrunesNorReports(t *testing.T) {
	var logs bytes.Buffer
	h := newDispatchHarness(t, newFakeDriver(), func(o *DispatcherOptions) {
		o.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
		o.Workspaces = waitingWorktrees(t, o.Ledger)
	})
	ctx := context.Background()
	now := time.Now()
	require.NoError(t, h.ledger.RecordRouteWait(ctx, RouteWait{
		Route: "/work/was-ours", Failures: 5, Reason: "fatal: cannot chdir",
		FirstAt: now.Add(-time.Hour), LastAt: now, Until: now.Add(PrepareBackoffMax),
	}))

	// What connectRoutes does for a file naming another agent: read, empty,
	// and not this connector's.
	h.mu.Lock()
	h.routes = map[int64]admission.Route{}
	h.routesUnknown = true
	h.mu.Unlock()

	h.reviewWaits(ctx)
	waits, err := h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	require.Len(t, waits, 1, "another identity's file does not delete this ledger's record")
	assert.NotContains(t, logs.String(), "/work/was-ours", "nor does it let the route be called this connector's")
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

// The wait goes with the transition that makes the worktree live, in that
// transaction. Cleared afterwards, a crash or a failed delete between the two
// left a durable worktree and a standing wait, and status reporting a route
// as failing that had just succeeded.
func TestAWorktreeGoingLiveClearsTheWaitInTheSameTransaction(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()
	route := filepath.Join(h.repo, "app")
	now := time.Now()
	require.NoError(t, h.ledger.RecordRouteWait(ctx, RouteWait{
		Route: route, Failures: 3, Reason: "fatal: cannot chdir",
		FirstAt: now.Add(-time.Hour), LastAt: now, Until: now.Add(PrepareBackoffMax),
	}))

	id, err := h.ledger.BeginWorktree(ctx, Worktree{
		Path: filepath.Join(h.root, "repo", "120-live"), WorkDir: filepath.Join(h.root, "repo", "120-live"),
		Route: route, Repository: h.repo, Branch: BranchPrefix + "120-live", BaseCommit: "abc",
		OriginatingEventID: 120, State: WorktreeCreating,
	})
	require.NoError(t, err)

	// No Prepare, no clearWait call: the move alone has to do it.
	require.NoError(t, h.ledger.MoveWorktree(ctx, id, WorktreeLive, WorktreeCreating))

	waits, err := h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	assert.Empty(t, waits, "a live worktree and a standing wait on its route cannot both be written")
}

// Only that transition clears it. A worktree that ends any other way proves
// nothing about the route, so the record of its failures stands.
func TestOtherWorktreeTransitionsLeaveTheWaitAlone(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()
	route := filepath.Join(h.repo, "app")
	now := time.Now()
	require.NoError(t, h.ledger.RecordRouteWait(ctx, RouteWait{
		Route: route, Failures: 3, FirstAt: now.Add(-time.Hour), LastAt: now, Until: now.Add(PrepareBackoffMax),
	}))
	id, err := h.ledger.BeginWorktree(ctx, Worktree{
		Path: filepath.Join(h.root, "repo", "121-kept"), WorkDir: filepath.Join(h.root, "repo", "121-kept"),
		Route: route, Repository: h.repo, Branch: BranchPrefix + "121-kept", BaseCommit: "abc",
		OriginatingEventID: 121, State: WorktreeCreating,
	})
	require.NoError(t, err)

	require.NoError(t, h.ledger.RetainWorktree(ctx, id, RetainedUnverified, WorktreeCreating))
	waits, err := h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	assert.Len(t, waits, 1, "a worktree kept without ever going live proves nothing about the route")
}

// The row outlives the process and the map does not. A restart that did not
// take the backoffs back read its own ledger saying a route was in a backoff
// until half past, while RoutesWaiting said nothing was waiting and the first
// tick started a record straight into it.
func TestARestartTakesTheArmedBackoffsBack(t *testing.T) {
	h := newWorktreeHarness(t)
	h.wt = h.worktrees(fakeGit(t, `case "$*" in *"worktree add"*) exit 128;; esac`))
	clock := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	h.wt.now = func() time.Time { return clock }
	route := filepath.Join(h.repo, "app")
	ctx := context.Background()

	_, err := h.wt.Prepare(ctx, route, 130)
	require.Error(t, err)
	require.Equal(t, []string{route}, h.wt.RoutesWaiting())

	// A new process on the same ledger: its own map, empty.
	restarted := h.worktrees(fakeGit(t, `case "$*" in *"worktree add"*) exit 128;; esac`))
	restarted.now = func() time.Time { return clock }
	require.Empty(t, restarted.RoutesWaiting(), "the map does not outlive the process")

	require.NoError(t, restarted.Recover(ctx))
	assert.Equal(t, []string{route}, restarted.RoutesWaiting(),
		"the dispatcher and the status line agree about the same route after a restart")
	_, err = restarted.Prepare(ctx, route, 131)
	require.ErrorIs(t, err, ErrPrepareBackoff, "and the wait the row promised is the wait that is kept")

	// The count comes back with it, so the backoff goes on doubling from
	// where it was rather than starting at a minute again on every restart.
	clock = clock.Add(PrepareBackoffMax)
	_, err = restarted.Prepare(ctx, route, 132)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrPrepareBackoff)
	waits, err := h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	require.Len(t, waits, 1)
	assert.Equal(t, 2, waits[0].Failures)
	assert.Equal(t, PrepareBackoff*2, waits[0].Until.Sub(waits[0].LastAt), "doubled from the first, not restarted at it")
}

// An expired row carries two facts and only one of them expires. The deadline
// is not resumed — that route is startable, which is what the row already
// says — but the history is, because a route that has failed nine times is
// still that route: coming back without the count would start the doubling at
// a minute again, so a connector restarting in a loop would hammer a broken
// route exactly as hard as on the first failure.
func TestARestartKeepsAnExpiredWaitsHistoryButNotItsDeadline(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()
	route := filepath.Join(h.repo, "app")
	first := time.Date(2026, 9, 18, 6, 0, 0, 0, time.UTC)
	clock := first.Add(6 * time.Hour)
	require.NoError(t, h.ledger.RecordRouteWait(ctx, RouteWait{
		Route: route, Failures: 9, FirstAt: first, LastAt: clock.Add(-time.Hour), Until: clock.Add(-time.Minute),
	}))

	restarted := h.worktrees(fakeGit(t, `case "$*" in *"worktree add"*) exit 128;; esac`))
	restarted.now = func() time.Time { return clock }
	require.NoError(t, restarted.Recover(ctx))
	assert.Empty(t, restarted.RoutesWaiting(), "an elapsed backoff holds nothing")

	waits, err := h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	require.Len(t, waits, 1, "and the record of the failures stands until a worktree disproves it")

	// The route is startable, so it is tried at once — and the failure that
	// follows doubles from ten, not from one.
	_, err = restarted.Prepare(ctx, route, 140)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrPrepareBackoff)

	waits, err = h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	require.Len(t, waits, 1)
	assert.Equal(t, 10, waits[0].Failures)
	assert.Equal(t, first, waits[0].FirstAt.UTC(), "and it is still counted from when the route started failing")
	assert.Equal(t, PrepareBackoffMax, waits[0].Until.Sub(waits[0].LastAt),
		"the tenth failure waits the capped wait, not the first failure's minute")
	assert.Equal(t, []string{route}, restarted.RoutesWaiting())
}

// Doctor plans a route to learn what a dispatch would learn, and planning
// runs the same reads Prepare runs. It must not write the diagnostic it
// reports: a read-only surface that manufactured a wait would have status
// naming a route nothing is waiting on, and would do it for the routes an
// operator asked doctor about rather than the ones a task failed on.
func TestPlanningARouteRecordsNoWait(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()

	// A route that plans, and one that cannot: both through Plan, which is
	// what doctor calls.
	_, err := h.wt.Plan(ctx, filepath.Join(h.repo, "app"), "1-aaaaaa")
	require.NoError(t, err)

	outside := filepath.Join(filepath.Dir(h.repo), "no-repository-here")
	require.NoError(t, os.MkdirAll(outside, 0o700))
	_, err = h.wt.Plan(ctx, outside, "2-bbbbbb")
	require.ErrorIs(t, err, ErrRouteUnusable)

	gone := filepath.Join(filepath.Dir(h.repo), "not-mounted")
	_, err = h.wt.Plan(ctx, gone, "3-cccccc")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRouteUnusable, "the kind of failure that does arm a backoff from Prepare")

	waits, err := h.ledger.RouteWaits(ctx)
	require.NoError(t, err)
	assert.Empty(t, waits, "planning decides; only a dispatch that tried and failed records a wait")
	assert.Empty(t, h.wt.RoutesWaiting(), "and nothing is held on the strength of a plan")
}

// The same through the planner doctor actually builds, which has no ledger at
// all: reaching for one is the nil dereference PlanWorktrees exists to rule
// out, and a wait recorded from a plan would be reaching for one.
func TestAPlannerPlansWithoutALedgerToRecordAWaitIn(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()
	planner, err := PlanWorktrees(WorktreesOptions{Root: h.root, Lookup: h.lookup})
	require.NoError(t, err)

	_, err = planner.Plan(ctx, filepath.Join(h.repo, "app"), "4-dddddd")
	require.NoError(t, err)

	gone := filepath.Join(filepath.Dir(h.repo), "not-mounted")
	_, err = planner.Plan(ctx, gone, "5-eeeeee")
	require.Error(t, err, "and a plan that fails the way a backoff-arming Prepare fails still writes nothing")
}

// A ledger that will not take the row must not leave a backoff armed with
// nothing to show for it. The case that writes it is the likely one: a
// worktree that failed because the disk is full is a row that will not insert
// for the same reason, and the route would then be held for half an hour with
// no row — the invisible wait this table exists to end, back again after the
// operator has freed the disk.
func TestAWaitThatCouldNotBeRecordedArmsNoBackoff(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()
	route := filepath.Join(h.repo, "app")
	require.NoError(t, h.ledger.Close())

	_, err := h.wt.Prepare(ctx, route, 150)
	require.Error(t, err)
	assert.ErrorContains(t, err, "recording the wait it left",
		"the error says the worktree failed and that the wait could not be written")
	assert.Empty(t, h.wt.RoutesWaiting(),
		"nothing is held on a wait no one can read; the route is tried again, loudly, until the ledger takes it")
}

// Recovery is where the map is brought level with the ledger. Starting with
// an empty map over a ledger that still advertises the backoffs is the split
// this closes, at the one moment it is guaranteed to be wrong.
func TestRecoveryRefusesToStartWhenTheRecordedWaitsCannotBeRead(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()
	require.NoError(t, h.ledger.Close())

	err := h.wt.Recover(ctx)
	require.Error(t, err)
	assert.ErrorContains(t, err, "read the recorded waits on routes",
		"and it is this read that stops it, before the lock and the worktree rows")
}

// With worktrees off nothing else ever clears these rows: Prepare hands back
// the route without reaching the ledger. A drop that failed and was carried
// past would leave status claiming routes wait for worktrees this run will
// never make, for as long as the run lasts.
func TestRecoveryRefusesToStartWhenTheWaitsCannotBeDroppedWithWorktreesOff(t *testing.T) {
	h := newWorktreeHarness(t)
	ctx := context.Background()
	off, err := NewWorktrees(WorktreesOptions{Ledger: h.ledger, Root: h.root, Lookup: h.lookup, Off: true})
	require.NoError(t, err)
	require.NoError(t, h.ledger.Close())

	err = off.Recover(ctx)
	require.Error(t, err)
	assert.ErrorContains(t, err, "drop the recorded waits on routes")
}

// One read of connect.json decides the whole tick. Asked twice, the two
// answers can straddle the two-second cache: the tick would start records
// against the routes it saw first while pruning the waits of the routes it
// saw second, so a route removed in between is both dispatched to and
// forgotten.
func TestADispatchTickDecidesOnOneRouteSnapshot(t *testing.T) {
	h := newDispatchHarness(t, newFakeDriver(), func(o *DispatcherOptions) {
		o.Workspaces = waitingWorktrees(t, o.Ledger)
	})
	ctx := context.Background()
	h.mu.Lock()
	h.routeReads = 0
	h.mu.Unlock()

	require.NoError(t, h.d.dispatchReady(ctx))

	h.mu.Lock()
	reads := h.routeReads
	h.mu.Unlock()
	assert.Equal(t, 1, reads, "the approval map and the prune come from the same read")
}

// A prune that the ledger refused leaves rows for routes this tick has just
// seen connect.json stop naming. Reporting them would state as fact something
// already observed to be false, and a confidently wrong warning is worse than
// no warning — which is the whole subject of this table.
func TestAFailedPruneDoesNotReportRoutesItJustSawRemoved(t *testing.T) {
	var logs bytes.Buffer
	ws := &waitingWorkspaces{forgetErr: errors.New("the ledger will not take it")}
	h := newDispatchHarness(t, newFakeDriver(), func(o *DispatcherOptions) {
		o.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
		o.Workspaces = ws
	})
	ctx := context.Background()
	now := time.Now()
	for _, route := range []string{testRoute, "/work/no-longer-routed"} {
		require.NoError(t, h.ledger.RecordRouteWait(ctx, RouteWait{
			Route: route, Failures: 3, Reason: "fatal: cannot chdir",
			FirstAt: now.Add(-time.Hour), LastAt: now, Until: now.Add(PrepareBackoffMax),
		}))
	}

	h.reviewWaits(ctx)

	assert.Contains(t, logs.String(), "the ledger will not take it", "the prune failure is said")
	assert.Contains(t, logs.String(), `"route":"`+testRoute+`"`, "a route connect.json still names is still reported")
	assert.NotContains(t, logs.String(), "/work/no-longer-routed",
		"and one it has stopped naming is not, however the prune went")
}
