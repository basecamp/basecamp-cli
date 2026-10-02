package connector

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// What an older build's worker side left in a ledger.
//
// This build writes no task, attempt or task event: the dispatcher that wrote
// them went with #815. The ledgers people have still hold those rows, and this
// build still reads them (status), decides about them (discard, import, a
// hold) and moves records out of the states they left (retention, the
// lifecycle's own guards). The helpers here write the rows the removed
// dispatcher wrote, with the statements it used and in its order, so the
// schema's triggers hold them to the same rules and those paths are tested
// against the ledgers that exist.

// olderTask is a task an older build launched, and its first attempt.
type olderTask struct {
	TaskID    int64
	AttemptID string
}

// olderDispatch puts records on a new task as the removed CreateTask did: at
// delivery admitted, the acknowledgement guard armed where the verdict asked
// for one, each record moved to dispatched in the same transaction. A record a
// test walked to admitted by hand has no instruction, so one is attached
// first; a seen record is admitted first.
func olderDispatch(t *testing.T, l *Ledger, ids ...int64) int64 {
	t.Helper()
	ctx := context.Background()
	for _, id := range ids {
		_, err := l.db.ExecContext(ctx, `UPDATE events
SET snapshot = COALESCE(snapshot, CAST('{"content":"do it"}' AS BLOB)),
    conversation_key = CASE WHEN conversation_key = '' THEN 'recording:' || id ELSE conversation_key END
WHERE id = ?`, id)
		require.NoError(t, err)
		if getRecord(t, l, id).State == StateSeen {
			require.NoError(t, l.SetState(ctx, id, StateAdmitted, ""))
		}
	}
	var taskID int64
	inTx(t, l, func(ctx context.Context, tx *sql.Tx) { taskID = writeOlderTask(ctx, t, l, tx, ids) })
	return taskID
}

