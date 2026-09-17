package connector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Invariant 8: status reads beside a writer, writes nothing, and shows no
// content, position or token.
func TestInvariant8StatusReadsBesideAWriterAndShowsNoSecrets(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", LedgerFile)
	l, err := OpenLedger(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	l.SetHooks(LifecycleHooks(l, LifecycleOptions{}))

	const position = "signed-position-not-real-7f3a"
	require.NoError(t, l.Save(ctx, testKey(), position))
	require.NoError(t, l.NotePollServed(ctx, testKey(), 41))
	require.NoError(t, l.NoteConnection(ctx, ConnectionConnected, "streaming"))

	unknownOutcome(t, l, 2)
	opAdmit(t, l, 1, "recording:1")
	launch := launchOf(t, l, 1)
	require.NoError(t, l.MarkRunning(ctx, launch.AttemptID, AttemptProcess{PID: 4242, PGID: 4242, StartedAt: time.Now()}))
	seenRecord(t, l, 5)
	_, err = l.Admission().Commit(ctx, blockedVerdict(5, 0, "read_failed"))
	require.NoError(t, err)
	_, err = l.db.ExecContext(context.Background(), `UPDATE outbox SET state = 'sending', sending_at = ? WHERE event_id = 1`, stamp(time.Now()))
	require.NoError(t, err)
	_, err = l.db.ExecContext(context.Background(), `UPDATE outbox SET state = 'indeterminate', note = 'two candidates' WHERE event_id = 1`)
	require.NoError(t, err)
	l.SetHooks(Hooks{})
	opAdmit(t, l, 3, "recording:3")
	seenRecord(t, l, 4)
	_, err = l.SetHold(ctx, opBy, HoldByOperator)
	require.NoError(t, err)

	// A writer holds the write lock while status reads.
	writer, err := l.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = writer.ExecContext(ctx, `UPDATE events SET updated_at = updated_at WHERE id = 4`)
	require.NoError(t, err)
	defer func() { _ = writer.Rollback() }()

	reader, err := OpenLedgerReadOnly(context.Background(), path)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	s, err := reader.Status(ctx, nil)
	require.NoError(t, err)

	require.NotNil(t, s.Hold)
	assert.Equal(t, "hold", s.Hold.Cause)
	require.NotNil(t, s.Connection)
	assert.Equal(t, ConnectionConnected, s.Connection.State)
	require.Len(t, s.Positions, 1)
	assert.True(t, s.Positions[0].HasPosition)
	assert.Equal(t, int64(41), s.Positions[0].LastPollServedID)
	require.Len(t, s.Tasks, 1)
	assert.Equal(t, launch.TaskID, s.Tasks[0].TaskID)
	assert.Equal(t, 4242, s.Tasks[0].PID)
	require.Len(t, s.Held, 1)
	assert.Equal(t, int64(3), s.Held[0].EventID)
	assert.Equal(t, 1, s.Queues["held"])
	assert.Equal(t, 1, s.Queues["seen"])
	assert.Equal(t, 3, s.Review, "the seen, the blocked and the dispatched record wait for review")
	assert.Equal(t, map[string]int{"read_failed": 1}, s.Blocked)
	require.Len(t, s.Indeterminate, 1)
	assert.Equal(t, int64(1), s.Indeterminate[0].EventID)
	require.Len(t, s.Dispatches, 2)
	assert.False(t, s.WorktreesKnown)

	raw, err := json.Marshal(s)
	require.NoError(t, err)
	out := string(raw)
	assert.NotContains(t, out, position)
	assert.NotContains(t, out, "please look", "no snapshot content")
	assert.NotContains(t, out, tokenHash(launch.Token))
	assert.NotContains(t, out, launch.Token)
}

func TestOpenLedgerReadOnlyCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	_, err := OpenLedgerReadOnly(context.Background(), filepath.Join(dir, LedgerFile))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Lstat(dir)
	assert.ErrorIs(t, err, os.ErrNotExist)

	l, err := OpenLedger(filepath.Join(dir, "other.db"))
	require.NoError(t, err)
	require.NoError(t, l.Close())
	_, err = OpenLedgerReadOnly(context.Background(), filepath.Join(dir, LedgerFile))
	require.ErrorIs(t, err, os.ErrNotExist, "a ledger gone from a private directory")
	_, err = os.Lstat(filepath.Join(dir, LedgerFile))
	assert.ErrorIs(t, err, os.ErrNotExist, "is not recreated by a reader")

	l, err = OpenLedger(filepath.Join(dir, LedgerFile))
	require.NoError(t, err)
	require.NoError(t, l.Close())
	reader, err := OpenLedgerReadOnly(context.Background(), filepath.Join(dir, LedgerFile))
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	_, err = reader.db.ExecContext(context.Background(), `DELETE FROM events`)
	assert.Error(t, err, "a read-only ledger refuses writes")
}
