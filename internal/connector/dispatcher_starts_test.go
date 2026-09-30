package connector

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// pickUp is a worker's first get_dispatch of its task's originating event.
func pickUp(t *testing.T, l *Ledger, taskID int64) {
	t.Helper()
	_, err := l.db.ExecContext(context.Background(),
		`UPDATE task_events SET pulled_at = ? WHERE task_id = ? AND pulled_at IS NULL`, l.timestamp(), taskID)
	require.NoError(t, err)
}

// failingBeforePickUp is a worker that ends its session before it asks for
// its request, n times, and then works.
func failingBeforePickUp(t *testing.T, h **dispatchHarness, n int32) func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
	var sessions atomic.Int32
	return func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
		if sessions.Add(1) <= n {
			return driver.PromptResult{}, fmt.Errorf("%w: %w: the agent closed its output before it confirmed the session", driver.ErrSessionUnverified, driver.ErrSessionEnded)
		}
		pickUp(t, (*h).ledger, s.cfg.Scope.TaskID)
		return driver.PromptResult{Stop: driver.TurnEndTurn}, nil
	}
}

func connectionOf(t *testing.T, l *Ledger) ConnectionStatus {
	t.Helper()
	s, err := l.Status(context.Background())
	require.NoError(t, err)
	if s.Connection == nil {
		return ConnectionStatus{}
	}
	return *s.Connection
}

func startsOf(fake *fakeDriver, calls *atomic.Int32) {
	fake.onStart = func(driver.SessionConfig) { calls.Add(1) }
}

// The hold is asked before every launch, not once a batch: starts that fail
// at once give their slots back, so a batch that only checked capacity would
// go on launching after the second failure (Codex on #794).
func TestAHoldStopsTheRestOfTheBatch(t *testing.T) {
	fake := newFakeDriver()
	notFound := fmt.Errorf("%w: exec: \"claude\": executable file not found in $PATH", driver.ErrNotStarted)
	fake.startErr = []error{notFound, notFound, notFound, notFound, notFound}
	var calls atomic.Int32
	startsOf(fake, &calls)
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) { o.Concurrency = 2 })
	for id := int64(1); id <= 5; id++ {
		admitOn(t, h.ledger, id, fmt.Sprintf("recording:%d", id))
	}
	stop := h.run(t)

	require.Eventually(t, func() bool { return connectionOf(t, h.ledger).State == ConnectionNotTakingWork }, 10*time.Second, 10*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	stop()
	assert.Equal(t, int32(StartFailuresToHold), calls.Load(), "nothing is started after the second failure, in the same batch or the next")
}

// The worker is checked before anything is handed to it: a connector that
// starts with Claude Code logged out holds new work from the first pass, says
// why, and takes work once a check passes. No request is spent finding out
// (Codex on #794).
func TestAWorkerThatIsNotReadyAtStartHoldsWorkUntilItIs(t *testing.T) {
	fake := newFakeDriver()
	var h *dispatchHarness
	fake.turn = func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
		pickUp(t, h.ledger, s.cfg.Scope.TaskID)
		return driver.PromptResult{Stop: driver.TurnEndTurn}, nil
	}
	var calls atomic.Int32
	startsOf(fake, &calls)
	var checks atomic.Int32
	var logs safeBuffer
	h = newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.Concurrency = 1
		o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
		o.HoldCheck = 300 * time.Millisecond
		o.Preflight = func(context.Context) driver.Preflight {
			p := driver.Preflight{Product: "Claude Code"}
			if checks.Add(1) == 1 {
				p.Checks = []driver.PreflightCheck{{Name: driver.PreflightLogin, Status: driver.PreflightFail,
					Message: "Claude Code is logged out on this computer — run `claude` and log in."}}
			}
			return p
		}
	})
	admitOn(t, h.ledger, 1, "recording:1")
	stop := h.run(t)
	defer stop()

	require.Eventually(t, func() bool { return connectionOf(t, h.ledger).State == ConnectionNotTakingWork }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, "Claude Code isn't ready — Claude Code is logged out on this computer — run `claude` and log in", connectionOf(t, h.ledger).Detail)
	assert.Equal(t, int32(0), calls.Load(), "nothing is handed to a worker that isn't ready")
	assert.Contains(t, logs.String(), "level=ERROR msg=\"connector: not taking work: Claude Code isn't ready")

	require.Eventually(t, func() bool { return calls.Load() == 1 }, 10*time.Second, 10*time.Millisecond, "a check that passes takes work")
	require.Eventually(t, func() bool { return connectionOf(t, h.ledger).State == ConnectionRunning }, 5*time.Second, 10*time.Millisecond)
}

