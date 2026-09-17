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
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

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
	// (a person discarded it, or its content was dropped) after it joined
	// the task.
	ErrNotDispatchable = errors.New("the event can no longer be dispatched")
)

// TaskGrant is a new task and the token that binds a worker to it. The token
// is returned once and stored only as a hash.
type TaskGrant struct {
	ID    int64
	Token string
}

// CreateTask puts admitted or queued records on a new task, each at delivery
// admitted, with the acknowledgement guard armed for the records whose
// verdict asks for an acknowledgement.
func (l *Ledger) CreateTask(ctx context.Context, eventIDs []int64) (TaskGrant, error) {
	if len(eventIDs) == 0 {
		return TaskGrant{}, errors.New("connector: a task needs at least one event")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return TaskGrant{}, fmt.Errorf("connector: task token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskGrant{}, fmt.Errorf("connector: begin task: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `INSERT INTO tasks (token_sha256, created_at) VALUES (?, ?)`, tokenHash(token), l.timestamp())
	if err != nil {
		return TaskGrant{}, fmt.Errorf("connector: create task: %w", err)
	}
	taskID, err := res.LastInsertId()
	if err != nil {
		return TaskGrant{}, fmt.Errorf("connector: create task: %w", err)
	}
	for _, id := range eventIDs {
		var (
			state       string
			acknowledge int
		)
		switch err := tx.QueryRowContext(ctx, `SELECT state, acknowledge FROM events WHERE id = ?`, id).Scan(&state, &acknowledge); {
		case errors.Is(err, sql.ErrNoRows):
			return TaskGrant{}, fmt.Errorf("connector: task event %d: %w", id, ErrNoSuchRecord)
		case err != nil:
			return TaskGrant{}, fmt.Errorf("connector: task event %d: %w", id, err)
		}
		if RecordState(state) != StateAdmitted && RecordState(state) != StateQueued {
			return TaskGrant{}, fmt.Errorf("connector: task event %d is %s; only an admitted or queued record joins a task", id, state)
		}
		guard := ""
		if acknowledge != 0 {
			guard = "armed"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_events (task_id, event_id, guard) VALUES (?, ?, ?)`, taskID, id, guard); err != nil {
			return TaskGrant{}, fmt.Errorf("connector: task event %d: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return TaskGrant{}, fmt.Errorf("connector: commit task: %w", err)
	}
	return TaskGrant{ID: taskID, Token: token}, nil
}

// SupersedeTask retires a task's token. Every later dispatch call made with
// it is refused.
func (l *Ledger) SupersedeTask(ctx context.Context, taskID int64) error {
	_, err := l.db.ExecContext(ctx, `UPDATE tasks SET superseded_at = COALESCE(superseded_at, ?) WHERE id = ?`, l.timestamp(), taskID)
	if err != nil {
		return fmt.Errorf("connector: supersede task %d: %w", taskID, err)
	}
	return nil
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

// Dispatch binds the ledger to a worker's task token. The token is checked on
// every call, in the call's own transaction, so a redispatch that supersedes
// it takes effect at once. agentID is the agent's Person id, whose own
// mentions are stripped from the instructions handed out.
func (l *Ledger) Dispatch(token string, agentID int64) (*TaskDispatch, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("connector: a dispatch needs the task token")
	}
	if agentID <= 0 {
		return nil, errors.New("connector: a dispatch needs the agent's Person id")
	}
	return &TaskDispatch{ledger: l, hash: tokenHash(token), agentID: agentID}, nil
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
// none. Handing out an event that was never exposed writes exposed — and moves
// its record to dispatched — before the instruction is returned, and cancels
// an armed guard. A repeat returns the same instruction and writes nothing.
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
SELECT event_id FROM task_events
WHERE task_id = ? AND delivery IN ('admitted', 'exposed')
ORDER BY event_id LIMIT 1`, taskID).Scan(&eventID)
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
	if record.ContentDropped || len(record.Decision.Snapshot) == 0 {
		return Instruction{}, false, fmt.Errorf("connector: event %d: %w", eventID, ErrNotDispatchable)
	}

	now := l.timestamp()
	wrote := false
	if te.delivery == DeliveryAdmitted {
		// Exposure is written before anything about the event leaves this
		// call, and the record moves to dispatched with it: a worker that
		// was handed an instruction may act on it whether or not it reports.
		switch record.State {
		case StateAdmitted, StateQueued:
			moved, err := l.move(ctx, tx, transition{id: eventID, state: StateDispatched, from: []RecordState{StateAdmitted, StateQueued}})
			if err != nil {
				return Instruction{}, false, err
			}
			if !moved {
				return Instruction{}, false, fmt.Errorf("connector: event %d: %w", eventID, ErrNotDispatchable)
			}
		case StateDispatched:
		default:
			return Instruction{}, false, fmt.Errorf("connector: event %d is %s: %w", eventID, record.State, ErrNotDispatchable)
		}
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
// not already; the record moves to completed. A repeat of the same report
// answers the same receipt; a different one is refused, because a reported
// outcome stands.
func (d *TaskDispatch) Complete(ctx context.Context, eventID int64, c Completion) (Receipt, error) {
	if c.Outcome != OutcomeSucceeded && c.Outcome != OutcomeFailed {
		return Receipt{}, fmt.Errorf("connector: outcome must be %q or %q", OutcomeSucceeded, OutcomeFailed)
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
			moved, err := d.ledger.move(ctx, tx, transition{id: eventID, state: StateCompleted, from: []RecordState{StateDispatched}})
			if err != nil {
				return false, err
			}
			if !moved {
				return false, fmt.Errorf("connector: event %d: %w", eventID, ErrNotDispatchable)
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
		return nil, fmt.Errorf("connector: at most %d links", maxCompletionLinks)
	}
	out := make([]string, 0, len(links))
	for _, link := range links {
		if len(link) > maxLinkLength {
			return nil, fmt.Errorf("connector: a link is at most %d characters", maxLinkLength)
		}
		u, err := url.Parse(link)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("connector: link %q is not an http(s) URL", link)
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

// attachmentOpen is a <bc-attachment> start tag, and attachmentSGID its sgid
// attribute. Basecamp serves rich text sanitized, with attributes
// double-quoted and no attachment nested inside another.
var (
	attachmentOpen  = regexp.MustCompile(`(?i)<bc-attachment\b[^>]*>`)
	attachmentClose = regexp.MustCompile(`(?i)</bc-attachment\s*>`)
	attachmentSGID  = regexp.MustCompile(`(?i)\ssgid\s*=\s*"([^"]*)"`)
)

// StripMentionsOf removes every mention of personID from rich text, and
// leaves every other attachment — other people's mentions, files — as it was.
// A worker handed its own mention reads an instruction addressed to itself,
// which says nothing the dispatch does not already say.
//
// A mention element runs from its start tag to the first closing tag, unless
// another attachment starts first or none closes, in which case the start
// tag — self-closing, or never closed — stands alone.
func StripMentionsOf(richText string, personID int64) string {
	var out strings.Builder
	pos := 0
	for {
		loc := attachmentOpen.FindStringIndex(richText[pos:])
		if loc == nil {
			out.WriteString(richText[pos:])
			return out.String()
		}
		start, end := pos+loc[0], pos+loc[1]
		out.WriteString(richText[pos:start])
		pos = end

		tag := richText[start:end]
		match := attachmentSGID.FindStringSubmatch(tag)
		id, ok := int64(0), false
		if match != nil {
			id, ok = basecamp.PersonIDFromSGID(unescapeAttribute(match[1]))
		}
		if !ok || id != personID {
			out.WriteString(tag)
			continue
		}
		rest := richText[end:]
		closing := attachmentClose.FindStringIndex(rest)
		next := attachmentOpen.FindStringIndex(rest)
		if closing != nil && (next == nil || closing[0] < next[0]) {
			pos = end + closing[1]
		}
	}
}

func unescapeAttribute(value string) string {
	if !strings.Contains(value, "&") {
		return value
	}
	replacer := strings.NewReplacer("&quot;", `"`, "&#39;", "'", "&lt;", "<", "&gt;", ">", "&#43;", "+", "&#x2B;", "+", "&#61;", "=", "&#x3D;", "=", "&amp;", "&")
	return replacer.Replace(value)
}

// StateDirName is the connector's state directory for one account and agent:
// "<account>-<agent person id>", under $XDG_STATE_HOME/basecamp/connect/.
func StateDirName(accountID string, agentID int64) string {
	return accountID + "-" + strconv.FormatInt(agentID, 10)
}

// LedgerFile is the ledger's file name inside the state directory.
const LedgerFile = "ledger.db"
