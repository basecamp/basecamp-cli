// Package claude is the spawn driver for Claude Code: `claude -p` with
// streaming JSON in and out, adapted onto the driver package's ACP-shaped
// session.
//
// One process is one session. Prompts are user messages written to its stdin,
// so a follow-up is a further prompt in the same session; a turn ends with the
// result message. The permission policy is frozen into flags before the
// process starts and verified on the first turn: the init message must report
// the permission mode asked for, or the session is ended as unsafe. The host's
// own Claude Code settings and MCP servers are not loaded, and the built-in
// tools are limited to the ones the policy allows, so a tool the policy
// refuses does not exist in the session at all.
package claude

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// Name is the driver's name.
const Name = "claude"

// Env is what Claude Code may take from the connector's environment besides
// driver.BaseEnv: where its configuration lives and how it authenticates.
var Env = []string{"CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"}

// Options configures the driver.
type Options struct {
	// Binary is the claude executable; "claude" on PATH when empty.
	Binary string
	// Model is passed as --model when set.
	Model string
	// Lookup reads the connector's environment for Env; os.LookupEnv when
	// nil.
	Lookup func(string) (string, bool)
	// CloseGrace is how long a session's process has to exit after its stdin
	// closes, before its group is terminated.
	CloseGrace time.Duration
}

// Driver starts Claude Code sessions.
type Driver struct {
	opts Options
}

var _ driver.Driver = (*Driver)(nil)

// New builds the driver.
func New(opts Options) *Driver {
	if opts.Binary == "" {
		opts.Binary = "claude"
	}
	if opts.Lookup == nil {
		opts.Lookup = os.LookupEnv
	}
	if opts.CloseGrace <= 0 {
		opts.CloseGrace = 5 * time.Second
	}
	return &Driver{opts: opts}
}

// Name implements driver.Driver.
func (d *Driver) Name() string { return Name }

// Capabilities implements driver.Driver.
func (d *Driver) Capabilities() driver.Capabilities {
	return driver.Capabilities{LoadSession: true, FollowUpPrompts: true}
}

// NewSession implements driver.Driver.
func (d *Driver) NewSession(ctx context.Context, cfg driver.SessionConfig) (driver.Session, error) {
	id, err := newUUID()
	if err != nil {
		return nil, d.redactor(cfg).Err(fmt.Errorf("%w: %w", driver.ErrNotStarted, err))
	}
	s, err := d.start(ctx, cfg, id, false)
	return s, d.redactor(cfg).Err(err)
}

// LoadSession implements driver.Driver.
func (d *Driver) LoadSession(ctx context.Context, cfg driver.SessionConfig, sessionID string) (driver.Session, error) {
	if !validUUID(sessionID) {
		return nil, d.redactor(cfg).Err(fmt.Errorf("%w: %w: session id %q is not a Claude Code session id", driver.ErrNotStarted, driver.ErrUnusable, sessionID))
	}
	s, err := d.start(ctx, cfg, sessionID, true)
	return s, d.redactor(cfg).Err(err)
}

// env is the worker's whole environment: the dispatcher's, plus the variables
// this driver names for its agent.
func (d *Driver) env(cfg driver.SessionConfig) []string {
	return mergeEnv(cfg.Env, driver.BuildEnv(Env, d.opts.Lookup, nil))
}

// redactor is what every error and text of a session passes through: the
// dispatcher's Redaction, plus the environment this driver builds, its MCP
// servers' environments and its private directory.
func (d *Driver) redactor(cfg driver.SessionConfig) *driver.Redactor {
	more := driver.Redaction{Env: d.env(cfg), Dirs: []string{cfg.PrivateDir}}
	for _, server := range cfg.MCPServers {
		more.Env = append(more.Env, driver.EnvOf(server.Env)...)
	}
	return driver.NewRedactor(cfg.Redaction.With(more))
}

// modeIDs maps the connector's permission modes to Claude Code's.
var modeIDs = map[driver.PermissionMode]string{
	driver.ModeEditsInWorkDir: "acceptEdits",
}

// kindTools are Claude Code's built-in tools for each kind the policy can
// allow. Edits are acceptEdits's, confined to the working directory.
var kindTools = map[driver.ToolKind][]string{
	driver.ToolRead:   {"Read"},
	driver.ToolSearch: {"Glob", "Grep"},
	driver.ToolThink:  {"TodoWrite"},
	driver.ToolEdit:   {"Edit", "Write", "NotebookEdit"},
}

