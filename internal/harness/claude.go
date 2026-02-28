package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DetectClaude returns true if Claude Code is installed (~/.claude/ exists).
func DetectClaude() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(home, ".claude"))
	return err == nil && info.IsDir()
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

	pluginsPath := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	data, err := os.ReadFile(pluginsPath) //nolint:gosec // G304: trusted path
	if err != nil {
		if os.IsNotExist(err) {
			return &StatusCheck{
				Name:    "Claude Code Plugin",
				Status:  "fail",
				Message: "Plugin not installed",
				Hint:    "Run: claude plugin install basecamp",
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
		Hint:    "Run: claude plugin install basecamp",
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

	// Try as map (key = plugin identifier)
	var pluginMap map[string]any
	if err := json.Unmarshal(data, &pluginMap); err == nil {
		for key := range pluginMap {
			if key == "basecamp" || key == "basecamp@basecamp" {
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
				if s == "basecamp" || s == "basecamp@basecamp" {
					return true
				}
			}
		}
	}
	return false
}

func jsonContainsBasecamp(data []byte) bool {
	// Fallback: raw string search for the plugin identifier
	s := string(data)
	return len(s) > 0 && (contains(s, `"basecamp@basecamp"`) || contains(s, `"basecamp"`))
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
