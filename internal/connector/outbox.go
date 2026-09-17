package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The outbox: every message the connector itself posts to Basecamp — the
// guard acknowledgement, the holding reply, still-running and the completion
// notice — goes through one table with one rule.
//
// # Invariants
//
// Each is held by the database where SQL can say it, and by a test that fails
// without it (outbox_invariants_test.go).
//
//  1. An intent is written in the transaction of the transition that calls
//     for it, through the ledger's hooks, so the two commit or roll back
//     together.
//  2. One intent per thing answered for: the key is the guard or holding
//     reply per event, the completion per attempt, still-running per attempt
//     and occurrence. A second write for a key writes nothing.
//  3. Nothing is sent without a durable sending row. The only path to a
//     request claims the intent — pending to sending, committed — first.
//  4. Nothing sending is sent again automatically. A request is made only for
//     an intent this process just claimed from pending. A sending intent is
//     reconciled by listing the destination, never by posting.
//  5. Reconciliation adopts only an unambiguous candidate: exactly one of the
//     agent's messages at the destination since the intent went sending
//     matches its body, the message is not a worker's own acknowledgement or
//     reply, no other intent owns it, and no other intent at the destination
//     whose own message may exist unreceipted — pending, sending,
//     indeterminate, or abandoned by a person who could not prove it absent —
//     has the same body. Anything else is indeterminate, for a person.
//  6. A receipt belongs to exactly one intent, and once written it never
//     changes. A unique index and a trigger.
//  7. States move along the lifecycle's edges only: pending → sending |
//     canceled; sending → sent | indeterminate, or canceled when Basecamp
//     answered the request by refusing it, which creates nothing; and
//     indeterminate → sent | abandoned | pending, those three only by a
//     person, as is refused → pending once a person has fixed the cause.
//  8. get_dispatch cancels the guard: a trigger moves the guard intent from
//     pending to canceled in get_dispatch's own transaction, and a guard that
//     already went out marks every task event it answers for as fired, so a
//     worker is told the connector acknowledged.
//  9. A guard is reported fired from the moment it is claimed, and that is
//     final: #736's task_events_guard_settles_once lets a guard move only
//     from armed. The claim marks its task events fired in the claim's own
//     transaction, so no worker asking while the request is in flight
//     acknowledges a second time. If Basecamp then refuses the request, the
//     intent is canceled — nothing was created — but its task events stay
//     fired, so that task's workers do not acknowledge either: the
//     acknowledgement is missing, never doubled, the spec's own preference.
//     A later task for the event (a person's redispatch) has its guard armed,
//     and the worker acknowledges itself: the intent is canceled, so nothing
//     remains to fire that guard, and get_dispatch cancels it. This is the spec's rule: a
//     missing acknowledgement costs less than a double one. A refusal here is
//     Basecamp refusing the connector's own lifecycle request, recorded on
//     the outbox row; a worker's permission refusals are another matter.
//  10. Reconciliation never holds up sending for long. A running connector
//     sends a batch, then lists at most one due destination; each listing is
//     bounded in time; each failure backs its intent off, doubling, and the
//     intent is indeterminate after MaxReconcileFailures, with that count
//     recorded. Start is the exception by design: it reconciles everything
//     due before it sends, within the bound its caller sets, and stops
//     sending at the first send that may not have landed.
const migrationOutbox = `
CREATE TABLE outbox (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  intent_key   TEXT    NOT NULL UNIQUE,
  kind         TEXT    NOT NULL CHECK (kind IN ('guard_ack', 'holding_reply', 'still_running', 'completion')),
  state        TEXT    NOT NULL DEFAULT 'pending'
               CHECK (state IN ('pending', 'sending', 'sent', 'indeterminate', 'canceled', 'abandoned')),
  event_id     INTEGER REFERENCES events (id),
  task_id      INTEGER REFERENCES tasks (id),
  attempt_id   TEXT    REFERENCES attempts (id),
  occurrence   INTEGER NOT NULL DEFAULT 0,
  bucket_id    INTEGER NOT NULL,
  message_kind TEXT    NOT NULL CHECK (message_kind IN ('boost', 'comment', 'chat_line')),
  recording_id INTEGER NOT NULL CHECK (recording_id > 0),
  body         TEXT    NOT NULL CHECK (body <> ''),
  created_at   TEXT    NOT NULL,
  not_before   TEXT    NOT NULL,
  sending_at   TEXT,
  finished_at  TEXT,
  receipt_id   INTEGER,
  note         TEXT    NOT NULL DEFAULT '',
  resolved_by  TEXT    NOT NULL DEFAULT '',
  -- A reconciliation listing that failed is tried again at reconcile_at,
  -- backing off; reconcile_failures counts the failures.
  reconcile_failures INTEGER NOT NULL DEFAULT 0,
  reconcile_at       TEXT,
  CHECK ((state = 'sent') = (receipt_id IS NOT NULL)),
  CHECK (state IN ('pending', 'canceled') OR sending_at IS NOT NULL)
);
CREATE UNIQUE INDEX outbox_receipt ON outbox (message_kind, receipt_id) WHERE receipt_id IS NOT NULL;
CREATE INDEX outbox_due ON outbox (state, not_before);
CREATE INDEX outbox_destination ON outbox (message_kind, recording_id, state);
CREATE INDEX outbox_event ON outbox (event_id, kind);

CREATE TRIGGER outbox_state_edges
BEFORE UPDATE OF state ON outbox
WHEN NEW.state <> OLD.state AND NOT (
     (OLD.state = 'pending'       AND NEW.state IN ('sending', 'canceled'))
  OR (OLD.state = 'sending'       AND NEW.state IN ('sent', 'indeterminate', 'canceled'))
  OR (OLD.state = 'indeterminate' AND NEW.state IN ('sent', 'abandoned', 'pending'))
  OR (OLD.state = 'canceled'      AND NEW.state = 'pending' AND OLD.note = 'the request was refused; no message was created')
)
BEGIN
  SELECT RAISE(ABORT, 'an outbox intent never moves along that edge');
END;

CREATE TRIGGER outbox_receipt_is_final
BEFORE UPDATE OF receipt_id ON outbox
WHEN OLD.receipt_id IS NOT NULL AND (NEW.receipt_id IS NULL OR NEW.receipt_id <> OLD.receipt_id)
BEGIN
  SELECT RAISE(ABORT, 'a receipt never changes');
END;

CREATE TRIGGER outbox_guard_canceled_by_get_dispatch
AFTER UPDATE OF guard ON task_events
WHEN OLD.guard = 'armed' AND NEW.guard = 'canceled'
BEGIN
  UPDATE outbox SET state = 'canceled', note = 'get_dispatch',
    finished_at = strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')
  WHERE intent_key = 'guard_ack:event:' || NEW.event_id AND state = 'pending';
END;

CREATE TRIGGER outbox_guard_fired_before_task
AFTER INSERT ON task_events
WHEN NEW.guard = 'armed' AND EXISTS (
  SELECT 1 FROM outbox
  WHERE intent_key = 'guard_ack:event:' || NEW.event_id AND state IN ('sending', 'sent', 'indeterminate', 'abandoned')
)
BEGIN
  UPDATE task_events SET guard = 'fired' WHERE task_id = NEW.task_id AND event_id = NEW.event_id;
END;
`

