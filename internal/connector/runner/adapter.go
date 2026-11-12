package runner

import (
	"cmp"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

// Adapter is one coding agent's command line: what a turn of a headless
// session is started with, and how its event stream reads. An adapter starts
// nothing and keeps nothing between turns; the runner does both.
//
// Under PermissionsFull an adapter starts its CLI with permission prompts
// off: a headless turn has nobody to answer one, and an agent that cannot run
// `basecamp` or `git` can neither read its request nor reply to it. Under
// PermissionsGuarded it starts the CLI under that CLI's own unattended
// guardrails, where it has any (Guarded).
type Adapter interface {
	// Name is the adapter's name as flags and runner.json spell it.
	Name() string
	// Binary is the CLI's executable, looked up on PATH.
	Binary() string
	// NewSessionID is the id a new session is started under, for a CLI that
	// lets its caller choose one, and empty for one that names its own
	// sessions. A chosen id is not a session until the CLI's stream says so.
	NewSessionID() string
	// Args are the arguments of one turn. The prompt is not among them: it
	// goes in on standard input, so nothing a person wrote is ever in argv.
	Args(t Turn) []string
	// Parse reads one line of the CLI's standard output. A line it does not
	// know is no events and no error: the streams grow new line types.
	Parse(line []byte) []Event
	// Guarded reports whether the CLI has guardrails of its own to run
	// unattended under. One without cannot be started under
	// PermissionsGuarded: running it wide open under that name would be
	// worse than refusing.
	Guarded() bool
}

// Role is which of the runner's two kinds of session a turn belongs to.
type Role string

const (
	RoleOrchestrator Role = "orchestrator"
	RoleWorker       Role = "worker"
)

// Turn is one prompt of one session.
type Turn struct {
	Role Role
	Spec Spec
	// Resume is the id of the session to continue. Empty starts a new one,
	// under NewID where the adapter chooses ids.
	Resume string
	NewID  string
	// Dir is the directory the session runs in. A session is continued from
	// the directory it was started in.
	Dir string
	// Permissions is what the agent may do. Empty means PermissionsFull.
	Permissions Permissions
}

// guarded reports whether the turn runs under its CLI's own guardrails.
func (t Turn) guarded() bool { return t.Permissions == PermissionsGuarded }

// EventKind names what a line of an agent's stream said.
type EventKind int

const (
	// EventSession is the CLI confirming the session, with its id.
	EventSession EventKind = iota + 1
	// EventText is something the agent said. The last one of a turn is its
	// answer unless an EventResult carries one.
	EventText
	// EventTool is the agent starting a tool call.
	EventTool
	// EventResult is the end of the turn.
	EventResult
	// EventError is the CLI reporting a failure. A turn that reports one and
	// no successful result after it has failed.
	EventError
	// EventUsage is cost reported apart from the turn's end.
	EventUsage
)

// Event is one thing an agent's stream said.
type Event struct {
	Kind      EventKind
	SessionID string
	Model     string
	// Text is what was said (EventText), the turn's answer when the CLI
	// repeats it (EventResult), or the failure (EventError).
	Text string
	// Tool is the tool's name (EventTool).
	Tool string
	// Failed says the turn ended in an error (EventResult).
	Failed bool
	Usage  Usage
	// SessionCost says Usage.CostUSD is what the whole session has cost so
	// far, not what this turn did: Claude Code reports a running total.
	SessionCost bool
	// Refused names the tools whose calls the CLI refused during the turn,
	// one entry per call (EventResult), where the CLI says. Prompts are off,
	// but a rule the person wrote to deny something still holds.
	Refused []string
}

// Usage is what a turn cost, as far as the CLI says.
type Usage struct {
	InputTokens  int64   `json:"input_tokens,omitempty"`
	OutputTokens int64   `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
}

func (u Usage) plus(v Usage) Usage {
	return Usage{InputTokens: u.InputTokens + v.InputTokens, OutputTokens: u.OutputTokens + v.OutputTokens, CostUSD: u.CostUSD + v.CostUSD}
}

var adapters = []Adapter{claudeAdapter{}, codexAdapter{}, piAdapter{}}

// AdapterNames lists the built-in adapters.
func AdapterNames() []string {
	names := make([]string, 0, len(adapters))
	for _, a := range adapters {
		names = append(names, a.Name())
	}
	return names
}

func adapterByName(name string) Adapter {
	i := slices.IndexFunc(adapters, func(a Adapter) bool { return a.Name() == name })
	if i < 0 {
		return nil
	}
	return adapters[i]
}

// newUUID is a random version 4 UUID, the form Claude Code and Pi take a
// session id in.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// --- Claude Code ------------------------------------------------------------

// claudeAdapter drives `claude -p` with its stream-json output.
type claudeAdapter struct{}

func (claudeAdapter) Name() string         { return "claude" }
func (claudeAdapter) Binary() string       { return "claude" }
func (claudeAdapter) NewSessionID() string { return newUUID() }

// Claude Code's guardrail is its auto mode: it judges each action itself,
// runs the ones it finds safe and stops the rest, with no prompt.
func (claudeAdapter) Guarded() bool { return true }

func (claudeAdapter) Args(t Turn) []string {
	mode := "bypassPermissions"
	if t.guarded() {
		mode = "auto"
	}
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--permission-mode", mode}
	if t.Resume != "" {
		args = append(args, "--resume", t.Resume)
	} else if t.NewID != "" {
		args = append(args, "--session-id", t.NewID)
	}
	if t.Spec.Model != "" {
		args = append(args, "--model", t.Spec.Model)
	}
	if t.Spec.Effort != "" {
		args = append(args, "--effort", t.Spec.Effort)
	}
	if t.Role == RoleOrchestrator {
		// Claude Code has the two tools the skill names, a monitor and
		// background subagents, and would reach for them. Here the connector
		// is the monitor and `basecamp connect dispatch` starts the worker,
		// on whichever agent the person chose for it.
		args = append(args, "--disallowedTools", "Task,Agent,Monitor")
	}
	return args
}

func (claudeAdapter) Parse(line []byte) []Event {
	var m struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		SessionID string `json:"session_id"`
		Model     string `json:"model"`
		Message   struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Result  string   `json:"result"`
		Errors  []string `json:"errors"`
		Denials []struct {
			Tool string `json:"tool_name"`
		} `json:"permission_denials"`
		IsError bool    `json:"is_error"`
		Cost    float64 `json:"total_cost_usd"`
		Usage   struct {
			Input         int64 `json:"input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			Output        int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(line, &m) != nil {
		return nil
	}
	switch m.Type {
	case "system":
		if m.Subtype == "init" && m.SessionID != "" {
			return []Event{{Kind: EventSession, SessionID: m.SessionID, Model: m.Model}}
		}
	case "assistant":
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Name string `json:"name"`
		}
		if json.Unmarshal(m.Message.Content, &parts) != nil {
			return nil
		}
		var events []Event
		for _, part := range parts {
			switch part.Type {
			case "text":
				if strings.TrimSpace(part.Text) != "" {
					events = append(events, Event{Kind: EventText, Text: part.Text})
				}
			case "tool_use":
				events = append(events, Event{Kind: EventTool, Tool: part.Name})
			}
		}
		return events
	case "result":
		text := m.Result
		if m.IsError && text == "" {
			text = strings.Join(m.Errors, "; ")
		}
		if m.IsError && text == "" {
			text = m.Subtype
		}
		// The id on this line is not passed on: when a session asked for
		// could not be found it is the id that was asked for, and only the
		// init line says a session is open.
		var refused []string
		for _, denial := range m.Denials {
			refused = append(refused, denial.Tool)
		}
		return []Event{{
			Kind: EventResult, Text: text, Failed: m.IsError, SessionCost: true, Refused: refused,
			Usage: Usage{InputTokens: m.Usage.Input + m.Usage.CacheCreation + m.Usage.CacheRead, OutputTokens: m.Usage.Output, CostUSD: m.Cost},
		}}
	}
	return nil
}

