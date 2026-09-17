package connector

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// The operator decisions and the hold (ledger_hold.go). Each test names the
// invariant it holds.

const (
	opRoute = "/work/connector"
	opBy    = "local:tester"
)

// opAdmit writes id seen and commits an admitted verdict on conversation key,
// returning the state the ledger wrote.
func opAdmit(t *testing.T, l *Ledger, id int64, key string) RecordState {
	t.Helper()
	ctx := context.Background()
	record := seenRecord(t, l, id)
	v := admittedVerdict(id, record.Revision, key)
	v.Route = opRoute
	state, err := l.Admission().Commit(ctx, v)
	require.NoError(t, err)
	return RecordState(state)
}

func launchOf(t *testing.T, l *Ledger, id int64) Launch {
	t.Helper()
	launch, err := l.LaunchTask(context.Background(), LaunchSpec{EventID: id, Route: opRoute, Driver: "claude"})
	require.NoError(t, err)
	return launch
}

func stateOf(t *testing.T, l *Ledger, id int64) RecordState {
	t.Helper()
	return getRecord(t, l, id).State
}

func decisionsFor(t *testing.T, l *Ledger, id int64) int {
	t.Helper()
	var n int
	require.NoError(t, l.db.QueryRow(`SELECT COUNT(*) FROM decisions WHERE event_id = ?`, id).Scan(&n))
	return n
}

