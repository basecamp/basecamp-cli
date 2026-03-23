package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/version"
)

func TestPluginInstalled_ArrayFormat(t *testing.T) {
	data := []byte(`[{"name": "basecamp", "version": "1.0.0"}]`)
	assert.True(t, pluginInstalled(data))
}

func TestPluginInstalled_ArrayFormat_StaleMarketplace(t *testing.T) {
	// basecamp@basecamp is a stale marketplace — should not count as installed
	data := []byte(`[{"package": "basecamp@basecamp", "version": "1.0.0"}]`)
	assert.False(t, pluginInstalled(data))
}

func TestPluginInstalled_ArrayFormat_NotFound(t *testing.T) {
	data := []byte(`[{"name": "other-plugin", "version": "1.0.0"}]`)
	assert.False(t, pluginInstalled(data))
}

func TestPluginInstalled_MapFormat_StaleMarketplace(t *testing.T) {
	// basecamp@basecamp is a stale marketplace — should not count as installed
	data := []byte(`{"basecamp@basecamp": {"version": "1.0.0"}}`)
	assert.False(t, pluginInstalled(data))
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

func TestPluginInstalled_V2Envelope_StaleMarketplace(t *testing.T) {
	// basecamp@basecamp is a stale marketplace — should not count as installed
	data := []byte(`{"version":2,"plugins":{"basecamp@basecamp":[{"scope":"user","version":"0.1.0"}]}}`)
	assert.False(t, pluginInstalled(data))
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

func TestStalePluginKeys_V2_StaleOnly(t *testing.T) {
	data := []byte(`{"version":2,"plugins":{"basecamp@basecamp":[{"scope":"user"}]}}`)
	assert.Equal(t, []StalePlugin{{Key: "basecamp@basecamp", Scopes: []string{"user"}}}, stalePluginKeys(data))
}

func TestStalePluginKeys_V2_Mixed(t *testing.T) {
	data := []byte(`{"version":2,"plugins":{"basecamp@37signals":[{"scope":"user"}],"basecamp@basecamp":[{"scope":"user"}]}}`)
	plugins := stalePluginKeys(data)
	assert.Equal(t, []StalePlugin{{Key: "basecamp@basecamp", Scopes: []string{"user"}}}, plugins)
}

func TestStalePluginKeys_V2_CorrectOnly(t *testing.T) {
	data := []byte(`{"version":2,"plugins":{"basecamp@37signals":[{"scope":"user"}]}}`)
	assert.Empty(t, stalePluginKeys(data))
}

func TestStalePluginKeys_V1_Stale(t *testing.T) {
	data := []byte(`{"basecamp@basecamp":{"version":"1.0.0"}}`)
	assert.Equal(t, []StalePlugin{{Key: "basecamp@basecamp"}}, stalePluginKeys(data))
}

func TestStalePluginKeys_V1_Correct(t *testing.T) {
	data := []byte(`{"basecamp@37signals":{"version":"1.0.0"}}`)
	assert.Empty(t, stalePluginKeys(data))
}

func TestStalePluginKeys_BareKey(t *testing.T) {
	// Bare "basecamp" key (no marketplace) is not stale — it's legacy
	data := []byte(`{"basecamp":{"version":"1.0.0"}}`)
	assert.Empty(t, stalePluginKeys(data))
}

func TestStalePluginKeys_ArrayFormat_Stale(t *testing.T) {
	data := []byte(`[{"package":"basecamp@basecamp","version":"1.0.0"},{"package":"basecamp@37signals","version":"0.1.0"}]`)
	assert.Equal(t, []StalePlugin{{Key: "basecamp@basecamp"}}, stalePluginKeys(data))
}

func TestStalePluginKeys_ArrayFormat_NoneStale(t *testing.T) {
	data := []byte(`[{"name":"basecamp","version":"1.0.0"}]`)
	assert.Empty(t, stalePluginKeys(data))
}

func TestStalePluginKeys_Empty(t *testing.T) {
	assert.Empty(t, stalePluginKeys([]byte(`{}`)))
	assert.Empty(t, stalePluginKeys([]byte(`[]`)))
	assert.Empty(t, stalePluginKeys([]byte(`not json`)))
}

func TestStalePluginKeys_V2_MultiScope(t *testing.T) {
	data := []byte(`{"version":2,"plugins":{"basecamp@basecamp":[{"scope":"user","version":"0.1.0"},{"scope":"project","version":"0.1.0"}]}}`)
	plugins := stalePluginKeys(data)
	require.Len(t, plugins, 1)
	assert.Equal(t, "basecamp@basecamp", plugins[0].Key)
	assert.Equal(t, []string{"user", "project"}, plugins[0].Scopes)
}

func TestStalePluginKeys_ArrayFormat_WithScope(t *testing.T) {
	data := []byte(`[{"package":"basecamp@basecamp","scope":"user","version":"1.0.0"}]`)
	plugins := stalePluginKeys(data)
	require.Len(t, plugins, 1)
	assert.Equal(t, "basecamp@basecamp", plugins[0].Key)
	assert.Equal(t, []string{"user"}, plugins[0].Scopes)
}

func TestIsStalePluginKey(t *testing.T) {
	assert.True(t, isStalePluginKey("basecamp@basecamp"))
	assert.False(t, isStalePluginKey("basecamp@old-marketplace"))
	assert.False(t, isStalePluginKey("basecamp@37signals"))
	assert.False(t, isStalePluginKey("basecamp"))
	assert.False(t, isStalePluginKey("other@basecamp"))
}

func TestInstalledPluginVersion_V2(t *testing.T) {
	data := []byte(`{"version":2,"plugins":{"basecamp@37signals":[{"scope":"user","version":"1.2.3"}]}}`)
	assert.Equal(t, "1.2.3", installedPluginVersion(data))
}

func TestInstalledPluginVersion_V2_BareKey(t *testing.T) {
	data := []byte(`{"version":2,"plugins":{"basecamp":[{"scope":"user","version":"0.5.0"}]}}`)
	assert.Equal(t, "0.5.0", installedPluginVersion(data))
}

func TestInstalledPluginVersion_V2_NotFound(t *testing.T) {
	data := []byte(`{"version":2,"plugins":{"other@marketplace":[{"scope":"user","version":"1.0.0"}]}}`)
	assert.Equal(t, "", installedPluginVersion(data))
}

func TestInstalledPluginVersion_Array(t *testing.T) {
	data := []byte(`[{"name":"basecamp@37signals","version":"2.0.0"}]`)
	assert.Equal(t, "2.0.0", installedPluginVersion(data))
}

func TestInstalledPluginVersion_Array_NotFound(t *testing.T) {
	data := []byte(`[{"name":"other-plugin","version":"1.0.0"}]`)
	assert.Equal(t, "", installedPluginVersion(data))
}

func TestInstalledPluginVersion_V1FlatMap(t *testing.T) {
	data := []byte(`{"basecamp@37signals":{"version":"1.0.0"}}`)
	assert.Equal(t, "1.0.0", installedPluginVersion(data))
}

func TestInstalledPluginVersion_Empty(t *testing.T) {
	assert.Equal(t, "", installedPluginVersion([]byte(`{}`)))
	assert.Equal(t, "", installedPluginVersion([]byte(`[]`)))
	assert.Equal(t, "", installedPluginVersion([]byte(`invalid`)))
}

func TestCheckClaudePluginVersion_UpToDate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	origVersion := version.Version
	version.Version = "1.5.0"
	defer func() { version.Version = origVersion }()

	pluginsDir := filepath.Join(home, ".claude", "plugins")
	require.NoError(t, os.MkdirAll(pluginsDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(pluginsDir, "installed_plugins.json"),
		[]byte(`{"version":2,"plugins":{"basecamp@37signals":[{"scope":"user","version":"1.5.0"}]}}`),
		0o644,
	))

	check := CheckClaudePluginVersion()
	assert.Equal(t, "pass", check.Status)
	assert.Contains(t, check.Message, "1.5.0")
}

func TestCheckClaudePluginVersion_Outdated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	origVersion := version.Version
	version.Version = "2.0.0"
	defer func() { version.Version = origVersion }()

	pluginsDir := filepath.Join(home, ".claude", "plugins")
	require.NoError(t, os.MkdirAll(pluginsDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(pluginsDir, "installed_plugins.json"),
		[]byte(`{"version":2,"plugins":{"basecamp@37signals":[{"scope":"user","version":"0.1.0"}]}}`),
		0o644,
	))

	check := CheckClaudePluginVersion()
	assert.Equal(t, "warn", check.Status)
	assert.Contains(t, check.Message, "0.1.0")
	assert.Contains(t, check.Message, "2.0.0")
	assert.Contains(t, check.Hint, "auto-update")
}

func TestCheckClaudePluginVersion_DevBuild(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	origVersion := version.Version
	version.Version = "dev"
	defer func() { version.Version = origVersion }()

	pluginsDir := filepath.Join(home, ".claude", "plugins")
	require.NoError(t, os.MkdirAll(pluginsDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(pluginsDir, "installed_plugins.json"),
		[]byte(`{"version":2,"plugins":{"basecamp@37signals":[{"scope":"user","version":"0.1.0"}]}}`),
		0o644,
	))

	check := CheckClaudePluginVersion()
	assert.Equal(t, "pass", check.Status)
	assert.Contains(t, check.Message, "dev build")
}

func TestCheckClaudePluginVersion_NoFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	check := CheckClaudePluginVersion()
	assert.Equal(t, "pass", check.Status)
	assert.Contains(t, check.Message, "not tracked")
}
