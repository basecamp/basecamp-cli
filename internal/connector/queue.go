package connector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/basecamp/basecamp-cli/internal/richtext"
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
// visible depth rather than one pipeline, so a busy dispatcher is absorbed up
// to the pause threshold instead of being felt on the socket — and so the
// place where work is piling up is the place the depth is showing. At that
// threshold the offer waits: the backlog is bounded, and the pause is the
// backpressure reaching the feed, reported rather than hidden.
type Queue struct {
	ids    chan int64
	warnAt int
	// recoverAt and resumeAt are the depths at or below which a warning and a
	// pause are reported as over: half of the depth that raised each. An edge
	// that cleared one item below where it was raised would be reported twice
	// for every event of a backlog that hovers at its threshold, and a pause
	// at capacity would be reported twice for every event the feed delivers,
	// since each take lets one waiting offer in and the next offer waits
	// again. Half is a band that a hovering backlog cannot cross by itself,
	// it needs no clock, and both bands are zero or more, so a drained queue
	// always ends recovered and resumed.
	recoverAt int
	resumeAt  int

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
	// waiter to resume while another — perhaps the feed — still waits. It is
	// held under edges with the depth, because its callbacks go through the
	// same ordered drain: told out of order, a resume that started first but
	// finished last leaves the operator reading "resumed" while the feed is
	// paused.
	waiting int
	// paused is the reported pause, not the live one. It is raised when an
	// offer first waits for room and lowered only once no offer waits and the
	// depth has drained to resumeAt. Paused reports the live state.
	paused bool

	// OnWarn fires when the depth reaches the warning threshold, and
	// OnRecover when it has drained to half of it. Both are optional, and
	// neither is needed for the operator to hear of it: every edge is
	// reported on the logger first.
	OnWarn    func(depth int)
	OnRecover func(depth int)
	// OnPause fires when an offer first waits for room, with the depth the
	// queue holds, not counting the waiting offer. OnResume fires once no
	// offer waits and the depth has drained to half the pause threshold. In
	// between, the feed is read only as fast as admission makes room.
	OnPause  func(depth int)
	OnResume func(depth int)
	// log reports every edge and any callback that panicked. Held under
	// edges: a queue can be adopted by intake while another component is
	// already using it, and a field written by one and read by the other is a
	// race.
	log *slog.Logger
}

// SetLogger sets where the backlog's edges, and a callback that panicked, are
// reported. Safe to call while the queue is in use.
func (q *Queue) SetLogger(log *slog.Logger) {
	q.edges.Lock()
	defer q.edges.Unlock()
	q.log = log
}

