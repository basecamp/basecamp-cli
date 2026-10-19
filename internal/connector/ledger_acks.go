package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// An ack is the boost the connector gave a request's recording, as the agent,
// to acknowledge it before handing it off. It is written once the boost
// lands and before the request's line is, so a line printed again after a
// crash (see handOff) carries the boost already given rather than giving a
// second one.
//
// What it cannot cover is a crash between Basecamp creating the boost and
// this row being written: the next run boosts again. That window is one
// local write wide, and it is the same double a boost that timed out but
// landed makes, which the session's fallback already risks.
const migrationAcks = `
CREATE TABLE acks (
  event_id   INTEGER PRIMARY KEY REFERENCES events (id),
  boost_id   INTEGER NOT NULL CHECK (boost_id > 0),
  content    TEXT    NOT NULL CHECK (content <> ''),
  boosted_at TEXT    NOT NULL
);
`

// recordAck records the boost that acknowledged eventID. A second record
// for one event keeps the first.
func (l *Ledger) recordAck(ctx context.Context, eventID int64, ack HandoffAck) error {
	return retryBusy(func() error {
		_, err := l.db.ExecContext(ctx,
			`INSERT INTO acks (event_id, boost_id, content, boosted_at) VALUES (?, ?, ?, ?) ON CONFLICT (event_id) DO NOTHING`,
			eventID, ack.BoostID, ack.Content, l.timestamp())
		if err != nil {
			return fmt.Errorf("connector: record the acknowledgement of %d: %w", eventID, err)
		}
		return nil
	})
}

// ack is the boost that acknowledged eventID, if one did.
func (l *Ledger) ack(ctx context.Context, eventID int64) (HandoffAck, bool, error) {
	var ack HandoffAck
	err := l.db.QueryRowContext(ctx, `SELECT boost_id, content FROM acks WHERE event_id = ?`, eventID).Scan(&ack.BoostID, &ack.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return HandoffAck{}, false, nil
	}
	if err != nil {
		return HandoffAck{}, false, fmt.Errorf("connector: read the acknowledgement of %d: %w", eventID, err)
	}
	return ack, true, nil
}
