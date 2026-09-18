package connector

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// TestDispatchLifecycleTable tries every transition of the dispatch lifecycle
// written at the top of ledger_dispatch.go — every pair, allowed and not —
// against the ledger, and checks the forbidden ones are refused. The tables
// here are that comment's, stated again independently of the code's own
// lifecycle map, so a change to either shows up as a disagreement.
func TestDispatchLifecycleTable(t *testing.T) {
	t.Run("record", testRecordTransitions)
	t.Run("delivery", testDeliveryTransitions)
	t.Run("guard", testGuardTransitions)
	t.Run("worker actions", testWorkerActions)
	t.Run("task", testTaskTransitions)
	t.Run("withdrawal", testWithdrawal)
	t.Run("a pull comes first", testDeliveryNeedsAPull)
}

// testWithdrawal: an exposure is withdrawn only on a superseded task, only
// while exposed, and only once — and then the record is work again.
func testWithdrawal(t *testing.T) {
	for _, superseded := range []bool{false, true} {
		for _, delivery := range deliveries {
			want := superseded && delivery == DeliveryExposed
			t.Run(fmt.Sprintf("at %s, superseded %v", delivery, superseded), func(t *testing.T) {
				f := newDispatchFixture(t)
				ctx := context.Background()
				// Staged along the allowed steps, so the triggers are left in
				// place. Past exposed, the steps are a worker's, so it pulled.
				for _, step := range deliveries[1 : slices.Index(deliveries, delivery)+1] {
					if step == DeliveryDelivered {
						_, err := f.ledger.db.ExecContext(ctx, `UPDATE task_events SET pulled_at = 'pulled' WHERE event_id = 1`)
						require.NoError(t, err)
					}
					_, err := f.ledger.db.ExecContext(ctx, `UPDATE task_events SET delivery = ? WHERE event_id = 1`, string(step))
					require.NoError(t, err)
				}
				tx, err := f.ledger.db.BeginTx(ctx, nil)
				require.NoError(t, err)
				defer func() { _ = tx.Rollback() }()
				if superseded {
					require.NoError(t, f.ledger.supersedeTask(ctx, tx, f.grant.ID))
				}

				err = f.ledger.withdrawExposure(ctx, tx, f.grant.ID, 1, StateAdmitted, "")

				if !want {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				require.NoError(t, tx.Commit())
				assert.Equal(t, StateAdmitted, getRecord(t, f.ledger, 1).State, "withdrawn, it is work again")

				tx2, err := f.ledger.db.BeginTx(ctx, nil)
				require.NoError(t, err)
				defer func() { _ = tx2.Rollback() }()
				require.Error(t, f.ledger.withdrawExposure(ctx, tx2, f.grant.ID, 1, StateAdmitted, ""), "once")
				require.Error(t, f.ledger.withdrawExposure(ctx, tx2, f.grant.ID, 2, StateAdmitted, ""), "a sibling never exposed has nothing to withdraw")
				// The database refuses the same, whoever writes.
				_, err = tx2.ExecContext(ctx, `UPDATE task_events SET withdrawn_at = 'raw' WHERE event_id = 2`)
				require.Error(t, err)
				_, err = tx2.ExecContext(ctx, `UPDATE task_events SET withdrawn_at = 'again' WHERE event_id = 1`)
				require.Error(t, err, "once, whoever writes")
				_, err = tx2.ExecContext(ctx, `UPDATE task_events SET delivery = 'delivered' WHERE event_id = 1`)
				require.Error(t, err, "a withdrawn exposure moves no more")
			})
		}
	}
}

var allRecordStates = []RecordState{StateSeen, StateAdmitted, StateQueued, StateBlocked, StateDispatched, StateCompleted, StateDiscarded}

// recordTable is the record table as a plain state write sees it: from → the
// states SetState may reach, the state itself (a repeat) excluded. Into
// dispatched and out of it is the task's business — a record enters only
// when a live task carries it, and leaves (but to completed) only when none
// does — so a plain write finds no way in, and from a dispatched record on a
// live task only completed. refusedFor says which refusal each such pair gets.
// heldRecordTable is dispatched when a worker was handed the event.
var (
	recordTable = map[RecordState][]RecordState{
		StateSeen:       {StateAdmitted, StateQueued, StateBlocked, StateDiscarded},
		StateAdmitted:   {StateQueued, StateBlocked, StateDiscarded},
		StateQueued:     {StateBlocked, StateDiscarded},
		StateBlocked:    {StateAdmitted, StateQueued, StateDiscarded},
		StateDispatched: {StateCompleted},
		StateCompleted:  nil,
		StateDiscarded:  nil,
	}
	heldRecordTable = []RecordState{StateCompleted}
)

// refusedFor is the refusal a pair outside the table gets.
func refusedFor(from, to RecordState, held bool) error {
	switch {
	case held && (to == StateAdmitted || to == StateBlocked):
		return ErrHeldByWorker
	case to == StateDispatched && slices.Contains([]RecordState{StateAdmitted, StateQueued, StateBlocked}, from):
		return ErrNotOnALiveTask
	case from == StateDispatched && (to == StateAdmitted || to == StateBlocked):
		return ErrOnALiveTask
	default:
		return ErrNotATransition
	}
}

// reachRecord puts event 1 in state, handed to a worker when held.
func reachRecord(t *testing.T, ledger *Ledger, state RecordState, held bool) {
	t.Helper()
	ctx := context.Background()
	commit := func(v admission.Verdict) {
		t.Helper()
		_, err := ledger.Admission().Commit(ctx, v)
		require.NoError(t, err)
	}
	seenRecord(t, ledger, 1)
	switch state {
	case StateSeen:
	case StateAdmitted:
		commit(admittedVerdict(1, 0, "recording:1"))
	case StateQueued:
		seenRecord(t, ledger, 9)
		commit(admittedVerdict(9, 0, "recording:1"))
		commit(admittedVerdict(1, 0, "recording:1"))
	case StateBlocked:
		commit(blockedVerdict(1, 0, admission.ReasonReadFailed))
	case StateDiscarded:
		v := blockedVerdict(1, 0, admission.ReasonStale)
		v.State = admission.StateDiscarded
		commit(v)
	case StateDispatched, StateCompleted:
		commit(admittedVerdict(1, 0, "recording:1"))
		grant, err := ledger.CreateTask(ctx, []int64{1})
		require.NoError(t, err)
		if held {
			d, err := ledger.Dispatch(ctx, grant.Token, adapterAgentID)
			require.NoError(t, err)
			_, _, err = d.Get(ctx, 1)
			require.NoError(t, err)
		}
		if state == StateCompleted {
			require.NoError(t, ledger.SetState(ctx, 1, StateCompleted, ""))
		}
	case StateHeld:
		// Held is written by a hold's review tag (ledger_hold.go), never by
		// a transition these tests drive.
		t.Fatalf("reachRecord does not build a %s record", state)
	}
	require.Equal(t, state, getRecord(t, ledger, 1).State)
}

func reasonFor(state RecordState) string {
	if state == StateBlocked || state == StateDiscarded {
		return "a_reason"
	}
	return ""
}

func testRecordTransitions(t *testing.T) {
	for _, held := range []bool{false, true} {
		for _, from := range allRecordStates {
			if held && from != StateDispatched {
				continue
			}
			allowed := recordTable[from]
			if held {
				allowed = heldRecordTable
			}
			for _, to := range allRecordStates {
				want := to == from || slices.Contains(allowed, to)
				t.Run(fmt.Sprintf("%s to %s, held %v", from, to, held), func(t *testing.T) {
					ledger := newTestLedger(t)
					reachRecord(t, ledger, from, held)

					err := ledger.SetState(context.Background(), 1, to, reasonFor(to))

					if want {
						require.NoError(t, err)
						assert.Equal(t, to, getRecord(t, ledger, 1).State)
						return
					}
					require.Error(t, err)
					assert.Equal(t, from, getRecord(t, ledger, 1).State, "a refused move moves nothing")
					assert.ErrorIs(t, err, refusedFor(from, to, held))
					// The dispatch and terminal rules are the database's too, so
					// they refuse whoever writes; the rest of the lifecycle map
					// is the ledger's write to keep.
					refusal := refusedFor(from, to, held)
					if refusal != ErrNotATransition || from == StateCompleted || from == StateDiscarded {
						_, rawErr := ledger.db.ExecContext(context.Background(), `UPDATE events SET state = ?, reason = ? WHERE id = 1`, string(to), reasonFor(to))
						assert.Error(t, rawErr, "a raw write of %s to %s", from, to)
					}
				})
			}
		}
	}
}

var deliveries = []Delivery{DeliveryAdmitted, DeliveryExposed, DeliveryDelivered, DeliveryCompleted}

// deliveryTable: from → the deliveries a row may move to, repeats excluded.
var deliveryTable = map[Delivery][]Delivery{
	DeliveryAdmitted:  {DeliveryExposed},
	DeliveryExposed:   {DeliveryDelivered, DeliveryCompleted},
	DeliveryDelivered: {DeliveryCompleted},
	DeliveryCompleted: nil,
}

// The delivery and guard tables are enforced by the database itself, so they
// are tried with raw writes: whatever writes to the file meets them.
func testDeliveryTransitions(t *testing.T) {
	for _, from := range deliveries {
		for _, to := range deliveries {
			want := to == from || slices.Contains(deliveryTable[from], to)
			t.Run(fmt.Sprintf("%s to %s", from, to), func(t *testing.T) {
				f := newDispatchFixture(t)
				ctx := context.Background()
				_, err := f.ledger.db.ExecContext(ctx, `DROP TRIGGER task_events_delivery_moves_forward`)
				require.NoError(t, err)
				_, err = f.ledger.db.ExecContext(ctx, `DROP TRIGGER task_events_exposure_comes_first`)
				require.NoError(t, err)
				// A worker pulled it: acknowledging and completing are what a
				// worker does with what it pulled.
				if from != DeliveryAdmitted {
					// Past admitted, a worker pulled it, which it does while
					// the row is exposed: acknowledging and completing are
					// what a worker does with what it pulled.
					_, err = f.ledger.db.ExecContext(ctx, `UPDATE task_events SET delivery = 'exposed' WHERE event_id = 1`)
					require.NoError(t, err)
					_, err = f.ledger.db.ExecContext(ctx, `UPDATE task_events SET pulled_at = 'pulled' WHERE event_id = 1`)
					require.NoError(t, err)
				}
				_, err = f.ledger.db.ExecContext(ctx, `UPDATE task_events SET delivery = ? WHERE event_id = 1`, string(from))
				require.NoError(t, err)
				reopened := f.ledger.restoreTriggers(t)

				_, err = reopened.db.ExecContext(ctx, `UPDATE task_events SET delivery = ? WHERE event_id = 1`, string(to))

				if want {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
					assert.Equal(t, string(from), f.row(t, 1).Delivery)
				}
			})
		}
	}
}

var guards = []string{"", "armed", "canceled", "fired"}

var guardTable = map[string][]string{
	"":         nil,
	"armed":    {"canceled", "fired"},
	"canceled": nil,
	"fired":    nil,
}

func testGuardTransitions(t *testing.T) {
	for _, from := range guards {
		for _, to := range guards {
			want := to == from || slices.Contains(guardTable[from], to)
			t.Run(fmt.Sprintf("%q to %q", from, to), func(t *testing.T) {
				f := newDispatchFixture(t)
				ctx := context.Background()
				_, err := f.ledger.db.ExecContext(ctx, `DROP TRIGGER task_events_guard_settles_once`)
				require.NoError(t, err)
				_, err = f.ledger.db.ExecContext(ctx, `UPDATE task_events SET guard = ? WHERE event_id = 1`, from)
				require.NoError(t, err)
				reopened := f.ledger.restoreTriggers(t)

				_, err = reopened.db.ExecContext(ctx, `UPDATE task_events SET guard = ? WHERE event_id = 1`, to)

				if want {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
					assert.Equal(t, from, f.row(t, 1).Guard)
				}
			})
		}
	}
}

// restoreTriggers puts back the triggers a test dropped to stage a row, by
// re-running migration 5's trigger statements.
func (l *Ledger) restoreTriggers(t *testing.T) *Ledger {
	t.Helper()
	ctx := context.Background()
	for _, name := range []string{"task_events_delivery_moves_forward", "task_events_exposure_comes_first", "task_events_guard_settles_once"} {
		var exists int
		require.NoError(t, l.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, name).Scan(&exists))
		if exists == 1 {
			continue
		}
		statement := triggerStatement(t, name)
		_, err := l.db.ExecContext(ctx, statement)
		require.NoError(t, err)
	}
	return l
}

