package connector

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// obRetraction is the retraction of one posted intent, for the event whose ask
// it answers.
func obRetraction(t *testing.T, ledger *Ledger, retracted Intent, eventID int64) Intent {
	t.Helper()
	return obIntent(t, ledger, retractionKey(retracted.ID, eventID))
}

func obRetractions(t *testing.T, ledger *Ledger) []Intent {
	t.Helper()
	out, err := ledger.Intents(context.Background(), IntentFilter{Kinds: []IntentKind{IntentRetraction}})
	require.NoError(t, err)
	return out
}

// Done when: a notice that asked a person to run something says so no longer,
// once they have. The ask is answered by another message at the same
// destination, and what went out is left exactly as it was posted.
func TestPostedAskIsRetractedWhenItIsAnswered(t *testing.T) {
	t.Run("a holding reply, once the record is redispatched", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		seenRecord(t, ledger, 1)
		_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
		require.NoError(t, err)
		basecamp := newFakeBasecamp(clock.Now)
		ob := obOutbox(t, ledger, basecamp)
		require.NoError(t, ob.Flush(ctx))
		holding := obIntent(t, ledger, holdingKey(1))
		require.Equal(t, IntentSent, holding.State)

		clock.Advance(12 * time.Minute)
		_, err = ledger.Redispatch(ctx, 1, "jorge")
		require.NoError(t, err)

		in := obRetraction(t, ledger, holding, 1)
		assert.Equal(t, IntentRetraction, in.Kind)
		assert.Equal(t, holding.ID, in.Retracts)
		assert.Equal(t, holding.Destination, in.Destination, "answered where it was asked")
		assert.Equal(t, "<div>Event 1: an earlier notice here asked a person to run basecamp connect redispatch 1. "+
			"It was run at 12:12 UTC; the notice stands as a record, and asks for nothing now.<br>"+
			"<br>Event 1 · automatic notice from basecamp connect</div>", in.Body)

		require.NoError(t, ob.Flush(ctx))
		assert.Equal(t, IntentSent, obRetraction(t, ledger, holding, 1).State)
		posted := basecamp.at(holding.Destination)
		require.Len(t, posted, 2)
		assert.Equal(t, holding.Body, posted[0].Content, "the notice it answers is not edited, and not deleted")
		assert.Equal(t, in.Body, posted[1].Content)
	})

	t.Run("a completion notice, once the record is redispatched", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		obAdmit(t, ledger, 1, "recording:10304028989")
		l := obLaunch(t, ledger, 1)
		clock.Advance(5 * time.Minute)
		_, err := ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopDeadline})
		require.NoError(t, err)
		basecamp := newFakeBasecamp(clock.Now)
		ob := obOutbox(t, ledger, basecamp)
		require.NoError(t, ob.Flush(ctx))
		completion := obIntent(t, ledger, completionKey(l.AttemptID))
		require.Equal(t, IntentSent, completion.State)
		require.Contains(t, completion.Body, "Needs a person: basecamp connect redispatch 1")

		clock.Advance(3 * time.Minute)
		_, err = ledger.Redispatch(ctx, 1, "jorge")
		require.NoError(t, err)

		in := obRetraction(t, ledger, completion, 1)
		assert.Equal(t, completion.Destination, in.Destination)
		assert.Contains(t, in.Body, "It was run at 12:08 UTC")
		require.NoError(t, ob.Flush(ctx))
		assert.Equal(t, IntentSent, obRetraction(t, ledger, completion, 1).State)
	})

	t.Run("a holding reply, once the record is discarded", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		seenRecord(t, ledger, 1)
		_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
		require.NoError(t, err)
		basecamp := newFakeBasecamp(clock.Now)
		ob := obOutbox(t, ledger, basecamp)
		require.NoError(t, ob.Flush(ctx))
		holding := obIntent(t, ledger, holdingKey(1))
		require.Equal(t, IntentSent, holding.State)

		clock.Advance(90 * time.Minute)
		_, err = ledger.Discard(ctx, 1, "jorge")
		require.NoError(t, err)

		in := obRetraction(t, ledger, holding, 1)
		assert.Equal(t, "<div>Event 1: an earlier notice here asked a person to run basecamp connect redispatch 1. "+
			"The record was closed instead, at 13:30 UTC; the notice stands as a record, and asks for nothing now.<br>"+
			"<br>Event 1 · automatic notice from basecamp connect</div>", in.Body)
	})

	t.Run("a refused start, once the record is redispatched", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		obAdmit(t, ledger, 1, "recording:10304028989")
		require.NoError(t, ledger.RefuseStart(ctx, 1, ReasonRouteUnusable))
		basecamp := newFakeBasecamp(clock.Now)
		ob := obOutbox(t, ledger, basecamp)
		require.NoError(t, ob.Flush(ctx))
		refusal := obIntent(t, ledger, refusedStartKey(1))
		require.Equal(t, IntentSent, refusal.State)

		clock.Advance(20 * time.Minute)
		_, err := ledger.Redispatch(ctx, 1, "jorge")
		require.NoError(t, err)

		in := obRetraction(t, ledger, refusal, 1)
		assert.Equal(t, refusal.Destination, in.Destination)
		assert.Contains(t, in.Body, "It was run at 12:20 UTC")
	})

	t.Run("a holding reply in a Campfire, as plain text", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		seenRecord(t, ledger, 1)
		_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, admission.ReplyDestination{Kind: admission.ReplyChatLine, RecordingID: obCampfire}))
		require.NoError(t, err)
		basecamp := newFakeBasecamp(clock.Now)
		ob := obOutbox(t, ledger, basecamp)
		require.NoError(t, ob.Flush(ctx))
		holding := obIntent(t, ledger, holdingKey(1))
		require.Equal(t, IntentSent, holding.State)

		_, err = ledger.Redispatch(ctx, 1, "jorge")
		require.NoError(t, err)
		in := obRetraction(t, ledger, holding, 1)
		assert.Equal(t, MessageChatLine, in.Destination.Kind)
		assert.NotContains(t, in.Body, "<", "a chat line is plain text")
	})
}

