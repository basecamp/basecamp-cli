package connector

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
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

// obRoutedNow is the prerequisite a redispatch of a blocked record hands back
// to its caller: admission runs again, the route is there this time, and the
// record is admitted. Until this has happened the record is still blocked and
// the operator is still being told to redispatch.
func obRoutedNow(t *testing.T, ledger *Ledger, id int64) {
	t.Helper()
	_, err := ledger.Admission().Commit(context.Background(),
		admittedVerdict(id, getRecord(t, ledger, id).Revision, "recording:10304028989"))
	require.NoError(t, err)
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
			"A person ran it at 12:12 UTC, and event 1 is not waiting for it now.<br>"+
			"The earlier notice stands as a record of when it was written; nothing in it needs doing.<br>"+
			"<br>Event 1 · automatic notice from basecamp connect</div>", in.Body)

		// The redispatch authorized a blocked record; its prerequisite runs
		// again, and that is what settles the block.
		obRoutedNow(t, ledger, 1)
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
		assert.Contains(t, in.Body, "A person ran it at 12:08 UTC")
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
			"A person closed event 1 instead, at 13:30 UTC, and it is not waiting for anything now.<br>"+
			"The earlier notice stands as a record of when it was written; nothing in it needs doing.<br>"+
			"<br>Event 1 · automatic notice from basecamp connect</div>", in.Body)
		require.NoError(t, ob.Flush(ctx))
		assert.Equal(t, IntentSent, obRetraction(t, ledger, holding, 1).State, "a closed record is waiting for nothing")
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
		obRoutedNow(t, ledger, 1)

		in := obRetraction(t, ledger, refusal, 1)
		assert.Equal(t, refusal.Destination, in.Destination)
		assert.Contains(t, in.Body, "A person ran it at 12:20 UTC")
		require.NoError(t, ob.Flush(ctx))
		assert.Equal(t, IntentSent, obRetraction(t, ledger, refusal, 1).State)
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

// A completion notice speaks for a whole attempt and can ask about several
// events. Answering one of them says so, and says nothing about the others:
// the closing line is the one a reader acts on, and "nothing in it needs
// doing" would dismiss asks nobody has touched.
func TestARetractionSpeaksOnlyForItsOwnEvent(t *testing.T) {
	ctx := context.Background()
	ledger, clock := obLedger(t)
	obAdmit(t, ledger, 1, "recording:10304028989")
	l := obLaunch(t, ledger, 1)
	obAdmit(t, ledger, 2, "recording:10304028989")
	joined, err := ledger.JoinConversation(ctx, l.TaskID)
	require.NoError(t, err)
	require.Equal(t, []int64{2}, joined)
	exposed, err := ledger.ExposeEvent(ctx, l.AttemptID, 2)
	require.NoError(t, err)
	require.True(t, exposed)
	clock.Advance(5 * time.Minute)
	_, err = ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopDeadline})
	require.NoError(t, err)

	basecamp := newFakeBasecamp(clock.Now)
	ob := obOutbox(t, ledger, basecamp)
	require.NoError(t, ob.Flush(ctx))
	completion := obIntent(t, ledger, completionKey(l.AttemptID))
	require.Equal(t, IntentSent, completion.State)
	require.Contains(t, completion.Body, "Needs a person: basecamp connect redispatch 1")
	require.Contains(t, completion.Body, "Needs a person: basecamp connect redispatch 2")

	// One of the two is decided. The other's ask is untouched, and the
	// retraction does not speak for it.
	clock.Advance(time.Minute)
	_, err = ledger.Redispatch(ctx, 1, "jorge")
	require.NoError(t, err)

	first := obRetraction(t, ledger, completion, 1)
	assert.Contains(t, first.Body, "It also asks about event 2; this answers only event 1.")
	assert.NotContains(t, first.Body, "nothing in it needs doing")
	require.NoError(t, ob.Flush(ctx))
	assert.Equal(t, IntentSent, obRetraction(t, ledger, completion, 1).State)

	// The second is decided later, and gets its own answer, naming the first
	// as the notice's other ask rather than claiming to answer it.
	clock.Advance(time.Minute)
	_, err = ledger.Discard(ctx, 2, "jorge")
	require.NoError(t, err)
	second := obRetraction(t, ledger, completion, 2)
	assert.Contains(t, second.Body, "It also asks about event 1; this answers only event 2.")
	require.NoError(t, ob.Flush(ctx))
	assert.Equal(t, IntentSent, obRetraction(t, ledger, completion, 2).State)
	assert.Len(t, basecamp.at(completion.Destination), 3, "the notice and one answer per event")
}

