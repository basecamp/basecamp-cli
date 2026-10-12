package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// An ask is a post of the agent's that asks the operator for a go: approval
// of exactly what the post names. The agent declares it when it posts
// (`basecamp connect ask <url>`), and nothing else makes one: the connector
// never decides that a post is an ask by reading it.
//
// The operator's boost on a declared ask is the go. It is decided in the
// verdict's own transaction (Admission.commit), so the boost that takes an
// ask is the one whose verdict committed first, and no other: one ask gets
// one go. A go taken stays taken, even when its record is closed unwritten
// (before_this_run): a connector that printed the line and stopped before
// closing the record looks the same on restart, and giving that go back would
// let the next boost approve twice. Losing a go costs the operator a mention;
// a second go costs a second action. Another boost on it afterwards — the operator's second, or one
// removed and given again, which Basecamp records as a new boost and a new
// event — is an ordinary boosted reply. The event that took the go keeps it
// when it is decided again (a redispatch), because go_event_id names it.
//
// An ask does not expire. The go is the operator's deliberate boost on the
// post itself, and what it approves is what the post names, which a worker
// checks is still current before acting (a branch still at the commit the
// ask named, an action not already done). A deadline would add a number
// with nothing to choose it by, and turn a late but deliberate approval into
// a thumbs-up without saying so.
const migrationAsks = `
CREATE TABLE asks (
  recording_id INTEGER PRIMARY KEY CHECK (recording_id > 0),
  declared_at  TEXT    NOT NULL,
  declared_by  TEXT    NOT NULL CHECK (declared_by <> ''),
  go_event_id  INTEGER UNIQUE REFERENCES events (id)
);
`

// AskResult is what declaring an ask did.
type AskResult struct {
	RecordingID int64 `json:"recording_id"`
	// Already says the recording was declared an ask before; the first
	// declaration stands.
	Already bool `json:"already"`
	// GoEventID is the event whose boost took the ask's go, zero while none
	// has.
	GoEventID int64 `json:"go_event_id,omitempty"`
}

// DeclareAsk records a recording the agent posted as an ask for the
// operator's go. Declaring one twice is not an error, and does not reopen an
// ask whose go was taken.
func (l *Ledger) DeclareAsk(ctx context.Context, recordingID int64, by string) (AskResult, error) {
	switch {
	case recordingID <= 0:
		return AskResult{}, errors.New("connector: an ask names a recording")
	case strings.TrimSpace(by) == "":
		return AskResult{}, errors.New("connector: an ask records who declared it")
	}
	var out AskResult
	err := retryBusy(func() error {
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("connector: begin ask: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		res, err := tx.ExecContext(ctx,
			`INSERT INTO asks (recording_id, declared_at, declared_by) VALUES (?, ?, ?) ON CONFLICT (recording_id) DO NOTHING`,
			recordingID, l.timestamp(), by)
		if err != nil {
			return fmt.Errorf("connector: declare ask on %d: %w", recordingID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("connector: declare ask on %d: %w", recordingID, err)
		}
		var goEvent sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT go_event_id FROM asks WHERE recording_id = ?`, recordingID).Scan(&goEvent); err != nil {
			return fmt.Errorf("connector: read ask on %d: %w", recordingID, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("connector: commit ask on %d: %w", recordingID, err)
		}
		out = AskResult{RecordingID: recordingID, Already: n == 0, GoEventID: goEvent.Int64}
		return nil
	})
	return out, err
}

// takeGo gives a declared ask's go to eventID, inside the verdict's
// transaction, and reports whether eventID holds it. An ask nobody declared,
// or one another event already took, gives nothing.
func takeGo(ctx context.Context, tx *sql.Tx, recordingID, eventID int64) (bool, error) {
	res, err := tx.ExecContext(ctx,
		`UPDATE asks SET go_event_id = ? WHERE recording_id = ? AND (go_event_id IS NULL OR go_event_id = ?)`,
		eventID, recordingID, eventID)
	if err != nil {
		return false, fmt.Errorf("connector: take the go on %d for %d: %w", recordingID, eventID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("connector: take the go on %d for %d: %w", recordingID, eventID, err)
	}
	return n == 1, nil
}

// isGo reports whether eventID took an ask's go.
func (l *Ledger) isGo(ctx context.Context, eventID int64) (bool, error) {
	var goes bool
	err := l.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM asks WHERE go_event_id = ?)`, eventID).Scan(&goes)
	if err != nil {
		return false, fmt.Errorf("connector: read the go for %d: %w", eventID, err)
	}
	return goes, nil
}
