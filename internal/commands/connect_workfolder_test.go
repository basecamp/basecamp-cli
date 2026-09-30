package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

// Workers run in the folder the connector starts in and may change files
// there without asking, so starting from the home folder is a warning.
func TestWorkFolderCheckWarnsInTheHomeFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)

	c := workFolderCheck()
	assert.Equal(t, setup.StatusWarn, c.Status)
	assert.Contains(t, c.Message, "your home folder")
	assert.NotEmpty(t, c.Hint)
}

func TestWorkFolderCheckPassesInAFolderOfItsOwn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
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
	t.Setenv("HOME", home)
	link := filepath.Join(t.TempDir(), "home-link")
	require.NoError(t, os.Symlink(home, link))
	t.Chdir(link)

	assert.Equal(t, setup.StatusWarn, workFolderCheck().Status)
}