// --- Codex ------------------------------------------------------------------

// codexAdapter drives `codex exec --json`, and `codex exec resume` for a
// session's later turns.
type codexAdapter struct{}

func (codexAdapter) Name() string   { return "codex" }
func (codexAdapter) Binary() string { return "codex" }

// Codex names its own sessions: the id arrives on the stream's first line.
func (codexAdapter) NewSessionID() string { return "" }

// Codex's guardrail is its sandbox with automatic review. A command starts
// in the sandbox, which reads anything and writes only in the folder it works
// in and the temporary directory, with the network on, since nothing reaches
// Basecamp without it. When the sandbox stops one, the agent may ask to run
// it outside, and Codex's own reviewer answers, not a person. It is the
// counterpart of Claude Code's auto mode, and without the review a sandboxed
// agent could neither commit in a repo (its git data is read-only there) nor
// read the profile's connect.json (inside the sandbox its ownership cannot
// be checked).
func (codexAdapter) Guarded() bool { return true }

func (codexAdapter) Args(t Turn) []string {
	args := []string{"exec"}
	if t.Resume != "" {
		args = append(args, "resume", t.Resume)
	}
	args = append(args, "--json", "--skip-git-repo-check")
	if t.guarded() {
		// As configuration, not as --sandbox and --approve-for-me: `codex
		// exec resume` has neither flag, and takes configuration the same
		// as `codex exec`.
		args = append(args,
			"-c", `sandbox_mode="workspace-write"`,
			"-c", "sandbox_workspace_write.network_access=true",
			"-c", `approval_policy="on-request"`,
			"-c", `approvals_reviewer="auto_review"`)
	} else {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	if t.Spec.Model != "" {
		args = append(args, "-m", t.Spec.Model)
	}
	if t.Spec.Effort != "" {
		// The value is TOML, so the level is a quoted string.
		args = append(args, "-c", `model_reasoning_effort="`+t.Spec.Effort+`"`)
	}
	// The prompt is read from standard input.
	return append(args, "-")
}

func (codexAdapter) Parse(line []byte) []Event {
	var m struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
		Message  string `json:"message"`
		Error    struct {
			Message string `json:"message"`
		} `json:"error"`
		Item struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Tool string `json:"tool"`
		} `json:"item"`
		Usage struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(line, &m) != nil {
		return nil
	}
	switch m.Type {
	case "thread.started":
		if m.ThreadID != "" {
			return []Event{{Kind: EventSession, SessionID: m.ThreadID}}
		}
	case "item.started":
		switch m.Item.Type {
		case "command_execution":
			return []Event{{Kind: EventTool, Tool: "shell"}}
		case "mcp_tool_call":
			return []Event{{Kind: EventTool, Tool: m.Item.Tool}}
		case "file_change", "web_search":
			return []Event{{Kind: EventTool, Tool: m.Item.Type}}
		}
	case "item.completed":
		// An item of type "error" is a warning the turn goes on after; only
		// the line-level "error" and "turn.failed" end it.
		if m.Item.Type == "agent_message" && strings.TrimSpace(m.Item.Text) != "" {
			return []Event{{Kind: EventText, Text: m.Item.Text}}
		}
	case "turn.completed":
		return []Event{{Kind: EventResult, Usage: Usage{InputTokens: m.Usage.Input, OutputTokens: m.Usage.Output}}}
	case "turn.failed":
		return []Event{{Kind: EventError, Text: m.Error.Message}}
	case "error":
		return []Event{{Kind: EventError, Text: m.Message}}
	}
	return nil
}