// Args is the command line for a session, without the binary. Exposed so the
// flags that hold the policy are tested as written.
func Args(cfg driver.SessionConfig, sessionID string, resume bool, mcpConfigPath, model string) ([]string, error) {
	rules := cfg.Policy.Rules()
	mode, ok := modeIDs[rules.Mode]
	if !ok {
		return nil, fmt.Errorf("claude: no Claude Code mode for policy mode %q", rules.Mode)
	}
	if filepath.Clean(rules.WorkDir) != filepath.Clean(cfg.Cwd) {
		return nil, fmt.Errorf("claude: the policy's working directory %q is not the session's %q", rules.WorkDir, cfg.Cwd)
	}
	tools := slices.Clone(kindTools[driver.ToolEdit])
	var allowed []string
	for _, kind := range rules.AllowKinds {
		names, ok := kindTools[kind]
		if !ok {
			return nil, fmt.Errorf("claude: no Claude Code tools for kind %q", kind)
		}
		// The tools exist in the session but get no allow rule: an allow
		// rule for Read is a read anywhere on disk, where the policy allows
		// reads in the working directory, which the mode already grants.
		tools = append(tools, names...)
	}
	for _, server := range rules.AllowMCPServers {
		allowed = append(allowed, "mcp__"+server)
	}

	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		// The host's settings (a defaultMode of bypassPermissions, allow
		// rules, hooks) are not this session's.
		"--setting-sources", "",
		"--permission-mode", mode,
		// Nobody answers a prompt: what the rules do not allow is refused.
		"--permission-prompts", "none",
		"--tools", strings.Join(tools, ","),
		"--allowed-tools", strings.Join(allowed, ","),
		"--strict-mcp-config",
		"--mcp-config", mcpConfigPath,
	}
	if resume {
		args = append(args, "--resume", sessionID)
	} else {
		args = append(args, "--session-id", sessionID)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args, nil
}

func (d *Driver) start(ctx context.Context, cfg driver.SessionConfig, sessionID string, resume bool) (driver.Session, error) {
	if cfg.Policy == nil || cfg.PrivateDir == "" || cfg.Cwd == "" {
		return nil, fmt.Errorf("%w: %w: a session needs a policy, a working directory and a private directory", driver.ErrNotStarted, driver.ErrUnusable)
	}
	mcpPath, err := writeMCPConfig(cfg.PrivateDir, cfg.MCPServers)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", driver.ErrNotStarted, err)
	}
	args, err := Args(cfg, sessionID, resume, mcpPath, d.opts.Model)
	if err != nil {
		_ = os.Remove(mcpPath)
		// A mode or a policy the flags cannot express is not a start to try
		// again: it is configuration.
		return nil, fmt.Errorf("%w: %w: %w", driver.ErrNotStarted, driver.ErrUnusable, err)
	}
	env := d.env(cfg)
	worker, err := driver.StartWorker(ctx, cfg.Launcher, cfg.Scope, driver.Command{Path: d.opts.Binary, Args: args, Env: env, Dir: cfg.Cwd})
	if err != nil {
		_ = os.Remove(mcpPath)
		return nil, err
	}
	s := &session{
		id:        sessionID,
		worker:    worker,
		mode:      args[slices.Index(args, "--permission-mode")+1],
		mcpPath:   mcpPath,
		mcpNames:  serverNames(cfg.MCPServers),
		grace:     d.opts.CloseGrace,
		updates:   make(chan driver.Update, 256),
		slot:      make(chan struct{}, 1),
		readerEnd: make(chan struct{}),
		red:       d.redactor(cfg),
		recorder:  cfg.Refusals,
		recorded:  map[string]bool{},
	}
	go s.read() //nolint:contextcheck // the reader outlives the start's context: it runs as long as the worker does
	return s, nil
}

// mergeEnv adds the driver's own variables to the dispatcher's allowlisted
// environment. A variable the dispatcher set wins.
func mergeEnv(base, extra []string) []string {
	have := map[string]bool{}
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		have[k] = true
	}
	out := slices.Clone(base)
	if out == nil {
		out = []string{}
	}
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		if !have[k] {
			out = append(out, kv)
		}
	}
	slices.Sort(out)
	return out
}

func serverNames(servers []driver.MCPServer) []string {
	names := make([]string, 0, len(servers))
	for _, s := range servers {
		names = append(names, s.Name)
	}
	return names
}

