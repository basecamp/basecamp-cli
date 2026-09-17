package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrDecisionRefused is a redispatch or discard the record's state does not
// accept. The message says why.
var ErrDecisionRefused = errors.New("refused")

// eventTask is the latest task an event was on, as a decision reads it.
type eventTask struct {
	found      bool
	taskID     int64
	delivery   Delivery
	outcome    Outcome
	superseded bool
	ended      bool
	// completedAt is when the event's outcome settled, as stored.
	completedAt string
	// live is the task's attempt that has not ended, if any, with its
	// recorded process.
	liveAttempt string
	process     AttemptProcess
}

func loadEventTask(ctx context.Context, tx *sql.Tx, eventID int64) (eventTask, error) {
	var (
		et                   eventTask
		delivery, outcome    string
		superseded, ended    sql.NullString
		completed            sql.NullString
		attempt, startedText sql.NullString
		pid, pgid            sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `
SELECT te.task_id, te.delivery, te.outcome, te.completed_at, t.superseded_at, t.ended_at,
       a.id, a.pid, a.pgid, a.process_started
FROM task_events te
JOIN tasks t ON t.id = te.task_id
LEFT JOIN attempts a ON a.task_id = t.id AND a.state <> 'ended'
WHERE te.event_id = ? AND te.withdrawn_at IS NULL
ORDER BY te.task_id DESC LIMIT 1`, eventID).Scan(&et.taskID, &delivery, &outcome, &completed, &superseded, &ended,
		&attempt, &pid, &pgid, &startedText)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return eventTask{}, nil
	case err != nil:
		return eventTask{}, fmt.Errorf("connector: read the task of event %d: %w", eventID, err)
	}
	et.found = true
	et.delivery, et.outcome = Delivery(delivery), Outcome(outcome)
	et.superseded, et.ended, et.completedAt = superseded.Valid, ended.Valid, completed.String
	if attempt.Valid {
		et.liveAttempt = attempt.String
		et.process = AttemptProcess{PID: int(pid.Int64), PGID: int(pgid.Int64)}
		if startedText.Valid {
			if et.process.StartedAt, err = parseStamp(startedText.String); err != nil {
				return eventTask{}, err
			}
		}
	}
	return et, nil
}

// operatorRecord is a record with the columns a decision reads.
type operatorRecord struct {
	Record
	review       bool
	authorizedAt sql.NullString
	// redispatchDecision is the redispatch waiting for the record's task to
	// end; zero when none is.
	redispatchDecision int64
}

