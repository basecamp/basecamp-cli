package connector

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueueWarnsOnceOnTheWayUpAndRecoversOnTheWayDown(t *testing.T) {
	queue, err := NewQueue(2, 4)
	require.NoError(t, err)

	var warnings, recoveries int
	queue.OnWarn = func(int) { warnings++ }
	queue.OnRecover = func(int) { recoveries++ }

	ctx := context.Background()
	require.NoError(t, queue.Offer(ctx, 1))
	assert.Zero(t, warnings)

	require.NoError(t, queue.Offer(ctx, 2))
	require.NoError(t, queue.Offer(ctx, 3))
	assert.Equal(t, 1, warnings, "a backlog that sits above the threshold is one warning, not one per event")

	_, err = queue.Take(ctx)
	require.NoError(t, err)
	_, err = queue.Take(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, recoveries, "a warning must not be left standing after the backlog drained")
}

// At the pause threshold intake stops consuming the feed. Because the feed
// saves a position only after its page's events were accepted, and intake has
// stopped accepting them, the checkpoint stops moving too — which is what makes
// the pause safe rather than a way to lose the backlog.
func TestQueuePausesTheCallerAtTheThreshold(t *testing.T) {
	queue, err := NewQueue(1, 2)
	require.NoError(t, err)

	paused := make(chan int, 1)
	queue.OnPause = func(depth int) { paused <- depth }

	ctx := context.Background()
	require.NoError(t, queue.Offer(ctx, 1))
	require.NoError(t, queue.Offer(ctx, 2))
	assert.Equal(t, 2, queue.Depth())

	done := make(chan error, 1)
	go func() { done <- queue.Offer(ctx, 3) }()

	select {
	case depth := <-paused:
		// Two: what the queue holds. The offer waiting to go in is not
		// counted, so the depth reported is never above the threshold.
		assert.Equal(t, 2, depth)
	case <-time.After(2 * time.Second):
		t.Fatal("the third offer should have waited for room")
	}
	assert.True(t, queue.Paused())

	select {
	case err := <-done:
		t.Fatalf("the third offer returned while the queue was full: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	_, err = queue.Take(ctx)
	require.NoError(t, err)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("the offer should have resumed once there was room")
	}
}

func TestQueueOfferHonoursCancellation(t *testing.T) {
	queue, err := NewQueue(1, 1)
	require.NoError(t, err)
	require.NoError(t, queue.Offer(context.Background(), 1))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- queue.Offer(ctx, 2) }()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("a paused offer must unblock on shutdown")
	}
}

func TestQueueRefusesNonsenseThresholds(t *testing.T) {
	_, err := NewQueue(10, 5)
	assert.Error(t, err)
	_, err = NewQueue(0, 5)
	assert.Error(t, err)
	_, err = NewQueue(5, 0)
	assert.Error(t, err)
}

// F2: a crossing is never lost. An offer and a concurrent take could both read
// the depth after the take, so a queue that really crossed the threshold
// raised no warning at all.
func TestACrossingIsNotLostToAConcurrentTake(t *testing.T) {
	queue, err := NewQueue(1, 4)
	require.NoError(t, err)
	var warns, recovers int
	var mu sync.Mutex
	queue.OnWarn = func(int) { mu.Lock(); warns++; mu.Unlock() }
	queue.OnRecover = func(int) { mu.Lock(); recovers++; mu.Unlock() }

	ctx := context.Background()
	taken := make(chan int64, 1)
	// The take lands between the offer's send and the transition it produces.
	// A flag, not a Once: the take runs this hook too, and must not wait on
	// the offer that is waiting for it.
	var interleaved atomic.Bool
	queue.afterChannelOp = func() {
		if !interleaved.CompareAndSwap(false, true) {
			return
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			id, err := queue.Take(ctx)
			assert.NoError(t, err)
			taken <- id
		}()
		<-done
	}

	require.NoError(t, queue.Offer(ctx, 42))
	assert.Equal(t, int64(42), <-taken)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, warns, "the queue crossed the threshold, so it warned")
	assert.Equal(t, 1, recovers, "and the take brought it back")
	assert.Zero(t, queue.Depth())
}