// A hold's reason is recorded only while the hold stands. Its preflight runs
// outside the lock, so a start that worked can clear the hold and record the
// connector running first; the late reason must not then say it isn't taking
// work (Codex on #794).
func TestAHoldClearedWhileItsPreflightRunsLeavesStatusRunning(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	h := newDispatchHarness(t, newFakeDriver(), func(o *DispatcherOptions) {
		o.Preflight = func(context.Context) driver.Preflight {
			entered <- struct{}{}
			<-release
			return driver.Preflight{Product: "Claude Code", Checks: []driver.PreflightCheck{{Name: driver.PreflightLogin,
				Status: driver.PreflightFail, Message: "Claude Code is logged out on this computer"}}}
		}
	})
	ctx := context.Background()
	h.d.startFailed(ctx, "")
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.d.startFailed(ctx, "") // the second in a row: holds, then asks the preflight why
	}()
	<-entered
	h.d.startWorked(ctx) // a healthy task settles meanwhile
	require.Equal(t, ConnectionRunning, connectionOf(t, h.ledger).State)
	close(release)
	<-done

	assert.Equal(t, ConnectionRunning, connectionOf(t, h.ledger).State, "the hold was cleared before its reason arrived")
	assert.False(t, h.d.startsHeld())
}

// A session that can't even be prepared — its directory can't be made — is a
// start that never ran, like a driver's refusal: two in a row hold new work
// rather than failing every queued request (Codex on #794).
func TestSessionsThatCannotBePreparedHoldNewWork(t *testing.T) {
	fake := newFakeDriver()
	var calls atomic.Int32
	startsOf(fake, &calls)
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) { o.Concurrency = 1 })
	for id := int64(1); id <= 4; id++ {
		admitOn(t, h.ledger, id, fmt.Sprintf("recording:%d", id))
	}
	require.NoError(t, os.RemoveAll(h.d.opts.PrivateDir)) // every session's directory now fails
	stop := h.run(t)

	require.Eventually(t, func() bool { return connectionOf(t, h.ledger).State == ConnectionNotTakingWork }, 10*time.Second, 10*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	stop()
	assert.Zero(t, calls.Load(), "no session reached the driver")
	for id := int64(1); id <= 4; id++ {
		assert.Equal(t, StateAdmitted, stateOf(t, h.ledger, id), "record %d waits, its automatic retry unspent, for a start that can work", id)
	}
}

// Two starts in a row that never ran hold new work: the next record waits,
// admitted, and the connector says so once, with the fix. A restart takes
// work again.
func TestTwoFailedStartsInARowHoldNewWork(t *testing.T) {
	fake := newFakeDriver()
	notFound := fmt.Errorf("%w: exec: \"claude\": executable file not found in $PATH", driver.ErrNotStarted)
	fake.startErr = []error{notFound, notFound}
	var calls atomic.Int32
	startsOf(fake, &calls)
	var logs safeBuffer
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.Concurrency = 1
		o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})
	admitOn(t, h.ledger, 1, "recording:1")
	admitOn(t, h.ledger, 2, "recording:2")
	stop := h.run(t)

	require.Eventually(t, func() bool { return connectionOf(t, h.ledger).State == ConnectionNotTakingWork }, 10*time.Second, 10*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	stop()
	assert.Equal(t, int32(2), calls.Load(), "nothing is started after the second failure")
	admitted := 0
	for _, id := range []int64{1, 2} {
		if stateOf(t, h.ledger, id) == StateAdmitted {
			admitted++
		}
	}
	assert.Positive(t, admitted, "the work waits for a worker that can take it")
	why := "fake couldn't start twice in a row — exec: \"claude\": executable file not found in $PATH"
	assert.Equal(t, why, connectionOf(t, h.ledger).Detail)
	assert.Equal(t, 1, strings.Count(logs.String(), "level=ERROR msg=\"connector: not taking work: "), logs.String())
	assert.Contains(t, logs.String(), "Fix it, then run `basecamp connect setup` or restart the connector.")

	restarted := newFakeDriver()
	opts := h.d.opts
	opts.Driver = restarted
	d, err := NewDispatcher(opts)
	require.NoError(t, err)
	h.d = d
	h.run(t)
	<-restarted.made
}

