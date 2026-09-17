package connector

import (
	"context"
	"strconv"
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
		_, err := ledger.JoinConversation(ctx, l.TaskID)
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
			"<div>Task "+itoa(l.TaskID)+" ended: the worker was stopped at the task&#39;s deadline.<br>"+
				"Event 1: unknown, the worker did not report on it. Needs a person: basecamp connect redispatch 1<br>"+
				"<br>Attempt "+l.AttemptID+" · automatic notice from basecamp connect</div>",
			in.Body, "event 2 was never exposed: it waits for a task of its own and is not named")
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
		assert.Equal(t, renderStillRunning(MessageComment, l.TaskID, l.AttemptID, 1, l.LaunchedAt, tick.ProgressAt), in.Body)
		assert.Equal(t,
			"<div>Still working on this: task "+itoa(l.TaskID)+" started at 12:00 UTC. Last progress at 12:03 UTC.<br><br>"+
				"Attempt "+l.AttemptID+", update 1 · automatic notice from basecamp connect</div>", in.Body)
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

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

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

// Completion: one notice per attempt, when anything is not succeeded with a
// reply; the notice names what needs redispatch.
func TestCompletionNoticeRule(t *testing.T) {
	cases := []struct {
		name   string
		events []SettledEvent
		want   []string
	}{
		{name: "all succeeded with replies", events: []SettledEvent{
			{EventID: 1, Outcome: OutcomeSucceeded, Reported: true, ReplyID: id64(5)},
			{EventID: 2, Outcome: OutcomeSucceeded, Reported: true, ReplyID: id64(6)},
		}},
		{name: "succeeded without a reply", events: []SettledEvent{
			{EventID: 1, Outcome: OutcomeSucceeded, Reported: true},
		}, want: []string{"Event 1: succeeded, with no reply reported."}},
		{name: "failed and unknown need redispatch", events: []SettledEvent{
			{EventID: 1, Outcome: OutcomeSucceeded, Reported: true, ReplyID: id64(5)},
			{EventID: 2, Outcome: OutcomeFailed, Reported: true, ReplyID: id64(6)},
			{EventID: 3, Outcome: OutcomeUnknown},
		}, want: []string{
			"Event 2: failed. Needs a person: basecamp connect redispatch 2",
			"Event 3: unknown, the worker did not report on it. Needs a person: basecamp connect redispatch 3",
		}},
		{name: "only returned or withdrawn for a retry", events: []SettledEvent{
			{EventID: 1, Withdrawn: true},
			{EventID: 2, Returned: true},
		}},
		{name: "blocked after a second failed start", events: []SettledEvent{
			{EventID: 1, Withdrawn: true, Blocked: true},
		}, want: []string{"Event 1: the worker could not be started, again. Needs a person: basecamp connect redispatch 1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := Settlement{TaskID: 9, AttemptID: "att_x", Stop: StopFinished, Events: tc.events}
			body := renderCompletion(MessageChatLine, s)
			assert.Equal(t, len(tc.want) > 0, CompletionNeeded(s))
			if len(tc.want) == 0 {
				assert.Empty(t, body)
				return
			}
			assert.Equal(t, "Task 9 ended: the worker finished.\n"+strings.Join(tc.want, "\n")+"\n\nAttempt att_x · automatic notice from basecamp connect", body)
		})
	}
}

// Every stop reason reads as itself; a failure is never called a cancel.
func TestCompletionNamesEachStopReason(t *testing.T) {
	seen := map[string]bool{}
	for _, stop := range []StopReason{StopFinished, StopFailed, StopDeadline, StopShutdown, StopLost} {
		sentence := stopSentence(stop)
		assert.NotEqual(t, "the worker stopped", sentence, stop)
		assert.NotContains(t, sentence, "cancel", stop)
		assert.False(t, seen[sentence], stop)
		seen[sentence] = true
	}
}

// An attempt whose events all succeeded with replies posts nothing.
func TestCompletionIsNotWrittenWhenEverythingSucceededWithAReply(t *testing.T) {
	ledger, _ := obLedger(t)
	ctx := context.Background()
	obAdmit(t, ledger, 1, "recording:10304028989")
	l := obLaunch(t, ledger, 1)
	d, err := ledger.Dispatch(ctx, l.Token, adapterAgentID)
	require.NoError(t, err)
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
	assert.Contains(t, completions[0].Body, "Event 1: the worker could not be started, again. Needs a person: basecamp connect redispatch 1")
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