// A callback belongs to whoever built the queue, and a panic in one is a
// reporting bug — not a reason to lose the feed. It is contained, and the
// queue's own state is correct afterwards because it was committed before the
// callback ran.
func TestAPanickingBacklogCallbackLeavesTheQueueUsable(t *testing.T) {
	queue, err := NewQueue(1, 4)
	require.NoError(t, err)

	var warnings, recoveries int
	queue.OnWarn = func(int) { warnings++; panic("a warning callback panics") }
	queue.OnRecover = func(int) { recoveries++ }

	ctx := context.Background()
	require.NoError(t, queue.Offer(ctx, 1), "the callback's panic is not the offer's failure")
	assert.Equal(t, 1, warnings)

	// The drain is unlatched and the queue still works: a second id goes in,
	// both come out, and the recovery edge is delivered.
	require.NoError(t, queue.Offer(ctx, 2))
	assert.Equal(t, 2, queue.Depth())

	first, err := queue.Take(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), first)
	second, err := queue.Take(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), second)
	assert.Equal(t, 0, queue.Depth())
	assert.Equal(t, 1, recoveries, "the drain should still be delivering edges")
}

// An operator watching the queue must never be told the feed resumed while it
// is paused. The transitions are delivered in the order they happened, even
// when an earlier callback is slower than a later transition.
func TestPauseAndResumeAreObservedInTheOrderTheyHappened(t *testing.T) {
	queue, err := NewQueue(1, 1)
	require.NoError(t, err)

	var mu sync.Mutex
	var seen []string
	record := func(what string) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, what)
	}
	observed := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
	resumeStarted, holdResume := make(chan struct{}), make(chan struct{})
	queue.OnPause = func(int) { record("paused") }
	var firstResume sync.Once
	queue.OnResume = func(int) {
		held := false
		firstResume.Do(func() { held = true; close(resumeStarted) })
		if held {
			<-holdResume
		}
		record("resumed")
	}

	ctx := context.Background()
	require.NoError(t, queue.Offer(ctx, 1))

	var waiters sync.WaitGroup
	waiters.Add(1)
	go func() {
		defer waiters.Done()
		assert.NoError(t, queue.Offer(ctx, 2)) // waits for room
	}()
	require.Eventually(t, queue.Paused, time.Second, time.Millisecond)

	first, err := queue.Take(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), first)
	waiters.Wait()
	// The take that drains the queue to its resume band delivers the resume,
	// and the callback holds it there.
	drained := make(chan int64, 1)
	go func() {
		id, err := queue.Take(ctx)
		assert.NoError(t, err)
		drained <- id
	}()
	<-resumeStarted // the resume has begun and is not finished

	require.NoError(t, queue.Offer(ctx, 3))
	waiters.Add(1)
	go func() {
		defer waiters.Done()
		assert.NoError(t, queue.Offer(ctx, 4)) // pauses again, mid-resume
	}()
	require.Eventually(t, queue.Paused, time.Second, time.Millisecond)

	close(holdResume)
	assert.Equal(t, int64(2), <-drained)
	require.Eventually(t, func() bool { return len(observed()) == 3 }, time.Second, time.Millisecond)
	assert.Equal(t, []string{"paused", "resumed", "paused"}, observed(),
		"a resume that started first must not be reported after the pause that followed it")

	third, err := queue.Take(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), third)
	waiters.Wait()
	fourth, err := queue.Take(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(4), fourth)
	assert.False(t, queue.Paused())
}

// A pause callback that panics must leave nothing behind: the id it was
// waiting for still goes in, the depth is the queue's real depth, the pause
// lifts, and the next crossing is reported.
func TestAPanickingPauseCallbackLeavesNoPhantomBacklog(t *testing.T) {
	queue, err := NewQueue(1, 1)
	require.NoError(t, err)

	var logs bytes.Buffer
	queue.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	var pauses, resumes atomic.Int32
	queue.OnPause = func(int) {
		if pauses.Add(1) == 1 {
			panic("a pause callback panics")
		}
	}
	queue.OnResume = func(int) { resumes.Add(1) }

	ctx := context.Background()
	require.NoError(t, queue.Offer(ctx, 1))

	waiting := make(chan error, 1)
	go func() { waiting <- queue.Offer(ctx, 2) }()
	require.Eventually(t, queue.Paused, time.Second, time.Millisecond)

	first, err := queue.Take(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), first)
	require.NoError(t, <-waiting, "the offer the panicking callback announced still completes")

	assert.Equal(t, 1, queue.Depth(), "no phantom item")
	assert.False(t, queue.Paused(), "no phantom pause")
	assert.Contains(t, logs.String(), "a backlog callback panicked")

	// And the next crossing is still reported, on both sides. The resume is
	// reported once the queue drains to its band, here empty.
	second, err := queue.Take(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), second)
	assert.Equal(t, int32(1), resumes.Load())
	require.NoError(t, queue.Offer(ctx, 3))
	blocked := make(chan error, 1)
	go func() { blocked <- queue.Offer(ctx, 4) }()
	require.Eventually(t, queue.Paused, time.Second, time.Millisecond)
	_, err = queue.Take(ctx)
	require.NoError(t, err)
	require.NoError(t, <-blocked)
	_, err = queue.Take(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(2), pauses.Load())
	assert.Equal(t, int32(2), resumes.Load())
	assert.Zero(t, queue.Depth())
}

