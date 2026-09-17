package connector

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Tasks and attempts: the dispatcher's half of the ledger.
//
// A task is one conversation's work, bound to a token; an attempt is one
// worker run under it. The basecamp_connect domain (ledger_dispatch.go) is
// the worker's view of the same rows.
//
// # Invariants
//
// Each is held by the database where SQL can say it, and by a test that fails
// without it (ledger_tasks_test.go).
//
//  1. Exposure before hand-off. An attempt is written launching in the same
//     transaction that writes its originating event exposed and moves the
//     record to dispatched, and before the driver is asked to start anything.
//     A follow-up is written exposed (ExposeEvent) before a prompt about it is
//     sent.
//  2. One live task per conversation, one per working directory, one live
//     attempt per task, and (migration 5's task_events_one_live_task) one live
//     task per event. Unique partial indexes, so two dispatchers on one ledger
//     cannot both win.
//  3. An ended task has no valid token and no live events. Ending a task,
//     superseding its token and retiring its events are one transaction, and
//     a trigger refuses the end without the supersession, so a worker that
//     outlives its task is refused by basecamp_connect.
//  4. Automatic retry is bounded and proven. An exposure is withdrawn — the
//     record back to admitted — only when the attempt that wrote it ended with
//     the driver's report that no worker process existed, and only for the
//     event's first such withdrawal (withdrawn_at, kept on the retired row,
//     is that budget); a second is blocked(spawn_failed), which
//     waits for a person. Anything else that ends an exposed, unreported event
//     makes it completed with outcome unknown.
//  5. Outcomes and stop reasons are separate. A stop reason is written on the
//     attempt, an outcome on the task event; neither is computed from the
//     other, and settlement never overwrites a reported outcome.
//  6. An adopted reply is a link, never an outcome: AdoptReply writes a reply
//     id beside an unknown outcome and leaves the outcome unknown.
//  7. Attempt states move forward only: launching → running → ended, or
//     launching → ended.
const migrationTasksAndAttempts = `
ALTER TABLE tasks ADD COLUMN conversation_key     TEXT    NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN route                TEXT    NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN work_dir             TEXT    NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN driver               TEXT    NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN originating_event_id INTEGER;
ALTER TABLE tasks ADD COLUMN deadline_at          TEXT;
ALTER TABLE tasks ADD COLUMN ended_at             TEXT;

CREATE UNIQUE INDEX tasks_live_conversation ON tasks (conversation_key)
  WHERE ended_at IS NULL AND conversation_key <> '';
CREATE UNIQUE INDEX tasks_live_work_dir ON tasks (work_dir)
  WHERE ended_at IS NULL AND work_dir <> '';

CREATE TRIGGER tasks_end_supersedes
BEFORE UPDATE OF ended_at ON tasks
WHEN NEW.ended_at IS NOT NULL AND NEW.superseded_at IS NULL
BEGIN
  SELECT RAISE(ABORT, 'a task ends with its token superseded');
END;

ALTER TABLE task_events ADD COLUMN exposed_attempt_id TEXT;
ALTER TABLE task_events ADD COLUMN adopted_reply_id   INTEGER;

CREATE TABLE attempts (
  id               TEXT    PRIMARY KEY,
  task_id          INTEGER NOT NULL REFERENCES tasks (id),
  seq              INTEGER NOT NULL,
  driver           TEXT    NOT NULL,
  state            TEXT    NOT NULL CHECK (state IN ('launching', 'running', 'ended')),
  pid              INTEGER,
  pgid             INTEGER,
  process_started  TEXT,
  session_id       TEXT    NOT NULL DEFAULT '',
  launched_at      TEXT    NOT NULL,
  running_at       TEXT,
  ended_at         TEXT,
  stop_reason      TEXT    NOT NULL DEFAULT ''
                   CHECK (stop_reason IN ('', 'finished', 'failed', 'deadline', 'shutdown', 'lost')),
  spawn_failed     INTEGER NOT NULL DEFAULT 0,
  refusals         INTEGER NOT NULL DEFAULT 0,
  progress_at      TEXT,
  still_running    INTEGER NOT NULL DEFAULT 0,
  UNIQUE (task_id, seq),
  CHECK ((state = 'ended') = (stop_reason <> ''))
);
CREATE UNIQUE INDEX attempts_live_per_task ON attempts (task_id) WHERE state <> 'ended';
CREATE INDEX attempts_state ON attempts (state);

CREATE TRIGGER attempts_state_moves_forward
BEFORE UPDATE OF state ON attempts
WHEN (CASE NEW.state WHEN 'launching' THEN 0 WHEN 'running' THEN 1 ELSE 2 END)
   < (CASE OLD.state WHEN 'launching' THEN 0 WHEN 'running' THEN 1 ELSE 2 END)
   OR (OLD.state = 'ended' AND NEW.state = 'ended' AND NEW.stop_reason <> OLD.stop_reason)
BEGIN
  SELECT RAISE(ABORT, 'an attempt state never goes back');
END;
`

// AttemptState is where an attempt is.
type AttemptState string

const (
	// AttemptLaunching is written before the driver is asked to start a
	// worker. Found after a crash it is treated as running: the worker may
	// exist.
	AttemptLaunching AttemptState = "launching"
	// AttemptRunning has its process or session id.
	AttemptRunning AttemptState = "running"
	// AttemptEnded has a stop reason.
	AttemptEnded AttemptState = "ended"
)

// StopReason is why an attempt ended. It is not an outcome.
type StopReason string

