package connector

import (
	"context"
	"fmt"
	"log/slog"
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
			// The hold's own check, and the first check after it, find
			// Claude Code logged out; then someone logs in.
			if checks.Add(1) <= 2 {
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
