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
	require.NoError(t, Save(path, validFile(t)))
	return path
}

func TestSaveIsOwnerOnly(t *testing.T) {
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })

	path := savedPath(t)
	for _, p := range []string{filepath.Dir(filepath.Dir(path)), filepath.Dir(path)} {
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

		err = Save(path, validFile(t))
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
	for _, level := range []int{1, 2} {
		path := savedPath(t)
		dir := path
		for range level {
			dir = filepath.Dir(dir)
		}
		require.NoError(t, os.Chmod(dir, 0o770))

		_, err := Load(path)
		assert.True(t, errors.Is(err, ErrNotPrivate), "%s: %v", dir, err)
		assert.True(t, errors.Is(Save(path, validFile(t)), ErrNotPrivate), dir)
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
	assert.True(t, errors.Is(Save(path, validFile(t)), ErrNotPrivate))
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