const (
	// StopFinished is a clean stop: the turn ended and the worker exited 0.
	StopFinished StopReason = "finished"
	// StopFailed is a refusal, a stop the connector did not ask for, a
	// non-zero exit, or a worker that could not be started.
	StopFailed StopReason = "failed"
	// StopDeadline is the task's deadline.
	StopDeadline StopReason = "deadline"
	// StopShutdown is the connector shutting down.
	StopShutdown StopReason = "shutdown"
	// StopLost is a worker that went away with a turn in flight, or one a
	// restarted connector found.
	StopLost StopReason = "lost"
)

// OutcomeUnknown is an event that was exposed to a worker and never
// reported: whatever ended the attempt, the worker may have acted on it.
const OutcomeUnknown Outcome = "unknown"

// ReasonSpawnFailed blocks an event whose worker could not be started a
// second time. It waits for a person's redispatch.
const ReasonSpawnFailed = "spawn_failed"

// Errors from the task ledger.
var (
	// ErrNotStartable is a launch for a record that is not waiting for a
	// worker: not admitted or queued, without its snapshot or route, on a
	// conversation or working directory that already has a live task.
	ErrNotStartable = errors.New("the record is not waiting for a worker")
	// ErrWorkDirMismatch is a launch naming a working directory the record
	// does not carry.
	ErrWorkDirMismatch = errors.New("the working directory is not the one the record carries")
	// ErrNoLiveAttempt is a write for an attempt that has ended or never was.
	ErrNoLiveAttempt = errors.New("no live attempt by that id")
)

// Tx is a ledger transaction a hook writes in, so what the hook writes (an
// outbox intent) commits or rolls back with the transition that called for
// it.
type Tx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Hooks run inside the transactions of the ledger's lifecycle transitions.
// A hook's error rolls the transition back. Set them once, before the ledger
// is used.
type Hooks struct {
	// VerdictCommitted runs in admission's verdict transaction, after the
	// verdict is written: where the guard acknowledgement and the holding
	// reply are called for.
	VerdictCommitted func(ctx context.Context, tx Tx, v CommittedVerdict) error
	// TaskLaunched runs in LaunchTask's transaction.
	TaskLaunched func(ctx context.Context, tx Tx, launch Launch) error
	// AttemptEnded runs in EndAttempt's transaction, after every event is
	// settled: where the attempt's completion message is called for.
	AttemptEnded func(ctx context.Context, tx Tx, s Settlement) error
	// StillRunning runs in StillRunning's transaction.
	StillRunning func(ctx context.Context, tx Tx, tick StillRunningTick) error
}

// SetHooks installs hooks. Not safe concurrently with ledger use.
func (l *Ledger) SetHooks(h Hooks) { l.hooks = h }

// CommittedVerdict is what VerdictCommitted is told.
type CommittedVerdict struct {
	EventID     int64
	State       RecordState
	Reason      string
	Trigger     string
	Acknowledge bool
	ReplyKind   string
	// ReplyRecordingID is where a reply to the event goes.
	ReplyRecordingID int64
}

// LaunchSpec asks for a task and its first attempt.
type LaunchSpec struct {
	// EventID is the originating event: an admitted or queued record.
	EventID int64
	// Route is the approved directory; it must be the route the record
	// carries.
	Route string
	// WorkDir is the directory the worker works in: Route itself, or a
	// directory made for the task from it (a git worktree). Empty means
	// Route. One live task holds a working directory.
	WorkDir string
	// Driver is the driver's name.
	Driver string
	// Deadline is how long the task may run; zero for none.
	Deadline time.Duration
}

// Launch is a task written launching.
type Launch struct {
	TaskID int64
	// Token binds the worker to the task. It is returned once and stored
	// only as a hash.
	Token     string
	AttemptID string
	// EventIDs are the task's events, originating first. Only the originating
	// event is exposed; the rest wait at delivery admitted.
	EventIDs        []int64
	ConversationKey string
	Route           string
	WorkDir         string
	Driver          string
	LaunchedAt      time.Time
	// DeadlineAt is zero when the task has no deadline.
	DeadlineAt time.Time
}

// LaunchTask writes a task, its first attempt as launching, and its
// originating event exposed, in one transaction (invariant 1). Records on the
// same conversation that wait for a worker join the task at delivery
// admitted.
func (l *Ledger) LaunchTask(ctx context.Context, spec LaunchSpec) (Launch, error) {
	if spec.WorkDir == "" {
		spec.WorkDir = spec.Route
	}
	if spec.Route == "" || spec.Driver == "" {
		return Launch{}, errors.New("connector: a launch needs a route and a driver")
	}
	attemptID, err := newAttemptID()
	if err != nil {
		return Launch{}, err
	}
	var out Launch
	err = retryBusy(func() error {
		var err error
		out, err = l.launchTask(ctx, spec, attemptID)
		return err
	})
	return out, err
}