func loadOperatorRecord(ctx context.Context, tx *sql.Tx, eventID int64) (operatorRecord, error) {
	record, err := loadRecord(ctx, tx, eventID)
	if err != nil {
		return operatorRecord{}, err
	}
	out := operatorRecord{Record: record}
	var decision sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT review, authorized_at, redispatch_decision FROM events WHERE id = ?`, eventID).
		Scan(&out.review, &out.authorizedAt, &decision); err != nil {
		return operatorRecord{}, fmt.Errorf("connector: read event %d: %w", eventID, err)
	}
	out.redispatchDecision = decision.Int64
	return out, nil
}

// RedispatchResult is what a redispatch did.
type RedispatchResult struct {
	EventID     int64
	FromState   RecordState
	FromReason  string
	FromOutcome Outcome
	// State is the record's state after the authorization.
	State RecordState
	// Admitted says the record waits for a worker now.
	Admitted bool
	// Pending says the record's task is still live: it is admitted in the
	// transaction that ends that task.
	Pending bool
	// Rerun says the record was authorized as blocked: the caller runs its
	// prerequisite again (admission), which admits it when it succeeds.
	Rerun bool
	// SupersededTaskID is the task whose token this redispatch retired; zero
	// when it was already retired.
	SupersededTaskID int64
	// Worker is the replaced attempt's recorded process, still live in the
	// ledger: the caller terminates it (driver.TerminateRecorded).
	Worker *LiveWorker
	// Held says the hold marker stands: authorized, and nothing launches
	// until release.
	Held bool
}

// LiveWorker is an attempt's recorded worker process.
type LiveWorker struct {
	AttemptID string
	TaskID    int64
	Process   AttemptProcess
}

// Redispatch authorizes a record to run again, or for the first time, and
// records who authorized it (invariants 4 to 6).
//
//   - completed with outcome unknown or failed: the task's token is
//     superseded; admitted at once when the task has ended, otherwise when it
//     ends. Refused without a snapshot or a route.
//   - held with its snapshot and route and no blocking reason: admitted.
//   - blocked, or held over a blocking reason: authorized as blocked, and
//     Rerun asks the caller to run what blocked it.
//   - succeeded, discarded, and anything live (seen, admitted, queued,
//     dispatched) are refused with ErrDecisionRefused.
func (l *Ledger) Redispatch(ctx context.Context, eventID int64, by string) (RedispatchResult, error) {
	if strings.TrimSpace(by) == "" {
		return RedispatchResult{}, errors.New("connector: a redispatch records who authorized it")
	}
	var out RedispatchResult
	err := retryBusy(func() error {
		var err error
		out, err = l.redispatch(ctx, eventID, by)
		return err
	})
	return out, err
}

func (l *Ledger) redispatch(ctx context.Context, eventID int64, by string) (RedispatchResult, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return RedispatchResult{}, fmt.Errorf("connector: begin redispatch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	record, err := loadOperatorRecord(ctx, tx, eventID)
	if err != nil {
		return RedispatchResult{}, err
	}
	task, err := loadEventTask(ctx, tx, eventID)
	if err != nil {
		return RedispatchResult{}, err
	}
	out := RedispatchResult{EventID: eventID, FromState: record.State, FromReason: record.Reason, FromOutcome: task.outcome}
	refuse := func(why string) error {
		return fmt.Errorf("connector: redispatch of event %d %s: %w", eventID, why, ErrDecisionRefused)
	}
	dispatchable := !record.ContentDropped && len(record.Decision.Snapshot) > 0 && record.Decision.Routed && record.Decision.ConversationKey != ""
	now := l.timestamp()
	authorize := []assignment{{column: "authorized_at", value: now}, {column: "authorized_by", value: by}}
	recorded := false

	switch record.State {
	case StateSeen, StateAdmitted, StateQueued, StateDispatched:
		return RedispatchResult{}, refuse(fmt.Sprintf("is %s: it is live, and runs without one", record.State))
	case StateDiscarded:
		return RedispatchResult{}, refuse(fmt.Sprintf("is discarded (%s)", record.Reason))

	case StateCompleted:
		switch {
		case !task.found || task.delivery != DeliveryCompleted:
			return RedispatchResult{}, refuse("has no settled outcome to redispatch")
		case task.outcome == OutcomeSucceeded:
			return RedispatchResult{}, refuse("succeeded; a success is not run again")
		case task.outcome != OutcomeUnknown && task.outcome != OutcomeFailed:
			return RedispatchResult{}, refuse(fmt.Sprintf("has outcome %q", task.outcome))
		case record.redispatchDecision != 0:
			return RedispatchResult{}, refuse("already has a redispatch waiting for its task to end")
		case !dispatchable:
			return RedispatchResult{}, refuse("no longer has the snapshot and route a dispatch needs (retention dropped them, or the verdict carried none)")
		}
		if !task.superseded {
			// The replaced worker is refused by basecamp_connect from here on
			// (invariant 5).
			if _, err := tx.ExecContext(ctx, `UPDATE tasks SET superseded_at = ? WHERE id = ? AND superseded_at IS NULL`, now, task.taskID); err != nil {
				return RedispatchResult{}, fmt.Errorf("connector: supersede task %d: %w", task.taskID, err)
			}
			out.SupersededTaskID = task.taskID
		}
		if task.liveAttempt != "" {
			out.Worker = &LiveWorker{AttemptID: task.liveAttempt, TaskID: task.taskID, Process: task.process}
		}
		to := StateCompleted
		if task.ended {
			to = StateAdmitted
		}
		// The decision is the authorization the database checks: the record
		// names it, and only a decision made after the outcome settled lets a
		// completed record move (invariant 4).
		decisionID, err := insertDecision(ctx, tx, decision{action: "redispatch", eventID: eventID, by: by, at: notBefore(now, task.completedAt),
			fromState: record.State, fromReason: record.Reason, fromOutcome: task.outcome, toState: to,
			supersededTask: out.SupersededTaskID, note: pendingNote(task)})
		if err != nil {
			return RedispatchResult{}, err
		}
		recorded = true
		if _, err := tx.ExecContext(ctx, `UPDATE events SET authorized_at = ?, authorized_by = ?, redispatch_decision = ? WHERE id = ?`, now, by, decisionID, eventID); err != nil {
			return RedispatchResult{}, fmt.Errorf("connector: authorize event %d: %w", eventID, err)
		}
		if task.ended {
			moved, err := l.move(ctx, tx, transition{id: eventID, state: StateAdmitted, from: []RecordState{StateCompleted}, byOperator: true,
				set: []assignment{{column: "redispatch_decision", value: nil}}})
			if err != nil {
				return RedispatchResult{}, err
			}
			if !moved {
				return RedispatchResult{}, fmt.Errorf("connector: admit event %d: %w", eventID, ErrNotATransition)
			}
			out.Admitted = true
		} else {
			out.Pending = true
		}

	case StateHeld:
		if record.Reason == "" && dispatchable {
			// Queued behind a live conversation, as admission would write it.
			target := StateAdmitted
			var live bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE conversation_key = ? AND id <> ? AND state IN ('admitted', 'dispatched'))`,
				record.Decision.ConversationKey, eventID).Scan(&live); err != nil {
				return RedispatchResult{}, fmt.Errorf("connector: read conversation of %d: %w", eventID, err)
			}
			if live {
				target = StateQueued
			}
			moved, err := l.move(ctx, tx, transition{id: eventID, state: target, from: []RecordState{StateHeld}, byOperator: true, set: authorize})
			if err != nil {
				return RedispatchResult{}, err
			}
			if !moved {
				return RedispatchResult{}, fmt.Errorf("connector: admit event %d: %w", eventID, ErrNotATransition)
			}
			out.Admitted = target == StateAdmitted
			break
		}
		reason := record.Reason
		if reason == "" {
			reason = "held_incomplete"
		}
		moved, err := l.move(ctx, tx, transition{id: eventID, state: StateBlocked, reason: reason, from: []RecordState{StateHeld}, byOperator: true})
		if err != nil {
			return RedispatchResult{}, err
		}
		if !moved {
			return RedispatchResult{}, fmt.Errorf("connector: authorize event %d: %w", eventID, ErrNotATransition)
		}
		if err := authorizeBlocked(ctx, tx, eventID, now, by); err != nil {
			return RedispatchResult{}, err
		}
		out.Rerun = true

	case StateBlocked:
		// The record keeps its state; what blocked it runs again. Writing
		// the authorization is not a state change and leaves the revision
		// the re-run loads at.
		if err := authorizeBlocked(ctx, tx, eventID, now, by); err != nil {
			return RedispatchResult{}, err
		}
		out.Rerun = true

	default:
		return RedispatchResult{}, refuse(fmt.Sprintf("is in a state %q this build does not know", record.State))
	}

	if err := tx.QueryRowContext(ctx, `SELECT state FROM events WHERE id = ?`, eventID).Scan(&out.State); err != nil {
		return RedispatchResult{}, fmt.Errorf("connector: read event %d back: %w", eventID, err)
	}
	if _, out.Held, err = readHold(ctx, tx); err != nil {
		return RedispatchResult{}, err
	}
	if !recorded {
		note := ""
		if out.Rerun {
			note = "prerequisite runs again"
		}
		if err := recordDecision(ctx, tx, decision{action: "redispatch", eventID: eventID, by: by, at: now,
			fromState: record.State, fromReason: record.Reason, fromOutcome: task.outcome, toState: out.State,
			supersededTask: out.SupersededTaskID, note: note}); err != nil {
			return RedispatchResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return RedispatchResult{}, fmt.Errorf("connector: commit redispatch of %d: %w", eventID, err)
	}
	return out, nil
}

