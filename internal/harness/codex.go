package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// CodexPluginName is the plugin identifier to install. It was "basecamp"
	// until the 37signals marketplace gave that name to the hosted-connector
	// plugin; see CodexLegacyPluginKey.
	CodexPluginName = "basecamp-cli"
	// CodexMarketplaceName is the marketplace name published by 37signals.
	CodexMarketplaceName = "37signals"
	// CodexExpectedPluginKey is the fully qualified Basecamp plugin ID.
	CodexExpectedPluginKey = CodexPluginName + "@" + CodexMarketplaceName
	// CodexLegacyPluginKey is the pre-rename plugin ID. It now also names
	// the hosted-connector plugin, so an install under it is this CLI's only
	// when its cached manifest says so (see codexLegacyCLIInstalled).
	CodexLegacyPluginKey = "basecamp@" + CodexMarketplaceName

	// codexQueryTimeout bounds how long the Codex probe may run.
	codexQueryTimeout = 5 * time.Second
	// codexWaitDelay is the grace period after the group kill before the
	// probe gives up on a pipe some escaped descendant still holds open.
	codexWaitDelay = time.Second
)

var (
	codexLookPath   = exec.LookPath
	runCodexCommand = func(ctx context.Context, path string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // path comes from exec.LookPath
		startInOwnProcessGroup(cmd)
		cmd.WaitDelay = codexWaitDelay
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}

		// `codex` is routinely a wrapper (an npm exec launcher, a mise shim)
		// that exits at once and leaves a descendant holding the inherited
		// stdout. That descendant is why the output is read here rather
		// than through Output: the exec package stops watching the context
		// once the direct child exits, so cmd.Cancel never fires for a
		// deadline that expires after that. The group kill below is the
		// only one, and it runs strictly before Wait on this goroutine: the
		// group ID is the leader's PID, reserved only until the leader is
		// reaped, so a kill issued from cmd.Cancel could race the reap and
		// land on a recycled ID. Once Wait begins, a leader still running
		// is killed alone by the exec package's own cancel.
		read := make(chan codexRead, 1)
		go func() {
			data, err := io.ReadAll(stdout)
			read <- codexRead{data: data, err: err}
		}()

		var out codexRead
		select {
		case out = <-read:
		case <-ctx.Done():
			_ = killProcessGroup(cmd)
			select {
			case out = <-read:
			case <-time.After(codexWaitDelay):
				// A descendant that left the group (setsid) is out of reach
				// and still holds the pipe; closing our end ends the read.
				_ = stdout.Close()
				out = <-read
			}
		}
		waitErr := cmd.Wait()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if waitErr != nil {
			return nil, waitErr
		}
		return out.data, out.err
	}
)

// codexRead is what the stdout reader hands back: everything the probe
// wrote, and the error that ended the read.
type codexRead struct {
	data []byte
	err  error
}

var (
	errCodexBinaryMissing = errors.New("codex executable not found")
	errCodexParse         = errors.New("parse Codex plugin list")
)

type codexPluginState struct {
	PluginID    string `json:"pluginId"`
	Name        string `json:"name"`
	Marketplace string `json:"marketplaceName"`
	Version     string `json:"version"`
	Installed   bool   `json:"installed"`
	Enabled     bool   `json:"enabled"`

	// legacyInstalled is set, on the state queryCodexPlugin returns, when
	// this CLI's plugin is still installed under CodexLegacyPluginKey.
	legacyInstalled bool
}