// triggerStatement is a trigger's CREATE statement as the migrations declare it.
func triggerStatement(t *testing.T, name string) string {
	t.Helper()
	for _, migration := range migrations {
		start := strings.Index(migration, "CREATE TRIGGER "+name)
		if start < 0 {
			continue
		}
		end := strings.Index(migration[start:], "END;")
		require.GreaterOrEqual(t, end, 0)
		return migration[start : start+end+len("END;")]
	}
	t.Fatalf("no migration declares trigger %s", name)
	return ""
}

// testWorkerActions is the worker's side: each action against each delivery
// state, on a live task and on a superseded one.
func testWorkerActions(t *testing.T) {
	type outcome struct {
		err      error
		delivery Delivery
	}
	type action struct {
		name string
		do   func(*TaskDispatch) error
	}
	actions := []action{
		{"get", func(d *TaskDispatch) error { _, _, err := d.Get(context.Background(), 1); return err }},
		{"ack", func(d *TaskDispatch) error { _, err := d.Ack(context.Background(), 1, nil); return err }},
		{"complete", func(d *TaskDispatch) error {
			_, err := d.Complete(context.Background(), 1, Completion{Outcome: OutcomeSucceeded})
			return err
		}},
	}
	live := map[string]map[Delivery]outcome{
		"get": {
			DeliveryAdmitted: {nil, DeliveryExposed}, DeliveryExposed: {nil, DeliveryExposed},
			DeliveryDelivered: {nil, DeliveryDelivered}, DeliveryCompleted: {nil, DeliveryCompleted},
		},
		"ack": {
			DeliveryAdmitted: {ErrNotExposed, DeliveryAdmitted}, DeliveryExposed: {nil, DeliveryDelivered},
			DeliveryDelivered: {nil, DeliveryDelivered}, DeliveryCompleted: {nil, DeliveryCompleted},
		},
		"complete": {
			DeliveryAdmitted: {ErrNotExposed, DeliveryAdmitted}, DeliveryExposed: {nil, DeliveryCompleted},
			DeliveryDelivered: {nil, DeliveryCompleted}, DeliveryCompleted: {nil, DeliveryCompleted},
		},
	}
	reach := func(t *testing.T, f dispatchFixture, delivery Delivery) {
		t.Helper()
		ctx := context.Background()
		if delivery == DeliveryAdmitted {
			return
		}
		_, _, err := f.d.Get(ctx, 1)
		require.NoError(t, err)
		switch delivery {
		case DeliveryAdmitted, DeliveryExposed:
		case DeliveryDelivered:
			_, err = f.d.Ack(ctx, 1, nil)
		case DeliveryCompleted:
			_, err = f.d.Complete(ctx, 1, Completion{Outcome: OutcomeSucceeded})
		}
		require.NoError(t, err)
	}
	for _, superseded := range []bool{false, true} {
		for _, a := range actions {
			for _, delivery := range deliveries {
				t.Run(fmt.Sprintf("%s at %s, superseded %v", a.name, delivery, superseded), func(t *testing.T) {
					f := newDispatchFixture(t)
					reach(t, f, delivery)
					if superseded {
						require.NoError(t, f.ledger.SupersedeTask(context.Background(), f.grant.ID))
					}

					err := a.do(f.d)

					want := live[a.name][delivery]
					if superseded {
						want = outcome{ErrTaskTokenRefused, delivery}
					}
					if want.err == nil {
						require.NoError(t, err)
					} else {
						require.ErrorIs(t, err, want.err)
					}
					assert.Equal(t, string(want.delivery), f.row(t, 1).Delivery)
				})
			}
		}
	}
}

