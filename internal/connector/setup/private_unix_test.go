//go:build unix

package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func savedPath(t *testing.T) string {
	t.Helper()
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	require.NoError(t, save(path, validFile(t)))
	return path
}

func TestSaveIsOwnerOnly(t *testing.T) {
	cfg := configDir(t)
	path, err := Path(cfg, "agent")
	require.NoError(t, err)
	file := validFile(t)
	require.NoError(t, os.Remove(cfg)) // Save creates the config directory too

	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })
	require.NoError(t, save(path, file))

	for _, p := range []string{cfg, filepath.Dir(filepath.Dir(path)), filepath.Dir(path)} {
		info, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), p)
	}
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temporary file is left behind")
}

// connect.json is the trust anchor: whoever can write it chooses whom the
// agent obeys and where its work runs. A copy someone else could have
// written is refused on read, and never silently replaced on write.
func TestLoadRefusesAFileOthersCanWrite(t *testing.T) {
	for _, mode := range []os.FileMode{0o620, 0o602, 0o666} {
		path := savedPath(t)
		require.NoError(t, os.Chmod(path, mode))

		_, err := Load(path)
		require.Error(t, err, "mode %04o", mode)
		assert.True(t, errors.Is(err, ErrNotPrivate), "mode %04o: %v", mode, err)

		err = save(path, validFile(t))
		assert.True(t, errors.Is(err, ErrNotPrivate), "Save over mode %04o: %v", mode, err)
	}
}

func TestLoadAcceptsAFileOthersCanOnlyRead(t *testing.T) {
	path := savedPath(t)
	require.NoError(t, os.Chmod(path, 0o644))
	_, err := Load(path)
	assert.NoError(t, err)
}

func TestLoadRefusesADirectoryOthersCanWrite(t *testing.T) {
	for _, level := range []int{1, 2, 3} {
		path := savedPath(t)
		dir := path
		for range level {
			dir = filepath.Dir(dir)
		}
		require.NoError(t, os.Chmod(dir, 0o770))

		_, err := Load(path)
		assert.True(t, errors.Is(err, ErrNotPrivate), "%s: %v", dir, err)
		assert.True(t, errors.Is(save(path, validFile(t)), ErrNotPrivate), dir)
	}
}

func TestLoadRefusesASymlink(t *testing.T) {
	path := savedPath(t)
	elsewhere := filepath.Join(t.TempDir(), "connect.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(elsewhere, data, 0o600))
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Symlink(elsewhere, path))

	_, err = Load(path)
	assert.True(t, errors.Is(err, ErrNotPrivate), "%v", err)
	assert.True(t, errors.Is(save(path, validFile(t)), ErrNotPrivate))
}

func TestLoadRefusesASymlinkedProfileDirectory(t *testing.T) {
	path := savedPath(t)
	profileDir := filepath.Dir(path)
	moved := filepath.Join(t.TempDir(), "agent")
	require.NoError(t, os.Rename(profileDir, moved))
	require.NoError(t, os.Symlink(moved, profileDir))

	_, err := Load(path)
	assert.True(t, errors.Is(err, ErrNotPrivate), "%v", err)
}

func TestLoadRefusesANonRegularFile(t *testing.T) {
	path := savedPath(t)
	require.NoError(t, os.Remove(path))
	require.NoError(t, syscall.Mkfifo(path, 0o600))

	_, err := Load(path)
	assert.True(t, errors.Is(err, ErrNotPrivate), "a FIFO is refused without blocking: %v", err)
}

// The trust anchor is only as private as the path to it: an ancestor
// another user can write lets them rename the config directory away and put
// their own in its place, whatever the modes inside say.
func TestLoadRefusesAnAncestorOthersCanWrite(t *testing.T) {
	shared := t.TempDir()
	cfg := filepath.Join(shared, "home", "basecamp")
	require.NoError(t, os.MkdirAll(cfg, 0o700))
	path, err := Path(cfg, "agent")
	require.NoError(t, err)
	require.NoError(t, save(path, validFile(t)))

	require.NoError(t, os.Chmod(filepath.Join(shared, "home"), 0o777))
	_, err = Load(path)
	assert.True(t, errors.Is(err, ErrNotPrivate), "%v", err)
	assert.True(t, errors.Is(save(path, validFile(t)), ErrNotPrivate))

	// A sticky shared directory, as /tmp is, lets nobody rename another's
	// entry, so it is not a way in.
	require.NoError(t, os.Chmod(filepath.Join(shared, "home"), 0o777|os.ModeSticky))
	_, err = Load(path)
	assert.NoError(t, err)
}

