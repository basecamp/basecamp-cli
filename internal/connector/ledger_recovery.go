package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// LossState is what became of one id the live buffer dropped.
type LossState string

const (
	// LossMissing is an id the overflow dropped and nothing has served since.
	LossMissing LossState = "missing"
	// LossNeverLost is an id the ledger already held when the overflow was
	// signaled. It was dropped from the buffer, not from the connector.
	LossNeverLost LossState = "never_lost"
	// LossRecovered is an id a later poll — the repair walk or the ordinary
	// lane — served.
	LossRecovered LossState = "recovered"
	// LossUnrecovered is an id still missing when the repair window closed: a
	// late straggler, a recording deleted before it became poll-visible, or
	// history behind the epoch. It is reported, never hidden.
	LossUnrecovered LossState = "unrecovered"
)

// Loss is one accepted buffer overflow and the state of its reconciliation.
type Loss struct {
	ID           int64
	DetectedAt   time.Time
	DroppedCount int
	// RepairSince is the walk's entry: one below the lowest missing id.
	RepairSince int64
	// RepairCursor is the walk's own position, kept here and never written to
	// the feed's checkpoint.
	RepairCursor string
	DeadlineAt   time.Time
	ResolvedAt   *time.Time
}

// GapClass distinguishes the feed's two 410s. They mean different things and
// must never share a recovery path.
type GapClass string

const (
	// GapEpoch is the account feed's 410: the held position fell below the
	// feed's epoch, and epoch_after_id names where servable history begins.
	// The served resume URL is followed as given.
	GapEpoch GapClass = "epoch"
	// GapRetention is the inbox lane's 410: the held position fell out of the
	// 30-day retention window, and there is no epoch — epoch_after_id is
	// absent, not zero. The account lane must never produce one, and a
	// connector that sees one has been told something it does not model.
	GapRetention GapClass = "retention"
)

// EntryClass is how the feed re-entered after a 410, decided by the cursor the
// server served — never by what the connector would have chosen.
type EntryClass string

const (
	// EntryPresent is a resume at since=now: the entry takes the head and
	// history below it is gone.
	EntryPresent EntryClass = "present"
	// EntryReplay is a resume at since=<id>: the entry is a replay that
	// checkpoints from its first poll page.
	EntryReplay EntryClass = "replay"
	// EntryUnknown is a resume whose cursor could not be read.
	EntryUnknown EntryClass = "unknown"
)

// Gap is one recorded 410.
type Gap struct {
	ID         int64
	DetectedAt time.Time
	Class      GapClass
	// EpochAfterID is set exactly when Class is GapEpoch. It is a pointer
	// because the distinction between "absent" and "zero" is the whole of the
	// difference between the two 410s.
	EpochAfterID *int64
	EntryClass   EntryClass
	Note         string
}