// DiscardResult is what a discard did.
type DiscardResult struct {
	EventID     int64
	FromState   RecordState
	FromReason  string
	FromOutcome Outcome
	// Already says the record was discarded by a person before; nothing
	// changed.
	Already bool
	// Canceled counts lifecycle messages still pending for the event that
	// will not be sent.
	Canceled int
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

	record, err := loadOperatorRecord(ctx, tx, eventID)
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

	now := l.timestamp()
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

// AuthorizedBlocked lists blocked records a person authorized, oldest first:
// authorized since the record entered its current run of blocked states, so
// an authorization that answered an earlier outcome does not count.
// The redispatch command runs the prerequisite itself; this is for the
// blocked-record recovery schedule to run it again when that did not settle
// it (the schedule is plan step 22's, and nothing calls this yet).
func (l *Ledger) AuthorizedBlocked(ctx context.Context, limit int) ([]int64, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT id FROM events WHERE state = 'blocked' AND authorized_at >= blocked_at ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("connector: authorized blocked records: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
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

func pendingNote(task eventTask) string {
	if task.ended {
		return ""
	}
	return fmt.Sprintf("waits for task %d to end", task.taskID)
}

// authorizeBlocked records a person's authorization on a blocked record, never
// dated before the record entered its current run of blocked states: an
// authorization counts for that block only when it is not older than it
// (AuthorizedBlocked), and neither a move's own later stamp nor a clock that
// stepped back may make a fresh one look stale.
func authorizeBlocked(ctx context.Context, tx *sql.Tx, eventID int64, now, by string) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE events SET authorized_at = MAX(?, COALESCE(blocked_at, '')), authorized_by = ?
WHERE id = ? AND state = 'blocked'`, now, by, eventID); err != nil {
		return fmt.Errorf("connector: authorize event %d: %w", eventID, err)
	}
	return nil
}