func (l *Ledger) launchTask(ctx context.Context, spec LaunchSpec, attemptID string) (Launch, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return Launch{}, fmt.Errorf("connector: begin launch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	record, err := loadRecord(ctx, tx, spec.EventID)
	if err != nil {
		return Launch{}, err
	}
	switch {
	case record.State != StateAdmitted && record.State != StateQueued,
		record.ContentDropped, len(record.Decision.Snapshot) == 0,
		!record.Decision.Routed, record.Decision.ConversationKey == "":
		return Launch{}, fmt.Errorf("connector: launch event %d (%s): %w", spec.EventID, record.State, ErrNotStartable)
	case record.Decision.Route != spec.Route:
		return Launch{}, fmt.Errorf("connector: launch event %d in %q: %w", spec.EventID, spec.Route, ErrWorkDirMismatch)
	}
	var busy bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM tasks WHERE ended_at IS NULL AND (conversation_key = ? OR work_dir = ?))
    OR EXISTS (SELECT 1 FROM task_events WHERE event_id = ? AND retired_at IS NULL)`,
		record.Decision.ConversationKey, spec.WorkDir, spec.EventID).Scan(&busy); err != nil {
		return Launch{}, fmt.Errorf("connector: launch event %d: %w", spec.EventID, err)
	}
	if busy {
		return Launch{}, fmt.Errorf("connector: launch event %d: a live task holds its conversation or working directory: %w", spec.EventID, ErrNotStartable)
	}

	now := l.now()
	nowStamp := stamp(now)
	var deadline any
	var deadlineAt time.Time
	if spec.Deadline > 0 {
		deadlineAt = now.Add(spec.Deadline)
		deadline = stamp(deadlineAt)
	}
	// The originating event first, then every other record on the
	// conversation that waits for a worker. createTask dispatches them all
	// and refuses an event a live task already carries.
	joinable, err := joinableOn(ctx, tx, record.Decision.ConversationKey, spec.Route, spec.EventID)
	if err != nil {
		return Launch{}, err
	}
	grant, err := l.createTask(ctx, tx, append([]int64{spec.EventID}, joinable...))
	if err != nil {
		return Launch{}, err
	}
	taskID := grant.ID
	if _, err := tx.ExecContext(ctx, `
UPDATE tasks SET conversation_key = ?, route = ?, work_dir = ?, driver = ?, originating_event_id = ?, deadline_at = ?
WHERE id = ?`, record.Decision.ConversationKey, spec.Route, spec.WorkDir, spec.Driver, spec.EventID, deadline, taskID); err != nil {
		return Launch{}, fmt.Errorf("connector: create task for %d: %w", spec.EventID, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO attempts (id, task_id, seq, driver, state, launched_at) VALUES (?, ?, 1, ?, 'launching', ?)`,
		attemptID, taskID, spec.Driver, nowStamp); err != nil {
		return Launch{}, fmt.Errorf("connector: write attempt for %d: %w", spec.EventID, err)
	}
	// The prompt names the originating event's recording, so it is exposed
	// before the driver is asked for anything.
	if _, err := tx.ExecContext(ctx, `
UPDATE task_events SET delivery = 'exposed', exposed_at = ?, exposed_attempt_id = ?
WHERE task_id = ? AND event_id = ?`, nowStamp, attemptID, taskID, spec.EventID); err != nil {
		return Launch{}, fmt.Errorf("connector: expose event %d: %w", spec.EventID, err)
	}
	joined := joinable
	token := grant.Token

	out := Launch{
		TaskID:          taskID,
		Token:           token,
		AttemptID:       attemptID,
		EventIDs:        append([]int64{spec.EventID}, joined...),
		ConversationKey: record.Decision.ConversationKey,
		Route:           spec.Route,
		WorkDir:         spec.WorkDir,
		Driver:          spec.Driver,
		LaunchedAt:      now,
		DeadlineAt:      deadlineAt,
	}
	if l.hooks.TaskLaunched != nil {
		if err := l.hooks.TaskLaunched(ctx, tx, out); err != nil {
			return Launch{}, fmt.Errorf("connector: launch hook for %d: %w", spec.EventID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Launch{}, fmt.Errorf("connector: commit launch of %d: %w", spec.EventID, err)
	}
	return out, nil
}

func guardFor(acknowledge bool) string {
	if acknowledge {
		return "armed"
	}
	return ""
}

// startableFrom is the SQL condition for a record waiting for a worker: it
// carries what a dispatch needs and no live task holds it.
const startableCondition = `
e.state IN ('admitted', 'queued') AND e.content_dropped = 0 AND e.snapshot IS NOT NULL
AND e.routed = 1 AND e.conversation_key <> ''
AND NOT EXISTS (SELECT 1 FROM task_events te WHERE te.event_id = e.id AND te.retired_at IS NULL)`

// joinableOn lists the records on key, other than except, that wait for a
// worker and carry route, oldest first. A record admitted under another route
// (connect.json changed while a task ran) waits for a task in its own
// directory rather than riding along in this one.
func joinableOn(ctx context.Context, tx *sql.Tx, key, route string, except int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.id FROM events e WHERE e.conversation_key = ? AND e.route = ? AND e.id <> ? AND `+startableCondition+` ORDER BY e.id`, key, route, except)
	if err != nil {
		return nil, fmt.Errorf("connector: find follow-ups on %s: %w", key, err)
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

// joinConversation puts every record on key that waits for a worker onto the
// live task taskID at delivery admitted, dispatched, as createTask would have,
// and returns their ids, oldest first.
func (l *Ledger) joinConversation(ctx context.Context, tx *sql.Tx, taskID int64, key, route string) ([]int64, error) {
	ids, err := joinableOn(ctx, tx, key, route, 0)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		var acknowledge bool
		if err := tx.QueryRowContext(ctx, `SELECT acknowledge FROM events WHERE id = ?`, id).Scan(&acknowledge); err != nil {
			return nil, fmt.Errorf("connector: join event %d to task %d: %w", id, taskID, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_events (task_id, event_id, guard) VALUES (?, ?, ?)`, taskID, id, guardFor(acknowledge)); err != nil {
			if isConstraint(err) {
				return nil, fmt.Errorf("connector: join event %d to task %d: %w", id, taskID, ErrEventOnLiveTask)
			}
			return nil, fmt.Errorf("connector: join event %d to task %d: %w", id, taskID, err)
		}
		moved, err := l.move(ctx, tx, transition{id: id, state: StateDispatched, from: []RecordState{StateAdmitted, StateQueued}})
		if err != nil {
			return nil, err
		}
		if !moved {
			return nil, fmt.Errorf("connector: join event %d to task %d: %w", id, taskID, ErrNotStartable)
		}
	}
	return ids, nil
}

