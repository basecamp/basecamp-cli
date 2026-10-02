package connector

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// The outbox: every message an older build's connector posted to Basecamp
// itself (the guard acknowledgement, the holding reply, still-running, the
// completion notice, and the retraction that answered an ask in one of them)
// went through this one table.
//
// Since #815 removed the worker side, this build writes no intent and sends
// none: the table is written only by its migrations and by the triggers below.
// What an older build left in it is still read and still decided about.
// Status lists the intents waiting for a person (statusIntents), and a
// discard or an import's done decision cancels the guard acknowledgement and
// holding reply still pending for the record it closes.
//
// What the schema held those writes to, and still holds any write to:
//
//  1. One intent per thing answered for: intent_key is unique.
//  2. A receipt belongs to exactly one intent, and once written it never
//     changes (outbox_receipt, outbox_receipt_is_final).
//  3. States move along the lifecycle's edges only (outbox_state_edges):
//     pending to sending or canceled; sending to sent, indeterminate, or
//     canceled when Basecamp refused the request; indeterminate to sent,
//     abandoned or pending, those three only by a person.
//  4. get_dispatch canceled the guard: a trigger moved the guard intent from
//     pending to canceled in get_dispatch's own transaction, and a guard that
//     had already gone out marked every task event it answered for as fired
//     (outbox_guard_canceled_by_get_dispatch, outbox_guard_fired_before_task).
//  5. A held record's pending guard is canceled, and nothing moves to sending
//     while the hold marker stands (events_held_cancels_guard,
//     outbox_refused_under_hold).
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

// migrationRetraction adds the retraction: a fifth kind, and the message it
// answers. Both live in constraints migrationOutbox already shipped — a kind
// this build did not know was refused by a CHECK — so the table is rebuilt
// rather than altered, which SQLite has no statement for.
//
// The order matters. Three triggers on other tables name outbox in their
// bodies — two on task_events, one on events — and SQLite re-parses every
// trigger in the schema when a table is renamed: with the old table dropped
// and the new one not yet named outbox, that parse fails. They are dropped
// first and written again at the end, unchanged. The table's own triggers and
// indexes go with the DROP and are written again too, also unchanged — the
// hold's refusal among them, so nothing is posted under a hold after this
// runs any more than before it.
//
// Ids are the outbox's own: they are copied, the AUTOINCREMENT sequence
// follows the highest copied, and no id is ever handed out twice.
const migrationRetraction = `
DROP TRIGGER outbox_guard_canceled_by_get_dispatch;
DROP TRIGGER outbox_guard_fired_before_task;
DROP TRIGGER events_held_cancels_guard;

CREATE TABLE outbox_rebuilt (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  intent_key   TEXT    NOT NULL UNIQUE,
  kind         TEXT    NOT NULL CHECK (kind IN ('guard_ack', 'holding_reply', 'still_running', 'completion', 'retraction')),
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
  reconcile_failures INTEGER NOT NULL DEFAULT 0,
  reconcile_at       TEXT,
  -- retracts is the intent whose posted message this one answers: set on a
  -- retraction, on nothing else.
  retracts     INTEGER REFERENCES outbox (id),
  CHECK ((kind = 'retraction') = (retracts IS NOT NULL)),
  CHECK ((state = 'sent') = (receipt_id IS NOT NULL)),
  CHECK (state IN ('pending', 'canceled') OR sending_at IS NOT NULL)
);

INSERT INTO outbox_rebuilt (id, intent_key, kind, state, event_id, task_id, attempt_id, occurrence, bucket_id,
  message_kind, recording_id, body, created_at, not_before, sending_at, finished_at, receipt_id, note, resolved_by,
  reconcile_failures, reconcile_at)
SELECT id, intent_key, kind, state, event_id, task_id, attempt_id, occurrence, bucket_id,
  message_kind, recording_id, body, created_at, not_before, sending_at, finished_at, receipt_id, note, resolved_by,
  reconcile_failures, reconcile_at
FROM outbox;

DROP TABLE outbox;
ALTER TABLE outbox_rebuilt RENAME TO outbox;

CREATE UNIQUE INDEX outbox_receipt ON outbox (message_kind, receipt_id) WHERE receipt_id IS NOT NULL;
CREATE INDEX outbox_due ON outbox (state, not_before);
CREATE INDEX outbox_destination ON outbox (message_kind, recording_id, state);
CREATE INDEX outbox_event ON outbox (event_id, kind);
CREATE INDEX outbox_retracts ON outbox (retracts) WHERE retracts IS NOT NULL;

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

CREATE TRIGGER events_held_cancels_guard
AFTER UPDATE OF state ON events
WHEN NEW.state = 'held' AND OLD.state <> 'held'
BEGIN
  UPDATE outbox SET state = 'canceled', note = 'held',
    finished_at = strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')
  WHERE intent_key = 'guard_ack:event:' || NEW.id AND state = 'pending';
END;

CREATE TRIGGER outbox_refused_under_hold
BEFORE UPDATE OF state ON outbox
WHEN NEW.state = 'sending' AND OLD.state <> 'sending' AND EXISTS (SELECT 1 FROM hold_marker)
BEGIN
  SELECT RAISE(ABORT, 'the connector is held: nothing is posted until basecamp connect release');
END;
`

// IntentKind is what a lifecycle message answers for.
type IntentKind string

const (
	// IntentGuardAck is the fixed-form acknowledgement a guard posts when no
	// worker called get_dispatch in time. One per event.
	IntentGuardAck IntentKind = "guard_ack"
	// IntentHoldingReply answers a request the connector is not going to
	// start work on until a person changes something: a mention or
	// assignment in a project the connector does not serve. One per event.
	IntentHoldingReply IntentKind = "holding_reply"
	// IntentStillRunning is one still-running notice. One per attempt and
	// occurrence.
	IntentStillRunning IntentKind = "still_running"
	// IntentCompletion is an attempt's completion notice. One per attempt.
	IntentCompletion IntentKind = "completion"
	// IntentRetraction answers an ask a posted notice made: it says the thing
	// the notice asked a person to do has been done, or been decided against.
	// One per posted message and the event whose ask it answers.
	IntentRetraction IntentKind = "retraction"
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
	// Retracts is the intent whose posted message a retraction answers; zero
	// on every other kind.
	Retracts int64

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

func nullableID64(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

const selectIntents = `
SELECT id, intent_key, kind, state, COALESCE(event_id, 0), COALESCE(task_id, 0), COALESCE(attempt_id, ''), occurrence,
       COALESCE(retracts, 0), bucket_id, message_kind, recording_id, body, created_at, not_before, sending_at, finished_at,
       receipt_id, note, resolved_by, reconcile_failures, reconcile_at
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
		if err := rows.Scan(&in.ID, &in.Key, &kind, &state, &in.EventID, &in.TaskID, &in.AttemptID, &in.Occurrence, &in.Retracts,
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

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