// The report is the queue's, not the callback's: a callback that panics is
// contained after the edge has already been reported.
func TestAPanickingCallbackDoesNotSilenceTheReport(t *testing.T) {
	queue, err := NewQueue(1, 4)
	require.NoError(t, err)
	var logs bytes.Buffer
	queue.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	queue.OnWarn = func(int) { panic("a warning callback panics") }

	require.NoError(t, queue.Offer(context.Background(), 1))

	assert.Contains(t, logs.String(), "the backlog reached its warning depth")
	assert.Contains(t, logs.String(), "depth=1 warn_at=1 recover_at=0 pause_at=4")
	assert.Contains(t, logs.String(), "a backlog callback panicked")
}

// A backlog that hovers at a threshold is one report each way, not one per
// event. At capacity each take lets the waiting offer in and the next offer
// waits again; that is still one pause, and the resume is reported only once
// the backlog has drained to half the pause depth. The warning clears the
// same way, at half the warning depth.
func TestABacklogHoveringAtItsThresholdsIsReportedOnceEachWay(t *testing.T) {
	queue, err := NewQueue(4, 8)
	require.NoError(t, err)
	var logs safeBuffer
	queue.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	var mu sync.Mutex
	var seen []string
	record := func(what string) func(int) {
		return func(int) { mu.Lock(); seen = append(seen, what); mu.Unlock() }
	}
	queue.OnWarn = record("warn")
	queue.OnRecover = record("recover")
	queue.OnPause = record("pause")
	queue.OnResume = record("resume")
	observed := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
	ctx := context.Background()
	take := func() {
		t.Helper()
		_, err := queue.Take(ctx)
		require.NoError(t, err)
	}

	// Around the warning depth: 3, 4, 3, 4, ...
	for i := range 4 {
		require.NoError(t, queue.Offer(ctx, int64(i)))
	}
	for i := range 20 {
		take()
		require.NoError(t, queue.Offer(ctx, int64(100+i)))
	}
	assert.Equal(t, []string{"warn"}, observed())

	// Around the pause depth: full, one waits, one taken, the next waits.
	for i := range 4 {
		require.NoError(t, queue.Offer(ctx, int64(200+i)))
	}
	for i := range 20 {
		waiting := make(chan error, 1)
		go func() { waiting <- queue.Offer(ctx, int64(300+i)) }()
		require.Eventually(t, queue.Paused, time.Second, time.Millisecond)
		take()
		require.NoError(t, <-waiting)
	}
	assert.Equal(t, []string{"warn", "pause"}, observed())

	// Draining: nothing until half the pause depth, then the resume; nothing
	// more until half the warning depth, then the recovery.
	for range 3 {
		take()
	}
	assert.Equal(t, []string{"warn", "pause"}, observed(), "depth 5 is above the resume band")
	take()
	assert.Equal(t, []string{"warn", "pause", "resume"}, observed())
	take()
	assert.Equal(t, []string{"warn", "pause", "resume"}, observed(), "depth 3 is above the recovery band")
	take()
	assert.Equal(t, []string{"warn", "pause", "resume", "recover"}, observed())
	assert.Equal(t, 2, queue.Depth())

	text := logs.String()
	assert.Equal(t, 1, strings.Count(text, "the backlog reached its warning depth"))
	assert.Equal(t, 1, strings.Count(text, "the backlog reached its pause depth"))
	assert.Equal(t, 1, strings.Count(text, "the backlog drained to half its pause depth"))
	assert.Equal(t, 1, strings.Count(text, "the backlog fell back below its warning depth"))
	assert.Contains(t, text, "depth=8 pause_at=8 resume_at=4", "the pause reports what the queue holds")
	assert.Contains(t, text, "depth=4 pause_at=8 resume_at=4")
	assert.Contains(t, text, "depth=2 warn_at=4 recover_at=2")
}
