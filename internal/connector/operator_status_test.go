package connector

import (
	"context"
	"encoding/json"
	"errors"
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
	require.NoError(t, l.NoteConnection(ctx, ConnectionRunning, ""))

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
	assert.Equal(t, ConnectionRunning, s.Connection.State)
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

// A ledger a newer build wrote is not this build's to read or decide in.
func TestOpenLedgerReadOnlyRefusesANewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", LedgerFile)
	l, err := OpenLedger(path)
	require.NoError(t, err)
	_, err = l.db.ExecContext(context.Background(), `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, len(migrations)+1, stamp(time.Now()))
	require.NoError(t, err)
	require.NoError(t, l.Close())

	_, err = OpenLedgerReadOnly(context.Background(), path)
	require.ErrorIs(t, err, ErrLedgerSchema)
	assert.NotErrorIs(t, err, ErrLedgerOutOfDate)
}

// The connector's own open refuses a ledger a newer build wrote too: an older
// binary rolled back onto it must not run over rules it does not know.
func TestOpenLedgerRefusesANewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", LedgerFile)
	l, err := OpenLedger(path)
	require.NoError(t, err)
	_, err = l.db.ExecContext(context.Background(), `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, len(migrations)+1, stamp(time.Now()))
	require.NoError(t, err)
	require.NoError(t, l.Close())

	_, err = OpenLedger(path)
	require.ErrorIs(t, err, ErrLedgerSchema)
}

// Status reports the retained worktrees the lister gives it. The lister card
// 19's ledger provides reads the same ledger, which holds one connection, so
// a listing made inside status's own read transaction would wait for the
// connection that transaction holds.
func TestStatusListsTheRetainedWorktrees(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	l, err := OpenLedger(filepath.Join(t.TempDir(), "state", LedgerFile))
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	id, err := l.BeginWorktree(ctx, Worktree{
		Path: "/w/one", WorkDir: "/w/one/app", Route: "app",
		Repository: "/repo", Branch: "basecamp-connect/1-a1b2c3", BaseCommit: "abc",
	})
	require.NoError(t, err)
	require.NoError(t, l.MoveWorktree(ctx, id, WorktreeLive, WorktreeCreating))
	require.NoError(t, l.RetainWorktree(ctx, id, RetainedDirty, WorktreeLive))

	s, err := l.Status(ctx, l.RetainedWorktreeStatus)
	require.NoError(t, err)
	assert.True(t, s.WorktreesKnown, "a status given a lister says what it knows")
	assert.Empty(t, s.WorktreesUnavailable)
	require.Len(t, s.Worktrees, 1)
	assert.Equal(t, WorktreeStatus{Path: "/w/one", Branch: "basecamp-connect/1-a1b2c3", Reason: "dirty"}, s.Worktrees[0])
}

// A listing that fails leaves the retained worktrees unavailable, with why:
// the rest of the status is still worth reading, and "unavailable" is never
// read as "none".
func TestStatusReportsAWorktreeListingThatFailedAsUnavailable(t *testing.T) {
	ctx := context.Background()
	l, err := OpenLedger(filepath.Join(t.TempDir(), "state", LedgerFile))
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	s, err := l.Status(ctx, func(context.Context) ([]WorktreeStatus, error) {
		return nil, errors.New("the worktrees table cannot be read")
	})
	require.NoError(t, err, "one unreadable listing does not blank the whole status")
	assert.False(t, s.WorktreesKnown)
	assert.Contains(t, s.WorktreesUnavailable, "the worktrees table cannot be read")
	assert.Empty(t, s.Worktrees)
}

// Every process read back out of the ledger is an identity the one-owner
// rule will answer about. The stamp stored is the kernel's — startedStamp
// writes no other — so each read-back path marks it exact. A path that
// rebuilds driver.Process by hand loses that bit, and the rule then calls a
// live worker neither gone nor running (driver.ErrIdentityUnknown): status
// reported every worker "unverified" and a redispatch refused to stop the
// worker it replaced.
func TestEveryProcessReadBackOutOfTheLedgerIsAnIdentity(t *testing.T) {
	at := time.Now().UTC()
	assert.True(t, AttemptProcess{PID: 7, PGID: 7, StartedAt: at, StartedExact: true}.Identity().StartedExact)
	assert.True(t, LiveWorker{PID: 7, PGID: 7, StartedAt: at}.Identity().StartedExact)
	assert.True(t, TaskStatus{PID: 7, PGID: 7, ProcessStartedAt: &at}.WorkerIdentity().StartedExact)
	assert.True(t, TaskStatus{TakerPID: 7, TakerPGID: 7, TakerStartedAt: &at}.TakerIdentity().StartedExact)

	// And a record with no stamp is no identity at all, rather than one the
	// rule would answer about.
	assert.False(t, AttemptProcess{PID: 7, PGID: 7}.Identity().StartedExact)
	assert.False(t, TaskStatus{PID: 7, PGID: 7}.WorkerIdentity().StartedExact)
	assert.False(t, TaskStatus{TakerPID: 7, TakerPGID: 7}.TakerIdentity().StartedExact)
}

// A token whose holder could not be accounted for is held, and status is
// where the person who must settle it reads that. The zero taker cannot say
// it: "nothing took the token" and "the token is out and nobody can name who
// has it" are opposite facts, and the ledger carries the difference the
// release point acts on (TokenHolder.Unaccounted) all the way out.
func TestStatusCarriesATokenHolderThatCannotBeAccountedFor(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	opAdmit(t, l, 1, "recording:1")
	launch := launchOf(t, l, 1)
	require.NoError(t, l.MarkRunning(ctx, launch.AttemptID, AttemptProcess{PID: 4242, PGID: 4242, StartedAt: time.Now(), StartedExact: true}))

	before, err := l.Status(ctx, nil)
	require.NoError(t, err)
	require.Len(t, before.Tasks, 1)
	assert.False(t, before.Tasks[0].TakerUnaccounted, "nothing has taken the token yet")

	require.NoError(t, l.MarkTakerUnaccounted(ctx, launch.AttemptID))
	after, err := l.Status(ctx, nil)
	require.NoError(t, err)
	require.Len(t, after.Tasks, 1)
	assert.True(t, after.Tasks[0].TakerUnaccounted, "the held attempt says its token is out")
	assert.Zero(t, after.Tasks[0].TakerPID, "and there is no process to name")
}
