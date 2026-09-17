package connector

import (
	"context"
	"fmt"
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
				// place.
				for _, step := range deliveries[1 : slices.Index(deliveries, delivery)+1] {
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
				_, err = tx2.ExecContext(ctx, `UPDATE task_events SET withdrawn_at = 'again' WHERE event_id = 1`)
				require.Error(t, err, "once, whoever writes")
				_, err = tx2.ExecContext(ctx, `UPDATE task_events SET delivery = 'delivered' WHERE event_id = 1`)
				require.Error(t, err, "a withdrawn exposure moves no more")
			})
		}
	}
}

var allRecordStates = []RecordState{StateSeen, StateAdmitted, StateQueued, StateBlocked, StateDispatched, StateCompleted, StateDiscarded}

// recordTable is the record table: from → the states a move may reach, the
// state itself (a repeat) excluded. heldRecordTable is dispatched when a
// worker was handed the event.
var (
	recordTable = map[RecordState][]RecordState{
		StateSeen:       {StateAdmitted, StateQueued, StateBlocked, StateDiscarded},
		StateAdmitted:   {StateQueued, StateDispatched, StateBlocked, StateDiscarded},
		StateQueued:     {StateDispatched, StateBlocked, StateDiscarded},
		StateBlocked:    {StateAdmitted, StateQueued, StateDispatched, StateDiscarded},
		StateDispatched: {StateCompleted, StateBlocked, StateAdmitted},
		StateCompleted:  nil,
		StateDiscarded:  nil,
	}
	heldRecordTable = []RecordState{StateCompleted}
)

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
					if held {
						assert.ErrorIs(t, err, ErrHeldByWorker)
					} else {
						assert.ErrorIs(t, err, ErrNotATransition)
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
