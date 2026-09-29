package connector

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

func id64(v int64) *int64 { return &v }

// Done when: each template renders from records alone. The body written with
// the intent is the one rendered again from the ledger's rows after commit.
func TestLifecycleTemplatesRenderFromRecordsAlone(t *testing.T) {
	t.Run("completion", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		obAdmit(t, ledger, 1, "recording:10304028989")
		l := obLaunch(t, ledger, 1)
		obAdmit(t, ledger, 2, "recording:10304028989")
		_, err := ledger.JoinConversation(ctx, l.TaskID, []int64{adapterBucketID})
		require.NoError(t, err)
		clock.Advance(5 * time.Minute)
		settlement, err := ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopDeadline})
		require.NoError(t, err)

		in := obIntent(t, ledger, completionKey(l.AttemptID))
		fromRows, err := settlementFromRecords(ctx, ledger.db, l.AttemptID)
		require.NoError(t, err)
		assert.Equal(t, in.Body, renderCompletion(in.Destination.Kind, fromRows))
		assert.Equal(t, in.Body, renderCompletion(in.Destination.Kind, settlement), "the ledger and the settlement agree")
		assert.Equal(t, Destination{BucketID: adapterBucketID, Kind: MessageComment, RecordingID: obReplyRecording}, in.Destination)
		assert.Equal(t,
			"<div>I ran out of time before I finished this.<br>"+
				"Mention me again to try again.<br>"+
				"<br>Ref 1 · attempt "+l.AttemptID+" · automatic notice from basecamp connect</div>",
			in.Body, "event 2 was never exposed: it waits for a task of its own and is not named")
	})

	t.Run("a worker that never picked the request up", func(t *testing.T) {
		for _, pulled := range []bool{false, true} {
			ctx := context.Background()
			ledger, _ := obLedger(t)
			obAdmit(t, ledger, 1, "recording:10304028989")
			l := obLaunch(t, ledger, 1)
			if pulled {
				d, err := ledger.Dispatch(ctx, l.Token, adapterAgentID)
				require.NoError(t, err)
				obPull(t, d, 1)
			}
			settlement, err := ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopFailed})
			require.NoError(t, err)

			in := obIntent(t, ledger, completionKey(l.AttemptID))
			fromRows, err := settlementFromRecords(ctx, ledger.db, l.AttemptID)
			require.NoError(t, err)
			assert.Equal(t, in.Body, renderCompletion(in.Destination.Kind, fromRows))
			assert.Equal(t, in.Body, renderCompletion(in.Destination.Kind, settlement), "the ledger and the settlement agree")
			if pulled {
				assert.Contains(t, MessageText(in.Body), "I stopped before I finished this. Mention me again to try again.")
			} else {
				assert.Contains(t, MessageText(in.Body), couldNotStart+" "+operatorChecks+" Ref 1 ·")
			}
		}
	})

	t.Run("still running", func(t *testing.T) {
		ctx := context.Background()
		ledger, clock := obLedger(t)
		obAdmit(t, ledger, 1, "recording:10304028989")
		l := obLaunch(t, ledger, 1)
		clock.Advance(3 * time.Minute)
		require.NoError(t, ledger.RecordProgress(ctx, l.AttemptID))
		clock.Advance(7 * time.Minute)
		tick, err := ledger.StillRunning(ctx, l.AttemptID)
		require.NoError(t, err)

		in := obIntent(t, ledger, stillRunningKey(l.AttemptID, 1))
		assert.Equal(t, IntentStillRunning, in.Kind)
		assert.Equal(t, renderStillRunning(MessageComment, l.TaskID, l.AttemptID, 1, in.CreatedAt, l.LaunchedAt, tick.ProgressAt), in.Body)
		assert.Equal(t,
			"<div>Working on this as of 12:10 UTC, started at 12:00 UTC. Last progress at 12:03 UTC.<br><br>"+
				"Ref attempt "+l.AttemptID+", update 1 · automatic notice from basecamp connect</div>", in.Body)
	})

	t.Run("holding reply in a Campfire", func(t *testing.T) {
		ctx := context.Background()
		ledger, _ := obLedger(t)
		seenRecord(t, ledger, 1)
		_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, admission.ReplyDestination{Kind: admission.ReplyChatLine, RecordingID: obCampfire}))
		require.NoError(t, err)
		in := obIntent(t, ledger, holdingKey(1))
		assert.Equal(t, Destination{BucketID: adapterBucketID, Kind: MessageChatLine, RecordingID: obCampfire}, in.Destination)
		assert.Equal(t, renderHoldingReply(MessageChatLine, 1), in.Body)
		assert.NotContains(t, in.Body, "<", "a chat line is plain text")
		assert.True(t, in.NotBefore.Equal(in.CreatedAt), "a holding reply is due at once")
	})

	t.Run("guard", func(t *testing.T) {
		ledger, _ := obLedger(t)
		obAdmit(t, ledger, 1, "recording:10304028989")
		in := obIntent(t, ledger, guardKey(1))
		assert.Equal(t, GuardAckBody, in.Body)
		assert.Equal(t, DefaultGuardDelay, in.NotBefore.Sub(in.CreatedAt))
	})
}

