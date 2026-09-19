package codex

import (
	"sync"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// A refusal is read on one goroutine and written on another
//
// Writing a refusal to the ledger is allowed ten seconds and used to run on
// the goroutine reading the worker's output. That made the reading
// unboundable: the read end of the pipe is released when the session stops
// waiting for it, and any bound on that — a clock, a count of bytes — is
// either spent by a ledger write, taking the pipe away with the worker's
// next refusal still in it, or not spent by a descendant writing without
// end, which then holds the session open for as long as it lives.
//
// So the reader only reads, and the scribe only writes. The reader's promise
// to driver.Worker.ReadingDone — that it does nothing slow between reads —
// is what lets the drain bound be a clock and mean what it says.
//
// # What the scribe owes the contract
//
// A refusal is recorded before the update for it is emitted (driver.go). The
// scribe emits it, after its write lands, so that ordering holds exactly
// rather than being narrowed. What is traded is the other half — that a
// refusal is never held only in a session's memory — for the window between
// the reader handing one over and the scribe writing it. A connector crash
// there loses it; a worker exiting, a turn cut short, and a session closing
// do not, because the queue is drained before the session's updates close,
// which is where the dispatcher settles what the ledger would not take.
//
// # When the queue is full
//
// It blocks the reader, and it never drops. Dropping would be the loss this
// exists to end, wearing a new hat.
//
// Blocking is only safe before the reader has been asked to stop, and there
// it is right: the pipe fills, the worker blocks writing, and an agent
// producing refusals faster than the ledger takes them is slowed to the
// ledger's pace. That is what a slow ledger already did to this driver when
// both ran on one goroutine, so it is not a new way to fail.
//
// After the asking it would be fatal — the drain's clock would run while the
// reader sat on a full queue, and the pipe would be abandoned with the
// worker's output still in it — so after the asking the reader never waits.
// It appends, and the queue grows past its mark. That growth is bounded by
// what one drain can produce, which the drain budget bounds in time, and it
// is a burst at the end of a session rather than a leak.
//
// The mark is not derived from the pipe's size or the scanner's buffer.
// Neither is a bound the code has: a worker inherits the pipe descriptor and
// may enlarge it up to the host's pipe-max-size, and the scanner is allowed
// to grow to 64 MiB. It is simply how many refusals are worth holding before
// a live worker is told to slow down.
const refusalMark = 256

// pending is a refusal the reader has read and the scribe has not yet
// written, with the update that is emitted once it has.
type pending struct {
	refusal driver.Refusal
	update  driver.Update
}

// scribe writes refusals to the ledger, one at a time, off the reader.
type scribe struct {
	record func(driver.Refusal)
	emit   func(driver.Update)

	mu       sync.Mutex
	room     *sync.Cond
	work     *sync.Cond
	queue    []pending
	nowait   bool // the reader has been asked to stop: never make it wait
	closing  bool
	finished bool // the scribe has drained and gone
	done     chan struct{}
	// inflight counts the writes being made on a caller's own goroutine,
	// after the scribe has gone.
	inflight sync.WaitGroup
}

func newScribe(record func(driver.Refusal), emit func(driver.Update)) *scribe {
	s := &scribe{record: record, emit: emit, done: make(chan struct{})}
	s.room = sync.NewCond(&s.mu)
	s.work = sync.NewCond(&s.mu)
	go s.run()
	return s
}

// hand gives the scribe a refusal to write. It waits for room while the
// reader can afford to, and never once it cannot.
func (s *scribe) hand(p pending) {
	s.mu.Lock()
	for len(s.queue) >= refusalMark && !s.nowait && !s.closing && !s.finished {
		s.room.Wait()
	}
	if s.finished {
		// The tail: a refusal read from the worker's last word by an ending
		// that is not the reader's, after the scribe has drained and gone.
		// Written here rather than lost. The count is taken under the same
		// lock that publishes finished, so a close cannot decide everything
		// is written while this one is starting.
		s.inflight.Add(1)
		s.mu.Unlock()
		defer s.inflight.Done()
		s.writeNow(p)
		return
	}
	s.queue = append(s.queue, p)
	s.work.Signal()
	s.mu.Unlock()
}

// noWaiting says the reader has been asked to stop, so it must not be held
// up handing anything over: from here the queue grows rather than blocks.
func (s *scribe) noWaiting() {
	s.mu.Lock()
	s.nowait = true
	s.room.Broadcast()
	s.mu.Unlock()
}

// close says there is no more, and waits for what there is to be written.
// Whoever calls it promises the reader is through. It is idempotent.
func (s *scribe) close() {
	s.mu.Lock()
	if !s.closing {
		s.closing = true
		s.room.Broadcast()
		s.work.Broadcast()
	}
	s.mu.Unlock()
	<-s.done
	// A refusal handed over after the scribe had gone is written by whoever
	// read it, and this waits for those. One discovered after this returns
	// is still written, on its reader's goroutine — the same window in which
	// an update emitted then is dropped.
	s.inflight.Wait()
}

func (s *scribe) run() {
	defer close(s.done)
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.closing {
			s.work.Wait()
		}
		if len(s.queue) == 0 {
			// Published under the lock a hand takes, so no refusal can be
			// queued to a scribe that has decided it is done.
			s.finished = true
			s.room.Broadcast()
			s.mu.Unlock()
			return
		}
		next := s.queue[0]
		s.queue = s.queue[1:]
		s.room.Signal()
		s.mu.Unlock()

		// Outside the lock: the write is the slow thing, and the reader goes
		// on reading while it happens.
		s.record(next.refusal)
		// The update comes after the write, which is the contract's own
		// order: a refusal is recorded before the update for it is emitted.
		s.emit(next.update)
	}
}

// writeNow writes a refusal on the caller's own goroutine, for one read
// after the scribe has gone.
func (s *scribe) writeNow(p pending) {
	s.record(p.refusal)
	s.emit(p.update)
}
