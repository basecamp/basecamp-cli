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
//
// refusalCap is where the growing stops. A drain bounded only by its clock
// is not bounded in memory: two seconds of reading from a descendant that
// refuses as fast as it can write is as much as the machine will take. At
// the cap the reader stops taking more FROM THE PIPE, and goes on parsing
// what it has already taken — a scanner holds a bufferful that the pipe no
// longer does, and abandoning that would drop lines nobody had seen.
const (
	refusalMark = 256
	refusalCap  = 4096
)

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
	heard    bool // the queue reached its cap during a drain
	// late counts the writes being made on a caller's own goroutine because
	// the scribe had already gone. It lives under the same lock that
	// publishes finished, so admitting one and deciding everything is
	// written are a single decision rather than two that can cross.
	late int
	// writes counts the writes the scribe itself has taken off the queue
	// and not yet finished, so a drain can tell an empty queue from a
	// finished one.
	writes int
	quiet  *sync.Cond
	// writing is held across every call to record, wherever it is made
	// from. The recorder is promised it is never called concurrently with
	// itself (driver.go), and a late write runs on its reader's goroutine
	// rather than the scribe's, so the promise needs a lock rather than a
	// single goroutine to keep it.
	writing sync.Mutex
	done    chan struct{}
}

func newScribe(record func(driver.Refusal), emit func(driver.Update)) *scribe {
	s := &scribe{record: record, emit: emit, done: make(chan struct{})}
	s.room = sync.NewCond(&s.mu)
	s.work = sync.NewCond(&s.mu)
	s.quiet = sync.NewCond(&s.mu)
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
		// It is written here rather than lost, and counted under the same
		// lock that published finished — so a close either waits for this
		// one or has not begun, never decides the ledger is whole while it
		// is starting.
		s.late++
		s.mu.Unlock()
		s.writeNow(p)
		s.mu.Lock()
		s.late--
		s.quiet.Broadcast()
		s.mu.Unlock()
		return
	}
	s.queue = append(s.queue, p)
	if s.nowait && len(s.queue) >= refusalCap {
		// A drain that has heard as much as it can hold. Nothing read is
		// lost; the reader is told to stop reading, which is what its own
		// clock was about to do.
		s.heard = true
	}
	s.work.Signal()
	s.mu.Unlock()
}

// enough reports that a drain filled the queue to its cap, so the reader
// should stop reading. Nothing that was read is dropped.
func (s *scribe) enough() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heard
}

// noWaiting says the reader has been asked to stop, so it must not be held
// up handing anything over: from here the queue grows rather than blocks.
func (s *scribe) noWaiting() {
	s.mu.Lock()
	s.nowait = true
	s.room.Broadcast()
	s.mu.Unlock()
}

// drain waits for everything handed over so far to be written, without
// saying there is no more. A turn's result is settled after this, so a
// refusal read in that turn is in the ledger before the prompt that made it
// comes back — the ordering callers had when the write was on the reader.
//
// It is called from a turn's ending, which runs away from the reader, so
// waiting here holds up no reading.
func (s *scribe) drain() {
	s.mu.Lock()
	for (len(s.queue) > 0 || s.writes > 0) && !s.finished {
		s.quiet.Wait()
	}
	s.mu.Unlock()
	// A late write is on its caller's own goroutine and counted the same
	// way; this waits for those too.
	s.mu.Lock()
	for s.late > 0 {
		s.quiet.Wait()
	}
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
	// # What this barrier promises
	//
	// When it returns, every refusal handed over so far has been written:
	// the ones the scribe took, and the ones a caller wrote itself because
	// the scribe had already gone. Neither is dropped and neither races
	// this, because admitting a late write and counting it down happen
	// under the lock this waits on.
	//
	// It promises nothing about a refusal handed over AFTER it returns. One
	// can be: an ending that is not the reader's may read the worker's last
	// word later still. That refusal is written too, by whoever read it, on
	// that goroutine — it is simply not waited for here, in the same window
	// where an update emitted then is dropped rather than sent.
	s.mu.Lock()
	for s.late > 0 {
		s.quiet.Wait()
	}
	s.mu.Unlock()
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
			s.quiet.Broadcast()
			s.mu.Unlock()
			return
		}
		next := s.queue[0]
		s.queue = s.queue[1:]
		s.writes++
		s.room.Signal()
		s.mu.Unlock()

		// Outside the queue's lock: the write is the slow thing, and the
		// reader goes on reading while it happens.
		s.writing.Lock()
		s.record(next.refusal)
		s.writing.Unlock()
		// The update comes after the write, which is the contract's own
		// order: a refusal is recorded before the update for it is emitted.
		s.emit(next.update)
		s.mu.Lock()
		s.writes--
		s.quiet.Broadcast()
		s.mu.Unlock()
	}
}

// writeNow writes a refusal on the caller's own goroutine, for one read
// after the scribe has gone. Two of these can be in flight at once — two
// endings that are not the reader's — so they take the same lock the scribe
// writes under.
func (s *scribe) writeNow(p pending) {
	s.writing.Lock()
	s.record(p.refusal)
	s.writing.Unlock()
	s.emit(p.update)
}