// unknownOutcome takes a fresh record to completed(unknown): launched, running,
// lost.
func unknownOutcome(t *testing.T, l *Ledger, id int64) Launch {
	t.Helper()
	ctx := context.Background()
	require.Equal(t, StateAdmitted, opAdmit(t, l, id, "recording:"+itoa(id)))
	launch := launchOf(t, l, id)
	require.NoError(t, l.MarkRunning(ctx, launch.AttemptID, AttemptProcess{PID: 4242, PGID: 4242, StartedAt: time.Now()}))
	_, err := l.EndAttempt(ctx, AttemptEnd{AttemptID: launch.AttemptID, Stop: StopLost})
	require.NoError(t, err)
	require.Equal(t, StateCompleted, stateOf(t, l, id))
	return launch
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

// Done when: redispatch of completed(unknown) admits the record, supersedes
// the task's token and records who authorized it.
func TestRedispatchAdmitsAnUnknownOutcome(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	launch := unknownOutcome(t, l, 1)

	got, err := l.Redispatch(ctx, 1, opBy)
	require.NoError(t, err)
	assert.True(t, got.Admitted)
	assert.Equal(t, StateAdmitted, got.State)
	assert.Equal(t, OutcomeUnknown, got.FromOutcome)
	assert.Equal(t, 1, decisionsFor(t, l, 1))

	var by string
	require.NoError(t, l.db.QueryRow(`SELECT authorized_by FROM events WHERE id = 1`).Scan(&by))
	assert.Equal(t, opBy, by)
	d, err := l.Dispatch(launch.Token, adapterAgentID)
	require.NoError(t, err)
	_, _, err = d.Get(ctx, 1)
	assert.ErrorIs(t, err, ErrTaskTokenRefused, "the replaced task's token is refused")

	startable, err := l.StartableRecords(ctx, 10)
	require.NoError(t, err)
	require.Len(t, startable, 1)
	second := launchOf(t, l, 1)
	assert.NotEqual(t, launch.TaskID, second.TaskID)
}

// Done when: redispatch of completed(failed) admits it.
func TestRedispatchAdmitsAFailedOutcome(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	require.Equal(t, StateAdmitted, opAdmit(t, l, 1, "recording:1"))
	launch := launchOf(t, l, 1)
	d, err := l.Dispatch(launch.Token, adapterAgentID)
	require.NoError(t, err)
	_, err = d.Complete(ctx, 1, Completion{Outcome: OutcomeFailed})
	require.NoError(t, err)
	_, err = l.EndAttempt(ctx, AttemptEnd{AttemptID: launch.AttemptID, Stop: StopFinished})
	require.NoError(t, err)

	got, err := l.Redispatch(ctx, 1, opBy)
	require.NoError(t, err)
	assert.True(t, got.Admitted)
	assert.Equal(t, OutcomeFailed, got.FromOutcome)
	assert.Equal(t, StateAdmitted, stateOf(t, l, 1))
}

// Invariant 5: an event whose task is still live is not admitted until the
// task ends, so two workers never run for it.
func TestRedispatchOnALiveTaskWaitsForItsEnd(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	require.Equal(t, StateAdmitted, opAdmit(t, l, 1, "recording:9"))
	require.Equal(t, StateQueued, opAdmit(t, l, 2, "recording:9"))
	launch := launchOf(t, l, 1)
	started := time.Now().Add(-time.Minute).UTC()
	require.NoError(t, l.MarkRunning(ctx, launch.AttemptID, AttemptProcess{PID: 4242, PGID: 4242, StartedAt: started}))
	d, err := l.Dispatch(launch.Token, adapterAgentID)
	require.NoError(t, err)
	_, err = d.Complete(ctx, 1, Completion{Outcome: OutcomeFailed})
	require.NoError(t, err)

	got, err := l.Redispatch(ctx, 1, opBy)
	require.NoError(t, err)
	assert.True(t, got.Pending)
	assert.False(t, got.Admitted)
	assert.Equal(t, launch.TaskID, got.SupersededTaskID)
	require.NotNil(t, got.Worker, "the live worker is handed back to be terminated")
	assert.Equal(t, 4242, got.Worker.Process.PGID)
	assert.Equal(t, StateCompleted, stateOf(t, l, 1))

	_, _, err = d.Get(ctx, 2)
	assert.ErrorIs(t, err, ErrTaskTokenRefused, "the old worker is refused at once")
	_, err = l.LaunchTask(ctx, LaunchSpec{EventID: 1, Route: opRoute, Driver: "claude"})
	assert.ErrorIs(t, err, ErrNotStartable, "no second task while the first is live")
	startable, err := l.StartableRecords(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, startable)

	_, err = l.Redispatch(ctx, 1, opBy)
	assert.ErrorIs(t, err, ErrDecisionRefused, "a second redispatch while the first waits")

	_, err = l.EndAttempt(ctx, AttemptEnd{AttemptID: launch.AttemptID, Stop: StopLost})
	require.NoError(t, err)
	assert.Equal(t, StateAdmitted, stateOf(t, l, 1), "admitted in the transaction that ended the task")
	var pending int
	require.NoError(t, l.db.QueryRow(`SELECT redispatch_pending FROM events WHERE id = 1`).Scan(&pending))
	assert.Zero(t, pending)
	second := launchOf(t, l, 1)
	assert.NotEqual(t, launch.TaskID, second.TaskID)
}

// Invariant 6: refused for succeeded, discarded and anything live, and a
// refusal writes nothing.
func TestRedispatchRefusesWhatItMustNotRun(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(t *testing.T, l *Ledger){
		"seen":     func(t *testing.T, l *Ledger) { seenRecord(t, l, 1) },
		"admitted": func(t *testing.T, l *Ledger) { opAdmit(t, l, 1, "recording:1") },
		"queued": func(t *testing.T, l *Ledger) {
			opAdmit(t, l, 7, "recording:1")
			require.Equal(t, StateQueued, opAdmit(t, l, 1, "recording:1"))
		},
		"dispatched": func(t *testing.T, l *Ledger) {
			opAdmit(t, l, 1, "recording:1")
			launchOf(t, l, 1)
		},
		"discarded": func(t *testing.T, l *Ledger) {
			seenRecord(t, l, 1)
			require.NoError(t, l.SetState(ctx, 1, StateDiscarded, "untrusted_author"))
		},
		"succeeded": func(t *testing.T, l *Ledger) {
			opAdmit(t, l, 1, "recording:1")
			launch := launchOf(t, l, 1)
			d, err := l.Dispatch(launch.Token, adapterAgentID)
			require.NoError(t, err)
			_, err = d.Complete(ctx, 1, Completion{Outcome: OutcomeSucceeded})
			require.NoError(t, err)
			_, err = l.EndAttempt(ctx, AttemptEnd{AttemptID: launch.AttemptID, Stop: StopFinished})
			require.NoError(t, err)
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			l := newTestLedger(t)
			arrange(t, l)
			before := getRecord(t, l, 1)

			_, err := l.Redispatch(ctx, 1, opBy)
			require.ErrorIs(t, err, ErrDecisionRefused)
			after := getRecord(t, l, 1)
			assert.Equal(t, before.State, after.State)
			assert.Equal(t, before.Revision, after.Revision)
			assert.Zero(t, decisionsFor(t, l, 1))
		})
	}
}

