package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

// OpenLedgerReadOnly opens an existing ledger for reading only: no migration,
// no write to the database, no lock. status uses it beside a running connector
// (invariant 8). SQLite may create the WAL sidecars of a cleanly closed ledger
// to read it; they sit in the ledger's private directory.
//
// The file must already exist, and it is refused unless it is private, as
// OpenLedger refuses it. A ledger an older binary wrote, which the running
// connector has not yet migrated, is refused: its columns are not the ones
// this build reads.
func OpenLedgerReadOnly(ctx context.Context, path string) (*Ledger, error) {
	if path == "" {
		return nil, errors.New("connector: ledger path is required")
	}
	if isInMemory(path) || strings.ContainsAny(path, "?#%") {
		return nil, fmt.Errorf("connector: ledger path %q cannot be opened as a file", path)
	}
	// Vetted as the writer's open vets it, without creating the file: a ledger
	// that vanishes under a reader (a promote renaming it) is not recreated
	// empty.
	if err := setup.CheckPrivateFile(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("connector: secure the ledger: %w", err)
	}
	if info, err := os.Lstat(filepath.Dir(path)); err != nil {
		return nil, err
	} else if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("connector: ledger directory %s is readable by other users (mode %04o); it must be 0700", filepath.Dir(path), info.Mode().Perm())
	}
	dsn := "file:" + path + "?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("connector: open ledger: %w", err)
	}
	db.SetMaxOpenConns(1)
	l := &Ledger{db: db, now: time.Now}
	version, err := l.SchemaVersion(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connector: read the ledger's schema: %w", err)
	}
	if version < len(migrations) {
		_ = db.Close()
		return nil, fmt.Errorf("connector: the ledger is at schema %d and this build reads %d: %w", version, len(migrations), ErrLedgerOutOfDate)
	}
	return l, nil
}

// ErrLedgerOutOfDate is a ledger an older binary wrote, which this build has
// not migrated. Starting the connector migrates it.
var ErrLedgerOutOfDate = errors.New("the ledger is older than this build; start the connector once to bring it up to date")

// StatusLimit is how many dispatches status lists.
const StatusLimit = 20

// Status is what `basecamp connect status` shows. Every field is ids, states,
// counts and timestamps: no content, no feed position, no token or token hash
// (invariant 8).
type Status struct {
	SchemaVersion int `json:"schema_version"`
	// Connection is the last run's own record, if one ran on this build.
	Connection *ConnectionStatus `json:"connection,omitempty"`
	// Hold is the standing hold marker.
	Hold       *HoldStatus `json:"hold,omitempty"`
	Generation int64       `json:"generation"`

	Positions   []PositionStatus `json:"positions"`
	Gaps        []GapStatus      `json:"gaps"`
	Losses      []LossStatus     `json:"open_losses"`
	Unrecovered int              `json:"unrecovered_ids"`

	// Queues counts records by state; Blocked counts blocked records by reason.
	Queues  map[string]int `json:"queues"`
	Blocked map[string]int `json:"blocked"`
	// Review counts records tagged for review that have not reached a
	// person yet, and Authorized those a person authorized that have not run.
	Review            int `json:"review_tagged"`
	AuthorizedBlocked int `json:"authorized_blocked"`
	RedispatchPending int `json:"redispatch_pending"`

	Tasks          []TaskStatus     `json:"live_tasks"`
	Worktrees      []WorktreeStatus `json:"retained_worktrees"`
	WorktreesKnown bool             `json:"worktrees_tracked"`
	Indeterminate  []IntentStatus   `json:"indeterminate_intents"`
	Held           []HeldStatus     `json:"held_records"`
	Dispatches     []DispatchStatus `json:"dispatches"`
}

// ConnectionStatus is the run command's own record of its last run: running
// once every part started, stopped when it exited. It is not the feed
// socket's state, which intake does not report.
type ConnectionStatus struct {
	State     string    `json:"state"`
	PID       int       `json:"pid"`
	ChangedAt time.Time `json:"changed_at"`
	Detail    string    `json:"detail,omitempty"`
}

