package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

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
	// MCPStatus is how the adapter tells the client whether the session's MCP
	// servers connected: MCPStatusInit (the agent's init message, which must
	// report every server connected before the first turn ends) or
	// MCPStatusStartupFailures (a failed startup is reported, success is
	// not). The driver ends a session whose server did not connect.
	MCPStatus MCPStatus
	// Readback is how the adapter is asked for its own account of the MCP
	// configuration the session is running, and how that answer is read. It
	// is the session's one check on the boundary, made after the adapter is
	// running (see mcp.go).
	Readback Readback
	// Preflight refuses, before anything starts, a session the adapter would
	// run with configuration the connector cannot switch off: nil when there is
	// none to check. read is how it reads a configuration file, os.ReadFile
	// when nil — the seam a caller stands in when the session's working
	// directory is not where the files to check are. Doctor stood in it to
	// read a planned worktree's files out of the repository; with worktrees
	// gone, every caller passes nil and reads the working directory itself.
	Preflight func(cwd string, lookup func(string) (string, bool), read func(string) ([]byte, error)) error
}

// ClaudeAgentACP is Claude Code over ACP.
//
// Its asking mode is "default" (the adapter's "Manual": ask before every
// change, inside the working directory too). Its session _meta turns off the
// host's Claude Code settings, which would otherwise bring the host's
// defaultMode, allow rules and hooks into the session; takes
// bypassPermissions out of the session's mode catalog altogether; and makes
// the session's mcpServers the only MCP servers it has (strictMcpConfig), so
// a user-scope or project .mcp.json server, one named basecamp among them,
// never loads beside or instead of the connector's.
var ClaudeAgentACP = Adapter{
	Name:    "claude-agent-acp",
	Package: "@agentclientprotocol/claude-agent-acp",
	Version: "0.78.0",
	Env:     append([]string{}, claude.Env...),
	Modes: map[driver.PermissionMode]string{
		driver.ModeEdits: "default",
	},
	SessionMeta: map[string]any{
		"claudeCode": map[string]any{
			"emitRawSDKMessages": []map[string]string{{"type": "system", "subtype": "init"}},
			"options": map[string]any{
				"settingSources":                  []string{},
				"allowDangerouslySkipPermissions": false,
				"strictMcpConfig":                 true,
				// A plan-mode switch is the model leaving the mode the driver
				// verified, which ends the session; the worker has no one to
				// present a plan to anyway.
				"disallowedTools": []string{"EnterPlanMode", "ExitPlanMode"},
			},
		},
	},
	// Claude Code's init message, and only it, is forwarded: the driver
	// reads each MCP server's name and status from it and nothing else.
	MCPStatus:   MCPStatusInit,
	Readback:    Readback{Command: "/mcp", Parse: claudeMCPReport},
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
	SetEnv: map[string]string{
		"CODEX_CONFIG":       codexConfig,
		"INITIAL_AGENT_MODE": "read-only",
		// Without it, codex-acp drops a requested MCP server whose name any
		// config layer already uses, and the agent gets that one instead.
		"DISABLE_MCP_CONFIG_FILTERING": "true",
	},
	Preflight: codexPreflight,
	MCPStatus: MCPStatusStartupFailures,
	// codex brings its own apps connector, which its /mcp lists whatever the
	// session declared. Its tools are not offered to the session's model
	// (features.apps is false in codexConfig, and compatibility check 9 asks
	// the agent what it can call), so it is named here and nothing else is.
	Readback: Readback{Command: "/mcp", Parse: codexMCPReport, BuiltIn: []string{"codex_apps"}},
	Modes: map[driver.PermissionMode]string{
		driver.ModeEdits: "read-only",
	},
	LoadSession: true,
}

// Readback is how an adapter is asked what MCP configuration it is actually
// running, and how its answer is read. Both pinned adapters answer a command
// of their own — claude-agent-acp's and codex-acp's "/mcp" — and both answer
// it themselves, without the model: the turn costs no tokens, and the answer
// is the adapter's, not something a prompt could talk it into.
type Readback struct {
	// Command is the prompt that asks for it. Empty means the adapter cannot
	// be asked, and a session on it can only be checked as it runs.
	Command string
	// Parse reads the adapter's answer. An answer it cannot read is a session
	// this driver will not vouch for, so a parse error ends the session.
	Parse func(text string) (MCPReport, error)
	// BuiltIn are servers the pinned adapter brings itself, which are in its
	// answer whatever the session declared. Each one is here because its
	// tools are not offered to the model — proven, per adapter, by the
	// compatibility check — and for no other reason.
	BuiltIn []string
}

