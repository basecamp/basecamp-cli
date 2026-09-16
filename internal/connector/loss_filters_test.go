package connector

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// C3: a loss is repaired under the filter set it was recorded with. A
// connector restarted with different filters would otherwise walk the wrong
// lane and condemn events the original filters would have served.
func TestALossIsRepairedUnderTheFilterSetItWasRecordedWith(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	recorded := eventfeed.Filters{Types: []string{"comment.created"}, Buckets: []int64{48699913}}

	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, time.Now(), 10*time.Minute, recorded)
	require.NoError(t, err)
	assert.Equal(t, recorded.Types, loss.Filters.Types)

	// A later start under a different filter set.
	open, err := ledger.OpenLosses(ctx)
	require.NoError(t, err)
	require.Len(t, open, 1)
	require.Equal(t, recorded.Types, open[0].Filters.Types)
	require.Equal(t, recorded.Buckets, open[0].Filters.Buckets)

	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	polls := &scriptedPolls{pages: []eventfeed.PollPage{
		{Events: []eventfeed.Event{testEvent(17099838509)}, Position: "p"},
	}}
	walker, _ := newTestWalker(t, ledger, polls, clock)
	walker.filters = eventfeed.Filters{Types: []string{"card.created"}} // the connector's current set
	require.NoError(t, walker.reconcileLoss(ctx, open[0]))

	require.NotEmpty(t, polls.filters)
	assert.Equal(t, recorded.Types, polls.filters[0].Types,
		"the walk uses the loss's own filters, not the connector's current ones")

	recoveredIDs, err := ledger.MissingIDs(ctx, loss.ID, LossRecovered)
	require.NoError(t, err)
	assert.Equal(t, []int64{17099838509}, recoveredIDs)
}

// The whole-account feed records a loss with no filters at all, which is not
// the same as a loss from before filters were stored. Restarted with filters
// added, the repair must still walk the whole account.
func TestALossRecordedWithNoFiltersIsRepairedWithNoFilters(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	_, err := ledger.RecordLoss(ctx, []int64{17099838509}, time.Now(), 10*time.Minute, eventfeed.Filters{})
	require.NoError(t, err)

	open, err := ledger.OpenLosses(ctx)
	require.NoError(t, err)
	require.Len(t, open, 1)

	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	polls := &scriptedPolls{pages: []eventfeed.PollPage{
		{Events: []eventfeed.Event{testEvent(17099838509)}, Position: "p"},
	}}
	walker, _ := newTestWalker(t, ledger, polls, clock)
	walker.filters = eventfeed.Filters{Types: []string{"comment.created"}} // added since
	require.NoError(t, walker.reconcileLoss(ctx, open[0]))

	require.NotEmpty(t, polls.filters)
	assert.Empty(t, polls.filters[0].Types,
		"the loss was recorded on the whole account; a narrower lane never carried its events")
}

// A loss written before losses carried their filters has none recorded, and is
// walked under the connector's own.
func TestALegacyLossWalksUnderTheConnectorsFilters(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	_, err := ledger.RecordLoss(ctx, []int64{17099838509}, time.Now(), 10*time.Minute, eventfeed.Filters{})
	require.NoError(t, err)
	// What migration 2 leaves on a row written before it.
	_, err = ledger.db.ExecContext(ctx, `UPDATE losses SET filters = ''`)
	require.NoError(t, err)

	open, err := ledger.OpenLosses(ctx)
	require.NoError(t, err)
	require.Len(t, open, 1)
	assert.False(t, open[0].HasFilters)

	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	polls := &scriptedPolls{pages: []eventfeed.PollPage{
		{Events: []eventfeed.Event{testEvent(17099838509)}, Position: "p"},
	}}
	walker, _ := newTestWalker(t, ledger, polls, clock)
	walker.filters = eventfeed.Filters{Types: []string{"comment.created"}}
	require.NoError(t, walker.reconcileLoss(ctx, open[0]))

	require.NotEmpty(t, polls.filters)
	assert.Equal(t, []string{"comment.created"}, polls.filters[0].Types)
}