// writeMCPConfig writes the session's MCP servers owner-only. The file holds
// the servers' environments, a task token among them, so it is created
// exclusively in the private directory and removed as soon as the agent has
// started its servers, and again on Close.
func writeMCPConfig(dir string, servers []driver.MCPServer) (string, error) {
	type entry struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	config := struct {
		MCPServers map[string]entry `json:"mcpServers"`
	}{MCPServers: map[string]entry{}}
	for _, s := range servers {
		if s.Name == "" || s.Command == "" {
			return "", fmt.Errorf("%w: an MCP server needs a name and a command", driver.ErrUnusable)
		}
		env := s.Env
		if env == nil {
			env = map[string]string{}
		}
		config.MCPServers[s.Name] = entry{Type: "stdio", Command: s.Command, Args: s.Args, Env: env}
	}
	data, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "mcp.json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("claude: write MCP config: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("claude: write MCP config: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("claude: write MCP config: %w", err)
	}
	return path, nil
}

// session is one Claude Code process.
type session struct {
	id       string
	worker   *driver.Worker
	mode     string
	mcpPath  string
	mcpNames []string
	grace    time.Duration

	updates   chan driver.Update
	readerEnd chan struct{}
	// red is what every error, update text and stderr tail of this session
	// passes through before it leaves the driver.
	red *driver.Redactor
	// recorder records each refusal once, as it is read (driver's
	// "Refusals"); recorded is the tool call ids already recorded. Both are
	// touched only by the reader goroutine.
	recorder driver.RefusalRecorder
	recorded map[string]bool

	// beforePromptWrite runs between a turn's registration and its write; a
	// test seam.
	beforePromptWrite func()
	// beforeCancelWrite runs inside Cancel, under the write lock, before the
	// interrupt is written; a test seam.
	beforeCancelWrite func()
	// cancelPending is a cancel that arrived with no turn to interrupt. The
	// next turn takes it.
	cancelPending bool
	// ended is why the session ended, when it ended with no turn in flight to
	// carry the reason: the next Prompt answers with it rather than waiting
	// for a turn nothing will finish.
	ended error

	mu       sync.Mutex
	turn     *turn
	verified bool
	closed   bool
	// slot is the right to write to the worker, held across registering a
	// turn and sending its prompt so an interrupt cannot reach a turn other
	// than the one it was asked for. A channel, not a mutex, because a
	// worker that stops reading its input makes a write block, and a caller
	// waiting for the slot must be able to give up: Cancel takes it with a
	// deadline, and Close does not take it at all.
	slot chan struct{}
}

// turn is a prompt in flight.
type turn struct {
	done     chan struct{}
	result   driver.PromptResult
	err      error
	canceled bool
	refusals []driver.Refusal
}

var _ driver.Session = (*session)(nil)

func (s *session) ID() string                    { return s.id }
func (s *session) Process() driver.Process       { return s.worker.Process() }
func (s *session) Updates() <-chan driver.Update { return s.updates }
func (s *session) Done() <-chan struct{}         { return s.worker.Done() }
func (s *session) Exit() driver.Exit             { return s.worker.Exit() }

// StderrTail is what may be passed on of the agent's stderr.
func (s *session) StderrTail() string { return s.worker.StderrTail(s.red) }

// Prompt implements driver.Session.
func (s *session) Prompt(ctx context.Context, prompt string) (driver.PromptResult, error) {
	result, err := s.prompt(ctx, prompt)
	return result, s.red.Err(err)
}

func (s *session) prompt(ctx context.Context, prompt string) (driver.PromptResult, error) {
	// The turn is registered and its message written under the write lock,
	// so a Cancel that sees the turn writes its interrupt after the prompt,
	// never before it, where it would interrupt nothing.
	if err := s.takeSlot(ctx, 0); err != nil {
		// A session that ended for a reason answers with that reason.
		s.mu.Lock()
		ended := s.ended
		s.mu.Unlock()
		if ended != nil {
			return driver.PromptResult{}, ended
		}
		return driver.PromptResult{}, err
	}
	s.mu.Lock()
	if s.closed || s.ended != nil {
		ended := s.ended
		s.mu.Unlock()
		s.releaseSlot()
		if ended != nil {
			return driver.PromptResult{}, ended
		}
		return driver.PromptResult{}, driver.ErrSessionEnded
	}
	if s.turn != nil {
		s.mu.Unlock()
		s.releaseSlot()
		return driver.PromptResult{}, errors.New("claude: a turn is already in flight")
	}
	t := &turn{done: make(chan struct{})}
	pending := s.cancelPending
	s.cancelPending = false
	t.canceled = pending
	s.turn = t
	s.mu.Unlock()
	if s.beforePromptWrite != nil {
		s.beforePromptWrite()
	}
	msg := map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": prompt}}
	err := s.writeHeld(msg)
	if pending && err == nil {
		// The interrupt follows the prompt it cancels, still holding the
		// slot, so nothing can come between them.
		err = s.writeHeld(interruptRequest())
	}
	s.releaseSlot()
	if err != nil {
		s.finish(t, driver.PromptResult{}, fmt.Errorf("%w: %w", driver.ErrSessionEnded, err))
	}
	select {
	case <-t.done:
		return t.result, t.err
	case <-ctx.Done():
		return driver.PromptResult{}, ctx.Err()
	}
}

