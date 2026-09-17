package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// session is one adapter process and the one ACP session it serves.
type session struct {
	worker  *driver.Worker
	conn    *conn
	policy  driver.PermissionPolicy
	askMode string
	grace   time.Duration

	updates   chan driver.Update
	readerEnd chan struct{}

	// promptSem orders a prompt's request and a cancel's notification on the
	// wire, so a cancel never reaches the agent before the prompt it ends. A
	// channel, not a mutex, so a cancel can give up waiting on a prompt whose
	// write is stuck.
	promptSem chan struct{}

	mu            sync.Mutex
	id            string
	turn          *turn
	mode          string
	modeSeen      chan struct{}
	verified      bool
	unsafe        error
	replaying     bool
	updatesClosed bool
	closed        bool
	context       driver.Usage
	// tools is what the agent said about each tool call it announced, so a
	// permission request that names only the call's id is decided on the call.
	tools map[string]toolInfo

	closeOnce sync.Once
	// endUnsafe ends the worker of a session found outside its asking mode;
	// the worker's Terminate, replaced only by this package's tests.
	endUnsafe func()
}

// turn is a prompt in flight.
type turn struct {
	done chan struct{}
	// call is the turn's session/prompt, registered before it is sent.
	call     *pendingCall
	canceled bool
	refusals []driver.Refusal
	result   driver.PromptResult
	err      error
}

var _ driver.Session = (*session)(nil)

func newSession(worker *driver.Worker, policy driver.PermissionPolicy, askMode string, grace time.Duration, trace func(string, []byte)) *session {
	s := &session{
		worker:    worker,
		policy:    policy,
		askMode:   askMode,
		grace:     grace,
		updates:   make(chan driver.Update, 256),
		readerEnd: make(chan struct{}),
		modeSeen:  make(chan struct{}),
		promptSem: make(chan struct{}, 1),
		tools:     map[string]toolInfo{},
	}
	s.endUnsafe = func() { worker.Terminate(0) }
	s.conn = newConn(worker.Stdin())
	s.conn.trace = trace
	s.conn.onNotification = s.onNotification
	s.conn.onRequest = s.onRequest
	go func() {
		if err := s.conn.read(worker.Stdout()); err != nil {
			// A line past maxLine or a broken pipe: the session cannot go
			// on, so its worker does not either.
			s.worker.Terminate(0)
		}
		// Drain what is left so the agent never blocks on a full pipe.
		_, _ = io.Copy(io.Discard, worker.Stdout())
		s.mu.Lock()
		s.updatesClosed = true
		close(s.updates)
		s.mu.Unlock()
		close(s.readerEnd)
	}()
	return s
}

func (s *session) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

func (s *session) Process() driver.Process       { return s.worker.Process() }
func (s *session) Updates() <-chan driver.Update { return s.updates }
func (s *session) Done() <-chan struct{}         { return s.worker.Done() }
func (s *session) Exit() driver.Exit             { return s.worker.Exit() }

// ---------------------------------------------------------------- handshake

type agentCaps struct {
	LoadSession bool
	Resume      bool
}

func (s *session) initialize(ctx context.Context, a Adapter) (agentCaps, error) {
	var r struct {
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			LoadSession         bool `json:"loadSession"`
			SessionCapabilities struct {
				Resume json.RawMessage `json:"resume"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
		AgentInfo *struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"agentInfo"`
	}
	err := s.conn.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		// No fs, no terminal: the agent works through its own tools, and asks.
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]any{"name": "basecamp-connect", "version": version.Version},
	}, &r)
	if err != nil {
		return agentCaps{}, err
	}
	if r.ProtocolVersion != ProtocolVersion {
		return agentCaps{}, fmt.Errorf("acp: the agent answered protocol version %d, not %d", r.ProtocolVersion, ProtocolVersion)
	}
	if r.AgentInfo == nil || r.AgentInfo.Name != a.Package || r.AgentInfo.Version != a.Version {
		name, ver := "", ""
		if r.AgentInfo != nil {
			name, ver = r.AgentInfo.Name, r.AgentInfo.Version
		}
		return agentCaps{}, fmt.Errorf("%w: it reports %s@%s, pinned is %s@%s", ErrWrongAdapter, agentText(name), agentText(ver), a.Package, a.Version)
	}
	resume := len(r.AgentCapabilities.SessionCapabilities.Resume) > 0 && string(r.AgentCapabilities.SessionCapabilities.Resume) != "null"
	return agentCaps{LoadSession: r.AgentCapabilities.LoadSession, Resume: resume}, nil
}

