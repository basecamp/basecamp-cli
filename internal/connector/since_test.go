package connector

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// A3: an explicit --since stays the entry until a checkpoint supersedes it. A
// connection that ends before its first page is saved — here, a reconnect for
// a project the live subscription does not hold, raised mid-page — must not
// leave the next connection with no position, entering at the present and
// skipping the history the operator asked for.
func TestAnExplicitSinceSurvivesAReconnectBeforeTheFirstCheckpoint(t *testing.T) {
	ledger := newTestLedger(t)
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{
		SinceEventID:       17099838000,
		Membership:         &flakyMembership{buckets: []int64{48699913}},
		MembershipInterval: time.Hour,
	})
	minter.ScriptTicket(ticket())
	minter.ScriptTicket(ticket())

	unlisted := testEvent(17099838001)
	unlisted.BucketID = 777
	// The first event raises the reconnect; the second is then refused
	// delivery, so the page is never checkpointed.
	polls.ScriptPage(eventfeed.PollPage{Events: []eventfeed.Event{unlisted, testEvent(17099838002)}, Position: "never-saved"})
	polls.ScriptPage(eventfeed.PollPage{Events: []eventfeed.Event{testEvent(17099838002)}, Position: "saved"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(ctx, t, intake)
	subscribedConn(t, transport)

	answered := 1
	require.Eventually(t, func() bool {
		if conns := transport.Conns(); len(conns) > answered {
			answerSubscription(conns[len(conns)-1])
			answered = len(conns)
		}
		return polls.CallCount() >= 2
	}, 5*time.Second, 10*time.Millisecond)

	assert.Equal(t, "17099838000", polls.Calls()[1].Cursor.Since,
		"nothing was checkpointed, so the reconnect still enters where the operator asked")

	require.Eventually(t, func() bool {
		_, ok, err := ledger.Get(ctx, 17099838002)
		return err == nil && ok
	}, 5*time.Second, 10*time.Millisecond, "no event after --since is skipped")

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}
