package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func shortFilingWait(t *testing.T) {
	t.Helper()
	saved := cardFilingWait
	cardFilingWait.first = time.Millisecond
	cardFilingWait.max = 2 * time.Millisecond
	cardFilingWait.total = 50 * time.Millisecond
	t.Cleanup(func() { cardFilingWait = saved })
}

func TestPollUntilStopsWhenDone(t *testing.T) {
	shortFilingWait(t)
	checks := 0
	done, err := pollUntil(context.Background(), func(context.Context) (bool, error) {
		checks++
		return checks == 3, nil
	})
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, 3, checks)
}

func TestPollUntilGivesUpAfterTotal(t *testing.T) {
	shortFilingWait(t)
	start := time.Now()
	done, err := pollUntil(context.Background(), func(context.Context) (bool, error) { return false, nil })
	require.NoError(t, err)
	assert.False(t, done)
	assert.Less(t, time.Since(start), time.Second)
}

func TestPollUntilHonoursCancellation(t *testing.T) {
	shortFilingWait(t)
	cardFilingWait.total = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	checks := 0
	done, err := pollUntil(ctx, func(context.Context) (bool, error) {
		checks++
		if checks == 2 {
			cancel()
		}
		return false, nil
	})
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, done)
	assert.Equal(t, 2, checks)
}
