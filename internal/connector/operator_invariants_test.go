package connector

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// The operator decisions and the hold (ledger_hold.go). Each test names the
// invariant it holds.

const opBy = "local:tester"

// opAdmit writes id seen and commits an admitted verdict on conversation key,
// returning the state the ledger wrote.
func opAdmit(t *testing.T, l *Ledger, id int64, key string) RecordState {
	t.Helper()
	ctx := context.Background()
	record := seenRecord(t, l, id)
	v := admittedVerdict(id, record.Revision, key)
	state, err := l.Admission().Commit(ctx, v)
	require.NoError(t, err)
	return RecordState(state)
}

func stateOf(t *testing.T, l *Ledger, id int64) RecordState {
	t.Helper()
	return getRecord(t, l, id).State
}

func decisionsFor(t *testing.T, l *Ledger, id int64) int {
	t.Helper()
	var n int
	require.NoError(t, l.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM decisions WHERE event_id = ?`, id).Scan(&n))
	return n
}

// Done when: a review-tagged seen record becomes held, not admitted, and the
// verdict says so.
func TestInvariant1AReviewTaggedSeenRecordIsHeldNotDispatched(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	seenRecord(t, l, 1)
	_, err := l.SetHold(ctx, opBy, HoldByOperator)
	require.NoError(t, err)

	state := opAdmit(t, l, 1, "recording:1")
	assert.Equal(t, StateHeld, state)
	assert.Equal(t, StateHeld, stateOf(t, l, 1))

	_, err = l.Release(ctx, opBy)
	require.NoError(t, err)
	assert.Equal(t, StateHeld, stateOf(t, l, 1), "held records stay held on release")
}

// Records of the generation a hold opened are not tagged: they are admitted
// under the hold, and stay admitted once it is released.
func TestANewGenerationIsNotTagged(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	_, err := l.SetHold(ctx, opBy, HoldByOperator)
	require.NoError(t, err)
	assert.Equal(t, StateAdmitted, opAdmit(t, l, 1, "recording:1"))
	_, err = l.Release(ctx, opBy)
	require.NoError(t, err)
	assert.Equal(t, StateAdmitted, stateOf(t, l, 1))
	var review bool
	require.NoError(t, l.db.QueryRowContext(ctx, `SELECT review FROM events WHERE id = 1`).Scan(&review))
	assert.False(t, review)
}

// A record an older build's redispatch authorized is a person's decision the
// review tag does not override: admission writes it admitted though a later
// hold tagged it, because the trigger holds only a tagged record nobody
// authorized.
func TestAnAuthorizedRecordIsAdmittedThoughTagged(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	seenRecord(t, l, 1)
	_, err := l.Admission().Commit(ctx, blockedVerdict(1, 0, admission.ReasonReadFailed))
	require.NoError(t, err)
	_, err = l.SetHold(ctx, opBy, HoldByOperator)
	require.NoError(t, err)
	// As the removed Redispatch authorized a blocked record: never dated
	// before its block, and due now.
	now := l.timestamp()
	_, err = l.db.ExecContext(ctx, `
UPDATE events SET authorized_at = MAX(?, COALESCE(blocked_at, '')), authorized_by = ?, next_retry_at = ?
WHERE id = 1 AND state = 'blocked'`, now, opBy, now)
	require.NoError(t, err)

	ev, ok, err := l.Admission().LoadUndecided(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)
	written, err := l.Admission().Commit(ctx, admittedVerdict(1, ev.Revision, "recording:1"))
	require.NoError(t, err)
	assert.Equal(t, admission.StateAdmitted, written)
}

// Invariant 1, at the database: any write of admitted onto a tagged,
// unauthorized record lands held.
func TestInvariant1TheDatabaseHoldsATaggedRecord(t *testing.T) {
	l := newTestLedger(t)
	opAdmit(t, l, 1, "recording:1")
	_, err := l.db.ExecContext(context.Background(), `UPDATE events SET state = 'blocked', reason = 'x' WHERE id = 1`)
	require.NoError(t, err)
	_, err = l.db.ExecContext(context.Background(), `UPDATE events SET review = 1 WHERE id = 1`)
	require.NoError(t, err)
	_, err = l.db.ExecContext(context.Background(), `UPDATE events SET state = 'admitted', reason = '' WHERE id = 1`)
	require.NoError(t, err)
	assert.Equal(t, StateHeld, stateOf(t, l, 1))
}

// Done when: a held ledger survives restart until release.
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
	_, held, err := l.HoldMarker(ctx)
	require.NoError(t, err)
	assert.True(t, held)

	released, err := l.Release(ctx, opBy)
	require.NoError(t, err)
	assert.True(t, released.Released)
	_, held, err = l.HoldMarker(ctx)
	require.NoError(t, err)
	assert.False(t, held)
}

// Invariant 4: a terminal record leaves its state only with a decision
// written in the same statement.
//
//nolint:contextcheck // subtests build their fixtures on background contexts
func TestInvariant4TheDatabaseRefusesATerminalMoveWithoutADecision(t *testing.T) {
	ctx := context.Background()
	t.Run("completed to admitted without a redispatch", func(t *testing.T) {
		l := newTestLedger(t)
		olderUnknownOutcome(t, l, 1)
		_, err := l.db.ExecContext(ctx, `UPDATE events SET state = 'admitted' WHERE id = 1`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "terminal")
	})
	t.Run("a redispatch of a success", func(t *testing.T) {
		l := newTestLedger(t)
		olderUnknownOutcome(t, l, 1)
		decision := rawDecision(t, l, 1, "redispatch", "9999-01-01T00:00:00.000000000Z")
		_, err := l.db.ExecContext(ctx, `UPDATE task_events SET outcome = 'succeeded' WHERE event_id = 1`)
		require.NoError(t, err)
		_, err = l.db.ExecContext(ctx, `UPDATE events SET redispatch_decision = ? WHERE id = 1`, decision)
		require.NoError(t, err)
		_, err = l.db.ExecContext(ctx, `UPDATE events SET state = 'admitted', redispatch_decision = NULL WHERE id = 1`)
		require.Error(t, err)
	})
	t.Run("a redispatch naming another event's decision", func(t *testing.T) {
		l := newTestLedger(t)
		olderUnknownOutcome(t, l, 1)
		seenRecord(t, l, 2)
		decision := rawDecision(t, l, 2, "redispatch", "9999-01-01T00:00:00.000000000Z")
		_, err := l.db.ExecContext(ctx, `UPDATE events SET redispatch_decision = ? WHERE id = 1`, decision)
		require.NoError(t, err)
		_, err = l.db.ExecContext(ctx, `UPDATE events SET state = 'admitted', redispatch_decision = NULL WHERE id = 1`)
		require.Error(t, err)
	})
	t.Run("a redispatch decided before the outcome settled", func(t *testing.T) {
		l := newTestLedger(t)
		olderUnknownOutcome(t, l, 1)
		decision := rawDecision(t, l, 1, "redispatch", "2000-01-01T00:00:00.000000000Z")
		_, err := l.db.ExecContext(ctx, `UPDATE events SET redispatch_decision = ? WHERE id = 1`, decision)
		require.NoError(t, err)
		_, err = l.db.ExecContext(ctx, `UPDATE events SET state = 'admitted', redispatch_decision = NULL WHERE id = 1`)
		require.Error(t, err)
	})
	t.Run("a discard with no decision", func(t *testing.T) {
		l := newTestLedger(t)
		olderUnknownOutcome(t, l, 1)
		// Decisions that are not this record's discard do not stand in for one.
		seenRecord(t, l, 2)
		rawDecision(t, l, 2, "discard", "9999-01-01T00:00:00.000000000Z")
		rawDecision(t, l, 1, "redispatch", "9999-01-01T00:00:00.000000000Z")
		_, err := l.db.ExecContext(ctx, `UPDATE events SET state = 'discarded', reason = 'by_operator' WHERE id = 1`)
		require.Error(t, err)
	})
	t.Run("a discard decided before the outcome settled", func(t *testing.T) {
		l := newTestLedger(t)
		olderUnknownOutcome(t, l, 1)
		rawDecision(t, l, 1, "discard", "2000-01-01T00:00:00.000000000Z")
		_, err := l.db.ExecContext(ctx, `UPDATE events SET state = 'discarded', reason = 'by_operator' WHERE id = 1`)
		require.Error(t, err)
	})
	t.Run("discarded never leaves", func(t *testing.T) {
		l := newTestLedger(t)
		seenRecord(t, l, 1)
		require.NoError(t, l.SetState(ctx, 1, StateDiscarded, ReasonByOperator))
		decision := rawDecision(t, l, 1, "redispatch", "9999-01-01T00:00:00.000000000Z")
		_, err := l.db.ExecContext(ctx, `UPDATE events SET redispatch_decision = ? WHERE id = 1`, decision)
		require.NoError(t, err)
		_, err = l.db.ExecContext(ctx, `UPDATE events SET state = 'admitted', redispatch_decision = NULL WHERE id = 1`)
		require.Error(t, err)
	})
	t.Run("an unknown outcome discarded for another reason", func(t *testing.T) {
		l := newTestLedger(t)
		olderUnknownOutcome(t, l, 1)
		rawDecision(t, l, 1, "discard", "9999-01-01T00:00:00.000000000Z")
		_, err := l.db.ExecContext(ctx, `UPDATE events SET state = 'discarded', reason = 'untrusted_author' WHERE id = 1`)
		require.Error(t, err)
	})
}

// Done when: discard closes held, blocked and unknown records as
// discarded(by_operator), and refuses the rest.
//
//nolint:contextcheck // subtests build their fixtures on background contexts
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
		"unknown": func(t *testing.T, l *Ledger) { olderUnknownOutcome(t, l, 1) },
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
		})
	}
	refused := map[string]func(t *testing.T, l *Ledger){
		"seen":     func(t *testing.T, l *Ledger) { seenRecord(t, l, 1) },
		"admitted": func(t *testing.T, l *Ledger) { opAdmit(t, l, 1, "recording:1") },
		"dispatched": func(t *testing.T, l *Ledger) {
			opAdmit(t, l, 1, "recording:1")
			olderLaunch(t, l, 1)
		},
		"failed": func(t *testing.T, l *Ledger) {
			opAdmit(t, l, 1, "recording:1")
			olderReport(t, l, olderLaunch(t, l, 1), 1, OutcomeFailed)
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

// rawDecision writes a decisions row directly, as something other than this
// package could.
func rawDecision(t *testing.T, l *Ledger, eventID int64, action, at string) int64 {
	t.Helper()
	res, err := l.db.ExecContext(context.Background(), `INSERT INTO decisions (action, event_id, decided_by, decided_at) VALUES (?, ?, 'raw', ?)`, action, eventID, at)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// Retention never strands a redispatch an older build left waiting: the
// record keeps what its admission needs.
func TestRetentionKeepsARecordAWaitingRedispatchNeeds(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	olderPendingRedispatch(t, l)
	dropped, err := l.DropContent(ctx, time.Now().Add(24*time.Hour), time.Now().Add(24*time.Hour))
	require.NoError(t, err)
	assert.Zero(t, dropped)
	assert.False(t, getRecord(t, l, 1).ContentDropped)
}

// waitingRedispatch reports whether event 1 still names a redispatch, and who
// authorized it.
func waitingRedispatch(t *testing.T, l *Ledger) (bool, string) {
	t.Helper()
	var (
		waiting bool
		by      string
	)
	require.NoError(t, l.db.QueryRowContext(context.Background(), `SELECT redispatch_decision IS NOT NULL, authorized_by FROM events WHERE id = 1`).Scan(&waiting, &by))
	return waiting, by
}

// Invariant 3: a hold withdraws a redispatch still waiting for its task.
func TestInvariant3AHoldWithdrawsAWaitingRedispatch(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	olderPendingRedispatch(t, l)
	waiting, by := waitingRedispatch(t, l)
	require.True(t, waiting)
	require.Equal(t, opBy, by)

	_, err := l.SetHold(ctx, opBy, HoldByOperator)
	require.NoError(t, err)
	waiting, by = waitingRedispatch(t, l)
	assert.False(t, waiting, "the authorization did not survive the hold")
	assert.Empty(t, by)
	assert.Equal(t, StateCompleted, stateOf(t, l, 1))
}

// An import withdraws a waiting redispatch, whether the file says the entry is
// done or does not name it.
func TestImportWithdrawsAWaitingRedispatch(t *testing.T) {
	for name, c := range map[string]struct {
		entries []ReconciliationEntry
		want    RecordState
	}{
		"done":    {entries: []ReconciliationEntry{{EventID: 1, Decision: DecisionDone}}, want: StateDiscarded},
		"unnamed": {want: StateCompleted},
	} {
		t.Run(name, func(t *testing.T) {
			l := newTestLedger(t)
			ctx := context.Background()
			olderPendingRedispatch(t, l)
			_, err := l.Import(ctx, Reconciliation{Version: 1, Entries: c.entries}, opBy)
			require.NoError(t, err)

			waiting, _ := waitingRedispatch(t, l)
			assert.False(t, waiting, "the redispatch is withdrawn")
			assert.Equal(t, c.want, stateOf(t, l, 1))
		})
	}
}

// An import that closes a record cancels what an older build's outbox still
// had to post about it, so nothing asks for a decision the ledger would now
// refuse.
func TestImportCancelsTheMessagesOfARecordItCloses(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	seenRecord(t, l, 1)
	v := blockedVerdict(1, 0, admission.ReasonNoRoute)
	v.Trigger, v.Acknowledge = admission.TriggerMentioned, true
	v.Reply = &admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 10304028989}
	_, err := l.Admission().Commit(ctx, v)
	require.NoError(t, err)
	now := l.timestamp()
	_, err = l.db.ExecContext(ctx, `
INSERT INTO outbox (intent_key, kind, event_id, bucket_id, message_kind, recording_id, body, created_at, not_before)
VALUES ('guard_ack:1', 'guard_ack', 1, ?, 'comment', 10304028989, 'On it.', ?, ?)`, adapterBucketID, now, now)
	require.NoError(t, err)

	_, err = l.Import(ctx, Reconciliation{Version: 1, Entries: []ReconciliationEntry{{EventID: 1, Decision: DecisionDone}}}, opBy)
	require.NoError(t, err)
	var state string
	require.NoError(t, l.db.QueryRowContext(ctx, `SELECT state FROM outbox WHERE event_id = 1`).Scan(&state))
	assert.Equal(t, "canceled", state, "nothing is posted about a record a person closed")
}

// An import's done decision closes an unknown or failed outcome for good. A
// success stays as it was.
func TestImportDoneClosesAnOutcomeThatWaitedForAPerson(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	olderUnknownOutcome(t, l, 1)
	opAdmit(t, l, 2, "recording:2")
	task := olderLaunch(t, l, 2)
	olderReport(t, l, task, 2, OutcomeSucceeded)
	olderEnd(t, l, task, "finished")

	got, err := l.Import(ctx, Reconciliation{Version: 1, Entries: []ReconciliationEntry{
		{EventID: 1, Decision: DecisionDone}, {EventID: 2, Decision: DecisionDone},
	}}, opBy)
	require.NoError(t, err)
	assert.Equal(t, 1, got.Tombstoned)
	assert.Equal(t, 1, got.AlreadyTerminal)

	one := getRecord(t, l, 1)
	assert.Equal(t, StateDiscarded, one.State)
	assert.Equal(t, ReasonImportedDone, one.Reason)
	assert.Equal(t, StateCompleted, stateOf(t, l, 2))
	var recordedAs string
	require.NoError(t, l.db.QueryRowContext(ctx, `SELECT to_state FROM decisions WHERE event_id = 2 AND action = 'import'`).Scan(&recordedAs))
	assert.Equal(t, string(StateCompleted), recordedAs, "the audit says what happened, not what would have")
}

// A record an older build's redispatch left waiting for its task, discarded by
// a person, has been decided twice: the discard stands, and the redispatch is
// withdrawn with it.
func TestADiscardWithdrawsARedispatchWaitingForItsTask(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	olderPendingRedispatch(t, l)
	_, err := l.db.ExecContext(ctx, `UPDATE task_events SET outcome = 'unknown' WHERE event_id = 1`)
	require.NoError(t, err)

	_, err = l.Discard(ctx, 1, opBy)
	require.NoError(t, err)
	record := getRecord(t, l, 1)
	assert.Equal(t, StateDiscarded, record.State)
	assert.Equal(t, ReasonByOperator, record.Reason)
	waiting, _ := waitingRedispatch(t, l)
	assert.False(t, waiting, "the authorization went with the record")
}