func TestLoadRefusesASymlinkedConfigDirectory(t *testing.T) {
	path := savedPath(t)
	cfg := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	moved := filepath.Join(t.TempDir(), "basecamp")
	require.NoError(t, os.Rename(cfg, moved))
	require.NoError(t, os.Symlink(moved, cfg))

	_, err := Load(path)
	assert.True(t, errors.Is(err, ErrNotPrivate), "%v", err)
}

// An ancestor symlink this user owns (a home directory reached through a
// link, /var on macOS) is followed, and where it leads is checked too.
func TestLoadFollowsAnOwnAncestorSymlinkAndChecksItsTarget(t *testing.T) {
	target := t.TempDir()
	cfg := filepath.Join(target, "basecamp")
	require.NoError(t, os.MkdirAll(cfg, 0o700))
	link := filepath.Join(t.TempDir(), "home")
	require.NoError(t, os.Symlink(target, link))

	path, err := Path(filepath.Join(link, "basecamp"), "agent")
	require.NoError(t, err)
	require.NoError(t, save(path, validFile(t)))
	_, err = Load(path)
	require.NoError(t, err)

	require.NoError(t, os.Chmod(target, 0o777))
	_, err = Load(path)
	assert.True(t, errors.Is(err, ErrNotPrivate), "the target's modes count: %v", err)
}

// shortLockWait keeps a test that means to see contention from sitting out
// the whole of LockWait to see it.
func shortLockWait(t *testing.T) {
	t.Helper()
	was := LockWait
	LockWait = 20 * time.Millisecond
	t.Cleanup(func() { LockWait = was })
}

func TestLockRefusesASecondSetup(t *testing.T) {
	shortLockWait(t)
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	unlock, err := Lock(context.Background(), path)
	require.NoError(t, err)

	_, err = Lock(context.Background(), path)
	assert.ErrorIs(t, err, ErrSetupRunning)

	unlock()
	unlockAgain, err := Lock(context.Background(), path)
	require.NoError(t, err)
	unlockAgain()
}

// The dispatcher's form: it never waits, so a pass that overlaps a setup
// gives up its turn instead of parking on a file another process writes.
func TestTryLockDoesNotWaitForAHolder(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	unlock, err := TryLock(path)
	require.NoError(t, err)

	// LockWait is left long on purpose: if TryLock ever waited, this would
	// take it rather than return inside the window asserted below.
	start := time.Now()
	_, err = TryLock(path)
	assert.ErrorIs(t, err, ErrSetupRunning)
	assert.Less(t, time.Since(start), LockWait/2, "TryLock returns rather than waits")

	unlock()
	again, err := TryLock(path)
	require.NoError(t, err)
	again()
}

// Lock waits for a holder that finishes inside LockWait, so a `connect
// setup` does not fail because a dispatcher pass happened to overlap it.
func TestLockWaitsForAHolderThatFinishes(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	unlock, err := TryLock(path)
	require.NoError(t, err)

	released := make(chan struct{})
	go func() {
		time.Sleep(5 * lockPoll)
		unlock()
		close(released)
	}()

	got, err := Lock(context.Background(), path)
	require.NoError(t, err, "the wait outlasts a holder that lets go")
	<-released
	got()
}