// olderLaunch is the removed LaunchTask: a task for the records, its first
// attempt launching, and the first record exposed to the worker about to
// start. The records are admitted or queued, with their verdicts.
func olderLaunch(t *testing.T, l *Ledger, ids ...int64) olderTask {
	t.Helper()
	var out olderTask
	inTx(t, l, func(ctx context.Context, tx *sql.Tx) {
		out.TaskID = writeOlderTask(ctx, t, l, tx, ids)
		var key string
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT conversation_key FROM events WHERE id = ?`, ids[0]).Scan(&key))
		now := l.timestamp()
		_, err := tx.ExecContext(ctx, `
UPDATE tasks SET conversation_key = ?, driver = 'claude', originating_event_id = ? WHERE id = ?`, key, ids[0], out.TaskID)
		require.NoError(t, err)
		out.AttemptID = fmt.Sprintf("att_%024x", out.TaskID)
		_, err = tx.ExecContext(ctx, `
INSERT INTO attempts (id, task_id, seq, driver, state, launched_at) VALUES (?, ?, 1, 'claude', 'launching', ?)`,
			out.AttemptID, out.TaskID, now)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `
UPDATE task_events SET delivery = 'exposed', exposed_at = ?, exposed_attempt_id = ?
WHERE task_id = ? AND event_id = ?`, now, out.AttemptID, out.TaskID, ids[0])
		require.NoError(t, err)
	})
	return out
}

// writeOlderTask is the removed createTask's writes, in its order.
func writeOlderTask(ctx context.Context, t *testing.T, l *Ledger, tx *sql.Tx, ids []int64) int64 {
	t.Helper()
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	res, err := tx.ExecContext(ctx, `INSERT INTO tasks (token_sha256, created_at) VALUES (?, ?)`, hex.EncodeToString(raw), l.timestamp())
	require.NoError(t, err)
	taskID, err := res.LastInsertId()
	require.NoError(t, err)
	for _, id := range ids {
		var (
			acknowledge  bool
			conversation string
		)
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT acknowledge, conversation_key FROM events WHERE id = ?`, id).Scan(&acknowledge, &conversation))
		guard := ""
		if acknowledge {
			guard = "armed"
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO task_events (task_id, event_id, guard, conversation_key) VALUES (?, ?, ?, ?)`, taskID, id, guard, conversation)
		require.NoError(t, err)
	}
	for _, id := range ids {
		moved, err := l.move(ctx, tx, transition{id: id, state: StateDispatched, from: []RecordState{StateAdmitted, StateQueued, StateDispatched}})
		require.NoError(t, err)
		require.True(t, moved, "event %d joins the task", id)
	}
	return taskID
}

// olderRunning is the removed MarkRunning: the attempt running, with its
// worker's process. A zero started records the pid with no start time, as a
// build that could not read the kernel's did.
func olderRunning(t *testing.T, l *Ledger, attemptID string, pid int, started time.Time) {
	t.Helper()
	var stamped any
	if !started.IsZero() {
		stamped = stamp(started)
	}
	res, err := l.db.ExecContext(context.Background(), `
UPDATE attempts SET state = 'running', running_at = ?, pid = ?, pgid = ?, process_started = ?
WHERE id = ? AND state = 'launching'`, l.timestamp(), pid, pid, stamped, attemptID)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "attempt %s was launching", attemptID)
}

// olderTakerUnaccounted is the removed MarkTakerUnaccounted: the task token
// went out and the process holding it could not be named.
func olderTakerUnaccounted(t *testing.T, l *Ledger, attemptID string) {
	t.Helper()
	_, err := l.db.ExecContext(context.Background(), `UPDATE attempts SET taker_unaccounted = 1 WHERE id = ? AND state <> 'ended'`, attemptID)
	require.NoError(t, err)
}

// olderReport is a worker pulling an event and completing it with outcome, as
// the removed get_dispatch and complete_dispatch wrote it: the delivery
// first, the record after.
func olderReport(t *testing.T, l *Ledger, task olderTask, eventID int64, outcome Outcome) {
	t.Helper()
	inTx(t, l, func(ctx context.Context, tx *sql.Tx) {
		now := l.timestamp()
		_, err := tx.ExecContext(ctx, `UPDATE task_events SET delivery = 'exposed', exposed_at = ? WHERE task_id = ? AND event_id = ? AND delivery = 'admitted'`, now, task.TaskID, eventID)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `UPDATE task_events SET pulled_at = ? WHERE task_id = ? AND event_id = ? AND pulled_at IS NULL`, now, task.TaskID, eventID)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `UPDATE task_events SET guard = 'canceled' WHERE task_id = ? AND event_id = ? AND guard = 'armed'`, task.TaskID, eventID)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `
UPDATE task_events
SET delivery = 'completed', delivered_at = COALESCE(delivered_at, ?), completed_at = ?, outcome = ?
WHERE task_id = ? AND event_id = ?`, now, now, string(outcome), task.TaskID, eventID)
		require.NoError(t, err)
		moved, err := l.move(ctx, tx, transition{id: eventID, state: StateCompleted, from: []RecordState{StateDispatched}})
		require.NoError(t, err)
		require.True(t, moved)
	})
}

// olderEnd is the removed EndAttempt, for a worker that started: the attempt
// ended with stop, every exposed event the worker did not report settled
// completed(unknown), the task superseded, the events it never exposed
// returned to admitted, and the task ended.
func olderEnd(t *testing.T, l *Ledger, task olderTask, stop string) {
	t.Helper()
	inTx(t, l, func(ctx context.Context, tx *sql.Tx) {
		now := l.timestamp()
		_, err := tx.ExecContext(ctx, `UPDATE attempts SET state = 'ended', ended_at = ?, stop_reason = ? WHERE id = ?`, now, stop, task.AttemptID)
		require.NoError(t, err)
		rows, err := tx.QueryContext(ctx, `SELECT event_id, delivery FROM task_events WHERE task_id = ? AND retired_at IS NULL ORDER BY event_id`, task.TaskID)
		require.NoError(t, err)
		deliveries := map[int64]string{}
		var order []int64
		for rows.Next() {
			var (
				id       int64
				delivery string
			)
			require.NoError(t, rows.Scan(&id, &delivery))
			deliveries[id] = delivery
			order = append(order, id)
		}
		require.NoError(t, rows.Close())
		var unexposed []int64
		for _, id := range order {
			switch deliveries[id] {
			case "completed":
			case "admitted":
				unexposed = append(unexposed, id)
			default:
				moved, err := l.move(ctx, tx, transition{id: id, state: StateCompleted, from: []RecordState{StateDispatched}})
				require.NoError(t, err)
				require.True(t, moved)
				_, err = tx.ExecContext(ctx, `
UPDATE task_events SET delivery = 'completed', completed_at = ?, outcome = ? WHERE task_id = ? AND event_id = ?`,
					now, string(OutcomeUnknown), task.TaskID, id)
				require.NoError(t, err)
			}
		}
		supersedeOlderTask(ctx, t, l, tx, task.TaskID, unexposed)
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET ended_at = ? WHERE id = ?`, now, task.TaskID)
		require.NoError(t, err)
	})
}

