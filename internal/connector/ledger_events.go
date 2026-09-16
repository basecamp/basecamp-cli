package connector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// Record is one row of the events table.
type Record struct {
	ID               int64
	State            RecordState
	Reason           string
	Lane             Lane
	EventType        string
	Kind             string
	Action           string
	BucketID         int64
	CreatorID        int64
	PerformedByID    *int64
	RecordingID      int64
	Details          json.RawMessage
	ActorType        string
	VisibleToClients *bool
	CreatedAt        time.Time
	SeenAt           time.Time
	UpdatedAt        time.Time
	ContentDropped   bool
}

// EffectivePerformer is the id the performers/exclude_performers filters
// match: the delegating agent when the action was delegated, else the creator.
func (r Record) EffectivePerformer() int64 {
	if r.PerformedByID != nil {
		return *r.PerformedByID
	}
	return r.CreatorID
}

// RecordSeen writes the pointer as seen if the id is new, and reports whether
// it was.
//
// This is the dedupe, and it is durable on purpose. The poll lane runs about
// thirty seconds behind the live lane, so the ordinary case is the same event
// arriving twice; a restart in between would let an in-memory set answer "new"
// to the second copy and dispatch it a second time.
//
// A known id is never rewritten. That is what makes a terminal record a
// tombstone: the ledger keeps (id, state, timestamps) indefinitely even after
// its pointer payload is dropped, so an explicit replay of old history can
// never turn a finished event back into a new task.
//
// It also resolves the id against any loss, open or closed: an event the repair walk — or
// the ordinary poll lane, later — serves is one the overflow did not cost us.
func (l *Ledger) RecordSeen(ctx context.Context, ev eventfeed.Event, lane Lane) (bool, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("connector: begin record seen: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := l.timestamp()
	res, err := tx.ExecContext(ctx, `
INSERT INTO events (
  id, state, lane, event_type, kind, action, bucket_id, creator_id,
  performed_by_id, recording_id, details, actor_type, visible_to_clients,
  created_at, seen_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING`,
		ev.ID, string(StateSeen), string(lane), ev.EventType, ev.Kind, ev.Action,
		ev.BucketID, ev.CreatorID, ev.PerformedByID, ev.RecordingID,
		detailsArg(ev.Details), ev.ActorType, ev.VisibleToClients,
		ev.CreatedAt.UTC().Format(time.RFC3339Nano), now, now)
	if err != nil {
		return false, fmt.Errorf("connector: record seen %d: %w", ev.ID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("connector: record seen %d: %w", ev.ID, err)
	}

	// Missing or already given up on: an id intake has received is recovered,
	// however late it came, and status must stop reporting it.
	if _, err := tx.ExecContext(ctx,
		`UPDATE loss_ids SET state = ? WHERE event_id = ? AND state IN (?, ?)`,
		string(LossRecovered), ev.ID, string(LossMissing), string(LossUnrecovered)); err != nil {
		return false, fmt.Errorf("connector: resolve loss for %d: %w", ev.ID, err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("connector: commit record seen %d: %w", ev.ID, err)
	}
	return affected == 1, nil
}

// detailsArg keeps a details object out of the row when the event publishes
// none. Only boost.created and card.moved do; storing an empty blob for every
// other type would make "has details" unanswerable in SQL.
func detailsArg(details json.RawMessage) any {
	if len(details) == 0 {
		return nil
	}
	return []byte(details)
}

// Get returns one record by event id.
func (l *Ledger) Get(ctx context.Context, id int64) (Record, bool, error) {
	rows, err := l.db.QueryContext(ctx, selectRecords+` WHERE id = ?`, id)
	if err != nil {
		return Record{}, false, fmt.Errorf("connector: get event %d: %w", id, err)
	}
	records, err := scanRecords(rows)
	if err != nil || len(records) == 0 {
		return Record{}, false, err
	}
	return records[0], true, nil
}

// RecordsInState returns up to limit records in state, oldest event first.
// Every non-terminal state is re-run on start, and this is how they are found.
func (l *Ledger) RecordsInState(ctx context.Context, state RecordState, limit int) ([]Record, error) {
	rows, err := l.db.QueryContext(ctx, selectRecords+` WHERE state = ? ORDER BY id LIMIT ?`, string(state), limit)
	if err != nil {
		return nil, fmt.Errorf("connector: list %s records: %w", state, err)
	}
	return scanRecords(rows)
}

// CountInState counts the records in state.
func (l *Ledger) CountInState(ctx context.Context, state RecordState) (int, error) {
	var n int
	err := l.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE state = ?`, string(state)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("connector: count %s records: %w", state, err)
	}
	return n, nil
}

// SetState moves a record to state with a reason, which must be empty for
// every state but blocked and discarded — those two are the only ones a reason
// explains.
func (l *Ledger) SetState(ctx context.Context, id int64, state RecordState, reason string) error {
	switch state {
	case StateBlocked, StateDiscarded:
		if reason == "" {
			return fmt.Errorf("connector: set state of %d: a %s record needs a reason", id, state)
		}
	case StateSeen, StateAdmitted, StateQueued, StateDispatched, StateCompleted:
		if reason != "" {
			return fmt.Errorf("connector: set state of %d: a %s record takes no reason", id, state)
		}
	default:
		// A state outside the lifecycle is a row no recovery scan looks for.
		return fmt.Errorf("connector: set state of %d: %q is not a ledger state", id, state)
	}
	res, err := l.db.ExecContext(ctx,
		`UPDATE events SET state = ?, reason = ?, updated_at = ? WHERE id = ?`,
		string(state), reason, l.timestamp(), id)
	if err != nil {
		return fmt.Errorf("connector: set state of %d: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("connector: set state of %d: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("connector: set state of %d: %w", id, ErrNoSuchRecord)
	}
	return nil
}

// ErrNoSuchRecord reports a state change addressed at an id the ledger does
// not hold.
var ErrNoSuchRecord = errors.New("no such event record")

// DropContent drops the pointer payload of terminal records past their
// retention window and leaves the tombstone: id, state, outcome timestamps.
//
// The tombstone is what makes an explicit replay safe forever, so it is never
// deleted — only the payload goes. Non-terminal records are never touched:
// their payload is the only copy of what intake was told.
func (l *Ledger) DropContent(ctx context.Context, discardedBefore, completedBefore time.Time) (int, error) {
	res, err := l.db.ExecContext(ctx, `
UPDATE events
SET details = NULL, event_type = '', kind = '', action = '', bucket_id = 0,
    creator_id = 0, performed_by_id = NULL, recording_id = 0, actor_type = '',
    visible_to_clients = NULL, content_dropped = 1, updated_at = updated_at
WHERE content_dropped = 0
  AND ((state = ? AND updated_at < ?) OR (state = ? AND updated_at < ?))`,
		string(StateDiscarded), discardedBefore.UTC().Format(time.RFC3339Nano),
		string(StateCompleted), completedBefore.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("connector: drop content: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("connector: drop content: %w", err)
	}
	return int(affected), nil
}

const selectRecords = `
SELECT id, state, reason, lane, event_type, kind, action, bucket_id, creator_id,
       performed_by_id, recording_id, details, actor_type, visible_to_clients,
       created_at, seen_at, updated_at, content_dropped
FROM events`

func scanRecords(rows *sql.Rows) ([]Record, error) {
	defer func() { _ = rows.Close() }()

	var records []Record
	for rows.Next() {
		var (
			r                            Record
			state, lane                  string
			details                      []byte
			createdAt, seenAt, updatedAt string
			contentDropped               int
			performedBy                  sql.NullInt64
			visibleToClients             sql.NullBool
		)
		if err := rows.Scan(&r.ID, &state, &r.Reason, &lane, &r.EventType, &r.Kind,
			&r.Action, &r.BucketID, &r.CreatorID, &performedBy, &r.RecordingID,
			&details, &r.ActorType, &visibleToClients, &createdAt, &seenAt,
			&updatedAt, &contentDropped); err != nil {
			return nil, fmt.Errorf("connector: scan event record: %w", err)
		}
		r.State = RecordState(state)
		r.Lane = Lane(lane)
		if performedBy.Valid {
			id := performedBy.Int64
			r.PerformedByID = &id
		}
		if visibleToClients.Valid {
			v := visibleToClients.Bool
			r.VisibleToClients = &v
		}
		if len(details) > 0 {
			r.Details = json.RawMessage(details)
		}
		var err error
		if r.CreatedAt, err = parseStamp(createdAt); err != nil {
			return nil, err
		}
		if r.SeenAt, err = parseStamp(seenAt); err != nil {
			return nil, err
		}
		if r.UpdatedAt, err = parseStamp(updatedAt); err != nil {
			return nil, err
		}
		r.ContentDropped = contentDropped != 0
		records = append(records, r)
	}
	return records, rows.Err()
}

func parseStamp(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("connector: parse ledger timestamp %q: %w", s, err)
	}
	return t, nil
}
