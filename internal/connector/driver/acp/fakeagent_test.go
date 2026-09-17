//go:build unix

package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The test binary doubles as a fake ACP agent: run as
// `<test binary> -fake-acp-agent <scenario.json>`, it speaks ACP on stdio as
// the scenario says and records what it was started with and what it was
// told. Arguments, not the environment, name the scenario, because the driver
// under test passes the agent an allowlisted environment.
const (
	fakeAgentArg = "-fake-acp-agent"
	fakeChildArg = "-fake-acp-child"
)

type scenario struct {
	Record string `json:"record"`
	// Probe names variables whose values are recorded (test values only).
	Probe []string `json:"probe"`

	ProtocolVersion int    `json:"protocol_version"`
	AgentName       string `json:"agent_name"`
	AgentVersion    string `json:"agent_version"`
	FailInitialize  bool   `json:"fail_initialize"`
	LoadSession     bool   `json:"load_session"`
	Resume          bool   `json:"resume"`
	SessionID       string `json:"session_id"`

	Modes       []string `json:"modes"`
	CurrentMode string   `json:"current_mode"`
	ModeConfig  bool     `json:"mode_config"`
	// Confirm is how a set mode is confirmed: "readback" (the config option
	// answer reports it), "stale" (it reports the old mode), "notify" (a
	// current_mode_update follows set_mode), "none", or "error" (set_mode
	// fails).
	Confirm string `json:"confirm"`
	// ModeBeforeSetAnswer is a mode update sent on the wire just before the
	// answer to the set that was supposed to confirm the asking mode.
	ModeBeforeSetAnswer string `json:"mode_before_set_answer"`

	// Replay are updates sent before a load's response.
	Replay []json.RawMessage `json:"replay"`
	// Turns script each prompt in order; the last repeats.
	Turns []turnScript `json:"turns"`

	// StopReadingAfter names a method after which the agent reads no more
	// input.
	StopReadingAfter string `json:"stop_reading_after"`
	// Hang names a method the agent never answers.
	Hang       string `json:"hang"`
	AuthEmail  string `json:"auth_email"`
	SpawnChild bool   `json:"spawn_child"`
	// EscapingChild starts the child in a session of its own, holding the
	// agent's output: a process group kill does not reach it.
	EscapingChild   bool `json:"escaping_child"`
	IgnoreStdinEOF  bool `json:"ignore_stdin_eof"`
	IgnoreTerminate bool `json:"ignore_terminate"`
}

type turnScript struct {
	Steps []step `json:"steps"`
	// Stop is the stop reason; with WaitForCancel it is sent once
	// session/cancel arrives.
	Stop          string          `json:"stop"`
	Usage         json.RawMessage `json:"usage,omitempty"`
	WaitForCancel bool            `json:"wait_for_cancel"`
	ErrorMessage  string          `json:"error_message"`
	// Hang never answers the prompt.
	Hang bool `json:"hang"`
	// FloodPermissions asks for this many permissions at once.
	FloodPermissions int             `json:"flood_permissions"`
	FloodCall        json.RawMessage `json:"flood_call,omitempty"`
}

type step struct {
	Update     json.RawMessage `json:"update,omitempty"`
	SessionID  string          `json:"session_id"`
	Permission json.RawMessage `json:"permission,omitempty"`
	ModeChange string          `json:"mode_change"`
	SleepMS    int             `json:"sleep_ms"`
}

type agentRecord struct {
	PID      int               `json:"pid"`
	ChildPID int               `json:"child_pid"`
	Env      []string          `json:"env"`
	Probe    map[string]string `json:"probe"`
	Methods  []string          `json:"methods"`
	Params   map[string]json.RawMessage
	Outcomes []json.RawMessage `json:"outcomes"`
}

type fakeAgent struct {
	sc  scenario
	out *bufio.Writer

	mu       sync.Mutex
	rec      agentRecord
	nextID   int
	pending  map[int]chan json.RawMessage
	mode     string
	prompts  int
	canceled chan struct{}
}