// IntentKind is what a lifecycle message answers for.
type IntentKind string

const (
	// IntentGuardAck is the fixed-form acknowledgement a guard posts when no
	// worker called get_dispatch in time. One per event.
	IntentGuardAck IntentKind = "guard_ack"
	// IntentHoldingReply answers a mention or assignment in a project with no
	// route. One per event.
	IntentHoldingReply IntentKind = "holding_reply"
	// IntentStillRunning is one still-running notice. One per attempt and
	// occurrence.
	IntentStillRunning IntentKind = "still_running"
	// IntentCompletion is an attempt's completion notice. One per attempt.
	IntentCompletion IntentKind = "completion"
)

// IntentState is where an intent is.
type IntentState string

const (
	// IntentPending is written and not yet asked for.
	IntentPending IntentState = "pending"
	// IntentSending was claimed for a request; the request may or may not
	// have reached Basecamp.
	IntentSending IntentState = "sending"
	// IntentSent has its receipt.
	IntentSent IntentState = "sent"
	// IntentIndeterminate could not be reconciled unambiguously. It is never
	// sent again automatically; a person decides.
	IntentIndeterminate IntentState = "indeterminate"
	// IntentCanceled was never sent: nothing called for it any more (a guard
	// get_dispatch canceled), or Basecamp refused the request, which creates
	// nothing.
	IntentCanceled IntentState = "canceled"
	// IntentAbandoned is an indeterminate intent a person decided not to
	// send.
	IntentAbandoned IntentState = "abandoned"
)

