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
		var acknowledge int
		switch err := tx.QueryRowContext(ctx, `SELECT acknowledge FROM events WHERE id = ?`, id).Scan(&acknowledge); {
		case errors.Is(err, sql.ErrNoRows):
			return TaskGrant{}, fmt.Errorf("connector: task event %d: %w", id, ErrNoSuchRecord)
		case err != nil:
			return TaskGrant{}, fmt.Errorf("connector: task event %d: %w", id, err)
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
// events are free to join a new task.
func (l *Ledger) SupersedeTask(ctx context.Context, taskID int64) error {
	return retryBusy(func() error {
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("connector: begin supersede: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		now := l.timestamp()
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET superseded_at = COALESCE(superseded_at, ?) WHERE id = ?`, now, taskID); err != nil {
			return fmt.Errorf("connector: supersede task %d: %w", taskID, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task_events SET retired_at = COALESCE(retired_at, ?) WHERE task_id = ?`, now, taskID); err != nil {
			return fmt.Errorf("connector: supersede task %d: %w", taskID, err)
		}
		return tx.Commit()
	})
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
WHERE te.task_id = ? AND te.delivery IN ('admitted', 'exposed')
  AND e.state = 'dispatched' AND e.content_dropped = 0 AND e.snapshot IS NOT NULL
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
	servable := record.State == StateDispatched || record.State == StateCompleted
	if !servable || record.ContentDropped || len(record.Decision.Snapshot) == 0 {
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
		te.delivery, wrote = DeliveryExposed, true
	}
	if te.guard == "armed" {
		if _, err := tx.ExecContext(ctx, `UPDATE task_events SET guard = 'canceled' WHERE task_id = ? AND event_id = ? AND guard = 'armed'`, taskID, eventID); err != nil {
			return Instruction{}, false, fmt.Errorf("connector: cancel guard on %d: %w", eventID, err)
		}
		wrote = true
	}
	if wrote {
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
// leaves every other attachment — other people's mentions, files — as it was.
// A worker handed its own mention reads an instruction addressed to itself,
// which says nothing the dispatch does not already say.
//
// The markup is walked tag by tag, the way the SDK's mention reader walks it,
// so the two agree on what a mention is: a comment is not markup, a ">" in a
// quoted attribute does not end its tag, either quote style works, and the
// sgid is entity-decoded before it is read. A mention element runs from its
// start tag to the first closing tag, unless another attachment starts first
// or none closes, in which case the start tag stands alone.
func StripMentionsOf(richText string, personID int64) string {
	var out strings.Builder
	pos := 0
	for pos < len(richText) {
		t, ok := nextTag(richText, pos)
		if !ok {
			break
		}
		out.WriteString(richText[pos:t.start])
		pos = t.end
		if strings.EqualFold(t.name, "bc-attachment") {
			if id, isPerson := basecamp.PersonIDFromSGID(html.UnescapeString(t.sgid)); isPerson && id == personID {
				pos = mentionEnd(richText, t.end)
				continue
			}
		}
		out.WriteString(richText[t.start:t.end])
	}
	out.WriteString(richText[pos:])
	return out.String()
}

// mentionEnd is where the mention whose start tag ends at from ends: after its
// closing tag, or at from when another attachment starts first or none closes.
func mentionEnd(text string, from int) int {
	for at := from; ; {
		t, ok := nextTag(text, at)
		if !ok || strings.EqualFold(t.name, "bc-attachment") {
			return from
		}
		if strings.EqualFold(t.name, "/bc-attachment") {
			return t.end
		}
		at = t.end
	}
}

// tag is one start or end tag: its bounds, its name ("/name" for an end tag)
// and its sgid attribute, raw.
type tag struct {
	start, end int
	name, sgid string
}

// nextTag finds the next complete tag at or after pos, skipping comments. ok
// is false when none remains; a tag or comment left unterminated ends the
// markup, as it does for a browser.
func nextTag(text string, pos int) (tag, bool) {
	for pos < len(text) {
		i := strings.IndexByte(text[pos:], '<')
		if i < 0 {
			return tag{}, false
		}
		start := pos + i
		rest := text[start+1:]
		if strings.HasPrefix(rest, "!--") {
			stop := strings.Index(rest[3:], "-->")
			if stop < 0 {
				return tag{}, false
			}
			pos = start + 1 + 3 + stop + 3
			continue
		}
		n := 0
		if strings.HasPrefix(rest, "/") {
			n = 1
		}
		nameStart := n
		for n < len(rest) && isTagNameByte(rest[n]) {
			n++
		}
		if n == nameStart {
			pos = start + 1
			continue
		}
		t := tag{start: start, name: rest[:n]}
		at := start + 1 + n
		for at < len(text) {
			c := text[at]
			switch {
			case c == '>':
				t.end = at + 1
				return t, true
			case isTagNameByte(c):
				attrStart := at
				for at < len(text) && isTagNameByte(text[at]) {
					at++
				}
				attr := text[attrStart:at]
				for at < len(text) && isSpaceByte(text[at]) {
					at++
				}
				if at >= len(text) || text[at] != '=' {
					continue
				}
				at++
				for at < len(text) && isSpaceByte(text[at]) {
					at++
				}
				value, next := attributeValue(text, at)
				if next < 0 {
					return tag{}, false
				}
				if t.sgid == "" && strings.EqualFold(attr, "sgid") {
					t.sgid = value
				}
				at = next
			default:
				at++
			}
		}
		return tag{}, false
	}
	return tag{}, false
}

// attributeValue reads a quoted or bare attribute value at pos and returns it
// with the position after it; next is -1 for an unterminated quote.
func attributeValue(text string, pos int) (value string, next int) {
	if pos < len(text) && (text[pos] == '"' || text[pos] == '\'') {
		end := strings.IndexByte(text[pos+1:], text[pos])
		if end < 0 {
			return "", -1
		}
		return text[pos+1 : pos+1+end], pos + end + 2
	}
	end := pos
	for end < len(text) && !isSpaceByte(text[end]) && text[end] != '>' {
		end++
	}
	return text[pos:end], end
}

func isTagNameByte(c byte) bool {
	return c == '-' || c == '_' || c == ':' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

// StateDirName is the connector's state directory for one account and agent:
// "<account>-<agent person id>", under $XDG_STATE_HOME/basecamp/connect/.
func StateDirName(accountID string, agentID int64) string {
	return accountID + "-" + strconv.FormatInt(agentID, 10)
}

// LedgerFile is the ledger's file name inside the state directory.
const LedgerFile = "ledger.db"