// A worker whose session ends before it asks for its request did not start
// either, though a process existed: two in a row hold new work, and the
// preflight's reason is the one given. A check that passes takes work again.
func TestAWorkerThatNeverPicksItsRequestUpHoldsNewWorkUntilItStarts(t *testing.T) {
	fake := newFakeDriver()
	var h *dispatchHarness
	fake.turn = failingBeforePickUp(t, &h, 2)
	var calls atomic.Int32
	startsOf(fake, &calls)
	var checks atomic.Int32
	var logs safeBuffer
	h = newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.Concurrency = 1
		o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
		o.HoldCheck = 50 * time.Millisecond
		o.Preflight = func(context.Context) driver.Preflight {
			p := driver.Preflight{Product: "Claude Code"}
			// The check at start passes: what ends these sessions is not
			// something it can see. The hold's own check, and the first check
			// after it, find Claude Code logged out; then someone logs in.
			if n := checks.Add(1); n == 2 || n == 3 {
				p.Checks = []driver.PreflightCheck{{Name: driver.PreflightLogin, Status: driver.PreflightFail,
					Message: "Claude Code is logged out on this computer — run `claude` and log in"}}
			}
			return p
		}
	})
	for id := int64(1); id <= 3; id++ {
		admitOn(t, h.ledger, id, fmt.Sprintf("recording:%d", id))
	}
	h.run(t)

	require.Eventually(t, func() bool { return connectionOf(t, h.ledger).State == ConnectionNotTakingWork }, 10*time.Second, 10*time.Millisecond)
	assert.Equal(t, "Claude Code couldn't start twice in a row — Claude Code is logged out on this computer — run `claude` and log in",
		connectionOf(t, h.ledger).Detail)
	assert.Equal(t, StateAdmitted, stateOf(t, h.ledger, 3), "the third request waits")
	for _, id := range []int64{1, 2} {
		assert.Equal(t, StateCompleted, stateOf(t, h.ledger, id))
		assert.Equal(t, string(OutcomeUnknown), outcomeOf(t, h.ledger, id), "a process existed: never run again automatically")
	}

	rows := h.attemptsEnded(t, 3)
	assert.Equal(t, "finished", rows[2].StopReason)
	assert.Equal(t, StateCompleted, stateOf(t, h.ledger, 3))
	assert.Equal(t, ConnectionRunning, connectionOf(t, h.ledger).State)
	assert.Contains(t, logs.String(), "connector: taking work again: the worker started when it was checked")
	assert.GreaterOrEqual(t, checks.Load(), int32(3))
}

// A start that works in between clears the count: failures are held only
// when they come in a row.
func TestAStartThatWorksClearsTheCount(t *testing.T) {
	fake := newFakeDriver()
	var h *dispatchHarness
	var sessions atomic.Int32
	fake.turn = func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
		if n := sessions.Add(1); n == 2 || n == 4 {
			pickUp(t, h.ledger, s.cfg.Scope.TaskID)
			return driver.PromptResult{Stop: driver.TurnEndTurn}, nil
		}
		return driver.PromptResult{}, fmt.Errorf("%w: %w: gone", driver.ErrSessionUnverified, driver.ErrSessionEnded)
	}
	h = newDispatchHarness(t, fake, func(o *DispatcherOptions) { o.Concurrency = 1 })
	for id := int64(1); id <= 4; id++ {
		admitOn(t, h.ledger, id, fmt.Sprintf("recording:%d", id))
	}
	h.run(t)
	h.attemptsEnded(t, 4)
	assert.NotEqual(t, ConnectionNotTakingWork, connectionOf(t, h.ledger).State)
}