// MessageKind is the kind of Basecamp message an intent posts.
type MessageKind string

const (
	// MessageBoost is a boost on Destination.RecordingID.
	MessageBoost MessageKind = "boost"
	// MessageComment is a comment on Destination.RecordingID.
	MessageComment MessageKind = "comment"
	// MessageChatLine is a line in the Campfire Destination.RecordingID.
	MessageChatLine MessageKind = "chat_line"
)

// Destination is where a lifecycle message goes.
type Destination struct {
	BucketID    int64
	Kind        MessageKind
	RecordingID int64
}

// Intent is one lifecycle message.
type Intent struct {
	ID    int64
	Key   string
	Kind  IntentKind
	State IntentState
	// EventID is the event a guard or holding reply answers for; zero for a
	// per-attempt intent.
	EventID int64
	// TaskID and AttemptID are set on per-attempt intents.
	TaskID     int64
	AttemptID  string
	Occurrence int

	Destination Destination
	// Body is the message exactly as it is posted, rendered from records when
	// the intent was written.
	Body string

	CreatedAt  time.Time
	NotBefore  time.Time
	SendingAt  *time.Time
	FinishedAt *time.Time
	ReceiptID  *int64
	// Note says why an intent is canceled or indeterminate.
	Note string
	// ResolvedBy names the person who resolved an indeterminate intent.
	ResolvedBy string
	// ReconcileFailures counts listings that failed for a sending intent;
	// ReconcileAt is when the next is due, nil when none failed.
	ReconcileFailures int
	ReconcileAt       *time.Time
}

// Intent keys.
func guardKey(eventID int64) string {
	return string(IntentGuardAck) + ":event:" + strconv.FormatInt(eventID, 10)
}

func holdingKey(eventID int64) string {
	return string(IntentHoldingReply) + ":event:" + strconv.FormatInt(eventID, 10)
}

func completionKey(attemptID string) string {
	return string(IntentCompletion) + ":attempt:" + attemptID
}

func stillRunningKey(attemptID string, occurrence int) string {
	return string(IntentStillRunning) + ":attempt:" + attemptID + ":" + strconv.Itoa(occurrence)
}

// Errors from the outbox.
var (
	// ErrNoSuchIntent is an intent id the ledger does not hold.
	ErrNoSuchIntent = errors.New("no such outbox intent")
	// ErrNotIndeterminate is a person's resolution for an intent that is not
	// indeterminate.
	ErrNotIndeterminate = errors.New("the intent is not indeterminate")
	// ErrReceiptOwned is a receipt another intent already owns.
	ErrReceiptOwned = errors.New("the receipt belongs to another intent")
)

// newIntent is an intent a hook writes.
type newIntent struct {
	key         string
	kind        IntentKind
	eventID     int64
	taskID      int64
	attemptID   string
	occurrence  int
	destination Destination
	body        string
	notBefore   time.Time
}