// JoinConversation puts the records on a live task's conversation that wait
// for a worker onto the task, at delivery admitted, and returns their ids. A
// task that has ended takes none: they start a task of their own.
func (l *Ledger) JoinConversation(ctx context.Context, taskID int64) ([]int64, error) {
	var out []int64
	err := retryBusy(func() error {
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("connector: begin join: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		var key, route string
		switch err := tx.QueryRowContext(ctx, `SELECT conversation_key, route FROM tasks WHERE id = ? AND ended_at IS NULL`, taskID).Scan(&key, &route); {
		case errors.Is(err, sql.ErrNoRows):
			out = nil
			return nil
		case err != nil:
			return fmt.Errorf("connector: join task %d: %w", taskID, err)
		}
		if key == "" {
			out = nil
			return nil
		}
		ids, err := l.joinConversation(ctx, tx, taskID, key, route)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("connector: commit join of task %d: %w", taskID, err)
		}
		out = ids
		return nil
	})
	return out, err
}

// UnexposedEvents are the events on a task still at delivery admitted, oldest
// first: the follow-ups a live session has not been prompted with.
func (l *Ledger) UnexposedEvents(ctx context.Context, taskID int64) ([]int64, error) {
	rows, err := l.db.QueryContext(ctx, `
SELECT event_id FROM task_events WHERE task_id = ? AND delivery = 'admitted' AND retired_at IS NULL ORDER BY event_id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("connector: unexposed events of task %d: %w", taskID, err)
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

// ExposeEvent writes a follow-up exposed by the live attempt, and moves its
// record to dispatched, before a prompt about it is sent (invariant 1). It
// reports false when the event was already exposed — by get_dispatch, say —
// which is not an error.
func (l *Ledger) ExposeEvent(ctx context.Context, attemptID string, eventID int64) (bool, error) {
	var exposed bool
	err := retryBusy(func() error {
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("connector: begin expose: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		taskID, err := liveAttemptTask(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		var delivery string
		switch err := tx.QueryRowContext(ctx, `SELECT delivery FROM task_events WHERE task_id = ? AND event_id = ? AND retired_at IS NULL`, taskID, eventID).Scan(&delivery); {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("connector: expose event %d: %w", eventID, ErrNotOnTask)
		case err != nil:
			return fmt.Errorf("connector: expose event %d: %w", eventID, err)
		}
		if Delivery(delivery) != DeliveryAdmitted {
			exposed = false
			return nil
		}
		moved, err := l.move(ctx, tx, transition{id: eventID, state: StateDispatched, from: []RecordState{StateAdmitted, StateQueued, StateDispatched}})
		if err != nil {
			return err
		}
		if !moved {
			return fmt.Errorf("connector: expose event %d: %w", eventID, ErrNotDispatchable)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE task_events SET delivery = 'exposed', exposed_at = ?, exposed_attempt_id = ?
WHERE task_id = ? AND event_id = ? AND delivery = 'admitted'`, l.timestamp(), attemptID, taskID, eventID); err != nil {
			return fmt.Errorf("connector: expose event %d: %w", eventID, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("connector: commit exposure of %d: %w", eventID, err)
		}
		exposed = true
		return nil
	})
	return exposed, err
}

func liveAttemptTask(ctx context.Context, tx *sql.Tx, attemptID string) (int64, error) {
	var taskID int64
	switch err := tx.QueryRowContext(ctx, `SELECT task_id FROM attempts WHERE id = ? AND state <> 'ended'`, attemptID).Scan(&taskID); {
	case errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("connector: attempt %s: %w", attemptID, ErrNoLiveAttempt)
	case err != nil:
		return 0, fmt.Errorf("connector: attempt %s: %w", attemptID, err)
	}
	return taskID, nil
}

// AttemptProcess is what MarkRunning records: the worker's process, where
// there is one, and its session id.
type AttemptProcess struct {
	PID       int
	PGID      int
	StartedAt time.Time
	SessionID string
}

