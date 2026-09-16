package admission

import (
	"context"
	"sync"
	"time"
)

// Ledger is the durable half of a verdict. Intake's SQLite ledger implements
// it once basecamp-cli PR 729 merges; until then only tests do.
type Ledger interface {
	// LiveTask reports whether a task is live on the conversation.
	LiveTask(ctx context.Context, conversationKey string) (bool, error)
	// Commit writes the verdict onto the event's record. LiveTask is read
	// before Commit and a task can close in between, so queued is the
	// committer's best knowledge, not a guarantee: the ledger re-reads the
	// conversation in the same transaction as the write, so an event queued
	// before the task closes joins it and one committed after starts a new
	// task, and neither is lost (spec, "6. Admission").
	Commit(ctx context.Context, v Verdict) error
}

// Committer serializes commits per conversation key, so two fetches for one
// conversation that finish in either order commit one after the other, each
// seeing what the other wrote.
type Committer struct {
	ledger Ledger
	locks  keyedMutex
}

// NewCommitter builds a committer over ledger.
func NewCommitter(ledger Ledger) *Committer {
	return &Committer{ledger: ledger}
}

// Commit turns an admitted verdict into queued when its conversation has a
// live task, and writes it. The decision and the write happen under the
// conversation's lock. It returns the verdict as committed.
func (c *Committer) Commit(ctx context.Context, v Verdict) (Verdict, error) {
	if v.ConversationKey == "" {
		return v, c.ledger.Commit(ctx, v)
	}
	unlock, err := c.locks.lock(ctx, v.ConversationKey)
	if err != nil {
		return v, err
	}
	defer unlock()

	if v.State == StateAdmitted {
		live, err := c.ledger.LiveTask(ctx, v.ConversationKey)
		if err != nil {
			return v, err
		}
		if live {
			v.State = StateQueued
		}
	}
	return v, c.ledger.Commit(ctx, v)
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
	case ReasonReadFailed, ReasonReadUnresolved, ReasonDeltaUnverified:
	default:
		return time.Time{}, false
	}
	next := lastAttempt.Add(BlockedRetryInterval)
	if next.After(blockedAt.Add(BlockedRetryWindow)) {
		return time.Time{}, false
	}
	return next, true
}
