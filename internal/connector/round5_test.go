package connector

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// A3: the last poll-served id counts what the poll lane SERVED, not what it
// delivered. In steady state the poll copy of nearly every event is
// suppressed, because the live lane delivered it thirty seconds earlier;
// counting deliveries leaves the id at zero and every later re-entry falls
// back to a full replay — or, on a filter change, to the present.
func TestThePollServedIDCountsServedEventsTheLiveLaneAlreadyDelivered(t *testing.T) {
	ledger := newTestLedger(t)
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{RepairInterval: 50 * time.Millisecond})
	minter.ScriptTicket(ticket())
	polls.ScriptPage(eventfeed.PollPage{Position: "p1"})
	polls.ScriptPage(eventfeed.PollPage{Events: []eventfeed.Event{testEvent(17099838600)}, Position: "p2"})
	for range 20 {
		polls.ScriptPage(eventfeed.PollPage{Position: "p2"})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(ctx, t, intake)
	conn, identifier := subscribedConn(t, transport)

	require.Eventually(t, func() bool { return polls.CallCount() >= 1 }, 5*time.Second, time.Millisecond)
	conn.Serve(pushFrame(t, identifier, testEvent(17099838600)))
	require.Eventually(t, func() bool {
		_, ok, err := ledger.Get(ctx, 17099838600)
		return err == nil && ok
	}, 5*time.Second, time.Millisecond)

	require.Eventually(t, func() bool {
		served, err := ledger.LastPollServedID(ctx, intake.CheckpointKey())
		return err == nil && served == 17099838600
	}, 5*time.Second, 5*time.Millisecond, "the repair poll served the event; its delivery being suppressed does not unserve it")

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}

// A3: a filter change enters under the new digest at the old lineage's
// poll-served id — and, when the lineage has a position but no id, at the
// beginning of served history. Never at the present.
func TestAFilterChangeFromAPositionWithNoServedIDReplaysInsteadOfEnteringAtThePresent(t *testing.T) {
	ledger := newTestLedger(t)
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{
		Filters: eventfeed.Filters{Types: []string{"comment.created"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	old := intake.CheckpointKey()
	old.FilterKey = eventfeed.Filters{}.FilterKey()
	require.NoError(t, ledger.Save(ctx, old, "old-digest-position-from-empty-pages"))

	minter.ScriptTicket(ticket())
	polls.ScriptPage(eventfeed.PollPage{Position: "p1"})

	done := runInBackground(ctx, t, intake)
	subscribedConn(t, transport)

	require.Eventually(t, func() bool { return polls.CallCount() > 0 }, 5*time.Second, 10*time.Millisecond)
	first := polls.Calls()[0].Cursor
	assert.Equal(t, "0", first.Since, "the old filter set had read up to a position; entering at the present skips what followed it")

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}

// A gap met by a deliberate recovery replay is labeled as one, so status does
// not present a replay's expected 410 as a loss.
func TestAGapMetByARecoveryReplayIsLabeledAsOne(t *testing.T) {
	intake, ledger, _ := newTestIntake(t, nil, nil)
	intake.replaying = true

	assert.Equal(t, eventfeed.Accept, intake.handleSignal(eventfeed.FeedGap{
		EpochAfterID: 17099838487,
		ResumeURL:    "https://3.basecampapi.com/2914079/events.json?since=17099838487",
	}))
	gaps, err := ledger.Gaps(context.Background())
	require.NoError(t, err)
	require.Len(t, gaps, 1)
	assert.Contains(t, gaps[0].Note, "replay")
}
