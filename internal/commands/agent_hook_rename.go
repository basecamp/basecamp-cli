package commands

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/harness"
)

// pluginRenameNoticeMarker records that the rename notice was shown for one
// plugin install, so it appears once rather than every session.
const pluginRenameNoticeMarker = "plugin-rename-notice-shown"

// emitPluginRenameNotice tells someone still running this plugin under its
// pre-rename id that it is now basecamp-cli. During the deprecation window the
// marketplace keeps "basecamp" as an alias with the same source, so the plugin
// keeps working; this nudges toward `basecamp setup`, once.
//
// The alias and basecamp-cli install the same files, so the manifest can't
// tell them apart. The plugin root can: agents cache each install under
// <marketplace>/<plugin id>/<version>. Anything other than an install as
// basecamp@37signals stays silent, so the hook injects nothing into normal
// sessions.
func emitPluginRenameNotice(cmd *cobra.Command) {
	root := agentPluginRoot()
	if !isLegacyPluginRoot(root) {
		return
	}
	dir := agentPluginDataDir()
	if dir == "" {
		dir = agentHookStateDir(cmd.Context())
	}
	if dir == "" || !claimOnce(filepath.Join(dir, pluginRenameNoticeMarker)) {
		return
	}
	message := "The Basecamp plugin is now `" + harness.ClaudePluginName + "`. " +
		"This copy still runs under its old name `" + harness.ClaudeLegacyPluginKey + "`, which keeps working for now. " +
		"Run `" + legacyPluginSetupCommand(root) + "` to switch."
	_ = json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
		"systemMessage": message,
		"hookSpecificOutput": map[string]string{
			"hookEventName":     "SessionStart",
			"additionalContext": "Mention this to the user once: " + message,
		},
	})
}

// agentPluginRoot is the plugin's install directory as the agent reports it:
// Claude Code sets CLAUDE_PLUGIN_ROOT, Codex sets PLUGIN_ROOT (and the
// Claude name too).
func agentPluginRoot() string {
	if root := os.Getenv("CLAUDE_PLUGIN_ROOT"); root != "" {
		return root
	}
	return os.Getenv("PLUGIN_ROOT")
}

func agentPluginDataDir() string {
	if dir := os.Getenv("CLAUDE_PLUGIN_DATA"); dir != "" {
		return dir
	}
	return os.Getenv("PLUGIN_DATA")
}

// isLegacyPluginRoot reports whether root is a cached install of
// basecamp@37signals: .../37signals/basecamp/<version>.
func isLegacyPluginRoot(root string) bool {
	if root == "" {
		return false
	}
	plugin := filepath.Dir(filepath.Clean(root))
	return filepath.Base(plugin) == harness.LegacyPluginName && filepath.Base(filepath.Dir(plugin)) == harness.ClaudeMarketplaceName
}

// legacyPluginSetupCommand names the setup command for the agent that owns
// root, falling back to setting up every detected agent.
func legacyPluginSetupCommand(root string) string {
	if home, err := os.UserHomeDir(); err == nil {
		for agent, dir := range map[string]string{"claude": ".claude", "codex": ".codex"} {
			if rel, err := filepath.Rel(filepath.Join(home, dir), root); err == nil && filepath.IsLocal(rel) {
				return "basecamp setup " + agent
			}
		}
	}
	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		if rel, err := filepath.Rel(codexHome, root); err == nil && filepath.IsLocal(rel) {
			return "basecamp setup codex"
		}
	}
	return "basecamp setup agents"
}

// claimOnce creates marker, reporting whether this call created it. Any
// failure counts as already shown: a notice that can't be recorded would
// otherwise repeat every session.
func claimOnce(marker string) bool {
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		return false
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // G304: marker under the agent's plugin data dir
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
