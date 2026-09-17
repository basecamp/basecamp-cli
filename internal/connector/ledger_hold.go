package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// The hold marker, intake generations, the review tag, and people's decisions
// on records: the ledger half of `basecamp connect --hold`, `release`,
// `redispatch`, `discard`, `shadow promote` and `import`.
//
// # Invariants
//
// Each is held by the database where SQL can say it, and by a test that fails
// without it (operator_invariants_test.go).
//
//  1. Nothing a hold tagged for review reaches a worker without a person. A
//     tagged, unauthorized record written admitted or queued — by admission,
//     by a task's end returning it, by anything — is written held instead, by
//     a trigger, in the same statement. A held record is not startable.
//  2. The hold marker stops dispatch and posting at the database. While it
//     stands no attempt row can be written and no outbox intent can move to
//     sending. It lives in the ledger, so every start respects it, and only
//     Release clears it.
//  3. A hold is one transaction: the marker, a new intake generation, the
//     review tag on every non-terminal record of the generations before it
//     (clearing any earlier authorization), and admitted or queued records
//     moved to held.
//  4. A person's decision is one transaction with the state change it makes,
//     and it records who decided. A terminal record leaves its state only
//     through such a decision: completed to admitted when the write also
//     clears a recorded redispatch, completed(unknown) to discarded(by_operator).
//     Discarded never leaves. A trigger refuses every other edge.
//  5. A redispatch never runs two workers for one event. The replaced task's
//     token is superseded in the authorization's transaction, and an event
//     whose task is still live is not admitted until that task ends: the
//     authorization waits on the record and a trigger applies it in the
//     transaction that ends the task. One live task per conversation keeps
//     the new task from starting before then.
//  6. Admitted means dispatchable. A redispatch admits only a record that
//     still has its snapshot and route; anything else is decided again by
//     admission, whose verdict stands.
//  7. Shadow promote and import are atomic under a crash: each is one ledger
//     transaction, and promote exposes the shadow ledger at the normal path
//     only after its hold committed, by one rename (promote.go).
//  8. Reading is not deciding. Status opens the ledger read-only, takes no
//     lock, and says nothing of content, feed positions or tokens.
const migrationOperator = `
CREATE TABLE generations (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  cause     TEXT NOT NULL CHECK (cause IN ('hold', 'shadow_promote')),
  opened_by TEXT NOT NULL CHECK (opened_by <> ''),
  opened_at TEXT NOT NULL
);

CREATE TABLE hold_marker (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  generation INTEGER NOT NULL REFERENCES generations (id),
  cause      TEXT    NOT NULL CHECK (cause IN ('hold', 'shadow_promote')),
  held_by    TEXT    NOT NULL CHECK (held_by <> ''),
  held_at    TEXT    NOT NULL
);

CREATE TABLE decisions (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  action             TEXT NOT NULL CHECK (action IN ('hold', 'release', 'redispatch', 'discard', 'shadow_promote', 'import')),
  event_id           INTEGER,
  decided_by         TEXT NOT NULL CHECK (decided_by <> ''),
  decided_at         TEXT NOT NULL,
  from_state         TEXT NOT NULL DEFAULT '',
  from_reason        TEXT NOT NULL DEFAULT '',
  from_outcome       TEXT NOT NULL DEFAULT '',
  to_state           TEXT NOT NULL DEFAULT '',
  superseded_task_id INTEGER,
  note               TEXT NOT NULL DEFAULT ''
);
CREATE INDEX decisions_event ON decisions (event_id, id);

CREATE TABLE connection (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  state      TEXT    NOT NULL,
  pid        INTEGER NOT NULL,
  changed_at TEXT    NOT NULL,
  detail     TEXT    NOT NULL DEFAULT ''
);

ALTER TABLE events ADD COLUMN generation         INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN review             INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN authorized_at      TEXT;
ALTER TABLE events ADD COLUMN authorized_by      TEXT    NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN redispatch_pending INTEGER NOT NULL DEFAULT 0;
CREATE INDEX events_review ON events (review, state);

CREATE TRIGGER events_generation
AFTER INSERT ON events
BEGIN
  UPDATE events SET generation = (SELECT COALESCE(MAX(id), 0) FROM generations) WHERE id = NEW.id;
END;

CREATE TRIGGER events_review_is_held
AFTER UPDATE OF state, review, authorized_at ON events
WHEN NEW.state IN ('admitted', 'queued') AND NEW.review = 1 AND NEW.authorized_at IS NULL
BEGIN
  UPDATE events SET state = 'held', reason = '', revision = revision + 1 WHERE id = NEW.id;
END;

CREATE TRIGGER events_held_cancels_guard
AFTER UPDATE OF state ON events
WHEN NEW.state = 'held' AND OLD.state <> 'held'
BEGIN
  UPDATE outbox SET state = 'canceled', note = 'held'
  WHERE intent_key = 'guard_ack:event:' || NEW.id AND state = 'pending';
END;

CREATE TRIGGER attempts_refused_under_hold
BEFORE INSERT ON attempts
WHEN EXISTS (SELECT 1 FROM hold_marker)
BEGIN
  SELECT RAISE(ABORT, 'the connector is held: nothing is dispatched until basecamp connect release');
END;

CREATE TRIGGER outbox_refused_under_hold
BEFORE UPDATE OF state ON outbox
WHEN NEW.state = 'sending' AND OLD.state <> 'sending' AND EXISTS (SELECT 1 FROM hold_marker)
BEGIN
  SELECT RAISE(ABORT, 'the connector is held: nothing is posted until basecamp connect release');
END;

DROP TRIGGER events_terminal_is_terminal;
CREATE TRIGGER events_terminal_is_terminal
BEFORE UPDATE OF state ON events
WHEN OLD.state IN ('completed', 'discarded') AND NEW.state <> OLD.state
  AND NOT (
    OLD.state = 'completed' AND NEW.state = 'admitted'
    AND OLD.redispatch_pending = 1 AND NEW.redispatch_pending = 0
    AND OLD.content_dropped = 0 AND OLD.snapshot IS NOT NULL
    AND (SELECT te.outcome FROM task_events te WHERE te.event_id = OLD.id AND te.withdrawn_at IS NULL
         ORDER BY te.task_id DESC LIMIT 1) IN ('unknown', 'failed')
  )
  AND NOT (
    OLD.state = 'completed' AND NEW.state = 'discarded' AND NEW.reason = 'by_operator'
    AND (SELECT te.outcome FROM task_events te WHERE te.event_id = OLD.id AND te.withdrawn_at IS NULL
         ORDER BY te.task_id DESC LIMIT 1) = 'unknown'
  )
BEGIN
  SELECT RAISE(ABORT, 'a terminal record cannot change state');
END;

CREATE TRIGGER tasks_end_applies_redispatch
AFTER UPDATE OF ended_at ON tasks
WHEN OLD.ended_at IS NULL AND NEW.ended_at IS NOT NULL
BEGIN
  UPDATE events
  SET state = 'admitted', reason = '', redispatch_pending = 0, revision = revision + 1,
      updated_at = NEW.ended_at, blocked_at = NULL, retry_at = NULL
  WHERE state = 'completed' AND redispatch_pending = 1
    AND id IN (SELECT event_id FROM task_events WHERE task_id = NEW.id);
END;
`

