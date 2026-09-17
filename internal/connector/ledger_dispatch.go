package connector

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// Delivery is an event's delivery state on a task. It moves forward only.
type Delivery string

const (
	// DeliveryAdmitted is on the task and not yet handed to a worker.
	DeliveryAdmitted Delivery = "admitted"
	// DeliveryExposed was handed to a worker, which may have acted on it.
	DeliveryExposed Delivery = "exposed"
	// DeliveryDelivered was acknowledged by the worker.
	DeliveryDelivered Delivery = "delivered"
	// DeliveryCompleted has the worker's outcome.
	DeliveryCompleted Delivery = "completed"
)

// Outcome is what a worker reports for an event.
type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
)

// Refusals a worker's dispatch call can meet. Each is an answer, not a fault.
var (
	// ErrTaskTokenRefused is a token that names no task, or one a
	// redispatch superseded. The two are not told apart: a worker holding
	// either has no task.
	ErrTaskTokenRefused = errors.New("the task token names no current task")
	// ErrNotOnTask is an event id the token's task does not carry. Whether
	// the event exists on another task is not said.
	ErrNotOnTask = errors.New("the event is not on this task")
	// ErrNotExposed is an acknowledgement or completion for an event the
	// worker was never handed.
	ErrNotExposed = errors.New("the event was not handed to this worker; call get_dispatch for it first")
	// ErrReportConflict is a second report that disagrees with the first.
	// Reported outcomes stand.
	ErrReportConflict = errors.New("the event already has a different report")
	// ErrNotDispatchable is an event whose record left the path to a worker
	// after it joined the task: blocked or withdrawn, or its content dropped.
	ErrNotDispatchable = errors.New("the event can no longer be dispatched")
	// ErrInvalidReport is a report the worker can correct: an outcome that
	// is not one of the two, a link that is not a URL, too many links.
	ErrInvalidReport = errors.New("the report is not valid")
	// ErrConversationBusy is an event whose conversation already has a
	// dispatched record outside the task being created: a running task, or
	// work a worker was handed that is not settled yet. A conversation has
	// one task at a time.
	ErrConversationBusy = errors.New("the event's conversation already has a task")
	// ErrEventOnLiveTask is an event a live task already carries. Handing it
	// to a second task would give two workers one instruction.
	ErrEventOnLiveTask = errors.New("the event is already on a live task")
)

// TaskGrant is a new task and the token that binds a worker to it. The token
// is returned once and stored only as a hash.
type TaskGrant struct {
	ID    int64
	Token string
}

// CreateTask puts records on a new task at delivery admitted, with the
// acknowledgement guard armed for the records whose verdict asks for one, and
// moves each record to dispatched in the same transaction: a record is
// dispatched exactly while a live task carries it.
//
// An event is on at most one live task. A second task for an event whose task
// was not superseded is refused with ErrEventOnLiveTask, and nothing is
// written. The refusal is the database's own (task_events_one_live_task), so a
// retried or concurrent launch cannot get past it. A redispatch supersedes the
// old task first.
func (l *Ledger) CreateTask(ctx context.Context, eventIDs []int64) (TaskGrant, error) {
	var grant TaskGrant
	err := retryBusy(func() error {
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("connector: begin task: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		if grant, err = l.createTask(ctx, tx, eventIDs); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("connector: commit task: %w", err)
		}
		return nil
	})
	return grant, err
}