// --- Pi ---------------------------------------------------------------------

// piAdapter drives `pi -p --mode json`.
type piAdapter struct{}

func (piAdapter) Name() string         { return "pi" }
func (piAdapter) Binary() string       { return "pi" }
func (piAdapter) NewSessionID() string { return newUUID() }

// Pi has no permission prompts to turn off, and no guardrails to run under.
func (piAdapter) Guarded() bool { return false }

func (piAdapter) Args(t Turn) []string {
	args := []string{"-p", "--mode", "json"}
	// --session opens a session that exists and fails when there is none,
	// which is how the runner learns a session can no longer be continued.
	// --session-id would quietly start an empty one under the same id.
	if t.Resume != "" {
		args = append(args, "--session", t.Resume)
	} else if t.NewID != "" {
		args = append(args, "--session-id", t.NewID)
	}
	if t.Spec.Model != "" {
		args = append(args, "--model", t.Spec.Model)
	}
	if t.Spec.Effort != "" {
		args = append(args, "--thinking", t.Spec.Effort)
	}
	return args
}

func (piAdapter) Parse(line []byte) []Event {
	var m struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Message struct {
			Role         string          `json:"role"`
			Provider     string          `json:"provider"`
			Model        string          `json:"model"`
			StopReason   string          `json:"stopReason"`
			ErrorMessage string          `json:"errorMessage"`
			Content      json.RawMessage `json:"content"`
			Usage        struct {
				Input      int64 `json:"input"`
				Output     int64 `json:"output"`
				CacheRead  int64 `json:"cacheRead"`
				CacheWrite int64 `json:"cacheWrite"`
				Cost       struct {
					Total float64 `json:"total"`
				} `json:"cost"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &m) != nil {
		return nil
	}
	switch m.Type {
	case "session":
		if m.ID != "" {
			return []Event{{Kind: EventSession, SessionID: m.ID}}
		}
	case "message_end":
		if m.Message.Role != "assistant" {
			return nil
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Name string `json:"name"`
		}
		_ = json.Unmarshal(m.Message.Content, &parts)
		var events []Event
		var said []string
		for _, part := range parts {
			switch part.Type {
			case "text":
				if strings.TrimSpace(part.Text) != "" {
					said = append(said, part.Text)
				}
			case "toolCall":
				events = append(events, Event{Kind: EventTool, Tool: part.Name})
			}
		}
		model := m.Message.Model
		if m.Message.Provider != "" && model != "" {
			model = m.Message.Provider + "/" + model
		}
		usage := Usage{
			InputTokens:  m.Message.Usage.Input + m.Message.Usage.CacheRead + m.Message.Usage.CacheWrite,
			OutputTokens: m.Message.Usage.Output, CostUSD: m.Message.Usage.Cost.Total,
		}
		if len(said) > 0 {
			events = append([]Event{{Kind: EventText, Text: strings.Join(said, "\n")}}, events...)
		}
		switch m.Message.StopReason {
		case "error", "aborted":
			events = append(events, Event{Kind: EventError, Text: cmp.Or(m.Message.ErrorMessage, "the model request "+m.Message.StopReason), Model: model, Usage: usage})
		case "stop", "length":
			// The message that ends the turn: no tool call follows it.
			events = append(events, Event{Kind: EventResult, Model: model, Usage: usage})
		default:
			// A message that called a tool: the turn goes on, and what the
			// message cost is still counted.
			events = append(events, Event{Kind: EventUsage, Model: model, Usage: usage})
		}
		return events
	}
	return nil
}

// CheckPermissions reports the first of specs whose coding agent cannot be
// started under mode: one with no guardrails of its own, under guarded.
func CheckPermissions(mode Permissions, specs ...Spec) error {
	if mode != PermissionsGuarded {
		return nil
	}
	for _, spec := range specs {
		if adapter := adapterByName(spec.Adapter); adapter != nil && !adapter.Guarded() {
			return fmt.Errorf("%s has no guardrails of its own to run under, so it cannot be started with guarded permissions: use another coding agent, or full permissions", adapter.Name())
		}
	}
	return nil
}

// CheckInstalled reports the first of specs whose coding agent's CLI is not
// on PATH, so a connector that could never run a turn says so as it starts
// rather than at its first request. A nil look means exec.LookPath.
func CheckInstalled(look func(string) (string, error), specs ...Spec) error {
	if look == nil {
		look = exec.LookPath
	}
	for _, spec := range specs {
		adapter := adapterByName(spec.Adapter)
		if adapter == nil {
			return fmt.Errorf("unknown adapter %q: expected one of %s", spec.Adapter, strings.Join(AdapterNames(), ", "))
		}
		if _, err := look(adapter.Binary()); err != nil {
			return fmt.Errorf("the %s adapter needs the `%s` command, which is not on PATH", adapter.Name(), adapter.Binary())
		}
	}
	return nil
}