// Done when: a blocked record keeps its state with the authorization
// recorded, and is admitted the moment its prerequisite succeeds.
func TestRedispatchOfABlockedRecordRerunsItsPrerequisite(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	seenRecord(t, l, 1)
	_, err := l.Admission().Commit(ctx, blockedVerdict(1, 0, admission.ReasonReadFailed))
	require.NoError(t, err)
	// A hold before the redispatch: the authorization is what lets it through.
	_, err = l.SetHold(ctx, opBy, HoldByOperator)
	require.NoError(t, err)

	got, err := l.Redispatch(ctx, 1, opBy)
	require.NoError(t, err)
	assert.True(t, got.Rerun)
	assert.True(t, got.Held)
	assert.Equal(t, StateBlocked, got.State)
	ids, err := l.AuthorizedBlocked(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, []int64{1}, ids)

	ev, ok, err := l.Admission().LoadUndecided(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)
	v := admittedVerdict(1, ev.Revision, "recording:1")
	v.Route = opRoute
	written, err := l.Admission().Commit(ctx, v)
	require.NoError(t, err)
	assert.Equal(t, admission.StateAdmitted, written, "authorized, so admitted though tagged for review")

	// Under the hold it is authorized and not launched.
	_, err = l.LaunchTask(ctx, LaunchSpec{EventID: 1, Route: opRoute, Driver: "claude"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "held")
	_, err = l.Release(ctx, opBy)
	require.NoError(t, err)
	launchOf(t, l, 1)
}

// Done when: a held record with its snapshot and route is admitted at once.
func TestRedispatchAdmitsAHeldRecord(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	opAdmit(t, l, 1, "recording:1")
	res, err := l.SetHold(ctx, opBy, HoldByOperator)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Held)
	require.Equal(t, StateHeld, stateOf(t, l, 1))

	got, err := l.Redispatch(ctx, 1, opBy)
	require.NoError(t, err)
	assert.True(t, got.Admitted)
	assert.Equal(t, StateAdmitted, stateOf(t, l, 1))
}

// A held record over a blocking reason runs what blocked it again.
func TestRedispatchOfARecordHeldOverAReasonRerunsIt(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	opAdmit(t, l, 1, "recording:1")
	_, err := l.SetHold(ctx, opBy, HoldByOperator)
	require.NoError(t, err)
	_, err = l.db.Exec(`UPDATE events SET reason = 'no_route' WHERE id = 1`)
	require.NoError(t, err)

	got, err := l.Redispatch(ctx, 1, opBy)
	require.NoError(t, err)
	assert.True(t, got.Rerun)
	record := getRecord(t, l, 1)
	assert.Equal(t, StateBlocked, record.State)
	assert.Equal(t, "no_route", record.Reason)
	assert.Nil(t, record.Decision.Snapshot, "a blocked record carries no content")
}