// MCPReport is an adapter's own account of the MCP configuration a session is
// running. An adapter that names its servers fills Names; one that only counts
// them fills Count and Unusable.
type MCPReport struct {
	Names    []string
	Count    int
	Unusable int
}

// ErrMCPReadback is an adapter whose account of its own MCP configuration
// cannot be read, or does not match what the session declared.
var ErrMCPReadback = fmt.Errorf("%w: the agent is not running the MCP configuration the session declared", driver.ErrSessionUnverified)

// claudeMCPReport reads claude-agent-acp's answer, which counts the servers
// rather than naming them: "1 MCP server(s): 1 connected, 0 not connected, 0
// disabled."
var claudeMCPCounts = regexp.MustCompile(`(\d+) MCP server\(s\): (\d+) connected, (\d+) not connected, (\d+) disabled`)

func claudeMCPReport(text string) (MCPReport, error) {
	m := claudeMCPCounts.FindStringSubmatch(text)
	if m == nil {
		return MCPReport{}, fmt.Errorf("%w: its answer does not count them", ErrMCPReadback)
	}
	total, err1 := strconv.Atoi(m[1])
	connected, err2 := strconv.Atoi(m[2])
	unconnected, err3 := strconv.Atoi(m[3])
	disabled, err4 := strconv.Atoi(m[4])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return MCPReport{}, fmt.Errorf("%w: its counts are not numbers", ErrMCPReadback)
	}
	if connected+unconnected+disabled != total {
		return MCPReport{}, fmt.Errorf("%w: its counts do not add up", ErrMCPReadback)
	}
	return MCPReport{Count: total, Unusable: unconnected + disabled}, nil
}

// codexMCPReport reads codex-acp's answer, which names them:
//
//	Configured MCP servers:
//	- codex_apps: 49 tools, 27 resources, auth=bearerToken
//	- basecamp
var codexMCPHeader = "Configured MCP servers:"

func codexMCPReport(text string) (MCPReport, error) {
	_, list, found := strings.Cut(text, codexMCPHeader)
	if !found {
		return MCPReport{}, fmt.Errorf("%w: its answer does not list them", ErrMCPReadback)
	}
	report := MCPReport{}
	for _, line := range strings.Split(list, "\n") {
		line = strings.TrimSpace(line)
		name, ok := strings.CutPrefix(line, "- ")
		if !ok {
			continue
		}
		if before, _, cut := strings.Cut(name, ":"); cut {
			name = before
		}
		if name = strings.TrimSpace(name); name != "" {
			report.Names = append(report.Names, name)
		}
	}
	if len(report.Names) == 0 {
		return MCPReport{}, fmt.Errorf("%w: it listed no server at all", ErrMCPReadback)
	}
	report.Count = len(report.Names)
	return report, nil
}

// MCPStatus names how an adapter reports its MCP servers' startup.
type MCPStatus string

const (
	// MCPStatusInit: claude-agent-acp forwards Claude Code's system/init
	// message, with each MCP server's status, as a _claude/sdkMessage
	// notification when the session asks for it.
	MCPStatusInit MCPStatus = "init"
	// MCPStatusStartupFailures: codex-acp reports a server that failed or
	// was canceled at startup as a failed tool call named
	// mcp_startup.<server>.
	MCPStatusStartupFailures MCPStatus = "startup_failures"
)

// ErrMCPServerNotConnected is a session whose MCP server did not connect: the
// worker would run without the tools the connector gave it, the Basecamp
// tools and its task token among them.
var ErrMCPServerNotConnected = fmt.Errorf("%w: an MCP server of the session did not connect", driver.ErrSessionUnverified)

// ErrForeignMCPConfig is agent configuration that declares MCP servers of its
// own, which the connector cannot keep out of a session.
var ErrForeignMCPConfig = errors.New("acp: the agent's configuration declares MCP servers of its own")

