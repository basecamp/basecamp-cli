package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/basecamp/basecamp-cli/internal/version"
)

func init() {
	RegisterAgent(AgentInfo{
		Name:       "Claude Code",
		ID:         "claude",
		Detect:     DetectClaude,
		FindBinary: FindClaudeBinary,
		Checks:     claudeChecks,
		Diagnostics: func(_ context.Context) []*StatusCheck {
			return append(claudeChecks(), CheckClaudePluginVersion())
		},
	})
}

func claudeChecks() []*StatusCheck {
	checks := []*StatusCheck{CheckClaudePlugin()}
	// Only check the skill link if ~/.claude exists (i.e. Claude is dir-detected)
	home, err := os.UserHomeDir()
	if err == nil {
		if info, statErr := os.Stat(filepath.Join(home, ".claude")); statErr == nil && info.IsDir() {
			checks = append(checks, CheckClaudeSkillLink())
		}
	}
	return checks
}

// ClaudeMarketplaceSource is the marketplace repository for the Basecamp plugin.
// Migrating from basecamp/basecamp-cli → basecamp/claude-plugins.
const ClaudeMarketplaceSource = "basecamp/claude-plugins"

// ClaudePluginName is the plugin identifier to install. It was "basecamp"
// until the 37signals marketplace gave that name to the hosted-connector
// plugin; see ClaudeLegacyPluginKey.
const ClaudePluginName = "basecamp-cli"

// ClaudeMarketplaceName is the marketplace name as it appears in plugin keys.
const ClaudeMarketplaceName = "37signals"

// ClaudeExpectedPluginKey is the fully-qualified key for a correctly installed plugin.
const ClaudeExpectedPluginKey = ClaudePluginName + "@" + ClaudeMarketplaceName

// ClaudeLegacyPluginKey is the key this plugin was installed under before the
// rename. The 37signals marketplace now publishes the hosted Basecamp
// connector plugin under that same key, so an entry with it is this CLI's
// plugin only when its installed manifest says so (see legacyCLIInstall).
const ClaudeLegacyPluginKey = "basecamp@" + ClaudeMarketplaceName

// CLIRepository is the repository both plugin manifests in this repo name.
// An installed plugin whose manifest names it is this CLI's plugin, whatever
// key it was installed under.
const CLIRepository = "https://github.com/basecamp/basecamp-cli"

// DetectClaude returns true if Claude Code is installed.
// Checks ~/.claude/ directory first, then falls back to binary on PATH.
func DetectClaude() bool {
	home, err := os.UserHomeDir()
	if err == nil {
		home = filepath.Clean(home)
		info, statErr := os.Stat(filepath.Join(home, ".claude"))
		if statErr == nil && info.IsDir() {
			return true
		}
	}
	return FindClaudeBinary() != ""
}

// IsPluginNeeded returns true if Claude Code is installed but the plugin is not.
func IsPluginNeeded() bool {
	if !DetectClaude() {
		return false
	}
	check := CheckClaudePlugin()
	return check.Status != "pass"
}

