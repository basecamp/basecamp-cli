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