// Reasons a person's decision writes.
const (
	// ReasonByOperator is a record a person closed without running it.
	ReasonByOperator = "by_operator"
	// ReasonImportedDone is a tombstone an import wrote for an entry a person
	// confirmed was finished before the cutover.
	ReasonImportedDone = "imported_done"
)

// operatorEdges are the moves only a person's decision makes, by target: the
// states a record may leave for it. The lifecycle's own edges (ledger_events.go)
// are what the connector does by itself; these are never taken automatically.
var operatorEdges = map[RecordState][]RecordState{
	// A redispatch admits a completed record, or a held one with its snapshot.
	StateAdmitted: {StateCompleted, StateHeld},
	// A hold holds what was waiting for a worker.
	StateHeld: {StateAdmitted, StateQueued},
	// A redispatch of a record held over a blocking reason runs it again as
	// blocked.
	StateBlocked: {StateHeld},
	// A discard closes a held record or an unknown outcome.
	StateDiscarded: {StateHeld, StateCompleted},
}

func operatorEdgesInto(target RecordState) []string {
	out := make([]string, 0, len(operatorEdges[target]))
	for _, from := range operatorEdges[target] {
		out = append(out, string(from))
	}
	return out
}

// HoldCause is what set a hold.
type HoldCause string

const (
	// HoldByOperator is `basecamp connect --hold`.
	HoldByOperator HoldCause = "hold"
	// HoldByPromote is `basecamp connect shadow promote`.
	HoldByPromote HoldCause = "shadow_promote"
)

