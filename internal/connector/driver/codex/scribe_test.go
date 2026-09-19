//go:build unix

package codex

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// A refusal's update is emitted after its write has landed, never before.
// That is the driver's contract — the recorder is called before the update
// is emitted — and it is the half of it the scribe keeps exactly rather than
// narrows. Emitting at hand-off would publish a refusal the ledger has not
// taken.
func TestTheUpdateComesAfterTheWrite(t *testing.T) {
	var mu sync.Mutex
	var order []string
	writing := make(chan struct{})
	release := make(chan struct{})
	s := newScribe(
		func(driver.Refusal) {
			mu.Lock()
			order = append(order, "write")
			mu.Unlock()
			close(writing)
			<-release
		},
		func(driver.Update) {
			mu.Lock()
			order = append(order, "update")
			mu.Unlock()
		},
	)
	s.hand(pending{refusal: driver.Refusal{ToolCallID: "one"}})

	// The write has begun and the update must not have been emitted.
	<-writing
	mu.Lock()
	assert.Equal(t, []string{"write"}, order, "the update waits for the write")
	mu.Unlock()

	close(release)
	s.close()
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"write", "update"}, order)
}

// Handing a refusal over never loses it, whatever the scribe is doing —
// including after it has drained and gone, when whoever read it writes it.
func TestARefusalHandedOverAfterTheScribeHasGoneIsStillWritten(t *testing.T) {
	var mu sync.Mutex
	var written []string
	s := newScribe(
		func(r driver.Refusal) {
			mu.Lock()
			written = append(written, r.ToolCallID)
			mu.Unlock()
		},
		func(driver.Update) {},
	)
	s.hand(pending{refusal: driver.Refusal{ToolCallID: "before"}})
	s.close()
	s.hand(pending{refusal: driver.Refusal{ToolCallID: "after"}})

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"before", "after"}, written, "nothing is dropped on either side of the close")
}

// The queue holds a live worker back rather than dropping anything: past the
// mark, handing over waits for room. Dropping would be the loss this exists
// to end.
func TestAFullQueueHoldsTheReaderBack(t *testing.T) {
	release := make(chan struct{})
	first := make(chan struct{})
	var once sync.Once
	s := newScribe(
		func(driver.Refusal) {
			once.Do(func() { close(first) })
			<-release
		},
		func(driver.Update) {},
	)
	// One is taken and stuck in its write; the rest fill the queue to the
	// mark, and the next has to wait.
	s.hand(pending{refusal: driver.Refusal{ToolCallID: "stuck"}})
	<-first
	for i := range refusalMark {
		s.hand(pending{refusal: driver.Refusal{ToolCallID: string(rune('a' + i%26))}})
	}

	waited := make(chan struct{})
	go func() {
		defer close(waited)
		s.hand(pending{refusal: driver.Refusal{ToolCallID: "over"}})
	}()
	select {
	case <-waited:
		t.Fatal("the queue took more than its mark without holding the reader back")
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	<-waited
	s.close()
}

// Once the reader has been asked to stop it is never held back, because the
// clock on its drain is running: a reader waiting for room would spend that
// clock and abandon the pipe with the worker's output still in it.
func TestAReaderAskedToStopIsNeverHeldBack(t *testing.T) {
	release := make(chan struct{})
	first := make(chan struct{})
	var once sync.Once
	s := newScribe(
		func(driver.Refusal) {
			once.Do(func() { close(first) })
			<-release
		},
		func(driver.Update) {},
	)
	s.hand(pending{refusal: driver.Refusal{ToolCallID: "stuck"}})
	<-first
	s.noWaiting()

	handed := make(chan struct{})
	go func() {
		defer close(handed)
		for range refusalMark * 3 {
			s.hand(pending{refusal: driver.Refusal{ToolCallID: "more"}})
		}
	}()
	select {
	case <-handed:
	case <-time.After(10 * time.Second):
		t.Fatal("a reader that has been asked to stop was held back handing refusals over")
	}

	close(release)
	s.close()
}

// Everything handed over is written by the time the scribe is closed, which
// is what the session leans on: it closes the scribe before the updates,
// because that is where the dispatcher settles what the ledger would not
// take.
func TestClosingTheScribeWritesEverythingHandedOver(t *testing.T) {
	var mu sync.Mutex
	count := 0
	s := newScribe(
		func(driver.Refusal) {
			mu.Lock()
			count++
			mu.Unlock()
		},
		func(driver.Update) {},
	)
	const handed = 500
	for range handed {
		s.hand(pending{refusal: driver.Refusal{ToolCallID: "one"}})
	}
	s.close()

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, handed, count, "the close is what makes the ledger whole")
}