// FindClaudeBinary returns the path to the claude binary, or "" if not found.
func FindClaudeBinary() string {
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	candidate := filepath.Join(filepath.Clean(home), ".local", "bin", "claude")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

// CheckClaudePlugin checks whether the basecamp plugin is installed in Claude Code.
func CheckClaudePlugin() *StatusCheck {
	home, err := os.UserHomeDir()
	if err != nil {
		return &StatusCheck{
			Name:    "Claude Code Plugin",
			Status:  "warn",
			Message: "Cannot determine home directory",
		}
	}

	pluginsPath := filepath.Join(filepath.Clean(home), ".claude", "plugins", "installed_plugins.json")
	data, err := os.ReadFile(pluginsPath) //nolint:gosec // G304: trusted path
	if err != nil {
		if os.IsNotExist(err) {
			return &StatusCheck{
				Name:    "Claude Code Plugin",
				Status:  "fail",
				Message: "Plugin not installed",
				Hint:    "Run: basecamp setup claude",
			}
		}
		return &StatusCheck{
			Name:    "Claude Code Plugin",
			Status:  "warn",
			Message: "Cannot check Claude Code integration",
			Hint:    "Unable to read " + pluginsPath,
		}
	}

	// Parse the plugins file — schema may vary, so be resilient.
	// Try as array of objects with "name" or "package" fields,
	// or as a map with plugin keys.
	if pluginInstalled(data) {
		return &StatusCheck{
			Name:    "Claude Code Plugin",
			Status:  "pass",
			Message: "Installed",
		}
	}

	if len(legacyCLIPluginEntries(data)) > 0 {
		return &StatusCheck{
			Name:    "Claude Code Plugin",
			Status:  "fail",
			Message: "Installed under the old name " + ClaudeLegacyPluginKey,
			Hint:    "Run: basecamp setup claude (reinstalls it as " + ClaudeExpectedPluginKey + ")",
		}
	}

	return &StatusCheck{
		Name:    "Claude Code Plugin",
		Status:  "fail",
		Message: "Plugin not installed",
		Hint:    "Run: basecamp setup claude",
	}
}

// CheckClaudeSkillLink checks whether ~/.claude/skills/basecamp contains a valid SKILL.md.
func CheckClaudeSkillLink() *StatusCheck {
	home, err := os.UserHomeDir()
	if err != nil {
		return &StatusCheck{
			Name:    "Claude Code Skill",
			Status:  "warn",
			Message: "Cannot determine home directory",
		}
	}

	skillPath := filepath.Join(filepath.Clean(home), ".claude", "skills", "basecamp", "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		if os.IsNotExist(err) {
			return &StatusCheck{
				Name:    "Claude Code Skill",
				Status:  "fail",
				Message: "Skill not linked",
				Hint:    "Run: basecamp setup claude",
			}
		}
		return &StatusCheck{
			Name:    "Claude Code Skill",
			Status:  "warn",
			Message: "Cannot check skill link",
			Hint:    "Unable to stat " + skillPath,
		}
	}

	return &StatusCheck{
		Name:    "Claude Code Skill",
		Status:  "pass",
		Message: "Linked",
	}
}

// AutoUpdateHint is the user-facing instruction for enabling plugin auto-update.
const AutoUpdateHint = "In Claude Code: /plugins → Marketplaces → 37signals → Enable auto-update"

// CheckClaudePluginVersion compares the installed plugin version against the
// running CLI version. Returns a warn check when they differ.
func CheckClaudePluginVersion() *StatusCheck {
	installed := InstalledPluginVersion()
	if installed == "" {
		// Can't determine version — don't nag.
		return &StatusCheck{
			Name:    "Claude Code Plugin Version",
			Status:  "pass",
			Message: "Version not tracked",
		}
	}

	cliVersion := version.Version
	if cliVersion == "dev" {
		return &StatusCheck{
			Name:    "Claude Code Plugin Version",
			Status:  "pass",
			Message: fmt.Sprintf("Installed (%s, dev build)", installed),
		}
	}

	if installed == cliVersion {
		return &StatusCheck{
			Name:    "Claude Code Plugin Version",
			Status:  "pass",
			Message: fmt.Sprintf("Up to date (%s)", installed),
		}
	}

	return &StatusCheck{
		Name:    "Claude Code Plugin Version",
		Status:  "warn",
		Message: fmt.Sprintf("Mismatched (plugin %s, CLI %s)", installed, cliVersion),
		Hint:    AutoUpdateHint,
	}
}

// InstalledPluginVersion reads the installed plugin version from
// ~/.claude/plugins/installed_plugins.json. Returns "" if unreadable.
func InstalledPluginVersion() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(filepath.Clean(home), ".claude", "plugins", "installed_plugins.json")) //nolint:gosec // G304: trusted path
	if err != nil {
		return ""
	}
	return installedPluginVersion(data)
}

