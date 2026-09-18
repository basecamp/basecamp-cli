package connector

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// failingWorkspaces fails every Prepare with one error, so a test says what
// kind of failure the dispatcher is looking at and nothing else.
type failingWorkspaces struct{ err error }

func (w failingWorkspaces) Prepare(context.Context, string, int64) (string, error) {
	return "", w.err
}

func (w failingWorkspaces) Finish(context.Context, string, string) error { return nil }

// unusableRoute is what Worktrees.Prepare returns for a route that is not a
// repository.
func unusableRoute() error {
	return fmt.Errorf("connector: route %s is not in a git repository: %w", testRoute, ErrRouteUnusable)
}

// The defect: a route no working directory could be made in left every
// record admitted, backed the route off and told nobody. It is now a
// refusal, in the ledger and on the recording that asked.
func TestARouteNoWorkingDirectoryCanBeMadeInIsRefused(t *testing.T) {
	ctx := context.Background()
	fake := newFakeDriver()
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.Workspaces = failingWorkspaces{err: unusableRoute()}
	})
	h.ledger.SetHooks(LifecycleHooks(h.ledger, LifecycleOptions{}))
	admitRouted(t, h.ledger, 1, adapterBucketID, "recording:10304028989", testRoute)
	h.run(t)

	require.Eventually(t, func() bool {
		r, ok, err := h.ledger.Get(ctx, 1)
		return err == nil && ok && r.State == StateBlocked
	}, 5*time.Second, 10*time.Millisecond, "the record was never refused: "+unsettled(ctx, h.ledger))

	r, _, err := h.ledger.Get(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, ReasonRouteUnusable, r.Reason, "connect status counts it under a reason a person can act on")

	in := obIntent(t, h.ledger, refusedStartKey(1))
	assert.Equal(t, IntentHoldingReply, in.Kind)
	assert.Equal(t, Destination{BucketID: adapterBucketID, Kind: MessageComment, RecordingID: obReplyRecording}, in.Destination)
	text := MessageText(in.Body)
	assert.Contains(t, text, "is not a git repository with a commit")
	assert.Contains(t, text, "basecamp connect redispatch 1")
}

// The other direction: a Prepare that failed for a reason a retry can fix
// leaves the record where it is, to be tried again.
func TestARouteThatFailedOnceIsStillRetried(t *testing.T) {
	ctx := context.Background()
	fake := newFakeDriver()
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.Workspaces = failingWorkspaces{err: errors.New("connector: route " + testRoute + " is not in a git repository: git rev-parse: exit status 128")}
	})
	h.ledger.SetHooks(LifecycleHooks(h.ledger, LifecycleOptions{}))
	admitRouted(t, h.ledger, 1, adapterBucketID, "recording:10304028989", testRoute)
	h.run(t)

	assert.Never(t, func() bool {
		r, ok, err := h.ledger.Get(ctx, 1)
		return err == nil && ok && r.State == StateBlocked
	}, time.Second, 20*time.Millisecond, "a transient failure is not a refusal")

	for _, in := range obIntents(t, h.ledger) {
		assert.NotEqual(t, refusedStartKey(1), in.Key, "nothing was said, because nothing has been settled")
	}
}

// The reply is posted. It stands down only when the record leaves the state
// it answers for, which is the rule the no-route holding reply already had.
func TestTheRefusalReplyIsPostedAndStandsDownOnRedispatch(t *testing.T) {
	t.Run("posted while the record is still refused", func(t *testing.T) {
		ledger, clock := obLedger(t)
		obAdmit(t, ledger, 1, "recording:10304028989")
		ctx := context.Background()
		require.NoError(t, ledger.RefuseStart(ctx, 1, ReasonRouteUnusable))

		basecamp := newFakeBasecamp(clock.Now)
		require.NoError(t, obOutbox(t, ledger, basecamp).Flush(ctx))
		assert.Equal(t, IntentSent, obIntent(t, ledger, refusedStartKey(1)).State)
	})

	t.Run("stood down once a person has redispatched it", func(t *testing.T) {
		ledger, clock := obLedger(t)
		obAdmit(t, ledger, 1, "recording:10304028989")
		// The directory is made a repository and the record is decided again.
		next := admittedVerdict(1, getRecord(t, ledger, 1).Revision+1, "recording:10304028989")
		ctx := context.Background()
		require.NoError(t, ledger.RefuseStart(ctx, 1, ReasonRouteUnusable))
		_, err := ledger.Admission().Commit(ctx, next)
		require.NoError(t, err)
		obLaunch(t, ledger, 1)

		basecamp := newFakeBasecamp(clock.Now)
		require.NoError(t, obOutbox(t, ledger, basecamp).Flush(ctx))
		got := obIntent(t, ledger, refusedStartKey(1))
		assert.Equal(t, IntentCanceled, got.State)
		assert.Equal(t, "no longer called for", got.Note)
	})
}