// ErrConfigUnreadable is an agent configuration file that is there and
// cannot be read. It refuses a session as a declaration does — the agent
// may read what this cannot, and a file nobody can read is not a file that
// declares nothing — but it is not a declaration, and what a person does
// about it is not what they do about one, so it is its own error and not
// ErrForeignMCPConfig.
var ErrConfigUnreadable = errors.New("acp: an agent configuration file cannot be read")

// unreadableConfigError is one such file.
type unreadableConfigError struct {
	file string
	err  error
}

func (e *unreadableConfigError) Error() string {
	return fmt.Sprintf("acp: %s cannot be read, so it cannot be said to declare no MCP server of its own: %v", e.file, e.err)
}

func (e *unreadableConfigError) Unwrap() []error { return []error{ErrConfigUnreadable, e.err} }

// refusalsError is every reason one session was refused: one line to read,
// and every one of them still to errors.Is.
type refusalsError struct{ errs []error }

func (e *refusalsError) Error() string {
	parts := make([]string, 0, len(e.errs))
	for i, err := range e.errs {
		msg := err.Error()
		if i > 0 {
			// The package's prefix is on each of them; once is enough to read.
			msg = strings.TrimPrefix(msg, "acp: ")
		}
		parts = append(parts, msg)
	}
	return strings.Join(parts, "; ")
}

func (e *refusalsError) Unwrap() []error { return e.errs }

// Refusals is every reason a preflight refused a session, one error each:
// the refusals of a joined refusal, and err itself when it is one refusal or
// something else entirely. A caller that reports a session's refusals — a
// doctor that groups them by the routes they affect — needs them apart, and
// reading them out of a joined message would be reading a message.
func Refusals(err error) []error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(*refusalsError); ok { //nolint:errorlint // this is the join itself, not something wrapping one
		return joined.errs
	}
	return []error{err}
}

// refused is every refusal as one error, and nil when there is none.
func refused(errs []error) error {
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	default:
		return &refusalsError{errs: errs}
	}
}

// escapedTOMLKey is a table header or a key whose name carries a backslash
// escape.
var escapedTOMLKey = regexp.MustCompile(`^\s*(\[\[?[^\]]*\\|[^=\n]*\\[^=\n]*=)`)

// codexPreflight refuses a session when a Codex config layer declares MCP
// servers: the user's ($CODEX_HOME, or ~/.codex), the system's, or a
// project's .codex/config.toml in the working directory or above it. Codex
// merges every layer into the session, and a server declared there would run
// beside the connector's, or, named basecamp, in place of it with every tool
// allowed; in the asking mode its tool calls need not be put to the policy at
// all.
//
// It reads every layer rather than stopping at the first that refuses: a
// person fixing this gets every file to change out of one run.
//
// It reads for the name, not the TOML: the name anywhere in the file — a
// table header, a dotted key, an inline table, a profile, a comment — refuses
// the session. Parsing it would mean matching Codex's own merge of profiles,
// includes and overrides, and being wrong there is being wrong in the
// direction that runs a foreign server. A false alarm refuses a session; a
// miss would not.
//
// It covers the layers a file on this machine can hold. Codex also takes
// configuration from layers this cannot read — an MDM profile, a cloud-managed
// config, a plugin — so it is a guard, not a proof. What would be a proof is
// the effective configuration the app server reports, which ACP does not carry.
func codexPreflight(cwd string, lookup func(string) (string, bool), read func(string) ([]byte, error)) error {
	if read == nil {
		read = os.ReadFile
	}
	var files []string
	home := ""
	if v, ok := lookup("CODEX_HOME"); ok && v != "" {
		// Codex reads a relative CODEX_HOME against the working directory.
		home = v
		if !filepath.IsAbs(v) {
			home = filepath.Join(cwd, v)
		}
	} else if v, ok := lookup("HOME"); ok && filepath.IsAbs(v) {
		home = filepath.Join(v, ".codex")
	}
	if home != "" {
		files = append(files, filepath.Join(home, "config.toml"), filepath.Join(home, "managed_config.toml"))
	}
	files = append(files, "/etc/codex/config.toml", "/etc/codex/managed_config.toml")
	for dir := filepath.Clean(cwd); ; dir = filepath.Dir(dir) {
		files = append(files, filepath.Join(dir, ".codex", "config.toml"))
		if filepath.Dir(dir) == dir {
			break
		}
	}
	// A working directory under the home directory names the user's layer
	// twice; a layer is read, and refused, once.
	files = slices.Compact(slices.Sorted(slices.Values(files)))

	// Every layer is read, and every one that refuses the session is
	// reported: what a person has to change is all of it, and naming the
	// first would have them run this again for the next.
	var refusals []error
	for _, file := range files {
		raw, err := read(file)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			// A file that is there and cannot be read is not a file this can
			// say anything about, and Codex may read it where this cannot.
			refusals = append(refusals, &unreadableConfigError{file: file, err: err})
			continue
		}
		text := strings.TrimPrefix(string(raw), "\ufeff")
		if strings.Contains(text, "mcp_servers") {
			refusals = append(refusals, fmt.Errorf("%w: %s (codex-acp would load them into the session)", ErrForeignMCPConfig, file))
			continue
		}
		for _, line := range strings.Split(text, "\n") {
			if escapedTOMLKey.MatchString(line) {
				// TOML decodes escapes in a quoted key, so "mcp\u005fservers"
				// is mcp_servers to Codex and something else to a reader. A
				// key this cannot read plainly is refused rather than guessed.
				refusals = append(refusals, fmt.Errorf("%w: %s has a key this cannot read (an escape in a quoted key)", ErrForeignMCPConfig, file))
				break
			}
		}
	}
	return refused(refusals)
}