// Done when: a review-tagged seen record becomes held, not dispatched — and
// the verdict says so, so no guard acknowledgement is called for.
func TestInvariant1AReviewTaggedSeenRecordIsHeldNotDispatched(t *testing.T) {
	l := newTestLedger(t)
	l.SetHooks(LifecycleHooks(l, LifecycleOptions{}))
	ctx := context.Background()
	seenRecord(t, l, 1)
	_, err := l.SetHold(ctx, opBy, HoldByOperator)
	require.NoError(t, err)

	state := opAdmit(t, l, 1, "recording:1")
	assert.Equal(t, StateHeld, state)
	assert.Equal(t, StateHeld, stateOf(t, l, 1))
	startable, err := l.StartableRecords(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, startable)
	intents, err := l.Intents(ctx, IntentFilter{EventID: 1})
	require.NoError(t, err)
	assert.Empty(t, intents, "a held record calls for no guard acknowledgement")

	_, err = l.Release(ctx, opBy)
	require.NoError(t, err)
	assert.Equal(t, StateHeld, stateOf(t, l, 1), "held records stay held on release")
	startable, err = l.StartableRecords(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, startable)
}

// Records of the generation a hold opened are not tagged, and dispatch once
// the hold is released.
func TestANewGenerationIsNotTagged(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	_, err := l.SetHold(ctx, opBy, HoldByOperator)
	require.NoError(t, err)
	assert.Equal(t, StateAdmitted, opAdmit(t, l, 1, "recording:1"))
	_, err = l.Release(ctx, opBy)
	require.NoError(t, err)
	launchOf(t, l, 1)
}

// Invariant 1, for every path to admitted: a tagged sibling a task's end
// returns is held.
func TestInvariant1ATaggedSiblingReturnedByATaskIsHeld(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	opAdmit(t, l, 1, "recording:9")
	require.Equal(t, StateQueued, opAdmit(t, l, 2, "recording:9"))
	launch := launchOf(t, l, 1)
	_, err := l.SetHold(ctx, opBy, HoldByOperator)
	require.NoError(t, err)
	require.Equal(t, StateDispatched, stateOf(t, l, 2))

	settlement, err := l.EndAttempt(ctx, AttemptEnd{AttemptID: launch.AttemptID, Stop: StopShutdown})
	require.NoError(t, err)
	require.Len(t, settlement.Events, 2)
	assert.True(t, settlement.Events[1].Returned)
	assert.Equal(t, StateHeld, stateOf(t, l, 2))
}

// Invariant 1, at the database: any write of admitted onto a tagged,
// unauthorized record lands held.
func TestInvariant1TheDatabaseHoldsATaggedRecord(t *testing.T) {
	l := newTestLedger(t)
	opAdmit(t, l, 1, "recording:1")
	_, err := l.db.Exec(`UPDATE events SET state = 'blocked', reason = 'x' WHERE id = 1`)
	require.NoError(t, err)
	_, err = l.db.Exec(`UPDATE events SET review = 1 WHERE id = 1`)
	require.NoError(t, err)
	_, err = l.db.Exec(`UPDATE events SET state = 'admitted', reason = '' WHERE id = 1`)
	require.NoError(t, err)
	assert.Equal(t, StateHeld, stateOf(t, l, 1))
}

// Done when: a held ledger survives restart until release, and the database
// refuses a launch while it stands.
func TestInvariant2AHeldLedgerSurvivesRestartUntilRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", LedgerFile)
	ctx := context.Background()
	l, err := OpenLedger(path)
	require.NoError(t, err)
	_, err = l.SetHold(ctx, opBy, HoldByOperator)
	require.NoError(t, err)
	require.NoError(t, l.Close())

	l, err = OpenLedger(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	held, err := l.Held(ctx)
	require.NoError(t, err)
	assert.True(t, held)
	// A record of the new generation, which a person need not review, still
	// does not launch while the marker stands.
	assert.Equal(t, StateAdmitted, opAdmit(t, l, 1, "recording:1"))
	_, err = l.LaunchTask(ctx, LaunchSpec{EventID: 1, Route: opRoute, Driver: "claude"})
	require.Error(t, err)
	assert.Equal(t, StateAdmitted, stateOf(t, l, 1), "the refused launch rolled back")

	released, err := l.Release(ctx, opBy)
	require.NoError(t, err)
	assert.True(t, released.Released)
	held, err = l.Held(ctx)
	require.NoError(t, err)
	assert.False(t, held)
	launchOf(t, l, 1)
}