// Cancel implements driver.Session: Claude Code's interrupt control request.
// Cancel implements driver.Session: Claude Code's interrupt control request.
//
// It takes the write lock before it looks at the turn, the same order Prompt
// takes them, so the turn it interrupts is the turn it observed: no prompt
// can register and be written in between and take the interrupt meant for
// another turn.
func (s *session) Cancel(ctx context.Context) error {
	return s.red.Err(s.cancel(ctx))
}

func (s *session) cancel(ctx context.Context) error {
	if err := s.takeSlot(ctx, s.grace); err != nil {
		// The worker is not reading its input; the connector's next step is
		// to close the session, which ends it whatever it is doing.
		s.mu.Lock()
		if s.turn != nil {
			s.turn.canceled = true
		}
		s.mu.Unlock()
		return fmt.Errorf("claude: the agent is not reading its input: %w", err)
	}
	defer s.releaseSlot()
	s.mu.Lock()
	t := s.turn
	if t != nil {
		t.canceled = true
	} else {
		// Nothing to interrupt yet: the next turn is the one the connector
		// meant to cancel, and starts canceled.
		s.cancelPending = true
	}
	s.mu.Unlock()
	if t == nil {
		return nil
	}
	if s.beforeCancelWrite != nil {
		s.beforeCancelWrite()
	}
	return s.writeHeld(interruptRequest())
}

// interruptRequest is Claude Code's interrupt control request. A request id
// it will not answer twice is enough; the reply is not awaited.
func interruptRequest() map[string]any {
	id, err := newUUID()
	if err != nil {
		id = "interrupt"
	}
	return map[string]any{"type": "control_request", "request_id": id, "request": map[string]any{"subtype": "interrupt"}}
}

// Close implements driver.Session.
func (s *session) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	// Closed without the slot on purpose: a write blocked on a worker that
	// stopped reading ends with a broken pipe rather than holding Close.
	_ = s.worker.Stdin().Close()
	select {
	case <-s.worker.Done():
	case <-time.After(s.grace):
	}
	s.worker.Terminate(s.grace)
	select {
	case <-s.readerEnd:
	case <-time.After(s.grace):
		// The worker is gone and a descendant outside its group still holds
		// the output: stop reading it.
		s.worker.CloseStdout()
		<-s.readerEnd
	}
	s.removeMCPConfig()
	return nil
}

func (s *session) removeMCPConfig() {
	if err := os.Remove(s.mcpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
}

// takeSlot waits for the right to write. A zero wait waits for ctx alone.
func (s *session) takeSlot(ctx context.Context, wait time.Duration) error {
	var deadline <-chan time.Time
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		deadline = timer.C
	}
	select {
	case s.slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-deadline:
		return context.DeadlineExceeded
	case <-s.worker.Done():
		return driver.ErrSessionEnded
	}
}

func (s *session) releaseSlot() { <-s.slot }

// writeHeld writes one message; the caller holds the slot.
func (s *session) writeHeld(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.worker.Stdin().Write(append(data, '\n'))
	return err
}

func (s *session) finish(t *turn, result driver.PromptResult, err error) {
	s.mu.Lock()
	if s.turn != t {
		s.mu.Unlock()
		return
	}
	s.turn = nil
	s.mu.Unlock()
	t.result, t.err = result, err
	close(t.done)
}

// end records why the session is over, for a prompt that comes after it.
func (s *session) end(err error) {
	s.mu.Lock()
	if s.ended == nil {
		s.ended = err
	}
	s.mu.Unlock()
}

