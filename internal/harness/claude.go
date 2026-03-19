package harness

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
)

func init() {
	RegisterAgent(AgentInfo{
		Name:   "Claude Code",
		ID:     "claude",
		Detect: DetectClaude,
		Checks: func() []*StatusCheck {
			checks := []*StatusCheck{CheckClaudePlugin()}
			// Only check the skill link if ~/.claude exists (i.e. Claude is dir-detected)
			home, err := os.UserHomeDir()
			if err == nil {
				if info, statErr := os.Stat(filepath.Join(home, ".claude")); statErr == nil && info.IsDir() {
					checks = append(checks, CheckClaudeSkillLink())
				}
			}
			return checks
		},
	})
}

// ClaudeMarketplaceSource is the marketplace repository for the Basecamp plugin.
// Migrating from basecamp/basecamp-cli → basecamp/claude-plugins.
const ClaudeMarketplaceSource = "basecamp/claude-plugins"

// ClaudePluginName is the plugin identifier to install.
const ClaudePluginName = "basecamp"

// ClaudeMarketplaceName is the marketplace name as it appears in plugin keys.
const ClaudeMarketplaceName = "37signals"

// ClaudeExpectedPluginKey is the fully-qualified key for a correctly installed plugin.
const ClaudeExpectedPluginKey = ClaudePluginName + "@" + ClaudeMarketplaceName

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
// basecamp plugin — either bare "basecamp" (legacy) or the expected
// marketplace-qualified key "basecamp@37signals".
func matchesPluginKey(key string) bool {
	return key == "basecamp" || key == ClaudeExpectedPluginKey
}

func jsonContainsBasecamp(data []byte) bool {
	// Fallback: raw string search for the expected plugin key or bare name.
	// Note: `"basecamp"` (with quotes) won't match `"basecamp@old"` since
	// the closing quote requires an exact boundary.
	s := string(data)
	return len(s) > 0 && (contains(s, `"`+ClaudeExpectedPluginKey+`"`) || contains(s, `"basecamp"`))
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

// StalePluginKeys returns stale plugin entries from installed_plugins.json
// that belong to old/dead marketplaces.
func StalePluginKeys() []StalePlugin {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(filepath.Clean(home), ".claude", "plugins", "installed_plugins.json")) //nolint:gosec // G304: trusted path
	if err != nil {
		return nil
	}
	return stalePluginKeys(data)
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
const claudeStalePluginKey = ClaudePluginName + "@" + "basecamp"

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
