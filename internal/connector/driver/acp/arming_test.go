//go:build unix

package acp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// tokenArming is when the connector was told the worker's process group,
// relative to the start it was told it during. It is what the adapter
// compatibility check reads (compat_test.go, check 7): that check used to arm
// the token socket on Session.Process() right after NewSession returned, which
// is what it does under either ordering, so it passed whichever one the
// connector used and could not have caught a regression in it.
//
// TestTheArmingCheckTellsTheTwoOrderingsApart drives this through both
// orderings against the real driver, so the compat check's verdict is one that
// has been shown to distinguish them rather than one assumed to.
type tokenArming struct {
	mu          sync.Mutex
	armed       bool
	group       int
	duringStart bool
	returned    bool
}

// arm is what SessionConfig.Started is given: the worker's process, as soon
// as it exists.
func (a *tokenArming) arm(p driver.Process) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.armed {
		return
	}
	a.armed, a.group, a.duringStart = true, p.PGID, !a.returned
}

// startReturned is called the moment NewSession returns.
func (a *tokenArming) startReturned() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.returned = true
}

// verdict says whether the socket was armed in time for an agent whose
// handshake does not finish until its MCP servers have connected.
func (a *tokenArming) verdict() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case !a.armed:
		return errors.New("the connector was never told the worker's process group, so the token socket served nobody")
	case !a.duringStart:
		return errors.New("the token socket was armed only after NewSession returned: an agent that cannot finish its handshake until its MCP servers have connected waits for a token this ordering cannot hand over until that handshake has finished")
	case a.group <= 1:
		return fmt.Errorf("the worker was announced as process group %d, which the token socket serves nothing", a.group)
	}
	return nil
}

// The compat check's verdict must come out differently under the two
// orderings, or it is measuring nothing about either. Both runs use the real
// driver and the same fake adapter; only where the arming happens differs.
func TestTheArmingCheckTellsTheTwoOrderingsApart(t *testing.T) {
	t.Run("armed as soon as the worker's process exists", func(t *testing.T) {
		h := newHarness(t)
		var arming tokenArming
		h.withConfig = func(cfg driver.SessionConfig) driver.SessionConfig {
			cfg.Started = arming.arm
			return cfg
		}
		s, err := h.driver().NewSession(context.Background(), h.config())
		arming.startReturned()
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })
		assert.NoError(t, arming.verdict(), "the ordering the connector uses now")
	})

	t.Run("armed once the session was open", func(t *testing.T) {
		h := newHarness(t)
		var arming tokenArming
		s, err := h.driver().NewSession(context.Background(), h.config())
		arming.startReturned()
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })
		// The ordering the connector used to use, and the one the compat
		// check used to perform on its own: Session.Process(), after the
		// handshake.
		arming.arm(s.Process())
		assert.ErrorContains(t, arming.verdict(), "after NewSession returned",
			"the check must fail on the ordering the card is about")
	})
}

// This driver is the one the handshake ordering is about: initialize, the
// session, its asking mode and the adapter's own /mcp read-back — a real
// prompt turn — all run inside NewSession. An adapter that will not finish
// that handshake until its MCP servers have connected is waiting for a task
// token, and the connector cannot hand one over until it knows the worker's
// process group. So the group must be known while the handshake is still
// running, not when it returns (driver invariant 7).
//
// The fake hangs at session/new, which is the middle of the handshake and
// stands in for an adapter waiting on its servers. The worker must already
// have been announced by then.
func TestTheWorkerIsAnnouncedWhileTheHandshakeIsStillRunning(t *testing.T) {
	h := newHarness(t)
	h.sc.Hang = "session/new"
	announced := make(chan driver.Process, 1)
	h.withConfig = func(cfg driver.SessionConfig) driver.SessionConfig {
		cfg.Started = func(p driver.Process) { announced <- p }
		return cfg
	}
	d := h.driver()
	d.opts.HandshakeTimeout = 3 * time.Second

	type start struct {
		s   driver.Session
		err error
	}
	done := make(chan start, 1)
	go func() {
		s, err := d.NewSession(context.Background(), h.config())
		done <- start{s, err}
	}()

	var worker driver.Process
	select {
	case worker = <-announced:
	case got := <-done:
		if got.s != nil {
			_ = got.s.Close()
		}
		t.Fatalf("the handshake ended without the worker ever being announced: %v", got.err)
	case <-time.After(30 * time.Second):
		t.Fatal("the worker was never announced")
	}
	assert.Greater(t, worker.PGID, 1, "announced by a process group the token socket can serve")
	assert.Equal(t, worker.PID, worker.PGID, "the adapter leads its own group")
	select {
	case got := <-done:
		if got.s != nil {
			_ = got.s.Close()
		}
		t.Fatalf("the handshake had already finished: this proves nothing about the order (%v)", got.err)
	default:
	}

	// And the hung handshake ends as it always did: a start that launched a
	// process, whose group the driver has already asked to end.
	got := <-done
	require.Error(t, got.err)
	require.Nil(t, got.s)
	assert.Equal(t, worker, driver.StartedProcess(got.err), "the process the failed start names is the one it announced")
}
