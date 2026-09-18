package connector

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Route waits in the ledger: a route whose last attempt at a worktree failed,
// and when the next attempt is due.
//
// The backoff itself lives in Worktrees, in memory, because that is where the
// dispatcher asks it. This table is the operator's copy of it. `connect
// status` and `connect doctor` run in another process, read the ledger
// read-only and never speak to a running connector, so a wait that stayed in
// memory was a wait nobody outside the connector's stdout could see — which is
// the whole of the problem this table exists for.
//
// One row per route, replaced on each failure and deleted the moment a
// worktree is made, so the table is never longer than the routes connect.json
// names. A row is a record of failure, not a schedule: it stands after `until`
// has passed, because nothing has proved the route works since.
const migrationRouteWaits = `
CREATE TABLE route_waits (
  route    TEXT    PRIMARY KEY,
  failures INTEGER NOT NULL CHECK (failures > 0),
  reason   TEXT    NOT NULL DEFAULT '',
  first_at TEXT    NOT NULL,
  last_at  TEXT    NOT NULL,
  until    TEXT    NOT NULL
);
`

// RouteWait is a route the connector could not make a worktree on, as status
// and doctor show it. Reason is the failure as the connector logged it, with
// the connector's own paths and environment already taken out of it.
type RouteWait struct {
	Route    string    `json:"route"`
	Failures int       `json:"failures"`
	Reason   string    `json:"reason,omitempty"`
	FirstAt  time.Time `json:"first_failed_at"`
	LastAt   time.Time `json:"last_failed_at"`
	Until    time.Time `json:"next_attempt_at"`
}

// Waiting is whether the route is still inside its backoff at now: the
// dispatcher leaves its records out while it is.
func (w RouteWait) Waiting(now time.Time) bool { return now.Before(w.Until) }

// RecordRouteWait writes the wait a failed Prepare left. FirstAt is kept from
// the row already there: a route that has been failing for six hours says so,
// rather than looking like it started failing on the last attempt.
func (l *Ledger) RecordRouteWait(ctx context.Context, w RouteWait) error {
	if w.Route == "" {
		return fmt.Errorf("connector: a route wait needs its route")
	}
	if w.Failures < 1 {
		w.Failures = 1
	}
	_, err := l.db.ExecContext(ctx, `
INSERT INTO route_waits (route, failures, reason, first_at, last_at, until)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (route) DO UPDATE SET
  failures = excluded.failures,
  reason   = excluded.reason,
  last_at  = excluded.last_at,
  until    = excluded.until`,
		w.Route, w.Failures, w.Reason, stamp(w.FirstAt), stamp(w.LastAt), stamp(w.Until))
	if err != nil {
		return fmt.Errorf("connector: record the wait on route %s: %w", w.Route, err)
	}
	return nil
}

// ClearRouteWait forgets a route's wait. A worktree was made, which is the
// only proof the route works that the connector ever has.
func (l *Ledger) ClearRouteWait(ctx context.Context, route string) error {
	if _, err := l.db.ExecContext(ctx, `DELETE FROM route_waits WHERE route = ?`, route); err != nil {
		return fmt.Errorf("connector: clear the wait on route %s: %w", route, err)
	}
	return nil
}

// RouteWaits is every route whose last worktree attempt failed, oldest
// failure first.
func (l *Ledger) RouteWaits(ctx context.Context) ([]RouteWait, error) {
	return readRouteWaits(ctx, l.db)
}

// rowsQuerier is a ledger connection or a transaction on one.
type rowsQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func readRouteWaits(ctx context.Context, q rowsQuerier) ([]RouteWait, error) {
	rows, err := q.QueryContext(ctx, `SELECT route, failures, reason, first_at, last_at, until FROM route_waits ORDER BY first_at, route`)
	if err != nil {
		return nil, fmt.Errorf("connector: read route waits: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []RouteWait{}
	for rows.Next() {
		var (
			w                  RouteWait
			first, last, until string
		)
		if err := rows.Scan(&w.Route, &w.Failures, &w.Reason, &first, &last, &until); err != nil {
			return nil, err
		}
		for _, f := range []struct {
			text string
			at   *time.Time
		}{{first, &w.FirstAt}, {last, &w.LastAt}, {until, &w.Until}} {
			if *f.at, err = parseStamp(f.text); err != nil {
				return nil, err
			}
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