// supersedeOlderTask is the removed supersedeTask: the token refused, every
// row retired by the trigger, and what the task never exposed returned to
// admitted where nothing else still holds it dispatched.
func supersedeOlderTask(ctx context.Context, t *testing.T, l *Ledger, tx *sql.Tx, taskID int64, unexposed []int64) {
	t.Helper()
	_, err := tx.ExecContext(ctx, `UPDATE tasks SET superseded_at = COALESCE(superseded_at, ?) WHERE id = ?`, l.timestamp(), taskID)
	require.NoError(t, err)
	for _, id := range unexposed {
		_, err := l.move(ctx, tx, transition{id: id, state: StateAdmitted, from: []RecordState{StateDispatched}})
		require.NoError(t, err)
	}
}

// olderSupersede retires a live task on its own, as the removed SupersedeTask
// did for a task whose worker never started.
func olderSupersede(t *testing.T, l *Ledger, taskID int64) {
	t.Helper()
	inTx(t, l, func(ctx context.Context, tx *sql.Tx) {
		rows, err := tx.QueryContext(ctx, `SELECT event_id FROM task_events WHERE task_id = ? AND retired_at IS NULL AND delivery = 'admitted'`, taskID)
		require.NoError(t, err)
		var unexposed []int64
		for rows.Next() {
			var id int64
			require.NoError(t, rows.Scan(&id))
			unexposed = append(unexposed, id)
		}
		require.NoError(t, rows.Close())
		supersedeOlderTask(ctx, t, l, tx, taskID, unexposed)
	})
}

// olderUnknownOutcome takes a fresh record to completed(unknown) the way an
// older build did: admitted, launched, running, lost.
func olderUnknownOutcome(t *testing.T, l *Ledger, id int64) olderTask {
	t.Helper()
	require.Equal(t, StateAdmitted, opAdmit(t, l, id, "recording:"+strconv.FormatInt(id, 10)))
	task := olderLaunch(t, l, id)
	olderRunning(t, l, task.AttemptID, 4242, time.Time{})
	olderEnd(t, l, task, "lost")
	require.Equal(t, StateCompleted, stateOf(t, l, id))
	return task
}

// olderPendingRedispatch leaves event 1 completed(failed) on a live task with
// a person's redispatch waiting for that task to end, as the removed
// Redispatch left it: the decision recorded after the outcome settled, the
// record authorized and naming it, and the task's token superseded.
func olderPendingRedispatch(t *testing.T, l *Ledger) olderTask {
	t.Helper()
	require.Equal(t, StateAdmitted, opAdmit(t, l, 1, "recording:9"))
	task := olderLaunch(t, l, 1)
	olderReport(t, l, task, 1, OutcomeFailed)
	inTx(t, l, func(ctx context.Context, tx *sql.Tx) {
		supersedeOlderTask(ctx, t, l, tx, task.TaskID, nil)
		var settled string
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT completed_at FROM task_events WHERE task_id = ? AND event_id = 1`, task.TaskID).Scan(&settled))
		now := notBefore(l.timestamp(), settled)
		res, err := tx.ExecContext(ctx, `
INSERT INTO decisions (action, event_id, decided_by, decided_at, from_state, from_outcome, to_state, superseded_task_id, note)
VALUES ('redispatch', 1, ?, ?, 'completed', 'failed', 'completed', ?, ?)`, opBy, now, task.TaskID, fmt.Sprintf("waits for task %d to end", task.TaskID))
		require.NoError(t, err)
		decision, err := res.LastInsertId()
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `UPDATE events SET authorized_at = ?, authorized_by = ?, redispatch_decision = ? WHERE id = 1`, now, opBy, decision)
		require.NoError(t, err)
	})
	return task
}

// inTx runs write in one ledger transaction, as each removed call did.
func inTx(t *testing.T, l *Ledger, write func(ctx context.Context, tx *sql.Tx)) {
	t.Helper()
	ctx := context.Background()
	tx, err := l.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	write(ctx, tx)
	require.NoError(t, tx.Commit())
}