// A still-running notice can be the connector's last word on a task — an
// attempt whose events all succeeded with a reply calls for no completion
// notice — so it says when it was true and claims nothing about now. The
// stamp is the connector's own sighting of a live attempt, not the worker's
// last progress: a task can be alive and quiet for an hour, and both notices
// carry the sighting.
func TestStillRunningNoticeIsDatedAndNotPresentTense(t *testing.T) {
	ctx := context.Background()
	ledger, clock := obLedger(t)
	obAdmit(t, ledger, 1, "recording:10304028989")
	l := obLaunch(t, ledger, 1)

	clock.Advance(40 * time.Minute)
	_, err := ledger.StillRunning(ctx, l.AttemptID)
	require.NoError(t, err)
	quiet := obIntent(t, ledger, stillRunningKey(l.AttemptID, 1))
	assert.Contains(t, MessageText(quiet.Body),
		"Working on this as of 12:40 UTC, started at 12:00 UTC. No progress has been reported yet.",
		"an attempt with nothing to report is still dated by the sighting")

	require.NoError(t, ledger.RecordProgress(ctx, l.AttemptID))
	clock.Advance(20 * time.Minute)
	_, err = ledger.StillRunning(ctx, l.AttemptID)
	require.NoError(t, err)
	reported := obIntent(t, ledger, stillRunningKey(l.AttemptID, 2))
	assert.Contains(t, MessageText(reported.Body),
		"Working on this as of 13:00 UTC, started at 12:00 UTC. Last progress at 12:40 UTC.",
		"the sighting and the worker's last progress are different facts, both said")

	for _, in := range []Intent{quiet, reported} {
		assert.NotContains(t, in.Body, "Still working", in.Key,
			"no lifecycle message claims the present")
	}
}

// No template carries anything a person or worker wrote: the snapshot's
// content never reaches a lifecycle message.
func TestLifecycleMessagesCarryNoContent(t *testing.T) {
	ledger, _ := obLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	v := admittedVerdict(1, 0, "recording:10304028989")
	v.Snapshot.Title = "SECRET-TITLE"
	v.Snapshot.Content = "<div>SECRET-CONTENT</div>"
	_, err := ledger.Admission().Commit(ctx, v)
	require.NoError(t, err)
	l := obLaunch(t, ledger, 1)
	_, err = ledger.StillRunning(ctx, l.AttemptID)
	require.NoError(t, err)
	_, err = ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopFailed})
	require.NoError(t, err)
	seenRecord(t, ledger, 2)
	_, err = ledger.Admission().Commit(ctx, obNoRouteVerdict(2, 0, obCommentReply))
	require.NoError(t, err)

	intents := obIntents(t, ledger)
	require.Len(t, intents, 4)
	for _, in := range intents {
		assert.NotContains(t, in.Body, "SECRET", in.Key)
		assert.NotContains(t, in.Body, "https://", in.Key)
	}
}