// Only a notice that asks a person to do something is retracted. A guard
// acknowledgement and a still-running notice are records of a moment: they ask
// for nothing, so a decision leaves them alone.
func TestOnlyAnAskIsRetracted(t *testing.T) {
	ctx := context.Background()
	ledger, clock := obLedger(t)
	obAdmit(t, ledger, 1, "recording:10304028989")
	l := obLaunch(t, ledger, 1)
	clock.Advance(10 * time.Minute)
	_, err := ledger.StillRunning(ctx, l.AttemptID)
	require.NoError(t, err)
	basecamp := newFakeBasecamp(clock.Now)
	ob := obOutbox(t, ledger, basecamp)
	require.NoError(t, ob.Flush(ctx))
	guard := obIntent(t, ledger, guardKey(1))
	running := obIntent(t, ledger, stillRunningKey(l.AttemptID, 1))
	require.Equal(t, IntentSent, guard.State)
	require.Equal(t, IntentSent, running.State)

	_, err = ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopFailed})
	require.NoError(t, err)
	require.NoError(t, ob.Flush(ctx))
	completion := obIntent(t, ledger, completionKey(l.AttemptID))
	require.Equal(t, IntentSent, completion.State)

	clock.Advance(time.Minute)
	_, err = ledger.Redispatch(ctx, 1, "jorge")
	require.NoError(t, err)

	retractions := obRetractions(t, ledger)
	require.Len(t, retractions, 1, "the notice that asked is retracted; the ones that only reported are not")
	assert.Equal(t, completion.ID, retractions[0].Retracts)
}

// A message the connector has not posted is not retracted: the ask stands down
// before it is sent, which is the half that already existed, and posting a
// retraction of it would answer something nobody read.
func TestAnAskThatWasNeverSentIsNotRetracted(t *testing.T) {
	ctx := context.Background()
	ledger, clock := obLedger(t)
	obAdmit(t, ledger, 1, "recording:10304028989")
	l := obLaunch(t, ledger, 1)
	clock.Advance(5 * time.Minute)
	_, err := ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopDeadline})
	require.NoError(t, err)
	completion := obIntent(t, ledger, completionKey(l.AttemptID))
	require.Equal(t, IntentPending, completion.State)

	_, err = ledger.Redispatch(ctx, 1, "jorge")
	require.NoError(t, err)
	assert.Empty(t, obRetractions(t, ledger))

	basecamp := newFakeBasecamp(clock.Now)
	require.NoError(t, obOutbox(t, ledger, basecamp).Flush(ctx))
	sent := obIntent(t, ledger, completionKey(l.AttemptID))
	require.Equal(t, IntentSent, sent.State)
	assert.NotContains(t, sent.Body, "Needs a person", "the ask stood down at its claim instead")
	assert.Contains(t, sent.Body, "Event 1: unknown, the worker did not report on it.", "what happened is still reported")
	assert.Empty(t, obRetractions(t, ledger), "nothing to retract: the ask was never posted")
}