// Which events a posted message asks about, read off the words that went out.
func TestRedispatchAsksInReadsEveryAsk(t *testing.T) {
	two := renderCompletion(MessageComment, Settlement{TaskID: 3, AttemptID: "a1", Stop: StopFailed, Events: []SettledEvent{
		{EventID: 41, Outcome: OutcomeFailed},
		{EventID: 42, Outcome: OutcomeSucceeded, Reported: true},
		{EventID: 43, Outcome: OutcomeUnknown},
	}})
	assert.Equal(t, []int64{41, 43}, redispatchAsksIn(two), "the succeeded event is reported, not asked about")
	assert.Equal(t, []int64{7}, redispatchAsksIn(renderHoldingReply(MessageComment, 7)))
	assert.Empty(t, redispatchAsksIn(GuardAckBody))
	assert.Empty(t, redispatchAsksIn(renderStillRunning(MessageComment, 3, "a1", 1, time.Now(), time.Time{})))
	assert.Equal(t, "event 42", eventList([]int64{42}))
	assert.Equal(t, "events 42 and 43", eventList([]int64{42, 43}))
	assert.Equal(t, "events 42, 43 and 44", eventList([]int64{42, 43, 44}))
}

// A redispatch of a blocked record authorizes it; what settles the block is
// the prerequisite its caller runs next, and that can fail. Until the record
// has actually moved, the operator is still being told to redispatch, and
// nothing is posted: a line saying the ask is answered, under an ask that is
// still live, means nobody acts on it. Silence is better than that.
func TestARetractionWaitsWhileTheAskIsStillOpen(t *testing.T) {
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
	require.Equal(t, StateBlocked, getRecord(t, ledger, 1).State, "authorized, and still waiting on the route")

	require.NoError(t, ob.Flush(ctx))
	assert.Equal(t, IntentPending, obRetraction(t, ledger, holding, 1).State, "the prerequisite has not settled the block")
	assert.Len(t, basecamp.at(holding.Destination), 1, "nothing is said under an ask that is still live")

	// The route arrives and admission admits the record. Now the ask is
	// demonstrably answered, and only now.
	obRoutedNow(t, ledger, 1)
	clock.Advance(RetractionWait)
	require.NoError(t, ob.Flush(ctx))
	assert.Equal(t, IntentSent, obRetraction(t, ledger, holding, 1).State)
	assert.Len(t, basecamp.at(holding.Destination), 2)
}

// What "the ask is answered" means, state by state. It is the question the
// retraction asks again at its claim, so a wrong answer here posts a wrong
// message on somebody's card.
func TestAskStillOpenReadsWhatTheRecordIsWaitingFor(t *testing.T) {
	open := func(t *testing.T, ctx context.Context, ledger *Ledger, id int64) bool {
		t.Helper()
		open, found, err := askStillOpen(ctx, ledger.db, id)
		require.NoError(t, err)
		require.True(t, found)
		return open
	}

	t.Run("blocked waits for a person", func(t *testing.T) {
		ctx := context.Background()
		ledger, _ := obLedger(t)
		seenRecord(t, ledger, 1)
		_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
		require.NoError(t, err)
		assert.True(t, open(t, ctx, ledger, 1))
	})

	t.Run("admitted is running, and waits for nobody", func(t *testing.T) {
		ctx := context.Background()
		ledger, _ := obLedger(t)
		obAdmit(t, ledger, 1, "recording:10304028989")
		assert.False(t, open(t, ctx, ledger, 1))
	})

	t.Run("completed with an unreported outcome waits", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		obAdmit(t, ledger, 1, "recording:10304028989")
		l := obLaunch(t, ledger, 1)
		clock.Advance(time.Minute)
		_, err := ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopDeadline})
		require.NoError(t, err)
		assert.True(t, open(t, ctx, ledger, 1))

		_, err = ledger.Redispatch(ctx, 1, "jorge")
		require.NoError(t, err)
		assert.False(t, open(t, ctx, ledger, 1), "redispatched: it is going to run")
	})

	t.Run("discarded waits for nothing", func(t *testing.T) {
		ctx := context.Background()
		ledger, _ := obLedger(t)
		seenRecord(t, ledger, 1)
		_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
		require.NoError(t, err)
		_, err = ledger.Discard(ctx, 1, "jorge")
		require.NoError(t, err)
		assert.False(t, open(t, ctx, ledger, 1))
	})

	t.Run("a record the ledger does not hold answers nothing", func(t *testing.T) {
		ctx := context.Background()
		ledger, _ := obLedger(t)
		stillOpen, found, err := askStillOpen(ctx, ledger.db, 404)
		require.NoError(t, err)
		assert.False(t, found)
		assert.False(t, stillOpen)
	})
}