// HoldStatus is the hold marker.
type HoldStatus struct {
	Generation int64     `json:"generation"`
	Cause      string    `json:"cause"`
	HeldBy     string    `json:"held_by"`
	HeldAt     time.Time `json:"held_at"`
}

// PositionStatus is one feed checkpoint: whether a position is held, never
// the position itself, which resumes the account's feed.
type PositionStatus struct {
	Filters          string    `json:"filters"`
	HasPosition      bool      `json:"has_position"`
	LastPollServedID int64     `json:"last_poll_served_id"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// GapStatus is one recorded 410.
type GapStatus struct {
	ID           int64     `json:"id"`
	DetectedAt   time.Time `json:"detected_at"`
	Class        string    `json:"class"`
	EpochAfterID *int64    `json:"epoch_after_id,omitempty"`
	EntryClass   string    `json:"entry_class,omitempty"`
}

// LossStatus is an overflow still being reconciled.
type LossStatus struct {
	ID         int64     `json:"id"`
	DetectedAt time.Time `json:"detected_at"`
	Dropped    int       `json:"dropped"`
	Missing    int       `json:"missing"`
	DeadlineAt time.Time `json:"deadline_at"`
}

// TaskStatus is a live task and its attempt.
type TaskStatus struct {
	TaskID    int64  `json:"task_id"`
	AttemptID string `json:"attempt_id"`
	State     string `json:"state"`
	Driver    string `json:"driver"`
	WorkDir   string `json:"work_dir"`
	PID       int    `json:"pid,omitempty"`
	PGID      int    `json:"pgid,omitempty"`
	// ProcessStartedAt is the start time recorded with the pid: with it, the
	// pid is an identity (driver.OwnsWorker).
	ProcessStartedAt *time.Time `json:"process_started_at,omitempty"`
	// Worker is whether the recorded process is still this task's worker, as
	// the caller established it; the ledger read leaves it empty.
	Worker     string     `json:"worker,omitempty"`
	LaunchedAt time.Time  `json:"launched_at"`
	DeadlineAt *time.Time `json:"deadline_at,omitempty"`
	EventIDs   []int64    `json:"event_ids"`
}

// WorktreeStatus is a retained worktree, as the worktree lister reports it.
type WorktreeStatus struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	TaskID int64  `json:"task_id,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// IntentStatus is a lifecycle message waiting for a person. The body is not
// shown.
type IntentStatus struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	EventID     int64      `json:"event_id,omitempty"`
	AttemptID   string     `json:"attempt_id,omitempty"`
	BucketID    int64      `json:"bucket_id"`
	MessageKind string     `json:"message_kind"`
	RecordingID int64      `json:"recording_id"`
	SendingAt   *time.Time `json:"sending_at,omitempty"`
	Note        string     `json:"note,omitempty"`
}