// A completion notice whose ask stood down before it was sent asked for
// nothing, and a later decision does not retract it: saying "an earlier notice
// asked a person to run this" of a notice that did not is the same false
// statement the other way round.
func TestANoticeThatNeverAskedIsNotRetracted(t *testing.T) {
	ctx := context.Background()
	ledger, clock := obLedger(t)
	obAdmit(t, ledger, 1, "recording:10304028989")
	first := obLaunch(t, ledger, 1)
	clock.Advance(5 * time.Minute)
	_, err := ledger.EndAttempt(ctx, AttemptEnd{AttemptID: first.AttemptID, Stop: StopDeadline})
	require.NoError(t, err)
	_, err = ledger.Redispatch(ctx, 1, "jorge")
	require.NoError(t, err)
	basecamp := newFakeBasecamp(clock.Now)
	ob := obOutbox(t, ledger, basecamp)
	require.NoError(t, ob.Flush(ctx))
	quiet := obIntent(t, ledger, completionKey(first.AttemptID))
	require.Equal(t, IntentSent, quiet.State)
	require.NotContains(t, quiet.Body, "Needs a person")

	// It runs again, ends unknown again, and this time a person closes it.
	clock.Advance(time.Minute)
	second := obLaunch(t, ledger, 1)
	clock.Advance(5 * time.Minute)
	_, err = ledger.EndAttempt(ctx, AttemptEnd{AttemptID: second.AttemptID, Stop: StopDeadline})
	require.NoError(t, err)
	_, err = ledger.Discard(ctx, 1, "jorge")
	require.NoError(t, err)

	assert.Empty(t, obRetractions(t, ledger), "neither notice ever asked a person for anything that is now done")
}

// A notice still in flight when the decision is made: the retraction is
// written, and waits at its claim until the message it answers is known to be
// at the destination.
func TestRetractionWaitsForTheNoticeItAnswers(t *testing.T) {
	t.Run("sent after the decision", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock, ob, basecamp, holding := obSendingHoldingReply(t)
		_, err := ledger.Redispatch(ctx, 1, "jorge")
		require.NoError(t, err)
		in := obRetraction(t, ledger, holding, 1)
		require.Equal(t, IntentPending, in.State)

		require.NoError(t, ob.Flush(ctx))
		waiting := obRetraction(t, ledger, holding, 1)
		assert.Equal(t, IntentPending, waiting.State, "held while the notice it answers is on its way")
		assert.True(t, waiting.NotBefore.After(in.NotBefore), "due again shortly, not claimed and left with nothing to say")

		// The notice lands: reconciliation adopts it, and the retraction goes
		// out behind it.
		receipt := basecamp.add(holding.Destination, adapterAgentID, holding.Body)
		_, err = ledger.settleReconciled(ctx, holding.ID, receipt, "")
		require.NoError(t, err)
		clock.Advance(RetractionWait)
		require.NoError(t, ob.Flush(ctx))
		assert.Equal(t, IntentSent, obRetraction(t, ledger, holding, 1).State)
	})

	t.Run("left indeterminate after the decision", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock, ob, basecamp, holding := obSendingHoldingReply(t)
		_, err := ledger.Redispatch(ctx, 1, "jorge")
		require.NoError(t, err)

		_, err = ledger.settleReconciled(ctx, holding.ID, 0, obUnreachableNote)
		require.NoError(t, err)
		clock.Advance(RetractionWait)
		require.NoError(t, ob.Flush(ctx))

		in := obRetraction(t, ledger, holding, 1)
		assert.Equal(t, IntentCanceled, in.State, "nothing at the destination to answer")
		assert.Equal(t, "the notice it answers was not posted", in.Note)
		assert.Empty(t, basecamp.at(holding.Destination), "a person settles the notice; nothing is posted on a guess")
	})
}

// obSendingHoldingReply leaves a holding reply sending: the request failed on
// the wire, so it may or may not have reached Basecamp.
func obSendingHoldingReply(t *testing.T) (*Ledger, *obClock, *Outbox, *fakeBasecamp, Intent) {
	t.Helper()
	ctx := context.Background()
	ledger, clock := obLedger(t)
	seenRecord(t, ledger, 1)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
	require.NoError(t, err)
	basecamp := newFakeBasecamp(clock.Now)
	basecamp.beforePost = func(Destination, string) error { return errWire }
	ob := obOutbox(t, ledger, basecamp)
	require.NoError(t, ob.Flush(ctx))
	basecamp.beforePost = nil
	holding := obIntent(t, ledger, holdingKey(1))
	require.Equal(t, IntentSending, holding.State)
	return ledger, clock, ob, basecamp, holding
}

