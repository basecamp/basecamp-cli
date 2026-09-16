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
		return intake.snapshot != nil && intake.snapshot[48699913]
	}, 2*time.Second, 10*time.Millisecond, "the first successful read becomes the baseline")

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}