// Invariant 2: nothing moves to sending under the hold.
func TestInvariant2NothingIsPostedUnderTheHold(t *testing.T) {
	l := newTestLedger(t)
	l.SetHooks(LifecycleHooks(l, LifecycleOptions{}))
	ctx := context.Background()
	seenRecord(t, l, 1)
	v := blockedVerdict(1, 0, admission.ReasonNoRoute)
	v.Trigger, v.Acknowledge = admission.TriggerMentioned, true
	v.Reply = &admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 10304028989}
	_, err := l.Admission().Commit(ctx, v)
	require.NoError(t, err)
	pending, err := l.Intents(ctx, IntentFilter{States: []IntentState{IntentPending}})
	require.NoError(t, err)
	require.Len(t, pending, 1)

	_, err = l.SetHold(ctx, opBy, HoldByOperator)
	require.NoError(t, err)
	_, _, err = l.claimIntent(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "held")

	_, err = l.Release(ctx, opBy)
	require.NoError(t, err)
	claimed, ok, err := l.claimIntent(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, IntentSending, claimed.State)
}

// A held record's pending guard acknowledgement is not sent later.
func TestHoldingARecordCancelsItsPendingGuard(t *testing.T) {
	l := newTestLedger(t)
	l.SetHooks(LifecycleHooks(l, LifecycleOptions{}))
	ctx := context.Background()
	opAdmit(t, l, 1, "recording:1")
	guards, err := l.Intents(ctx, IntentFilter{Kinds: []IntentKind{IntentGuardAck}, States: []IntentState{IntentPending}})
	require.NoError(t, err)
	require.Len(t, guards, 1)

	_, err = l.SetHold(ctx, opBy, HoldByOperator)
	require.NoError(t, err)
	guard, err := l.Intent(ctx, guards[0].ID)
	require.NoError(t, err)
	assert.Equal(t, IntentCanceled, guard.State)
}

// Invariant 4: a terminal record leaves its state only with a decision
// written in the same statement.
func TestInvariant4TheDatabaseRefusesATerminalMoveWithoutADecision(t *testing.T) {
	ctx := context.Background()
	t.Run("completed to admitted without a redispatch", func(t *testing.T) {
		l := newTestLedger(t)
		unknownOutcome(t, l, 1)
		_, err := l.db.ExecContext(ctx, `UPDATE events SET state = 'admitted' WHERE id = 1`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "terminal")
	})
	t.Run("a redispatch of a success", func(t *testing.T) {
		l := newTestLedger(t)
		unknownOutcome(t, l, 1)
		_, err := l.db.ExecContext(ctx, `UPDATE task_events SET outcome = 'succeeded' WHERE event_id = 1`)
		require.NoError(t, err)
		_, err = l.db.ExecContext(ctx, `UPDATE events SET redispatch_pending = 1 WHERE id = 1`)
		require.NoError(t, err)
		_, err = l.db.ExecContext(ctx, `UPDATE events SET state = 'admitted', redispatch_pending = 0 WHERE id = 1`)
		require.Error(t, err)
	})
	t.Run("discarded never leaves", func(t *testing.T) {
		l := newTestLedger(t)
		seenRecord(t, l, 1)
		require.NoError(t, l.SetState(ctx, 1, StateDiscarded, ReasonByOperator))
		_, err := l.db.ExecContext(ctx, `UPDATE events SET state = 'admitted', redispatch_pending = 0 WHERE id = 1`)
		require.Error(t, err)
	})
	t.Run("an unknown outcome discarded for another reason", func(t *testing.T) {
		l := newTestLedger(t)
		unknownOutcome(t, l, 1)
		_, err := l.db.ExecContext(ctx, `UPDATE events SET state = 'discarded', reason = 'untrusted_author' WHERE id = 1`)
		require.Error(t, err)
	})
}

