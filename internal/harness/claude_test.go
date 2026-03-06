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

func TestPluginInstalled_V2Envelope(t *testing.T) {
	data := []byte(`{"version":2,"plugins":{"basecamp@basecamp":[{"scope":"user","version":"0.1.0"}]}}`)
	assert.True(t, pluginInstalled(data))
}

func TestPluginInstalled_V2Envelope_AltMarketplace(t *testing.T) {
	data := []byte(`{"version":2,"plugins":{"basecamp@37signals":[{"scope":"user","version":"0.1.0"}]}}`)
	assert.True(t, pluginInstalled(data))
}

func TestPluginInstalled_V2Envelope_NotFound(t *testing.T) {
	data := []byte(`{"version":2,"plugins":{"other-plugin@marketplace":[{"scope":"user","version":"1.0.0"}]}}`)
	assert.False(t, pluginInstalled(data))
}

func TestPluginInstalled_V2Envelope_EmptyPlugins(t *testing.T) {
	data := []byte(`{"version":2,"plugins":{}}`)
	assert.False(t, pluginInstalled(data))
}

func TestPluginInstalled_ArrayFormat_AltMarketplace(t *testing.T) {
	data := []byte(`[{"package": "basecamp@37signals", "version": "0.1.0"}]`)
	assert.True(t, pluginInstalled(data))
}

func TestPluginInstalled_MapFormat_AltMarketplace(t *testing.T) {
	data := []byte(`{"basecamp@37signals": {"version": "0.1.0"}}`)
	assert.True(t, pluginInstalled(data))
}

func TestCheckClaudeSkillLink_Missing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	assert.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	check := CheckClaudeSkillLink()
	assert.Equal(t, "fail", check.Status)
	assert.Equal(t, "Claude Code Skill", check.Name)
}

func TestCheckClaudeSkillLink_Present(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	skillDir := filepath.Join(home, ".claude", "skills", "basecamp")
	assert.NoError(t, os.MkdirAll(skillDir, 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("test"), 0o644))

	check := CheckClaudeSkillLink()
	assert.Equal(t, "pass", check.Status)
}

func TestCheckClaudeSkillLink_BrokenSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	symlinkDir := filepath.Join(home, ".claude", "skills")
	assert.NoError(t, os.MkdirAll(symlinkDir, 0o755))
	// Create a symlink pointing to nowhere
	assert.NoError(t, os.Symlink("/nonexistent/path", filepath.Join(symlinkDir, "basecamp")))

	check := CheckClaudeSkillLink()
	assert.Equal(t, "fail", check.Status)
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