// codexConfig is the thread config codex-acp layers onto every session. The
// features are the ones the codex spawn driver disables; the same host
// surfaces reach an app-server thread.
const codexConfig = `{"features":{"apps":false,"plugins":false,"remote_plugin":false,"hooks":false,` +
	`"browser_use":false,"browser_use_external":false,"computer_use":false,"in_app_browser":false,` +
	`"image_generation":false,"memories":false,"skill_mcp_dependency_install":false,"tool_suggest":false},` +
	`"skills":{"bundled":{"enabled":false},"include_instructions":false},` +
	`"shell_environment_policy":{"inherit":"core"},"web_search":"disabled"}`

// Preflight is adapter a's own refusal of a session it would not start,
// made before anything starts and run here for a session that would work in
// cwd: nil for an adapter that has nothing on this machine to check, and
// nil when the adapter would start there.
//
// The environment it reads is the one the adapter would run in —
// driver.BaseEnv and the adapter's own names, read with lookup (os.LookupEnv
// when nil), under the driver's own switches — so what the preflight
// resolves its configuration against (a CODEX_HOME, a HOME) is what the
// adapter will. A dispatch runs the same preflight against the session's
// environment, which is this one plus what the dispatcher gives a session
// (see Driver.open); anything outside a dispatch — doctor — asks here, and
// gets the answer a dispatch would rather than one read from its own
// environment. read is how a configuration file is read, os.ReadFile when
// nil: a caller checking a directory that does not exist yet reads its
// files from wherever they will come from.
func Preflight(a Adapter, cwd string, lookup func(string) (string, bool), read func(string) ([]byte, error)) error {
	if a.Preflight == nil {
		return nil
	}
	if lookup == nil {
		lookup = os.LookupEnv
	}
	env := mergeEnv(driver.BuildEnv(driver.BaseEnv, lookup, nil), driver.BuildEnv(a.Env, lookup, nil))
	return a.Preflight(cwd, lookupIn(setEnv(env, a.SetEnv)), read)
}

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

// AdapterForWorker is the pinned adapter the acp driver runs for a
// connect.json worker. It is how anything outside this package — doctor
// among them — names the adapter a profile would run, rather than spelling
// the naming rule again.
func AdapterForWorker(worker string) (Adapter, bool) {
	a, ok := workerAdapters[worker]
	return a, ok
}

// ForWorker is the acp driver for a connect.json worker: its pinned adapter,
// located in adaptersDir (DefaultAdaptersDir when empty). lookup reads the
// connector's environment; os.LookupEnv when nil.
func ForWorker(worker, adaptersDir string, lookup func(string) (string, bool)) (*Driver, error) {
	a, ok := AdapterForWorker(worker)
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
