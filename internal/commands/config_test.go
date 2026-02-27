package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAtomicWriteFile_OverwriteExisting(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	// Create initial file
	require.NoError(t, atomicWriteFile(path, []byte(`{"v":1}`)))

	// Overwrite (exercises the Windows pre-remove path)
	require.NoError(t, atomicWriteFile(path, []byte(`{"v":2}`)),
		"overwrite of existing file must succeed")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, `{"v":2}`, string(data))
}

func TestAtomicWriteFile_Permissions(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "secret.json")

	require.NoError(t, atomicWriteFile(path, []byte(`{}`)))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm(),
		"file should have restricted permissions")
}

func TestAtomicWriteFile_NoStaleTempFiles(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	require.NoError(t, atomicWriteFile(path, []byte(`{}`)))

	entries, err := os.ReadDir(tmpDir)
	require.NoError(t, err)
	for _, e := range entries {
		if e.Name() != "config.json" {
			t.Errorf("stale temp file left behind: %s", e.Name())
		}
	}
}