// adoptLogger sets log only if nobody has set one, as one step: a check and a
// set taken apart would let two adopters both find it unset.
func (q *Queue) adoptLogger(log *slog.Logger) {
	q.edges.Lock()
	defer q.edges.Unlock()
	if q.log == nil {
		q.log = log
	}
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
	return &Queue{
		ids:       make(chan int64, pauseAt),
		warnAt:    warnAt,
		recoverAt: warnAt / 2,
		resumeAt:  pauseAt / 2,
	}, nil
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
	q.stage(1)
	// An offer that leaves without sending must take its count back with it,
	// on every way out — the ordinary cancellation, and an unwinding this
	// function did not choose. A count left behind is a queue that reports an
	// item nobody can take and, at the threshold, a pause nothing can lift.
	settled := false
	settle := func(sent bool) {
		if settled {
			return
		}
		settled = true
		if !sent {
			q.stage(-1)
			q.deliver()
		}
	}
	defer func() { settle(false) }()

	select {
	case q.ids <- id:
		settle(true)
		q.afterOp()
		q.deliver()
		return nil
	default:
	}

	q.stageWait(1)
	// Registered BEFORE the pause is delivered: the wait is recorded, and
	// what ends it must already be in place when anything else runs.
	defer func() {
		q.stageWait(-1)
		q.deliver()
	}()
	q.deliver()

	select {
	case q.ids <- id:
		settle(true)
		q.afterOp()
		q.deliver()
		return nil
	case <-ctx.Done():
		// It never went in, so it is not backlog. Taken back here rather than
		// left to the defer, so the resume that follows reports the depth
		// without it.
		settle(false)
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
		q.stage(-1)
		q.deliver()
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
func (q *Queue) Paused() bool {
	q.edges.Lock()
	defer q.edges.Unlock()
	return q.waiting > 0
}

// afterOp is a test seam: it runs between a channel operation and whatever
// follows it, which is where a crossing used to be lost.
func (q *Queue) afterOp() {
	if q.afterChannelOp != nil {
		q.afterChannelOp()
	}
}

// stage records what one operation does to the depth and decides the edges it
// crosses, without delivering them.
//
// The depth is a counter this method owns, not a later read of the channel's
// length. With a length read, an offer and a concurrent take could both
// observe the depth AFTER the take, and a queue that really crossed the
// threshold raised no warning at all. Every delta is applied under one lock
// and the crossing is derived from the depth that operation produced.
//
// Deciding and delivering are separate because an offer counts its id before
// it sends it: a callback delivered there could call Take and wait for an id
// the offer has not sent yet, and neither would ever finish. So the edges wait
// for deliver, which every operation calls once its channel work is done.
func (q *Queue) stage(delta int) {
	q.edges.Lock()
	defer q.edges.Unlock()
	q.depth += delta
	q.decide()
}

// stageWait records an offer starting or finishing its wait for room and the
// pause edge that crosses, without delivering it.
//
// Pause and resume are transitions of the same state as the warning edges, so
// they are decided under the same lock and delivered by the same drain, in the
// order they were decided. Fired from the waiting goroutines themselves they
// could interleave: a resume delayed in its callback could land after a later
// pause, and an observer would be left believing the feed is being read when
// it is not.
func (q *Queue) stageWait(delta int) {
	q.edges.Lock()
	defer q.edges.Unlock()
	q.waiting += delta
	q.decide()
}

// decide stages every edge the current depth and waiters cross. It is called
// holding edges, after any change to either.
//
// A raised edge clears only at its band, not one item below where it was
// raised, so a backlog that hovers at a threshold is one report each way.
// The edges up are decided before the edges down. One change can clear both
// the pause and the warning where their bands meet, and then the resume is
// told before the recovery, the reverse of the order they were raised in.
func (q *Queue) decide() {
	if !q.warned && q.depth >= q.warnAt {
		q.warned = true
		q.pending = append(q.pending, queueEdge{kind: edgeWarn, fire: q.OnWarn, depth: q.depth})
	}
	if !q.paused && q.waiting > 0 {
		q.paused = true
		// The held depth: a waiting offer has counted its id but cannot put
		// it in, so it is not part of what the queue holds.
		q.pending = append(q.pending, queueEdge{kind: edgePause, fire: q.OnPause, depth: q.depth - q.waiting})
	}
	if q.paused && q.waiting == 0 && q.depth <= q.resumeAt {
		q.paused = false
		q.pending = append(q.pending, queueEdge{kind: edgeResume, fire: q.OnResume, depth: q.depth})
	}
	if q.warned && q.depth <= q.recoverAt {
		q.warned = false
		q.pending = append(q.pending, queueEdge{kind: edgeRecover, fire: q.OnRecover, depth: q.depth})
	}
}

// deliver hands the staged edges to their callbacks, in the order they were
// decided, one goroutine at a time and with the lock released around each: a
// callback may use the queue, and its own edge is delivered by the drain
// already running.
func (q *Queue) deliver() {
	q.edges.Lock()
	if q.delivering {
		q.edges.Unlock()
		return
	}
	q.delivering = true
	// Cleared with a defer: a callback that panics and is recovered above
	// would otherwise leave the drain latched and every later edge
	// undelivered. The loop below holds the lock whenever it is not inside a
	// callback, so this runs holding it on every path.
	defer func() {
		q.delivering = false
		q.edges.Unlock()
	}()
	for len(q.pending) > 0 {
		edge := q.pending[0]
		q.pending = q.pending[1:]
		q.edges.Unlock()
		// The lock is retaken by a defer, not after the call: fire contains a
		// panic, but a future change to it must not be able to leave this
		// loop, or the cleanup above, holding nothing.
		func() {
			defer q.edges.Lock()
			q.fire(edge)
		}()
	}
}

// fire reports one edge and runs its callback, with the lock released and a
// panic contained.
//
// The report comes first and does not depend on the callback: a caller that
// set none, as production does, still has the backlog reported rather than
// hidden, and one that panics cannot take the report down with it.
//
// The callbacks belong to whoever built the queue, and the goroutine they run
// on is the feed's delivery path or a worker taking work off it. A callback
// that panics is a reporting bug; it is not a reason to lose the connection,
// and it must not leave the queue part-way through an update. The state the
// callback is told about was committed before it ran, so what it does cannot
// change it.
func (q *Queue) fire(edge queueEdge) {
	log := q.logger()
	q.report(log, edge)
	defer func() {
		if p := recover(); p != nil {
			log.Error("a backlog callback panicked; the queue carried on without it",
				"panic", richtext.SanitizeSingleLine(fmt.Sprint(p)))
		}
	}()
	if edge.fire != nil {
		edge.fire(edge.depth)
	}
}

// backlogNextStep is what a warning tells the operator to look at.
const backlogNextStep = "The usual cause is slow or failing Basecamp reads during admission; basecamp connect status shows the backlog of records by state."

// report tells the operator about one edge, with the depth it was decided at,
// the threshold it crossed and the depth at which it clears. The depth of a
// pause is what the queue holds; the offer waiting for room is not counted.
func (q *Queue) report(log *slog.Logger, edge queueEdge) {
	pauseAt := cap(q.ids)
	switch edge.kind {
	case edgeWarn:
		log.Warn("the backlog reached its warning depth; admission is falling behind the feed. "+backlogNextStep,
			"depth", edge.depth, "warn_at", q.warnAt, "recover_at", q.recoverAt, "pause_at", pauseAt)
	case edgeRecover:
		log.Info("the backlog fell back below its warning depth",
			"depth", edge.depth, "warn_at", q.warnAt, "recover_at", q.recoverAt)
	case edgePause:
		log.Warn("the backlog reached its pause depth; the next event waits for room, and the feed is read only as fast as admission makes room, so the backlog cannot outgrow memory. "+backlogNextStep,
			"depth", edge.depth, "pause_at", pauseAt, "resume_at", q.resumeAt)
	case edgeResume:
		log.Info("the backlog drained to half its pause depth; the feed is read at full speed again",
			"depth", edge.depth, "pause_at", pauseAt, "resume_at", q.resumeAt)
	}
}

// logger is read under edges, so it must never be called holding them. Its one
// caller, fire, runs with the lock released.
func (q *Queue) logger() *slog.Logger {
	q.edges.Lock()
	defer q.edges.Unlock()
	if q.log != nil {
		return q.log
	}
	return slog.New(slog.DiscardHandler)
}

type queueEdge struct {
	kind  edgeKind
	fire  func(int)
	depth int
}

// edgeKind is which of the backlog's transitions an edge is.
type edgeKind int

const (
	edgeWarn edgeKind = iota
	edgeRecover
	edgePause
	edgeResume
)