// A task's own columns are the database's too: supersession and retirement
// are final, and neither row is ever deleted.
func TestATaskIsSupersededNeverUnsupersededOrDeleted(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	require.NoError(t, f.ledger.SupersedeTask(ctx, f.grant.ID))

	for name, statement := range map[string]string{
		"un-supersede the task":    `UPDATE tasks SET superseded_at = NULL WHERE id = ?`,
		"delete the task":          `DELETE FROM tasks WHERE id = ?`, // its events reference it
		"un-retire its events":     `UPDATE task_events SET retired_at = NULL WHERE task_id = ?`,
		"delete its events":        `DELETE FROM task_events WHERE task_id = ?`,
		"change the retired stamp": `UPDATE task_events SET retired_at = 'later' WHERE task_id = ?`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.ledger.db.ExecContext(ctx, statement, f.grant.ID)
			require.Error(t, err)
		})
	}
	_, err := f.ledger.Dispatch(ctx, f.grant.Token, adapterAgentID)
	assert.ErrorIs(t, err, ErrTaskTokenRefused, "the token stays refused")
	assert.ErrorIs(t, f.ledger.SupersedeTask(ctx, 404), ErrNoSuchTask, "an unknown task is not silently superseded")
}

// testTaskTransitions: live to superseded, once, and a token valid only
// while its task is live.
func testTaskTransitions(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	_, err := f.ledger.Dispatch(ctx, f.grant.Token, adapterAgentID)
	require.NoError(t, err, "live: the token binds")

	require.NoError(t, f.ledger.SupersedeTask(ctx, f.grant.ID))
	_, err = f.ledger.Dispatch(ctx, f.grant.Token, adapterAgentID)
	require.ErrorIs(t, err, ErrTaskTokenRefused, "superseded: the token is refused")

	var first string
	require.NoError(t, f.ledger.db.QueryRowContext(ctx, `SELECT superseded_at FROM tasks WHERE id = ?`, f.grant.ID).Scan(&first))
	require.NoError(t, f.ledger.SupersedeTask(ctx, f.grant.ID), "superseded is terminal, and a repeat is harmless")
	var again string
	require.NoError(t, f.ledger.db.QueryRowContext(ctx, `SELECT superseded_at FROM tasks WHERE id = ?`, f.grant.ID).Scan(&again))
	assert.Equal(t, first, again)
}