func init() {
	RegisterAgent(AgentInfo{
		Name:       "Codex",
		ID:         "codex",
		Detect:     DetectCodex,
		FindBinary: FindCodexBinary,
		Checks: func() []*StatusCheck {
			return []*StatusCheck{CheckCodexPlugin()}
		},
		Diagnostics: CheckCodexPluginDiagnosticsContext,
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
		if state.legacyInstalled {
			return &StatusCheck{
				Name:    "Codex Plugin",
				Status:  "fail",
				Message: "Installed under the old name " + CodexLegacyPluginKey,
				Hint:    "Run: basecamp setup codex (reinstalls it as " + CodexExpectedPluginKey + ")",
			}
		}
		return &StatusCheck{
			Name:    "Codex Plugin",
			Status:  "fail",
			Message: "Plugin not installed",
			Hint:    "Run: basecamp setup codex",
		}
	}
	if !state.Enabled {
		message := "Installed but disabled"
		if state.legacyInstalled {
			message += "; the old " + CodexLegacyPluginKey + " copy is still installed too"
		}
		return &StatusCheck{
			Name:    "Codex Plugin",
			Status:  "fail",
			Message: message,
			Hint:    "Run: basecamp setup codex",
		}
	}
	if state.legacyInstalled {
		return &StatusCheck{
			Name:    "Codex Plugin",
			Status:  "warn",
			Message: "Installed, but the old " + CodexLegacyPluginKey + " copy is still installed too",
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
	ctx, cancel := context.WithTimeout(parent, codexQueryTimeout)
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
		return codexPluginState{}, false, fmt.Errorf("%w: %w", errCodexParse, err)
	}
	if envelope.Installed == nil && envelope.Available == nil {
		return codexPluginState{}, false, fmt.Errorf("%w: missing installed and available fields", errCodexParse)
	}
	legacy := false
	if envelope.Installed != nil {
		for _, plugin := range *envelope.Installed {
			if plugin.PluginID == CodexLegacyPluginKey && plugin.Installed && codexLegacyCLIInstalled(plugin.Version) {
				legacy = true
			}
		}
		for _, plugin := range *envelope.Installed {
			if plugin.PluginID == CodexExpectedPluginKey {
				plugin.legacyInstalled = legacy
				return plugin, true, nil
			}
		}
	}
	if envelope.Available != nil {
		for _, plugin := range *envelope.Available {
			if plugin.PluginID == CodexExpectedPluginKey {
				plugin.legacyInstalled = legacy
				return plugin, true, nil
			}
		}
	}
	return codexPluginState{legacyInstalled: legacy}, false, nil
}

// CodexLegacyCLIInstalled reports whether this CLI's plugin is still installed
// in Codex under CodexLegacyPluginKey. It reads the plugin list itself, so
// callers that already hold a query result should use its state instead.
func CodexLegacyCLIInstalled(ctx context.Context) bool {
	state, _, err := queryCodexPlugin(ctx)
	return err == nil && state.legacyInstalled
}

// codexLegacyCLIInstalled reports whether the cached copy of the
// CodexLegacyPluginKey install at version is this CLI's plugin. Codex's
// plugin list reports the marketplace's current source for an ID, not the
// source the installed copy came from — after the rename that is the hosted
// connector's — so the installed manifest is the only reliable witness. A
// manifest that can't be read is not counted: leaving a legacy CLI install
// in place beats removing a hosted-connector one.
func codexLegacyCLIInstalled(version string) bool {
	if version == "" || strings.ContainsAny(version, `/\`) || version == "." || version == ".." {
		return false
	}
	home := codexHome()
	if home == "" {
		return false
	}
	name := strings.TrimSuffix(CodexLegacyPluginKey, "@"+CodexMarketplaceName)
	return isCLIPluginManifest(filepath.Join(home, "plugins", "cache", CodexMarketplaceName, name, version, ".codex-plugin", "plugin.json"))
}

// codexHome is $CODEX_HOME, or ~/.codex.
func codexHome() string {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Clean(home)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(filepath.Clean(home), ".codex")
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
	if errors.Is(err, errCodexParse) {
		message = "Cannot parse Codex plugin list"
	}
	return &StatusCheck{
		Name:    name,
		Status:  "fail",
		Message: message,
		Hint:    "Run `codex plugin list --available --json`, then: basecamp setup codex",
	}
}
