package connector

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// failingFirstMembership fails its first read and serves buckets afterwards.
type failingFirstMembership struct {
	mu      sync.Mutex
	calls   atomic.Int32
	buckets []int64
}

func (m *failingFirstMembership) Buckets(context.Context) ([]int64, error) {
	if m.calls.Add(1) == 1 {
		return nil, errors.New("projects listing unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buckets, nil
}

// E3: with the read at subscribe failed, the live subscription's buckets are
// unknown, not "everything". An event from a project the poll lane serves must
// still reconnect the live lane.
func TestAFailedFirstMembershipReadStillReconnectsForANewProject(t *testing.T) {
	ledger := newTestLedger(t)
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{
		Membership:         &failingFirstMembership{buckets: []int64{48699913}},
		MembershipInterval: time.Hour,
	})
	intake.membershipRetry = time.Hour
	minter.ScriptTicket(ticket())
	minter.ScriptTicket(ticket())
	granted := testEvent(17099838600)
	granted.BucketID = 777
	polls.ScriptPage(eventfeed.PollPage{Events: []eventfeed.Event{granted}, Position: "p1"})
	polls.ScriptPage(eventfeed.PollPage{Position: "p1"})

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
		return len(transport.Dials()) >= 2
	}, 5*time.Second, 10*time.Millisecond, "a project the subscription may not hold reconnects the live lane")

	// Learned once, it costs one reconnect, not one per event.
	time.Sleep(100 * time.Millisecond)
	assert.LessOrEqual(t, len(transport.Dials()), 2)

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}

// E3: a failed read is retried on a backoff, not left for a full membership
// interval with no baseline to compare against.
func TestAFailedFirstMembershipReadIsRetriedSoon(t *testing.T) {
	ledger := newTestLedger(t)
	membership := &failingFirstMembership{buckets: []int64{48699913}}
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{
		Membership:         membership,
		MembershipInterval: time.Hour,
	})
	intake.membershipRetry = 20 * time.Millisecond
	minter.ScriptTicket(ticket())
	polls.ScriptPage(eventfeed.PollPage{Position: "p1"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(ctx, t, intake)
	subscribedConn(t, transport)

	require.Eventually(t, func() bool { return membership.calls.Load() >= 2 }, 2*time.Second, 10*time.Millisecond,
		"the read is retried within its backoff, not after an hour")
	require.Eventually(t, func() bool {
		intake.mu.Lock()
		defer intake.mu.Unlock()
		return intake.listed != nil && intake.listed[48699913]
	}, 2*time.Second, 10*time.Millisecond, "the first successful read becomes the baseline")

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}

// E2/E3: while the project lister is failing, an unlisted project still costs
// one reconnect, not one per event. The buckets learned from arriving events
// are what the connector knows when it has no listing at all.
func TestAFailingListerStillCostsOneReconnectPerProject(t *testing.T) {
	ledger := newTestLedger(t)
	always := &alwaysFailingMembership{}
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{
		Membership:         always,
		MembershipInterval: time.Hour,
	})
	intake.membershipRetry = time.Hour
	for range 10 {
		minter.ScriptTicket(ticket())
	}
	var events []eventfeed.Event
	for id := int64(600); id < 605; id++ {
		e := testEvent(id)
		e.BucketID = 777
		events = append(events, e)
	}
	for range 10 {
		polls.ScriptPage(eventfeed.PollPage{Events: events, Position: "p"})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(ctx, t, intake)
	subscribedConn(t, transport)

	answered := 1
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if conns := transport.Conns(); len(conns) > answered {
			answerSubscription(conns[len(conns)-1])
			answered = len(conns)
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.LessOrEqual(t, len(transport.Dials()), 2, "five events from one project are one reconnect, lister or no lister")

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}

type alwaysFailingMembership struct{}

func (alwaysFailingMembership) Buckets(context.Context) ([]int64, error) {
	return nil, errors.New("projects listing unavailable")
}

// A bucket learned from an arriving event is provisional. The lister is the
// trust boundary: once it lists projects and does not name that one, the
// connector stops holding it, and the next event from it is unknown again.
func TestALearnedProjectIsRevokedWhenTheListerStopsNamingIt(t *testing.T) {
	intake, _, _ := newTestIntake(t, nil, nil)
	intake.opts.Membership = &flakyMembership{buckets: []int64{1}}

	require.False(t, intake.adoptListing([]int64{1}), "the first listing is a baseline")
	intake.noteBucket(777)
	intake.mu.Lock()
	learned := intake.learned[777]
	intake.mu.Unlock()
	require.True(t, learned, "an event proved the project visible")

	// A later listing that does not name it takes it back.
	intake.adoptListing([]int64{1})
	intake.mu.Lock()
	stillHeld := intake.learned[777] || intake.listed[777]
	intake.mu.Unlock()
	assert.False(t, stillHeld, "the lister no longer names it, so the connector no longer holds it")

	// And its next event is unknown again, not silently trusted. Relearning
	// is rate-limited per bucket, so the clock has to move past the
	// membership interval for the next event to count as new.
	intake.reconnectRequested()
	require.Empty(t, intake.reconnect)
	intake.opts.MembershipInterval = 0
	intake.noteBucket(777)
	assert.Len(t, intake.reconnect, 1, "a revoked project's next event is unknown")
}

// The listing the lister does name is what changes trigger a reconnect.
func TestOnlyTheListedSetDecidesAMembershipChange(t *testing.T) {
	intake, _, _ := newTestIntake(t, nil, nil)
	intake.opts.Membership = &flakyMembership{buckets: []int64{1, 2}}

	assert.False(t, intake.adoptListing([]int64{1, 2}))
	intake.noteBucket(9) // learned, never listed
	assert.False(t, intake.adoptListing([]int64{1, 2}), "a learned bucket the listing omits is not a change")
	assert.True(t, intake.adoptListing([]int64{1}), "a listed bucket dropping off is a change")
	assert.True(t, intake.adoptListing([]int64{1, 2}), "and its return is a change")
}
