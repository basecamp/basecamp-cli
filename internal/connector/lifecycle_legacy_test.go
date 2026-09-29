package connector

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// Notices as they were written before notices stopped asking a person to run
// `basecamp connect redispatch`. A ledger in use still holds notices like
// these, sent and standing on somebody's card, and a person acting on one is
// still answered (renderRetraction). These are the fixtures for that path:
// the words are the earlier version's, verbatim, and nothing here renders a
// notice the connector sends now.

func legacyHoldingReply(kind MessageKind, eventID int64) string {
	lines := []string{
		"I can't start on this here yet: this project is not one my connector is set up to work in, so nothing was run.",
		"Once the project is added to connect.json, a person can run it with: " + redispatchAsk(eventID),
		"",
		"Event " + strconv.FormatInt(eventID, 10) + " · " + lifecycleSignature,
	}
	return renderLines(kind, lines)
}

func legacyCompletionLine(e SettledEvent) string {
	id := strconv.FormatInt(e.EventID, 10)
	redispatch := " Needs a person: " + redispatchAsk(e.EventID)
	if e.Decided {
		redispatch = ""
	}
	switch {
	case e.Blocked:
		return "Event " + id + ": the worker could not be started." + redispatch
	case e.Withdrawn, e.Returned:
		return ""
	case e.Outcome == OutcomeFailed:
		return "Event " + id + ": failed." + redispatch
	case e.Outcome == OutcomeUnknown:
		return "Event " + id + ": unknown, the worker did not report on it." + redispatch
	case e.Outcome == OutcomeSucceeded && e.ReplyID == nil:
		return "Event " + id + ": succeeded, with no reply reported."
	}
	return ""
}

func legacyStopSentence(stop StopReason) string {
	switch stop {
	case StopFinished:
		return "the worker finished"
	case StopFailed:
		return "the worker failed"
	case StopDeadline:
		return "the worker was stopped at the task's deadline"
	case StopShutdown:
		return "the connector shut down and stopped the worker"
	case StopLost:
		return "the worker was lost"
	}
	return "the worker stopped"
}

func legacyCompletion(kind MessageKind, s Settlement) string {
	lines := []string{"Task " + strconv.FormatInt(s.TaskID, 10) + " ended: " + legacyStopSentence(s.Stop) + "."}
	for _, e := range s.Events {
		if line := legacyCompletionLine(e); line != "" {
			lines = append(lines, line)
		}
	}
	lines = append(lines, "", "Attempt "+s.AttemptID+" · "+lifecycleSignature)
	return renderLines(kind, lines)
}

// obPostedByAnEarlierVersion makes a sent notice read as an earlier version
// wrote it — asking a person to run redispatch — both in the ledger, which is
// what a retraction reads back, and in Basecamp, which is what a reader sees.
// basecamp may be nil when the test holds no fake.
func obPostedByAnEarlierVersion(t *testing.T, ledger *Ledger, basecamp *fakeBasecamp, in Intent) Intent {
	t.Helper()
	ctx := context.Background()
	require.Contains(t, []IntentState{IntentSent, IntentSending}, in.State,
		"a notice that went out, or may have: those are the ones a retraction reads back")

	var body string
	switch in.Kind {
	case IntentHoldingReply:
		body = legacyHoldingReply(in.Destination.Kind, in.EventID)
	case IntentCompletion:
		s, err := settlementFromRecords(ctx, ledger.db, in.AttemptID)
		require.NoError(t, err)
		body = legacyCompletion(in.Destination.Kind, s)
	default:
		t.Fatalf("no earlier wording for a %s notice", in.Kind)
	}
	require.NotEmpty(t, redispatchAsksIn(body), "an earlier notice asks")

	_, err := ledger.db.ExecContext(ctx, `UPDATE outbox SET body = ? WHERE id = ?`, body, in.ID)
	require.NoError(t, err)

	if basecamp != nil && in.ReceiptID != nil {
		basecamp.mu.Lock()
		messages := basecamp.messages[obKey(in.Destination)]
		for i := range messages {
			if messages[i].ID == *in.ReceiptID {
				messages[i].Content = body
			}
		}
		basecamp.mu.Unlock()
	}

	return obIntent(t, ledger, in.Key)
}