// sessionState is what session/new, session/load and session/resume answer.
type sessionState struct {
	SessionID string `json:"sessionId"`
	Modes     *struct {
		CurrentModeID  string `json:"currentModeId"`
		AvailableModes []struct {
			ID string `json:"id"`
		} `json:"availableModes"`
	} `json:"modes"`
	ConfigOptions []configOption `json:"configOptions"`
}

// configOption is a session config option, reduced to what finds the mode.
type configOption struct {
	ID           string          `json:"id"`
	Category     string          `json:"category"`
	Type         string          `json:"type"`
	CurrentValue json.RawMessage `json:"currentValue"`
	Options      json.RawMessage `json:"options"`
}

// wireServer is ACP's stdio McpServer.
type wireServer struct {
	Name    string    `json:"name"`
	Command string    `json:"command"`
	Args    []string  `json:"args"`
	Env     []wireEnv `json:"env"`
}

type wireEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// wireServers declares every server's whole environment (invariant 1): some
// adapters pass their own environment down to MCP servers and some pass
// almost nothing, so nothing a server needs is left to inheritance.
func wireServers(servers []driver.MCPServer) ([]wireServer, error) {
	out := make([]wireServer, 0, len(servers))
	for _, srv := range servers {
		if srv.Name == "" || !filepath.IsAbs(srv.Command) {
			return nil, errors.New("acp: an MCP server needs a name and an absolute command")
		}
		env := make([]wireEnv, 0, len(srv.Env))
		for k, v := range srv.Env {
			if k == "" || strings.ContainsAny(k, "=\x00") {
				return nil, fmt.Errorf("acp: MCP server %q has an invalid environment name", srv.Name)
			}
			env = append(env, wireEnv{Name: k, Value: v})
		}
		slices.SortFunc(env, func(a, b wireEnv) int { return strings.Compare(a.Name, b.Name) })
		args := srv.Args
		if args == nil {
			args = []string{}
		}
		out = append(out, wireServer{Name: srv.Name, Command: srv.Command, Args: args, Env: env})
	}
	return out, nil
}

func (s *session) newSession(ctx context.Context, cwd string, servers []wireServer, meta map[string]any) (sessionState, error) {
	params := map[string]any{"cwd": cwd, "mcpServers": servers}
	if meta != nil {
		params["_meta"] = meta
	}
	var st sessionState
	if err := s.conn.call(ctx, "session/new", params, &st); err != nil {
		return st, err
	}
	if !validSessionID(st.SessionID) {
		return st, errors.New("acp: session/new answered no usable session id")
	}
	s.mu.Lock()
	s.id = st.SessionID
	s.mu.Unlock()
	return st, nil
}

// loadSession reopens a session by id, by the method the agent advertised
// (invariant 5). The history the agent replays is not progress.
func (s *session) loadSession(ctx context.Context, caps agentCaps, id, cwd string, servers []wireServer, meta map[string]any) (sessionState, error) {
	var method string
	switch {
	case caps.LoadSession:
		method = "session/load"
	case caps.Resume:
		method = "session/resume"
	default:
		return sessionState{}, ErrLoadUnsupported
	}
	s.mu.Lock()
	s.id = id
	s.replaying = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.replaying = false
		s.mu.Unlock()
	}()
	params := map[string]any{"sessionId": id, "cwd": cwd, "mcpServers": servers}
	if meta != nil {
		params["_meta"] = meta
	}
	var st sessionState
	if err := s.conn.call(ctx, method, params, &st); err != nil {
		return st, err
	}
	st.SessionID = id
	return st, nil
}

