package connector

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// migrationsBeforeWorktreesDropped is the last migration a ledger that still
// had a worktrees table had applied. Migration 12 drops the table, so a test
// that wants a row in it stops one short.
const migrationsBeforeWorktreesDropped = 11

// migrationsBeforeRoutePathsDropped is the last migration a ledger that still
// carried events.routed, events.route, tasks.route and tasks.work_dir had
// applied. Migration 13 renames the first and drops the rest.
const migrationsBeforeRoutePathsDropped = 12

// applyMigrationsThrough opens a raw database at path and applies the first n
// migrations, recording each, as an older build would have left it. It leaves
// the handle open for the caller to seed rows through.
func applyMigrationsThrough(t *testing.T, path string, n int) *sql.DB {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	old, err := sql.Open("sqlite", ledgerDSN(path, true))
	require.NoError(t, err)
	_, err = old.ExecContext(context.Background(), `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`)
	require.NoError(t, err)
	for i := range n {
		_, err = old.ExecContext(context.Background(), migrations[i])
		require.NoError(t, err, "migration %d", i+1)
		_, err = old.ExecContext(context.Background(), `INSERT INTO schema_migrations (version, applied_at) VALUES (?, 'then')`, i+1)
		require.NoError(t, err)
	}
	return old
}

// openUpgraded makes the file private, as the connector's own open would,
// then opens it through OpenLedger so the remaining migrations run.
func openUpgraded(t *testing.T, path string) *Ledger {
	t.Helper()
	// The connector's own open makes the file private; a raw sql.Open does
	// not, and the privacy check refuses what it finds.
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(name); err == nil {
			require.NoError(t, os.Chmod(name, 0o600))
		}
	}
	ledger, err := OpenLedger(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ledger.Close() })
	version, err := ledger.SchemaVersion(context.Background())
	require.NoError(t, err)
	assert.Equal(t, len(migrations), version, "the upgrade ran")
	return ledger
}

// Worktrees are gone, and the ledger forgets them: migration 12 drops the
// table migration 9 made. Migration 9 itself stays in the list, unedited —
// a ledger that has applied it never sees it renumbered, and a fresh ledger
// walks the same numbers to reach 12 — so the shape this proves is "made,
// then dropped", on a ledger that really did run 9.
func TestTheWorktreesTableIsDroppedFromALedgerThatHadOne(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "connector.db")
	old := applyMigrationsThrough(t, path, migrationsBeforeWorktreesDropped)
	// A ledger with a worktree in it, as one that ran with them on would have.
	_, err := old.ExecContext(ctx, `
INSERT INTO worktrees (path, work_dir, route, repository, branch, base_commit, originating_event_id, state, retained_reason, created_at, retained_at)
VALUES ('/w/one', '/w/one/app', '/repo/app', '/repo', 'basecamp-connect/1-a1b2c3', 'abc', 1, 'retained', 'dirty', 'then', 'then')`)
	require.NoError(t, err, "migration 9 made the table this row goes in")
	require.NoError(t, old.Close())

	ledger := openUpgraded(t, path)

	var left int
	require.NoError(t, ledger.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'worktrees%'`).Scan(&left))
	assert.Zero(t, left, "the table, its indexes and its trigger are all gone")
}

// No directory is associated with a project any more, and the ledger stops
// holding one: migration 13 renames events.routed to served — the value it
// always held — and drops the three columns that named a path, with the
// unique index over the last of them.
//
// The rename is the part that has to keep a value. A record blocked no_route
// on a ledger written by an older build is still blocked no_route after the
// upgrade, and one admitted in a served project is still served, or the
// holding replies and their retractions would answer for the wrong records.
func TestTheRoutePathsAreDroppedFromALedgerThatHadThem(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "connector.db")
	old := applyMigrationsThrough(t, path, migrationsBeforeRoutePathsDropped)

	// Two records an older build wrote: one in a routed project, one blocked
	// for want of a route.
	_, err := old.ExecContext(ctx, `
INSERT INTO events (id, state, reason, lane, event_type, kind, action, bucket_id, creator_id, recording_id,
                    created_at, seen_at, updated_at, routed, route, conversation_key)
VALUES (1, 'admitted', '', 'import', 'comment.created', '', '', 48699913, 26909558, 10304028972,
        ?1, ?1, ?1, 1, '/work/app', 'recording:1'),
       (2, 'blocked', 'no_route', 'import', 'comment.created', '', '', 777, 26909558, 10304028973,
        ?1, ?1, ?1, 0, '', '')`, "2026-09-17T11:00:00.000000000Z")
	require.NoError(t, err, "migration 4 made the columns these rows use")
	_, err = old.ExecContext(ctx, `
INSERT INTO tasks (token_sha256, created_at, conversation_key, route, work_dir, driver)
VALUES ('abc', '2026-09-17T11:00:00.000000000Z', 'recording:1', '/work/app', '/work/app/wt', 'claude')`)
	require.NoError(t, err, "migration 7 made the columns this row uses")
	require.NoError(t, old.Close())

	ledger := openUpgraded(t, path)

	var served int
	var reason string
	require.NoError(t, ledger.db.QueryRowContext(ctx, `SELECT served, reason FROM events WHERE id = 1`).Scan(&served, &reason))
	assert.Equal(t, 1, served, "a record admitted in a routed project is served")
	require.NoError(t, ledger.db.QueryRowContext(ctx, `SELECT served, reason FROM events WHERE id = 2`).Scan(&served, &reason))
	assert.Equal(t, 0, served)
	assert.Equal(t, "no_route", reason, "the reason a holding reply answers for is read as it was written")

	for _, q := range []string{
		`SELECT route FROM events`,
		`SELECT route FROM tasks`,
		`SELECT work_dir FROM tasks`,
		`SELECT routed FROM events`,
	} {
		_, err := ledger.db.ExecContext(ctx, q)
		assert.Error(t, err, q)
	}

	var index int
	require.NoError(t, ledger.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name = 'tasks_live_work_dir'`).Scan(&index))
	assert.Zero(t, index, "the index that would have admitted one live task on the whole machine")

	// And the rows themselves are still there, read through the ledger.
	record, ok, err := ledger.Get(ctx, 2)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, StateBlocked, record.State)
	assert.False(t, record.Decision.Served)
}
