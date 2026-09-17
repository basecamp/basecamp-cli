package connector

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// Backlog thresholds. Intake is the only work on the feed's delivery path, so
// the queue is where a slow admission shows up.
const (
	// DefaultBacklogWarn is the depth at which the backlog is worth saying out
	// loud. Nothing changes; the operator is told.
	DefaultBacklogWarn = 1_000
	// DefaultBacklogPause is the depth at which intake stops consuming the
	// feed. The checkpoint does not move while it is paused, because the
	// package saves a position only after its page's events were accepted and
	// intake has stopped accepting them — so a crash while paused resumes from
	// before the backlog rather than after it.
	DefaultBacklogPause = 10_000
)

// Queue is the seam between intake and admission: intake writes a pointer and
// hands over an id, admission reads it when it gets there. Two queues with
// visible depth rather than one pipeline, so a busy dispatcher can never stall
// the socket — and so the place where work is piling up is the place the depth
// is showing.
type Queue struct {
	ids    chan int64
	warnAt int

	// edges serializes the depth sample with the warning transition and its
	// callback. Unserialized, an offer can sample a warning depth, a take can
	// drain and find nothing to recover from, and the offer then raises a
	// warning that no later event will clear.
	edges  sync.Mutex
	warned bool
	// depth is the queue's length as the operations themselves report it.
	depth int
	// afterChannelOp runs between a channel operation and the transition it
	// produces. A test seam: it is where the queue was losing a crossing.
	afterChannelOp func()
	// pending holds edges decided but not yet delivered, in the order they
	// were decided; delivering says a goroutine is already draining them.
	pending    []queueEdge
	delivering bool
	// waiting counts offers blocked for room. Intake and every open repair
	// walk offer concurrently, so a single flag would be cleared by the first
	// waiter to resume while another — perhaps the feed — still waits.
	waiting atomic.Int32

	// OnWarn fires when the depth first crosses the warning threshold, and
	// OnRecover when it falls back below. Both are optional.
	OnWarn    func(depth int)
	OnRecover func(depth int)
	// OnPause fires when an offer begins waiting for room, and OnResume when
	// it stops. The feed is not being consumed in between.
	OnPause  func(depth int)
	OnResume func(depth int)
}

// NewQueue builds a queue that warns at warnAt and pauses the feed at pauseAt.
func NewQueue(warnAt, pauseAt int) (*Queue, error) {
	if pauseAt <= 0 {
		return nil, errors.New("connector: queue pause threshold must be positive")
	}
	if warnAt <= 0 || warnAt > pauseAt {
		return nil, errors.New("connector: queue warning threshold must be positive and no higher than the pause threshold")
	}
	// Capacity IS the pause threshold: a full channel is a blocked offer is a
	// feed that has stopped being read. There is no second mechanism to keep
	// in agreement with this one.
	return &Queue{ids: make(chan int64, pauseAt), warnAt: warnAt}, nil
}

// Offer hands an event id to admission, waiting for room when the backlog is
// at the pause threshold. Waiting here is the pause: the caller is the feed's
// delivery path, and it is not reading the feed while it waits.
func (q *Queue) Offer(ctx context.Context, id int64) error {
	// Counted before it is sent. An id on its way into the queue is backlog
	// either way, and counting it first is what keeps the depth honest: a
	// take can only receive what a send has already put in, so its decrement
	// can never be applied before the increment it belongs to, and a crossing
	// can never be lost to the order two operations happen to take the lock in.
	q.applyDelta(1)
	select {
	case q.ids <- id:
		q.afterOp()
		return nil
	default:
	}

	if q.waiting.Add(1) == 1 && q.OnPause != nil {
		q.OnPause(q.Depth())
	}
	defer func() {
		if q.waiting.Add(-1) == 0 && q.OnResume != nil {
			q.OnResume(q.Depth())
		}
	}()

	select {
	case q.ids <- id:
		q.afterOp()
		return nil
	case <-ctx.Done():
		// It never went in, so it is not backlog.
		q.applyDelta(-1)
		return ctx.Err()
	}
}

// Take returns the next id, waiting for one until ctx ends.
//
// There is deliberately no Close. Shutdown is the context: a closed channel
// would turn every offer racing the close into a panic, and nothing here needs
// a drained-and-done signal that cancellation does not already give.
func (q *Queue) Take(ctx context.Context) (int64, error) {
	select {
	case id := <-q.ids:
		q.afterOp()
		q.applyDelta(-1)
		return id, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Depth is the number of ids waiting.
func (q *Queue) Depth() int {
	q.edges.Lock()
	defer q.edges.Unlock()
	return q.depth
}

// Paused reports whether an offer is currently waiting for room — which is to
// say whether the feed is being consumed.
func (q *Queue) Paused() bool { return q.waiting.Load() > 0 }

// afterOp is a test seam: it runs between a channel operation and whatever
// follows it, which is where a crossing used to be lost.
func (q *Queue) afterOp() {
	if q.afterChannelOp != nil {
		q.afterChannelOp()
	}
}

// applyDelta records what one operation does to the depth and fires the
// warning edges it crosses.
//
// The depth is a counter this method owns, not a later read of the channel's
// length. With a length read, an offer and a concurrent take could both
// observe the depth AFTER the take, and a queue that really crossed the
// threshold raised no warning at all. Every delta is applied under one lock
// and the crossing is derived from the depth that operation produced.
//
// The edges are edge-triggered, not level: a backlog that sits above the
// threshold for an hour is one warning, and the recovery is the other half of
// the pair, so a warning is never left standing after the thing it warned
// about went away.
func (q *Queue) applyDelta(delta int) {
	// The transition is decided under the lock; the callback runs after it, so
	// a callback may observe or use the queue without deadlocking against the
	// operation that raised it. Callbacks can therefore arrive out of order
	// across goroutines, but the state they report on never is.
	q.edges.Lock()
	q.depth += delta
	depth := q.depth
	switch {
	case depth >= q.warnAt && !q.warned:
		q.warned = true
		q.pending = append(q.pending, queueEdge{fire: q.OnWarn, depth: depth})
	case depth < q.warnAt && q.warned:
		q.warned = false
		q.pending = append(q.pending, queueEdge{fire: q.OnRecover, depth: depth})
	}
	if q.delivering {
		q.edges.Unlock()
		return
	}
	q.delivering = true
	for len(q.pending) > 0 {
		edge := q.pending[0]
		q.pending = q.pending[1:]
		q.edges.Unlock()
		if edge.fire != nil {
			edge.fire(edge.depth)
		}
		q.edges.Lock()
	}
	q.delivering = false
	q.edges.Unlock()
}

type queueEdge struct {
	fire  func(int)
	depth int
}
