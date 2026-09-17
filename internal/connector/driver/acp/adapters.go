package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/claude"
)

// Adapter is one ACP agent adapter at a pinned version: what it is called,
// what it may take from the connector's environment, and which of its modes
// is the asking mode for each connector permission mode. Mode ids are not
// portable across adapters, so they are named here and nowhere else.
type Adapter struct {
	// Name is the adapter's executable, as connect.json names it.
	Name string
	// Package is its npm package, and the agentInfo.name it reports at
	// initialize.
	Package string
	// Version is the pinned version, and the agentInfo.version it must report.
	Version string
	// Env names what the adapter may take from the connector's environment
	// besides driver.BaseEnv: where its agent's configuration lives and how it
	// authenticates. Exact names only. Nothing that swaps the agent binary the
	// adapter bundles (CLAUDE_CODE_EXECUTABLE, CODEX_PATH) is among them: the
	// pin covers the agent too.
	Env []string
	// SetEnv are variables the driver itself sets for the adapter: its own
	// switches, never a secret and never taken from the connector's
	// environment.
	SetEnv map[string]string
	// Modes maps a connector permission mode to the adapter's asking mode:
	// the mode in which the agent sends session/request_permission for what
	// it would otherwise do unasked. A permission mode with no entry cannot
	// be run.
	Modes map[driver.PermissionMode]string
	// SessionMeta is the _meta sent with session/new, session/load and
	// session/resume: the adapter's own switches, for what ACP itself cannot
	// say. Never a secret, never content.
	SessionMeta map[string]any
	// LoadSession is what the pinned version advertises, until a session
	// reports what the installed one does.
	LoadSession bool
}

// ClaudeAgentACP is Claude Code over ACP.
//
// Its asking mode is "default" (the adapter's "Manual": ask before every
// change, inside the working directory too). Its session _meta turns off the
// host's Claude Code settings, which would otherwise bring the host's
// defaultMode, allow rules and hooks into the session, and takes
// bypassPermissions out of the session's mode catalog altogether.
var ClaudeAgentACP = Adapter{
	Name:    "claude-agent-acp",
	Package: "@agentclientprotocol/claude-agent-acp",
	Version: "0.78.0",
	Env:     append([]string{}, claude.Env...),
	Modes: map[driver.PermissionMode]string{
		driver.ModeEditsInWorkDir: "default",
	},
	SessionMeta: map[string]any{
		"claudeCode": map[string]any{
			"options": map[string]any{
				"settingSources":                  []string{},
				"allowDangerouslySkipPermissions": false,
			},
		},
	},
	LoadSession: true,
}

// CodexACP is Codex over ACP.
//
// Its asking mode is "read-only" (the adapter's "Ask for approval"). Codex
// gates less than Claude in it: work inside the workspace goes through
// unasked, and only what reaches outside it is put to the policy. Same policy,
// different reach; neither is containment.
//
// codex-acp runs `codex app-server`, which has no --ignore-user-config, so
// the host's config is switched off where a session's config can do it
// (CODEX_CONFIG, which the adapter layers onto every thread it starts): the
// host's plugins, hooks and apps, its skills' instructions, and the parts of
// the environment a model's shell command would otherwise inherit.
//
// The adapter's modes fix the sandbox per turn, and "read-only" leaves /tmp
// and $TMPDIR writable: Codex writes there unasked, where the policy never
// sees it. The session also opens in the asking mode (INITIAL_AGENT_MODE)
// rather than in the adapter's default, before the driver sets and confirms
// it.
var CodexACP = Adapter{
	Name:    "codex-acp",
	Package: "@agentclientprotocol/codex-acp",
	Version: "1.12.0",
	Env:     []string{"CODEX_HOME", "OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL"},
	SetEnv:  map[string]string{"CODEX_CONFIG": codexConfig, "INITIAL_AGENT_MODE": "read-only"},
	Modes: map[driver.PermissionMode]string{
		driver.ModeEditsInWorkDir: "read-only",
	},
	LoadSession: true,
}