const (
	couldNotStart  = "I couldn't start on this: something's wrong on the computer I run on."
	operatorChecks = "The person who runs me needs to check it."
)

// Completion: one notice per attempt, when anything is not succeeded with a
// reply. It tells the person in the thread what happened in plain words, and
// suggests mentioning the agent again only when that would help.
func TestCompletionNoticeRule(t *testing.T) {
	const sig = "automatic notice from basecamp connect"
	cases := []struct {
		name   string
		stop   StopReason
		events []SettledEvent
		want   string
	}{
		{name: "all succeeded with replies", events: []SettledEvent{
			{EventID: 1, Outcome: OutcomeSucceeded, Reported: true, ReplyID: id64(5)},
			{EventID: 2, Outcome: OutcomeSucceeded, Reported: true, ReplyID: id64(6)},
		}},
		{name: "succeeded without a reply", events: []SettledEvent{
			{EventID: 1, Outcome: OutcomeSucceeded, Reported: true},
		}, want: "I finished this, but didn't post a reply.\n\nRef 1 · attempt att_x · " + sig},
		{name: "failed and unknown suggest mentioning again", events: []SettledEvent{
			{EventID: 1, Outcome: OutcomeSucceeded, Reported: true, ReplyID: id64(5)},
			{EventID: 2, Outcome: OutcomeFailed, Reported: true},
			{EventID: 3, Outcome: OutcomeUnknown},
		}, want: "Something went wrong and I couldn't finish this. I stopped before I finished this.\n" +
			"Mention me again to try again.\n\nRef 2, 3 · attempt att_x · " + sig},
		{name: "failed with a reply has already said why", events: []SettledEvent{
			{EventID: 1, Outcome: OutcomeFailed, Reported: true, ReplyID: id64(6)},
		}},
		{name: "decided by a person asks for nothing", events: []SettledEvent{
			{EventID: 1, Outcome: OutcomeUnknown, Decided: true, Pulled: true},
		}, want: "I stopped before I finished this.\n\nRef 1 · attempt att_x · " + sig},
		{name: "only returned or withdrawn for a retry", events: []SettledEvent{
			{EventID: 1, Withdrawn: true},
			{EventID: 2, Returned: true},
		}},
		{name: "blocked after a second failed start", events: []SettledEvent{
			{EventID: 1, Withdrawn: true, Blocked: true},
		}, want: couldNotStart + "\n" + operatorChecks + "\n\nRef 1 · attempt att_x · " + sig},
		{name: "a worker that failed before it picked the request up", stop: StopFailed, events: []SettledEvent{
			{EventID: 1, Outcome: OutcomeUnknown},
		}, want: couldNotStart + "\n" + operatorChecks + "\n\nRef 1 · attempt att_x · " + sig},
		{name: "a worker that finished a turn without picking the request up", events: []SettledEvent{
			{EventID: 1, Outcome: OutcomeUnknown},
		}, want: "I stopped before I finished this.\nMention me again to try again.\n\nRef 1 · attempt att_x · " + sig},
		{name: "never picked up and decided by a person", stop: StopFailed, events: []SettledEvent{
			{EventID: 1, Outcome: OutcomeUnknown, Decided: true},
		}, want: couldNotStart + "\n" + operatorChecks + "\n\nRef 1 · attempt att_x · " + sig},
		{name: "never picked up before the connector was interrupted", stop: StopLost, events: []SettledEvent{
			{EventID: 1, Outcome: OutcomeUnknown},
		}, want: "I was interrupted before I finished this.\nMention me again to try again.\n\nRef 1 · attempt att_x · " + sig},
		{name: "never picked up before its deadline", stop: StopDeadline, events: []SettledEvent{
			{EventID: 1, Outcome: OutcomeUnknown},
		}, want: "I ran out of time before I finished this.\nMention me again to try again.\n\nRef 1 · attempt att_x · " + sig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stop := tc.stop
			if stop == "" {
				stop = StopFinished
			}
			s := Settlement{TaskID: 9, AttemptID: "att_x", Stop: stop, Events: tc.events}
			assert.Equal(t, tc.want != "", CompletionNeeded(s))
			assert.Equal(t, tc.want, renderCompletion(MessageChatLine, s))
		})
	}
}

