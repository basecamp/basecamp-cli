package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrDecisionRefused is a decision the record's state does not accept: a
// discard, or an import's entry. The message says why.
var ErrDecisionRefused = errors.New("refused")

// eventTask is the latest task an event was on, as a decision reads it: a
// record a worker ran under an older build settled its outcome there.
type eventTask struct {
	found   bool
	outcome Outcome
	// completedAt is when the event's outcome settled, as stored.
	completedAt string
}

func loadEventTask(ctx context.Context, tx *sql.Tx, eventID int64) (eventTask, error) {
	var (
		et        eventTask
		outcome   string
		completed sql.NullString
	)
	err := tx.QueryRowContext(ctx, `
SELECT te.outcome, te.completed_at
FROM task_events te
JOIN tasks t ON t.id = te.task_id
WHERE te.event_id = ? AND te.withdrawn_at IS NULL
ORDER BY te.task_id DESC LIMIT 1`, eventID).Scan(&outcome, &completed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return eventTask{}, nil
	case err != nil:
		return eventTask{}, fmt.Errorf("connector: read the task of event %d: %w", eventID, err)
	}
	et.found = true
	et.outcome, et.completedAt = Outcome(outcome), completed.String
	return et, nil
}

// DiscardResult is what a discard did.
type DiscardResult struct {
	EventID     int64       `json:"event_id"`
	FromState   RecordState `json:"from_state"`
	FromReason  string      `json:"from_reason,omitempty"`
	FromOutcome Outcome     `json:"from_outcome,omitempty"`
	// Already says the record was discarded by a person before; nothing
	// changed.
	Already bool `json:"already_discarded,omitempty"`
	// Canceled counts lifecycle messages still pending for the event that
	// will not be sent.
	Canceled int `json:"canceled_messages"`
}

// Discard closes a held, blocked or unknown record without running it, as
// discarded(by_operator), and records who decided. Anything else is refused
// with ErrDecisionRefused.
func (l *Ledger) Discard(ctx context.Context, eventID int64, by string) (DiscardResult, error) {
	if strings.TrimSpace(by) == "" {
		return DiscardResult{}, errors.New("connector: a discard records who decided")
	}
	var out DiscardResult
	err := retryBusy(func() error {
		var err error
		out, err = l.discard(ctx, eventID, by)
		return err
	})
	return out, err
}

func (l *Ledger) discard(ctx context.Context, eventID int64, by string) (DiscardResult, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return DiscardResult{}, fmt.Errorf("connector: begin discard: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	record, err := loadRecord(ctx, tx, eventID)
	if err != nil {
		return DiscardResult{}, err
	}
	task, err := loadEventTask(ctx, tx, eventID)
	if err != nil {
		return DiscardResult{}, err
	}
	out := DiscardResult{EventID: eventID, FromState: record.State, FromReason: record.Reason, FromOutcome: task.outcome}
	refuse := func(why string) error {
		return fmt.Errorf("connector: discard of event %d %s: %w", eventID, why, ErrDecisionRefused)
	}
	switch record.State {
	case StateDiscarded:
		if record.Reason == ReasonByOperator {
			out.Already = true
			return out, nil
		}
		return DiscardResult{}, refuse(fmt.Sprintf("is already discarded (%s)", record.Reason))
	case StateCompleted:
		if !task.found || task.outcome != OutcomeUnknown {
			return DiscardResult{}, refuse(fmt.Sprintf("completed with outcome %q; only an unknown outcome is discarded", task.outcome))
		}
	case StateHeld, StateBlocked:
	default:
		return DiscardResult{}, refuse(fmt.Sprintf("is %s: only a held, blocked or unknown record is discarded", record.State))
	}

	at := l.now()
	now := stamp(at)
	// Recorded before the move, which the database allows out of completed
	// only against it (invariant 4).
	if err := recordDecision(ctx, tx, decision{action: "discard", eventID: eventID, by: by, at: notBefore(now, task.completedAt),
		fromState: record.State, fromReason: record.Reason, fromOutcome: task.outcome, toState: StateDiscarded}); err != nil {
		return DiscardResult{}, err
	}
	moved, err := l.move(ctx, tx, transition{id: eventID, state: StateDiscarded, reason: ReasonByOperator,
		from: []RecordState{StateHeld, StateBlocked, StateCompleted}, byOperator: true,
		set: []assignment{{column: "redispatch_decision", value: nil}}})
	if err != nil {
		return DiscardResult{}, err
	}
	if !moved {
		return DiscardResult{}, fmt.Errorf("connector: discard event %d: %w", eventID, ErrNotATransition)
	}
	// What the connector would still have said about this event is not said:
	// a guard acknowledgement or holding reply for a record a person closed.
	res, err := tx.ExecContext(ctx, `
UPDATE outbox SET state = 'canceled', finished_at = ?, note = 'discarded by a person'
WHERE event_id = ? AND state = 'pending' AND kind IN ('guard_ack', 'holding_reply')`, now, eventID)
	if err != nil {
		return DiscardResult{}, fmt.Errorf("connector: cancel lifecycle messages for %d: %w", eventID, err)
	}
	canceled, err := res.RowsAffected()
	if err != nil {
		return DiscardResult{}, err
	}
	out.Canceled = int(canceled)
	if err := tx.Commit(); err != nil {
		return DiscardResult{}, fmt.Errorf("connector: commit discard of %d: %w", eventID, err)
	}
	return out, nil
}

// notBefore is now, or the stored time an outcome settled when that is later:
// a decision is never recorded as made before the outcome it decides on, even
// with a clock that stepped back.
func notBefore(now, settled string) string {
	if settled > now {
		return settled
	}
	return now
}
