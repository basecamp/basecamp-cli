package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/basecamp/basecamp-cli/internal/version"
)

const (
	// CodexMarketplaceSource is the Git marketplace repository containing Basecamp.
	CodexMarketplaceSource = "basecamp/claude-plugins"
	// CodexPluginName is the plugin identifier to install.
	CodexPluginName = "basecamp"
	// CodexMarketplaceName is the marketplace name published by 37signals.
	CodexMarketplaceName = "37signals"
	// CodexExpectedPluginKey is the fully qualified Basecamp plugin ID.
	CodexExpectedPluginKey = CodexPluginName + "@" + CodexMarketplaceName
)

var (
	codexLookPath   = exec.LookPath
	runCodexCommand = func(ctx context.Context, path string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, path, args...).Output() //nolint:gosec // path comes from exec.LookPath
	}
)

var errCodexBinaryMissing = errors.New("codex executable not found")

type codexPluginState struct {
	PluginID    string `json:"pluginId"`
	Name        string `json:"name"`
	Marketplace string `json:"marketplaceName"`
	Version     string `json:"version"`
	Installed   bool   `json:"installed"`
	Enabled     bool   `json:"enabled"`
}

func init() {
	RegisterAgent(AgentInfo{
		Name:   "Codex",
		ID:     "codex",
		Detect: DetectCodex,
		Checks: func() []*StatusCheck {
			return []*StatusCheck{CheckCodexPlugin()}
		},
	})
}

// DetectCodex returns true when Codex has a home directory or executable.
func DetectCodex() bool {
	home, err := os.UserHomeDir()
	if err == nil {
		info, statErr := os.Stat(filepath.Join(filepath.Clean(home), ".codex"))
		if statErr == nil && info.IsDir() {
			return true
		}
	}
	return FindCodexBinary() != ""
}

// FindCodexBinary returns the Codex executable path, or an empty string.
func FindCodexBinary() string {
	if path, err := codexLookPath("codex"); err == nil {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	candidate := filepath.Join(filepath.Clean(home), ".local", "bin", "codex")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

// CheckCodexPlugin verifies that Basecamp is installed and enabled in Codex.
func CheckCodexPlugin() *StatusCheck {
	return CheckCodexPluginContext(context.Background())
}

// CheckCodexPluginContext verifies the plugin using the caller's context.
func CheckCodexPluginContext(ctx context.Context) *StatusCheck {
	state, found, err := queryCodexPlugin(ctx)
	return codexPluginCheck(state, found, err)
}

// CheckCodexPluginDiagnosticsContext checks plugin health and version from one Codex query.
func CheckCodexPluginDiagnosticsContext(ctx context.Context) []*StatusCheck {
	state, found, err := queryCodexPlugin(ctx)
	return []*StatusCheck{
		codexPluginCheck(state, found, err),
		codexPluginVersionCheck(state, found, err),
	}
}

func codexPluginCheck(state codexPluginState, found bool, err error) *StatusCheck {
	if err != nil {
		return codexQueryFailure("Codex Plugin", err)
	}
	if !found || !state.Installed {
		return &StatusCheck{
			Name:    "Codex Plugin",
			Status:  "fail",
			Message: "Plugin not installed",
			Hint:    "Run: basecamp setup codex",
		}
	}
	if !state.Enabled {
		return &StatusCheck{
			Name:    "Codex Plugin",
			Status:  "fail",
			Message: "Installed but disabled",
			Hint:    "Run: basecamp setup codex",
		}
	}
	return &StatusCheck{
		Name:    "Codex Plugin",
		Status:  "pass",
		Message: "Installed and enabled",
	}
}

// CheckCodexPluginVersion compares the installed plugin and CLI versions.
func CheckCodexPluginVersion() *StatusCheck {
	return CheckCodexPluginVersionContext(context.Background())
}

// CheckCodexPluginVersionContext compares plugin and CLI versions using the caller's context.
func CheckCodexPluginVersionContext(ctx context.Context) *StatusCheck {
	state, found, err := queryCodexPlugin(ctx)
	return codexPluginVersionCheck(state, found, err)
}

func codexPluginVersionCheck(state codexPluginState, found bool, err error) *StatusCheck {
	if err != nil {
		return codexQueryFailure("Codex Plugin Version", err)
	}
	if !found || !state.Installed {
		return &StatusCheck{
			Name:    "Codex Plugin Version",
			Status:  "skip",
			Message: "Skipped (plugin not installed)",
		}
	}
	if state.Version == "" {
		return &StatusCheck{
			Name:    "Codex Plugin Version",
			Status:  "fail",
			Message: "Installed plugin version unavailable",
			Hint:    "Run: basecamp setup codex",
		}
	}
	if version.Version == "dev" {
		return &StatusCheck{
			Name:    "Codex Plugin Version",
			Status:  "pass",
			Message: fmt.Sprintf("Installed (%s, dev build)", state.Version),
		}
	}
	if state.Version == version.Version {
		return &StatusCheck{
			Name:    "Codex Plugin Version",
			Status:  "pass",
			Message: fmt.Sprintf("Up to date (%s)", state.Version),
		}
	}
	return &StatusCheck{
		Name:    "Codex Plugin Version",
		Status:  "warn",
		Message: fmt.Sprintf("Mismatched (plugin %s, CLI %s)", state.Version, version.Version),
		Hint:    "Run: basecamp setup codex",
	}
}

func queryCodexPlugin(parent context.Context) (codexPluginState, bool, error) {
	path := FindCodexBinary()
	if path == "" {
		return codexPluginState{}, false, errCodexBinaryMissing
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	data, err := runCodexCommand(ctx, path, "plugin", "list", "--available", "--json")
	if err != nil {
		return codexPluginState{}, false, fmt.Errorf("query Codex plugins: %w", err)
	}

	var envelope struct {
		Installed *[]codexPluginState `json:"installed"`
		Available *[]codexPluginState `json:"available"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return codexPluginState{}, false, fmt.Errorf("parse Codex plugin list: %w", err)
	}
	if envelope.Installed == nil && envelope.Available == nil {
		return codexPluginState{}, false, errors.New("parse Codex plugin list: missing installed and available fields")
	}
	if envelope.Installed != nil {
		for _, plugin := range *envelope.Installed {
			if plugin.PluginID == CodexExpectedPluginKey {
				return plugin, true, nil
			}
		}
	}
	if envelope.Available != nil {
		for _, plugin := range *envelope.Available {
			if plugin.PluginID == CodexExpectedPluginKey {
				return plugin, true, nil
			}
		}
	}
	return codexPluginState{}, false, nil
}

func codexQueryFailure(name string, err error) *StatusCheck {
	if errors.Is(err, errCodexBinaryMissing) {
		return &StatusCheck{
			Name:    name,
			Status:  "fail",
			Message: "Codex executable not found",
			Hint:    "Install Codex, then run: basecamp setup codex",
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &StatusCheck{
			Name:    name,
			Status:  "fail",
			Message: "Cannot query Codex plugins",
			Hint:    "Run `codex plugin list --available --json`, then: basecamp setup codex",
		}
	}
	message := "Cannot query Codex plugins"
	if strings.HasPrefix(err.Error(), "parse ") {
		message = "Cannot parse Codex plugin list"
	}
	return &StatusCheck{
		Name:    name,
		Status:  "fail",
		Message: message,
		Hint:    "Run `codex plugin list --available --json`, then: basecamp setup codex",
	}
}