// HeldStatus is a held record.
type HeldStatus struct {
	EventID      int64     `json:"event_id"`
	EventType    string    `json:"event_type"`
	Trigger      string    `json:"trigger,omitempty"`
	BucketID     int64     `json:"bucket_id"`
	RecordingURL string    `json:"recording_url,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	Generation   int64     `json:"generation"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// DispatchStatus is one attempt and the outcomes of the events on its task.
type DispatchStatus struct {
	TaskID     int64             `json:"task_id"`
	AttemptID  string            `json:"attempt_id"`
	State      string            `json:"state"`
	StopReason string            `json:"stop_reason,omitempty"`
	LaunchedAt time.Time         `json:"launched_at"`
	EndedAt    *time.Time        `json:"ended_at,omitempty"`
	Events     []DispatchedEvent `json:"events"`
}

// DispatchedEvent is one event's delivery and outcome on a task.
type DispatchedEvent struct {
	EventID  int64  `json:"event_id"`
	Delivery string `json:"delivery"`
	Outcome  string `json:"outcome,omitempty"`
	ReplyID  *int64 `json:"reply_id,omitempty"`
	// Withdrawn is an exposure taken back after a start that ran nothing.
	Withdrawn bool `json:"withdrawn,omitempty"`
}

// WorktreeLister lists retained worktrees for status. Card 19's worktree
// ledger provides it; nil means this build does not track them.
type WorktreeLister func(ctx context.Context) ([]WorktreeStatus, error)

// Status reads everything status shows in one read transaction, so the
// numbers agree with each other.
func (l *Ledger) Status(ctx context.Context, worktrees WorktreeLister) (Status, error) {
	tx, err := l.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Status{}, fmt.Errorf("connector: begin status: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	s := Status{Queues: map[string]int{}, Blocked: map[string]int{}}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&s.SchemaVersion); err != nil {
		return Status{}, fmt.Errorf("connector: status schema: %w", err)
	}
	if err := statusConnection(ctx, tx, &s); err != nil {
		return Status{}, err
	}
	hold, ok, err := readHold(ctx, tx)
	if err != nil {
		return Status{}, err
	}
	if ok {
		s.Hold = &HoldStatus{Generation: hold.Generation, Cause: string(hold.Cause), HeldBy: hold.HeldBy, HeldAt: hold.HeldAt}
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM generations`).Scan(&s.Generation); err != nil {
		return Status{}, fmt.Errorf("connector: status generation: %w", err)
	}
	for _, step := range []func(context.Context, *sql.Tx, *Status) error{
		statusPositions, statusGaps, statusQueues, statusTasks, statusIntents, statusHeld, statusDispatches,
	} {
		if err := step(ctx, tx, &s); err != nil {
			return Status{}, err
		}
	}
	if worktrees != nil {
		s.WorktreesKnown = true
		if s.Worktrees, err = worktrees(ctx); err != nil {
			return Status{}, fmt.Errorf("connector: status worktrees: %w", err)
		}
	}
	if s.Worktrees == nil {
		s.Worktrees = []WorktreeStatus{}
	}
	return s, nil
}

func statusConnection(ctx context.Context, tx *sql.Tx, s *Status) error {
	var (
		c       ConnectionStatus
		changed string
	)
	err := tx.QueryRowContext(ctx, `SELECT state, pid, changed_at, detail FROM connection WHERE id = 1`).Scan(&c.State, &c.PID, &changed, &c.Detail)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("connector: status connection: %w", err)
	}
	if c.ChangedAt, err = parseStamp(changed); err != nil {
		return err
	}
	s.Connection = &c
	return nil
}

func statusPositions(ctx context.Context, tx *sql.Tx, s *Status) error {
	rows, err := tx.QueryContext(ctx, `SELECT flat_key, position <> '', last_poll_served_id, updated_at FROM checkpoints ORDER BY updated_at DESC`)
	if err != nil {
		return fmt.Errorf("connector: status positions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	s.Positions = []PositionStatus{}
	for rows.Next() {
		var (
			p       PositionStatus
			updated string
		)
		if err := rows.Scan(&p.Filters, &p.HasPosition, &p.LastPollServedID, &updated); err != nil {
			return err
		}
		if p.UpdatedAt, err = parseStamp(updated); err != nil {
			return err
		}
		s.Positions = append(s.Positions, p)
	}
	return rows.Err()
}

func statusGaps(ctx context.Context, tx *sql.Tx, s *Status) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, detected_at, class, epoch_after_id, entry_class FROM gaps ORDER BY id`)
	if err != nil {
		return fmt.Errorf("connector: status gaps: %w", err)
	}
	s.Gaps = []GapStatus{}
	for rows.Next() {
		var (
			g        GapStatus
			detected string
			epoch    sql.NullInt64
		)
		if err := rows.Scan(&g.ID, &detected, &g.Class, &epoch, &g.EntryClass); err != nil {
			_ = rows.Close()
			return err
		}
		if g.DetectedAt, err = parseStamp(detected); err != nil {
			_ = rows.Close()
			return err
		}
		if epoch.Valid {
			id := epoch.Int64
			g.EpochAfterID = &id
		}
		s.Gaps = append(s.Gaps, g)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx, `
SELECT l.id, l.detected_at, l.dropped_count, l.deadline_at,
       (SELECT COUNT(*) FROM loss_ids i WHERE i.loss_id = l.id AND i.state = 'missing')
FROM losses l WHERE l.resolved_at IS NULL ORDER BY l.id`)
	if err != nil {
		return fmt.Errorf("connector: status losses: %w", err)
	}
	s.Losses = []LossStatus{}
	for rows.Next() {
		var (
			loss               LossStatus
			detected, deadline string
		)
		if err := rows.Scan(&loss.ID, &detected, &loss.Dropped, &deadline, &loss.Missing); err != nil {
			_ = rows.Close()
			return err
		}
		if loss.DetectedAt, err = parseStamp(detected); err != nil {
			_ = rows.Close()
			return err
		}
		if loss.DeadlineAt, err = parseStamp(deadline); err != nil {
			_ = rows.Close()
			return err
		}
		s.Losses = append(s.Losses, loss)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT event_id) FROM loss_ids WHERE state = 'unrecovered'`).Scan(&s.Unrecovered)
}

func statusQueues(ctx context.Context, tx *sql.Tx, s *Status) error {
	rows, err := tx.QueryContext(ctx, `SELECT state, reason, COUNT(*) FROM events WHERE state <> 'discarded' AND state <> 'completed' GROUP BY state, reason`)
	if err != nil {
		return fmt.Errorf("connector: status queues: %w", err)
	}
	for rows.Next() {
		var (
			state, reason string
			n             int
		)
		if err := rows.Scan(&state, &reason, &n); err != nil {
			_ = rows.Close()
			return err
		}
		s.Queues[state] += n
		if state == string(StateBlocked) {
			s.Blocked[reason] += n
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return tx.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM events WHERE review = 1 AND authorized_at IS NULL AND state IN ('seen', 'blocked', 'dispatched')),
  (SELECT COUNT(*) FROM events WHERE state = 'blocked' AND authorized_at IS NOT NULL),
  (SELECT COUNT(*) FROM events WHERE redispatch_decision IS NOT NULL)`).Scan(&s.Review, &s.AuthorizedBlocked, &s.RedispatchPending)
}