// Hold is the standing hold marker.
type Hold struct {
	// Generation is the intake generation the latest hold opened. Records of
	// earlier generations were tagged for review.
	Generation int64
	Cause      HoldCause
	HeldBy     string
	HeldAt     time.Time
}

// HoldResult is what setting a hold did.
type HoldResult struct {
	Hold Hold
	// Tagged is how many non-terminal records were tagged for review.
	Tagged int
	// Held is how many of them were waiting for a worker and are now held.
	Held int
}

// SetHold sets the durable hold marker, opens a new intake generation, and
// tags every non-terminal record of the generations before it for review, in
// one transaction (invariant 3). A hold already standing keeps its first
// setter and time; the new generation and tags are written again.
func (l *Ledger) SetHold(ctx context.Context, by string, cause HoldCause) (HoldResult, error) {
	if strings.TrimSpace(by) == "" {
		return HoldResult{}, errors.New("connector: a hold records who set it")
	}
	if cause != HoldByOperator && cause != HoldByPromote {
		return HoldResult{}, fmt.Errorf("connector: %q is not a hold cause", cause)
	}
	var out HoldResult
	err := retryBusy(func() error {
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("connector: begin hold: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		out, err = l.hold(ctx, tx, by, cause)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("connector: commit hold: %w", err)
		}
		return nil
	})
	return out, err
}

// holdStep is a test seam: a crash test kills the process at a named step.
var holdStep = func(string) {}

func (l *Ledger) hold(ctx context.Context, tx *sql.Tx, by string, cause HoldCause) (HoldResult, error) {
	now := l.timestamp()
	res, err := tx.ExecContext(ctx, `INSERT INTO generations (cause, opened_by, opened_at) VALUES (?, ?, ?)`, string(cause), by, now)
	if err != nil {
		return HoldResult{}, fmt.Errorf("connector: open a generation: %w", err)
	}
	generation, err := res.LastInsertId()
	if err != nil {
		return HoldResult{}, fmt.Errorf("connector: open a generation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO hold_marker (id, generation, cause, held_by, held_at) VALUES (1, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET generation = excluded.generation`, generation, string(cause), by, now); err != nil {
		return HoldResult{}, fmt.Errorf("connector: set the hold marker: %w", err)
	}
	holdStep("marker")

	var waiting int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE state IN ('admitted', 'queued')`).Scan(&waiting); err != nil {
		return HoldResult{}, fmt.Errorf("connector: count waiting records: %w", err)
	}
	// Tagging a waiting record holds it: events_review_is_held fires on the
	// review column in this same statement (invariant 1).
	tagged, err := tx.ExecContext(ctx, `
UPDATE events SET review = 1, authorized_at = NULL, authorized_by = ''
WHERE state NOT IN ('completed', 'discarded') AND generation < ?`, generation)
	if err != nil {
		return HoldResult{}, fmt.Errorf("connector: tag records for review: %w", err)
	}
	n, err := tagged.RowsAffected()
	if err != nil {
		return HoldResult{}, err
	}
	holdStep("tagged")
	var stillWaiting int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE state IN ('admitted', 'queued') AND review = 1`).Scan(&stillWaiting); err != nil {
		return HoldResult{}, fmt.Errorf("connector: count held records: %w", err)
	}
	if stillWaiting != 0 {
		return HoldResult{}, fmt.Errorf("connector: %d tagged records are still waiting for a worker", stillWaiting)
	}
	if err := recordDecision(ctx, tx, decision{action: string(cause), by: by, at: now,
		note: fmt.Sprintf("generation %d; %d tagged for review", generation, n)}); err != nil {
		return HoldResult{}, err
	}
	hold, ok, err := readHold(ctx, tx)
	if err != nil {
		return HoldResult{}, err
	}
	if !ok {
		return HoldResult{}, errors.New("connector: the hold marker did not stand")
	}
	return HoldResult{Hold: hold, Tagged: int(n), Held: waiting}, nil
}