// createTask writes a task inside the caller's transaction, so the dispatcher
// can write the task, its attempt and the originating event's exposure as one
// commit. Every guarantee CreateTask documents holds within tx; nothing is
// committed here, and a refusal leaves tx for the caller to roll back.
func (l *Ledger) createTask(ctx context.Context, tx *sql.Tx, eventIDs []int64) (TaskGrant, error) {
	if len(eventIDs) == 0 {
		return TaskGrant{}, errors.New("connector: a task needs at least one event")
	}
	seen := make(map[int64]bool, len(eventIDs))
	for _, id := range eventIDs {
		if seen[id] {
			return TaskGrant{}, fmt.Errorf("connector: event %d is named twice for one task", id)
		}
		seen[id] = true
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return TaskGrant{}, fmt.Errorf("connector: task token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	res, err := tx.ExecContext(ctx, `INSERT INTO tasks (token_sha256, created_at) VALUES (?, ?)`, tokenHash(token), l.timestamp())
	if err != nil {
		return TaskGrant{}, fmt.Errorf("connector: create task: %w", err)
	}
	taskID, err := res.LastInsertId()
	if err != nil {
		return TaskGrant{}, fmt.Errorf("connector: create task: %w", err)
	}
	for _, id := range eventIDs {
		var acknowledge, hasInstruction int
		switch err := tx.QueryRowContext(ctx, `SELECT acknowledge, content_dropped = 0 AND snapshot IS NOT NULL FROM events WHERE id = ?`, id).Scan(&acknowledge, &hasInstruction); {
		case errors.Is(err, sql.ErrNoRows):
			return TaskGrant{}, fmt.Errorf("connector: task event %d: %w", id, ErrNoSuchRecord)
		case err != nil:
			return TaskGrant{}, fmt.Errorf("connector: task event %d: %w", id, err)
		}
		if hasInstruction == 0 {
			// A task a worker could pull nothing from would read as a task
			// with nothing left to do. A record without its instruction
			// needs a new verdict first.
			return TaskGrant{}, fmt.Errorf("connector: task event %d has no instruction: %w", id, ErrNotDispatchable)
		}
		guard := ""
		if acknowledge != 0 {
			guard = "armed"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_events (task_id, event_id, guard) VALUES (?, ?, ?)`, taskID, id, guard); err != nil {
			if isConstraint(err) {
				return TaskGrant{}, fmt.Errorf("connector: task event %d: %w", id, ErrEventOnLiveTask)
			}
			return TaskGrant{}, fmt.Errorf("connector: task event %d: %w", id, err)
		}
	}

	// One task per conversation: every dispatched record on the events'
	// conversations must be among the events this task takes. Checked after
	// every event is on the task — so an event already on a live task is told
	// as that — and before any of them moves, so the records this call
	// dispatches never count.
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(eventIDs)), ", ")
	args := make([]any, 0, len(eventIDs)*2)
	for _, id := range eventIDs {
		args = append(args, id)
	}
	for _, id := range eventIDs {
		args = append(args, id)
	}
	var busy int64
	//nolint:gosec // G202: placeholders, not values
	switch err := tx.QueryRowContext(ctx, `
SELECT other.id FROM events other
JOIN events mine ON mine.conversation_key = other.conversation_key
WHERE mine.id IN (`+placeholders+`) AND mine.conversation_key <> ''
  AND other.state = 'dispatched' AND other.id NOT IN (`+placeholders+`)
LIMIT 1`, args...).Scan(&busy); {
	case err == nil:
		return TaskGrant{}, fmt.Errorf("connector: event %d is dispatched on the same conversation: %w", busy, ErrConversationBusy)
	case !errors.Is(err, sql.ErrNoRows):
		return TaskGrant{}, fmt.Errorf("connector: read conversations: %w", err)
	}

	for _, id := range eventIDs {
		// Admitted or queued work joins a task; a dispatched record whose
		// task was superseded joins its replacement.
		moved, err := l.move(ctx, tx, transition{id: id, state: StateDispatched, from: []RecordState{StateAdmitted, StateQueued, StateDispatched}})
		if err != nil {
			return TaskGrant{}, err
		}
		if !moved {
			var state string
			_ = tx.QueryRowContext(ctx, `SELECT state FROM events WHERE id = ?`, id).Scan(&state)
			return TaskGrant{}, fmt.Errorf("connector: task event %d is %s; only admitted, queued or redispatched work joins a task", id, state)
		}
	}
	return TaskGrant{ID: taskID, Token: token}, nil
}

// SupersedeTask retires a task: its token is refused from then on, and its
// events are free to join a new task. An event the task never exposed returns
// to admitted, to be dispatched again once its conversation is free; an event
// a worker was handed stays dispatched, because that worker may have acted on
// it, and waits for its settlement or a person's redispatch.
func (l *Ledger) SupersedeTask(ctx context.Context, taskID int64) error {
	return retryBusy(func() error {
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("connector: begin supersede: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := l.supersedeTask(ctx, tx, taskID); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// supersedeTask is SupersedeTask inside the caller's transaction, so a
// redispatch can retire the old task and create the new one in one commit.
func (l *Ledger) supersedeTask(ctx context.Context, tx *sql.Tx, taskID int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT event_id FROM task_events WHERE task_id = ? AND retired_at IS NULL AND delivery = 'admitted'`, taskID)
	if err != nil {
		return fmt.Errorf("connector: supersede task %d: %w", taskID, err)
	}
	var unexposed []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("connector: supersede task %d: %w", taskID, err)
		}
		unexposed = append(unexposed, id)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("connector: supersede task %d: %w", taskID, err)
	}

	now := l.timestamp()
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET superseded_at = COALESCE(superseded_at, ?) WHERE id = ?`, now, taskID); err != nil {
		return fmt.Errorf("connector: supersede task %d: %w", taskID, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_events SET retired_at = COALESCE(retired_at, ?) WHERE task_id = ?`, now, taskID); err != nil {
		return fmt.Errorf("connector: supersede task %d: %w", taskID, err)
	}
	for _, id := range unexposed {
		// Only a record still dispatched moves: one a person or a later
		// verdict already moved stays where it was put.
		if _, err := l.move(ctx, tx, transition{id: id, state: StateAdmitted, from: []RecordState{StateDispatched}}); err != nil {
			return err
		}
	}
	return nil
}

