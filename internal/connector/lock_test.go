package connector

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

// Two connectors on one agent identity dispatch every mention twice. The Ruby
// connector's own notes record that happening.
func TestSecondConnectorOnTheSameAgentIsRefused(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	first, err := AcquireInstanceLock(dir, "2914079", 52007412, now)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Release() })

	_, err = AcquireInstanceLock(dir, "2914079", 52007412, now)
	require.ErrorIs(t, err, ErrAlreadyRunning)
	assert.Contains(t, err.Error(), "pid", "the refusal says who is holding it")
}

// The key is the identity, not the profile that names it: two profiles can
// hold credentials for one agent, and it is the agent that gets double-served.
func TestTheLockIsKeyedOnAccountAndAgent(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	first, err := AcquireInstanceLock(dir, "2914079", 52007412, now)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Release() })

	otherAgent, err := AcquireInstanceLock(dir, "2914079", 26909558, now)
	require.NoError(t, err, "a different agent in the same account is a different connector")
	t.Cleanup(func() { _ = otherAgent.Release() })

	otherAccount, err := AcquireInstanceLock(dir, "5951425", 52007412, now)
	require.NoError(t, err, "the same agent id in another account is a different connector")
	t.Cleanup(func() { _ = otherAccount.Release() })
}

func TestReleasedLockCanBeRetaken(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	first, err := AcquireInstanceLock(dir, "2914079", 52007412, now)
	require.NoError(t, err)
	require.NoError(t, first.Release())

	second, err := AcquireInstanceLock(dir, "2914079", 52007412, now)
	require.NoError(t, err)
	require.NoError(t, second.Release())
}

func TestLockRefusesAnIncompleteIdentity(t *testing.T) {
	dir := t.TempDir()
	_, err := AcquireInstanceLock(dir, "", 52007412, time.Now())
	assert.Error(t, err)
	_, err = AcquireInstanceLock(dir, "2914079", 0, time.Now())
	assert.Error(t, err)
}

// A lock in a directory other local users can write is not a lock: they can
// replace the file between two connectors' opens, and each then holds an
// inode the other never sees.
func TestTheLockRefusesADirectoryOthersCanWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.Chmod(dir, 0o770)) // after the umask, not through it

	_, err := AcquireInstanceLock(dir, "2914079", 52007412, time.Now())

	require.Error(t, err)
	assert.ErrorIs(t, err, setup.ErrNotPrivate)
	assert.NotErrorIs(t, err, ErrAlreadyRunning, "a directory this connector will not use is not a second connector")
}

// A lock path that is a symlink locks whatever it points at, so two
// connectors pointed at two symlinks exclude nobody.
func TestTheLockRefusesASymlinkedLockPath(t *testing.T) {
	dir := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.lock")
	require.NoError(t, os.WriteFile(elsewhere, nil, 0o600))
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(dir, "instance-2914079-52007412.lock")))

	_, err := AcquireInstanceLock(dir, "2914079", 52007412, time.Now())

	require.Error(t, err)
	assert.ErrorIs(t, err, setup.ErrNotPrivate)
}

// The directory is created when it is missing, and created owner-only.
func TestTheLockCreatesItsDirectoryOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")

	lock, err := AcquireInstanceLock(dir, "2914079", 52007412, time.Now())
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Release() })

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	file, err := os.Stat(lock.Path())
	require.NoError(t, err)
	assert.Zero(t, file.Mode().Perm()&0o077, "the lock file is the owner's alone")
}

// A lock that cannot be taken is a refusal, never a connector running
// unlocked.
func TestTheLockRefusesWhenItCannotBeEstablished(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent", "state")

	_, err := AcquireInstanceLock(missing, "2914079", 52007412, time.Now())

	require.Error(t, err)
	assert.True(t, errors.Is(err, setup.ErrNotPrivate) || errors.Is(err, os.ErrNotExist), "got %v", err)
}