// Import's done entries are the same terminal decision the discard command
// makes — the file says a person finished the work — so a posted ask is
// answered by one too. Every path that decides a record runs the hook.
func TestImportedDoneRetractsAPostedAsk(t *testing.T) {
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

	clock.Advance(30 * time.Minute)
	_, err = ledger.Import(ctx, Reconciliation{Version: ReconciliationVersion,
		Entries: []ReconciliationEntry{{EventID: 1, Decision: DecisionDone}}}, "jorge")
	require.NoError(t, err)

	in := obRetraction(t, ledger, holding, 1)
	assert.Contains(t, in.Body, "A person closed event 1 instead, at 12:30 UTC")
	require.NoError(t, ob.Flush(ctx))
	assert.Equal(t, IntentSent, obRetraction(t, ledger, holding, 1).State)
	assert.Len(t, basecamp.at(holding.Destination), 2)
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

		// The notice lands: reconciliation adopts it, and once the route is
		// there too the retraction goes out behind it.
		receipt := basecamp.add(holding.Destination, adapterAgentID, holding.Body)
		_, err = ledger.settleReconciled(ctx, holding.ID, receipt, "")
		require.NoError(t, err)
		obRoutedNow(t, ledger, 1)
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
	obRoutedNow(t, ledger, 1)
	// The retraction goes out, and names the command in its own words. A
	// second decision does not answer it: a retraction asks for nothing, so
	// nothing retracts it.
	require.NoError(t, ob.Flush(ctx))
	require.Equal(t, IntentSent, obRetraction(t, ledger, holding, 1).State)

	// The record runs, ends unreported, and a person decides it again. Its
	// completion notice is still pending, so the only posted ask is the
	// holding reply, which has had its answer.
	clock.Advance(8 * time.Minute)
	l := obLaunch(t, ledger, 1)
	clock.Advance(time.Minute)
	_, err = ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopDeadline})
	require.NoError(t, err)
	require.Equal(t, IntentPending, obIntent(t, ledger, completionKey(l.AttemptID)).State)
	_, err = ledger.Redispatch(ctx, 1, "jorge")
	require.NoError(t, err)

	retractions := obRetractions(t, ledger)
	require.Len(t, retractions, 1)
	assert.Contains(t, retractions[0].Body, "A person ran it at 12:12 UTC", "the first answer stands; it is not rewritten")
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