func TestCheckPrivateFileCreatesNothingAndHoldsTheRules(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.Mkdir(dir, 0o700))
	path := filepath.Join(dir, "ledger.db")

	err := CheckPrivateFile(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, statErr := os.Lstat(path)
	require.ErrorIs(t, statErr, os.ErrNotExist, "nothing was created")

	require.NoError(t, os.WriteFile(path, nil, 0o600))
	require.NoError(t, CheckPrivateFile(path))

	require.NoError(t, os.Chmod(path, 0o644))
	require.ErrorIs(t, CheckPrivateFile(path), ErrNotPrivate)
	require.NoError(t, os.Chmod(path, 0o600))

	link := filepath.Join(dir, "link.db")
	require.NoError(t, os.Symlink(path, link))
	require.Error(t, CheckPrivateFile(link))

	require.NoError(t, os.Chmod(dir, 0o775))
	require.ErrorIs(t, CheckPrivateFile(path), ErrNotPrivate, "a directory others can write")
}

// A wait that is canceled stops waiting and takes nothing. An operator who
// presses Ctrl-C during contention should not sit out LockWait, and a
// command whose context has already ended must not come away holding the
// lock and act under it: "the policy could not be checked" is a refusal, not
// permission (Copilot on #771).
func TestLockStopsWaitingWhenTheContextEnds(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	unlock, err := TryLock(path)
	require.NoError(t, err)
	t.Cleanup(unlock)

	// LockWait is left at its production value on purpose: a wait that
	// ignored the context would take it, and this would take that long.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * lockPoll)
		cancel()
	}()

	start := time.Now()
	held, err := Lock(ctx, path)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, held, "a canceled wait comes away with nothing to release")
	assert.Less(t, time.Since(start), LockWait/2, "and it stops when the context does, not when the bound expires")
}

// A context that has already ended takes the lock from nobody, even when the
// lock is free: the caller is not going to act on what it reads.
func TestLockRefusesAnAlreadyEndedContext(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	held, err := Lock(ctx, path)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, held)

	// Proof that it refused rather than failed to acquire: the lock is free.
	free, err := TryLock(path)
	require.NoError(t, err)
	free()
}

// endsWhileAcquiring is a context that is live when the wait looks before it
// tries, and over by the time it looks again with the lock in hand. It is
// the race a real cancellation runs — Ctrl-C landing in the microseconds
// between the check and the flock — scheduled rather than hoped for, since
// nothing else in a test can place a signal inside that window.
type endsWhileAcquiring struct {
	context.Context
	looks int
}

func (c *endsWhileAcquiring) Err() error {
	c.looks++
	if c.looks == 1 {
		return nil
	}
	return context.Canceled
}

// A context that ends while the lock is being acquired must not come away
// holding it: the caller is no longer entitled to act, and a lock handed to
// somebody who will not use it is a lock nobody else can take until the
// process exits (Codex on #771).
func TestLockGivesBackALockItAcquiredForAnEndedContext(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)

	ctx := &endsWhileAcquiring{Context: context.Background()}
	held, err := Lock(ctx, path)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, held)
	require.Equal(t, 2, ctx.looks, "the wait looked before it tried and again once it held")

	free, err := TryLock(path)
	require.NoError(t, err, "and gave back what it had taken: nothing is left holding it")
	free()
}

// The simpler half: a context that was already over before the call takes
// the lock from nobody at all.
func TestLockRefusesAnEndedContextWithoutTakingTheLock(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	held, err := Lock(ctx, path)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, held)

	free, err := TryLock(path)
	require.NoError(t, err)
	free()
}

// A wait that runs out at the same moment its context ends reports the
// interruption rather than the holder: "somebody else has it" sends a person
// back to try again, and they stopped on purpose.
func TestAnExpiredWaitOnAnEndedContextReportsTheInterruption(t *testing.T) {
	was := LockWait
	LockWait = 0
	t.Cleanup(func() { LockWait = was })

	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	unlock, err := TryLock(path)
	require.NoError(t, err)
	t.Cleanup(unlock)

	// Live when the wait looks before trying, over by the time the bound
	// has run out — so the two verdicts are available at the same moment
	// and the interruption is the one reported.
	ctx := &endsWhileAcquiring{Context: context.Background()}
	_, err = Lock(ctx, path)
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, ErrSetupRunning, "a person who stopped the command is not told to wait for somebody else")
}
