package admission

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrAlreadyDecided is the ledger's answer to a verdict whose record changed
// after the decision loaded it: another decision was written first.
var ErrAlreadyDecided = errors.New("admission: event already decided")

// Ledger is the durable half of a verdict. Intake's SQLite ledger implements
// it (internal/connector, Ledger.Admission).
type Ledger interface {
	// Commit writes v onto its event's record in one transaction and returns
	// the state it wrote. Within that transaction it must:
	//
	//   - apply only when the record's revision is still v.Revision, the one
	//     the decision was loaded at, and bump it; otherwise return
	//     ErrAlreadyDecided. So an event is admitted at most once however many
	//     fetches, restarts or redispatches decide it, and a decision made on
	//     an older load never overwrites a newer verdict, blocked ones
	//     included;
	//   - for an admitted verdict, read the conversation in the same
	//     transaction and write queued when it is live — a task running, or
	//     an admitted record not yet dispatched, since that record becomes the
	//     task — so an event queued before the task closes joins it and one
	//     after starts a new task;
	//   - write content only for an admitted verdict, which it may write as
	//     queued;
	//   - keep a throttled verdict's RetryAt, which NextBlockedRetry needs.
	Commit(ctx context.Context, v Verdict) (State, error)
}

// Committer serializes commits per conversation key, so the verdicts for one
// conversation reach the ledger one at a time, in the order they finished.
// Which state is written is the ledger's decision, taken in its own
// transaction; the committer reports that state rather than guessing it.
type Committer struct {
	ledger Ledger
	locks  keyedMutex
}

// NewCommitter builds a committer over ledger.
func NewCommitter(ledger Ledger) *Committer {
	return &Committer{ledger: ledger}
}

// Commit writes the verdict and returns it with the state the ledger wrote.
func (c *Committer) Commit(ctx context.Context, v Verdict) (Verdict, error) {
	if v.ConversationKey != "" {
		unlock, err := c.locks.lock(ctx, v.ConversationKey)
		if err != nil {
			return v, err
		}
		defer unlock()
	}
	// A shutdown that arrived while this waited is not a verdict to write.
	if err := ctx.Err(); err != nil {
		return v, err
	}
	written, err := c.ledger.Commit(ctx, v)
	if err != nil {
		return v, err
	}
	switch {
	case written == v.State:
	case v.State == StateAdmitted && (written == StateQueued || written == StateHeld):
		v.State = written
	default:
		return v, fmt.Errorf("admission: ledger wrote %s for a %s verdict on event %d", written, v.State, v.EventID)
	}
	return v, nil
}

// keyedMutex is a set of mutexes created on demand and dropped when nobody
// holds or waits for them, so a long-running process does not keep one per
// conversation it has ever seen.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyLock
}

type keyLock struct {
	ch   chan struct{}
	refs int
}

func (k *keyedMutex) lock(ctx context.Context, key string) (func(), error) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*keyLock{}
	}
	l := k.locks[key]
	if l == nil {
		l = &keyLock{ch: make(chan struct{}, 1)}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()

	release := func() {
		k.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}

	select {
	case l.ch <- struct{}{}:
		// With the lock free and ctx already done, select may pick either
		// case; a canceled caller never holds the lock.
		if err := ctx.Err(); err != nil {
			<-l.ch
			release()
			return nil, err
		}
		return func() {
			<-l.ch
			release()
		}, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}

// Blocked-record recovery. A record blocked on a read (read_failed,
// read_unresolved), on unverified trust (trust_unverified) or on an
// unverified assignment delta is retried every ten minutes for a day after it
// was first blocked, and on redispatch at any time after that. A throttled
// record (throttled) is on the same schedule, never before the server's
// deadline. A record blocked because connect.json could not be read
// (config_unreadable) is on the same interval with no window at all: that is
// a local file a person repairs, and an operator away for a week is
// ordinary, where a day of a failing server means the server is not coming
// back on its own. bucket_mismatch and unroutable are not timed: the
// pointer's bucket and type never change, so only a person's redispatch
// re-runs them. no_route is not timed either — it waits for the operator to
// serve the project, which is a decision, not a delay. A blocked record is
// never discarded for having failed: the checkpoint may already be past the
// event, and a tombstone would turn an outage into a permanent loss.
//
// The intake sweep is what runs this schedule (internal/connector,
// Intake.sweepBlockedRetries): it asks the ledger for the records this
// function calls due and offers them back to admission.
const (
	BlockedRetryInterval = 10 * time.Minute
	BlockedRetryWindow   = 24 * time.Hour
)

// timedBlockedReasons is every blocked reason the schedule re-runs, and for
// each whether its retries stop at BlockedRetryWindow.
//
// NextBlockedRetry is its only reader, and that is the whole of how a reason
// reaches the sweep: the answer is computed once, when the verdict is
// written, and stored on the row (events.next_retry_at). The ledger's queries
// never see a reason — they compare that stored moment. A second filter by
// reason down there could only ever repeat this one, and would be a copy of
// the schedule that could fall out of step with it (Copilot on #770).
var timedBlockedReasons = map[Reason]bool{
	ReasonReadFailed:       true,
	ReasonReadUnresolved:   true,
	ReasonDeltaUnverified:  true,
	ReasonTrustUnverified:  true,
	ReasonThrottled:        true,
	ReasonConfigUnreadable: false,
}

// NextBlockedRetry returns when a blocked record should next be re-run, and
// false when it waits for something other than time: the operator serving the
// project (no_route), or a person's redispatch once the window has passed.
// notBefore is a throttled record's Verdict.RetryAt; no retry is scheduled
// before it, and a deadline past the window hands the record to redispatch
// rather than asking early.
//
// since is when the record entered the reason it is blocked on NOW, which is
// not when it entered blocked. The reasons carry different windows, so a
// record that spent a week as the unbounded config_unreadable and then blocks
// read_failed must get read_failed's twenty-four hours from the moment
// read_failed began; measured from the older stamp it would get none, which
// is the retry stranding the work it is there to recover (Copilot on #770).
// The ledger keeps that stamp per reason (events.retry_since).
//
// The returned time may be in the past — a connector that was not running
// when a retry came due is late, not excused — so a caller asks whether next
// is at or before now, never whether it is in the future.
func NextBlockedRetry(reason Reason, since, lastAttempt, notBefore time.Time) (time.Time, bool) {
	bounded, timed := timedBlockedReasons[reason]
	if !timed {
		return time.Time{}, false
	}
	next := lastAttempt.Add(BlockedRetryInterval)
	if notBefore.After(next) {
		next = notBefore
	}
	if bounded && next.After(since.Add(BlockedRetryWindow)) {
		return time.Time{}, false
	}
	return next, true
}