// One retraction per posted ask, however often a person decides again.
func TestAnAskIsRetractedOnce(t *testing.T) {
	ctx := context.Background()
	ledger, clock := obLedger(t)
	seenRecord(t, ledger, 1)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
	require.NoError(t, err)
	basecamp := newFakeBasecamp(clock.Now)
	ob := obOutbox(t, ledger, basecamp)
	require.NoError(t, ob.Flush(ctx))
	holding := obIntent(t, ledger, holdingKey(1))

	clock.Advance(12 * time.Minute)
	_, err = ledger.Redispatch(ctx, 1, "jorge")
	require.NoError(t, err)
	// The retraction goes out, and names the command in its own words. A
	// second decision does not answer it: a retraction asks for nothing, so
	// nothing retracts it.
	require.NoError(t, ob.Flush(ctx))
	require.Equal(t, IntentSent, obRetraction(t, ledger, holding, 1).State)
	clock.Advance(8 * time.Minute)
	_, err = ledger.Redispatch(ctx, 1, "jorge")
	require.NoError(t, err)

	retractions := obRetractions(t, ledger)
	require.Len(t, retractions, 1)
	assert.Contains(t, retractions[0].Body, "It was run at 12:12 UTC", "the first answer stands; it is not rewritten")
	assert.Equal(t, holding.ID, retractions[0].Retracts)
	assert.Len(t, basecamp.at(holding.Destination), 2, "the reply and one answer to it")
}

// The retraction's migration rebuilds the outbox table, which takes its
// indexes and triggers with it and re-parses the triggers elsewhere that name
// it. Everything it carried is still there afterwards, the hold's refusal
// included: a schema this rebuild quietly dropped something from would post
// under a hold, or stop canceling a guard.
func TestTheOutboxRebuildKeepsEverythingThatNamesIt(t *testing.T) {
	ctx := context.Background()
	ledger, _ := obLedger(t)
	rows, err := ledger.db.QueryContext(ctx, `
SELECT type, name FROM sqlite_master
WHERE sql LIKE '%outbox%' AND type IN ('index', 'trigger') ORDER BY name`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var found []string
	for rows.Next() {
		var kind, name string
		require.NoError(t, rows.Scan(&kind, &name))
		found = append(found, kind+" "+name)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{
		"trigger events_held_cancels_guard",
		"index outbox_destination",
		"index outbox_due",
		"index outbox_event",
		"trigger outbox_guard_canceled_by_get_dispatch",
		"trigger outbox_guard_fired_before_task",
		"index outbox_receipt",
		"trigger outbox_receipt_is_final",
		"trigger outbox_refused_under_hold",
		"index outbox_retracts",
		"trigger outbox_state_edges",
	}, found)

	// And the hold still refuses a send.
	seenRecord(t, ledger, 1)
	_, err = ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
	require.NoError(t, err)
	_, err = ledger.SetHold(ctx, "jorge", HoldByOperator)
	require.NoError(t, err)
	_, _, err = ledger.claimIntent(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing is posted until basecamp connect release")
}

// The ask is read off the words that went out, and read exactly: a notice
// about event 12 is not an ask for event 1.
func TestAsksRedispatchReadsTheAskExactly(t *testing.T) {
	assert.True(t, asksRedispatch(renderHoldingReply(MessageComment, 1), 1))
	assert.True(t, asksRedispatch(renderHoldingReply(MessageChatLine, 12), 12))
	assert.False(t, asksRedispatch(renderHoldingReply(MessageComment, 12), 1))
	assert.False(t, asksRedispatch(renderHoldingReply(MessageComment, 1), 12))
	assert.False(t, asksRedispatch(GuardAckBody, 1))
	assert.False(t, asksRedispatch(renderStillRunning(MessageComment, 3, "a1", 1, time.Now(), time.Time{}), 1))

	asking := renderCompletion(MessageComment, Settlement{TaskID: 3, AttemptID: "a1", Stop: StopFailed,
		Events: []SettledEvent{{EventID: 1, Outcome: OutcomeFailed}}})
	assert.True(t, asksRedispatch(asking, 1))
	reporting := renderCompletion(MessageComment, Settlement{TaskID: 3, AttemptID: "a1", Stop: StopFinished,
		Events: []SettledEvent{{EventID: 1, Outcome: OutcomeSucceeded, Reported: true}}})
	require.NotEmpty(t, reporting)
	assert.False(t, asksRedispatch(reporting, 1), "succeeded with no reply reported asks for nothing")
}
