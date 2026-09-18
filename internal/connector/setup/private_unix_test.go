//go:build unix

package setup

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

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

func TestLockRefusesASecondSetup(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	unlock, err := Lock(path)
	require.NoError(t, err)

	_, err = Lock(path)
	assert.Error(t, err)

	unlock()
	unlockAgain, err := Lock(path)
	require.NoError(t, err)
	unlockAgain()
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