// Retirement follows supersession, and a pull is recorded only on a live
// exposure — in the database, so a raw writer meets the same rules.
func TestTheDatabaseTiesRetirementAndPullsToTheirTask(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()

	_, err := f.ledger.db.ExecContext(ctx, `UPDATE task_events SET retired_at = 'now' WHERE event_id = 1`)
	require.Error(t, err, "a live task's events are not retired")

	require.NoError(t, f.ledger.SupersedeTask(ctx, f.grant.ID))
	_, err = f.ledger.db.ExecContext(ctx, `UPDATE task_events SET pulled_at = 'now' WHERE event_id = 1`)
	require.Error(t, err, "a retired exposure is not pulled")

	fresh := newDispatchFixture(t)
	_, err = fresh.ledger.db.ExecContext(ctx, `UPDATE task_events SET delivery = 'exposed' WHERE event_id = 1`)
	require.NoError(t, err)
	tx, err := fresh.ledger.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, fresh.ledger.supersedeTask(ctx, tx, fresh.grant.ID))
	require.NoError(t, fresh.ledger.withdrawExposure(ctx, tx, fresh.grant.ID, 1, StateAdmitted, ""))
	require.NoError(t, tx.Commit())
	_, err = fresh.ledger.db.ExecContext(ctx, `UPDATE task_events SET pulled_at = 'now' WHERE event_id = 1`)
	require.Error(t, err, "a withdrawn exposure is not pulled either")
}

