package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// installPluginAt fakes an agent's cached install of the plugin at root,
// stamped with pluginVersion, and points the hook at it.
func installPluginAt(t *testing.T, envRoot, root, manifestDir, pluginVersion string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, manifestDir), 0o755))
	manifest := `{"name":"basecamp-cli","version":"` + pluginVersion + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(root, manifestDir, "plugin.json"), []byte(manifest), 0o644))
	for _, name := range []string{"CLAUDE_PLUGIN_ROOT", "PLUGIN_ROOT", "CLAUDE_PLUGIN_DATA", "PLUGIN_DATA"} {
		t.Setenv(name, "")
	}
	t.Setenv(envRoot, root)
	data := "CLAUDE_PLUGIN_DATA"
	if envRoot == "PLUGIN_ROOT" {
		data = "PLUGIN_DATA"
	}
	t.Setenv(data, t.TempDir())
}

func setCLIVersion(t *testing.T, v string) {
	t.Helper()
	original := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = original })
}

func claudePluginRoot(t *testing.T, pluginVersion string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".claude", "plugins", "cache", "37signals", "basecamp-cli", pluginVersion)
}

func sessionStartSystemMessage(t *testing.T, output string) string {
	t.Helper()
	var payload struct {
		SystemMessage string `json:"systemMessage"`
	}
	require.NoError(t, json.Unmarshal([]byte(output), &payload))
	return payload.SystemMessage
}

func TestAgentHookVersionNoticeOncePerPairWhenPluginIsBehind(t *testing.T) {
	setCLIVersion(t, "0.17.0")
	installPluginAt(t, "CLAUDE_PLUGIN_ROOT", claudePluginRoot(t, "0.14.0"), ".claude-plugin", "0.14.0")

	output := runAgentHookPreservingHome(t, sessionStartPayload)
	message := sessionStartSystemMessage(t, output)
	assert.Contains(t, message, "plugin (0.14.0) is older than the `basecamp` command (0.17.0)")
	assert.Contains(t, message, "claude plugin update basecamp-cli@37signals")
	assert.Contains(t, message, "Enable auto-update")
	_, context := hookSpecificOutput(t, output)
	assert.Contains(t, context, "Mention this to the user once: "+message)

	assert.Empty(t, runAgentHookPreservingHome(t, sessionStartPayload), "the notice appears once per pairing")

	setCLIVersion(t, "0.18.0")
	assert.Contains(t, sessionStartSystemMessage(t, runAgentHookPreservingHome(t, sessionStartPayload)),
		"(0.18.0)", "a new pairing speaks again")
}

func TestAgentHookVersionNoticeSaysUpgradeWhenCLIIsBehind(t *testing.T) {
	setCLIVersion(t, "0.16.0")
	installPluginAt(t, "CLAUDE_PLUGIN_ROOT", claudePluginRoot(t, "0.17.0"), ".claude-plugin", "0.17.0")

	message := sessionStartSystemMessage(t, runAgentHookPreservingHome(t, sessionStartPayload))
	assert.Contains(t, message, "plugin (0.17.0) is newer than the `basecamp` command (0.16.0)")
	assert.Contains(t, message, "Run `basecamp upgrade`.")
	assert.NotContains(t, message, "claude plugin update")
}

func TestAgentHookVersionNoticeNamesCodexSetup(t *testing.T) {
	setCLIVersion(t, "0.17.0")
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".codex", "plugins", "cache", "37signals", "basecamp-cli", "0.15.0")
	installPluginAt(t, "PLUGIN_ROOT", root, ".codex-plugin", "0.15.0")

	message := sessionStartSystemMessage(t, runAgentHookPreservingHome(t, sessionStartPayload))
	assert.Contains(t, message, "plugin (0.15.0) is older")
	assert.Contains(t, message, "basecamp setup codex")
	assert.NotContains(t, message, "claude plugin update")
}

func TestAgentHookVersionNoticeSilentWhenNothingToSay(t *testing.T) {
	cases := []struct {
		name, cli, plugin string
	}{
		{"same release", "0.17.0", "0.17.0"},
		{"same release spelled with a v", "v0.17.0", "0.17.0"},
		{"dev build", "dev", "0.17.0"},
		{"prerelease CLI ahead of the plugin", "0.18.0-rc.1", "0.17.0"},
		{"go install pseudo-version", "0.17.1-0.20261022120000-abcdef123456", "0.17.0"},
		{"plugin version not semver", "0.17.0", "main"},
		{"plugin without a version", "0.17.0", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setCLIVersion(t, tc.cli)
			installPluginAt(t, "CLAUDE_PLUGIN_ROOT", claudePluginRoot(t, "x"), ".claude-plugin", tc.plugin)
			assert.Empty(t, runAgentHookPreservingHome(t, sessionStartPayload))
		})
	}
}

