package connector

import (
	"context"
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