func (s *session) emit(u driver.Update) {
	u.At = time.Now()
	u.Tool = s.red.Sanitize(u.Tool)
	u.ToolCallID = s.red.Sanitize(u.ToolCallID)
	select {
	case s.updates <- u:
	default:
	}
}

// read maps the process's stream onto updates and turn results until the
// process closes its stdout.
func (s *session) read() {
	defer func() {
		// Nothing more will be read from the worker's output.
		s.worker.CloseStdout()
		close(s.updates)
		s.mu.Lock()
		t := s.turn
		s.mu.Unlock()
		if t != nil {
			s.finish(t, driver.PromptResult{}, driver.ErrSessionEnded)
		}
		// Whatever comes next: there is no reader to finish a turn, so a
		// later prompt is answered rather than left waiting.
		s.end(driver.ErrSessionEnded)
		close(s.readerEnd)
	}()
	scanner := bufio.NewScanner(s.worker.Stdout())
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	for scanner.Scan() {
		s.handle(scanner.Bytes())
	}
	// Drain what a scanner error left, so the process never blocks writing.
	_, _ = io.Copy(io.Discard, s.worker.Stdout())
}

// streamMessage is the part of a stream-json line the driver reads. Text and
// tool inputs are never decoded into anything kept.
type streamMessage struct {
	Type           string `json:"type"`
	Subtype        string `json:"subtype"`
	SessionID      string `json:"session_id"`
	PermissionMode string `json:"permissionMode"`
	MCPServers     []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"mcp_servers"`
	Message *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	ToolName          string `json:"tool_name"`
	ToolUseID         string `json:"tool_use_id"`
	StopReason        string `json:"stop_reason"`
	IsError           bool   `json:"is_error"`
	PermissionDenials []struct {
		ToolName  string `json:"tool_name"`
		ToolUseID string `json:"tool_use_id"`
	} `json:"permission_denials"`
	Usage *struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

type contentBlock struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Text      string `json:"text"`
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
}

func (s *session) handle(line []byte) {
	var m streamMessage
	if err := json.Unmarshal(line, &m); err != nil {
		return
	}
	switch {
	case m.Type == "system" && m.Subtype == "init":
		s.handleInit(m)
	case m.Type == "system" && m.Subtype == "permission_denied":
		s.refused(m.ToolUseID, m.ToolName)
	case m.Type == "assistant" && m.Message != nil:
		var blocks []contentBlock
		if json.Unmarshal(m.Message.Content, &blocks) != nil {
			return
		}
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				s.emit(driver.Update{Kind: driver.UpdateToolCall, ToolCallID: b.ID, Tool: b.Name, ToolKind: toolKind(b.Name), Status: driver.ToolInProgress})
			case "text":
				s.emit(driver.Update{Kind: driver.UpdateAgentMessageChunk, Chars: len(b.Text)})
			}
		}
	case m.Type == "user" && m.Message != nil:
		var blocks []contentBlock
		if json.Unmarshal(m.Message.Content, &blocks) != nil {
			return
		}
		for _, b := range blocks {
			if b.Type != "tool_result" {
				continue
			}
			status := driver.ToolCompleted
			if b.IsError {
				status = driver.ToolFailed
			}
			s.emit(driver.Update{Kind: driver.UpdateToolCallUpdate, ToolCallID: b.ToolUseID, Status: status})
		}
	case m.Type == "result":
		s.handleResult(m)
	}
}

// handleInit verifies the session is the one asked for (driver invariant 2):
// the mode, and the MCP servers connected. A session that is not is ended.
func (s *session) handleInit(m streamMessage) {
	var problem error
	switch {
	case m.PermissionMode != s.mode:
		problem = fmt.Errorf("%w: asked for %q, the agent reports %q", driver.ErrUnsafeMode, s.mode, m.PermissionMode)
	case m.SessionID != s.id:
		problem = fmt.Errorf("claude: asked for session %s, the agent reports another", s.id)
	default:
		for _, name := range s.mcpNames {
			connected := false
			for _, server := range m.MCPServers {
				if server.Name == name && server.Status == "connected" {
					connected = true
				}
			}
			if !connected {
				problem = fmt.Errorf("claude: MCP server %q did not connect", name)
			}
		}
	}
	// The agent has started its servers, or failed to: the config file, which
	// holds their environments, is not needed again.
	s.removeMCPConfig()
	s.mu.Lock()
	t := s.turn
	if problem == nil {
		s.verified = true
	}
	s.mu.Unlock()
	if problem != nil {
		if t != nil {
			s.finish(t, driver.PromptResult{}, problem)
		} else {
			// No turn to carry it: the next Prompt answers with the reason
			// this session was ended, so an unsafe mode is never read as a
			// worker merely gone.
			s.end(problem)
		}
		s.worker.Terminate(0)
	}
}

func (s *session) refused(toolUseID, tool string) {
	refusal, first := s.record(toolUseID, tool)
	if !first {
		// A stream that announces one refusal twice refused once.
		return
	}
	s.mu.Lock()
	if s.turn != nil {
		s.turn.refusals = append(s.turn.refusals, refusal)
	}
	s.mu.Unlock()
	s.emit(driver.Update{Kind: driver.UpdatePermission, ToolCallID: toolUseID, Tool: tool, ToolKind: toolKind(tool), Allowed: false})
}

// record is the moment a refusal is read from the stream: it is recorded
// through the session's recorder before anything else is done with it, and
// only the first time its tool call id is seen (driver's "Refusals").
func (s *session) record(toolUseID, tool string) (driver.Refusal, bool) {
	refusal := driver.Refusal{ToolCallID: s.red.Sanitize(toolUseID), Tool: s.red.Sanitize(tool)}
	if s.recorded[toolUseID] {
		return refusal, false
	}
	s.recorded[toolUseID] = true
	if s.recorder != nil {
		// The recorder owns what happens when the ledger refuses the write;
		// the refusal happened either way.
		_ = s.recorder.RecordRefusal(context.Background(), refusal)
	}
	return refusal, true
}

func (s *session) handleResult(m streamMessage) {
	s.mu.Lock()
	t := s.turn
	verified := s.verified
	s.mu.Unlock()
	if t == nil {
		return
	}
	if !verified {
		// A result before the init message proved the mode is not a turn this
		// driver can vouch for.
		s.finish(t, driver.PromptResult{}, fmt.Errorf("%w: no init message before the result", driver.ErrUnsafeMode))
		s.worker.Terminate(0)
		return
	}
	s.mu.Lock()
	refusals := slices.Clone(t.refusals)
	canceled := t.canceled
	s.mu.Unlock()
	for _, d := range m.PermissionDenials {
		if slices.ContainsFunc(refusals, func(r driver.Refusal) bool { return r.ToolCallID == s.red.Sanitize(d.ToolUseID) }) {
			continue
		}
		// A refusal the stream did not announce is still the driver's own
		// record, and is reported both ways (invariant 3).
		refusal, first := s.record(d.ToolUseID, d.ToolName)
		if !first {
			continue
		}
		refusals = append(refusals, refusal)
		s.emit(driver.Update{Kind: driver.UpdatePermission, ToolCallID: d.ToolUseID, Tool: d.ToolName, ToolKind: toolKind(d.ToolName), Allowed: false})
	}
	result := driver.PromptResult{Refusals: refusals}
	if m.Usage != nil {
		result.Usage = driver.Usage{InputTokens: m.Usage.InputTokens, OutputTokens: m.Usage.OutputTokens}
		s.emit(driver.Update{Kind: driver.UpdateUsage, Usage: &result.Usage})
	}
	switch {
	case canceled:
		// Only a cancel the connector asked for reads as canceled (driver
		// invariant 3).
		result.Stop = driver.TurnCanceled
	case m.Subtype == "error_max_turns":
		result.Stop = driver.TurnMaxTurnRequests
	case m.StopReason == "max_tokens":
		result.Stop = driver.TurnMaxTokens
	case m.StopReason == "refusal":
		result.Stop = driver.TurnRefusal
	case m.Subtype == "success" && !m.IsError:
		result.Stop = driver.TurnEndTurn
	default:
		s.finish(t, result, fmt.Errorf("claude: the turn ended in error (%s)", sanitize(m.Subtype)))
		return
	}
	s.finish(t, result, nil)
}

// toolKind maps a Claude Code tool name to ACP's kind.
func toolKind(name string) driver.ToolKind {
	for kind, tools := range kindTools {
		if slices.Contains(tools, name) {
			return kind
		}
	}
	switch name {
	case "Bash":
		return driver.ToolExecute
	case "WebFetch", "WebSearch":
		return driver.ToolFetch
	}
	return driver.ToolOther
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || r == '_' {
			out = append(out, r)
		}
		if len(out) >= 40 {
			break
		}
	}
	return string(out)
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				return false
			}
		}
	}
	return true
}
