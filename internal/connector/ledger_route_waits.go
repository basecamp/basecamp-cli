package connector

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
// One row per route, updated on each failure and deleted the moment a worktree
// is made — and pruned against connect.json's routes, so a route that is no
// longer routed, or routed somewhere else, stops being counted rather than
// standing in status for as long as the ledger does. A row is a record of
// failure, not a schedule: it stands after `until` has passed, because nothing
// has proved the route works since, and the next record on that route is what
// tries again.
//
// The count is the ledger's, not the connector process's. A restart begins its
// own in-memory tally at one, and a row overwritten with that would turn
// fourteen failures since six this morning into one — a smaller, more
// reassuring number than the truth, under a timestamp that says otherwise.
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
//
// Until is the end of the backoff the last failure armed, and nothing more.
// It is not a scheduled attempt: the next attempt happens when a record on
// that route reaches the dispatcher, which may be a second after the backoff
// ends or never. The field is named for what it is, because a consumer told
// it was the next attempt would be told something the connector does not
// promise.
type RouteWait struct {
	Route    string    `json:"route"`
	Failures int       `json:"failures"`
	Reason   string    `json:"reason,omitempty"`
	FirstAt  time.Time `json:"first_failed_at"`
	LastAt   time.Time `json:"last_failed_at"`
	Until    time.Time `json:"backoff_until"`
}

// Waiting is whether the route is still inside its backoff at now: the
// dispatcher leaves its records out while it is. False is a route that is
// startable again, not a route that has been tried again.
func (w RouteWait) Waiting(now time.Time) bool { return now.Before(w.Until) }

// RecordRouteWait writes the wait a failed Prepare left. FirstAt is kept from
// the row already there: a route that has been failing for six hours says so,
// rather than looking like it started failing on the last attempt. So is the
// count, which only ever goes up — a caller's number is a floor, and a row
// that is already there is incremented past it, so a connector that restarted
// into a fresh tally adds to what the ledger knows instead of replacing it.
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
  failures = MAX(route_waits.failures + 1, excluded.failures),
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

// PruneRouteWaits drops every recorded wait whose route is not in keep, and
// returns how many went. An empty keep drops them all.
//
// A wait is about a route the profile is configured for. Once connect.json
// stops naming it — the route changed, the project was unrouted, worktrees
// were turned off altogether — nothing is waiting on it, and a row that
// outlives the condition is worse than no row: silence sends a person
// looking, while a confident line about a route that no longer exists sends
// them nowhere.
func (l *Ledger) PruneRouteWaits(ctx context.Context, keep []string) (int, error) {
	query := `DELETE FROM route_waits`
	args := make([]any, 0, len(keep))
	if len(keep) > 0 {
		//nolint:gosec // G202: what is concatenated is placeholders, never a value
		query += ` WHERE route NOT IN (` + strings.TrimSuffix(strings.Repeat("?, ", len(keep)), ", ") + `)`
		for _, route := range keep {
			args = append(args, route)
		}
	}
	res, err := l.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("connector: prune route waits: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
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
