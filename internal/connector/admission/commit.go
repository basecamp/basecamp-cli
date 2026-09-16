package admission

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrAlreadyDecided is the ledger's answer to a verdict for a record that has
// already moved past deciding.
var ErrAlreadyDecided = errors.New("admission: event already decided")

// Ledger is the durable half of a verdict. Intake's SQLite ledger implements
// it once basecamp-cli PR 729 merges; until then only tests do.
type Ledger interface {
	// Commit writes v onto its event's record in one transaction and returns
	// the state it wrote. Within that transaction it must:
	//
	//   - apply only to a record still being decided — seen, or blocked (a
	//     blocked record is decided again on recovery) — and return
	//     ErrAlreadyDecided for any other, so an event is admitted at most
	//     once however many fetches or restarts decide it;
	//   - for an admitted verdict, read the conversation in the same
	//     transaction and write queued when it is live — a task running, or
	//     an admitted record not yet dispatched, since that record becomes the
	//     task — so an event queued before the task closes joins it and one
	//     after starts a new task;
	//   - write no content for a verdict that is not admitted.
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
	written, err := c.ledger.Commit(ctx, v)
	if err != nil {
		return v, err
	}
	switch {
	case written == v.State:
	case v.State == StateAdmitted && written == StateQueued:
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
// read_unresolved) or on an unverified assignment delta is retried every ten
// minutes for a day after it was first blocked, and on redispatch at any time
// after that. It is never discarded for having failed: the checkpoint may
// already be past the event, and a tombstone would turn an outage into a
// permanent loss.
const (
	BlockedRetryInterval = 10 * time.Minute
	BlockedRetryWindow   = 24 * time.Hour
)

// NextBlockedRetry returns when a blocked record should next be re-run, and
// false when it waits for something other than time: a route (no_route), or
// a person's redispatch once the window has passed.
func NextBlockedRetry(reason Reason, blockedAt, lastAttempt time.Time) (time.Time, bool) {
	switch reason {
	case ReasonReadFailed, ReasonReadUnresolved, ReasonDeltaUnverified, ReasonBucketMismatch:
	default:
		return time.Time{}, false
	}
	next := lastAttempt.Add(BlockedRetryInterval)
	if next.After(blockedAt.Add(BlockedRetryWindow)) {
		return time.Time{}, false
	}
	return next, true
}