// installedPluginVersion extracts the version of the basecamp plugin from
// the installed_plugins.json data. Handles v2, v1 flat map, and array formats.
func installedPluginVersion(data []byte) string {
	// v2 format: {"version": 2, "plugins": {"basecamp@37signals": [{"version": "1.0.0", ...}]}}
	var pluginMap map[string]any
	if err := json.Unmarshal(data, &pluginMap); err == nil {
		if inner, ok := pluginMap["plugins"]; ok {
			if innerMap, ok := inner.(map[string]any); ok {
				for key, val := range innerMap {
					if !matchesPluginKey(key) {
						continue
					}
					if arr, ok := val.([]any); ok {
						for _, entry := range arr {
							if obj, ok := entry.(map[string]any); ok {
								if v, ok := obj["version"].(string); ok {
									return v
								}
							}
						}
					}
				}
			}
		}

		// v1 flat map: {"basecamp@37signals": {"version": "1.0.0"}}
		for key, val := range pluginMap {
			if !matchesPluginKey(key) {
				continue
			}
			if obj, ok := val.(map[string]any); ok {
				if v, ok := obj["version"].(string); ok {
					return v
				}
			}
		}
	}

	// Array format: [{"name": "basecamp@37signals", "version": "1.0.0", ...}]
	var plugins []map[string]any
	if err := json.Unmarshal(data, &plugins); err == nil {
		for _, p := range plugins {
			if matchesBasecamp(p) {
				if v, ok := p["version"].(string); ok {
					return v
				}
			}
		}
	}

	return ""
}

// pluginInstalled checks if "basecamp" appears as an installed plugin.
// Handles multiple possible JSON schemas without panicking.
func pluginInstalled(data []byte) bool {
	// Try as array of objects
	var plugins []map[string]any
	if err := json.Unmarshal(data, &plugins); err == nil {
		for _, p := range plugins {
			if matchesBasecamp(p) {
				return true
			}
		}
		return false
	}

	// Try as map (key = plugin identifier, or v2 envelope with "plugins" key)
	var pluginMap map[string]any
	if err := json.Unmarshal(data, &pluginMap); err == nil {
		// v2 format: {"version": 2, "plugins": {"basecamp@marketplace": [...]}}
		if inner, ok := pluginMap["plugins"]; ok {
			if innerMap, ok := inner.(map[string]any); ok {
				for key := range innerMap {
					if matchesPluginKey(key) {
						return true
					}
				}
				return false
			}
		}
		// v1 flat map: {"basecamp@37signals": {...}}
		for key := range pluginMap {
			if matchesPluginKey(key) {
				return true
			}
		}
		return false
	}

	// Try as raw JSON and search for the string
	return jsonContainsBasecamp(data)
}

func matchesBasecamp(p map[string]any) bool {
	for _, field := range []string{"name", "package", "id"} {
		if v, ok := p[field]; ok {
			if s, ok := v.(string); ok {
				if matchesPluginKey(s) {
					return true
				}
			}
		}
	}
	return false
}

// matchesPluginKey returns true if the key identifies a correctly installed
// plugin — either the bare name or the expected marketplace-qualified key
// "basecamp-cli@37signals". The pre-rename "basecamp" names deliberately do
// not match: they now belong to the hosted-connector plugin.
func matchesPluginKey(key string) bool {
	return key == ClaudePluginName || key == ClaudeExpectedPluginKey
}

func jsonContainsBasecamp(data []byte) bool {
	// Fallback: raw string search for the expected plugin key or bare name.
	// Note: `"basecamp-cli"` (with quotes) won't match `"basecamp-cli@old"`
	// since the closing quote requires an exact boundary.
	s := string(data)
	return len(s) > 0 && (contains(s, `"`+ClaudeExpectedPluginKey+`"`) || contains(s, `"`+ClaudePluginName+`"`))
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// StalePlugin carries a stale plugin key along with the scopes it was found in.
type StalePlugin struct {
	Key    string
	Scopes []string // empty = unknown, fall back to unscoped uninstall
}

// StalePluginKeys returns stale plugin entries from installed_plugins.json:
// entries from old/dead marketplaces, and this CLI's plugin still installed
// under its pre-rename key. Setup uninstalls each and reinstalls
// ClaudeExpectedPluginKey at the scopes it removed.
func StalePluginKeys() []StalePlugin {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(filepath.Clean(home), ".claude", "plugins", "installed_plugins.json")) //nolint:gosec // G304: trusted path
	if err != nil {
		return nil
	}
	return append(stalePluginKeys(data), legacyCLIPluginEntries(data)...)
}