// writeIntent inserts an intent in tx unless its key already exists.
func writeIntent(ctx context.Context, tx Tx, now time.Time, in newIntent) error {
	if in.destination.RecordingID <= 0 || in.body == "" {
		return nil
	}
	if in.notBefore.IsZero() {
		in.notBefore = now
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO outbox (intent_key, kind, event_id, task_id, attempt_id, occurrence, bucket_id, message_kind, recording_id, body, created_at, not_before)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (intent_key) DO NOTHING`,
		in.key, string(in.kind), nullableID64(in.eventID), nullableID64(in.taskID), nullableString(in.attemptID), in.occurrence,
		in.destination.BucketID, string(in.destination.Kind), in.destination.RecordingID, in.body, stamp(now), stamp(in.notBefore))
	if err != nil {
		return fmt.Errorf("connector: write outbox intent %s: %w", in.key, err)
	}
	return nil
}

func nullableID64(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const selectIntents = `
SELECT id, intent_key, kind, state, COALESCE(event_id, 0), COALESCE(task_id, 0), COALESCE(attempt_id, ''), occurrence,
       bucket_id, message_kind, recording_id, body, created_at, not_before, sending_at, finished_at, receipt_id, note, resolved_by,
       reconcile_failures, reconcile_at
FROM outbox`

func scanIntents(rows *sql.Rows) ([]Intent, error) {
	defer func() { _ = rows.Close() }()
	var out []Intent
	for rows.Next() {
		var (
			in                       Intent
			kind, state, messageKind string
			created, notBefore       string
			sendingAt, finishedAt    sql.NullString
			reconcileAt              sql.NullString
			receipt                  sql.NullInt64
		)
		if err := rows.Scan(&in.ID, &in.Key, &kind, &state, &in.EventID, &in.TaskID, &in.AttemptID, &in.Occurrence,
			&in.Destination.BucketID, &messageKind, &in.Destination.RecordingID, &in.Body, &created, &notBefore,
			&sendingAt, &finishedAt, &receipt, &in.Note, &in.ResolvedBy, &in.ReconcileFailures, &reconcileAt); err != nil {
			return nil, fmt.Errorf("connector: read outbox: %w", err)
		}
		in.Kind, in.State, in.Destination.Kind = IntentKind(kind), IntentState(state), MessageKind(messageKind)
		var err error
		if in.CreatedAt, err = parseStamp(created); err != nil {
			return nil, err
		}
		if in.NotBefore, err = parseStamp(notBefore); err != nil {
			return nil, err
		}
		if in.SendingAt, err = parseNullStamp(sendingAt); err != nil {
			return nil, err
		}
		if in.FinishedAt, err = parseNullStamp(finishedAt); err != nil {
			return nil, err
		}
		if in.ReconcileAt, err = parseNullStamp(reconcileAt); err != nil {
			return nil, err
		}
		if receipt.Valid {
			id := receipt.Int64
			in.ReceiptID = &id
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func parseNullStamp(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := parseStamp(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// IntentFilter selects intents. Zero values select everything.
type IntentFilter struct {
	States  []IntentState
	Kinds   []IntentKind
	EventID int64
	// Limit is the most returned, newest first; zero for all.
	Limit int
}

// Intents lists outbox intents, newest first. It only reads.
func (l *Ledger) Intents(ctx context.Context, f IntentFilter) ([]Intent, error) {
	var out []Intent
	err := retryBusy(func() error {
		var err error
		out, err = l.intents(ctx, f)
		return err
	})
	return out, err
}

func (l *Ledger) intents(ctx context.Context, f IntentFilter) ([]Intent, error) {
	var (
		where []string
		args  []any
	)
	if len(f.States) > 0 {
		where = append(where, "state IN ("+placeholders(len(f.States))+")")
		for _, s := range f.States {
			args = append(args, string(s))
		}
	}
	if len(f.Kinds) > 0 {
		where = append(where, "kind IN ("+placeholders(len(f.Kinds))+")")
		for _, k := range f.Kinds {
			args = append(args, string(k))
		}
	}
	if f.EventID != 0 {
		where = append(where, "event_id = ?")
		args = append(args, f.EventID)
	}
	query := selectIntents
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY id DESC"
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("connector: list outbox: %w", err)
	}
	return scanIntents(rows)
}

// Intent reads one intent by id.
func (l *Ledger) Intent(ctx context.Context, id int64) (Intent, error) {
	var intents []Intent
	err := retryBusy(func() error {
		rows, err := l.db.QueryContext(ctx, selectIntents+` WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("connector: read outbox intent %d: %w", id, err)
		}
		intents, err = scanIntents(rows)
		return err
	})
	if err != nil {
		return Intent{}, err
	}
	if len(intents) == 0 {
		return Intent{}, fmt.Errorf("connector: outbox intent %d: %w", id, ErrNoSuchIntent)
	}
	return intents[0], nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// IsLifecycleReceipt reports whether a message id is the receipt of one of the