func statusTasks(ctx context.Context, tx *sql.Tx, s *Status) error {
	rows, err := tx.QueryContext(ctx, `
SELECT t.id, a.id, a.state, a.driver, t.work_dir, COALESCE(a.pid, 0), COALESCE(a.pgid, 0), a.process_started, a.launched_at, t.deadline_at
FROM attempts a JOIN tasks t ON t.id = a.task_id
WHERE a.state <> 'ended' ORDER BY a.launched_at, a.id`)
	if err != nil {
		return fmt.Errorf("connector: status tasks: %w", err)
	}
	s.Tasks = []TaskStatus{}
	for rows.Next() {
		var (
			t        TaskStatus
			launched string
			deadline sql.NullString
			started  sql.NullString
		)
		if err := rows.Scan(&t.TaskID, &t.AttemptID, &t.State, &t.Driver, &t.WorkDir, &t.PID, &t.PGID, &started, &launched, &deadline); err != nil {
			_ = rows.Close()
			return err
		}
		if started.Valid {
			at, err := parseStamp(started.String)
			if err != nil {
				_ = rows.Close()
				return err
			}
			t.ProcessStartedAt = &at
		}
		if t.LaunchedAt, err = parseStamp(launched); err != nil {
			_ = rows.Close()
			return err
		}
		if deadline.Valid {
			at, err := parseStamp(deadline.String)
			if err != nil {
				_ = rows.Close()
				return err
			}
			t.DeadlineAt = &at
		}
		s.Tasks = append(s.Tasks, t)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for i := range s.Tasks {
		ids, err := taskEventIDs(ctx, tx, s.Tasks[i].TaskID)
		if err != nil {
			return err
		}
		s.Tasks[i].EventIDs = ids
	}
	return nil
}

func taskEventIDs(ctx context.Context, tx *sql.Tx, taskID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT event_id FROM task_events WHERE task_id = ? AND withdrawn_at IS NULL ORDER BY event_id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("connector: status task %d: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func statusIntents(ctx context.Context, tx *sql.Tx, s *Status) error {
	rows, err := tx.QueryContext(ctx, selectIntents+` WHERE state = 'indeterminate' ORDER BY id`)
	if err != nil {
		return fmt.Errorf("connector: status intents: %w", err)
	}
	intents, err := scanIntents(rows)
	if err != nil {
		return err
	}
	s.Indeterminate = make([]IntentStatus, 0, len(intents))
	for _, in := range intents {
		s.Indeterminate = append(s.Indeterminate, IntentStatus{
			ID: in.ID, Kind: string(in.Kind), EventID: in.EventID, AttemptID: in.AttemptID,
			BucketID: in.Destination.BucketID, MessageKind: string(in.Destination.Kind), RecordingID: in.Destination.RecordingID,
			SendingAt: in.SendingAt, Note: in.Note,
		})
	}
	return nil
}

func statusHeld(ctx context.Context, tx *sql.Tx, s *Status) error {
	rows, err := tx.QueryContext(ctx, `
SELECT id, event_type, trigger_name, bucket_id, recording_url, reason, generation, updated_at
FROM events WHERE state = 'held' ORDER BY id`)
	if err != nil {
		return fmt.Errorf("connector: status held records: %w", err)
	}
	defer func() { _ = rows.Close() }()
	s.Held = []HeldStatus{}
	for rows.Next() {
		var (
			h       HeldStatus
			updated string
		)
		if err := rows.Scan(&h.EventID, &h.EventType, &h.Trigger, &h.BucketID, &h.RecordingURL, &h.Reason, &h.Generation, &updated); err != nil {
			return err
		}
		if h.UpdatedAt, err = parseStamp(updated); err != nil {
			return err
		}
		s.Held = append(s.Held, h)
	}
	return rows.Err()
}

func statusDispatches(ctx context.Context, tx *sql.Tx, s *Status) error {
	rows, err := tx.QueryContext(ctx, `
SELECT task_id, id, state, stop_reason, launched_at, ended_at FROM attempts
ORDER BY launched_at DESC, id DESC LIMIT ?`, StatusLimit)
	if err != nil {
		return fmt.Errorf("connector: status dispatches: %w", err)
	}
	s.Dispatches = []DispatchStatus{}
	for rows.Next() {
		var (
			d        DispatchStatus
			launched string
			ended    sql.NullString
		)
		if err := rows.Scan(&d.TaskID, &d.AttemptID, &d.State, &d.StopReason, &launched, &ended); err != nil {
			_ = rows.Close()
			return err
		}
		if d.LaunchedAt, err = parseStamp(launched); err != nil {
			_ = rows.Close()
			return err
		}
		if ended.Valid {
			at, err := parseStamp(ended.String)
			if err != nil {
				_ = rows.Close()
				return err
			}
			d.EndedAt = &at
		}
		s.Dispatches = append(s.Dispatches, d)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for i := range s.Dispatches {
		events, err := dispatchedEvents(ctx, tx, s.Dispatches[i].TaskID)
		if err != nil {
			return err
		}
		s.Dispatches[i].Events = events
	}
	return nil
}

func dispatchedEvents(ctx context.Context, tx *sql.Tx, taskID int64) ([]DispatchedEvent, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT event_id, delivery, outcome, COALESCE(reply_id, adopted_reply_id), withdrawn_at IS NOT NULL
FROM task_events WHERE task_id = ? ORDER BY event_id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("connector: status task %d events: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	out := []DispatchedEvent{}
	for rows.Next() {
		var (
			e     DispatchedEvent
			reply sql.NullInt64
		)
		if err := rows.Scan(&e.EventID, &e.Delivery, &e.Outcome, &reply, &e.Withdrawn); err != nil {
			return nil, err
		}
		if reply.Valid {
			id := reply.Int64
			e.ReplyID = &id
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
