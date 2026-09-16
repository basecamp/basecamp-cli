package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// F2: an operator sees warning edges in the order the state took them.
// Delivered out of order, a drained queue can end on a warning.
func TestQueueWarningEdgesArriveInTheOrderTheyHappened(t *testing.T) {
	for range 30 {
		queue, err := NewQueue(1, 64)
		require.NoError(t, err)
		var mu sync.Mutex
		var seen []string
		queue.OnWarn = func(int) { mu.Lock(); seen = append(seen, "warn"); mu.Unlock() }
		queue.OnRecover = func(int) { mu.Lock(); seen = append(seen, "recover"); mu.Unlock() }

		ctx := context.Background()
		var wg sync.WaitGroup
		for g := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range 300 {
					require.NoError(t, queue.Offer(ctx, int64(g*1000+i)))
					_, err := queue.Take(ctx)
					require.NoError(t, err)
				}
			}()
		}
		wg.Wait()

		mu.Lock()
		for i, edge := range seen {
			want := "warn"
			if i%2 == 1 {
				want = "recover"
			}
			require.Equal(t, want, edge, "edge %d of %v", i, len(seen))
		}
		if len(seen) > 0 {
			require.Equal(t, "recover", seen[len(seen)-1], "a drained queue's last word is a recovery")
		}
		mu.Unlock()
	}
}

// C2: a loss that ended with ids behind the epoch is not reported as fully
// reconciled.
func TestAPartlyUnrecoveredLossIsNotReportedAsFullyReconciled(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	loss, err := ledger.RecordLoss(ctx, []int64{100, 200}, clock.at, 10*time.Minute)
	require.NoError(t, err)

	polls := &scriptedPolls{
		errs: []error{&eventfeed.PollError{Kind: eventfeed.PollGone, EpochAfterID: 150,
			ResumeURL: "https://3.basecampapi.com/2914079/events.json?since=150"}},
		pages: []eventfeed.PollPage{{}, {Events: []eventfeed.Event{testEvent(200)}, Position: "p"}},
	}
	var logs bytes.Buffer
	walker, _ := newTestWalker(t, ledger, polls, clock)
	walker.log = slog.New(slog.NewTextHandler(&logs, nil))
	require.NoError(t, walker.reconcile(ctx, loss))

	assert.NotContains(t, logs.String(), "fully reconciled", "id 100 is behind the epoch and was never recovered")
}

// The pointer line keeps the event's timestamp at the precision the feed gave.
func TestPointerLineKeepsTheTimestampsPrecision(t *testing.T) {
	var pointers bytes.Buffer
	intake, _, _ := newTestIntake(t, nil, &pointers)
	event := testEvent(1)
	event.CreatedAt = time.Date(2026, 9, 16, 10, 0, 0, 120_000_000, time.UTC)
	require.NoError(t, intake.ingest(context.Background(), event, LanePoll))

	var pointer Pointer
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(pointers.String())), &pointer))
	parsed, err := time.Parse(time.RFC3339Nano, pointer.CreatedAt)
	require.NoError(t, err)
	assert.True(t, parsed.Equal(event.CreatedAt), "got %s", pointer.CreatedAt)
}
