package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPluginInstalled_ArrayFormat(t *testing.T) {
	data := []byte(`[{"name": "basecamp", "version": "1.0.0"}]`)
	assert.True(t, pluginInstalled(data))
}

func TestPluginInstalled_ArrayFormat_FullQualified(t *testing.T) {
	data := []byte(`[{"package": "basecamp@basecamp", "version": "1.0.0"}]`)
	assert.True(t, pluginInstalled(data))
}

func TestPluginInstalled_ArrayFormat_NotFound(t *testing.T) {
	data := []byte(`[{"name": "other-plugin", "version": "1.0.0"}]`)
	assert.False(t, pluginInstalled(data))
}

func TestPluginInstalled_MapFormat(t *testing.T) {
	data := []byte(`{"basecamp@basecamp": {"version": "1.0.0"}}`)
	assert.True(t, pluginInstalled(data))
}

func TestPluginInstalled_MapFormat_Simple(t *testing.T) {
	data := []byte(`{"basecamp": {"version": "1.0.0"}}`)
	assert.True(t, pluginInstalled(data))
}

func TestPluginInstalled_MapFormat_NotFound(t *testing.T) {
	data := []byte(`{"other-plugin": {"version": "1.0.0"}}`)
	assert.False(t, pluginInstalled(data))
}

func TestPluginInstalled_EmptyArray(t *testing.T) {
	data := []byte(`[]`)
	assert.False(t, pluginInstalled(data))
}

func TestPluginInstalled_EmptyObject(t *testing.T) {
	data := []byte(`{}`)
	assert.False(t, pluginInstalled(data))
}

func TestPluginInstalled_InvalidJSON(t *testing.T) {
	data := []byte(`not json`)
	assert.False(t, pluginInstalled(data))
}

func TestPluginInstalled_EmptyData(t *testing.T) {
	data := []byte(``)
	assert.False(t, pluginInstalled(data))
}

func TestDetectClaude_DirOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Ensure claude is not on PATH in this test
	t.Setenv("PATH", home) // empty directory, no binaries

	assert.False(t, DetectClaude(), "no ~/.claude and no binary should return false")

	// Create ~/.claude dir
	assert.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	assert.True(t, DetectClaude(), "~/.claude dir should make DetectClaude true")
}

func TestDetectClaude_BinaryOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// No ~/.claude dir, but create a fake claude binary on PATH
	binDir := filepath.Join(home, "bin")
	assert.NoError(t, os.MkdirAll(binDir, 0o755))
	fakeBinary := filepath.Join(binDir, "claude")
	assert.NoError(t, os.WriteFile(fakeBinary, []byte("#!/bin/sh\n"), 0o755)) //nolint:gosec // G306: test helper
	t.Setenv("PATH", binDir)

	assert.True(t, DetectClaude(), "claude binary on PATH should make DetectClaude true")
}