// enterAskingMode puts the session in its adapter's asking mode and reads the
// mode back (invariant 2). session/set_mode answers nothing, so the read-back
// is session/set_config_option's full option list where the agent has a mode
// option, and otherwise a current_mode_update.
func (s *session) enterAskingMode(ctx context.Context, st sessionState) error {
	offered := false
	if st.Modes != nil {
		for _, m := range st.Modes.AvailableModes {
			offered = offered || m.ID == s.askMode
		}
	}
	modeOpt := modeOption(st.ConfigOptions)
	if modeOpt != nil && slices.Contains(optionValues(modeOpt.Options), s.askMode) {
		offered = true
	}
	if !offered {
		return fmt.Errorf("%w: the agent does not offer the asking mode %q", driver.ErrUnsafeMode, s.askMode)
	}
	if st.Modes != nil {
		s.reportMode(st.Modes.CurrentModeID)
	}
	if v, ok := stringValue(modeOpt); ok {
		s.reportMode(v)
	}

	if st.Modes != nil {
		if err := s.conn.call(ctx, "session/set_mode", map[string]any{"sessionId": st.SessionID, "modeId": s.askMode}, nil); err != nil {
			return fmt.Errorf("%w: session/set_mode: %w", driver.ErrUnsafeMode, err)
		}
	}
	if modeOpt != nil {
		var r struct {
			ConfigOptions []configOption `json:"configOptions"`
		}
		err := s.conn.call(ctx, "session/set_config_option", map[string]any{"sessionId": st.SessionID, "configId": modeOpt.ID, "value": s.askMode}, &r)
		if err != nil {
			return fmt.Errorf("%w: session/set_config_option: %w", driver.ErrUnsafeMode, err)
		}
		v, ok := stringValue(modeOption(r.ConfigOptions))
		if !ok {
			return fmt.Errorf("%w: session/set_config_option answered no mode", driver.ErrUnsafeMode)
		}
		s.reportMode(v)
	} else {
		wait, cancel := context.WithTimeout(ctx, modeConfirmWait)
		defer cancel()
		s.awaitMode(wait)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode != s.askMode {
		return fmt.Errorf("%w: asked for mode %q, the agent reports %q", driver.ErrUnsafeMode, s.askMode, agentText(s.mode))
	}
	s.verified = true
	return nil
}

// awaitMode waits for the agent to report the asking mode, or for ctx.
func (s *session) awaitMode(ctx context.Context) {
	for {
		s.mu.Lock()
		if s.mode == s.askMode {
			s.mu.Unlock()
			return
		}
		seen := s.modeSeen
		s.mu.Unlock()
		select {
		case <-seen:
		case <-s.readerEnd:
			return
		case <-ctx.Done():
			return
		}
	}
}

// reportMode records the mode the agent reports. Once the asking mode is
// confirmed, any other mode makes the session unsafe: its turn fails with
// ErrUnsafeMode and its process group is ended (invariant 2).
func (s *session) reportMode(id string) {
	s.mu.Lock()
	s.mode = id
	close(s.modeSeen)
	s.modeSeen = make(chan struct{})
	unsafe := s.verified && id != s.askMode && s.unsafe == nil
	if unsafe {
		s.unsafe = fmt.Errorf("%w: the agent left mode %q for %q", driver.ErrUnsafeMode, s.askMode, agentText(id))
	}
	t := s.turn
	end := s.endUnsafe
	s.mu.Unlock()
	if unsafe {
		// The turn is failed first and the worker ended after, so whoever
		// waits on both hears ErrUnsafeMode before the worker is gone.
		go func() {
			if t != nil {
				s.conn.abandon(t.call)
				<-t.done
			}
			end()
		}()
	}
}

func modeOption(options []configOption) *configOption {
	for i := range options {
		if options[i].Category == "mode" && options[i].Type == "select" {
			return &options[i]
		}
	}
	return nil
}

func stringValue(o *configOption) (string, bool) {
	if o == nil {
		return "", false
	}
	var v string
	if json.Unmarshal(o.CurrentValue, &v) != nil {
		return "", false
	}
	return v, true
}

// optionValues are a select option's values, flat or grouped.
func optionValues(raw json.RawMessage) []string {
	var items []struct {
		Value   *string         `json:"value"`
		Options json.RawMessage `json:"options"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []string
	for _, it := range items {
		if it.Value != nil {
			out = append(out, *it.Value)
		}
		if len(it.Options) > 0 {
			out = append(out, optionValues(it.Options)...)
		}
	}
	return out
}

// ---------------------------------------------------------------- turns

// Prompt implements driver.Session.
func (s *session) Prompt(ctx context.Context, prompt string) (driver.PromptResult, error) {
	s.promptSem <- struct{}{}
	s.mu.Lock()
	var refuse error
	switch {
	case s.closed:
		refuse = driver.ErrSessionEnded
	case s.unsafe != nil:
		refuse = s.unsafe
	case !s.verified:
		refuse = fmt.Errorf("%w: the mode was never confirmed", driver.ErrUnsafeMode)
	case s.turn != nil:
		refuse = errors.New("acp: a turn is already in flight")
	}
	if refuse != nil {
		s.mu.Unlock()
		<-s.promptSem
		return driver.PromptResult{}, refuse
	}
	t := &turn{done: make(chan struct{}), call: s.conn.register("session/prompt")}
	s.turn = t
	id := s.id
	s.mu.Unlock()

	answer := t.call
	err := s.conn.sendCall(answer, map[string]any{
		"sessionId": id,
		"prompt":    []any{map[string]any{"type": "text", "text": prompt}},
	})
	<-s.promptSem
	go s.finishTurn(t, answer, err)

	select {
	case <-t.done:
		return t.result, t.err
	case <-ctx.Done():
		return driver.PromptResult{}, ctx.Err()
	}
}

// finishTurn waits for the prompt's response and settles the turn, whether or
// not anyone is still waiting on Prompt.
func (s *session) finishTurn(t *turn, answer *pendingCall, sendErr error) {
	var resp struct {
		StopReason string `json:"stopReason"`
		Usage      *struct {
			InputTokens  int64 `json:"inputTokens"`
			OutputTokens int64 `json:"outputTokens"`
		} `json:"usage"`
	}
	err := sendErr
	if err == nil {
		err = answer.wait(&resp)
	}

	s.mu.Lock()
	if s.turn == t {
		s.turn = nil
	}
	refusals := slices.Clone(t.refusals)
	canceled := t.canceled
	unsafe := s.unsafe
	usage := s.context
	s.mu.Unlock()

	result := driver.PromptResult{Refusals: refusals, Usage: usage}
	if resp.Usage != nil {
		result.Usage.InputTokens = resp.Usage.InputTokens
		result.Usage.OutputTokens = resp.Usage.OutputTokens
	}
	switch {
	case unsafe != nil:
		err = unsafe
	case err != nil:
	default:
		result.Stop, err = stopOf(resp.StopReason, canceled, len(refusals))
		if err == nil && resp.Usage != nil {
			u := result.Usage
			s.emit(driver.Update{Kind: driver.UpdateUsage, Usage: &u})
		}
	}
	t.result, t.err = result, err
	close(t.done)
}

// stopOf maps ACP's stop reason to the driver's (invariant 4).
func stopOf(reason string, canceled bool, refusals int) (driver.TurnStop, error) {
	switch driver.TurnStop(reason) {
	case driver.TurnEndTurn, driver.TurnMaxTokens, driver.TurnMaxTurnRequests, driver.TurnRefusal:
		return driver.TurnStop(reason), nil
	case driver.TurnCanceled:
		switch {
		case canceled:
			return driver.TurnCanceled, nil
		case refusals > 0:
			// codex-acp ends a turn it was refused in as canceled.
			return driver.TurnRefusal, nil
		}
		return "", errors.New("acp: the agent ended the turn as canceled, and the connector asked for no cancel")
	}
	return "", fmt.Errorf("acp: the agent ended the turn with an unknown stop reason %q", agentText(reason))
}

// Cancel implements driver.Session: session/cancel for the turn in flight.
//
// A cancel waits at most for ctx or the close grace, whichever ends first,
// both for the prompt's own write and for its notification's, so an agent that
// has stopped reading its input cannot hold the caller.
func (s *session) Cancel(ctx context.Context) error {
	grace := time.NewTimer(s.grace)
	defer grace.Stop()
	stuck := errors.New("acp: the agent is not reading its input; the cancel could not be sent")
	select {
	case s.promptSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-grace.C:
		return stuck
	}
	s.mu.Lock()
	t := s.turn
	if t != nil {
		t.canceled = true
	}
	id := s.id
	s.mu.Unlock()
	// The prompt this cancel ends is on the wire; a later prompt cannot start
	// while its turn is in flight.
	<-s.promptSem
	if t == nil {
		return nil
	}
	sent := make(chan error, 1)
	go func() { sent <- s.conn.notify("session/cancel", map[string]any{"sessionId": id}) }()
	select {
	case err := <-sent:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-grace.C:
		return stuck
	}
}

// Close implements driver.Session: the adapter's input is closed, it is given
// grace to exit, and its process group is ended either way, which takes the
// agent and every MCP server it started with it.
func (s *session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.conn.closeWrite(s.worker.Stdin())
		select {
		case <-s.worker.Done():
		case <-time.After(s.grace):
		}
		s.worker.Terminate(s.grace)
		<-s.readerEnd
	})
	return nil
}

// abort ends a session that failed its handshake, without grace.
func (s *session) abort() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.worker.Terminate(0)
		<-s.readerEnd
	})
}

// stderrNote is the end of the adapter's stderr, redacted, for an error.
func (s *session) stderrNote() string {
	tail := strings.TrimSpace(s.worker.StderrTail())
	if tail == "" {
		return ""
	}
	if i := strings.LastIndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	return " (adapter stderr: " + agentText(tail) + ")"
}

// ---------------------------------------------------------------- from the agent

// sessionUpdate is the part of a session/update (or a permission request's
// tool call) the driver reads. Text, titles beyond an MCP call's, raw inputs
// beyond an MCP call's server and tool, and outputs are never decoded into
// anything kept.
type sessionUpdate struct {
	SessionUpdate string
	ToolCallID    string
	Kind          string
	Status        string
	Name          string
	MetaToolName  string
	// MCPCall is codex-acp's _meta.is_mcp_tool_call.
	MCPCall       bool
	Title         string
	MCPServer     string
	MCPTool       string
	Locations     []string
	Used          *int64
	Size          *int64
	Chars         int
	CurrentModeID string
	ConfigOptions []configOption
}

// decodeUpdate reads an update field by field, so one field of an unexpected
// shape costs that field, not the update: an agent that sends a mode report
// beside something this client does not know still has its mode read.
func decodeUpdate(raw json.RawMessage) (sessionUpdate, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return sessionUpdate{}, false
	}
	var u sessionUpdate
	str := func(key string) string {
		var v string
		_ = json.Unmarshal(fields[key], &v)
		return v
	}
	u.SessionUpdate = str("sessionUpdate")
	u.ToolCallID = str("toolCallId")
	u.Kind = str("kind")
	u.Status = str("status")
	u.Name = str("name")
	u.Title = str("title")
	u.CurrentModeID = str("currentModeId")
	var meta struct {
		ClaudeCode struct {
			ToolName string `json:"toolName"`
		} `json:"claudeCode"`
		MCPCall bool `json:"is_mcp_tool_call"`
	}
	if json.Unmarshal(fields["_meta"], &meta) == nil {
		u.MetaToolName = meta.ClaudeCode.ToolName
		u.MCPCall = meta.MCPCall
	}
	var input struct {
		Server string `json:"server"`
		Tool   string `json:"tool"`
	}
	if json.Unmarshal(fields["rawInput"], &input) == nil {
		u.MCPServer, u.MCPTool = input.Server, input.Tool
	}
	var locations []json.RawMessage
	if json.Unmarshal(fields["locations"], &locations) == nil {
		for _, l := range locations {
			var loc struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(l, &loc) == nil && loc.Path != "" {
				u.Locations = append(u.Locations, loc.Path)
			}
		}
	}
	var n int64
	if json.Unmarshal(fields["used"], &n) == nil && len(fields["used"]) > 0 {
		used := n
		u.Used = &used
	}
	if json.Unmarshal(fields["size"], &n) == nil && len(fields["size"]) > 0 {
		size := n
		u.Size = &size
	}
	var block struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(fields["content"], &block) == nil {
		u.Chars = len(block.Text)
	}
	var options []json.RawMessage
	if json.Unmarshal(fields["configOptions"], &options) == nil {
		for _, o := range options {
			var opt configOption
			if json.Unmarshal(o, &opt) == nil {
				u.ConfigOptions = append(u.ConfigOptions, opt)
			}
		}
	}
	return u, true
}

// onNotification handles the agent's notifications in wire order. Only
// session/update is read; _auth/status_update, which carries the account's
// email, and every extension are dropped unread (invariant 7).
func (s *session) onNotification(method string, params json.RawMessage) {
	if method != "session/update" {
		return
	}
	var n struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &n) != nil || !s.ours(n.SessionID) {
		return
	}
	u, ok := decodeUpdate(n.Update)
	if !ok {
		return
	}
	switch u.SessionUpdate {
	case "current_mode_update":
		s.reportMode(u.CurrentModeID)
	case "config_option_update":
		if v, ok := stringValue(modeOption(u.ConfigOptions)); ok {
			s.reportMode(v)
		}
	case "tool_call", "tool_call_update":
		info := s.noteTool(u)
		kind := driver.UpdateToolCall
		if u.SessionUpdate == "tool_call_update" {
			kind = driver.UpdateToolCallUpdate
		}
		s.emit(driver.Update{Kind: kind, ToolCallID: u.ToolCallID, Tool: info.name, ToolKind: info.kind, Status: toolStatus(u.Status)})
	case "usage_update":
		s.mu.Lock()
		if u.Used != nil {
			s.context.ContextUsed = *u.Used
		}
		if u.Size != nil {
			s.context.ContextSize = *u.Size
		}
		usage := s.context
		s.mu.Unlock()
		s.emit(driver.Update{Kind: driver.UpdateUsage, Usage: &usage})
	case "agent_message_chunk":
		s.emit(driver.Update{Kind: driver.UpdateAgentMessageChunk, Chars: u.Chars})
	case "plan":
		s.emit(driver.Update{Kind: driver.UpdatePlan})
	}
}

// ours reports whether a message names this session. One adapter process
// serves one session, so this is a guard, not routing.
func (s *session) ours(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id == "" || id == s.id
}

func (s *session) emit(u driver.Update) {
	u.At = time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updatesClosed || s.replaying {
		return
	}
	select {
	case s.updates <- u:
	default:
	}
}

// onRequest answers the agent's requests. The client offers no fs and no
// terminal, so a permission is the only request it serves.
func (s *session) onRequest(id json.RawMessage, method string, params json.RawMessage) {
	if method != "session/request_permission" {
		s.conn.replyError(id, codeMethodNotFound, "method not supported by this client")
		return
	}
	var p struct {
		SessionID string          `json:"sessionId"`
		ToolCall  json.RawMessage `json:"toolCall"`
		Options   []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		s.conn.replyError(id, codeInvalidParams, "unreadable permission request")
		return
	}
	call, _ := decodeUpdate(p.ToolCall)

	s.mu.Lock()
	t := s.turn
	askable := t != nil && s.verified && s.unsafe == nil && !s.closed && s.id != "" && p.SessionID == s.id
	canceled := t != nil && t.canceled
	s.mu.Unlock()

	// Only a request the session can be asked is merged into what it knows
	// of its tool calls: one for another session, or outside a turn, could
	// otherwise name a call that a later request is decided on.
	info := toolInfo{name: toolName(call), kind: toolKind(call.Kind), locations: call.Locations}
	if askable {
		info = s.noteTool(call)
	}
	req := driver.PermissionRequest{
		ToolCallID: call.ToolCallID,
		Tool:       info.name,
		Kind:       info.kind,
		Locations:  slices.Clone(info.locations),
	}
	for _, o := range p.Options {
		req.Options = append(req.Options, driver.PermissionOption{ID: o.OptionID, Kind: driver.PermissionOptionKind(o.Kind)})
	}

	if canceled {
		// A turn being canceled answers its open requests as canceled, as
		// ACP asks of a client.
		s.conn.reply(id, map[string]any{"outcome": map[string]any{"outcome": outcomeCanceled}})
		return
	}
	allow := askable && s.policy.Decide(context.Background(), req).Allow
	option := chooseOption(req.Options, allow)
	if allow && option == "" {
		// Allowing is only ever allow_once; without it, the answer is no.
		allow = false
		option = chooseOption(req.Options, false)
	}
	if !allow {
		s.mu.Lock()
		if t != nil && s.turn == t {
			t.refusals = append(t.refusals, driver.Refusal{ToolCallID: req.ToolCallID, Tool: refusalTool(req)})
		}
		s.mu.Unlock()
	}
	s.emit(driver.Update{Kind: driver.UpdatePermission, ToolCallID: req.ToolCallID, Tool: req.Tool, ToolKind: req.Kind, Allowed: allow})
	if option == "" {
		s.conn.reply(id, map[string]any{"outcome": map[string]any{"outcome": outcomeCanceled}})
		return
	}
	s.conn.reply(id, map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": option}})
}

// outcomeCanceled is ACP's permission outcome for a request not answered by
// an option.
const outcomeCanceled = "cancelled" //nolint:misspell // ACP's wire value

// chooseOption selects by kind, never by id or label (invariant 3).
func chooseOption(options []driver.PermissionOption, allow bool) string {
	want := []driver.PermissionOptionKind{driver.RejectOnce, driver.RejectAlways}
	if allow {
		want = []driver.PermissionOptionKind{driver.AllowOnce}
	}
	for _, kind := range want {
		for _, o := range options {
			if o.Kind == kind && o.ID != "" {
				return o.ID
			}
		}
	}
	return ""
}

func refusalTool(req driver.PermissionRequest) string {
	if req.Tool != "" {
		return req.Tool
	}
	return string(req.Kind)
}

// toolInfo is what is known of one tool call.
type toolInfo struct {
	name      string
	kind      driver.ToolKind
	locations []string
}

// maxTools bounds the tool calls remembered for one session.
const maxTools = 1024

// noteTool merges what u says about its tool call into what the session
// knows of it, and returns the result. A later message fills in what an
// earlier one left out; it never blanks what was known.
func (s *session) noteTool(u sessionUpdate) toolInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := s.tools[u.ToolCallID]
	if name := toolName(u); name != "" {
		info.name = name
	}
	if u.Kind != "" {
		info.kind = toolKind(u.Kind)
	}
	if info.kind == "" {
		info.kind = driver.ToolOther
	}
	if len(u.Locations) > 0 {
		info.locations = slices.Clone(u.Locations)
	}
	if u.ToolCallID == "" {
		return info
	}
	switch toolStatus(u.Status) {
	case driver.ToolCompleted, driver.ToolFailed:
		delete(s.tools, u.ToolCallID)
	default:
		if _, known := s.tools[u.ToolCallID]; known || len(s.tools) < maxTools {
			s.tools[u.ToolCallID] = info
		}
	}
	return info
}

// toolName is the agent's name for the tool, where it says one: never the
// call's title or input, which carry what the call does.
//
// claude-agent-acp names its tools in _meta or in name (mcp__<server>__<tool>
// for an MCP tool), and a name it gives is final: its titles and raw inputs
// are the model's to write. codex-acp gives an MCP call no name; it marks it
// in _meta and titles it "mcp.<server>.<tool>" beside a raw input of
// {server, tool}. Only a call with no name, so marked, whose title and input
// agree, is given the MCP tool's name.
func toolName(u sessionUpdate) string {
	if u.MetaToolName != "" {
		return plainName(u.MetaToolName)
	}
	if u.Name != "" {
		return plainName(u.Name)
	}
	if u.MCPCall && u.MCPServer != "" && u.MCPTool != "" && u.Title == "mcp."+u.MCPServer+"."+u.MCPTool &&
		plainName(u.MCPServer) == u.MCPServer && plainName(u.MCPTool) == u.MCPTool &&
		!strings.Contains(u.MCPServer, "__") && !strings.Contains(u.MCPServer, ".") {
		return "mcp__" + u.MCPServer + "__" + u.MCPTool
	}
	return ""
}

// plainName keeps a tool name to identifier characters.
func plainName(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			out = append(out, r)
		}
		if len(out) >= 100 {
			break
		}
	}
	return string(out)
}

func toolKind(kind string) driver.ToolKind {
	switch k := driver.ToolKind(kind); k {
	case driver.ToolRead, driver.ToolEdit, driver.ToolDelete, driver.ToolMove, driver.ToolSearch,
		driver.ToolExecute, driver.ToolThink, driver.ToolFetch, driver.ToolOther:
		return k
	}
	return driver.ToolOther
}

func toolStatus(status string) driver.ToolStatus {
	switch st := driver.ToolStatus(status); st {
	case driver.ToolPending, driver.ToolInProgress, driver.ToolCompleted, driver.ToolFailed:
		return st
	}
	return ""
}

// validSessionID is an id the ledger can keep and a later process can hand
// back: short, and plain.
func validSessionID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' && r != ':' {
			return false
		}
	}
	return true
}

// mergeEnv adds the adapter's own variables to the dispatcher's allowlisted
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

// setEnv sets the adapter's own switches over whatever env holds of the same
// name.
func setEnv(env []string, set map[string]string) []string {
	if len(set) == 0 {
		return env
	}
	out := make([]string, 0, len(env)+len(set))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := set[k]; !ok {
			out = append(out, kv)
		}
	}
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}
