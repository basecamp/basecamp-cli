package connector

import (
	"bytes"
	"context"
	"log/slog"
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

	h.d.reportWaitingRoutes(ctx)
	assert.Equal(t, 1, strings.Count(logs.String(), `"route":"/work/broken"`))
	assert.Contains(t, logs.String(), `"failures":7`)
	assert.Contains(t, logs.String(), "fatal: not a git repository")

	h.d.reportWaitingRoutes(ctx)
	assert.Equal(t, 1, strings.Count(logs.String(), `"route":"/work/broken"`), "not on every tick")

	h.d.waitingAt = time.Time{}
	h.d.reportWaitingRoutes(ctx)
	assert.Equal(t, 2, strings.Count(logs.String(), `"route":"/work/broken"`), "and again ten minutes later")

	// A route whose wait has elapsed is one the next record will try, so it is
	// not reported as waiting — and a route nothing is routed to any more does
	// not warn for as long as the connector runs.
	logs.Reset()
	require.NoError(t, h.ledger.RecordRouteWait(ctx, RouteWait{
		Route: "/work/broken", Failures: 8, FirstAt: now.Add(-6 * time.Hour), LastAt: now.Add(-time.Hour), Until: now.Add(-time.Minute),
	}))
	h.d.waitingAt = time.Time{}
	h.d.reportWaitingRoutes(ctx)
	assert.NotContains(t, logs.String(), "/work/broken")
}
