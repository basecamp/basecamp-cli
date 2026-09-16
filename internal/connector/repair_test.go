package connector

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

type walkClock struct{ at time.Time }

func (c *walkClock) now() time.Time { return c.at }
func (c *walkClock) advance(d time.Duration) func(context.Context, time.Duration) error {
	return func(context.Context, time.Duration) error {
		c.at = c.at.Add(d)
		return nil
	}
}

func newTestWalker(t *testing.T, ledger *Ledger, polls eventfeed.PollSource, clock *walkClock) (*repairWalker, *[]int64) {
	t.Helper()
	var ingested []int64
	walker := &repairWalker{
		ledger: ledger,
		polls:  polls,
		now:    clock.now,
		log:    slog.New(slog.DiscardHandler),
		sleep:  clock.advance(time.Minute),
		ingest: func(ctx context.Context, event eventfeed.Event, lane Lane) error {
			ingested = append(ingested, event.ID)
			_, err := ledger.RecordSeen(ctx, event, lane)
			return err
		},
		interval: time.Minute,
	}
	return walker, &ingested
}

func TestRepairWalkEntersOneBelowTheLowestMissingID(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}

	loss, err := ledger.RecordLoss(ctx, []int64{17099838505, 17099838502}, clock.at, 10*time.Minute)
	require.NoError(t, err)

	polls := &scriptedPolls{pages: []eventfeed.PollPage{{
		Events:   []eventfeed.Event{testEvent(17099838502), testEvent(17099838505)},
		Position: "walk-1",
	}}}
	walker, ingested := newTestWalker(t, ledger, polls, clock)
	require.NoError(t, walker.reconcile(ctx, loss))

	require.NotEmpty(t, polls.cursors)
	assert.Equal(t, "17099838501", polls.cursors[0].Since,
		"the feed's since is exclusive, so the walk enters one below the lowest missing id")
	assert.Equal(t, []int64{17099838502, 17099838505}, *ingested)

	recovered, err := ledger.MissingIDs(ctx, loss.ID, LossRecovered)
	require.NoError(t, err)
	assert.Equal(t, []int64{17099838502, 17099838505}, recovered)
}

// A request crosses up to a thousand ledger rows and serves at most a hundred
// matches; the rows the filters excluded still advanced the cursor. Stopping at
// the first empty page abandons the repair one page short.
func TestRepairWalkFollowsNextThroughAnEmptyPage(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}

	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, clock.at, 10*time.Minute)
	require.NoError(t, err)

	polls := &scriptedPolls{pages: []eventfeed.PollPage{
		{Events: nil, Position: "walk-1", Next: "https://3.basecampapi.com/2914079/events.json?position=walk-1"},
		{Events: []eventfeed.Event{testEvent(17099838509)}, Position: "walk-2"},
	}}
	walker, ingested := newTestWalker(t, ledger, polls, clock)
	require.NoError(t, walker.reconcile(ctx, loss))

	assert.Equal(t, 2, polls.calls)
	assert.Equal(t, []int64{17099838509}, *ingested)
	assert.Equal(t, "https://3.basecampapi.com/2914079/events.json?position=walk-1", polls.cursors[1].PageURL)
}

// A missing `next` means the walk reached its frozen head, not that history
// ended: a page cut short by the safety horizon withholds the link on purpose.
func TestRepairWalkRepeatsAfterAMissingNext(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}

	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, clock.at, 10*time.Minute)
	require.NoError(t, err)

	polls := &scriptedPolls{pages: []eventfeed.PollPage{
		{Events: nil, Position: "walk-1"},
		{Events: nil, Position: "walk-2"},
		{Events: []eventfeed.Event{testEvent(17099838509)}, Position: "walk-3"},
	}}
	walker, ingested := newTestWalker(t, ledger, polls, clock)
	require.NoError(t, walker.reconcile(ctx, loss))

	assert.Equal(t, 3, polls.calls, "the walk repeats on the repair cadence rather than concluding")
	assert.Equal(t, []int64{17099838509}, *ingested)
	assert.Equal(t, "walk-2", polls.cursors[2].Position,
		"each repeat resumes from the walk's own cursor")
}

// The walk is seeded from a live id that ran far ahead of the poll lane. Its
// cursor belongs to the loss record and must never reach the feed's checkpoint.
func TestRepairCursorNeverReachesTheFeedCheckpoint(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	key := testKey()
	require.NoError(t, ledger.Save(ctx, key, "feed-position-before-the-overflow"))

	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, clock.at, 10*time.Minute)
	require.NoError(t, err)

	polls := &scriptedPolls{pages: []eventfeed.PollPage{{
		Events:   []eventfeed.Event{testEvent(17099838509)},
		Position: "repair-walk-position",
	}}}
	walker, _ := newTestWalker(t, ledger, polls, clock)
	require.NoError(t, walker.reconcile(ctx, loss))

	position, ok, err := ledger.Load(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "feed-position-before-the-overflow", position,
		"a checkpoint taken from the repair walk would skip everything inside the safety delay behind it")
}