// Why a request was left unfinished, in a reader's words: an interruption, the
// deadline, or an unexplained stop. Never "cancel", and never the machinery.
func TestUnfinishedSaysWhyInPlainWords(t *testing.T) {
	assert.Equal(t, unfinishedSentence(StopShutdown), unfinishedSentence(StopLost), "to a reader, a shutdown and a crash are both an interruption")
	sentences := map[string]bool{}
	for _, stop := range []StopReason{StopFinished, StopFailed, StopDeadline, StopShutdown, StopLost} {
		sentence := unfinishedSentence(stop)
		sentences[sentence] = true
		for _, jargon := range []string{"cancel", "worker", "task", "connector", "attempt"} {
			assert.NotContains(t, sentence, jargon, stop)
		}
	}
	assert.Len(t, sentences, 3, "interrupted, out of time, stopped")
}

// Notices are for the person in the thread: no commands, no config files, no
// machinery, and no ask a person has to come back and answer. The ids live in
// the signature line, where reconciliation matches a notice against Basecamp.
func TestNoticesSpeakToThePersonInTheThread(t *testing.T) {
	at := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	bodies := map[string]string{
		"holding reply": renderHoldingReply(MessageComment, 41),
		"still running": renderStillRunning(MessageComment, 7, "att_y", 1, at.Add(10*time.Minute), at, time.Time{}),
		"completion": renderCompletion(MessageComment, Settlement{TaskID: 7, AttemptID: "att_y", Stop: StopLost,
			Events: []SettledEvent{{EventID: 41, Outcome: OutcomeUnknown}}}),
		"never started": renderCompletion(MessageComment, Settlement{TaskID: 7, AttemptID: "att_y", Stop: StopFailed,
			Events: []SettledEvent{{EventID: 41, Outcome: OutcomeUnknown}}}),
	}
	for name, body := range bodies {
		text := MessageText(body)
		for _, jargon := range []string{"basecamp connect redispatch", "connect.json", "Needs a person", "worker", "Task 7", "Event 41"} {
			assert.NotContains(t, text, jargon, name)
		}
		assert.True(t, strings.HasSuffix(text, "· "+lifecycleSignature), name)
		assert.Empty(t, redispatchAsksIn(body), "%s asks nothing a person has to come back and answer", name)
	}
	assert.Contains(t, MessageText(bodies["holding reply"]), "Ref 41 ·")
	assert.Contains(t, MessageText(bodies["completion"]), "Ref 41 · attempt att_y ·")
	assert.NotContains(t, MessageText(bodies["never started"]), "Mention me again",
		"mentioning the agent again would meet the same computer")
}

// An attempt whose events all succeeded with replies posts nothing.
func TestCompletionIsNotWrittenWhenEverythingSucceededWithAReply(t *testing.T) {
	ledger, _ := obLedger(t)
	ctx := context.Background()
	obAdmit(t, ledger, 1, "recording:10304028989")
	l := obLaunch(t, ledger, 1)
	d, err := ledger.Dispatch(ctx, l.Token, adapterAgentID)
	require.NoError(t, err)
	obPull(t, d, 1)
	_, err = d.Complete(ctx, 1, Completion{Outcome: OutcomeSucceeded, ReplyID: id64(4242)})
	require.NoError(t, err)
	_, err = ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopFinished})
	require.NoError(t, err)
	for _, in := range obIntents(t, ledger) {
		assert.NotEqual(t, IntentCompletion, in.Kind)
	}
}

