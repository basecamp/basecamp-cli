package commands

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/basecamp/basecamp-cli/internal/harness"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// pluginVersionNoticeMarker prefixes the marker recording that the version
// notice was shown for one pairing of plugin and CLI versions.
const pluginVersionNoticeMarker = "plugin-version-notice-"

// pluginVersionNotice tells someone whose plugin and basecamp command come
// from different releases which one to update. The skills name commands, so
// a plugin behind the CLI misses ones it has, and a CLI behind the plugin
// lacks ones the skills run.
//
// Neither agent updates a third-party plugin unless the user turns that on,
// and the plugin changes only when its version string does, so a plugin
// installed once can stay where it was indefinitely while the CLI moves on.
// This hook is the one place that reaches such a user without their asking:
// the agent runs it at every session start. It speaks once per pairing of
// versions, so updating either side, or both drifting again, says it anew.
//
// It reads the version from this install's own manifest rather than the
// agent's records, so it describes the copy this session loaded. A pre-rename
// install gets the rename notice instead, whose setup command also updates it,
// and versions that aren't semver stay silent: doctor still reports them.
// It returns the notice, or "" when there is none to show.
func pluginVersionNotice(cmd *cobra.Command) string {
	if version.IsDev() {
		return ""
	}
	root := agentPluginRoot()
	if root == "" || isLegacyPluginRoot(root) {
		return ""
	}
	pluginVersion := pluginManifestVersion(root)
	plugin, cli := normalizeSemver(pluginVersion), normalizeSemver(version.Version)
	if !semver.IsValid(plugin) || !semver.IsValid(cli) || semver.Compare(plugin, cli) == 0 {
		return ""
	}
	cliBehind := semver.Compare(cli, plugin) < 0
	// A prerelease or go-install build ahead of the plugin is ahead of every
	// plugin the marketplace offers, so there is no plugin to update to. One
	// behind it, an rc left installed past its release, still needs upgrading.
	if semver.Prerelease(cli) != "" && !cliBehind {
		return ""
	}
	dir := agentHookNoticeDir(cmd.Context())
	// Valid semver holds only [0-9A-Za-z.+-], so the marker stays one name.
	if dir == "" || !claimOnce(filepath.Join(dir, pluginVersionNoticeMarker+plugin+"-"+cli)) {
		return ""
	}
	if cliBehind {
		return "The Basecamp plugin (" + pluginVersion + ") is newer than the `basecamp` command (" + version.Version + "), " +
			"so its skills may run commands this CLI doesn't have. Run `basecamp upgrade`."
	}
	return "The Basecamp plugin (" + pluginVersion + ") is older than the `basecamp` command (" + version.Version + "), " +
		"so its skills don't know this CLI's newer commands. " + pluginUpdateRemedy(root)
}

// pluginUpdateRemedy says how to bring forward the plugin installed at root.
func pluginUpdateRemedy(root string) string {
	switch pluginRootAgent(root) {
	case "claude":
		return "Run `claude plugin update " + harness.ClaudeExpectedPluginKey + "`, then restart Claude Code. " +
			"To keep it current: " + harness.AutoUpdateHint + "."
	case "codex":
		return "Run `basecamp setup codex`, then restart Codex."
	default:
		return "Update the " + harness.ClaudePluginName + " plugin, then restart your agent."
	}
}

// pluginManifestVersion reads the version the install at root was stamped
// with. Both manifests carry the release's version; either will do.
func pluginManifestVersion(root string) string {
	for _, manifest := range []string{".claude-plugin", ".codex-plugin"} {
		data, err := os.ReadFile(filepath.Join(root, manifest, "plugin.json")) //nolint:gosec // G304: the agent's own plugin root
		if err != nil {
			continue
		}
		var parsed struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &parsed) == nil && parsed.Version != "" {
			return parsed.Version
		}
	}
	return ""
}