// The close waits for a late write that has already begun. A refusal read
// by an ending that is not the reader's can arrive after the scribe has
// drained and gone; whoever read it writes it, and the close either waits
// for that write or has not started — never decides the ledger is whole
// while one is running.
func TestClosingWaitsForALateWriteAlreadyBegun(t *testing.T) {
	var mu sync.Mutex
	var done []string
	writing := make(chan struct{})
	release := make(chan struct{})
	s := newScribe(
		func(r driver.Refusal) {
			if r.ToolCallID == "late" {
				close(writing)
				<-release
			}
			mu.Lock()
			done = append(done, r.ToolCallID)
			mu.Unlock()
		},
		func(driver.Update) {},
	)
	s.close() // the scribe has drained and gone

	handed := make(chan struct{})
	go func() {
		defer close(handed)
		s.hand(pending{refusal: driver.Refusal{ToolCallID: "late"}})
	}()
	<-writing // the late write has begun

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		s.close()
	}()
	select {
	case <-closed:
		t.Fatal("the close decided the ledger was whole while a late write was running")
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	<-closed
	<-handed
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"late"}, done, "and the write it waited for did happen")
}

// A drain that hears more refusals than it can hold tells the reader to stop
// reading, rather than growing without limit. Nothing that was read is lost;
// what was not read is abandoned, which is what the drain's clock was about
// to do anyway.
func TestADrainThatFillsTheQueueTellsTheReaderToStop(t *testing.T) {
	release := make(chan struct{})
	first := make(chan struct{})
	var once sync.Once
	s := newScribe(
		func(driver.Refusal) {
			once.Do(func() { close(first) })
			<-release
		},
		func(driver.Update) {},
	)
	s.hand(pending{refusal: driver.Refusal{ToolCallID: "stuck"}})
	<-first
	s.noWaiting()

	require.False(t, s.enough(), "nothing has been heard yet")
	for range refusalCap {
		s.hand(pending{refusal: driver.Refusal{ToolCallID: "more"}})
	}
	assert.True(t, s.enough(), "the reader is told to stop once the queue is full")

	close(release)
	s.close()
}

// The recorder is never called concurrently with itself, which driver.go
// promises an implementation it may rely on. A late write runs on its
// reader's goroutine rather than the scribe's, and two endings that are not
// the reader's can make one each, so the promise needs a lock rather than a
// single goroutine to keep it.
func TestTheRecorderIsNeverCalledTwiceAtOnce(t *testing.T) {
	var mu sync.Mutex
	inside, peak := 0, 0
	held := make(chan struct{})
	var once sync.Once
	s := newScribe(
		func(driver.Refusal) {
			mu.Lock()
			inside++
			peak = max(peak, inside)
			mu.Unlock()
			once.Do(func() { close(held) })
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			inside--
			mu.Unlock()
		},
		func(driver.Update) {},
	)
	s.close() // every write from here is a late one, on its caller's goroutine

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.hand(pending{refusal: driver.Refusal{ToolCallID: string(rune('a' + i))}})
		}()
	}
	<-held
	wg.Wait()
	s.close()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, peak, "the recorder saw one call at a time, and %d at once", peak)
}
