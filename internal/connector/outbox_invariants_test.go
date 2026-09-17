package connector

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// The outbox's invariants (outbox.go), one test or group each.

var obCommentReply = admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: obReplyRecording}

// Invariant 1: an intent and its transition commit or roll back together. An
// intent that cannot be written takes the verdict down with it.
func TestOutboxIntentRollsBackWithItsTransition(t *testing.T) {
	ledger, _ := obLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	_, err := ledger.db.ExecContext(ctx, `CREATE TRIGGER refuse_outbox BEFORE INSERT ON outbox BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	require.NoError(t, err)

	_, err = ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
	require.Error(t, err)
	assert.Equal(t, StateSeen, getRecord(t, ledger, 1).State, "the verdict rolled back with its intent")
	assert.Empty(t, obIntents(t, ledger))
}

// Invariant 1, the other direction: a transition that fails after its intent
// was written leaves no intent.
func TestOutboxIntentRollsBackWhenTheTransitionFails(t *testing.T) {
	ledger, _ := obLedger(t)
	ctx := context.Background()
	obAdmit(t, ledger, 1, "recording:10304028989")
	l := obLaunch(t, ledger, 1)

	hooks := LifecycleHooks(ledger, LifecycleOptions{})
	written := hooks.AttemptEnded
	hooks.AttemptEnded = func(ctx context.Context, tx Tx, s Settlement) error {
		if err := written(ctx, tx, s); err != nil {
			return err
		}
		return errWire
	}
	ledger.SetHooks(hooks)
	_, err := ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopLost})
	require.Error(t, err)

	for _, in := range obIntents(t, ledger) {
		assert.NotEqual(t, IntentCompletion, in.Kind, "the completion rolled back with the settlement")
	}
	live, err := ledger.LiveAttempts(ctx)
	require.NoError(t, err)
	assert.Len(t, live, 1)
}

// Invariant 2: one intent per thing answered for.
func TestOutboxOneIntentPerKey(t *testing.T) {
	ledger, _ := obLedger(t)
	ctx := context.Background()

	// A no_route record is decided again every time it is retried.
	seenRecord(t, ledger, 1)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
	require.NoError(t, err)
	_, err = ledger.Admission().Commit(ctx, obNoRouteVerdict(1, getRecord(t, ledger, 1).Revision, obCommentReply))
	require.NoError(t, err)

	obAdmit(t, ledger, 2, "recording:10304028989")
	l := obLaunch(t, ledger, 2)
	for range 2 {
		_, err := ledger.StillRunning(ctx, l.AttemptID)
		require.NoError(t, err)
	}

	count := map[IntentKind]int{}
	for _, in := range obIntents(t, ledger) {
		count[in.Kind]++
	}
	assert.Equal(t, 1, count[IntentHoldingReply], "one holding reply per event however often it is decided")
	assert.Equal(t, 1, count[IntentGuardAck])
	assert.Equal(t, 2, count[IntentStillRunning], "one per occurrence")
	obIntent(t, ledger, stillRunningKey(l.AttemptID, 1))
	obIntent(t, ledger, stillRunningKey(l.AttemptID, 2))
}

// sendingChecker is a poster that, when asked to post, reads the intent from a
// second ledger handle: what another process would find if this one died now.
type sendingChecker struct {
	*fakeBasecamp
	t      *testing.T
	other  *Ledger
	states []IntentState
}

func (s *sendingChecker) Post(ctx context.Context, dest Destination, body string) (int64, error) {
	intents, err := s.other.Intents(ctx, IntentFilter{})
	require.NoError(s.t, err)
	for _, in := range intents {
		if in.Body == body {
			s.states = append(s.states, in.State)
		}
	}
	return s.fakeBasecamp.Post(ctx, dest, body)
}

// Invariant 3: the sending row is durable before the request.
func TestOutboxNothingIsSentWithoutADurableSendingRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "connector.db")
	ledger, err := OpenLedger(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ledger.Close() })
	ledger.SetHooks(LifecycleHooks(ledger, LifecycleOptions{}))
	ctx := context.Background()

	seenRecord(t, ledger, 1)
	_, err = ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
	require.NoError(t, err)

	other, err := OpenLedger(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close() })
	poster := &sendingChecker{fakeBasecamp: newFakeBasecamp(time.Now), t: t, other: other}
	require.NoError(t, obOutbox(t, ledger, poster).Flush(ctx))
	require.Equal(t, []IntentState{IntentSending}, poster.states, "another handle saw the intent sending while the request was made")
	assert.Equal(t, IntentSent, obIntent(t, ledger, holdingKey(1)).State)
}

// Invariant 4: a request that fails leaves the intent sending, and nothing
// automatic posts it again — not the next flush, not a restart.
func TestOutboxNeverResendsASendingIntent(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
	require.NoError(t, err)

	basecamp := newFakeBasecamp(clock.Now)
	basecamp.beforePost = func(Destination, string) error { return errWire }
	ob := obOutbox(t, ledger, basecamp)
	require.NoError(t, ob.Flush(ctx))
	assert.Equal(t, 1, basecamp.postCount())
	assert.Equal(t, IntentSending, obIntent(t, ledger, holdingKey(1)).State)

	basecamp.beforePost = nil
	require.NoError(t, ob.Flush(ctx))
	clock.Advance(time.Hour)
	require.NoError(t, ob.Flush(ctx))
	restarted := obOutbox(t, ledger, basecamp)
	require.NoError(t, restarted.Recover(ctx))
	require.NoError(t, restarted.Flush(ctx))

	assert.Equal(t, 1, basecamp.postCount(), "one request, ever")
	in := obIntent(t, ledger, holdingKey(1))
	assert.Equal(t, IntentIndeterminate, in.State, "nothing matched, so a person decides")
	assert.Empty(t, basecamp.at(in.Destination))
}

// Invariant 4: a stale sending intent is reconciled by the running process
// too, never posted.
func TestOutboxReconcilesAStaleSendingIntentWithoutPosting(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
	require.NoError(t, err)

	// The request landed, but its answer was lost on the wire.
	basecamp := newFakeBasecamp(clock.Now)
	basecamp.afterPost = func(Destination, int64) error { return errWire }
	ob := obOutbox(t, ledger, basecamp)
	require.NoError(t, ob.Flush(ctx))
	require.Equal(t, IntentSending, obIntent(t, ledger, holdingKey(1)).State)

	settled, err := ob.reconcileStale(ctx, ob.opts.ReconcileAfter)
	require.NoError(t, err)
	assert.Zero(t, settled, "a request just made is given time to land")

	clock.Advance(2 * time.Minute)
	settled, err = ob.reconcileStale(ctx, ob.opts.ReconcileAfter)
	require.NoError(t, err)
	assert.Equal(t, 1, settled)
	in := obIntent(t, ledger, holdingKey(1))
	require.Equal(t, IntentSent, in.State)
	messages := basecamp.at(in.Destination)
	require.Len(t, messages, 1)
	assert.Equal(t, messages[0].ID, *in.ReceiptID)
	assert.Equal(t, 1, basecamp.postCount())
}

// sendingHolding writes a holding reply intent and moves it to sending as a
// crashed process would have left it.
func sendingHolding(t *testing.T, ledger *Ledger, id int64, reply admission.ReplyDestination) Intent {
	t.Helper()
	ctx := context.Background()
	seenRecord(t, ledger, id)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(id, 0, reply))
	require.NoError(t, err)
	claimed, ok, err := ledger.claimIntent(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, holdingKey(id), claimed.Key)
	return claimed
}

// Invariant 5: reconciliation adopts only an unambiguous candidate.
func TestOutboxReconciliationAdoptsOnlyTheUnambiguous(t *testing.T) {
	t.Run("exactly one match is adopted", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		in := sendingHolding(t, ledger, 1, obCommentReply)
		basecamp := newFakeBasecamp(clock.Now)
		basecamp.add(in.Destination, adapterAgentID, "<div>Working on it now</div>") // the worker's own words
		basecamp.add(in.Destination, obOtherPersonID, in.Body)                       // someone quoting it
		posted := basecamp.add(in.Destination, adapterAgentID, `<div dir="auto">`+in.Body+`</div>`)

		require.NoError(t, obOutbox(t, ledger, basecamp).Recover(ctx))
		got := obIntent(t, ledger, in.Key)
		require.Equal(t, IntentSent, got.State)
		assert.Equal(t, posted, *got.ReceiptID)
		assert.Zero(t, basecamp.postCount())
	})

	t.Run("two matches are indeterminate", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		in := sendingHolding(t, ledger, 1, obCommentReply)
		basecamp := newFakeBasecamp(clock.Now)
		basecamp.add(in.Destination, adapterAgentID, in.Body)
		basecamp.add(in.Destination, adapterAgentID, in.Body)

		require.NoError(t, obOutbox(t, ledger, basecamp).Recover(ctx))
		got := obIntent(t, ledger, in.Key)
		assert.Equal(t, IntentIndeterminate, got.State)
		assert.Nil(t, got.ReceiptID)
		assert.Zero(t, basecamp.postCount())
	})

	t.Run("a match another intent owns is not a candidate", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		// Two guards on one recording: the same boost body, the same
		// destination. The first went out and has its receipt.
		obAdmit(t, ledger, 1, "recording:10304028989")
		obAdmit(t, ledger, 2, "recording:10304028989")
		clock.Advance(DefaultGuardDelay)
		basecamp := newFakeBasecamp(clock.Now)
		first, ok, err := ledger.claimIntent(ctx)
		require.NoError(t, err)
		require.True(t, ok)
		receipt := basecamp.add(first.Destination, adapterAgentID, first.Body)
		_, err = ledger.recordReceipt(ctx, first.ID, receipt)
		require.NoError(t, err)
		second, ok, err := ledger.claimIntent(ctx)
		require.NoError(t, err)
		require.True(t, ok)

		require.NoError(t, obOutbox(t, ledger, basecamp).Recover(ctx))
		got := obIntent(t, ledger, second.Key)
		assert.Equal(t, IntentIndeterminate, got.State, "the only matching boost is the first guard's")
		assert.Equal(t, receipt, *obIntent(t, ledger, first.Key).ReceiptID)
	})

	t.Run("a match another unfinished intent could claim is indeterminate", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		obAdmit(t, ledger, 1, "recording:10304028989")
		obAdmit(t, ledger, 2, "recording:10304028989")
		clock.Advance(DefaultGuardDelay)
		first, ok, err := ledger.claimIntent(ctx)
		require.NoError(t, err)
		require.True(t, ok)
		basecamp := newFakeBasecamp(clock.Now)
		basecamp.add(first.Destination, adapterAgentID, first.Body)

		// The second guard is still pending: it could have been the one sent.
		ob := obOutbox(t, ledger, basecamp)
		_, err = ob.reconcileStale(ctx, 0)
		require.NoError(t, err)
		assert.Equal(t, IntentIndeterminate, obIntent(t, ledger, first.Key).State)
		assert.Zero(t, basecamp.postCount())
	})

	t.Run("a listing that fails settles nothing", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		in := sendingHolding(t, ledger, 1, obCommentReply)
		basecamp := newFakeBasecamp(clock.Now)
		basecamp.add(in.Destination, adapterAgentID, in.Body)
		basecamp.listErr = errWire

		require.Error(t, obOutbox(t, ledger, basecamp).Recover(ctx))
		assert.Equal(t, IntentSending, obIntent(t, ledger, in.Key).State, "tried again later, still never posted")
		assert.Zero(t, basecamp.postCount())
	})
}

// Invariant 6: a receipt belongs to one intent and never changes.
func TestOutboxAReceiptBelongsToOneIntent(t *testing.T) {
	ledger, _ := obLedger(t)
	ctx := context.Background()
	a := sendingHolding(t, ledger, 1, obCommentReply)
	b := sendingHolding(t, ledger, 2, obCommentReply)

	_, err := ledger.recordReceipt(ctx, a.ID, 777)
	require.NoError(t, err)
	_, err = ledger.recordReceipt(ctx, b.ID, 777)
	require.ErrorIs(t, err, ErrReceiptOwned)
	assert.Equal(t, IntentSending, obIntent(t, ledger, b.Key).State)

	_, err = ledger.db.ExecContext(ctx, `UPDATE outbox SET receipt_id = 778 WHERE id = ?`, a.ID)
	require.Error(t, err, "a receipt never changes")
}

// Invariant 7: states move along the lifecycle's edges only.
func TestOutboxIntentStatesMoveAlongTheirEdges(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	in := sendingHolding(t, ledger, 1, obCommentReply)

	_, err := ledger.db.ExecContext(ctx, `UPDATE outbox SET state = 'pending' WHERE id = ?`, in.ID)
	require.Error(t, err, "sending never returns to pending by itself")

	require.NoError(t, obOutbox(t, ledger, newFakeBasecamp(clock.Now)).Recover(ctx))
	require.Equal(t, IntentIndeterminate, obIntent(t, ledger, in.Key).State)

	err = ledger.ResolveIntent(ctx, in.ID, IntentResolution{Resolution: ResolveResend})
	require.Error(t, err, "a resolution names who decided")
	require.NoError(t, ledger.ResolveIntent(ctx, in.ID, IntentResolution{Resolution: ResolveAbandon, By: "person:26909558"}))
	got := obIntent(t, ledger, in.Key)
	assert.Equal(t, IntentAbandoned, got.State)
	assert.Equal(t, "person:26909558", got.ResolvedBy)
	require.ErrorIs(t, ledger.ResolveIntent(ctx, in.ID, IntentResolution{Resolution: ResolveResend, By: "person:26909558"}), ErrNotIndeterminate)
	_, err = ledger.db.ExecContext(ctx, `UPDATE outbox SET state = 'pending' WHERE id = ?`, in.ID)
	require.Error(t, err, "abandoned is final")
}

// A person's resend is the only way an intent goes out a second time.
func TestOutboxAPersonMayResendAnIndeterminateIntent(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	in := sendingHolding(t, ledger, 1, obCommentReply)
	basecamp := newFakeBasecamp(clock.Now)
	ob := obOutbox(t, ledger, basecamp)
	require.NoError(t, ob.Recover(ctx))
	require.NoError(t, ob.Flush(ctx))
	require.Zero(t, basecamp.postCount())

	require.NoError(t, ledger.ResolveIntent(ctx, in.ID, IntentResolution{Resolution: ResolveResend, By: "person:26909558"}))
	require.NoError(t, ob.Flush(ctx))
	assert.Equal(t, 1, basecamp.postCount())
	assert.Equal(t, IntentSent, obIntent(t, ledger, in.Key).State)
}

// Invariant 8: get_dispatch within the delay cancels the guard in its own
// transaction, and the guard never posts.
func TestOutboxGetDispatchCancelsTheGuard(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	obAdmit(t, ledger, 1, "recording:10304028989")
	l := obLaunch(t, ledger, 1)
	basecamp := newFakeBasecamp(clock.Now)
	ob := obOutbox(t, ledger, basecamp)

	clock.Advance(20 * time.Second)
	require.NoError(t, ob.Flush(ctx))
	require.Zero(t, basecamp.postCount(), "not due yet")

	d, err := ledger.Dispatch(ctx, l.Token, adapterAgentID)
	require.NoError(t, err)
	instruction, ok, err := d.Get(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)
	assert.False(t, instruction.GuardAcknowledged)
	assert.Equal(t, IntentCanceled, obIntent(t, ledger, guardKey(1)).State, "canceled in get_dispatch's transaction")

	clock.Advance(time.Minute)
	require.NoError(t, ob.Flush(ctx))
	assert.Zero(t, basecamp.postCount())
}

// Invariant 8: a guard that fired is reported to the worker, whether its task
// existed when it fired or was created after.
func TestOutboxAFiredGuardIsReportedToTheWorker(t *testing.T) {
	t.Run("task live when the guard fires", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		obAdmit(t, ledger, 1, "recording:10304028989")
		l := obLaunch(t, ledger, 1)
		basecamp := newFakeBasecamp(clock.Now)
		clock.Advance(DefaultGuardDelay)
		require.NoError(t, obOutbox(t, ledger, basecamp).Flush(ctx))
		require.Equal(t, 1, basecamp.postCount())

		guard := obIntent(t, ledger, guardKey(1))
		assert.Equal(t, IntentSent, guard.State)
		assert.Equal(t, Destination{BucketID: adapterBucketID, Kind: MessageBoost, RecordingID: obEventRecording}, guard.Destination)
		d, err := ledger.Dispatch(ctx, l.Token, adapterAgentID)
		require.NoError(t, err)
		instruction, _, err := d.Get(ctx, 1)
		require.NoError(t, err)
		assert.True(t, instruction.GuardAcknowledged)
	})

	t.Run("task created after the guard fired", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		obAdmit(t, ledger, 1, "recording:10304028989")
		basecamp := newFakeBasecamp(clock.Now)
		clock.Advance(DefaultGuardDelay)
		require.NoError(t, obOutbox(t, ledger, basecamp).Flush(ctx))
		require.Equal(t, 1, basecamp.postCount(), "a slow launch is what the guard is for")

		l := obLaunch(t, ledger, 1)
		d, err := ledger.Dispatch(ctx, l.Token, adapterAgentID)
		require.NoError(t, err)
		instruction, _, err := d.Get(ctx, 1)
		require.NoError(t, err)
		assert.True(t, instruction.GuardAcknowledged)
	})

	t.Run("follow-up joined after its guard fired", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		obAdmit(t, ledger, 1, "recording:10304028989")
		l := obLaunch(t, ledger, 1)
		obAdmit(t, ledger, 2, "recording:10304028989")
		require.Equal(t, StateQueued, getRecord(t, ledger, 2).State)
		d, err := ledger.Dispatch(ctx, l.Token, adapterAgentID)
		require.NoError(t, err)
		_, _, err = d.Get(ctx, 1)
		require.NoError(t, err)

		basecamp := newFakeBasecamp(clock.Now)
		clock.Advance(DefaultGuardDelay)
		require.NoError(t, obOutbox(t, ledger, basecamp).Flush(ctx))
		require.Equal(t, 1, basecamp.postCount(), "only the follow-up's guard: the first was canceled")

		joined, err := ledger.JoinConversation(ctx, l.TaskID)
		require.NoError(t, err)
		require.Equal(t, []int64{2}, joined)
		instruction, _, err := d.Get(ctx, 2)
		require.NoError(t, err)
		assert.True(t, instruction.GuardAcknowledged)
	})
}

// The guard arms only for a request, and stands down for a record that left
// the path to a worker.
func TestOutboxTheGuardArmsOnlyForRequestsStillWaiting(t *testing.T) {
	t.Run("no guard for a trigger that is not a request", func(t *testing.T) {
		ctx := context.Background()
		ledger, _ := obLedger(t)
		seenRecord(t, ledger, 1)
		v := admittedVerdict(1, 0, "recording:10304028989")
		v.Trigger, v.Acknowledge = admission.TriggerCompleted, false
		_, err := ledger.Admission().Commit(ctx, v)
		require.NoError(t, err)
		assert.Empty(t, obIntents(t, ledger))
	})

	t.Run("a record discarded before the guard is due", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		obAdmit(t, ledger, 1, "recording:10304028989")
		_, err := ledger.db.ExecContext(ctx, `UPDATE events SET state = 'discarded', reason = 'by_operator' WHERE id = 1`)
		require.NoError(t, err)
		basecamp := newFakeBasecamp(clock.Now)
		clock.Advance(DefaultGuardDelay)
		require.NoError(t, obOutbox(t, ledger, basecamp).Flush(ctx))
		assert.Zero(t, basecamp.postCount())
		assert.Equal(t, IntentCanceled, obIntent(t, ledger, guardKey(1)).State)
	})
}

// The hold marker holds sending; what was sent is still reconciled.
func TestOutboxPausedHoldsSending(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	in := sendingHolding(t, ledger, 1, obCommentReply)
	seenRecord(t, ledger, 2)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(2, 0, obCommentReply))
	require.NoError(t, err)

	basecamp := newFakeBasecamp(clock.Now)
	basecamp.add(in.Destination, adapterAgentID, in.Body)
	ob, err := NewOutbox(OutboxOptions{Ledger: ledger, Poster: basecamp, Paused: func(context.Context) (bool, error) { return true, nil }})
	require.NoError(t, err)
	require.NoError(t, ob.Recover(ctx))
	require.NoError(t, ob.Flush(ctx))
	assert.Zero(t, basecamp.postCount())
	assert.Equal(t, IntentSent, obIntent(t, ledger, in.Key).State)
	assert.Equal(t, IntentPending, obIntent(t, ledger, holdingKey(2)).State)
}

// Invariant 4, defended in the sender too: should an intent it already
// claimed ever come back as pending within one flush, the flush leaves it for
// the next rather than post it a second time.
func TestOutboxFlushNeverClaimsAnIntentTwice(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
	require.NoError(t, err)
	_, err = ledger.db.ExecContext(ctx, `DROP TRIGGER outbox_state_edges`)
	require.NoError(t, err)

	basecamp := newFakeBasecamp(clock.Now)
	reset := false
	basecamp.beforePost = func(Destination, string) error {
		if !reset {
			// Something outside the rules puts the row back to pending
			// mid-send, once.
			reset = true
			_, err := ledger.db.ExecContext(ctx, `UPDATE outbox SET state = 'pending', sending_at = NULL WHERE intent_key = ?`, holdingKey(1))
			require.NoError(t, err)
		}
		return errWire
	}
	require.NoError(t, obOutbox(t, ledger, basecamp).Flush(ctx))
	assert.Equal(t, 1, basecamp.postCount())
	assert.Equal(t, IntentPending, obIntent(t, ledger, holdingKey(1)).State, "left for the next flush, not claimed again in this one")
}

// A listing that keeps failing backs off, and gives up as indeterminate —
// never a request a second, never a resend.
func TestOutboxAFailingListingBacksOffThenGivesUp(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	in := sendingHolding(t, ledger, 1, obCommentReply)
	basecamp := newFakeBasecamp(clock.Now)
	basecamp.listErr = errWire
	ob := obOutbox(t, ledger, basecamp)

	lists := func() int { basecamp.mu.Lock(); defer basecamp.mu.Unlock(); return basecamp.lists }
	require.Error(t, ob.Recover(ctx))
	require.Equal(t, 1, lists())
	for range 5 {
		_, _ = ob.reconcileStale(ctx, 0)
	}
	assert.Equal(t, 1, lists(), "not tried again before its backoff")
	got := obIntent(t, ledger, in.Key)
	require.Equal(t, IntentSending, got.State)
	require.NotNil(t, got.ReconcileAt)
	assert.Equal(t, DefaultReconcileBackoff, got.ReconcileAt.Sub(clock.Now()))

	for i := 2; i <= MaxReconcileFailures; i++ {
		clock.Advance(MaxReconcileBackoff)
		_, _ = ob.reconcileStale(ctx, 0)
		assert.Equal(t, i, lists())
	}
	got = obIntent(t, ledger, in.Key)
	assert.Equal(t, IntentIndeterminate, got.State)
	assert.Equal(t, MaxReconcileFailures, got.ReconcileFailures, "the count that gave up is the count recorded")
	assert.Zero(t, basecamp.postCount())
}

// A destination that cannot be listed settles at once as indeterminate.
func TestOutboxAnUnlistableDestinationIsIndeterminate(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	in := sendingHolding(t, ledger, 1, obCommentReply)
	basecamp := newFakeBasecamp(clock.Now)
	basecamp.listErr = fmt.Errorf("gone: %w", ErrUnlistable)
	require.NoError(t, obOutbox(t, ledger, basecamp).Recover(ctx))
	got := obIntent(t, ledger, in.Key)
	assert.Equal(t, IntentIndeterminate, got.State)
	assert.Equal(t, "destination cannot be listed", got.Note)
	assert.Equal(t, 1, got.ReconcileFailures)
}

// On start, a sending intent younger than ReconcileAfter is left to land.
func TestOutboxRunLeavesAYoungSendingIntentToLand(t *testing.T) {
	ledger, clock := obLedger(t)
	in := sendingHolding(t, ledger, 1, obCommentReply)
	basecamp := newFakeBasecamp(clock.Now)
	ob, err := NewOutbox(OutboxOptions{Ledger: ledger, Poster: basecamp, Tick: time.Millisecond})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.NoError(t, ob.Run(ctx))
	assert.Zero(t, basecamp.lists)
	assert.Equal(t, IntentSending, obIntent(t, ledger, in.Key).State)
}

// Rivals: an intent left indeterminate or abandoned at the destination with
// the same body may own the only match, so nothing is adopted.
func TestOutboxUnsettledRivalsBlockAdoption(t *testing.T) {
	for _, rivalState := range []IntentState{IntentIndeterminate, IntentAbandoned} {
		t.Run(string(rivalState), func(t *testing.T) {
			ledger, clock := obLedger(t)
			ctx := context.Background()
			obAdmit(t, ledger, 1, "recording:10304028989")
			obAdmit(t, ledger, 2, "recording:10304028989")
			clock.Advance(DefaultGuardDelay)
			first, ok, err := ledger.claimIntent(ctx)
			require.NoError(t, err)
			require.True(t, ok)
			basecamp := newFakeBasecamp(clock.Now)
			// The first guard's request never got an answer; nothing listed yet.
			require.NoError(t, obOutbox(t, ledger, basecamp).Recover(ctx))
			require.Equal(t, IntentIndeterminate, obIntent(t, ledger, first.Key).State)
			if rivalState == IntentAbandoned {
				require.NoError(t, ledger.ResolveIntent(ctx, first.ID, IntentResolution{Resolution: ResolveAbandon, By: "person:26909558"}))
			}

			// The first boost shows up late; the second guard went sending.
			second, ok, err := ledger.claimIntent(ctx)
			require.NoError(t, err)
			require.True(t, ok)
			basecamp.add(second.Destination, adapterAgentID, second.Body)
			require.NoError(t, obOutbox(t, ledger, basecamp).Recover(ctx))
			assert.Equal(t, IntentIndeterminate, obIntent(t, ledger, second.Key).State)
		})
	}
}

// A lifecycle message whose receipt the ledger does not hold yet is not
// adopted as the worker's reply; it is recognized by its words at its own
// destination, so a notice in flight elsewhere, or one left for a person,
// never hides a reply.
func TestOutboxAnUnreceiptedNoticeIsNeverAdopted(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	in := sendingHolding(t, ledger, 1, obCommentReply)
	basecamp := newFakeBasecamp(clock.Now)
	since := clock.Now().Add(-time.Minute)
	landed := basecamp.add(in.Destination, adapterAgentID, `<div dir="auto">`+in.Body+`</div>`)
	reply := basecamp.add(in.Destination, adapterAgentID, "<div>Done: the fix is on the branch.</div>")
	replies := LifecycleFilteredReplies{Lister: basecamp, Ledger: ledger}

	listed, err := replies.AgentReplies(ctx, adapterBucketID, "comment", obReplyRecording, since)
	require.NoError(t, err)
	require.Len(t, listed, 1, "the sending notice is left out by its words")
	assert.Equal(t, reply, listed[0].ID)

	// Elsewhere, an abandoned notice hides nothing.
	other := admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 555}
	abandoned := sendingHolding(t, ledger, 2, other)
	require.NoError(t, obOutbox(t, ledger, newFakeBasecamp(clock.Now)).Recover(ctx))
	require.NoError(t, ledger.ResolveIntent(ctx, abandoned.ID, IntentResolution{Resolution: ResolveAbandon, By: "person:26909558"}))
	listed, err = replies.AgentReplies(ctx, adapterBucketID, "comment", obReplyRecording, since)
	require.NoError(t, err)
	require.Len(t, listed, 1)

	// That recovery listed an empty Basecamp, so the first notice is
	// indeterminate too, and still left out by its words. A person then finds
	// it and records its receipt, which identifies it from then on.
	require.Equal(t, IntentIndeterminate, obIntent(t, ledger, in.Key).State)
	require.NoError(t, ledger.ResolveIntent(ctx, in.ID, IntentResolution{Resolution: ResolveSent, ReceiptID: landed, By: "person:26909558"}))
	assert.True(t, IsLifecycleMessageIn(ledger)(landed))
	assert.False(t, IsLifecycleMessageIn(ledger)(reply))
	id, ok := AdoptableReply(AdoptionCandidate{DeliveredAt: since}, []AgentReply{{ID: landed, CreatedAt: clock.Now()}}, IsLifecycleMessageIn(ledger))
	assert.False(t, ok, "adopted %d", id)
}

// blockingPoster answers nothing until its request's context ends.
type blockingPoster struct{ *fakeBasecamp }

func (b blockingPoster) Post(ctx context.Context, _ Destination, _ string) (int64, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

// A flush with a deadline — the shutdown's — is not held past it by a request,
// and claims nothing it has no time left to send.
func TestOutboxFlushHonoursItsDeadline(t *testing.T) {
	ledger, clock := obLedger(t)
	for _, id := range []int64{1, 2} {
		seenRecord(t, ledger, id)
		_, err := ledger.Admission().Commit(context.Background(), obNoRouteVerdict(id, 0, obCommentReply))
		require.NoError(t, err)
	}
	// The first claim has half a second of slack; once its request is cut off
	// at PostTimeout, less than PostTimeout is left, so nothing more is
	// claimed.
	ob, err := NewOutbox(OutboxOptions{Ledger: ledger, Poster: blockingPoster{newFakeBasecamp(clock.Now)}, PostTimeout: time.Second})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	started := time.Now()
	_ = ob.Flush(ctx)
	assert.Less(t, time.Since(started), 5*time.Second)
	assert.Equal(t, IntentSending, obIntent(t, ledger, holdingKey(1)).State, "cut off mid-flight: reconciled later, never resent")
	assert.Equal(t, IntentPending, obIntent(t, ledger, holdingKey(2)).State, "not claimed with too little time left")
}

// A person's resend starts the request's reconciliation afresh: the failures
// of the listing before it are not counted against it.
func TestOutboxAResendStartsReconciliationAfresh(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	in := sendingHolding(t, ledger, 1, obCommentReply)
	basecamp := newFakeBasecamp(clock.Now)
	basecamp.listErr = errWire
	ob := obOutbox(t, ledger, basecamp)
	for range MaxReconcileFailures {
		_, _ = ob.reconcileStale(ctx, 0)
		clock.Advance(MaxReconcileBackoff)
	}
	require.Equal(t, IntentIndeterminate, obIntent(t, ledger, in.Key).State)

	require.NoError(t, ledger.ResolveIntent(ctx, in.ID, IntentResolution{Resolution: ResolveResend, By: "person:26909558"}))
	got := obIntent(t, ledger, in.Key)
	assert.Zero(t, got.ReconcileFailures)
	assert.Nil(t, got.ReconcileAt)

	basecamp.beforePost = func(Destination, string) error { return errWire }
	require.NoError(t, ob.Flush(ctx))
	_, _ = ob.reconcileStale(ctx, 0)
	assert.Equal(t, IntentSending, obIntent(t, ledger, in.Key).State, "one failed listing is the first of a fresh budget")
}

// A request that failed — a timeout, say — is given ReconcileAfter from its
// failure to land, not from its claim.
func TestOutboxAFailedPostIsGivenTimeToLand(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
	require.NoError(t, err)
	basecamp := newFakeBasecamp(clock.Now)
	basecamp.beforePost = func(Destination, string) error {
		clock.Advance(DefaultPostTimeout) // the request ran out its whole timeout
		return context.DeadlineExceeded
	}
	ob := obOutbox(t, ledger, basecamp)
	require.NoError(t, ob.Flush(ctx))
	_, _ = ob.reconcileStale(ctx, ob.opts.ReconcileAfter)
	assert.Zero(t, basecamp.lists, "not listed straight after the failure")
	assert.Equal(t, IntentSending, obIntent(t, ledger, holdingKey(1)).State)
}

// A request claimed with time left is still cut off at the flush's deadline,
// not at its own longer timeout.
func TestOutboxFlushCapsARequestAtItsDeadline(t *testing.T) {
	ledger, clock := obLedger(t)
	seenRecord(t, ledger, 1)
	_, err := ledger.Admission().Commit(context.Background(), obNoRouteVerdict(1, 0, obCommentReply))
	require.NoError(t, err)
	ob, err := NewOutbox(OutboxOptions{Ledger: ledger, Poster: blockingPoster{newFakeBasecamp(clock.Now)}, PostTimeout: time.Minute})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), MinPostWindow+500*time.Millisecond)
	defer cancel()
	started := time.Now()
	_ = ob.Flush(ctx)
	assert.Less(t, time.Since(started), MinPostWindow+5*time.Second)
	assert.Equal(t, IntentSending, obIntent(t, ledger, holdingKey(1)).State)
}

// A queue of intents arriving as fast as they can be sent does not starve
// reconciliation: the running connector sends in batches.
func TestOutboxRunReconcilesWhileSendsKeepArriving(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	stale := sendingHolding(t, ledger, 1, obCommentReply)
	basecamp := newFakeBasecamp(clock.Now)
	basecamp.add(stale.Destination, adapterAgentID, stale.Body)

	// Every send admits another request, so there is always one more to send.
	next := int64(100)
	admit := func() {
		next++
		seenRecord(t, ledger, next)
		_, err := ledger.Admission().Commit(context.Background(), obNoRouteVerdict(next, 0, obCommentReply))
		require.NoError(t, err)
	}
	basecamp.beforePost = func(Destination, string) error {
		admit()
		return nil
	}
	clock.Advance(2 * DefaultReconcileAfter)
	seenRecord(t, ledger, 2)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(2, 0, obCommentReply))
	require.NoError(t, err)

	ob, err := NewOutbox(OutboxOptions{Ledger: ledger, Poster: basecamp, Tick: time.Millisecond})
	require.NoError(t, err)
	// Cancel without a deadline: a flush with one claims nothing, and this
	// run must actually be sending while reconciliation is due. The run is
	// stopped once the stale intent settles, or after a bound generous enough
	// for a loaded race-detector runner; a drain that starves reconciliation
	// never settles it.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ob.Run(runCtx) }()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if in, err := ledger.Intent(ctx, stale.ID); err == nil && in.State != IntentSending {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	require.NoError(t, <-done)
	assert.Equal(t, IntentSent, obIntent(t, ledger, stale.Key).State, "the stale sending intent was reconciled")
}

// A holding reply is never posted about work the connector went on to run: it
// stands down when its record leaves blocked(no_route).
func TestOutboxAHoldingReplyStandsDownWhenTheRouteArrives(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
	require.NoError(t, err)

	// connect.json gains the route: the record is decided again and dispatched.
	_, err = ledger.Admission().Commit(ctx, admittedVerdict(1, getRecord(t, ledger, 1).Revision, "recording:10304028989"))
	require.NoError(t, err)
	obLaunch(t, ledger, 1)

	basecamp := newFakeBasecamp(clock.Now)
	require.NoError(t, obOutbox(t, ledger, basecamp).Flush(ctx))
	assert.Zero(t, basecamp.postCount())
	got := obIntent(t, ledger, holdingKey(1))
	assert.Equal(t, IntentCanceled, got.State)
	assert.Equal(t, "no longer called for", got.Note)
}

// A message the worker reported as its own acknowledgement or reply is the
// worker's, whatever it says: the guard's fixed form is short enough to
// collide with an acknowledgement in the worker's own words.
func TestOutboxNeverAdoptsAWorkersOwnMessage(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	obAdmit(t, ledger, 1, "recording:10304028989")
	l := obLaunch(t, ledger, 1)
	clock.Advance(DefaultGuardDelay)
	claimed, ok, err := ledger.claimIntent(ctx)
	require.NoError(t, err)
	require.True(t, ok)

	basecamp := newFakeBasecamp(clock.Now)
	workersOwn := basecamp.add(claimed.Destination, adapterAgentID, GuardAckBody)
	d, err := ledger.Dispatch(ctx, l.Token, adapterAgentID)
	require.NoError(t, err)
	_, err = d.Ack(ctx, 1, &workersOwn)
	require.NoError(t, err)

	clock.Advance(2 * time.Minute)
	require.NoError(t, obOutbox(t, ledger, basecamp).Recover(ctx))
	got := obIntent(t, ledger, guardKey(1))
	assert.Equal(t, IntentIndeterminate, got.State)
	assert.Nil(t, got.ReceiptID)
}

// A request Basecamp refused created nothing, and says so: no listing, no
// backoff, and a note a person can act on.
func TestOutboxARefusedRequestSaysNoMessageExists(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
	require.NoError(t, err)
	basecamp := newFakeBasecamp(clock.Now)
	basecamp.beforePost = func(Destination, string) error { return fmt.Errorf("404: %w", ErrNotPosted) }

	ob := obOutbox(t, ledger, basecamp)
	require.NoError(t, ob.Flush(ctx))
	got := obIntent(t, ledger, holdingKey(1))
	assert.Equal(t, IntentCanceled, got.State, "refused is not uncertain: nothing was created")
	assert.Equal(t, "the request was refused; no message was created", got.Note)
	assert.Zero(t, basecamp.lists, "nothing to look for")
	require.NoError(t, ob.Flush(ctx))
	assert.Equal(t, 1, basecamp.postCount(), "never asked again")
}

// A reconciliation listing is bounded in time: a destination that pages
// forever cannot hold up the sending of what is due.
func TestOutboxReconciliationListingIsBounded(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	in := sendingHolding(t, ledger, 1, obCommentReply)
	basecamp := newFakeBasecamp(clock.Now)
	ob, err := NewOutbox(OutboxOptions{Ledger: ledger, Poster: hangingLister{basecamp}, Tick: time.Millisecond})
	require.NoError(t, err)

	started := time.Now()
	_, err = ob.reconcileStale(ctx, 0)
	require.Error(t, err)
	assert.Less(t, time.Since(started), AdoptionScanTimeout+5*time.Second)
	assert.Equal(t, IntentSending, obIntent(t, ledger, in.Key).State, "a listing cut short is a failed listing")
	assert.NotNil(t, obIntent(t, ledger, in.Key).ReconcileAt)
}

// hangingLister answers a listing only when its request's context ends.
type hangingLister struct{ *fakeBasecamp }

func (h hangingLister) List(ctx context.Context, _ Destination, _ time.Time) ([]PostedMessage, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A guard or holding reply whose record is gone is canceled, not claimed
// again on every tick behind everything else waiting to be sent.
func TestOutboxAnIntentWithNoRecordIsCanceled(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
	require.NoError(t, err)
	_, err = ledger.db.ExecContext(ctx, `PRAGMA foreign_keys = off`)
	require.NoError(t, err)
	_, err = ledger.db.ExecContext(ctx, `DELETE FROM events WHERE id = 1`)
	require.NoError(t, err)

	basecamp := newFakeBasecamp(clock.Now)
	require.NoError(t, obOutbox(t, ledger, basecamp).Flush(ctx))
	assert.Equal(t, IntentCanceled, obIntent(t, ledger, holdingKey(1)).State)
	assert.Zero(t, basecamp.postCount())
}

// A guard Basecamp refused acknowledged nothing, so the worker is not told it
// did: the guard stands down and the worker acknowledges in its own words.
func TestOutboxARefusedGuardStandsDownAgain(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	obAdmit(t, ledger, 1, "recording:10304028989")
	// The task is already live, so its task event carries the armed guard the
	// claim marks fired.
	l := obLaunch(t, ledger, 1)
	basecamp := newFakeBasecamp(clock.Now)
	basecamp.beforePost = func(Destination, string) error { return fmt.Errorf("403: %w", ErrNotPosted) }
	clock.Advance(DefaultGuardDelay)
	require.NoError(t, obOutbox(t, ledger, basecamp).Flush(ctx))
	assert.Equal(t, IntentCanceled, obIntent(t, ledger, guardKey(1)).State)

	d, err := ledger.Dispatch(ctx, l.Token, adapterAgentID)
	require.NoError(t, err)
	instruction, ok, err := d.Get(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, instruction.Acknowledge)
	assert.False(t, instruction.GuardAcknowledged, "the worker acknowledges, since nobody did")
}

// Slow destinations cannot hold up a guard that is due: the running connector
// lists one destination between sends.
func TestOutboxSlowDestinationsDoNotHoldUpSending(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	for id := int64(1); id <= 3; id++ {
		sendingHolding(t, ledger, id, admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 900 + id})
	}
	clock.Advance(2 * DefaultReconcileAfter)
	seenRecord(t, ledger, 9)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(9, 0, obCommentReply))
	require.NoError(t, err)

	lister := &countingHangingLister{fakeBasecamp: newFakeBasecamp(clock.Now)}
	ob, err := NewOutbox(OutboxOptions{Ledger: ledger, Poster: lister, Tick: time.Millisecond})
	require.NoError(t, err)
	listCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	lister.cancelAfterFirst = cancel
	require.NoError(t, ob.Run(listCtx))
	assert.Equal(t, 1, lister.calls, "one listing per pass")
	assert.Equal(t, IntentSent, obIntent(t, ledger, holdingKey(9)).State, "the due intent went out before any listing")
}

// countingHangingLister fails every listing, and ends the run after the first.
type countingHangingLister struct {
	*fakeBasecamp
	calls            int
	cancelAfterFirst func()
}

func (c *countingHangingLister) List(context.Context, Destination, time.Time) ([]PostedMessage, error) {
	c.calls++
	if c.calls == 1 {
		c.cancelAfterFirst()
	}
	return nil, errWire
}

// A refused request created nothing, so once a person has fixed the cause
// they may send it again; nothing else ever takes a canceled intent back.
func TestOutboxAPersonMayResendARefusedIntent(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
	require.NoError(t, err)
	basecamp := newFakeBasecamp(clock.Now)
	basecamp.beforePost = func(Destination, string) error { return fmt.Errorf("403: %w", ErrNotPosted) }
	ob := obOutbox(t, ledger, basecamp)
	require.NoError(t, ob.Flush(ctx))
	in := obIntent(t, ledger, holdingKey(1))
	require.Equal(t, IntentCanceled, in.State)

	basecamp.beforePost = nil
	require.NoError(t, ledger.ResolveIntent(ctx, in.ID, IntentResolution{Resolution: ResolveResend, By: "person:26909558"}))
	require.NoError(t, ob.Flush(ctx))
	assert.Equal(t, IntentSent, obIntent(t, ledger, holdingKey(1)).State)
	assert.Equal(t, 2, basecamp.postCount())

	// A guard get_dispatch canceled is not refused, and is not resendable.
	obAdmit(t, ledger, 2, "recording:10304028989")
	l := obLaunch(t, ledger, 2)
	d, err := ledger.Dispatch(ctx, l.Token, adapterAgentID)
	require.NoError(t, err)
	_, _, err = d.Get(ctx, 2)
	require.NoError(t, err)
	guard := obIntent(t, ledger, guardKey(2))
	require.Equal(t, IntentCanceled, guard.State)
	require.ErrorIs(t, ledger.ResolveIntent(ctx, guard.ID, IntentResolution{Resolution: ResolveResend, By: "person:26909558"}), ErrNotIndeterminate)
	_, err = ledger.db.ExecContext(ctx, `UPDATE outbox SET state = 'pending' WHERE id = ?`, guard.ID)
	require.Error(t, err, "the database refuses it too")
}

// A person's resend that lands while a flush is still draining waits for the
// next flush: this one never claims the intent again, so it is not left
// sending with no request made.
func TestOutboxAResendDuringAFlushWaitsForTheNext(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	for _, id := range []int64{1, 2} {
		seenRecord(t, ledger, id)
		_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(id, 0, obCommentReply))
		require.NoError(t, err)
	}
	basecamp := newFakeBasecamp(clock.Now)
	firstID := obIntent(t, ledger, holdingKey(1)).ID
	refused := false
	basecamp.beforePost = func(Destination, string) error {
		if !refused {
			refused = true
			return fmt.Errorf("403: %w", ErrNotPosted)
		}
		// The second intent's request: meanwhile a person resends the first.
		require.NoError(t, ledger.ResolveIntent(context.Background(), firstID, IntentResolution{Resolution: ResolveResend, By: "person:26909558"}))
		return nil
	}
	ob := obOutbox(t, ledger, basecamp)
	require.NoError(t, ob.Flush(ctx))
	assert.Equal(t, IntentPending, obIntent(t, ledger, holdingKey(1)).State, "not claimed twice in one flush")

	basecamp.beforePost = nil
	require.NoError(t, ob.Flush(ctx))
	assert.Equal(t, IntentSent, obIntent(t, ledger, holdingKey(1)).State)
	assert.Equal(t, 3, basecamp.postCount())
}

// Invariant 9: a worker that asks while a guard is in flight is told the
// connector acknowledged, and a worker that asks after Basecamp refused it is
// not. The first case is the stated trade: its acknowledgement goes missing
// rather than doubled.
func TestOutboxAGuardIsFiredWhileInFlightAndArmedAfterRefusal(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	obAdmit(t, ledger, 1, "recording:10304028989")
	l := obLaunch(t, ledger, 1)
	d, err := ledger.Dispatch(ctx, l.Token, adapterAgentID)
	require.NoError(t, err)

	basecamp := newFakeBasecamp(clock.Now)
	var inFlight Instruction
	basecamp.beforePost = func(Destination, string) error {
		// The worker asks while the guard's request is in flight.
		var err error
		inFlight, _, err = d.Get(ctx, 1)
		require.NoError(t, err)
		return fmt.Errorf("404: %w", ErrNotPosted)
	}
	clock.Advance(DefaultGuardDelay)
	require.NoError(t, obOutbox(t, ledger, basecamp).Flush(ctx))
	assert.True(t, inFlight.GuardAcknowledged, "no double acknowledgement while the guard may land")

	// A follow-up worker, or the same one asking again, is told the truth.
	after, _, err := d.Get(ctx, 1)
	require.NoError(t, err)
	assert.False(t, after.GuardAcknowledged, "a refused guard acknowledged nothing")
}

// orderedPoster records the order of listings and posts.
type orderedPoster struct {
	*fakeBasecamp
	calls []string
}

func (o *orderedPoster) Post(ctx context.Context, dest Destination, body string) (int64, error) {
	o.calls = append(o.calls, "post")
	return o.fakeBasecamp.Post(ctx, dest, body)
}

func (o *orderedPoster) List(ctx context.Context, dest Destination, since time.Time) ([]PostedMessage, error) {
	o.calls = append(o.calls, "list")
	return o.fakeBasecamp.List(ctx, dest, since)
}

// On start: what a previous process left sending is reconciled, then what is
// due is sent, all before Start returns and so before anything else runs —
// except an intent that went sending too recently to have landed, which waits.
func TestOutboxStartSettlesWhatAPreviousProcessLeftBeforeSending(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	stale := sendingHolding(t, ledger, 1, admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 901})
	basecamp := &orderedPoster{fakeBasecamp: newFakeBasecamp(clock.Now)}
	landed := basecamp.add(stale.Destination, adapterAgentID, stale.Body)

	clock.Advance(10 * time.Minute) // the previous process died a while ago
	young := sendingHolding(t, ledger, 2, admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 902})
	seenRecord(t, ledger, 3)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(3, 0, admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 903}))
	require.NoError(t, err)

	require.NoError(t, obOutbox(t, ledger, basecamp).Start(ctx))
	got := obIntent(t, ledger, stale.Key)
	require.Equal(t, IntentSent, got.State, "reconciled on start")
	assert.Equal(t, landed, *got.ReceiptID)
	assert.Equal(t, IntentSending, obIntent(t, ledger, young.Key).State, "a request that may still be landing waits")
	assert.Equal(t, IntentSent, obIntent(t, ledger, holdingKey(3)).State, "what was due went out on start")
	assert.Equal(t, []string{"list", "post"}, basecamp.calls, "reconcile, then send")
}

// A ledger that cannot settle what a previous process left stops the start:
// nothing is sent past an intent that could not be reconciled.
func TestOutboxStartStopsOnALedgerThatCannotReconcile(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	stale := sendingHolding(t, ledger, 1, admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 901})
	basecamp := newFakeBasecamp(clock.Now)
	basecamp.add(stale.Destination, adapterAgentID, stale.Body)
	clock.Advance(10 * time.Minute)
	seenRecord(t, ledger, 2)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(2, 0, obCommentReply))
	require.NoError(t, err)

	// Adoption reads task_events; the ledger now cannot.
	_, err = ledger.db.ExecContext(ctx, `ALTER TABLE task_events RENAME TO task_events_gone`)
	require.NoError(t, err)

	require.Error(t, obOutbox(t, ledger, basecamp).Start(ctx))
	assert.Zero(t, basecamp.postCount(), "nothing sent past it")
	assert.Equal(t, IntentPending, obIntent(t, ledger, holdingKey(2)).State)
}

// A listing that failed and backed off is not a reason to stop starting:
// Run tries it again, and what is due goes out now.
func TestOutboxStartCarriesOnPastABackedOffListing(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	stale := sendingHolding(t, ledger, 1, admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 901})
	clock.Advance(10 * time.Minute)
	seenRecord(t, ledger, 2)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(2, 0, obCommentReply))
	require.NoError(t, err)
	basecamp := newFakeBasecamp(clock.Now)
	basecamp.listErr = errWire

	require.NoError(t, obOutbox(t, ledger, basecamp).Start(ctx))
	got := obIntent(t, ledger, stale.Key)
	assert.Equal(t, IntentSending, got.State)
	assert.NotNil(t, got.ReconcileAt, "backed off")
	assert.Equal(t, IntentSent, obIntent(t, ledger, holdingKey(2)).State)
}

// The start's sending stops at the first send that may not have landed: the
// next is likely to meet the same Basecamp, and the connector's start should
// not wait out a timeout per notice. Run carries on.
func TestOutboxStartStopsSendingAtTheFirstUncertainSend(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	for _, id := range []int64{1, 2, 3} {
		seenRecord(t, ledger, id)
		_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(id, 0, obCommentReply))
		require.NoError(t, err)
	}
	basecamp := newFakeBasecamp(clock.Now)
	basecamp.beforePost = func(Destination, string) error { return context.DeadlineExceeded }

	require.NoError(t, obOutbox(t, ledger, basecamp).Start(ctx))
	assert.Equal(t, 1, basecamp.postCount())
	assert.Equal(t, IntentPending, obIntent(t, ledger, holdingKey(3)).State)
}

// failingAt fails listings at one destination only.
type failingAt struct {
	*fakeBasecamp
	recording int64
}

func (f failingAt) List(ctx context.Context, dest Destination, since time.Time) ([]PostedMessage, error) {
	if dest.RecordingID == f.recording {
		return nil, errWire
	}
	return f.fakeBasecamp.List(ctx, dest, since)
}

// A hard failure is not hidden behind a listing that merely backed off
// earlier in the same pass.
func TestOutboxStartSeesAHardErrorAfterABackedOffListing(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	sendingHolding(t, ledger, 1, admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 901})
	second := sendingHolding(t, ledger, 2, admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 902})
	basecamp := newFakeBasecamp(clock.Now)
	basecamp.add(second.Destination, adapterAgentID, second.Body)
	clock.Advance(10 * time.Minute)
	_, err := ledger.db.ExecContext(ctx, `ALTER TABLE task_events RENAME TO task_events_gone`)
	require.NoError(t, err)

	require.Error(t, obOutbox(t, ledger, failingAt{basecamp, 901}).Start(ctx))
}

// A ledger that cannot record what a send settled — a receipt, or a
// refusal — is an error wherever it happens: a start stops on it and sends
// nothing more, and the intent stays sending for reconciliation to settle.
func TestOutboxALedgerFailureAfterASendStopsTheStart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger string
		postErr error
	}{
		{name: "receipt", trigger: `CREATE TRIGGER refuse_receipt BEFORE UPDATE OF receipt_id ON outbox WHEN NEW.receipt_id IS NOT NULL BEGIN SELECT RAISE(ABORT, 'injected'); END`},
		{name: "refusal", trigger: `CREATE TRIGGER refuse_cancel BEFORE UPDATE OF state ON outbox WHEN NEW.state = 'canceled' BEGIN SELECT RAISE(ABORT, 'injected'); END`, postErr: fmt.Errorf("403: %w", ErrNotPosted)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger, clock := obLedger(t)
			ctx := context.Background()
			for _, id := range []int64{1, 2} {
				seenRecord(t, ledger, id)
				_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(id, 0, obCommentReply))
				require.NoError(t, err)
			}
			_, err := ledger.db.ExecContext(ctx, tc.trigger)
			require.NoError(t, err)
			basecamp := newFakeBasecamp(clock.Now)
			basecamp.beforePost = func(Destination, string) error { return tc.postErr }

			require.Error(t, obOutbox(t, ledger, basecamp).Start(ctx))
			assert.Equal(t, 1, basecamp.postCount(), "nothing sent past the failure")
			assert.Equal(t, IntentSending, obIntent(t, ledger, holdingKey(1)).State, "left for reconciliation")
			assert.Equal(t, IntentPending, obIntent(t, ledger, holdingKey(2)).State)
		})
	}
}

// hangingAt blocks listings at one destination until ctx ends.
type hangingAt struct {
	*fakeBasecamp
	recording int64
}

func (h hangingAt) List(ctx context.Context, dest Destination, since time.Time) ([]PostedMessage, error) {
	if dest.RecordingID == h.recording {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return h.fakeBasecamp.List(ctx, dest, since)
}

// A ledger failure met during the start's reconciliation stops the start even
// when the start's bound runs out later in the same pass.
func TestOutboxStartStopsOnALedgerFailureWhateverTheBoundDoesAfter(t *testing.T) {
	ledger, clock := obLedger(t)
	first := sendingHolding(t, ledger, 1, admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 902})
	sendingHolding(t, ledger, 2, admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 901})
	basecamp := newFakeBasecamp(clock.Now)
	basecamp.add(first.Destination, adapterAgentID, first.Body)
	clock.Advance(10 * time.Minute)
	_, err := ledger.db.ExecContext(context.Background(), `ALTER TABLE task_events RENAME TO task_events_gone`)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	require.Error(t, obOutbox(t, ledger, hangingAt{basecamp, 901}).Start(ctx))
	assert.Less(t, time.Since(started), 2500*time.Millisecond, "the pass stops at the failure rather than spending the bound")
}

// A ledger that cannot even list what is sending stops the start before
// anything is sent.
func TestOutboxStartStopsWhenTheLedgerCannotListSendingIntents(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	stale := sendingHolding(t, ledger, 1, admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 901})
	clock.Advance(10 * time.Minute)
	seenRecord(t, ledger, 2)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(2, 0, obCommentReply))
	require.NoError(t, err)
	// A row the ledger cannot read back.
	_, err = ledger.db.ExecContext(ctx, `UPDATE outbox SET reconcile_at = 'garbage' WHERE id = ?`, stale.ID)
	require.NoError(t, err)

	basecamp := newFakeBasecamp(clock.Now)
	require.Error(t, obOutbox(t, ledger, basecamp).Start(ctx))
	assert.Zero(t, basecamp.postCount())
}

// cancelingLister ends its caller's context as it answers, as a shutdown
// arriving mid-reconciliation would.
type cancelingLister struct {
	*fakeBasecamp
	cancel func()
}

func (c cancelingLister) List(ctx context.Context, dest Destination, since time.Time) ([]PostedMessage, error) {
	out, err := c.fakeBasecamp.List(ctx, dest, since)
	c.cancel()
	return out, err
}

// A ledger failure is marked where it happens: a context that ends at the
// same moment does not hide it from a start.
func TestOutboxALedgerFailureIsNotHiddenByAnEndingContext(t *testing.T) {
	ledger, clock := obLedger(t)
	stale := sendingHolding(t, ledger, 1, admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 901})
	basecamp := newFakeBasecamp(clock.Now)
	basecamp.add(stale.Destination, adapterAgentID, stale.Body)
	clock.Advance(10 * time.Minute)
	_, err := ledger.db.ExecContext(context.Background(), `ALTER TABLE task_events RENAME TO task_events_gone`)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.Error(t, obOutbox(t, ledger, cancelingLister{basecamp, cancel}).Start(ctx))
}