// An event told there is no route is owed the second answer too: the route
// arrived, and the directory it named is not one a worktree can be made in.
func TestAnEventToldThereIsNoRouteIsToldAgainWhenTheRouteIsUnusable(t *testing.T) {
	ledger, _ := obLedger(t)
	seenRecord(t, ledger, 1)
	noRoute := obNoRouteVerdict(1, 0, admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: obReplyRecording})
	ctx := context.Background()
	_, err := ledger.Admission().Commit(ctx, noRoute)
	require.NoError(t, err)
	_, err = ledger.Admission().Commit(ctx, admittedVerdict(1, 1, "recording:10304028989"))
	require.NoError(t, err)
	require.NoError(t, ledger.RefuseStart(ctx, 1, ReasonRouteUnusable))

	held, refused := obIntents(t, ledger), ""
	for _, in := range held {
		if in.Key == refusedStartKey(1) {
			refused = in.Body
		}
	}
	require.NotEmpty(t, refused, "the refusal was never said")
	for _, in := range held {
		if in.Key == holdingKey(1) {
			assert.NotEqual(t, in.Body, refused, "two answers, not one overwritten by the other")
		}
	}
}

// Two holding replies can be pending on one event at once, and each is an
// answer to one reason only. Neither ever stands in for the other: a reply
// that says the project is not routed must not go out once it is, even
// though the record is blocked again for a different reason (Copilot on
// #753).
func TestEachHoldingReplyAnswersForItsOwnReasonOnly(t *testing.T) {
	reply := admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: obReplyRecording}

	t.Run("the no-route reply stands down once the project is routed", func(t *testing.T) {
		ledger, clock := obLedger(t)
		seenRecord(t, ledger, 1)
		ctx := context.Background()
		_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, reply))
		require.NoError(t, err)
		// The route arrives and is a directory no worktree can be made in,
		// all before the outbox got to the first reply.
		_, err = ledger.Admission().Commit(ctx, admittedVerdict(1, 1, "recording:10304028989"))
		require.NoError(t, err)
		require.NoError(t, ledger.RefuseStart(ctx, 1, ReasonRouteUnusable))

		basecamp := newFakeBasecamp(clock.Now)
		require.NoError(t, obOutbox(t, ledger, basecamp).Flush(ctx))
		stale := obIntent(t, ledger, holdingKey(1))
		assert.Equal(t, IntentCanceled, stale.State, "the project is routed now, so that answer is false")
		assert.Equal(t, "no longer called for", stale.Note)
		assert.Equal(t, IntentSent, obIntent(t, ledger, refusedStartKey(1)).State)
		assert.Equal(t, 1, basecamp.postCount(), "one answer, the true one")
	})

	t.Run("the refusal stands down once the route is taken away", func(t *testing.T) {
		ledger, clock := obLedger(t)
		obAdmit(t, ledger, 1, "recording:10304028989")
		ctx := context.Background()
		require.NoError(t, ledger.RefuseStart(ctx, 1, ReasonRouteUnusable))
		// The project is unrouted instead of the directory being fixed, so
		// the record waits on the other reason and the refusal is stale.
		_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, getRecord(t, ledger, 1).Revision, reply))
		require.NoError(t, err)

		basecamp := newFakeBasecamp(clock.Now)
		require.NoError(t, obOutbox(t, ledger, basecamp).Flush(ctx))
		assert.Equal(t, IntentCanceled, obIntent(t, ledger, refusedStartKey(1)).State)
		assert.Equal(t, IntentSent, obIntent(t, ledger, holdingKey(1)).State)
		assert.Equal(t, 1, basecamp.postCount())
	})
}

// A record that moved on since the dispatcher read it is left where it is.
func TestRefuseStartLeavesARecordThatMovedOn(t *testing.T) {
	ledger, _ := obLedger(t)
	obAdmit(t, ledger, 1, "recording:10304028989")
	obLaunch(t, ledger, 1)

	require.Error(t, ledger.RefuseStart(context.Background(), 1, ReasonRouteUnusable))
	assert.Equal(t, StateDispatched, getRecord(t, ledger, 1).State)
}
