package connector

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ReconciliationVersion is the reconciliation file format import reads.
const ReconciliationVersion = 1

// Reconciliation decisions.
const (
	// DecisionDone is an entry a person confirmed the old connector's work
	// finished: the record becomes a tombstone.
	DecisionDone = "done"
	// DecisionHeld is every other mapped entry: the record is tagged for
	// review and waits for a person.
	DecisionHeld = "held"
)

// Reconciliation is the cutover's reconciliation file: the old connector's
// handled entries, each mapped to a feed event and decided.
type Reconciliation struct {
	Version int                   `json:"version"`
	Entries []ReconciliationEntry `json:"entries"`
}

// ReconciliationEntry is one mapped entry.
type ReconciliationEntry struct {
	EventID  int64  `json:"event_id"`
	Decision string `json:"decision"`
}

// ParseReconciliation reads a reconciliation file strictly: unknown fields,
// a second entry for one event, and a decision other than done or held are
// refused, so a file that means something else is never half-understood.
func ParseReconciliation(data []byte) (Reconciliation, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r Reconciliation
	if err := dec.Decode(&r); err != nil {
		return Reconciliation{}, fmt.Errorf("connector: reconciliation file: %w", err)
	}
	if dec.More() {
		return Reconciliation{}, errors.New("connector: reconciliation file: more than one JSON value")
	}
	if r.Version != ReconciliationVersion {
		return Reconciliation{}, fmt.Errorf("connector: reconciliation file version %d; this build reads %d", r.Version, ReconciliationVersion)
	}
	seen := make(map[int64]bool, len(r.Entries))
	for i, e := range r.Entries {
		switch {
		case e.EventID <= 0:
			return Reconciliation{}, fmt.Errorf("connector: reconciliation entry %d names no event id", i)
		case e.Decision != DecisionDone && e.Decision != DecisionHeld:
			return Reconciliation{}, fmt.Errorf("connector: reconciliation entry for event %d has decision %q; use done or held", e.EventID, e.Decision)
		case seen[e.EventID]:
			return Reconciliation{}, fmt.Errorf("connector: reconciliation file names event %d twice", e.EventID)
		}
		seen[e.EventID] = true
	}
	return r, nil
}

// ImportResult is what an import did.
type ImportResult struct {
	// Tombstoned counts records closed as discarded(imported_done), Inserted
	// the tombstones written for events the ledger had never seen.
	Tombstoned int
	Inserted   int
	// AlreadyTerminal counts done entries whose record had already finished.
	AlreadyTerminal int
	// Tagged counts non-terminal records tagged for review, and Held those
	// of them that were waiting for a worker and are now held.
	Tagged int
	Held   int
}

// importStep is a test seam: a crash test kills the process at a named step.
var importStep = func(string) {}

// Import applies a reconciliation in one transaction (invariant 7): a
// tombstone for each entry decided done, and the review tag on every other
// non-terminal record, mapped or not, each keeping its state and blocking
// reason. An entry that cannot be applied — a done entry whose record a
// worker holds, a held entry for an event the ledger never saw — refuses the
// whole file, and nothing is written.
func (l *Ledger) Import(ctx context.Context, r Reconciliation, by string) (ImportResult, error) {
	if strings.TrimSpace(by) == "" {
		return ImportResult{}, errors.New("connector: an import records who applied it")
	}
	var out ImportResult
	err := retryBusy(func() error {
		var err error
		out, err = l.importReconciliation(ctx, r, by)
		return err
	})
	return out, err
}

func (l *Ledger) importReconciliation(ctx context.Context, r Reconciliation, by string) (ImportResult, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportResult{}, fmt.Errorf("connector: begin import: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var out ImportResult
	now := l.timestamp()
	done := map[int64]bool{}
	for _, e := range r.Entries {
		var state string
		err := tx.QueryRowContext(ctx, `SELECT state FROM events WHERE id = ?`, e.EventID).Scan(&state)
		missing := errors.Is(err, sql.ErrNoRows)
		if err != nil && !missing {
			return ImportResult{}, fmt.Errorf("connector: import event %d: %w", e.EventID, err)
		}
		switch e.Decision {
		case DecisionDone:
			done[e.EventID] = true
			switch {
			case missing:
				// A tombstone and nothing else: the event can never become a
				// task, whichever lane serves it later.
				if _, err := tx.ExecContext(ctx, `
INSERT INTO events (id, state, reason, lane, event_type, kind, action, bucket_id, creator_id, recording_id,
                    created_at, seen_at, updated_at, content_dropped)
VALUES (?, 'discarded', ?, 'import', '', '', '', 0, 0, 0, ?, ?, ?, 1)`, e.EventID, ReasonImportedDone, now, now, now); err != nil {
					return ImportResult{}, fmt.Errorf("connector: import tombstone for %d: %w", e.EventID, err)
				}
				out.Inserted++
			case state == string(StateCompleted) || state == string(StateDiscarded):
				out.AlreadyTerminal++
			case state == string(StateDispatched):
				return ImportResult{}, fmt.Errorf("connector: import: event %d is dispatched to a worker; a done decision cannot close it: %w", e.EventID, ErrDecisionRefused)
			default:
				moved, err := l.move(ctx, tx, transition{id: e.EventID, state: StateDiscarded, reason: ReasonImportedDone,
					from: []RecordState{StateSeen, StateAdmitted, StateQueued, StateBlocked, StateHeld}, byOperator: true})
				if err != nil {
					return ImportResult{}, err
				}
				if !moved {
					return ImportResult{}, fmt.Errorf("connector: import: event %d (%s) cannot be closed: %w", e.EventID, state, ErrDecisionRefused)
				}
				out.Tombstoned++
			}
			if err := recordDecision(ctx, tx, decision{action: "import", eventID: e.EventID, by: by, at: now,
				fromState: RecordState(state), toState: StateDiscarded, note: "done"}); err != nil {
				return ImportResult{}, err
			}
		case DecisionHeld:
			if missing {
				return ImportResult{}, fmt.Errorf("connector: import: event %d is not in the ledger, so it cannot be held for review: %w", e.EventID, ErrDecisionRefused)
			}
		}
		importStep("entry")
	}

	var waiting int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE state IN ('admitted', 'queued')`).Scan(&waiting); err != nil {
		return ImportResult{}, fmt.Errorf("connector: import: %w", err)
	}
	// Everything not decided done waits for a person: tagging holds a waiting
	// record in the same statement (invariant 1), and leaves every other
	// record's state and reason as they are.
	res, err := tx.ExecContext(ctx, `
UPDATE events SET review = 1, authorized_at = NULL, authorized_by = ''
WHERE state NOT IN ('completed', 'discarded')`)
	if err != nil {
		return ImportResult{}, fmt.Errorf("connector: import: tag for review: %w", err)
	}
	tagged, err := res.RowsAffected()
	if err != nil {
		return ImportResult{}, err
	}
	out.Tagged, out.Held = int(tagged), waiting
	importStep("tagged")
	if err := recordDecision(ctx, tx, decision{action: "import", by: by, at: now,
		note: fmt.Sprintf("%d entries; %d tombstoned, %d tombstones inserted, %d tagged for review", len(r.Entries), out.Tombstoned, out.Inserted, out.Tagged)}); err != nil {
		return ImportResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ImportResult{}, fmt.Errorf("connector: commit import: %w", err)
	}
	return out, nil
}