// MarkRunning moves a launching attempt to running with its process and
// session.
func (l *Ledger) MarkRunning(ctx context.Context, attemptID string, p AttemptProcess) error {
	return retryBusy(func() error {
		var started any
		if !p.StartedAt.IsZero() {
			started = stamp(p.StartedAt)
		}
		res, err := l.db.ExecContext(ctx, `
UPDATE attempts SET state = 'running', running_at = ?, pid = ?, pgid = ?, process_started = ?, session_id = ?
WHERE id = ? AND state = 'launching'`,
			l.timestamp(), nullableInt(p.PID), nullableInt(p.PGID), started, p.SessionID, attemptID)
		if err != nil {
			return fmt.Errorf("connector: mark attempt %s running: %w", attemptID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			// The write is already committed; a driver that cannot say how
			// many rows it touched is not a reason to count the refusal
			// again at settlement.
			return nil //nolint:nilerr // the write is committed; an unreadable row count is not a reason to count it again
		}
		if n == 0 {
			return fmt.Errorf("connector: mark attempt %s running: %w", attemptID, ErrNoLiveAttempt)
		}
		return nil
	})
}

func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

// AttemptEnd is how an attempt ended.
type AttemptEnd struct {
	AttemptID string
	Stop      StopReason
	// SpawnFailed is the driver's report that no worker process ever existed
	// (driver.ErrNotStarted). Nothing else makes an exposure withdrawable.
	SpawnFailed bool
	// NoAutomaticRetry refuses the withdrawal even then: a task under the
	// sandbox launcher is never retried automatically.
	NoAutomaticRetry bool
	// UnrecordedRefusals are refusals RecordRefusal could not write when they
	// happened, settled here with the attempt. Refusals it did write are
	// already on the attempt.
	UnrecordedRefusals int
}

// Settlement is what ending an attempt did to its task.
type Settlement struct {
	TaskID    int64
	AttemptID string
	Stop      StopReason
	// SpawnFailed repeats AttemptEnd.SpawnFailed.
	SpawnFailed bool
	// OriginatingEventID is the task's originating event.
	OriginatingEventID int64
	Events             []SettledEvent
}

// SettledEvent is one event's state after its task ended.
type SettledEvent struct {
	EventID int64
	// Outcome is the reported outcome, or unknown for an event exposed and
	// never reported. Empty for an event never exposed, or withdrawn.
	Outcome Outcome
	// Reported is whether the outcome is the worker's own report.
	Reported bool
	ReplyID  *int64
	// Returned is an event never exposed: it waits for a task of its own.
	Returned bool
	// Withdrawn is an exposure withdrawn after a start that ran nothing; the
	// record is admitted again, or blocked(spawn_failed) when it already was
	// once.
	Withdrawn bool
	// Blocked is a withdrawal refused a second automatic retry.
	Blocked bool
}

// EndAttempt ends a live attempt with its stop reason, supersedes the task's
// token, settles every event on the task, and ends the task, in one
// transaction (invariants 3 to 5). Ending an attempt that already ended is
// ErrNoLiveAttempt.
func (l *Ledger) EndAttempt(ctx context.Context, end AttemptEnd) (Settlement, error) {
	switch end.Stop {
	case StopFinished, StopFailed, StopDeadline, StopShutdown, StopLost:
	default:
		return Settlement{}, fmt.Errorf("connector: %q is not a stop reason", end.Stop)
	}
	if end.SpawnFailed && end.Stop != StopFailed {
		return Settlement{}, errors.New("connector: a worker that was never started stops as failed")
	}
	var out Settlement
	err := retryBusy(func() error {
		var err error
		out, err = l.endAttempt(ctx, end)
		return err
	})
	return out, err
}

func (l *Ledger) endAttempt(ctx context.Context, end AttemptEnd) (Settlement, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return Settlement{}, fmt.Errorf("connector: begin end of attempt: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	taskID, err := liveAttemptTask(ctx, tx, end.AttemptID)
	if err != nil {
		return Settlement{}, err
	}
	now := l.timestamp()
	if _, err := tx.ExecContext(ctx, `
UPDATE attempts SET state = 'ended', ended_at = ?, stop_reason = ?, spawn_failed = ?, refusals = refusals + ? WHERE id = ?`,
		now, string(end.Stop), end.SpawnFailed, end.UnrecordedRefusals, end.AttemptID); err != nil {
		return Settlement{}, fmt.Errorf("connector: end attempt %s: %w", end.AttemptID, err)
	}

	settlement := Settlement{TaskID: taskID, AttemptID: end.AttemptID, Stop: end.Stop, SpawnFailed: end.SpawnFailed}
	var originating sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT originating_event_id FROM tasks WHERE id = ?`, taskID).Scan(&originating); err != nil {
		return Settlement{}, fmt.Errorf("connector: settle task %d: %w", taskID, err)
	}
	settlement.OriginatingEventID = originating.Int64

	type row struct {
		eventID   int64
		delivery  Delivery
		outcome   string
		replyID   sql.NullInt64
		exposedBy sql.NullString
	}
	rows, err := tx.QueryContext(ctx, `
SELECT event_id, delivery, outcome, reply_id, exposed_attempt_id FROM task_events
WHERE task_id = ? AND retired_at IS NULL ORDER BY event_id`, taskID)
	if err != nil {
		return Settlement{}, fmt.Errorf("connector: settle task %d: %w", taskID, err)
	}
	var events []row
	for rows.Next() {
		var r row
		var delivery string
		if err := rows.Scan(&r.eventID, &delivery, &r.outcome, &r.replyID, &r.exposedBy); err != nil {
			_ = rows.Close()
			return Settlement{}, fmt.Errorf("connector: settle task %d: %w", taskID, err)
		}
		r.delivery = Delivery(delivery)
		events = append(events, r)
	}
	if err := rows.Close(); err != nil {
		return Settlement{}, err
	}

	// Withdrawals wait for the supersession: #736's withdrawExposure takes an
	// exposure only on a task already superseded.
	var withdrawals []int
	for _, r := range events {
		se := SettledEvent{EventID: r.eventID}
		switch {
		case r.delivery == DeliveryCompleted:
			// A reported outcome stands (invariant 5).
			se.Outcome, se.Reported = Outcome(r.outcome), r.outcome != string(OutcomeUnknown)
			if r.replyID.Valid {
				id := r.replyID.Int64
				se.ReplyID = &id
			}
		case r.delivery == DeliveryAdmitted:
			// Never exposed: supersedeTask below returns it to admitted, to
			// wait for a task of its own.
			se.Returned = true
		case end.SpawnFailed && r.exposedBy.Valid && r.exposedBy.String == end.AttemptID:
			// Exposed by this attempt, whose driver proved nothing ran
			// (invariant 4): withdrawn once the task is superseded, below.
			withdrawals = append(withdrawals, len(settlement.Events))
		default:
			moved, err := l.move(ctx, tx, transition{id: r.eventID, state: StateCompleted, from: []RecordState{StateDispatched}})
			if err != nil {
				return Settlement{}, err
			}
			if !moved {
				// #736's invariant 4: a record a worker was handed leaves
				// dispatched only to completed, so nothing else can have moved
				// it. Reaching here is a ledger someone wrote by hand.
				return Settlement{}, fmt.Errorf("connector: settle event %d: %w", r.eventID, ErrNotDispatchable)
			}
			if _, err := tx.ExecContext(ctx, `
UPDATE task_events SET delivery = 'completed', completed_at = ?, outcome = ? WHERE task_id = ? AND event_id = ?`,
				now, string(OutcomeUnknown), taskID, r.eventID); err != nil {
				return Settlement{}, fmt.Errorf("connector: settle event %d: %w", r.eventID, err)
			}
			se.Outcome = OutcomeUnknown
		}
		settlement.Events = append(settlement.Events, se)
	}

	// #736's supersession: the token refused, every row retired, and the
	// never-exposed events returned to admitted. Then the task ends; the
	// trigger refuses an end the supersession did not precede.
	if err := l.supersedeTask(ctx, tx, taskID); err != nil {
		return Settlement{}, err
	}
	for _, i := range withdrawals {
		if err := l.withdraw(ctx, tx, taskID, settlement.Events[i].EventID, end.NoAutomaticRetry, &settlement.Events[i]); err != nil {
			return Settlement{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET ended_at = ? WHERE id = ?`, now, taskID); err != nil {
		return Settlement{}, fmt.Errorf("connector: end task %d: %w", taskID, err)
	}
	if l.hooks.AttemptEnded != nil {
		if err := l.hooks.AttemptEnded(ctx, tx, settlement); err != nil {
			return Settlement{}, fmt.Errorf("connector: attempt-ended hook for %s: %w", end.AttemptID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Settlement{}, fmt.Errorf("connector: commit end of attempt %s: %w", end.AttemptID, err)
	}
	return settlement, nil
}

// withdraw takes back an exposure whose worker never existed: once, the record
// returns to admitted; a second time, or with automatic retry refused, it is
// blocked(spawn_failed).
func (l *Ledger) withdraw(ctx context.Context, tx *sql.Tx, taskID, eventID int64, noRetry bool, se *SettledEvent) error {
	var earlier int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_events WHERE event_id = ? AND withdrawn_at IS NOT NULL`, eventID).Scan(&earlier); err != nil {
		return fmt.Errorf("connector: withdraw event %d: %w", eventID, err)
	}
	to, reason := StateAdmitted, ""
	if earlier > 0 || noRetry {
		to, reason = StateBlocked, ReasonSpawnFailed
		se.Blocked = true
	}
	// #736's one withdrawal: the marker, then the record's move, refused by
	// the database for anything but a launch exposure no worker pulled.
	if err := l.withdrawExposure(ctx, tx, taskID, eventID, to, reason); err != nil {
		return err
	}
	se.Withdrawn = true
	return nil
}

// LiveAttempt is an attempt that has not ended.
type LiveAttempt struct {
	AttemptID       string
	TaskID          int64
	State           AttemptState
	Driver          string
	Route           string
	WorkDir         string
	ConversationKey string
	Process         AttemptProcess
	LaunchedAt      time.Time
	// DeadlineAt is zero when the task has none.
	DeadlineAt time.Time
}

// LiveAttempts lists every attempt not ended, oldest first. On start they are
// all a previous process's: launching is read as running, because the worker
// may exist.
func (l *Ledger) LiveAttempts(ctx context.Context) ([]LiveAttempt, error) {
	rows, err := l.db.QueryContext(ctx, `
SELECT a.id, a.task_id, a.state, a.driver, t.route, t.work_dir, t.conversation_key,
       COALESCE(a.pid, 0), COALESCE(a.pgid, 0), a.process_started, a.session_id, a.launched_at, t.deadline_at
FROM attempts a JOIN tasks t ON t.id = a.task_id
WHERE a.state <> 'ended' ORDER BY a.launched_at, a.id`)
	if err != nil {
		return nil, fmt.Errorf("connector: live attempts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []LiveAttempt
	for rows.Next() {
		var (
			a                 LiveAttempt
			state, launched   string
			started, deadline sql.NullString
		)
		if err := rows.Scan(&a.AttemptID, &a.TaskID, &state, &a.Driver, &a.Route, &a.WorkDir, &a.ConversationKey,
			&a.Process.PID, &a.Process.PGID, &started, &a.Process.SessionID, &launched, &deadline); err != nil {
			return nil, fmt.Errorf("connector: live attempts: %w", err)
		}
		a.State = AttemptState(state)
		if a.LaunchedAt, err = parseStamp(launched); err != nil {
			return nil, err
		}
		if started.Valid {
			if a.Process.StartedAt, err = parseStamp(started.String); err != nil {
				return nil, err
			}
		}
		if deadline.Valid {
			if a.DeadlineAt, err = parseStamp(deadline.String); err != nil {
				return nil, err
			}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// StartableRecords returns up to limit records waiting for a worker, the
// oldest per conversation, oldest first, whatever their route.
func (l *Ledger) StartableRecords(ctx context.Context, limit int) ([]Record, error) {
	return l.startable(ctx, "", nil, limit)
}

// StartableFilter narrows StartableRecordsWhere to what the dispatcher can
// start now, in the query itself: a record it would skip must never take a
// place in the window, or a backlog it cannot start starves everything behind
// it.
type StartableFilter struct {
	// Routes are the approved directories by project, connect.json's as they
	// are now, already narrowed to --project. A record whose (project, route)
	// is not among them is not startable. Empty means nothing is.
	Routes map[int64]string
	// RouteHeld: a route with a live task holds its directory, so a record on
	// it waits. False when every task gets a directory of its own.
	RouteHeld bool
	Limit     int
}

// StartableRecordsWhere is StartableRecords narrowed by f.
func (l *Ledger) StartableRecordsWhere(ctx context.Context, f StartableFilter) ([]Record, error) {
	if len(f.Routes) == 0 {
		return nil, nil
	}
	buckets := make([]int64, 0, len(f.Routes))
	for bucket := range f.Routes {
		buckets = append(buckets, bucket)
	}
	slices.Sort(buckets)
	var where strings.Builder
	var args []any
	where.WriteString(" AND (")
	for i, bucket := range buckets {
		if i > 0 {
			where.WriteString(" OR ")
		}
		where.WriteString("(e.bucket_id = ? AND e.route = ?)")
		args = append(args, bucket, f.Routes[bucket])
	}
	where.WriteString(")")
	if f.RouteHeld {
		where.WriteString(" AND NOT EXISTS (SELECT 1 FROM tasks h WHERE h.ended_at IS NULL AND h.route = e.route)")
	}
	return l.startable(ctx, where.String(), args, f.Limit)
}

// startable runs the startable query with an extra condition. extra is built
// from this package's constants and placeholders only.
func (l *Ledger) startable(ctx context.Context, extra string, args []any, limit int) ([]Record, error) {
	//nolint:gosec // G202: extra is this package's constants and placeholders, never a value
	query := `
SELECT MIN(e.id) FROM events e
WHERE ` + startableCondition + extra + `
  AND NOT EXISTS (SELECT 1 FROM tasks t WHERE t.ended_at IS NULL AND t.conversation_key = e.conversation_key)
GROUP BY e.conversation_key ORDER BY MIN(e.id) LIMIT ?`
	rows, err := l.db.QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("connector: startable records: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(ids))
	for _, id := range ids {
		r, ok, err := l.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// StrandedRecords counts the records waiting for a worker whose (project,
// route) no approved pair covers: work admitted under a route connect.json no
// longer has, which nothing will start until a person routes it again or
// discards it.
// buckets is the run's --project scope: work in a project this run does not
// hear is another run's to dispatch, not stranded, so it is not counted.
func (l *Ledger) StrandedRecords(ctx context.Context, approved map[int64]string, buckets []int64) (int, error) {
	var where strings.Builder
	args := make([]any, 0, 2*len(approved)+len(buckets))
	for bucket, route := range approved {
		where.WriteString(" AND NOT (e.bucket_id = ? AND e.route = ?)")
		args = append(args, bucket, route)
	}
	if len(buckets) > 0 {
		where.WriteString(" AND e.bucket_id IN (" + strings.TrimSuffix(strings.Repeat("?, ", len(buckets)), ", ") + ")")
		for _, bucket := range buckets {
			args = append(args, bucket)
		}
	}
	//nolint:gosec // G202: the condition is this package's constants and placeholders, never a value
	query := `SELECT COUNT(*) FROM events e WHERE ` + startableCondition + where.String()
	var n int
	if err := l.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("connector: count stranded records: %w", err)
	}
	return n, nil
}

// RecordRefusal records one refusal on a live attempt, at the moment the
// driver made or observed it (driver's "Refusals"). An attempt that has ended
// is ErrNoLiveAttempt: its count was settled with it.
func (l *Ledger) RecordRefusal(ctx context.Context, attemptID string) error {
	return retryBusy(func() error {
		res, err := l.db.ExecContext(ctx, `UPDATE attempts SET refusals = refusals + 1 WHERE id = ? AND state <> 'ended'`, attemptID)
		if err != nil {
			return fmt.Errorf("connector: record refusal on %s: %w", attemptID, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("connector: record refusal on %s: %w", attemptID, ErrNoLiveAttempt)
		}
		return nil
	})
}

// RecordProgress stamps the live attempt's last progress, which still-running
// reads.
func (l *Ledger) RecordProgress(ctx context.Context, attemptID string) error {
	return retryBusy(func() error {
		_, err := l.db.ExecContext(ctx, `UPDATE attempts SET progress_at = ? WHERE id = ? AND state <> 'ended'`, l.timestamp(), attemptID)
		return err
	})
}

// StillRunningTick is one still-running occurrence of a live attempt.
type StillRunningTick struct {
	AttemptID string
	TaskID    int64
	// Occurrence counts from 1 per attempt.
	Occurrence int
	// ProgressAt is the attempt's last progress; zero when none was seen.
	ProgressAt time.Time
}

// StillRunning counts one more still-running occurrence for a live attempt,
// running the StillRunning hook in the same transaction.
func (l *Ledger) StillRunning(ctx context.Context, attemptID string) (StillRunningTick, error) {
	var out StillRunningTick
	err := retryBusy(func() error {
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("connector: begin still-running: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		taskID, err := liveAttemptTask(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE attempts SET still_running = still_running + 1 WHERE id = ?`, attemptID); err != nil {
			return fmt.Errorf("connector: still-running %s: %w", attemptID, err)
		}
		tick := StillRunningTick{AttemptID: attemptID, TaskID: taskID}
		var progress sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT still_running, progress_at FROM attempts WHERE id = ?`, attemptID).Scan(&tick.Occurrence, &progress); err != nil {
			return fmt.Errorf("connector: still-running %s: %w", attemptID, err)
		}
		if progress.Valid {
			if tick.ProgressAt, err = parseStamp(progress.String); err != nil {
				return err
			}
		}
		if l.hooks.StillRunning != nil {
			if err := l.hooks.StillRunning(ctx, tx, tick); err != nil {
				return fmt.Errorf("connector: still-running hook for %s: %w", attemptID, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("connector: commit still-running %s: %w", attemptID, err)
		}
		out = tick
		return nil
	})
	return out, err
}

// AdoptionCandidate is an event whose worker's report was lost after it
// acknowledged: settled unknown, delivered, and with no reply of its own.
type AdoptionCandidate struct {
	TaskID           int64
	EventID          int64
	ReplyKind        string
	ReplyRecordingID int64
	// DeliveredAt is the event's ack_dispatch.
	DeliveredAt time.Time
	// NextAckAt is the first acknowledgement of a later instruction on the
	// task; zero when there is none.
	NextAckAt time.Time
	// AckID is the worker's own acknowledgement, which is never its reply
	// however the clocks compare.
	AckID int64
}

// AdoptionCandidates lists a settled task's events a reply could be adopted
// for.
func (l *Ledger) AdoptionCandidates(ctx context.Context, taskID int64) ([]AdoptionCandidate, error) {
	rows, err := l.db.QueryContext(ctx, `
SELECT te.event_id, e.reply_kind, e.reply_recording_id, te.delivered_at, te.ack_id,
       (SELECT MIN(later.delivered_at) FROM task_events later
        WHERE later.task_id = te.task_id AND later.event_id > te.event_id AND later.delivered_at IS NOT NULL)
FROM task_events te JOIN events e ON e.id = te.event_id
WHERE te.task_id = ? AND te.outcome = 'unknown' AND te.delivered_at IS NOT NULL
  AND te.reply_id IS NULL AND te.adopted_reply_id IS NULL
ORDER BY te.event_id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("connector: adoption candidates of task %d: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []AdoptionCandidate
	for rows.Next() {
		c := AdoptionCandidate{TaskID: taskID}
		var delivered string
		var next sql.NullString
		var ackID sql.NullInt64
		if err := rows.Scan(&c.EventID, &c.ReplyKind, &c.ReplyRecordingID, &delivered, &ackID, &next); err != nil {
			return nil, err
		}
		if c.DeliveredAt, err = parseStamp(delivered); err != nil {
			return nil, err
		}
		if next.Valid {
			if c.NextAckAt, err = parseStamp(next.String); err != nil {
				return nil, err
			}
		}
		if ackID.Valid {
			c.AckID = ackID.Int64
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AgentReply is a comment or chat line by the agent at a destination.
type AgentReply struct {
	ID        int64
	CreatedAt time.Time
}

// AdoptableReply applies the adopted-reply rule: exactly one reply by the
// agent at the destination after the event's acknowledgement, not after a
// later instruction's acknowledgement, and not one of the connector's own
// lifecycle messages.
func AdoptableReply(c AdoptionCandidate, replies []AgentReply, lifecycle func(id int64) bool) (int64, bool) {
	var found []int64
	for _, r := range replies {
		if r.ID == c.AckID {
			// The worker's acknowledgement is not the worker's reply, and
			// the server's clock is not this machine's.
			continue
		}
		if !r.CreatedAt.After(c.DeliveredAt) {
			continue
		}
		if !c.NextAckAt.IsZero() && !r.CreatedAt.Before(c.NextAckAt) {
			continue
		}
		if lifecycle != nil && lifecycle(r.ID) {
			continue
		}
		found = append(found, r.ID)
	}
	if len(found) != 1 {
		return 0, false
	}
	return found[0], true
}

// AdoptReply links a reply to an event whose outcome is unknown. The outcome
// stays unknown (invariant 6).
func (l *Ledger) AdoptReply(ctx context.Context, taskID, eventID, replyID int64) error {
	if replyID <= 0 {
		return errors.New("connector: adopt a reply by its id")
	}
	return retryBusy(func() error {
		res, err := l.db.ExecContext(ctx, `
UPDATE task_events SET adopted_reply_id = ?
WHERE task_id = ? AND event_id = ? AND outcome = 'unknown' AND reply_id IS NULL AND adopted_reply_id IS NULL`,
			replyID, taskID, eventID)
		if err != nil {
			return fmt.Errorf("connector: adopt reply for %d: %w", eventID, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("connector: adopt reply for %d: the event is not unknown, or already has a reply", eventID)
		}
		return nil
	})
}

func newAttemptID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("connector: attempt id: %w", err)
	}
	return "att_" + strings.ToLower(hex.EncodeToString(raw)), nil
}