// Reconciliation compares words, not markup Basecamp may rewrite.
func TestMessageTextComparesWordsNotMarkup(t *testing.T) {
	body := renderHoldingReply(MessageComment, 7)
	stored := `<div dir="auto">` + strings.ReplaceAll(body, "<br>", "<br />\n") + `</div>`
	assert.Equal(t, MessageText(body), MessageText(stored))
	assert.Equal(t, MessageText(renderHoldingReply(MessageChatLine, 7)), MessageText(body), "a line and a comment say the same words")
	assert.NotEqual(t, MessageText(body), MessageText(renderHoldingReply(MessageComment, 8)))
	assert.Equal(t, "a & b", MessageText("<p>a &amp;\n b</p>"))
}

// The dispatch prompt's first instruction for a request is the worker's own
// acknowledgement, before any work, reported through ack_dispatch.
func TestDispatchPromptAcknowledgesFirst(t *testing.T) {
	record := Record{ID: 17, Decision: Decision{Trigger: "mentioned", Acknowledge: true, RecordingURL: "https://app.basecamp.com/2914079/buckets/1/recordings/2"}}
	prompt := DispatchPrompt(Launch{TaskID: 3}, record)
	ack := strings.Index(prompt, "acknowledge first")
	work := strings.Index(prompt, "Do the work")
	require.Positive(t, ack)
	require.Positive(t, work)
	assert.Less(t, ack, work)
	assert.Contains(t, prompt, "ack_dispatch")
	assert.Contains(t, prompt, "guard_acknowledged")
}

// A second failed start is read back from the ledger as blocked, and named.
func TestOutboxCompletionReadsBlockedBack(t *testing.T) {
	ledger, _ := obLedger(t)
	ctx := context.Background()
	obAdmit(t, ledger, 1, "recording:10304028989")
	for range 2 {
		l := obLaunch(t, ledger, 1)
		_, err := ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopFailed, SpawnFailed: true})
		require.NoError(t, err)
	}
	require.Equal(t, StateBlocked, getRecord(t, ledger, 1).State)
	completions, err := ledger.Intents(ctx, IntentFilter{Kinds: []IntentKind{IntentCompletion}})
	require.NoError(t, err)
	require.Len(t, completions, 1, "the first withdrawal retries quietly; the second needs a person")
	assert.Contains(t, MessageText(completions[0].Body), couldNotStart+" "+operatorChecks)
	assert.NotContains(t, completions[0].Body, "Mention me again", "a mention would meet the same computer")
}

// The holding reply answers only a request blocked for want of a route.
func TestOutboxHoldingReplyOnlyForNoRoute(t *testing.T) {
	ledger, _ := obLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	v := obNoRouteVerdict(1, 0, obCommentReply)
	v.Reason = admission.ReasonReadFailed
	_, err := ledger.Admission().Commit(ctx, v)
	require.NoError(t, err)
	assert.Empty(t, obIntents(t, ledger), "a failed read is not answered")

	_, err = ledger.Admission().Commit(ctx, obNoRouteVerdict(1, getRecord(t, ledger, 1).Revision, obCommentReply))
	require.NoError(t, err)
	in := obIntent(t, ledger, holdingKey(1))
	assert.Equal(t, Destination{BucketID: adapterBucketID, Kind: MessageComment, RecordingID: obReplyRecording}, in.Destination)
}

// The dispatcher's adopted-reply rule never adopts a lifecycle message.
func TestOutboxLifecycleMessagesAreRecognized(t *testing.T) {
	ledger, clock := obLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	_, err := ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
	require.NoError(t, err)
	basecamp := newFakeBasecamp(clock.Now)
	ob := obOutbox(t, ledger, basecamp)
	require.NoError(t, ob.Flush(ctx))
	receipt := *obIntent(t, ledger, holdingKey(1)).ReceiptID

	assert.True(t, ob.IsLifecycleMessage(receipt))
	assert.False(t, ob.IsLifecycleMessage(receipt+1))
	id, ok := AdoptableReply(AdoptionCandidate{DeliveredAt: clock.Now().Add(-time.Minute)},
		[]AgentReply{{ID: receipt, CreatedAt: clock.Now()}}, ob.IsLifecycleMessage)
	assert.False(t, ok, "adopted %d", id)
}