// codexConfig is the thread config codex-acp layers onto every session. The
// features are the ones the codex spawn driver disables; the same host
// surfaces reach an app-server thread.
const codexConfig = `{"features":{"apps":false,"plugins":false,"remote_plugin":false,"hooks":false,` +
	`"browser_use":false,"browser_use_external":false,"computer_use":false,"in_app_browser":false,` +
	`"image_generation":false,"memories":false,"skill_mcp_dependency_install":false,"tool_suggest":false},` +
	`"skills":{"bundled":{"enabled":false},"include_instructions":false},` +
	`"shell_environment_policy":{"inherit":"core"},"web_search":"disabled"}`

// Adapters are the pinned adapters the driver runs.
func Adapters() []Adapter { return []Adapter{ClaudeAgentACP, CodexACP} }

// AdapterNamed is the pinned adapter of that name.
func AdapterNamed(name string) (Adapter, bool) {
	for _, a := range Adapters() {
		if a.Name == name {
			return a, true
		}
	}
	return Adapter{}, false
}

// workerAdapters is the adapter for each worker connect.json names.
var workerAdapters = map[string]Adapter{
	"claude": ClaudeAgentACP,
	"codex":  CodexACP,
}

// ForWorker is the acp driver for a connect.json worker: its pinned adapter,
// located in adaptersDir (DefaultAdaptersDir when empty). lookup reads the
// connector's environment; os.LookupEnv when nil.
func ForWorker(worker, adaptersDir string, lookup func(string) (string, bool)) (*Driver, error) {
	a, ok := workerAdapters[worker]
	if !ok {
		return nil, fmt.Errorf("acp: no ACP adapter for worker %q", worker)
	}
	if adaptersDir == "" {
		dir, err := DefaultAdaptersDir(lookup)
		if err != nil {
			return nil, err
		}
		adaptersDir = dir
	}
	bin, err := Locate(adaptersDir, a)
	if err != nil {
		return nil, err
	}
	return New(Options{Adapter: a, Binary: bin, Lookup: lookup})
}

// ErrAdapterMissing is an adapter that is not installed where the connector
// was told to look.
var ErrAdapterMissing = errors.New("acp: adapter not installed")

// DefaultAdaptersDir is where `make acp-adapters` installs the pinned
// adapters unless told otherwise: $XDG_DATA_HOME/basecamp/acp-adapters, or
// ~/.local/share/basecamp/acp-adapters.
func DefaultAdaptersDir(lookup func(string) (string, bool)) (string, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if data, ok := lookup("XDG_DATA_HOME"); ok && filepath.IsAbs(data) {
		return filepath.Join(data, "basecamp", "acp-adapters"), nil
	}
	home, ok := lookup("HOME")
	if !ok || !filepath.IsAbs(home) {
		return "", errors.New("acp: no home directory to find the adapters under")
	}
	return filepath.Join(home, ".local", "share", "basecamp", "acp-adapters"), nil
}

// Locate finds adapter a installed in dir (an npm prefix, as `npm ci --prefix
// dir` makes one) and checks it is the pinned version. It never installs
// anything: an adapter is downloaded when an operator installs it, never when
// a task is dispatched.
func Locate(dir string, a Adapter) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("acp: the adapters directory %q is not absolute", dir)
	}
	manifest := filepath.Join(dir, "node_modules", filepath.FromSlash(a.Package), "package.json")
	raw, err := os.ReadFile(manifest) //nolint:gosec // G304: the operator's adapters directory
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: %s@%s is not in %s (run make acp-adapters)", ErrAdapterMissing, a.Package, a.Version, dir)
		}
		return "", fmt.Errorf("acp: read %s: %w", manifest, err)
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return "", fmt.Errorf("acp: read %s: %w", manifest, err)
	}
	if pkg.Name != a.Package || pkg.Version != a.Version {
		return "", fmt.Errorf("acp: %s has %s@%s installed; the connector is pinned to %s@%s", dir, pkg.Name, pkg.Version, a.Package, a.Version)
	}
	bin := filepath.Join(dir, "node_modules", ".bin", a.Name)
	info, err := os.Stat(bin)
	if err != nil {
		return "", fmt.Errorf("%w: %s has no %s executable: %w", ErrAdapterMissing, dir, a.Name, err)
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%w: %s is not executable", ErrAdapterMissing, bin)
	}
	return bin, nil
}
