package commands

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

// Workers run in the folder the connector starts in and may change files
// there without asking, so starting from the home folder is a warning.
func TestWorkFolderCheckWarnsInTheHomeFolder(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Chdir(home)

	c := workFolderCheck()
	assert.Equal(t, setup.StatusWarn, c.Status)
	assert.Contains(t, c.Message, "your home folder")
	assert.NotEmpty(t, c.Hint)
}

func TestWorkFolderCheckPassesInAFolderOfItsOwn(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	work := filepath.Join(home, "agent")
	require.NoError(t, os.Mkdir(work, 0o700))
	t.Chdir(work)

	c := workFolderCheck()
	assert.Equal(t, setup.StatusPass, c.Status)
	assert.Contains(t, c.Message, "agent")
	assert.NotContains(t, c.Message, "home folder")
}

// A symlink to the home folder is still the home folder.
func TestWorkFolderCheckSeesThroughASymlinkToHome(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	link := filepath.Join(t.TempDir(), "home-link")
	require.NoError(t, os.Symlink(home, link))
	t.Chdir(link)

	assert.Equal(t, setup.StatusWarn, workFolderCheck().Status)
}

// The filesystem root is a warning too, and says so rather than calling it
// the home folder.
func TestWorkFolderCheckWarnsAtTheFilesystemRoot(t *testing.T) {
	setHome(t, t.TempDir())
	t.Chdir(string(filepath.Separator))

	c := workFolderCheck()
	assert.Equal(t, setup.StatusWarn, c.Status)
	assert.Contains(t, c.Message, "the root of the filesystem")
	assert.NotContains(t, c.Message, "home folder")
}

func TestIsFilesystemRoot(t *testing.T) {
	assert.True(t, isFilesystemRoot(string(filepath.Separator)))
	assert.False(t, isFilesystemRoot(t.TempDir()))
}

// A symlink to the root is the root.
func TestIsFilesystemRootSeesThroughASymlink(t *testing.T) {
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(string(filepath.Separator), link); err != nil {
		t.Skipf("can't make a symlink here: %v", err)
	}
	assert.True(t, isFilesystemRoot(link))
}

// setHome points the home folder at dir, where os.UserHomeDir looks for it on
// every platform: HOME on Unix, USERPROFILE on Windows.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// A folder name can carry newlines and terminal escapes. The check shows the
// path on one line, escaped, and still recognises the folder.
func TestWorkFolderCheckShowsTheFolderOnOneLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows forbids newlines and escapes in folder names")
	}
	setHome(t, t.TempDir())
	odd := filepath.Join(t.TempDir(), "evil\n\x1b[2J✓ fake")
	require.NoError(t, os.Mkdir(odd, 0o700))
	t.Chdir(odd)

	c := workFolderCheck()
	assert.Equal(t, setup.StatusPass, c.Status)
	assert.NotContains(t, c.Message, "\n")
	assert.NotContains(t, c.Message, "\x1b")
}