// The rebuild copies the table, so the rows have to arrive on the other side
// exactly as they were: a lost receipt is a message the connector would post
// twice, and a lost id is one reconciliation can no longer match. A ledger at
// the previous schema, with a row in each state that matters, migrated for
// real.
func TestTheOutboxRebuildCarriesTheRowsOver(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "connector.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))

	old, err := sql.Open("sqlite", ledgerDSN(path, true))
	require.NoError(t, err)
	_, err = old.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`)
	require.NoError(t, err)
	for i := range len(migrations) - 1 {
		_, err = old.ExecContext(ctx, migrations[i])
		require.NoError(t, err, "migration %d", i+1)
		_, err = old.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, 'then')`, i+1)
		require.NoError(t, err)
	}
	_, err = old.ExecContext(ctx, `
INSERT INTO events (id, state, reason, lane, event_type, kind, action, bucket_id, creator_id, recording_id,
                    created_at, seen_at, updated_at)
VALUES (1, 'blocked', 'no_route', 'import', '', '', '', 48699913, 26909558, 10304028972, '2026-09-17T11:00:00.000000000Z', '2026-09-17T11:00:00.000000000Z', '2026-09-17T11:00:00.000000000Z')`)
	require.NoError(t, err)
	// A sent one with its receipt, a pending one, and one a person abandoned:
	// a receipt, a note and a resolver between them.
	_, err = old.ExecContext(ctx, `
INSERT INTO outbox (id, intent_key, kind, state, event_id, bucket_id, message_kind, recording_id, body,
                    created_at, not_before, sending_at, finished_at, receipt_id, note, resolved_by, reconcile_failures)
VALUES (7,  'holding_reply:event:1', 'holding_reply', 'sent',      1, 48699913, 'comment', 10304028989, 'first',  '2026-09-17T11:00:00.000000000Z', '2026-09-17T11:00:00.000000000Z', '2026-09-17T11:01:00.000000000Z', '2026-09-17T11:02:00.000000000Z', 555, '', '', 0),
       (8,  'guard_ack:event:1',     'guard_ack',     'pending',   1, 48699913, 'boost',   10304028972, 'second', '2026-09-17T11:00:00.000000000Z', '2026-09-17T11:00:00.000000000Z', NULL, NULL, NULL, '', '', 0),
       (9,  'completion:attempt:a1', 'completion',    'abandoned', 1, 48699913, 'comment', 10304028989, 'third',  '2026-09-17T11:00:00.000000000Z', '2026-09-17T11:00:00.000000000Z', '2026-09-17T11:01:00.000000000Z', '2026-09-17T11:02:00.000000000Z', NULL, 'unlistable', 'jorge', 3)`)
	require.NoError(t, err)
	require.NoError(t, old.Close())
	// The connector's own open makes the file private; a raw sql.Open does
	// not, and the privacy check refuses what it finds.
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(name); err == nil {
			require.NoError(t, os.Chmod(name, 0o600))
		}
	}

	ledger, err := OpenLedger(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ledger.Close() })
	version, err := ledger.SchemaVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, len(migrations), version, "the rebuild ran")

	carried, err := ledger.Intents(ctx, IntentFilter{})
	require.NoError(t, err)
	require.Len(t, carried, 3)
	byID := map[int64]Intent{}
	for _, in := range carried {
		byID[in.ID] = in
		assert.Zero(t, in.Retracts, "nothing written before the rebuild retracts anything")
	}

	sent := byID[7]
	assert.Equal(t, holdingKey(1), sent.Key)
	assert.Equal(t, IntentHoldingReply, sent.Kind)
	assert.Equal(t, IntentSent, sent.State)
	require.NotNil(t, sent.ReceiptID)
	assert.Equal(t, int64(555), *sent.ReceiptID)
	assert.Equal(t, "first", sent.Body)
	assert.Equal(t, Destination{BucketID: 48699913, Kind: MessageComment, RecordingID: 10304028989}, sent.Destination)
	require.NotNil(t, sent.SendingAt)
	require.NotNil(t, sent.FinishedAt)

	pending := byID[8]
	assert.Equal(t, IntentPending, pending.State)
	assert.Equal(t, MessageBoost, pending.Destination.Kind)
	assert.Nil(t, pending.SendingAt)
	assert.Nil(t, pending.ReceiptID)

	abandoned := byID[9]
	assert.Equal(t, IntentAbandoned, abandoned.State)
	assert.Equal(t, "unlistable", abandoned.Note)
	assert.Equal(t, "jorge", abandoned.ResolvedBy)
	assert.Equal(t, 3, abandoned.ReconcileFailures)
	assert.Equal(t, "completion:attempt:a1", abandoned.Key)

	// And the next id carries on past the copied rows rather than colliding
	// with one reconciliation already owns.
	res, err := ledger.db.ExecContext(ctx, `
INSERT INTO outbox (intent_key, kind, event_id, bucket_id, message_kind, recording_id, body, created_at, not_before)
VALUES ('holding_reply:refused:event:1', 'holding_reply', 1, 48699913, 'comment', 10304028989, 'fourth', '2026-09-17T11:00:00.000000000Z', '2026-09-17T11:00:00.000000000Z')`)
	require.NoError(t, err)
	next, err := res.LastInsertId()
	require.NoError(t, err)
	assert.Equal(t, int64(10), next, "the autoincrement sequence followed the highest copied id")
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