func runFakeAgent(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		os.Exit(3)
	}
	var sc scenario
	if json.Unmarshal(raw, &sc) != nil {
		os.Exit(3)
	}
	if sc.IgnoreTerminate {
		signal.Ignore(syscall.SIGTERM)
	}
	a := &fakeAgent{sc: sc, out: bufio.NewWriter(os.Stdout), pending: map[int]chan json.RawMessage{}, mode: sc.CurrentMode}
	a.rec.PID = os.Getpid()
	a.rec.Params = map[string]json.RawMessage{}
	a.rec.Probe = map[string]string{}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		a.rec.Env = append(a.rec.Env, name)
		if slices.Contains(sc.Probe, name) {
			a.rec.Probe[name] = os.Getenv(name)
		}
	}
	slices.Sort(a.rec.Env)
	if sc.SpawnChild || sc.EscapingChild {
		child := exec.CommandContext(context.Background(), os.Args[0], fakeChildArg)
		if sc.EscapingChild {
			child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			child.Stdout = os.Stdout
		}
		if child.Start() == nil {
			a.rec.ChildPID = child.Process.Pid
		}
	}
	a.flush()

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 16<<20)
	for in.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(in.Bytes(), &m) != nil {
			continue
		}
		if m.Method == "" {
			var id int
			if json.Unmarshal(m.ID, &id) == nil {
				a.mu.Lock()
				ch := a.pending[id]
				a.mu.Unlock()
				if ch != nil {
					ch <- m.Result
				}
			}
			continue
		}
		a.mu.Lock()
		a.rec.Methods = append(a.rec.Methods, m.Method)
		a.rec.Params[m.Method] = m.Params
		a.mu.Unlock()
		a.flush()
		go a.handle(m.ID, m.Method, m.Params)
		if m.Method == sc.StopReadingAfter {
			select {}
		}
	}
	if sc.IgnoreStdinEOF {
		select {}
	}
}

// runFakeChild is a process the fake agent leaves in its group: it ignores
// SIGTERM, so only a group SIGKILL ends it.
func runFakeChild() {
	signal.Ignore(syscall.SIGTERM, syscall.SIGHUP)
	time.Sleep(time.Hour)
}

func (a *fakeAgent) flush() {
	a.mu.Lock()
	data, _ := json.Marshal(a.rec)
	a.mu.Unlock()
	tmp := a.sc.Record + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, a.sc.Record)
	}
}

func (a *fakeAgent) send(v any) {
	data, _ := json.Marshal(v)
	a.mu.Lock()
	defer a.mu.Unlock()
	_, _ = a.out.Write(append(data, '\n'))
	_ = a.out.Flush()
}

func (a *fakeAgent) reply(id json.RawMessage, result any) {
	a.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (a *fakeAgent) fail(id json.RawMessage, message string) {
	a.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32603, "message": message}})
}

func (a *fakeAgent) update(sessionID string, update any) {
	a.send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": update}})
}

func (a *fakeAgent) request(method string, params any) json.RawMessage {
	a.mu.Lock()
	a.nextID++
	id := a.nextID
	ch := make(chan json.RawMessage, 1)
	a.pending[id] = ch
	a.mu.Unlock()
	a.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return <-ch
}

func (a *fakeAgent) sessionID() string {
	if a.sc.SessionID != "" {
		return a.sc.SessionID
	}
	return "sess-1"
}