// A pre-rename install gets the rename notice, whose setup command also
// brings it to the newest version, and never both.
func TestAgentHookVersionNoticeDefersToRenameNotice(t *testing.T) {
	setCLIVersion(t, "0.17.0")
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".claude", "plugins", "cache", "37signals", "basecamp", "0.12.0")
	installPluginAt(t, "CLAUDE_PLUGIN_ROOT", root, ".claude-plugin", "0.12.0")

	message := sessionStartSystemMessage(t, runAgentHookPreservingHome(t, sessionStartPayload))
	assert.Contains(t, message, "now `basecamp-cli`")
	assert.NotContains(t, message, "older than")
	assert.Empty(t, runAgentHookPreservingHome(t, sessionStartPayload), "the rename notice is the only one")
}

// The notice rides only on SessionStart: a tool call's hook stays silent.
func TestAgentHookVersionNoticeOnlyAtSessionStart(t *testing.T) {
	setCLIVersion(t, "0.17.0")
	installPluginAt(t, "CLAUDE_PLUGIN_ROOT", claudePluginRoot(t, "0.14.0"), ".claude-plugin", "0.14.0")

	assert.Empty(t, runAgentHookPreservingHome(t, `{"hook_event_name":"PreToolUse","session_id":"s","tool_use_id":"t","tool_input":{"command":"ls"}}`))
}

// An rc left installed past its release is behind the plugin, and upgrading
// it is the remedy, prerelease or not.
func TestAgentHookVersionNoticeSaysUpgradeForPrereleaseBehindPlugin(t *testing.T) {
	setCLIVersion(t, "0.17.0-rc.1")
	installPluginAt(t, "CLAUDE_PLUGIN_ROOT", claudePluginRoot(t, "0.17.0"), ".claude-plugin", "0.17.0")

	message := sessionStartSystemMessage(t, runAgentHookPreservingHome(t, sessionStartPayload))
	assert.Contains(t, message, "plugin (0.17.0) is newer than the `basecamp` command (0.17.0-rc.1)")
	assert.Contains(t, message, "Run `basecamp upgrade`.")
}

// Without a plugin data directory the marker lives in the CLI's cache, and
// must survive the commit-snapshot cleanup, which removes every file in its
// own directory older than an hour.
func TestAgentHookVersionNoticeMarkerSurvivesSnapshotCleanup(t *testing.T) {
	setCLIVersion(t, "0.17.0")
	installPluginAt(t, "CLAUDE_PLUGIN_ROOT", claudePluginRoot(t, "0.14.0"), ".claude-plugin", "0.14.0")
	t.Setenv("CLAUDE_PLUGIN_DATA", "")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("BASECAMP_CACHE_DIR", "")

	require.NotEmpty(t, runAgentHookPreservingHome(t, sessionStartPayload))

	cache := config.Default().CacheDir
	aged := time.Now().Add(-2 * agentHookSnapshotTTL)
	require.NoError(t, filepath.Walk(cache, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, aged, aged)
	}))
	removeExpiredAgentHookSnapshots(filepath.Join(cache, "agent-hook"))

	assert.Empty(t, runAgentHookPreservingHome(t, sessionStartPayload), "the notice stays shown once")
}

// Without plugin data directories Claude Code and Codex share the fallback,
// and each still gets its own notice once.
func TestAgentHookVersionNoticeOncePerAgentInSharedFallback(t *testing.T) {
	setCLIVersion(t, "0.17.0")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("BASECAMP_CACHE_DIR", "")
	claude := filepath.Join(home, ".claude", "plugins", "cache", "37signals", "basecamp-cli", "0.14.0")
	codex := filepath.Join(home, ".codex", "plugins", "cache", "37signals", "basecamp-cli", "0.14.0")

	installPluginAt(t, "CLAUDE_PLUGIN_ROOT", claude, ".claude-plugin", "0.14.0")
	t.Setenv("CLAUDE_PLUGIN_DATA", "")
	assert.Contains(t, sessionStartSystemMessage(t, runAgentHookPreservingHome(t, sessionStartPayload)), "claude plugin update")

	installPluginAt(t, "PLUGIN_ROOT", codex, ".codex-plugin", "0.14.0")
	t.Setenv("PLUGIN_DATA", "")
	assert.Contains(t, sessionStartSystemMessage(t, runAgentHookPreservingHome(t, sessionStartPayload)), "basecamp setup codex")
	assert.Empty(t, runAgentHookPreservingHome(t, sessionStartPayload), "and each only once")
}