// Done when: discard closes held, blocked and unknown records as
// discarded(by_operator), and refuses the rest.
func TestDiscard(t *testing.T) {
	ctx := context.Background()
	accepted := map[string]func(t *testing.T, l *Ledger){
		"held": func(t *testing.T, l *Ledger) {
			opAdmit(t, l, 1, "recording:1")
			_, err := l.SetHold(ctx, opBy, HoldByOperator)
			require.NoError(t, err)
		},
		"blocked": func(t *testing.T, l *Ledger) {
			seenRecord(t, l, 1)
			_, err := l.Admission().Commit(ctx, blockedVerdict(1, 0, admission.ReasonReadFailed))
			require.NoError(t, err)
		},
		"unknown": func(t *testing.T, l *Ledger) { unknownOutcome(t, l, 1) },
	}
	for name, arrange := range accepted {
		t.Run(name, func(t *testing.T) {
			l := newTestLedger(t)
			arrange(t, l)
			got, err := l.Discard(ctx, 1, opBy)
			require.NoError(t, err)
			assert.False(t, got.Already)
			record := getRecord(t, l, 1)
			assert.Equal(t, StateDiscarded, record.State)
			assert.Equal(t, ReasonByOperator, record.Reason)
			assert.Equal(t, 1, decisionsFor(t, l, 1))

			again, err := l.Discard(ctx, 1, opBy)
			require.NoError(t, err)
			assert.True(t, again.Already)
			_, err = l.Redispatch(ctx, 1, opBy)
			assert.ErrorIs(t, err, ErrDecisionRefused)
		})
	}
	refused := map[string]func(t *testing.T, l *Ledger){
		"seen":     func(t *testing.T, l *Ledger) { seenRecord(t, l, 1) },
		"admitted": func(t *testing.T, l *Ledger) { opAdmit(t, l, 1, "recording:1") },
		"dispatched": func(t *testing.T, l *Ledger) {
			opAdmit(t, l, 1, "recording:1")
			launchOf(t, l, 1)
		},
		"failed": func(t *testing.T, l *Ledger) {
			opAdmit(t, l, 1, "recording:1")
			launch := launchOf(t, l, 1)
			d, err := l.Dispatch(launch.Token, adapterAgentID)
			require.NoError(t, err)
			_, err = d.Complete(ctx, 1, Completion{Outcome: OutcomeFailed})
			require.NoError(t, err)
		},
		"discarded by admission": func(t *testing.T, l *Ledger) {
			seenRecord(t, l, 1)
			require.NoError(t, l.SetState(ctx, 1, StateDiscarded, "untrusted_author"))
		},
	}
	for name, arrange := range refused {
		t.Run("refuses "+name, func(t *testing.T) {
			l := newTestLedger(t)
			arrange(t, l)
			before := getRecord(t, l, 1)
			_, err := l.Discard(ctx, 1, opBy)
			require.ErrorIs(t, err, ErrDecisionRefused)
			assert.Equal(t, before.State, stateOf(t, l, 1))
			assert.Zero(t, decisionsFor(t, l, 1))
		})
	}
}

// A discard cancels the lifecycle messages still pending for the record.
func TestDiscardCancelsAPendingHoldingReply(t *testing.T) {
	l := newTestLedger(t)
	l.SetHooks(LifecycleHooks(l, LifecycleOptions{}))
	ctx := context.Background()
	seenRecord(t, l, 1)
	v := blockedVerdict(1, 0, admission.ReasonNoRoute)
	v.Trigger, v.Acknowledge = admission.TriggerMentioned, true
	v.Reply = &admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 10304028989}
	_, err := l.Admission().Commit(ctx, v)
	require.NoError(t, err)

	got, err := l.Discard(ctx, 1, opBy)
	require.NoError(t, err)
	assert.Equal(t, 1, got.Canceled)
	pending, err := l.Intents(ctx, IntentFilter{States: []IntentState{IntentPending}})
	require.NoError(t, err)
	assert.Empty(t, pending)
}