// stalePluginKeys extracts known stale plugin entries (e.g. basecamp@basecamp from
// the marketplace rename), including per-scope information when the format provides it.
func stalePluginKeys(data []byte) []StalePlugin {
	// Try as array of objects: [{"package": "basecamp@basecamp", "scope": "user", ...}]
	var plugins []map[string]any
	if err := json.Unmarshal(data, &plugins); err == nil {
		byKey := map[string][]string{}
		var order []string
		for _, p := range plugins {
			var key string
			for _, field := range []string{"package", "id", "name"} {
				if v, ok := p[field]; ok {
					if s, ok := v.(string); ok && isStalePluginKey(s) {
						key = s
						break
					}
				}
			}
			if key == "" {
				continue
			}
			if _, exists := byKey[key]; !exists {
				order = append(order, key)
				byKey[key] = nil
			}
			if s, ok := p["scope"].(string); ok && s != "" {
				byKey[key] = appendUnique(byKey[key], s)
			}
		}
		stale := make([]StalePlugin, 0, len(order))
		for _, k := range order {
			stale = append(stale, StalePlugin{Key: k, Scopes: byKey[k]})
		}
		return stale
	}

	var pluginMap map[string]any
	if err := json.Unmarshal(data, &pluginMap); err != nil {
		return nil
	}

	// v2 format: {"version": 2, "plugins": {"basecamp@old": [{"scope":"user"}, ...]}}
	if inner, ok := pluginMap["plugins"]; ok {
		if innerMap, ok := inner.(map[string]any); ok {
			var stale []StalePlugin
			for key, val := range innerMap {
				if !isStalePluginKey(key) {
					continue
				}
				var scopes []string
				if arr, ok := val.([]any); ok {
					for _, entry := range arr {
						if obj, ok := entry.(map[string]any); ok {
							if s, ok := obj["scope"].(string); ok && s != "" {
								scopes = appendUnique(scopes, s)
							}
						}
					}
				}
				stale = append(stale, StalePlugin{Key: key, Scopes: scopes})
			}
			return stale
		}
	}

	// v1 flat map — no scope info available
	var stale []StalePlugin
	for key := range pluginMap {
		if isStalePluginKey(key) {
			stale = append(stale, StalePlugin{Key: key})
		}
	}
	return stale
}

// claudeStalePluginKey is the known stale key from the basecamp → 37signals marketplace rename.
const claudeStalePluginKey = "basecamp@basecamp"

// isStalePluginKey returns true only for the known stale marketplace key.
func isStalePluginKey(key string) bool {
	return key == claudeStalePluginKey
}

func appendUnique(ss []string, s string) []string {
	for _, existing := range ss {
		if existing == s {
			return ss
		}
	}
	return append(ss, s)
}

// legacyCLIPluginEntries returns this CLI's plugin when it is still installed
// under ClaudeLegacyPluginKey. That key now also names the hosted-connector
// plugin, so the key alone proves nothing: only the scopes whose installed
// manifest names CLIRepository count. An entry whose manifest can't be read —
// or a file format that records no install path — is left alone, since
// removing a hosted-connector install would be worse than leaving a legacy
// CLI one in place.
func legacyCLIPluginEntries(data []byte) []StalePlugin {
	var envelope struct {
		Plugins map[string][]struct {
			Scope       string `json:"scope"`
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil
	}
	entries, ok := envelope.Plugins[ClaudeLegacyPluginKey]
	if !ok {
		return nil
	}
	found := false
	var scopes []string
	for _, entry := range entries {
		if entry.InstallPath == "" || !isCLIPluginManifest(filepath.Join(entry.InstallPath, ".claude-plugin", "plugin.json")) {
			continue
		}
		found = true
		if entry.Scope != "" {
			scopes = appendUnique(scopes, entry.Scope)
		}
	}
	if !found {
		return nil
	}
	return []StalePlugin{{Key: ClaudeLegacyPluginKey, Scopes: scopes}}
}

// isCLIPluginManifest reports whether the plugin manifest at path is this
// CLI's: its repository is CLIRepository.
func isCLIPluginManifest(path string) bool {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path from the agent's own plugin records
	if err != nil {
		return false
	}
	var manifest struct {
		Repository any `json:"repository"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return false
	}
	var repo string
	switch r := manifest.Repository.(type) {
	case string:
		repo = r
	case map[string]any:
		repo, _ = r["url"].(string)
	}
	return normalizeRepositoryURL(repo) == CLIRepository
}

func normalizeRepositoryURL(url string) string {
	url = strings.TrimSpace(strings.ToLower(url))
	url = strings.TrimPrefix(url, "git+")
	url = strings.TrimSuffix(url, "/")
	return strings.TrimSuffix(url, ".git")
}