// Acknowledging and completing are what a worker does with what it pulled. An
// exposure written at launch that no worker pulled moves no further, except
// where the dispatcher settles the record itself.
func testDeliveryNeedsAPull(t *testing.T) {
	for _, to := range []Delivery{DeliveryDelivered, DeliveryCompleted} {
		t.Run(string(to), func(t *testing.T) {
			f := newDispatchFixture(t)
			ctx := context.Background()
			_, err := f.ledger.db.ExecContext(ctx, `UPDATE task_events SET delivery = 'exposed' WHERE event_id = 1`)
			require.NoError(t, err)

			_, err = f.ledger.db.ExecContext(ctx, `UPDATE task_events SET delivery = ? WHERE event_id = 1`, string(to))
			require.Error(t, err, "nothing was pulled")

			// The dispatcher settling its record is the one other way to
			// completed.
			require.NoError(t, f.ledger.SetState(ctx, 1, StateCompleted, ""))
			_, err = f.ledger.db.ExecContext(ctx, `UPDATE task_events SET delivery = ? WHERE event_id = 1`, string(to))
			if to == DeliveryCompleted {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// A pull and a withdrawal are opposites: one says a worker has the
// instruction, the other that none ever did. No statement writes both, nor a
// pull together with retirement or a move — whichever of the delivery rules
// is the one that catches it. Which rule that is depends on the row's state;
// TestOneWriteCannotWithdrawAndComplete pins the case where the withdrawal
// rule's reading of the row being written is the only thing in the way.
func TestOneWriteCannotBothPullAndWithdraw(t *testing.T) {
	for name, statement := range map[string]string{
		"pull and withdraw": `UPDATE task_events SET pulled_at = 'now', withdrawn_at = 'now' WHERE event_id = 1`,
		"withdraw and pull": `UPDATE task_events SET withdrawn_at = 'now', pulled_at = 'now' WHERE event_id = 1`,
		"pull and retire":   `UPDATE task_events SET pulled_at = 'now', retired_at = 'now' WHERE event_id = 1`,
		"pull and move on":  `UPDATE task_events SET pulled_at = 'now', delivery = 'delivered' WHERE event_id = 1`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newDispatchFixture(t)
			_, err := f.ledger.db.ExecContext(ctx, `UPDATE task_events SET delivery = 'exposed' WHERE event_id = 1`)
			require.NoError(t, err)

			_, err = f.ledger.db.ExecContext(ctx, statement)

			require.Error(t, err)
			assert.Equal(t, "exposed", f.rowContext(ctx, t, 1).Delivery)
		})
	}
}

// A withdrawal says no worker process ever existed; a completed delivery says
// one reported an outcome. One statement does not write both — and here the
// only thing that says so is the withdrawal rule reading the row as it is
// being written: the task is superseded with no live row, nothing was pulled,
// and the record was settled by the dispatcher, so every other delivery rule
// lets this statement through.
func TestOneWriteCannotWithdrawAndComplete(t *testing.T) {
	ctx := context.Background()
	f := newDispatchFixture(t)
	_, err := f.ledger.db.ExecContext(ctx, `UPDATE task_events SET delivery = 'exposed', exposed_at = 'launch' WHERE event_id = 1`)
	require.NoError(t, err)
	require.NoError(t, f.ledger.SupersedeTask(ctx, f.grant.ID))
	require.NoError(t, f.ledger.SetState(ctx, 1, StateCompleted, ""))

	_, err = f.ledger.db.ExecContext(ctx, `UPDATE task_events SET withdrawn_at = 'now', delivery = 'completed' WHERE task_id = ? AND event_id = 1`, f.grant.ID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "only a launch exposure no worker pulled")
	var withdrawn *string
	require.NoError(t, f.ledger.db.QueryRowContext(ctx, `SELECT withdrawn_at FROM task_events WHERE task_id = ? AND event_id = 1`, f.grant.ID).Scan(&withdrawn))
	assert.Nil(t, withdrawn)
	assert.Equal(t, "exposed", f.rowContext(ctx, t, 1).Delivery)
}

// An acknowledgement id belongs to the statement that acknowledges. Writing it
// onto a row that stays exposed would leave an id in the receipt that no worker
// ever reported, which is the half of "with the acknowledgement or never" that
// checking only the old row cannot see.
func TestAnAcknowledgementIDIsNotWrittenWithoutTheAcknowledgement(t *testing.T) {
	ctx := context.Background()
	f := newDispatchFixture(t)
	_, err := f.ledger.db.ExecContext(ctx, `UPDATE task_events SET delivery = 'exposed', exposed_at = 'launch' WHERE event_id = 1`)
	require.NoError(t, err)

	_, err = f.ledger.db.ExecContext(ctx, `UPDATE task_events SET ack_id = 99 WHERE task_id = ? AND event_id = 1`, f.grant.ID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "an acknowledgement id is written with the acknowledgement, once")
	var ackID *int64
	require.NoError(t, f.ledger.db.QueryRowContext(ctx, `SELECT ack_id FROM task_events WHERE task_id = ? AND event_id = 1`, f.grant.ID).Scan(&ackID))
	assert.Nil(t, ackID)
	assert.Equal(t, "exposed", f.rowContext(ctx, t, 1).Delivery)
}

// And the acknowledgement itself still writes one: the rule narrows what may
// write an id, not whether Ack can.
func TestAcknowledgingWritesTheIDItWasGiven(t *testing.T) {
	ctx := context.Background()
	f := newDispatchFixture(t)
	_, err := f.ledger.db.ExecContext(ctx, `UPDATE task_events SET delivery = 'exposed', exposed_at = 'launch' WHERE event_id = 1`)
	require.NoError(t, err)

	d, err := f.ledger.Dispatch(ctx, f.grant.Token, adapterAgentID)
	require.NoError(t, err)
	_, _, err = d.Get(ctx, 1)
	require.NoError(t, err)
	ackID := int64(99)
	_, err = d.Ack(ctx, 1, &ackID)
	require.NoError(t, err)

	var got *int64
	require.NoError(t, f.ledger.db.QueryRowContext(ctx, `SELECT ack_id FROM task_events WHERE task_id = ? AND event_id = 1`, f.grant.ID).Scan(&got))
	require.NotNil(t, got)
	assert.Equal(t, int64(99), *got)
	assert.Equal(t, "delivered", f.rowContext(ctx, t, 1).Delivery)
}

// A ledger born under migration 5 carries that migration's trigger, which
// read only the row as it was. Editing migration 5 would have left every such
// ledger with it, because migrate skips what it has already applied — so the
// replacement is migration 6, and this is the upgrade actually happening.
func TestAnExistingLedgerGetsTheTighterAcknowledgementRule(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "connector.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))

	// A ledger as the previous version wrote it: migrations 1 through 5 and
	// nothing after them.
	old, err := sql.Open("sqlite", ledgerDSN(path, true))
	require.NoError(t, err)
	_, err = old.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		_, err = old.ExecContext(ctx, migrations[i])
		require.NoError(t, err, "migration %d", i+1)
		_, err = old.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, 'then')`, i+1)
		require.NoError(t, err)
	}
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
	assert.Equal(t, len(migrations), version, "the upgrade ran")

	// The same refusal the fresh-ledger test pins, on a ledger that was not
	// born with it.
	for _, id := range []int64{1, 2} {
		seenRecord(t, ledger, id)
		_, err := ledger.Admission().Commit(ctx, admittedVerdict(id, 0, "recording:10304028989"))
		require.NoError(t, err)
	}
	grant, err := ledger.CreateTask(ctx, []int64{1, 2})
	require.NoError(t, err)
	_, err = ledger.db.ExecContext(ctx, `UPDATE task_events SET delivery = 'exposed', exposed_at = 'launch' WHERE event_id = 1`)
	require.NoError(t, err)

	_, err = ledger.db.ExecContext(ctx, `UPDATE task_events SET ack_id = 99 WHERE task_id = ? AND event_id = 1`, grant.ID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "an acknowledgement id is written with the acknowledgement, once")
}