func (a *fakeAgent) modes() map[string]any {
	available := make([]any, 0, len(a.sc.Modes))
	for _, m := range a.sc.Modes {
		available = append(available, map[string]any{"id": m, "name": m})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return map[string]any{"currentModeId": a.mode, "availableModes": available}
}

func (a *fakeAgent) configOptions(current string) []any {
	options := make([]any, 0, len(a.sc.Modes))
	for _, m := range a.sc.Modes {
		options = append(options, map[string]any{"value": m, "name": m})
	}
	return []any{
		map[string]any{"id": "model", "category": "model", "type": "select", "currentValue": "x", "options": []any{map[string]any{"value": "x", "name": "x"}}},
		map[string]any{"id": "mode", "category": "mode", "type": "select", "currentValue": current, "options": options},
	}
}

func (a *fakeAgent) sessionState() map[string]any {
	st := map[string]any{"sessionId": a.sessionID()}
	if len(a.sc.Modes) > 0 {
		st["modes"] = a.modes()
	}
	if a.sc.ModeConfig {
		a.mu.Lock()
		st["configOptions"] = a.configOptions(a.mode)
		a.mu.Unlock()
	}
	return st
}

func (a *fakeAgent) handle(id json.RawMessage, method string, params json.RawMessage) {
	sc := a.sc
	if method == sc.Hang {
		return
	}
	switch method {
	case "initialize":
		if sc.AuthEmail != "" {
			a.send(map[string]any{"jsonrpc": "2.0", "method": "_auth/status_update", "params": map[string]any{"authStatus": map[string]any{"account": map[string]any{"email": sc.AuthEmail}}}})
		}
		if sc.FailInitialize {
			a.fail(id, "initialize failed for "+sc.AuthEmail)
			return
		}
		version := sc.ProtocolVersion
		if version == 0 {
			version = 1
		}
		caps := map[string]any{"loadSession": sc.LoadSession}
		if sc.Resume {
			caps["sessionCapabilities"] = map[string]any{"resume": map[string]any{}}
		}
		a.reply(id, map[string]any{"protocolVersion": version, "agentCapabilities": caps, "agentInfo": map[string]any{"name": sc.AgentName, "version": sc.AgentVersion}})
	case "session/new":
		a.reply(id, a.sessionState())
	case "session/load", "session/resume":
		for _, u := range sc.Replay {
			a.update(a.sessionID(), u)
		}
		st := a.sessionState()
		delete(st, "sessionId")
		a.reply(id, st)
	case "session/set_mode":
		var p struct {
			ModeID string `json:"modeId"`
		}
		_ = json.Unmarshal(params, &p)
		switch sc.Confirm {
		case "error":
			a.fail(id, "no")
			return
		case "stale", "none":
		default:
			a.mu.Lock()
			a.mode = p.ModeID
			a.mu.Unlock()
		}
		if sc.Confirm == "notify" {
			a.update(a.sessionID(), map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": p.ModeID})
		}
		a.reply(id, map[string]any{})
	case "session/set_config_option":
		var p struct {
			Value string `json:"value"`
		}
		_ = json.Unmarshal(params, &p)
		a.mu.Lock()
		if sc.Confirm != "stale" && sc.Confirm != "none" {
			a.mode = p.Value
		}
		opts := a.configOptions(a.mode)
		a.mu.Unlock()
		if sc.ModeBeforeSetAnswer != "" {
			a.update(a.sessionID(), map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": sc.ModeBeforeSetAnswer})
		}
		a.reply(id, map[string]any{"configOptions": opts})
	case "session/cancel":
		a.mu.Lock()
		if a.canceled != nil {
			close(a.canceled)
			a.canceled = nil
		}
		a.mu.Unlock()
	case "session/prompt":
		a.prompt(id)
	default:
		if len(id) > 0 {
			a.fail(id, "unknown method")
		}
	}
}

func (a *fakeAgent) prompt(id json.RawMessage) {
	a.mu.Lock()
	n := a.prompts
	a.prompts++
	canceled := make(chan struct{})
	a.canceled = canceled
	a.mu.Unlock()
	if len(a.sc.Turns) == 0 {
		a.reply(id, map[string]any{"stopReason": "end_turn"})
		return
	}
	ts := a.sc.Turns[min(n, len(a.sc.Turns)-1)]
	for _, st := range ts.Steps {
		if st.SleepMS > 0 {
			time.Sleep(time.Duration(st.SleepMS) * time.Millisecond)
		}
		sid := a.sessionID()
		if st.SessionID != "" {
			sid = st.SessionID
		}
		if len(st.Update) > 0 {
			a.update(sid, st.Update)
		}
		if st.ModeChange != "" {
			a.update(sid, map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": st.ModeChange})
		}
		if len(st.Permission) > 0 {
			var p map[string]any
			_ = json.Unmarshal(st.Permission, &p)
			if _, ok := p["sessionId"]; !ok {
				p["sessionId"] = sid
			}
			outcome := a.request("session/request_permission", p)
			a.mu.Lock()
			a.rec.Outcomes = append(a.rec.Outcomes, outcome)
			a.mu.Unlock()
			a.flush()
		}
	}
	if ts.FloodPermissions > 0 {
		var wg sync.WaitGroup
		for i := range ts.FloodPermissions {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var p map[string]any
				_ = json.Unmarshal(ts.FloodCall, &p)
				p["sessionId"] = a.sessionID()
				call, _ := p["toolCall"].(map[string]any)
				call["toolCallId"] = fmt.Sprintf("flood-%d", i)
				outcome := a.request("session/request_permission", p)
				a.mu.Lock()
				a.rec.Outcomes = append(a.rec.Outcomes, outcome)
				a.mu.Unlock()
			}()
		}
		wg.Wait()
		a.flush()
	}
	if ts.Hang {
		select {}
	}
	if ts.WaitForCancel {
		<-canceled
	}
	if ts.ErrorMessage != "" {
		a.fail(id, ts.ErrorMessage)
		return
	}
	result := map[string]any{"stopReason": ts.Stop}
	if len(ts.Usage) > 0 {
		result["usage"] = ts.Usage
	}
	a.reply(id, result)
}