// isConstraint reports a SQLite constraint violation.
func isConstraint(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_CONSTRAINT
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TaskDispatch is the ledger as one worker sees it: the events on the task its
// token names, and nothing else. There is no listing; a worker never reads
// other tasks.
type TaskDispatch struct {
	ledger  *Ledger
	hash    string
	agentID int64
}

// Dispatch binds the ledger to a worker's task token, refusing one that names
// no live task now. The token is checked again on every call, in the call's
// own transaction, so a redispatch that supersedes it later takes effect at
// once. agentID is the agent's Person id, whose own mentions are stripped from
// the instructions handed out.
func (l *Ledger) Dispatch(ctx context.Context, token string, agentID int64) (*TaskDispatch, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("connector: a dispatch needs the task token")
	}
	if agentID <= 0 {
		return nil, errors.New("connector: a dispatch needs the agent's Person id")
	}
	d := &TaskDispatch{ledger: l, hash: tokenHash(token), agentID: agentID}
	var live int
	if err := l.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE token_sha256 = ? AND superseded_at IS NULL`, d.hash).Scan(&live); err != nil {
		return nil, fmt.Errorf("connector: resolve task token: %w", err)
	}
	if live == 0 {
		return nil, fmt.Errorf("connector: %w", ErrTaskTokenRefused)
	}
	return d, nil
}

// Instruction is what get_dispatch hands a worker. It is an allowlist: every
// field is named here, and nothing the ledger holds reaches a worker unless
// it is one of them. No route, no feed position, no token.
type Instruction struct {
	EventID   int64  `json:"event_id"`
	EventType string `json:"event_type"`
	Trigger   string `json:"trigger"`
	Class     string `json:"class,omitempty"`

	Recording   InstructionRecording `json:"recording"`
	ReplyTo     InstructionReply     `json:"reply_to"`
	RequesterID int64                `json:"requester_id"`

	// Acknowledge says a person asked for something: the worker acknowledges
	// it and reports the id through ack_dispatch.
	Acknowledge bool `json:"acknowledge"`
	// GuardAcknowledged says the connector's guard already acknowledged the
	// event, so the worker does not acknowledge it again.
	GuardAcknowledged bool     `json:"guard_acknowledged"`
	Delivery          Delivery `json:"delivery"`

	// Content is the instruction as admission read it, with the agent's own
	// mention stripped. The live recording may be newer.
	Content          string    `json:"content"`
	ContentUpdatedAt time.Time `json:"content_updated_at"`
}

// InstructionRecording points at the recording the event is about.
type InstructionRecording struct {
	BucketID    int64  `json:"bucket_id"`
	RecordingID int64  `json:"recording_id"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	URL         string `json:"url"`
}

// InstructionReply is where the worker's acknowledgement and reply go.
type InstructionReply struct {
	Kind        string `json:"kind"`
	RecordingID int64  `json:"recording_id"`
}

// Receipt is the delivery state an acknowledgement or completion left.
type Receipt struct {
	EventID  int64    `json:"event_id"`
	Delivery Delivery `json:"delivery"`
	AckID    *int64   `json:"ack_id,omitempty"`
	Outcome  Outcome  `json:"outcome,omitempty"`
	ReplyID  *int64   `json:"reply_id,omitempty"`
	Links    []string `json:"links,omitempty"`
}

// Completion is a worker's report for one event.
type Completion struct {
	Outcome Outcome
	Links   []string
	ReplyID *int64
}

// Completion limits: a report is a handful of links, not a document.
const (
	maxCompletionLinks = 20
	maxLinkLength      = 2048
)

// Get returns the instruction for eventID, or for the earliest event on the
// task not yet acknowledged when eventID is zero; ok is false when there is
// none. Handing out an event that was never exposed writes exposed before the
// instruction is returned, and cancels an armed guard. A repeat returns the
// same instruction and writes nothing.
//
// Only an event whose record is still on the way to a worker is handed out:
// dispatched, or completed, with its content. The earliest skips any other,
// so one event withdrawn or blocked never hides the rest of the task.
func (d *TaskDispatch) Get(ctx context.Context, eventID int64) (Instruction, bool, error) {
	var (
		out Instruction
		ok  bool
	)
	err := retryBusy(func() error {
		var err error
		out, ok, err = d.get(ctx, eventID)
		return err
	})
	return out, ok, err
}

type taskEvent struct {
	delivery Delivery
	guard    string
	ackID    sql.NullInt64
	outcome  string
	links    string
	replyID  sql.NullInt64
}