// connector's own lifecycle messages of that kind.
func (l *Ledger) IsLifecycleReceipt(ctx context.Context, kind MessageKind, id int64) (bool, error) {
	var found bool
	err := l.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM outbox WHERE message_kind = ? AND receipt_id = ?)`, string(kind), id).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("connector: lifecycle receipt %d: %w", id, err)
	}
	return found, nil
}

// Resolution is a person's decision on an indeterminate intent.
type Resolution string

const (
	// ResolveSent says the message is in Basecamp: ReceiptID names it.
	ResolveSent Resolution = "sent"
	// ResolveAbandon says it is not to be sent.
	ResolveAbandon Resolution = "abandon"
	// ResolveResend authorizes sending it again: the intent returns to
	// pending. Only a person may choose this; nothing automatic does.
	ResolveResend Resolution = "resend"
)

// IntentResolution is a person's decision and who made it.
type IntentResolution struct {
	Resolution Resolution
	// ReceiptID is the message a ResolveSent names.
	ReceiptID int64
	// By names who decided, for the record. Required.
	By string
}

// ResolveIntent applies a person's decision to an indeterminate intent.
func (l *Ledger) ResolveIntent(ctx context.Context, id int64, r IntentResolution) error {
	if strings.TrimSpace(r.By) == "" {
		return errors.New("connector: a resolution records who decided")
	}
	now := l.timestamp()
	var (
		query string
		args  []any
	)
	switch r.Resolution {
	case ResolveSent:
		if r.ReceiptID <= 0 {
			return errors.New("connector: a sent resolution names the message")
		}
		query = `UPDATE outbox SET state = 'sent', receipt_id = ?, finished_at = ?, resolved_by = ?, note = ? WHERE id = ? AND state = 'indeterminate'`
		args = []any{r.ReceiptID, now}
	case ResolveAbandon:
		query = `UPDATE outbox SET state = 'abandoned', finished_at = ?, resolved_by = ?, note = ? WHERE id = ? AND state = 'indeterminate'`
		args = []any{now}
	case ResolveResend:
		// A refused request created nothing, so a person may send it again
		// once the cause is fixed, as they may an indeterminate one.
		query = `UPDATE outbox SET state = 'pending', sending_at = NULL, finished_at = NULL, reconcile_failures = 0, reconcile_at = NULL, not_before = ?, resolved_by = ?, note = ?
WHERE id = ? AND (state = 'indeterminate' OR (state = 'canceled' AND note = '` + RefusedNote + `'))`
		args = []any{now}
	default:
		return fmt.Errorf("connector: %q is not a resolution", r.Resolution)
	}
	return retryBusy(func() error {
		res, err := l.db.ExecContext(ctx, query, append(args, r.By, "resolved: "+string(r.Resolution), id)...)
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("connector: resolve intent %d: %w", id, ErrReceiptOwned)
			}
			return fmt.Errorf("connector: resolve intent %d: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			if _, err := l.Intent(ctx, id); err != nil {
				return err
			}
			return fmt.Errorf("connector: resolve intent %d: %w", id, ErrNotIndeterminate)
		}
		return nil
	})
}

// RefusedNote is the note on an intent Basecamp refused.
const RefusedNote = "the request was refused; no message was created"

// refuse settles a sending intent Basecamp refused. The request created
// nothing, so the intent is canceled rather than left uncertain, and no later
// task event is written fired for it. Task events the claim already marked
// fired stay fired (invariant 9).
func (l *Ledger) refuse(ctx context.Context, in Intent, note string) (Intent, error) {
	err := retryBusy(func() error {
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("connector: begin refusal of %d: %w", in.ID, err)
		}
		defer func() { _ = tx.Rollback() }()
		res, err := tx.ExecContext(ctx, `UPDATE outbox SET state = 'canceled', finished_at = ?, note = ? WHERE id = ? AND state = 'sending'`,
			l.timestamp(), note, in.ID)
		if err != nil {
			return fmt.Errorf("connector: refuse intent %d: %w", in.ID, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("connector: refuse intent %d: it is not sending", in.ID)
		}
		return tx.Commit()
	})
	if err != nil {
		return Intent{}, err
	}
	return l.Intent(ctx, in.ID)
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
