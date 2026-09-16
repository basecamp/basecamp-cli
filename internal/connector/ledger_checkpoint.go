package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// Ledger implements the feed's checkpoint seam.
var _ eventfeed.CheckpointStore = (*Ledger)(nil)

// Load returns the stored position for key. It runs once, before the first
// mint, and a store failure is a failure to start rather than a silent entry
// at the present: re-entering at the head after a crash drops everything the
// feed committed while we were down.
func (l *Ledger) Load(ctx context.Context, key eventfeed.CheckpointKey) (string, bool, error) {
	var position string
	err := l.db.QueryRowContext(ctx,
		`SELECT position FROM checkpoints WHERE flat_key = ? AND position <> ''`, key.FlatKey()).Scan(&position)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// No row, or a row whose position was dropped by a 409 while its
		// last poll-served id was kept. Both mean "no position to resume";
		// an empty string reported as present would be sent to the server
		// as a position and refused as malformed.
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("connector: load checkpoint: %w", err)
	}
	return position, true, nil
}

// Save durably records position under key.
//
// The feed calls this only for a poll page, and only once that page's events
// have been accepted by the consumer — which, here, means they are already
// rows in the events table. A live event id never reaches this method, and
// must not: a live id runs ahead of the poll lane's safety delay, so a
// position taken from one would skip everything inside that window.
func (l *Ledger) Save(ctx context.Context, key eventfeed.CheckpointKey, position string) error {
	_, err := l.db.ExecContext(ctx, `
INSERT INTO checkpoints (flat_key, lineage, position, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (flat_key) DO UPDATE SET position = excluded.position, updated_at = excluded.updated_at`,
		key.FlatKey(), lineageOf(key), position, l.timestamp())
	if err != nil {
		return fmt.Errorf("connector: save checkpoint: %w", err)
	}
	return nil
}

// NotePollServed advances the last poll-served event id for key.
//
// This is tracked apart from the position, and apart from the last id
// delivered, because all three answer different questions. The position is an
// opaque signed token, so nothing can be compared to it or derived from it. The
// last delivered id is usually a live id, thirty-odd seconds ahead of the poll
// lane. Only the last poll-served id is a safe place to re-enter the feed
// after a 409 or a rejected position — anything ahead of it skips events the
// poll lane had not served yet.
//
// It only ever moves forward.
func (l *Ledger) NotePollServed(ctx context.Context, key eventfeed.CheckpointKey, eventID int64) error {
	_, err := l.db.ExecContext(ctx, `
INSERT INTO checkpoints (flat_key, lineage, position, last_poll_served_id, updated_at) VALUES (?, ?, '', ?, ?)
ON CONFLICT (flat_key) DO UPDATE SET
  last_poll_served_id = MAX(checkpoints.last_poll_served_id, excluded.last_poll_served_id),
  updated_at = excluded.updated_at`,
		key.FlatKey(), lineageOf(key), eventID, l.timestamp())
	if err != nil {
		return fmt.Errorf("connector: note poll-served id: %w", err)
	}
	return nil
}

// LastPollServedID returns the last id the poll lane served under key, or zero
// if it has served none.
func (l *Ledger) LastPollServedID(ctx context.Context, key eventfeed.CheckpointKey) (int64, error) {
	var id int64
	err := l.db.QueryRowContext(ctx,
		`SELECT last_poll_served_id FROM checkpoints WHERE flat_key = ?`, key.FlatKey()).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("connector: read last poll-served id: %w", err)
	}
	return id, nil
}

// LineagePollServedID returns the highest id the poll lane has served to this
// consumer under ANY filter set: same origin, account and namespace.
//
// It is the re-entry for a filter change. The new digest holds no position of
// its own, and entering it at the present would skip everything committed
// since the old digest's walk stopped. Events a narrower old filter excluded
// before this id are not recovered by widening — the spec says so, and so does
// this comment — but nothing after it is skipped.
func (l *Ledger) LineagePollServedID(ctx context.Context, key eventfeed.CheckpointKey) (int64, error) {
	var id int64
	err := l.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(last_poll_served_id), 0) FROM checkpoints WHERE lineage = ?`, lineageOf(key)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("connector: read lineage poll-served id: %w", err)
	}
	return id, nil
}

// lineageOf is the checkpoint identity without its filter digest.
func lineageOf(key eventfeed.CheckpointKey) string {
	key.FilterKey = ""
	return key.FlatKey()
}

// ForgetPosition drops the held position for key while keeping the last
// poll-served id — the 409 path. The server refused the position because it
// was minted for a different filter set; the id the poll lane had reached is
// still true, and is where the new digest re-enters.
func (l *Ledger) ForgetPosition(ctx context.Context, key eventfeed.CheckpointKey) error {
	_, err := l.db.ExecContext(ctx,
		`UPDATE checkpoints SET position = '', updated_at = ? WHERE flat_key = ?`,
		l.timestamp(), key.FlatKey())
	if err != nil {
		return fmt.Errorf("connector: forget position: %w", err)
	}
	return nil
}
