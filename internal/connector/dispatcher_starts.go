package connector

import (
	"context"
	"strings"
	"time"

	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// A worker that cannot start fails every request the same way: a launcher
// whose target is gone, a logged-out agent, a version too old for the
// connector's flags. Each request it is handed is answered "I couldn't start
// on this", and the next one meets the same computer. So after
// StartFailuresToHold attempts in a row whose worker never picked its request
// up, the dispatcher starts nothing new: records wait, admitted, for a worker
// that can take them. It says so once, as an ERROR line, and in `connect
// status` through the connection row.
//
// The hold is this process's, not the ledger's. It decides only whether a
// pass starts anything, which the dispatcher already declines to do when
// connect.json cannot be read; it changes no record, takes no worker slot,
// and every guarantee the ledger holds for a start holds for the start that
// ends it. A restart ends it, and so does the worker: the dispatcher checks it
// with its preflight (DispatcherOptions.Preflight), without any work, and a
// check that passes takes work again. When the check cannot see what stopped
// the worker, the next request finds out; one more failure holds work again,
// and each time that happens the dispatcher waits twice as long before its
// next check, up to HoldCheckLimit.

// StartFailuresToHold is how many attempts in a row whose worker never picked
// its request up hold new work. One could be anything; two in a row is the
// computer.
const StartFailuresToHold = 2

// HoldCheckInterval is how long held work waits before the worker is first
// checked again; HoldCheckLimit is the longest it waits.
const (
	HoldCheckInterval = time.Minute
	HoldCheckLimit    = time.Hour
)

// ConnectionNotTakingWork is a running connector holding new work because its
// worker could not start. The connection row's detail says why.
const ConnectionNotTakingWork = "not_taking_work"

// NotTakingWorkFix is what a person does about it.
const NotTakingWorkFix = "Fix it, then run `basecamp connect setup` or restart the connector."

// startRecord is whether the worker has been starting.
type startRecord struct {
	// failures counts attempts in a row whose worker never picked its request
	// up.
	failures int
	// held is whether new work is held.
	held bool
	// checkAt is when a held dispatcher next checks the worker, and checking
	// is whether a check is running.
	checkAt  time.Time
	checking bool
	// checkEvery is how long a hold waits before its first check.
	checkEvery time.Duration
}

// noteStart reads a settled attempt for whether its worker started: one that
// picked a request up did, whatever happened after; one refused a start, or
// gone before it picked its request up, did not. An attempt interrupted by
// the connector says nothing either way. said is what the worker or its start
// said last, for the person.
func (d *Dispatcher) noteStart(ctx context.Context, s Settlement, said string) {
	switch {
	case pickedUp(s):
		d.startWorked(ctx)
	case s.SpawnFailed || workerNeverStarted(s):
		d.startFailed(ctx, said)
	}
}

func pickedUp(s Settlement) bool {
	for _, e := range s.Events {
		if e.Pulled {
			return true
		}
	}
	return false
}

func workerNeverStarted(s Settlement) bool {
	for _, e := range s.Events {
		if neverStarted(e, s.Stop) {
			return true
		}
	}
	return false
}

func (d *Dispatcher) startWorked(ctx context.Context) {
	d.mu.Lock()
	wasHeld := d.starts.held
	d.starts = startRecord{checkEvery: d.opts.HoldCheck}
	d.mu.Unlock()
	if wasHeld {
		d.takeWorkAgain(ctx, "the worker started")
	}
}

func (d *Dispatcher) startFailed(ctx context.Context, said string) {
	d.mu.Lock()
	d.starts.failures++
	hold := d.starts.failures >= StartFailuresToHold && !d.starts.held
	if hold {
		// Checking until the hold is recorded: a check that passed in the
		// meantime would take work again before the hold was said.
		d.starts.held, d.starts.checking = true, true
	}
	d.mu.Unlock()
	if !hold {
		return
	}

	why := d.whyNotStarting(ctx, said)
	d.log.Error("connector: not taking work: " + why + ". " + NotTakingWorkFix)
	if err := d.ledger.NoteConnection(ctx, ConnectionNotTakingWork, why); err != nil {
		d.log.Warn("connector: could not record that no work is being taken, for status", "error", err)
	}
	d.mu.Lock()
	d.starts.checking = false
	d.starts.checkAt = time.Now().Add(d.starts.checkEvery)
	d.mu.Unlock()
}

// whyNotStarting is the hold's reason in a person's words: what the worker's
// preflight finds wrong, when it finds something, else what the worker said.
func (d *Dispatcher) whyNotStarting(ctx context.Context, said string) string {
	product := d.opts.Driver.Name()
	reason := strings.TrimSpace(said)
	if d.opts.Preflight != nil {
		p := d.opts.Preflight(ctx)
		if p.Product != "" {
			product = p.Product
		}
		if c, failed := p.Failed(); failed {
			reason = c.Message
		}
	}
	if reason == "" {
		reason = "it stopped before it picked up its request"
	}
	reason = strings.TrimRight(richtext.SanitizeSingleLine(reason), ".")
	return product + " couldn't start twice in a row — " + reason
}

// startsHeld reports whether new work is held, and nothing more: a pass asks
// holdingNewWork once, which may start a check; each launch in the pass asks
// this.
func (d *Dispatcher) startsHeld() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.starts.held
}

// holdingNewWork reports whether new work is held, and starts a check of the
// worker when one is due.
func (d *Dispatcher) holdingNewWork(ctx context.Context) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.starts.held {
		return false
	}
	if d.opts.Preflight != nil && !d.starts.checking && !time.Now().Before(d.starts.checkAt) {
		d.starts.checking = true
		d.wg.Go(func() { d.checkHeldWorker(ctx) })
	}
	return true
}

// checkHeldWorker runs the worker's preflight for a hold. A pass takes work
// again one failure short of holding it again, and doubles the wait before
// the next hold's first check: the check could not see what stopped the
// worker, or the worker has been fixed, and the next start says which.
func (d *Dispatcher) checkHeldWorker(ctx context.Context) {
	p := d.opts.Preflight(ctx)
	_, failed := p.Failed()
	d.mu.Lock()
	d.starts.checking = false
	if !d.starts.held {
		d.mu.Unlock()
		return
	}
	if failed || ctx.Err() != nil {
		d.starts.checkAt = time.Now().Add(d.starts.checkEvery)
		d.mu.Unlock()
		return
	}
	d.starts.held = false
	d.starts.failures = StartFailuresToHold - 1
	d.starts.checkEvery = min(d.starts.checkEvery*2, HoldCheckLimit)
	d.mu.Unlock()
	d.takeWorkAgain(ctx, "the worker started when it was checked")
}

func (d *Dispatcher) takeWorkAgain(ctx context.Context, why string) {
	d.log.Info("connector: taking work again: " + why)
	if err := d.ledger.NoteConnection(ctx, ConnectionRunning, ""); err != nil {
		d.log.Warn("connector: could not record that work is being taken again, for status", "error", err)
	}
}
