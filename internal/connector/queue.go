package connector

import (
	"context"
	"errors"
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

	warned atomic.Bool
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
	select {
	case q.ids <- id:
		q.noteDepth()
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
		q.noteDepth()
		return nil
	case <-ctx.Done():
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
		q.noteDepth()
		return id, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Depth is the number of ids waiting.
func (q *Queue) Depth() int { return len(q.ids) }

// Paused reports whether an offer is currently waiting for room — which is to
// say whether the feed is being consumed.
func (q *Queue) Paused() bool { return q.waiting.Load() > 0 }

// noteDepth fires the warning edges. It is edge-triggered, not level: a
// backlog that sits above the threshold for an hour is one warning, and the
// recovery is the other half of the pair, so a warning is never left standing
// after the thing it warned about went away.
func (q *Queue) noteDepth() {
	depth := q.Depth()
	switch {
	case depth >= q.warnAt && q.warned.CompareAndSwap(false, true):
		if q.OnWarn != nil {
			q.OnWarn(depth)
		}
	case depth < q.warnAt && q.warned.CompareAndSwap(true, false):
		if q.OnRecover != nil {
			q.OnRecover(depth)
		}
	}
}