func (d *TaskDispatch) get(ctx context.Context, eventID int64) (Instruction, bool, error) {
	l := d.ledger
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return Instruction{}, false, fmt.Errorf("connector: begin get_dispatch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	taskID, err := d.task(ctx, tx)
	if err != nil {
		return Instruction{}, false, err
	}

	if eventID == 0 {
		err := tx.QueryRowContext(ctx, `
SELECT te.event_id FROM task_events te JOIN events e ON e.id = te.event_id
WHERE te.task_id = ? AND te.delivery IN ('admitted', 'exposed') AND `+servableSQL+`
ORDER BY te.event_id LIMIT 1`, taskID).Scan(&eventID)
		if errors.Is(err, sql.ErrNoRows) {
			return Instruction{}, false, nil
		}
		if err != nil {
			return Instruction{}, false, fmt.Errorf("connector: get_dispatch: %w", err)
		}
	}
	te, err := loadTaskEvent(ctx, tx, taskID, eventID)
	if err != nil {
		return Instruction{}, false, err
	}
	record, err := loadRecord(ctx, tx, eventID)
	if err != nil {
		return Instruction{}, false, err
	}
	if !servable(record, te.delivery) {
		return Instruction{}, false, fmt.Errorf("connector: event %d: %w", eventID, ErrNotDispatchable)
	}

	now := l.timestamp()
	wrote := false
	if te.delivery == DeliveryAdmitted {
		// Written before anything about the event leaves this call: a worker
		// handed an instruction may act on it whether or not it reports.
		if _, err := tx.ExecContext(ctx, `UPDATE task_events SET delivery = 'exposed', exposed_at = ? WHERE task_id = ? AND event_id = ? AND delivery = 'admitted'`, now, taskID, eventID); err != nil {
			return Instruction{}, false, fmt.Errorf("connector: expose event %d: %w", eventID, err)
		}
		wrote = true
	}
	if te.guard == "armed" {
		if _, err := tx.ExecContext(ctx, `UPDATE task_events SET guard = 'canceled' WHERE task_id = ? AND event_id = ? AND guard = 'armed'`, taskID, eventID); err != nil {
			return Instruction{}, false, fmt.Errorf("connector: cancel guard on %d: %w", eventID, err)
		}
		wrote = true
	}
	if wrote {
		// Read back what the writes left rather than what was read before
		// them: the instruction reports the row, not this call's expectation
		// of it.
		if te, err = loadTaskEvent(ctx, tx, taskID, eventID); err != nil {
			return Instruction{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return Instruction{}, false, fmt.Errorf("connector: commit get_dispatch: %w", err)
		}
	}

	var snapshot struct {
		Type      string    `json:"type"`
		Title     string    `json:"title"`
		AppURL    string    `json:"app_url"`
		Content   string    `json:"content"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal(record.Decision.Snapshot, &snapshot); err != nil {
		return Instruction{}, false, fmt.Errorf("connector: event %d snapshot: %w", eventID, err)
	}
	return Instruction{
		EventID:   record.ID,
		EventType: record.EventType,
		Trigger:   record.Decision.Trigger,
		Class:     record.Decision.Class,
		Recording: InstructionRecording{
			BucketID:    record.BucketID,
			RecordingID: record.RecordingID,
			Type:        snapshot.Type,
			Title:       snapshot.Title,
			URL:         record.Decision.RecordingURL,
		},
		ReplyTo:           InstructionReply{Kind: record.Decision.ReplyKind, RecordingID: record.Decision.ReplyRecordingID},
		RequesterID:       record.Decision.RequesterID,
		Acknowledge:       record.Decision.Acknowledge,
		GuardAcknowledged: te.guard == "fired",
		Delivery:          te.delivery,
		Content:           StripMentionsOf(snapshot.Content, d.agentID),
		ContentUpdatedAt:  snapshot.UpdatedAt,
	}, true, nil
}

// servable is whether an event on a task is handed to its worker: its record
// is dispatched, or completed after this worker was exposed to it — finished
// work is never handed out for the first time — and it still has its
// instruction. servableSQL is the same rule over task_events te and events e,
// for the earliest-event query; the two are kept side by side so they cannot
// drift.
func servable(record Record, delivery Delivery) bool {
	state := record.State == StateDispatched || (record.State == StateCompleted && delivery != DeliveryAdmitted)
	return state && !record.ContentDropped && len(record.Decision.Snapshot) > 0
}

const servableSQL = `(e.state = 'dispatched' OR (e.state = 'completed' AND te.delivery <> 'admitted'))
  AND e.content_dropped = 0 AND e.snapshot IS NOT NULL AND length(e.snapshot) > 0`

// Ack records the worker's acknowledgement: delivery moves to delivered, and
// ackID, when given, is the worker's own boost or comment. A repeat — a lost
// tool response retried — answers the same receipt.
func (d *TaskDispatch) Ack(ctx context.Context, eventID int64, ackID *int64) (Receipt, error) {
	var out Receipt
	err := retryBusy(func() error {
		var err error
		out, err = d.report(ctx, eventID, func(ctx context.Context, tx *sql.Tx, taskID int64, te taskEvent) (bool, error) {
			if ackID != nil && te.ackID.Valid && te.ackID.Int64 != *ackID {
				return false, fmt.Errorf("connector: event %d acknowledged as %d: %w", eventID, te.ackID.Int64, ErrReportConflict)
			}
			if te.delivery != DeliveryExposed && (ackID == nil || te.ackID.Valid) {
				return false, nil
			}
			_, err := tx.ExecContext(ctx, `
UPDATE task_events
SET delivery = CASE WHEN delivery = 'exposed' THEN 'delivered' ELSE delivery END,
    delivered_at = COALESCE(delivered_at, ?),
    ack_id = COALESCE(ack_id, ?)
WHERE task_id = ? AND event_id = ?`, d.ledger.timestamp(), nullableID(ackID), taskID, eventID)
			return true, err
		})
		return err
	})
	return out, err
}

// Complete records the worker's outcome and acknowledges the event if it was
// not already. The report is recorded whatever has happened to the record
// since the worker was handed it, because it is what the worker did; the
// record moves to completed when it is dispatched. A repeat of the same report
// answers the same receipt; a different one is refused, because a reported
// outcome stands.
func (d *TaskDispatch) Complete(ctx context.Context, eventID int64, c Completion) (Receipt, error) {
	if c.Outcome != OutcomeSucceeded && c.Outcome != OutcomeFailed {
		return Receipt{}, fmt.Errorf("connector: outcome must be %q or %q: %w", OutcomeSucceeded, OutcomeFailed, ErrInvalidReport)
	}
	links, err := normalizeLinks(c.Links)
	if err != nil {
		return Receipt{}, err
	}
	encoded, err := json.Marshal(links)
	if err != nil {
		return Receipt{}, err
	}
	var out Receipt
	err = retryBusy(func() error {
		var err error
		out, err = d.report(ctx, eventID, func(ctx context.Context, tx *sql.Tx, taskID int64, te taskEvent) (bool, error) {
			if te.delivery == DeliveryCompleted {
				if te.outcome == string(c.Outcome) && te.links == string(encoded) && sameID(te.replyID, c.ReplyID) {
					return false, nil
				}
				return false, fmt.Errorf("connector: event %d completed as %s: %w", eventID, te.outcome, ErrReportConflict)
			}
			if _, err := d.ledger.move(ctx, tx, transition{id: eventID, state: StateCompleted, from: []RecordState{StateDispatched}}); err != nil {
				return false, err
			}
			now := d.ledger.timestamp()
			_, err = tx.ExecContext(ctx, `
UPDATE task_events
SET delivery = 'completed', delivered_at = COALESCE(delivered_at, ?), completed_at = ?,
    outcome = ?, links = ?, reply_id = ?
WHERE task_id = ? AND event_id = ?`, now, now, string(c.Outcome), string(encoded), nullableID(c.ReplyID), taskID, eventID)
			return true, err
		})
		return err
	})
	return out, err
}

// report runs an acknowledgement or completion in one transaction: the token
// checked, the event found on the task and known to have been exposed, apply
// deciding whether to write, and the receipt read back.
func (d *TaskDispatch) report(ctx context.Context, eventID int64, apply func(context.Context, *sql.Tx, int64, taskEvent) (bool, error)) (Receipt, error) {
	if eventID <= 0 {
		return Receipt{}, errors.New("connector: event_id must name an event")
	}
	tx, err := d.ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return Receipt{}, fmt.Errorf("connector: begin report: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	taskID, err := d.task(ctx, tx)
	if err != nil {
		return Receipt{}, err
	}
	te, err := loadTaskEvent(ctx, tx, taskID, eventID)
	if err != nil {
		return Receipt{}, err
	}
	if te.delivery == DeliveryAdmitted {
		return Receipt{}, fmt.Errorf("connector: event %d: %w", eventID, ErrNotExposed)
	}
	wrote, err := apply(ctx, tx, taskID, te)
	if err != nil {
		return Receipt{}, fmt.Errorf("connector: report on event %d: %w", eventID, err)
	}
	if wrote {
		if te, err = loadTaskEvent(ctx, tx, taskID, eventID); err != nil {
			return Receipt{}, err
		}
		if err := tx.Commit(); err != nil {
			return Receipt{}, fmt.Errorf("connector: commit report on %d: %w", eventID, err)
		}
	}
	receipt := Receipt{EventID: eventID, Delivery: te.delivery, Outcome: Outcome(te.outcome)}
	if te.ackID.Valid {
		receipt.AckID = &te.ackID.Int64
	}
	if te.replyID.Valid {
		receipt.ReplyID = &te.replyID.Int64
	}
	if err := json.Unmarshal([]byte(te.links), &receipt.Links); err != nil {
		return Receipt{}, fmt.Errorf("connector: event %d links: %w", eventID, err)
	}
	return receipt, nil
}

// task resolves the token to its task inside tx, or refuses it.
func (d *TaskDispatch) task(ctx context.Context, tx *sql.Tx) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE token_sha256 = ? AND superseded_at IS NULL`, d.hash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("connector: %w", ErrTaskTokenRefused)
	}
	if err != nil {
		return 0, fmt.Errorf("connector: resolve task token: %w", err)
	}
	return id, nil
}

func loadTaskEvent(ctx context.Context, tx *sql.Tx, taskID, eventID int64) (taskEvent, error) {
	var (
		te       taskEvent
		delivery string
	)
	err := tx.QueryRowContext(ctx, `
SELECT delivery, guard, ack_id, outcome, links, reply_id
FROM task_events WHERE task_id = ? AND event_id = ?`, taskID, eventID).Scan(&delivery, &te.guard, &te.ackID, &te.outcome, &te.links, &te.replyID)
	if errors.Is(err, sql.ErrNoRows) {
		return te, fmt.Errorf("connector: event %d: %w", eventID, ErrNotOnTask)
	}
	if err != nil {
		return te, fmt.Errorf("connector: load task event %d: %w", eventID, err)
	}
	te.delivery = Delivery(delivery)
	return te, nil
}

func loadRecord(ctx context.Context, tx *sql.Tx, id int64) (Record, error) {
	rows, err := tx.QueryContext(ctx, selectRecords+` WHERE id = ?`, id)
	if err != nil {
		return Record{}, fmt.Errorf("connector: load event %d: %w", id, err)
	}
	records, err := scanRecords(rows)
	if err != nil {
		return Record{}, err
	}
	if len(records) == 0 {
		return Record{}, fmt.Errorf("connector: event %d: %w", id, ErrNoSuchRecord)
	}
	return records[0], nil
}

func normalizeLinks(links []string) ([]string, error) {
	if len(links) > maxCompletionLinks {
		return nil, fmt.Errorf("connector: at most %d links: %w", maxCompletionLinks, ErrInvalidReport)
	}
	out := make([]string, 0, len(links))
	for _, link := range links {
		if len(link) > maxLinkLength {
			return nil, fmt.Errorf("connector: a link is at most %d characters: %w", maxLinkLength, ErrInvalidReport)
		}
		u, err := url.Parse(link)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("connector: link %q is not an http(s) URL: %w", link, ErrInvalidReport)
		}
		out = append(out, link)
	}
	return out, nil
}

func nullableID(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

func sameID(stored sql.NullInt64, given *int64) bool {
	if given == nil {
		return !stored.Valid
	}
	return stored.Valid && stored.Int64 == *given
}

// StripMentionsOf removes every mention of personID from rich text, and
// leaves the rest as it was. A worker handed its own mention reads an
// instruction addressed to itself, which says nothing the dispatch does not
// already say.
//
// What counts as a mention is exactly what basecamp.MentionedPersonIDs — the
// reader admission decided the trigger with — counts: the tag walk below is
// that reader's, rule for rule (comments, "<!", "<?" and end tags skipped,
// punctuation part of a name, the first sgid attribute authoritative even when
// empty, entities decoded, an unterminated tag ending the markup). The two
// are held to agreement by a differential test and a fuzz target over hostile
// markup, not by review.
//
// A mention element runs from its start tag to the first end tag of the same
// name, unless another attachment starts first or none closes, in which case
// the start tag stands alone. What it leaves behind is a space, not nothing:
// closing the gap could join a "<" before the element to the text after it
// into a tag that swallows what follows — someone else's mention included —
// and a space can never begin one.
func StripMentionsOf(richText string, personID int64) string {
	if !slices.Contains(basecamp.MentionedPersonIDs(richText), personID) {
		return richText
	}
	out, _ := stripOnce(richText, personID)
	if slices.Contains(basecamp.MentionedPersonIDs(out), personID) {
		// The walk is the reader's and a removal cannot join what is around
		// it, so one pass removes every mention the reader reads: this is a
		// backstop, and no corpus or fuzz input has reached it. Handing the
		// text out would put the agent's own mention in front of the worker,
		// and handing out nothing would lose the instruction; the escaped
		// text keeps the words and no markup.
		return html.EscapeString(out)
	}
	return out
}

// strippedMention is what a removed mention leaves in the text.
const strippedMention = " "

// stripOnce removes each mention element of personID the walk finds, and
// returns the text and the removed spans, as offsets into text.
func stripOnce(text string, personID int64) (string, [][2]int) {
	var (
		out     strings.Builder
		removed [][2]int
	)
	pos := 0
	for pos < len(text) {
		t, ok := nextMarkup(text, pos)
		if !ok {
			break
		}
		if !t.isEnd && strings.EqualFold(t.name, "bc-attachment") {
			if id, isPerson := basecamp.PersonIDFromSGID(t.sgid); isPerson && id == personID {
				out.WriteString(text[pos:t.start])
				out.WriteString(strippedMention)
				pos = mentionEnd(text, t.end)
				removed = append(removed, [2]int{t.start, pos})
				continue
			}
		}
		out.WriteString(text[pos:t.end])
		pos = t.end
	}
	out.WriteString(text[pos:])
	return out.String(), removed
}

// mentionEnd is where the mention whose start tag ends at from ends: after the
// first </bc-attachment>, or at from when another attachment starts first or
// none closes.
func mentionEnd(text string, from int) int {
	for at := from; ; {
		t, ok := nextMarkup(text, at)
		if !ok {
			return from
		}
		if strings.EqualFold(t.name, "bc-attachment") {
			if t.isEnd {
				return t.end
			}
			return from
		}
		at = t.end
	}
}

// markup is one start or end tag the walk found: where it starts and ends,
// its name, whether it is an end tag, and a start tag's first sgid, decoded.
type markup struct {
	start, end int
	name       string
	isEnd      bool
	sgid       string
}

// nextMarkup returns the next start or end tag at or after pos, walking the
// text as basecamp.MentionedPersonIDs does. ok is false when the markup ends:
// no "<" left, or a comment, declaration or tag left unterminated, after
// which nothing is markup.
func nextMarkup(text string, pos int) (markup, bool) {
	for pos < len(text) {
		i := strings.IndexByte(text[pos:], '<')
		if i < 0 {
			return markup{}, false
		}
		start := pos + i
		pos = start + 1
		rest := text[pos:]
		switch {
		case strings.HasPrefix(rest, "!--"):
			stop := strings.Index(rest, "-->")
			if stop < 0 {
				return markup{}, false
			}
			pos += stop + 3
			continue
		case strings.HasPrefix(rest, "/"):
			stop := strings.IndexByte(rest, '>')
			if stop < 0 {
				return markup{}, false
			}
			nameEnd := 1
			for nameEnd < len(rest) && isMarkupNameChar(rest[nameEnd]) {
				nameEnd++
			}
			return markup{start: start, end: pos + stop + 1, name: rest[1:nameEnd], isEnd: true}, true
		case strings.HasPrefix(rest, "!"), strings.HasPrefix(rest, "?"):
			stop := strings.IndexByte(rest, '>')
			if stop < 0 {
				return markup{}, false
			}
			pos += stop + 1
			continue
		}
		nameEnd := 0
		for nameEnd < len(rest) && isMarkupNameChar(rest[nameEnd]) {
			nameEnd++
		}
		if nameEnd == 0 {
			continue // a bare "<" in text
		}
		sgid, end, ok := scanAttributes(text, pos+nameEnd)
		if !ok {
			return markup{}, false
		}
		return markup{start: start, end: end, name: rest[:nameEnd], sgid: sgid}, true
	}
	return markup{}, false
}

// isMarkupNameChar is what may follow "<" in a tag name: everything but space,
// "/", ">", "<", "=" and quotes, so "<bc-attachment.x" is its own name.
func isMarkupNameChar(c byte) bool {
	return !isMarkupSpace(c) && c != '/' && c != '>' && c != '<' && c != '=' && c != '"' && c != '\''
}

func isMarkupSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// scanAttributes walks a start tag's attributes from pos to its ">", and
// returns the first sgid attribute's decoded value (empty when absent or
// empty), the index after the ">", and whether the tag closed.
func scanAttributes(text string, pos int) (sgid string, end int, ok bool) {
	seen := false
	for pos < len(text) {
		for pos < len(text) && (isMarkupSpace(text[pos]) || text[pos] == '/') {
			pos++
		}
		if pos >= len(text) {
			return sgid, pos, false
		}
		if text[pos] == '>' {
			return sgid, pos + 1, true
		}
		nameStart := pos
		for pos < len(text) && !isMarkupSpace(text[pos]) && text[pos] != '=' && text[pos] != '>' && text[pos] != '/' {
			pos++
		}
		name := text[nameStart:pos]
		for pos < len(text) && isMarkupSpace(text[pos]) {
			pos++
		}
		value := ""
		if pos < len(text) && text[pos] == '=' {
			pos++
			for pos < len(text) && isMarkupSpace(text[pos]) {
				pos++
			}
			if pos < len(text) && (text[pos] == '"' || text[pos] == '\'') {
				quote := text[pos]
				pos++
				closing := strings.IndexByte(text[pos:], quote)
				if closing < 0 {
					return sgid, len(text), false
				}
				value = text[pos : pos+closing]
				pos += closing + 1
			} else {
				valueStart := pos
				for pos < len(text) && !isMarkupSpace(text[pos]) && text[pos] != '>' {
					pos++
				}
				value = text[valueStart:pos]
			}
		}
		if name == "" {
			pos++
			continue
		}
		if !seen && strings.EqualFold(name, "sgid") {
			seen = true
			sgid = html.UnescapeString(value)
		}
	}
	return sgid, pos, false
}

// StateDirName is the connector's state directory for one account and agent,
// "<account>-<agent person id>", inside StateRoot.
func StateDirName(accountID string, agentID int64) string {
	return accountID + "-" + strconv.FormatInt(agentID, 10)
}

// ErrNotAStateDir is a directory that is not a connector state directory for
// the account asked about. StateDirError carries why.
var ErrNotAStateDir = errors.New("not the connector's state directory for this account")

// StateDirError says which rule a state directory failed, in fields a caller
// can build its own message from rather than by reading this one.
type StateDirError struct {
	// Dir is the directory as given, made absolute.
	Dir string
	// Root is the connector's state root, the only place a state directory
	// lives.
	Root string
	// Account is the account the directory names, empty when it names none;
	// Want is the account it had to name.
	Account, Want string
	// Why is the rule it failed.
	Why StateDirProblem
}

// StateDirProblem is why a state directory was refused.
type StateDirProblem string

const (
	// StateDirElsewhere is a directory outside the state root.
	StateDirElsewhere StateDirProblem = "outside the connector's state root"
	// StateDirMisnamed is a directory not named <account>-<agent person id>.
	StateDirMisnamed StateDirProblem = "not named <account>-<agent person id>"
	// StateDirOtherAccount is another account's state directory.
	StateDirOtherAccount StateDirProblem = "another account's"
)

func (e *StateDirError) Error() string {
	switch e.Why {
	case StateDirElsewhere:
		return fmt.Sprintf("%s is not inside %s", e.Dir, e.Root)
	case StateDirOtherAccount:
		return fmt.Sprintf("%s belongs to account %s, not %s", e.Dir, e.Account, e.Want)
	default:
		return fmt.Sprintf("%s is not named <account>-<agent person id>", e.Dir)
	}
}

func (e *StateDirError) Unwrap() error { return ErrNotAStateDir }

// StateRoot is where every connector state directory lives:
// $XDG_STATE_HOME/basecamp/connect, or ~/.local/state/basecamp/connect when
// XDG_STATE_HOME is unset or not absolute, as the XDG specification says.
func StateRoot() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", fmt.Errorf("connector: no state home: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(filepath.Clean(base), "basecamp", "connect"), nil
}

// ResolveStateDir is the one place a state directory is accepted: dir must
// be exactly StateRoot/<account>-<agent person id>, and its account must be
// accountID, compared as numbers. It returns the agent's Person id.
//
// The location is part of the check, not only the name. A directory named
// for this account anywhere else — a copy of another account's ledger renamed
// to match — is refused, because the name is what binds a ledger to an
// account and anyone can choose a name.
func ResolveStateDir(dir, accountID string) (int64, error) {
	root, err := StateRoot()
	if err != nil {
		return 0, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return 0, fmt.Errorf("connector: state directory %q: %w", dir, err)
	}
	refuse := func(why StateDirProblem, account string) (int64, error) {
		return 0, &StateDirError{Dir: abs, Root: root, Account: account, Want: accountID, Why: why}
	}
	if filepath.Dir(abs) != root {
		return refuse(StateDirElsewhere, "")
	}
	account, agent, ok := strings.Cut(filepath.Base(abs), "-")
	agentID, err := strconv.ParseInt(agent, 10, 64)
	if !ok || err != nil || agentID <= 0 {
		return refuse(StateDirMisnamed, account)
	}
	given, errGiven := strconv.ParseUint(account, 10, 64)
	want, errWant := strconv.ParseUint(accountID, 10, 64)
	if errGiven != nil || errWant != nil || given == 0 || given != want {
		return refuse(StateDirOtherAccount, account)
	}
	return agentID, nil
}

// LedgerFile is the ledger's file name inside the state directory.
const LedgerFile = "ledger.db"
