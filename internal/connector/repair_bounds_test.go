package connector

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// nextPolls serves empty pages whose next URL is chosen by pick, and gives up
// after limit calls so a walk that never stops fails the test instead of
// hanging it.
type nextPolls struct {
	mu    sync.Mutex
	calls int
	limit int
	pick  func(call int) string
}

func (n *nextPolls) Poll(context.Context, eventfeed.Cursor, eventfeed.Filters) (eventfeed.PollPage, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls++
	if n.calls > n.limit {
		return eventfeed.PollPage{}, &eventfeed.PollError{Kind: eventfeed.PollUnrecoverable}
	}
	return eventfeed.PollPage{Position: "pos-" + strconv.Itoa(n.calls), Next: n.pick(n.calls)}, nil
}

const walkBase = "https://3.basecampapi.com/2914079/events.json?position="

func onePassWalker(t *testing.T, polls eventfeed.PollSource) (*repairWalker, *Ledger, Loss) {
	t.Helper()
	ledger := newTestLedger(t)
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	loss, err := ledger.RecordLoss(context.Background(), []int64{17099838509}, clock.at, 10*time.Minute)
	require.NoError(t, err)
	walker, _ := newTestWalker(t, ledger, polls, clock)
	return walker, ledger, loss
}

// C3: a next that points at itself ends the pass instead of spinning.
func TestARepairWalkStopsOnASelfReferentialNext(t *testing.T) {
	polls := &nextPolls{limit: 300, pick: func(int) string { return walkBase + "same" }}
	walker, _, loss := onePassWalker(t, polls)

	_, err := walker.walk(context.Background(), &loss)
	require.NoError(t, err)
	assert.LessOrEqual(t, polls.calls, 3, "a next already walked this pass is a cycle")
}

// C3: an A→B→A cycle ends the pass too.
func TestARepairWalkStopsOnACycleOfNextURLs(t *testing.T) {
	polls := &nextPolls{limit: 300, pick: func(call int) string {
		if call%2 == 1 {
			return walkBase + "A"
		}
		return walkBase + "B"
	}}
	walker, _, loss := onePassWalker(t, polls)

	_, err := walker.walk(context.Background(), &loss)
	require.NoError(t, err)
	assert.LessOrEqual(t, polls.calls, 4)
}

// C3: distinct positions forever evade cycle detection, so a pass is also
// capped in pages, and returns the saved cursor so the next pass resumes.
func TestARepairPassIsCappedInPages(t *testing.T) {
	polls := &nextPolls{limit: 300, pick: func(call int) string { return walkBase + "distinct-" + strconv.Itoa(call) }}
	walker, ledger, loss := onePassWalker(t, polls)
	walker.maxPages = 20

	cursor, err := walker.walk(context.Background(), &loss)
	require.NoError(t, err)
	assert.Equal(t, 20, polls.calls)
	assert.Equal(t, "pos-20", cursor, "the next pass resumes from the last saved page")

	open, err := ledger.OpenLosses(context.Background())
	require.NoError(t, err)
	require.Len(t, open, 1)
	assert.Equal(t, "pos-20", open[0].RepairCursor)
}

// cancelingPolls cancels the walk's context on its first call, as a shutdown
// arriving mid-walk would, and answers with the context's error.
type cancelingPolls struct {
	cancel context.CancelFunc
	calls  int
}

func (c *cancelingPolls) Poll(ctx context.Context, _ eventfeed.Cursor, _ eventfeed.Filters) (eventfeed.PollPage, error) {
	c.calls++
	c.cancel()
	return eventfeed.PollPage{}, ctx.Err()
}

// C4: cancellation is not the walk failing. A shutdown mid-walk leaves the
// loss open, with nothing condemned, and the next start completes it.
func TestACanceledRepairWalkLeavesTheLossOpenForTheNextStart(t *testing.T) {
	for name, age := range map[string]time.Duration{
		"inside the window": 0,
		"on the final pass": 24 * time.Hour,
	} {
		t.Run(name, func(t *testing.T) {
			ledger := newTestLedger(t)
			clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
			loss, err := ledger.RecordLoss(context.Background(), []int64{17099838509}, clock.at.Add(-age), 10*time.Minute)
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			walker, _ := newTestWalker(t, ledger, &cancelingPolls{cancel: cancel}, clock)
			err = walker.reconcile(ctx, loss)
			assert.ErrorIs(t, err, context.Canceled)

			open, err := ledger.OpenLosses(context.Background())
			require.NoError(t, err)
			require.Len(t, open, 1, "a shutdown mid-walk is a delay, not a verdict")
			unrecovered, err := ledger.UnrecoveredIDs(context.Background())
			require.NoError(t, err)
			assert.Empty(t, unrecovered)

			// The next start completes it.
			restart, _ := newTestWalker(t, ledger, &scriptedPolls{pages: []eventfeed.PollPage{
				{Events: []eventfeed.Event{testEvent(17099838509)}, Position: "after"},
			}}, clock)
			require.NoError(t, restart.reconcile(context.Background(), open[0]))
			open, err = ledger.OpenLosses(context.Background())
			require.NoError(t, err)
			assert.Empty(t, open)
			recovered, err := ledger.MissingIDs(context.Background(), loss.ID, LossRecovered)
			require.NoError(t, err)
			assert.Equal(t, []int64{17099838509}, recovered)
		})
	}
}
