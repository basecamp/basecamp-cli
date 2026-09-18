package connector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
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

	// Revision counts every state write applied to the record since intake
	// recorded it, a repeat of the state it already has included. A decision
	// applies only at the revision it loaded.
	Revision int64
	// Decision is admission's latest verdict on the record; its zero value
	// until one is written.
	Decision Decision
}

// Decision is what admission wrote onto a record with its verdict. The pointer
// fields stay intake's; these are admission's, and dispatch starts a task from
// them.
type Decision struct {
	// DecidedAt is when the latest verdict was written.
	DecidedAt *time.Time
	// BlockedAt is when the record entered its current run of blocked
	// verdicts; nil unless it is blocked.
	BlockedAt *time.Time
	// RetryAt is a throttled verdict's server deadline; nil otherwise.
	RetryAt *time.Time

	Trigger          string
	Acknowledge      bool
	ConversationKey  string
	ReplyKind        string
	ReplyRecordingID int64
	Routed           bool
	Route            string
	Class            string
	RecordingURL     string
	RequesterID      int64
	// Snapshot is the recording's content as admission read it, JSON. An
	// admitted verdict writes it, as admitted or queued; it stays through
	// dispatch and completion until retention drops it, and any move to
	// blocked or discarded clears it.
	Snapshot json.RawMessage
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
		stamp(ev.CreatedAt), now, now)
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

