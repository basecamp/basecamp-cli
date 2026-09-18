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

// Worktrees are gone, and the ledger forgets them: migration 12 drops the
// table migration 9 made. Migration 9 itself stays in the list, unedited —
// a ledger that has applied it never sees it renumbered, and a fresh ledger
// walks the same numbers to reach 12 — so the shape this proves is "made,
// then dropped", on a ledger that really did run 9.
func TestTheWorktreesTableIsDroppedFromALedgerThatHadOne(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "connector.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))

	old, err := sql.Open("sqlite", ledgerDSN(path, true))
	require.NoError(t, err)
	_, err = old.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`)
	require.NoError(t, err)
	for i := range len(migrations) - 1 {
		_, err = old.ExecContext(ctx, migrations[i])
		require.NoError(t, err, "migration %d", i+1)
		_, err = old.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, 'then')`, i+1)
		require.NoError(t, err)
	}
	// A ledger with a worktree in it, as one that ran with them on would have.
	_, err = old.ExecContext(ctx, `
INSERT INTO worktrees (path, work_dir, route, repository, branch, base_commit, originating_event_id, state, retained_reason, created_at, retained_at)
VALUES ('/w/one', '/w/one/app', '/repo/app', '/repo', 'basecamp-connect/1-a1b2c3', 'abc', 1, 'retained', 'dirty', 'then', 'then')`)
	require.NoError(t, err, "migration 9 made the table this row goes in")
	require.NoError(t, old.Close())

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
	version, err := ledger.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, len(migrations), version, "the upgrade ran")

	var left int
	require.NoError(t, ledger.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'worktrees%'`).Scan(&left))
	assert.Zero(t, left, "the table, its indexes and its trigger are all gone")
}