func TestRepairWalkSavesItsOwnCursorOnTheLoss(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}

	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, clock.at, 10*time.Minute)
	require.NoError(t, err)

	polls := &scriptedPolls{pages: []eventfeed.PollPage{{Events: nil, Position: "repair-walk-position"}}}
	walker, _ := newTestWalker(t, ledger, polls, clock)
	// One pass only: stop the clock past the deadline so the walk gives up.
	walker.sleep = func(context.Context, time.Duration) error {
		clock.at = clock.at.Add(11 * time.Minute)
		return nil
	}
	require.NoError(t, walker.reconcile(ctx, loss))

	open, err := ledger.OpenLosses(ctx)
	require.NoError(t, err)
	assert.Empty(t, open)

	unrecovered, err := ledger.UnrecoveredIDs(ctx)
	require.NoError(t, err)
	assert.Equal(t, []int64{17099838509}, unrecovered)
}

func TestIdsStillMissingWhenTheWindowClosesAreUnrecovered(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}

	loss, err := ledger.RecordLoss(ctx, []int64{17099838509, 17099838510}, clock.at, 10*time.Minute)
	require.NoError(t, err)

	// The straggler arrives on the fourth repair poll; the other never does.
	polls := &scriptedPolls{pages: []eventfeed.PollPage{
		{Position: "w1"},
		{Position: "w2"},
		{Position: "w3"},
		{Events: []eventfeed.Event{testEvent(17099838510)}, Position: "w4"},
	}}
	walker, _ := newTestWalker(t, ledger, polls, clock)
	require.NoError(t, walker.reconcile(ctx, loss))

	assert.GreaterOrEqual(t, polls.calls, 10, "ten minutes at a sixty-second cadence")

	recovered, err := ledger.MissingIDs(ctx, loss.ID, LossRecovered)
	require.NoError(t, err)
	assert.Equal(t, []int64{17099838510}, recovered)

	unrecovered, err := ledger.UnrecoveredIDs(ctx)
	require.NoError(t, err)
	assert.Equal(t, []int64{17099838509}, unrecovered,
		"a late straggler is reported, never hidden")
}

// An unrecovered id the poll lane serves later is resolved by intake like any
// other event — the ledger is one dedupe, not two.
func TestAnUnrecoveredIDIsStillIngestedNormallyLater(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()

	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, time.Now(), time.Minute)
	require.NoError(t, err)
	_, err = ledger.CloseLoss(ctx, loss.ID, time.Now())
	require.NoError(t, err)

	fresh, err := ledger.RecordSeen(ctx, testEvent(17099838509), LanePoll)
	require.NoError(t, err)
	assert.True(t, fresh)

	record, ok, err := ledger.Get(ctx, 17099838509)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, StateSeen, record.State)
}

func TestRepairWalkBelowTheEpochEndsWithAGap(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}

	loss, err := ledger.RecordLoss(ctx, []int64{100, 101}, clock.at, 10*time.Minute)
	require.NoError(t, err)

	polls := &scriptedPolls{errs: []error{&eventfeed.PollError{
		Kind:         eventfeed.PollGone,
		EpochAfterID: 17099838487,
		ResumeURL:    "https://3.basecampapi.com/2914079/events.json?since=17099838487",
	}}}
	walker, _ := newTestWalker(t, ledger, polls, clock)
	require.NoError(t, walker.reconcile(ctx, loss))

	assert.Equal(t, 1, polls.calls, "no number of repeats will serve history below the epoch")

	gaps, err := ledger.Gaps(ctx)
	require.NoError(t, err)
	require.Len(t, gaps, 1)
	assert.Equal(t, GapEpoch, gaps[0].Class)

	unrecovered, err := ledger.UnrecoveredIDs(ctx)
	require.NoError(t, err)
	assert.Equal(t, []int64{100, 101}, unrecovered)
}

func TestATransientRepairPollIsRetriedNotGivenUpOn(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}

	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, clock.at, 10*time.Minute)
	require.NoError(t, err)

	polls := &scriptedPolls{
		errs:  []error{&eventfeed.PollError{Kind: eventfeed.PollThrottled, RetryAfter: time.Second}, nil},
		pages: []eventfeed.PollPage{{}, {Events: []eventfeed.Event{testEvent(17099838509)}, Position: "w2"}},
	}
	walker, ingested := newTestWalker(t, ledger, polls, clock)
	require.NoError(t, walker.reconcile(ctx, loss))

	assert.Equal(t, []int64{17099838509}, *ingested,
		"a throttled walk delays the repair; it does not condemn the ids")

	unrecovered, err := ledger.UnrecoveredIDs(ctx)
	require.NoError(t, err)
	assert.Empty(t, unrecovered)
}