// RecordsInStateAfter returns up to limit records in state whose id is above
// afterID, oldest first — the paging form of RecordsInState.
func (l *Ledger) RecordsInStateAfter(ctx context.Context, state RecordState, afterID int64, limit int) ([]Record, error) {
	rows, err := l.db.QueryContext(ctx,
		selectRecords+` WHERE state = ? AND id > ? ORDER BY id LIMIT ?`, string(state), afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("connector: list %s records: %w", state, err)
	}
	return scanRecords(rows)
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

// lifecycle is the ledger's state machine: for each state, the states a record
// may move to from it.
//
// Intake writes only seen; the rest is written by admission and dispatch. The
// table lives here anyway, because the guarantee it makes is the ledger's: a
// completed or discarded record is finished, and a store that lets one move
// back into the working states is a store where an event can be dispatched
// twice, or where a tombstone whose payload has been dropped is picked up as
// work with nothing in it.
//
// Blocked is not terminal on purpose: it is retained and retried, so it has
// edges back into the working states. Completed and discarded have none.
var lifecycle = map[RecordState][]RecordState{
	// seen to queued is one edge, not two: admission commits an admitted
	// verdict AS queued when the conversation is already live — another
	// record on it admitted or dispatched, admission.Ledger's definition — so
	// the record never passes through admitted at all. Deciding that moves
	// only the incoming record; a queued record leaves queued by dispatch.
	//
	// A blocked record's edges back into the working states are for the
	// lifecycle's own bookkeeping. It returns to work with content only
	// through a new verdict, because a move to blocked drops the snapshot.
	StateSeen:     {StateAdmitted, StateQueued, StateBlocked, StateDiscarded},
	StateAdmitted: {StateQueued, StateDispatched, StateBlocked, StateDiscarded},
	StateQueued:   {StateDispatched, StateBlocked, StateDiscarded},
	StateBlocked:  {StateAdmitted, StateQueued, StateDispatched, StateDiscarded},
	// A dispatched record whose spawn failed before any worker process
	// existed has its exposure withdrawn (task_events.withdrawn_at) and
	// returns to admitted; one a worker was handed otherwise leaves only to
	// completed (the dispatch lifecycle, ledger_dispatch.go). It is never discarded: a dispatched
	// event ends completed, with an outcome, even when the outcome is
	// unknown.
	StateDispatched: {StateCompleted, StateBlocked, StateAdmitted},
	StateCompleted:  nil,
	StateDiscarded:  nil,
}

// enterableFrom is the states a record may be in for a move to target to be
// allowed, target itself included: writing the state a record already has is a
// repeat, not a move, so a retry after a crash is not an error.
func enterableFrom(target RecordState) []string {
	froms := []string{string(target)}
	for from, tos := range lifecycle {
		if slices.Contains(tos, target) {
			froms = append(froms, string(from))
		}
	}
	// Sorted so the statement is the same every time it is built, whatever
	// order the map ranges in.
	slices.Sort(froms)
	return froms
}

// ErrNotATransition reports a state change the lifecycle does not have.
var ErrNotATransition = errors.New("not a transition the ledger's lifecycle allows")

// SetState moves a record to state with a reason, which must be empty for
// every state but blocked and discarded — those two are the only ones a reason
// explains.
//
// The move is refused unless the lifecycle has that edge, and it is refused by
// the UPDATE itself rather than by a read before it: a check and a write in
// two statements is a race, and this is the guarantee that a finished record
// stays finished.
func (l *Ledger) SetState(ctx context.Context, id int64, state RecordState, reason string) error {
	moved, err := l.move(ctx, l.db, transition{id: id, state: state, reason: reason})
	if err != nil {
		return err
	}
	if !moved {
		return l.explainRefusal(ctx, id, state)
	}
	return nil
}

// dbtx is what a transition runs against: the ledger's handle, or a
// transaction a caller already holds so the move commits with its other
// writes.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// transition is one state change and what is written with it.
type transition struct {
	id     int64
	state  RecordState
	reason string
	// from narrows the states the record may be leaving. Empty means every
	// state the lifecycle lets reach state; a narrowing can only remove edges,
	// never add one.
	from []RecordState
	// revision, when set, applies the move only while the record is still at
	// that revision.
	revision *int64
	// retryAt is a blocked record's not-before deadline, stored as retry_at;
	// the zero time stores none. Only a move to blocked may carry one.
	retryAt time.Time
	// set is further columns written in the same statement.
	set []assignment
}

// assignment is one further column a transition writes. column is this
// package's own constant, never input; value is bound.
type assignment struct {
	column string
	value  any
}

// move is the ledger's one state-changing write. Every change of state goes
// through it, so every change meets the lifecycle, bumps the revision, and
// keeps the retention clock where a repeat must leave it. It reports whether
// the record moved; a refusal is the caller's to explain.
//
// revision is bumped on every applied write, a repeat included. It is what a
// decision loaded earlier is compared against, and any write since that load
// is a reason the decision may no longer hold.
func (l *Ledger) move(ctx context.Context, db dbtx, t transition) (bool, error) {
	switch t.state {
	case StateBlocked, StateDiscarded:
		if t.reason == "" {
			return false, fmt.Errorf("connector: set state of %d: a %s record needs a reason", t.id, t.state)
		}
	case StateSeen, StateAdmitted, StateQueued, StateDispatched, StateCompleted:
		if t.reason != "" {
			return false, fmt.Errorf("connector: set state of %d: a %s record takes no reason", t.id, t.state)
		}
	default:
		// A state outside the lifecycle is a row no recovery scan looks for.
		return false, fmt.Errorf("connector: set state of %d: %q is not a ledger state", t.id, t.state)
	}
	if !t.retryAt.IsZero() && t.state != StateBlocked {
		return false, fmt.Errorf("connector: set state of %d: only a blocked record has a retry deadline", t.id)
	}
	froms := enterableFrom(t.state)
	if len(t.from) > 0 {
		froms = slices.DeleteFunc(froms, func(from string) bool {
			return !slices.Contains(t.from, RecordState(from))
		})
	}

	// updated_at is left alone when the state does not change. It is the
	// retention clock DropContent reads, and a repeated write of the state a
	// record already has would silently restart the window on a finished
	// record. Every right-hand side reads the row as it was before this
	// statement, which is SQLite's rule for UPDATE.
	//
	// The blocked schedule's inputs are the move's too, so no path into or out
	// of blocked can leave them stale. blocked_at is when the record entered
	// its current run of blocked states — cleared on leaving, so it is set
	// exactly on entering and kept across a repeat — because the retry
	// window counts from it.
	// retry_at holds only for the move that set it. And a record moving to
	// blocked or discarded loses its snapshot: only a record on its way to a
	// worker carries content.
	now := l.timestamp()
	var retryAt any
	if !t.retryAt.IsZero() {
		retryAt = stamp(t.retryAt)
	}
	var query strings.Builder
	query.WriteString(`UPDATE events SET state = ?, reason = ?, revision = revision + 1,
  updated_at = CASE WHEN state = ? THEN updated_at ELSE ? END,
  blocked_at = CASE WHEN ? <> 'blocked' THEN NULL ELSE COALESCE(blocked_at, ?) END,
  retry_at = ?,
  snapshot = CASE WHEN ? IN ('blocked', 'discarded') THEN NULL ELSE snapshot END`)
	args := []any{string(t.state), t.reason, string(t.state), now, string(t.state), now, retryAt, string(t.state)}
	for _, a := range t.set {
		query.WriteString(", " + a.column + " = ?")
		args = append(args, a.value)
	}
	query.WriteString(" WHERE id = ?")
	args = append(args, t.id)
	if t.revision != nil {
		query.WriteString(" AND revision = ?")
		args = append(args, *t.revision)
	}
	query.WriteString(" AND state IN (" + strings.TrimSuffix(strings.Repeat("?, ", len(froms)), ", ") + ")")
	switch t.state {
	case StateDispatched:
		// A record is dispatched exactly while a live task carries it: it
		// enters dispatched only with its task row already written
		// (createTask), and a repeat is a repeat.
		query.WriteString(" AND (state = 'dispatched' OR " + onLiveTask + ")")
	case StateCompleted:
	default:
		// Invariant 4 of the dispatch lifecycle (ledger_dispatch.go): a
		// record a worker was handed leaves dispatched only to completed —
		// and any other record leaves it only once no live task carries it
		// (supersedeTask retires the row first).
		query.WriteString(" AND NOT (" + heldByWorker + ")")
		query.WriteString(" AND NOT (state = 'dispatched' AND " + onLiveTask + ")")
	}
	for _, from := range froms {
		args = append(args, from)
	}

	// Concatenated are column names this package declares,
	// and a list of "?" as long as the lifecycle's own edge list. Every value
	// is bound.
	res, err := db.ExecContext(ctx, query.String(), args...) //nolint:gosec // G202: constants and placeholders, not values
	if err != nil {
		return false, fmt.Errorf("connector: set state of %d: %w", t.id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("connector: set state of %d: %w", t.id, err)
	}
	return affected > 0, nil
}

// heldByWorker is true of a dispatched events row a worker was handed and
// has not reported on: a delivery exposed or delivered, on any task, and not
// withdrawn because the spawn failed before any worker existed.
const heldByWorker = `state = 'dispatched' AND EXISTS (
  SELECT 1 FROM task_events WHERE task_events.event_id = events.id
    AND delivery IN ('exposed', 'delivered') AND withdrawn_at IS NULL)`

// onLiveTask is true of an events row a live task carries.
const onLiveTask = `EXISTS (
  SELECT 1 FROM task_events WHERE task_events.event_id = events.id AND retired_at IS NULL)`

// ErrNotOnALiveTask is a move into dispatched for a record no live task
// carries. Records are dispatched by creating a task for them.
var ErrNotOnALiveTask = errors.New("no live task carries this event; a record is dispatched by creating a task for it")

// ErrOnALiveTask is a move out of dispatched, other than to completed, for a
// record a live task still carries. Its task is superseded first.
var ErrOnALiveTask = errors.New("a live task still carries this event; supersede the task first")

// ErrHeldByWorker is a move out of dispatched for an event a worker was
// handed and has not reported on. Only its outcome moves it.
var ErrHeldByWorker = errors.New("a worker was handed this event; it leaves dispatched only when completed")

// explainRefusal says why an update changed nothing: there is no such record,
// or the record is somewhere the lifecycle cannot leave for state.
//
// The refusal itself already happened, in the UPDATE. This is the message, and
// it reads the row a second time: under a concurrent writer it can name a
// state the record has since left. Diagnostics, not a verdict to act on.
func (l *Ledger) explainRefusal(ctx context.Context, id int64, state RecordState) error {
	var current string
	switch err := l.db.QueryRowContext(ctx, `SELECT state FROM events WHERE id = ?`, id).Scan(&current); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("connector: set state of %d: %w", id, ErrNoSuchRecord)
	case err != nil:
		return fmt.Errorf("connector: set state of %d: %w", id, err)
	}
	if slices.Contains(enterableFrom(state), current) {
		// The edge exists; a dispatch rule refused it.
		var held, live bool
		if err := l.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE id = ? AND `+heldByWorker+`), EXISTS (SELECT 1 FROM events WHERE id = ? AND `+onLiveTask+`)`, id, id).Scan(&held, &live); err != nil {
			return fmt.Errorf("connector: set state of %d: %w", id, err)
		}
		switch {
		case held:
			return fmt.Errorf("connector: set state of %d: %w", id, ErrHeldByWorker)
		case state == StateDispatched && !live:
			return fmt.Errorf("connector: set state of %d: %w", id, ErrNotOnALiveTask)
		case live:
			return fmt.Errorf("connector: set state of %d: %w", id, ErrOnALiveTask)
		}
	}
	return fmt.Errorf("connector: set state of %d: %s to %s is %w", id, current, state, ErrNotATransition)
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
    visible_to_clients = NULL, content_dropped = 1, updated_at = updated_at,
    snapshot = NULL, trigger_name = '', acknowledge = 0, conversation_key = '',
    reply_kind = '', reply_recording_id = 0, routed = 0, route = '', class = '',
    recording_url = '', requester_id = 0
WHERE content_dropped = 0
  AND ((state = ? AND updated_at < ?) OR (state = ? AND updated_at < ?))`,
		string(StateDiscarded), stamp(discardedBefore),
		string(StateCompleted), stamp(completedBefore))
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
       created_at, seen_at, updated_at, content_dropped, revision, decided_at,
       blocked_at, retry_at, trigger_name, acknowledge, conversation_key,
       reply_kind, reply_recording_id, routed, route, class, recording_url,
       requester_id, snapshot
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
			decidedAt, blockedAt         sql.NullString
			retryAt                      sql.NullString
			acknowledge, routed          int
			snapshot                     []byte
			d                            = &r.Decision
		)
		if err := rows.Scan(&r.ID, &state, &r.Reason, &lane, &r.EventType, &r.Kind,
			&r.Action, &r.BucketID, &r.CreatorID, &performedBy, &r.RecordingID,
			&details, &r.ActorType, &visibleToClients, &createdAt, &seenAt,
			&updatedAt, &contentDropped, &r.Revision, &decidedAt, &blockedAt,
			&retryAt, &d.Trigger, &acknowledge, &d.ConversationKey, &d.ReplyKind,
			&d.ReplyRecordingID, &routed, &d.Route, &d.Class, &d.RecordingURL,
			&d.RequesterID, &snapshot); err != nil {
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
		d.Acknowledge, d.Routed = acknowledge != 0, routed != 0
		if len(snapshot) > 0 {
			d.Snapshot = json.RawMessage(snapshot)
		}
		for _, stamped := range []struct {
			raw sql.NullString
			to  **time.Time
		}{{decidedAt, &d.DecidedAt}, {blockedAt, &d.BlockedAt}, {retryAt, &d.RetryAt}} {
			if !stamped.raw.Valid {
				continue
			}
			at, err := parseStamp(stamped.raw.String)
			if err != nil {
				return nil, err
			}
			*stamped.to = &at
		}
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