// ReleaseResult is what a release did.
type ReleaseResult struct {
	// Released is false when no hold stood.
	Released bool
	Hold     Hold
	// StillHeld counts held records, which stay held.
	StillHeld int
}

// Release clears the hold marker. Held records stay held; records a person
// authorized and records of the newest generation dispatch.
func (l *Ledger) Release(ctx context.Context, by string) (ReleaseResult, error) {
	if strings.TrimSpace(by) == "" {
		return ReleaseResult{}, errors.New("connector: a release records who released")
	}
	var out ReleaseResult
	err := retryBusy(func() error {
		out = ReleaseResult{}
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("connector: begin release: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		hold, ok, err := readHold(ctx, tx)
		if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE state = 'held'`).Scan(&out.StillHeld); err != nil {
			return fmt.Errorf("connector: count held records: %w", err)
		}
		if !ok {
			return nil
		}
		now := l.timestamp()
		if _, err := tx.ExecContext(ctx, `DELETE FROM hold_marker WHERE id = 1`); err != nil {
			return fmt.Errorf("connector: clear the hold marker: %w", err)
		}
		if err := recordDecision(ctx, tx, decision{action: "release", by: by, at: now,
			note: fmt.Sprintf("hold of generation %d set by %s", hold.Generation, hold.HeldBy)}); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("connector: commit release: %w", err)
		}
		out.Released, out.Hold = true, hold
		return nil
	})
	return out, err
}

// Held reports whether the hold marker stands. Its signature is
// OutboxOptions.Paused's.
func (l *Ledger) Held(ctx context.Context) (bool, error) {
	_, ok, err := l.HoldMarker(ctx)
	return ok, err
}

// HoldMarker reads the standing hold, if any.
func (l *Ledger) HoldMarker(ctx context.Context) (Hold, bool, error) {
	return readHold(ctx, l.db)
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readHold(ctx context.Context, q rowQuerier) (Hold, bool, error) {
	var (
		h            Hold
		cause, stamp string
	)
	err := q.QueryRowContext(ctx, `SELECT generation, cause, held_by, held_at FROM hold_marker WHERE id = 1`).Scan(&h.Generation, &cause, &h.HeldBy, &stamp)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Hold{}, false, nil
	case err != nil:
		return Hold{}, false, fmt.Errorf("connector: read the hold marker: %w", err)
	}
	h.Cause = HoldCause(cause)
	if h.HeldAt, err = parseStamp(stamp); err != nil {
		return Hold{}, false, err
	}
	return h, true, nil
}

// decision is one row of the decisions table.
type decision struct {
	action         string
	eventID        int64
	by, at         string
	fromState      RecordState
	fromReason     string
	fromOutcome    Outcome
	toState        RecordState
	supersededTask int64
	note           string
}

func recordDecision(ctx context.Context, tx Tx, d decision) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO decisions (action, event_id, decided_by, decided_at, from_state, from_reason, from_outcome, to_state, superseded_task_id, note)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.action, nullableID64(d.eventID), d.by, d.at, string(d.fromState), d.fromReason, string(d.fromOutcome),
		string(d.toState), nullableID64(d.supersededTask), d.note)
	if err != nil {
		return fmt.Errorf("connector: record the decision: %w", err)
	}
	return nil
}

// Connection states the run command reports for status.
const (
	ConnectionStarting  = "starting"
	ConnectionConnected = "connected"
	ConnectionReconnect = "reconnecting"
	ConnectionPaused    = "paused"
	ConnectionStopped   = "stopped"
)

// NoteConnection records the running connector's connection state, for
// status. detail is a short diagnostic phrase and never carries a position,
// a ticket or content.
func (l *Ledger) NoteConnection(ctx context.Context, state, detail string) error {
	return retryBusy(func() error {
		_, err := l.db.ExecContext(ctx, `
INSERT INTO connection (id, state, pid, changed_at, detail) VALUES (1, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET state = excluded.state, pid = excluded.pid, changed_at = excluded.changed_at, detail = excluded.detail`,
			state, os.Getpid(), l.timestamp(), detail)
		if err != nil {
			return fmt.Errorf("connector: note connection state: %w", err)
		}
		return nil
	})
}