// RecordLoss writes an accepted buffer overflow, in one transaction, before
// the handler returns Accept.
//
// Accept means the consumer owns the incompleteness. Owning it starts with it
// being on disk: a crash a millisecond later must still find the loss and
// resume its reconciliation, and a loss that only ever lived in memory is one
// the next start would never know to repair. The corollary is that a failed
// write is a reason to refuse the signal, not to accept it anyway.
//
// Dropped ids the ledger already holds were lost from the buffer, not from the
// connector; they are recorded as never_lost so the repair walk does not go
// looking for events it already has.
func (l *Ledger) RecordLoss(ctx context.Context, droppedIDs []int64, now time.Time, window time.Duration) (Loss, error) {
	if len(droppedIDs) == 0 {
		return Loss{}, errors.New("connector: buffer overflow with no dropped ids")
	}

	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return Loss{}, fmt.Errorf("connector: begin record loss: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	states := make(map[int64]LossState, len(droppedIDs))
	lowestMissing := int64(0)
	for _, id := range droppedIDs {
		if _, seen := states[id]; seen {
			continue
		}
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM events WHERE id = ?`, id).Scan(&one)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			states[id] = LossMissing
			if lowestMissing == 0 || id < lowestMissing {
				lowestMissing = id
			}
		case err != nil:
			return Loss{}, fmt.Errorf("connector: classify dropped id %d: %w", id, err)
		default:
			states[id] = LossNeverLost
		}
	}

	loss := Loss{
		DetectedAt:   now.UTC(),
		DroppedCount: len(states),
		// One below the lowest missing id, so the walk's first page can serve
		// it: the feed's since is exclusive.
		RepairSince: lowestMissing - 1,
		DeadlineAt:  now.UTC().Add(window),
	}
	if lowestMissing == 0 {
		// Every dropped id was already in the ledger. There is nothing to
		// walk for, but the overflow still happened and is still recorded.
		loss.RepairSince = 0
		resolved := now.UTC()
		loss.ResolvedAt = &resolved
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO losses (detected_at, dropped_count, repair_since, deadline_at, resolved_at) VALUES (?, ?, ?, ?, ?)`,
		stamp(loss.DetectedAt), loss.DroppedCount, loss.RepairSince, stamp(loss.DeadlineAt), nullableStamp(loss.ResolvedAt))
	if err != nil {
		return Loss{}, fmt.Errorf("connector: insert loss: %w", err)
	}
	if loss.ID, err = res.LastInsertId(); err != nil {
		return Loss{}, fmt.Errorf("connector: insert loss: %w", err)
	}

	for id, state := range states {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO loss_ids (loss_id, event_id, state) VALUES (?, ?, ?)`,
			loss.ID, id, string(state)); err != nil {
			return Loss{}, fmt.Errorf("connector: insert dropped id %d: %w", id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Loss{}, fmt.Errorf("connector: commit loss: %w", err)
	}
	return loss, nil
}

// OpenLosses returns the losses whose reconciliation has not finished, oldest
// first. Reconciliation resumes from this on every start.
func (l *Ledger) OpenLosses(ctx context.Context) ([]Loss, error) {
	rows, err := l.db.QueryContext(ctx, `
SELECT id, detected_at, dropped_count, repair_since, repair_cursor, deadline_at, resolved_at
FROM losses WHERE resolved_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("connector: list open losses: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var losses []Loss
	for rows.Next() {
		var (
			loss               Loss
			detected, deadline string
			resolved           sql.NullString
		)
		if err := rows.Scan(&loss.ID, &detected, &loss.DroppedCount, &loss.RepairSince,
			&loss.RepairCursor, &deadline, &resolved); err != nil {
			return nil, fmt.Errorf("connector: scan loss: %w", err)
		}
		var err error
		if loss.DetectedAt, err = parseStamp(detected); err != nil {
			return nil, err
		}
		if loss.DeadlineAt, err = parseStamp(deadline); err != nil {
			return nil, err
		}
		if resolved.Valid {
			t, err := parseStamp(resolved.String)
			if err != nil {
				return nil, err
			}
			loss.ResolvedAt = &t
		}
		losses = append(losses, loss)
	}
	return losses, rows.Err()
}

// MissingIDs returns the ids of a loss still in state, ascending.
func (l *Ledger) MissingIDs(ctx context.Context, lossID int64, state LossState) ([]int64, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT event_id FROM loss_ids WHERE loss_id = ? AND state = ? ORDER BY event_id`,
		lossID, string(state))
	if err != nil {
		return nil, fmt.Errorf("connector: list loss ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("connector: scan loss id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SaveRepairCursor records where the repair walk has reached.
//
// This is the walk's own cursor and it is stored on the loss, never on the
// feed's checkpoint. The walk is seeded from a live id that ran far ahead of
// the poll lane; a checkpoint taken from it would skip every event still
// inside the safety delay behind it.
func (l *Ledger) SaveRepairCursor(ctx context.Context, lossID int64, cursor string) error {
	_, err := l.db.ExecContext(ctx,
		`UPDATE losses SET repair_cursor = ? WHERE id = ?`, cursor, lossID)
	if err != nil {
		return fmt.Errorf("connector: save repair cursor: %w", err)
	}
	return nil
}

// CloseLoss ends a loss's reconciliation: everything still missing becomes
// unrecovered, and the loss is resolved. It reports how many ids were left
// unrecovered.
func (l *Ledger) CloseLoss(ctx context.Context, lossID int64, now time.Time) (int, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("connector: begin close loss: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`UPDATE loss_ids SET state = ? WHERE loss_id = ? AND state = ?`,
		string(LossUnrecovered), lossID, string(LossMissing))
	if err != nil {
		return 0, fmt.Errorf("connector: mark unrecovered: %w", err)
	}
	unrecovered, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("connector: mark unrecovered: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE losses SET resolved_at = ? WHERE id = ? AND resolved_at IS NULL`,
		stamp(now.UTC()), lossID); err != nil {
		return 0, fmt.Errorf("connector: resolve loss: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("connector: commit close loss: %w", err)
	}
	return int(unrecovered), nil
}

// SetRepairEntry moves a loss's walk entry to since and drops its cursor —
// after a 410 has fenced off everything at or below since.
func (l *Ledger) SetRepairEntry(ctx context.Context, lossID, since int64) error {
	_, err := l.db.ExecContext(ctx,
		`UPDATE losses SET repair_since = ?, repair_cursor = '' WHERE id = ?`, since, lossID)
	if err != nil {
		return fmt.Errorf("connector: set repair entry: %w", err)
	}
	return nil
}

// MarkUnrecoveredThrough gives up on a loss's missing ids at or below id — the
// ones a 410's epoch has fenced off — and leaves the rest missing.
func (l *Ledger) MarkUnrecoveredThrough(ctx context.Context, lossID, id int64) (int, error) {
	res, err := l.db.ExecContext(ctx,
		`UPDATE loss_ids SET state = ? WHERE loss_id = ? AND state = ? AND event_id <= ?`,
		string(LossUnrecovered), lossID, string(LossMissing), id)
	if err != nil {
		return 0, fmt.Errorf("connector: mark unrecovered below the epoch: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("connector: mark unrecovered below the epoch: %w", err)
	}
	return int(n), nil
}

// UnrecoveredIDs returns every id the connector has given up on, across all
// losses. `basecamp connect status` and `doctor` show these: an unrecovered id
// is documented, not hidden, and the poll lane may still serve it later.
func (l *Ledger) UnrecoveredIDs(ctx context.Context) ([]int64, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT DISTINCT event_id FROM loss_ids WHERE state = ? ORDER BY event_id`,
		string(LossUnrecovered))
	if err != nil {
		return nil, fmt.Errorf("connector: list unrecovered ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("connector: scan unrecovered id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RecordGap writes one 410. Nothing is posted to Basecamp for a gap; it is a
// fact about the feed, shown by status and doctor.
func (l *Ledger) RecordGap(ctx context.Context, gap Gap) (int64, error) {
	switch gap.Class {
	case GapEpoch:
		if gap.EpochAfterID == nil {
			return 0, errors.New("connector: an epoch gap must carry epoch_after_id")
		}
	case GapRetention:
		if gap.EpochAfterID != nil {
			return 0, errors.New("connector: a retention gap has no epoch_after_id")
		}
	default:
		return 0, fmt.Errorf("connector: unknown gap class %q", gap.Class)
	}

	res, err := l.db.ExecContext(ctx,
		`INSERT INTO gaps (detected_at, class, epoch_after_id, entry_class, note) VALUES (?, ?, ?, ?, ?)`,
		stamp(gap.DetectedAt.UTC()), string(gap.Class), gap.EpochAfterID, string(gap.EntryClass), gap.Note)
	if err != nil {
		return 0, fmt.Errorf("connector: record gap: %w", err)
	}
	return res.LastInsertId()
}

// Gaps returns every recorded 410, oldest first.
func (l *Ledger) Gaps(ctx context.Context) ([]Gap, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT id, detected_at, class, epoch_after_id, entry_class, note FROM gaps ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("connector: list gaps: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var gaps []Gap
	for rows.Next() {
		var (
			gap        Gap
			detected   string
			class      string
			entryClass string
			epoch      sql.NullInt64
		)
		if err := rows.Scan(&gap.ID, &detected, &class, &epoch, &entryClass, &gap.Note); err != nil {
			return nil, fmt.Errorf("connector: scan gap: %w", err)
		}
		var err error
		if gap.DetectedAt, err = parseStamp(detected); err != nil {
			return nil, err
		}
		gap.Class = GapClass(class)
		gap.EntryClass = EntryClass(entryClass)
		if epoch.Valid {
			id := epoch.Int64
			gap.EpochAfterID = &id
		}
		gaps = append(gaps, gap)
	}
	return gaps, rows.Err()
}

func stamp(t time.Time) string { return t.UTC().Format(ledgerTime) }

func nullableStamp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return stamp(*t)
}
