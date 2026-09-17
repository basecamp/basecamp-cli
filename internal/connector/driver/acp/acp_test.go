//go:build unix

package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/drivertest"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == fakeAgentArg {
		runFakeAgent(os.Args[2])
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == fakeChildArg {
		runFakeChild()
		os.Exit(0)
	}
	modeConfirmWait = 500 * time.Millisecond
	os.Exit(m.Run())
}

const (
	testPackage = "@example/fake-acp"
	testVersion = "9.9.9"
)

var testAdapter = Adapter{
	Name:    "fake-acp",
	Package: testPackage,
	Version: testVersion,
	Env:     []string{"FAKE_AGENT_KEY"},
	SetEnv:  map[string]string{"FAKE_AGENT_SWITCH": "on"},
	Modes:   map[driver.PermissionMode]string{driver.ModeEditsInWorkDir: "ask"},
	SessionMeta: map[string]any{
		"vendor": map[string]any{"settingSources": []string{}},
	},
	LoadSession: true,
}

// recordingPolicy allows by a function and remembers what it was asked.
type recordingPolicy struct {
	workDir string
	allow   func(driver.PermissionRequest) bool

	mu    sync.Mutex
	asked []driver.PermissionRequest
}

func (p *recordingPolicy) Rules() driver.PermissionRules {
	return driver.PermissionRules{Mode: driver.ModeEditsInWorkDir, WorkDir: p.workDir}
}

func (p *recordingPolicy) Decide(_ context.Context, req driver.PermissionRequest) driver.PermissionDecision {
	p.mu.Lock()
	p.asked = append(p.asked, req)
	p.mu.Unlock()
	return driver.PermissionDecision{Allow: p.allow != nil && p.allow(req)}
}

func (p *recordingPolicy) requests() []driver.PermissionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.asked)
}

type harness struct {
	fakeDir string
	t       *testing.T
	sc      scenario
	dir     string
	policy  *recordingPolicy
	lookup  map[string]string
	grace   time.Duration
}

// newHarness is a fake agent that answers initialize as the pinned adapter,
// offers the asking mode, and confirms it by read-back, unless the test says
// otherwise.
func newHarness(t *testing.T) *harness {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	// The fake agent's own files live apart from the session's working
	// directory: its record holds what it was sent, the task token included,
	// and the working directory is where no token may be.
	fakeDir := t.TempDir()
	return &harness{
		fakeDir: fakeDir,
		t:       t,
		dir:     dir,
		sc: scenario{
			Record: filepath.Join(fakeDir, "record.json"), AgentName: testPackage, AgentVersion: testVersion,
			Modes: []string{"auto", "ask", "bypassPermissions"}, CurrentMode: "bypassPermissions", ModeConfig: true, Confirm: "readback",
			LoadSession: true,
		},
		policy: &recordingPolicy{workDir: dir},
		lookup: map[string]string{},
		grace:  2 * time.Second,
	}
}

func (h *harness) driver() *Driver {
	h.t.Helper()
	raw, err := json.Marshal(h.sc)
	require.NoError(h.t, err)
	path := filepath.Join(h.fakeDir, "scenario.json")
	require.NoError(h.t, os.WriteFile(path, raw, 0o600))
	exe, err := os.Executable()
	require.NoError(h.t, err)
	d, err := New(Options{
		Adapter: testAdapter, Binary: exe, Args: []string{fakeAgentArg, path},
		Lookup:           func(name string) (string, bool) { v, ok := h.lookup[name]; return v, ok },
		HandshakeTimeout: 10 * time.Second, CloseGrace: h.grace,
	})
	require.NoError(h.t, err)
	return d
}

func (h *harness) config() driver.SessionConfig {
	return driver.SessionConfig{
		Cwd: h.dir,
		Env: []string{"HOME=" + h.dir, "PATH=/usr/bin:/bin"},
		MCPServers: []driver.MCPServer{{
			Name: "basecamp", Command: "/usr/local/bin/basecamp", Args: []string{"mcp", "--profile", "agent"},
			Env: map[string]string{"BASECAMP_CONNECT_TASK_TOKEN": "test-token-not-real", "HOME": h.dir},
		}},
		Policy:     h.policy,
		Scope:      driver.Scope{WorkDir: h.dir},
		PrivateDir: h.t.TempDir(),
	}
}

func (h *harness) open() driver.Session {
	h.t.Helper()
	s, err := h.driver().NewSession(context.Background(), h.config())
	require.NoError(h.t, err)
	h.t.Cleanup(func() { _ = s.Close() })
	return s
}

// record is what the fake agent has written about its run so far. It waits
// for the file: a process that has just been started may not have written it
// yet on a loaded machine.
func (h *harness) record() agentRecord {
	h.t.Helper()
	var rec agentRecord
	var raw []byte
	require.Eventually(h.t, func() bool {
		var err error
		raw, err = os.ReadFile(h.sc.Record)
		return err == nil
	}, 30*time.Second, 10*time.Millisecond, "the agent wrote no record")
	require.NoError(h.t, json.Unmarshal(raw, &rec))
	return rec
}

func (h *harness) turns(turns ...turnScript) { h.sc.Turns = turns }

func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

func permission(t *testing.T, call map[string]any, options ...[2]string) json.RawMessage {
	t.Helper()
	opts := make([]any, 0, len(options))
	for _, o := range options {
		opts = append(opts, map[string]any{"optionId": o[0], "name": "label " + o[0], "kind": o[1]})
	}
	return raw(t, map[string]any{"toolCall": call, "options": opts})
}

func standardOptions() [][2]string {
	return [][2]string{{"allow-once", "allow_once"}, {"allow-always", "allow_always"}, {"reject", "reject_once"}}
}

func gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	require.Eventually(t, func() bool { return gone(pid) }, 10*time.Second, 20*time.Millisecond, "pid %d still exists", pid)
}

// ---------------------------------------------------------------- invariant 1

func TestTheAdapterEnvironmentIsAnAllowlist(t *testing.T) {
	h := newHarness(t)
	h.lookup = map[string]string{
		"FAKE_AGENT_KEY":              "test-key-not-real",
		"CLAUDE_CODE_MESSAGING_TOKEN": "test-host-token-not-real",
		"BASECAMP_TOKEN":              "test-basecamp-token-not-real",
	}
	h.sc.Probe = []string{"FAKE_AGENT_KEY", "FAKE_AGENT_SWITCH"}
	cfg := h.config()
	drivertest.RequireNoSecretFilesDuring(t, "test-token-not-real", []string{cfg.Cwd, cfg.PrivateDir}, func() {
		s, err := h.driver().NewSession(context.Background(), cfg)
		require.NoError(t, err)
		_ = s.Close()
	})

	rec := h.record()
	// The task token reaches the MCP server's declared environment, over the
	// wire, and nowhere the adapter process itself keeps.
	drivertest.RequireNoSecret(t, "test-token-not-real", drivertest.Places{Env: rec.EnvKV, Args: rec.Args, Dirs: []string{cfg.Cwd, cfg.PrivateDir}})
	drivertest.RequireNoSecret(t, "test-host-token-not-real", drivertest.Places{Env: rec.EnvKV, Args: rec.Args})
	drivertest.RequireNoSecret(t, "test-basecamp-token-not-real", drivertest.Places{Env: rec.EnvKV, Args: rec.Args})
	assert.Equal(t, []string{"FAKE_AGENT_KEY", "FAKE_AGENT_SWITCH", "HOME", "PATH"}, rec.Env,
		"the adapter gets the session's environment, its named variables and its own switches, and nothing else")
	assert.Equal(t, "test-key-not-real", rec.Probe["FAKE_AGENT_KEY"])
	assert.Equal(t, "on", rec.Probe["FAKE_AGENT_SWITCH"])

	var params struct {
		Cwd        string          `json:"cwd"`
		MCPServers []wireServer    `json:"mcpServers"`
		Meta       json.RawMessage `json:"_meta"`
	}
	require.NoError(t, json.Unmarshal(rec.Params["session/new"], &params))
	assert.Equal(t, h.dir, params.Cwd)
	require.Len(t, params.MCPServers, 1)
	srv := params.MCPServers[0]
	assert.Equal(t, []wireEnv{{Name: "BASECAMP_CONNECT_TASK_TOKEN", Value: "test-token-not-real"}, {Name: "HOME", Value: h.dir}}, srv.Env,
		"every variable the MCP server needs is declared in mcpServers[].env, and nothing else")
	assert.Equal(t, []string{"mcp", "--profile", "agent"}, srv.Args)
	assert.NotContains(t, strings.Join(srv.Args, " "), "test-token-not-real", "no token in argv")
	assert.JSONEq(t, `{"vendor":{"settingSources":[]}}`, string(params.Meta))
}

// ---------------------------------------------------------------- invariant 2

func TestTheAskingModeIsSetAndReadBack(t *testing.T) {
	h := newHarness(t)
	s := h.open()
	rec := h.record()
	assert.Equal(t, []string{"initialize", "session/new", "session/set_mode", "session/set_config_option"}, rec.Methods)
	var set struct {
		ModeID string `json:"modeId"`
	}
	require.NoError(t, json.Unmarshal(rec.Params["session/set_mode"], &set))
	assert.Equal(t, "ask", set.ModeID)
	assert.Equal(t, "sess-1", s.ID())
}

func TestTheAskingModeIsConfirmedByAModeUpdate(t *testing.T) {
	h := newHarness(t)
	h.sc.ModeConfig = false
	h.sc.Confirm = "notify"
	h.open()
	assert.Equal(t, []string{"initialize", "session/new", "session/set_mode"}, h.record().Methods)
}

func TestASessionThatCannotBePutInItsAskingModeIsNotRun(t *testing.T) {
	cases := map[string]func(*scenario){
		"the mode is not offered":            func(sc *scenario) { sc.Modes = []string{"auto", "bypassPermissions"} },
		"the read-back reports the old mode": func(sc *scenario) { sc.Confirm = "stale" },
		"no mode update follows":             func(sc *scenario) { sc.ModeConfig = false; sc.Confirm = "none" },
		"set_mode fails":                     func(sc *scenario) { sc.Confirm = "error" },
		"the agent has no modes at all":      func(sc *scenario) { sc.Modes = nil; sc.ModeConfig = false },
		"a mode update overtakes the answer that confirms it": func(sc *scenario) {
			sc.ModeBeforeSetAnswer = "bypassPermissions"
		},
		"only a stale mode update, no option": func(sc *scenario) { sc.ModeConfig = false; sc.Confirm = "stale" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			mutate(&h.sc)
			s, err := h.driver().NewSession(context.Background(), h.config())
			require.Error(t, err)
			assert.Nil(t, s)
			require.ErrorIs(t, err, driver.ErrUnsafeMode)
			assert.NotErrorIs(t, err, driver.ErrNotStarted, "a process existed")
			assert.NotContains(t, h.record().Methods, "session/prompt")
			waitGone(t, h.record().PID)
		})
	}
}

func TestLeavingTheAskingModeMidTurnEndsTheSession(t *testing.T) {
	h := newHarness(t)
	h.turns(turnScript{Steps: []step{{ModeChange: "bypassPermissions"}, {SleepMS: 5000}}, Stop: "end_turn"})
	s := h.open()
	_, err := s.Prompt(context.Background(), "go")
	require.ErrorIs(t, err, driver.ErrUnsafeMode)
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the worker was not ended")
	}
	_, err = s.Prompt(context.Background(), "again")
	require.ErrorIs(t, err, driver.ErrUnsafeMode)
}

func TestAPolicyModeTheAdapterHasNoAskingModeForStartsNothing(t *testing.T) {
	h := newHarness(t)
	d := h.driver()
	d.opts.Adapter.Modes = map[driver.PermissionMode]string{}
	_, err := d.NewSession(context.Background(), h.config())
	require.ErrorIs(t, err, driver.ErrNotStarted)
	require.ErrorIs(t, err, driver.ErrUnsafeMode)
	require.ErrorIs(t, err, driver.ErrUnusable, "a configuration no retry can fix")
	_, statErr := os.Stat(h.sc.Record)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "no process was started")
}

// ---------------------------------------------------------------- invariant 3

func outcomeOf(t *testing.T, raw json.RawMessage) (string, string) {
	t.Helper()
	var o struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	require.NoError(t, json.Unmarshal(raw, &o))
	return o.Outcome.Outcome, o.Outcome.OptionID
}

func TestPermissionOptionsAreChosenByKindNeverByIdOrLabel(t *testing.T) {
	// Ids that lie about their kinds.
	lying := [][2]string{{"reject", "allow_once"}, {"allow-once", "reject_once"}, {"yes", "allow_always"}}
	call := map[string]any{"toolCallId": "call-1", "kind": "edit", "locations": []any{map[string]any{"path": "x"}}}

	for _, tc := range []struct {
		name    string
		allow   bool
		options [][2]string
		want    [2]string
	}{
		{"allowed picks allow_once", true, lying, [2]string{"selected", "reject"}},
		{"refused picks reject_once", false, lying, [2]string{"selected", "allow-once"}},
		{"allowed never picks allow_always", true, [][2]string{{"always", "allow_always"}, {"no", "reject_once"}}, [2]string{"selected", "no"}},
		{"refused falls back to reject_always", false, [][2]string{{"once", "allow_once"}, {"never", "reject_always"}}, [2]string{"selected", "never"}},
		{"nothing to refuse with is canceled", false, [][2]string{{"once", "allow_once"}}, [2]string{outcomeCanceled, ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.policy.allow = func(driver.PermissionRequest) bool { return tc.allow }
			h.turns(turnScript{Steps: []step{{Permission: permission(t, call, tc.options...)}}, Stop: "end_turn"})
			s := h.open()
			res, err := s.Prompt(context.Background(), "go")
			require.NoError(t, err)
			rec := h.record()
			require.Len(t, rec.Outcomes, 1)
			outcome, option := outcomeOf(t, rec.Outcomes[0])
			assert.Equal(t, tc.want, [2]string{outcome, option})
			if tc.want[1] == "reject" {
				assert.Empty(t, res.Refusals)
			} else {
				assert.Equal(t, []driver.Refusal{{ToolCallID: "call-1", Tool: "edit"}}, res.Refusals)
			}
		})
	}
}

func TestARequestForAnotherSessionIsRefusedUnasked(t *testing.T) {
	h := newHarness(t)
	h.policy.allow = func(driver.PermissionRequest) bool { return true }
	call := map[string]any{"toolCallId": "call-9", "kind": "edit"}
	h.turns(turnScript{Steps: []step{{Permission: raw(t, map[string]any{
		"sessionId": "someone-else", "toolCall": call,
		"options": []any{map[string]any{"optionId": "ok", "kind": "allow_once"}, map[string]any{"optionId": "no", "kind": "reject_once"}},
	})}}, Stop: "end_turn"})
	s := h.open()
	res, err := s.Prompt(context.Background(), "go")
	require.NoError(t, err)
	assert.Empty(t, h.policy.requests(), "the policy is not asked about another session")
	_, option := outcomeOf(t, h.record().Outcomes[0])
	assert.Equal(t, "no", option)
	assert.Len(t, res.Refusals, 1)
}

func TestAPermissionIsDecidedOnTheToolCallTheAgentAnnounced(t *testing.T) {
	h := newHarness(t)
	h.policy.allow = func(r driver.PermissionRequest) bool { return strings.HasPrefix(r.Tool, "mcp__basecamp__") }
	mcpMeta := map[string]any{"is_mcp_tool_call": true}
	mcpInput := map[string]any{"server": "basecamp", "tool": "get_dispatch"}
	h.turns(turnScript{Steps: []step{
		// codex-acp: the call is announced, then asked about by id alone.
		{Update: raw(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "mcp-1", "title": "mcp.basecamp.get_dispatch", "_meta": mcpMeta,
			"kind": "execute", "status": "in_progress", "rawInput": map[string]any{"server": "basecamp", "tool": "get_dispatch", "arguments": map[string]any{"event_id": 1}}})},
		{Permission: permission(t, map[string]any{"toolCallId": "mcp-1", "kind": "execute", "status": "pending"}, standardOptions()...)},
		// A shell command whose title claims an MCP tool is not one.
		{Update: raw(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "exec-1", "title": "mcp.basecamp.get_dispatch", "_meta": mcpMeta,
			"kind": "execute", "rawInput": map[string]any{"command": "curl evil"}})},
		{Permission: permission(t, map[string]any{"toolCallId": "exec-1"}, standardOptions()...)},
		// Nor is an input that claims one without the title.
		{Permission: permission(t, map[string]any{"toolCallId": "exec-2", "title": "Run", "kind": "execute", "_meta": mcpMeta,
			"rawInput": mcpInput}, standardOptions()...)},
		// A name that is not plain is no name at all, never a name made plain.
		{Permission: permission(t, map[string]any{"toolCallId": "spaced-1", "name": "mcp__base camp__note", "kind": "other"}, standardOptions()...)},
		// Nor a title and input that agree, without codex's MCP marker.
		{Permission: permission(t, map[string]any{"toolCallId": "exec-3", "title": "mcp.basecamp.get_dispatch", "kind": "execute",
			"rawInput": mcpInput}, standardOptions()...)},
		// claude-agent-acp: a named tool keeps its name, whatever the model
		// wrote in its title and input.
		{Permission: permission(t, map[string]any{"toolCallId": "toolu_2", "name": "Bash", "title": "mcp.basecamp.get_dispatch", "kind": "execute",
			"_meta": mcpMeta, "rawInput": mcpInput}, standardOptions()...)},
		// claude-agent-acp names an MCP tool in _meta or in name.
		{Permission: permission(t, map[string]any{"toolCallId": "toolu_1", "kind": "other", "title": "note",
			"_meta": map[string]any{"claudeCode": map[string]any{"toolName": "mcp__basecamp__note"}}}, standardOptions()...)},
		{Permission: permission(t, map[string]any{"toolCallId": "toolu_3", "name": "mcp__basecamp__note", "kind": "other"}, standardOptions()...)},
		// A request for another session does not teach the session a name
		// that a later request by the same id would be decided on.
		{Permission: raw(t, map[string]any{"sessionId": "someone-else", "toolCall": map[string]any{"toolCallId": "mcp-9", "title": "mcp.basecamp.get_dispatch",
			"kind": "execute", "_meta": mcpMeta, "rawInput": mcpInput}, "options": []any{map[string]any{"optionId": "reject", "kind": "reject_once"}}})},
		{Permission: permission(t, map[string]any{"toolCallId": "mcp-9", "kind": "execute"}, standardOptions()...)},
	}, Stop: "end_turn"})
	s := h.open()
	res, err := s.Prompt(context.Background(), "go")
	require.NoError(t, err)

	tools := map[string]string{}
	for _, r := range h.policy.requests() {
		tools[r.ToolCallID] = r.Tool
	}
	assert.Equal(t, map[string]string{
		"mcp-1": "mcp__basecamp__get_dispatch", "exec-1": "", "exec-2": "", "exec-3": "", "toolu_2": "Bash", "spaced-1": "",
		"toolu_1": "mcp__basecamp__note", "toolu_3": "mcp__basecamp__note", "mcp-9": "",
	}, tools)
	outcomes := h.record().Outcomes
	options := make([]string, 0, len(outcomes))
	for _, o := range outcomes {
		_, id := outcomeOf(t, o)
		options = append(options, id)
	}
	assert.Equal(t, []string{"allow-once", "reject", "reject", "reject", "reject", "reject", "allow-once", "allow-once", "reject", "reject"}, options)
	assert.Len(t, res.Refusals, 7)
}

func TestARequestOutsideATurnIsRefusedUnasked(t *testing.T) {
	h := newHarness(t)
	h.policy.allow = func(driver.PermissionRequest) bool { return true }
	s := h.open().(*session)
	// Feed the request straight in: no turn is in flight.
	params := raw(t, map[string]any{"sessionId": "sess-1", "toolCall": map[string]any{"toolCallId": "c", "kind": "edit"},
		"options": []any{map[string]any{"optionId": "ok", "kind": "allow_once"}, map[string]any{"optionId": "no", "kind": "reject_once"}}})
	s.onRequest(json.RawMessage(`99`), "session/request_permission", params, s.claim("session/request_permission"))
	assert.Empty(t, h.policy.requests())
}

// ---------------------------------------------------------------- invariant 4

func TestARefusalIsNeverReportedAsACancel(t *testing.T) {
	call := map[string]any{"toolCallId": "exec-1", "kind": "execute"}
	t.Run("codex ends a refused turn as canceled", func(t *testing.T) {
		h := newHarness(t)
		h.turns(turnScript{Steps: []step{{Permission: permission(t, call, standardOptions()...)}}, Stop: string(driver.TurnCanceled)})
		res, err := h.open().Prompt(context.Background(), "go")
		require.NoError(t, err)
		assert.Equal(t, driver.TurnRefusal, res.Stop)
		assert.Equal(t, []driver.Refusal{{ToolCallID: "exec-1", Tool: "execute"}}, res.Refusals)
	})
	t.Run("claude ends it as end_turn, with the refusal on record", func(t *testing.T) {
		h := newHarness(t)
		h.turns(turnScript{Steps: []step{{Permission: permission(t, call, standardOptions()...)}}, Stop: "end_turn"})
		res, err := h.open().Prompt(context.Background(), "go")
		require.NoError(t, err)
		assert.Equal(t, driver.TurnEndTurn, res.Stop)
		assert.Len(t, res.Refusals, 1)
	})
	t.Run("a canceled stop nobody asked for is an error", func(t *testing.T) {
		h := newHarness(t)
		h.turns(turnScript{Stop: string(driver.TurnCanceled)})
		res, err := h.open().Prompt(context.Background(), "go")
		require.Error(t, err)
		assert.NotEqual(t, driver.TurnCanceled, res.Stop)
	})
	t.Run("a cancel the connector asked for is canceled", func(t *testing.T) {
		h := newHarness(t)
		h.turns(turnScript{Steps: []step{{Update: raw(t, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "hi"}})}},
			WaitForCancel: true, Stop: string(driver.TurnCanceled)})
		s := h.open()
		answers := make(chan driver.PromptResult, 1)
		go func() {
			res, err := s.Prompt(context.Background(), "go")
			assert.NoError(t, err)
			answers <- res
		}()
		<-s.Updates()
		require.NoError(t, s.Cancel(context.Background()))
		select {
		case res := <-answers:
			assert.Equal(t, driver.TurnCanceled, res.Stop)
		case <-time.After(5 * time.Second):
			t.Fatal("no answer after cancel")
		}
	})
	t.Run("an unknown stop reason is an error", func(t *testing.T) {
		h := newHarness(t)
		h.turns(turnScript{Stop: "gave_up"})
		_, err := h.open().Prompt(context.Background(), "go")
		require.Error(t, err)
	})
}

func TestACancelWithNoTurnEndsTheNextOneAndOnlyIt(t *testing.T) {
	h := newHarness(t)
	h.turns(turnScript{WaitForCancel: true, Stop: string(driver.TurnCanceled)}, turnScript{Stop: "end_turn"})
	s := h.open()
	require.NoError(t, s.Cancel(context.Background()))
	assert.NotContains(t, h.record().Methods, "session/cancel", "nothing is sent for a turn that is not there")

	res, err := s.Prompt(context.Background(), "go")
	require.NoError(t, err)
	assert.Equal(t, driver.TurnCanceled, res.Stop, "the turn the cancel raced starts canceled")
	assert.Contains(t, h.record().Methods, "session/cancel")

	res, err = s.Prompt(context.Background(), "follow-up")
	require.NoError(t, err)
	assert.Equal(t, driver.TurnEndTurn, res.Stop, "a cancel ends one turn, not the session's every turn after it")
	n := 0
	for _, m := range h.record().Methods {
		if m == "session/cancel" {
			n++
		}
	}
	assert.Equal(t, 1, n, "one cancel, for one turn")
}

func TestAPermissionIsNotAllowedOnceTheTurnIsCanceled(t *testing.T) {
	h := newHarness(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.policy.allow = func(driver.PermissionRequest) bool {
		once.Do(func() { close(started) })
		<-release
		return true
	}
	h.turns(turnScript{Steps: []step{{Permission: permission(t, map[string]any{"toolCallId": "c1", "kind": "edit"}, standardOptions()...)}},
		WaitForCancel: true, Stop: string(driver.TurnCanceled)}, turnScript{Stop: "end_turn"})
	s := h.open()
	answers := make(chan driver.PromptResult, 1)
	go func() {
		res, err := s.Prompt(context.Background(), "go")
		assert.NoError(t, err)
		answers <- res
	}()
	<-started
	require.NoError(t, s.Cancel(context.Background()))
	close(release)
	select {
	case res := <-answers:
		assert.Equal(t, driver.TurnCanceled, res.Stop)
		assert.Len(t, res.Refusals, 1, "a permission the policy allowed while the turn was canceled is refused")
	case <-time.After(10 * time.Second):
		t.Fatal("the canceled turn never ended")
	}
	_, option := outcomeOf(t, h.record().Outcomes[0])
	assert.Equal(t, "reject", option)

	// The cancel ended the turn it found; the next one is not born canceled.
	res, err := s.Prompt(context.Background(), "follow-up")
	require.NoError(t, err)
	assert.Equal(t, driver.TurnEndTurn, res.Stop)
	n := 0
	for _, m := range h.record().Methods {
		if m == "session/cancel" {
			n++
		}
	}
	assert.Equal(t, 1, n)
}

// ---------------------------------------------------------------- invariant 5

func TestLoadIsGatedByWhatTheAgentAdvertises(t *testing.T) {
	replay := []json.RawMessage{
		raw(t, map[string]any{"sessionUpdate": "user_message_chunk", "content": map[string]any{"type": "text", "text": "old"}}),
		raw(t, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "old answer"}}),
		raw(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t0", "kind": "read"}),
	}
	for _, tc := range []struct {
		name         string
		load, resume bool
		method       string
	}{
		{"loadSession", true, false, "session/load"},
		{"resume only", false, true, "session/resume"},
		{"both prefers load", true, true, "session/load"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.sc.LoadSession, h.sc.Resume, h.sc.Replay = tc.load, tc.resume, replay
			h.sc.SessionID = "sess-earlier"
			d := h.driver()
			s, err := d.LoadSession(context.Background(), h.config(), "sess-earlier")
			require.NoError(t, err)
			defer s.Close()
			assert.Equal(t, "sess-earlier", s.ID())
			rec := h.record()
			assert.Contains(t, rec.Methods, tc.method)
			assert.NotContains(t, rec.Methods, "session/new")
			assert.True(t, d.Capabilities().LoadSession, "a session this driver can reload, by load or resume")
			select {
			case u := <-s.Updates():
				t.Fatalf("a load's replay was reported as progress: %+v", u)
			default:
			}
			assert.Contains(t, rec.Methods, "session/set_config_option", "a loaded session is put in its asking mode too")
		})
	}
	t.Run("neither", func(t *testing.T) {
		h := newHarness(t)
		h.sc.LoadSession, h.sc.Resume = false, false
		d := h.driver()
		_, err := d.LoadSession(context.Background(), h.config(), "sess-earlier")
		require.ErrorIs(t, err, ErrLoadUnsupported)
		assert.False(t, d.Capabilities().LoadSession)
		assert.NotErrorIs(t, err, driver.ErrNotStarted)
		waitGone(t, h.record().PID)
	})
	t.Run("a session id the ledger could not have written starts nothing", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.driver().LoadSession(context.Background(), h.config(), "../../etc; rm")
		require.ErrorIs(t, err, driver.ErrNotStarted)
	})
}

// ---------------------------------------------------------------- invariant 6 and driver invariant 4

func TestOnlyAStartThatRanNothingIsErrNotStarted(t *testing.T) {
	t.Run("missing binary", func(t *testing.T) {
		h := newHarness(t)
		d := h.driver()
		d.opts.Binary = filepath.Join(h.dir, "no-such-adapter")
		_, err := d.NewSession(context.Background(), h.config())
		require.ErrorIs(t, err, driver.ErrNotStarted)
	})
	for name, mutate := range map[string]func(*scenario){
		"initialize fails":        func(sc *scenario) { sc.FailInitialize = true },
		"another adapter":         func(sc *scenario) { sc.AgentName = "@someone/else" },
		"another adapter version": func(sc *scenario) { sc.AgentVersion = "9.9.10" },
		"another protocol":        func(sc *scenario) { sc.ProtocolVersion = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			mutate(&h.sc)
			_, err := h.driver().NewSession(context.Background(), h.config())
			require.Error(t, err)
			assert.NotErrorIs(t, err, driver.ErrNotStarted)
			assert.Equal(t, h.record().PID, driver.StartedProcess(err).PID, "a start that launched a process says which")
			assert.NotContains(t, h.record().Methods, "session/new")
			waitGone(t, h.record().PID)
		})
	}
	t.Run("a handshake that never answers", func(t *testing.T) {
		h := newHarness(t)
		h.sc.Hang = "session/new"
		d := h.driver()
		d.opts.HandshakeTimeout = 3 * time.Second
		_, err := d.NewSession(context.Background(), h.config())
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.NotErrorIs(t, err, driver.ErrNotStarted)
		waitGone(t, h.record().PID)
	})
}

// ---------------------------------------------------------------- driver invariant 5

func TestCloseEndsTheWholeProcessGroup(t *testing.T) {
	h := newHarness(t)
	h.sc.SpawnChild, h.sc.IgnoreStdinEOF, h.sc.IgnoreTerminate = true, true, true
	h.grace = 200 * time.Millisecond
	s := h.open()
	rec := h.record()
	require.NotZero(t, rec.ChildPID)
	assert.Equal(t, rec.PID, s.Process().PGID)

	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		_ = syscall.Kill(-rec.PID, syscall.SIGKILL)
		t.Fatal("Close did not end an adapter that ignores EOF and SIGTERM")
	}
	require.NoError(t, s.Close(), "Close is idempotent")
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the adapter outlived Close")
	}
	waitGone(t, rec.PID)
	waitGone(t, rec.ChildPID)
	_, err := s.Prompt(context.Background(), "go")
	require.ErrorIs(t, err, driver.ErrSessionEnded)
}

func TestAWorkerThatDiesMidTurnEndsThePrompt(t *testing.T) {
	h := newHarness(t)
	h.turns(turnScript{Hang: true})
	s := h.open()
	answers := make(chan error, 1)
	go func() {
		_, err := s.Prompt(context.Background(), "go")
		answers <- err
	}()
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, syscall.Kill(s.Process().PID, syscall.SIGKILL))
	select {
	case err := <-answers:
		require.ErrorIs(t, err, driver.ErrSessionEnded)
	case <-time.After(5 * time.Second):
		t.Fatal("Prompt did not return when the worker died")
	}
}

// ---------------------------------------------------------------- invariant 7

func TestNothingTheAgentVolunteersIsKept(t *testing.T) {
	h := newHarness(t)
	h.sc.AuthEmail = "person@example.com"
	h.turns(
		turnScript{Steps: []step{
			{Update: raw(t, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "secret words the connector never keeps"}})},
			{Update: raw(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "cat /home/person/.ssh/id_rsa", "kind": "read",
				"status": "pending", "rawInput": map[string]any{"path": "/home/person/.ssh/id_rsa"}, "name": "Read person@example.com"})},
			{Update: raw(t, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t2", "title": "cat /home/person/.ssh/id_rsa", "kind": "read"})},
			{Update: raw(t, map[string]any{"sessionUpdate": "usage_update", "used": 1200, "size": 200000})},
			{Update: raw(t, map[string]any{"sessionUpdate": "plan", "entries": []any{map[string]any{"content": "step one"}}})},
		}, Stop: "end_turn", Usage: raw(t, map[string]any{"inputTokens": 12, "outputTokens": 34})},
		turnScript{ErrorMessage: "quota exhausted for person@example.com"},
	)
	s := h.open()
	res, err := s.Prompt(context.Background(), "go")
	require.NoError(t, err)
	assert.Equal(t, driver.Usage{InputTokens: 12, OutputTokens: 34, ContextUsed: 1200, ContextSize: 200000}, res.Usage)

	var updates []driver.Update
	for len(updates) < 6 {
		select {
		case u := <-s.Updates():
			updates = append(updates, u)
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d updates", len(updates))
		}
	}
	kinds := make([]driver.UpdateKind, 0, len(updates))
	for _, u := range updates {
		kinds = append(kinds, u.Kind)
		assert.NotContains(t, u.Tool, "@")
		assert.NotContains(t, u.Tool, "ssh")
	}
	assert.Equal(t, []driver.UpdateKind{driver.UpdateAgentMessageChunk, driver.UpdateToolCall, driver.UpdateToolCallUpdate, driver.UpdateUsage, driver.UpdatePlan, driver.UpdateUsage}, kinds)
	assert.Empty(t, updates[2].Tool, "a title is never a tool's name")
	assert.Equal(t, len("secret words the connector never keeps"), updates[0].Chars)
	assert.Equal(t, driver.ToolRead, updates[1].ToolKind)
	assert.Equal(t, driver.ToolPending, updates[1].Status)

	_, err = s.Prompt(context.Background(), "again")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "person@example.com")
	assert.Contains(t, err.Error(), "quota exhausted")

	h2 := newHarness(t)
	h2.sc.AuthEmail, h2.sc.FailInitialize = "person@example.com", true
	_, err = h2.driver().NewSession(context.Background(), h2.config())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "person@example.com")
}

// ---------------------------------------------------------------- turns

func TestAPromptWhoseContextEndsLeavesTheTurnToFinish(t *testing.T) {
	h := newHarness(t)
	h.turns(turnScript{Steps: []step{{SleepMS: 400}}, Stop: "end_turn"}, turnScript{Stop: "end_turn"})
	s := h.open()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := s.Prompt(ctx, "slow")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = s.Prompt(context.Background(), "overlapping")
	require.Error(t, err, "the first turn is still in flight")
	require.Eventually(t, func() bool {
		_, err := s.Prompt(context.Background(), "next")
		return err == nil
	}, 5*time.Second, 50*time.Millisecond)
}

func TestFollowUpsArePromptsInTheSameSession(t *testing.T) {
	h := newHarness(t)
	d := h.driver()
	caps := d.Capabilities()
	assert.True(t, caps.FollowUpPrompts)
	assert.True(t, caps.PermissionCallback)
	s, err := d.NewSession(context.Background(), h.config())
	require.NoError(t, err)
	defer s.Close()
	for range 3 {
		res, err := s.Prompt(context.Background(), "next")
		require.NoError(t, err)
		assert.Equal(t, driver.TurnEndTurn, res.Stop)
	}
	methods := h.record().Methods
	n := 0
	for _, m := range methods {
		if m == "session/prompt" {
			n++
		}
	}
	assert.Equal(t, 3, n)
	assert.Equal(t, Name, d.Name())
}

// ---------------------------------------------------------------- adapters

func TestLocateFindsOnlyThePinnedVersion(t *testing.T) {
	dir := t.TempDir()
	a := Adapter{Name: "fake-acp", Package: "@example/fake-acp", Version: "1.2.3"}
	_, err := Locate(dir, a)
	require.ErrorIs(t, err, ErrAdapterMissing)
	_, err = Locate("relative/dir", a)
	require.Error(t, err)

	pkg := filepath.Join(dir, "node_modules", "@example", "fake-acp")
	require.NoError(t, os.MkdirAll(pkg, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@example/fake-acp","version":"1.2.4"}`), 0o600))
	_, err = Locate(dir, a)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pinned")

	require.NoError(t, os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@example/fake-acp","version":"1.2.3"}`), 0o600))
	_, err = Locate(dir, a)
	require.ErrorIs(t, err, ErrAdapterMissing, "no executable yet")
	bin := filepath.Join(dir, "node_modules", ".bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "fake-acp"), []byte("#!/bin/sh\n"), 0o700))
	got, err := Locate(dir, a)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(bin, "fake-acp"), got)
}

func TestThePinnedAdapters(t *testing.T) {
	for _, a := range Adapters() {
		got, ok := AdapterNamed(a.Name)
		require.True(t, ok)
		assert.Equal(t, a.Package, got.Package)
		assert.NotEmpty(t, a.Modes[driver.ModeEditsInWorkDir], a.Name)
		for _, name := range a.Env {
			assert.NotContains(t, []string{"CLAUDE_CODE_EXECUTABLE", "CODEX_PATH", "CLAUDE_CODE_MESSAGING_TOKEN", "BASECAMP_TOKEN"}, name,
				"%s may not take a variable that swaps its pinned agent or carries the host's token", a.Name)
		}
	}
	options := ClaudeAgentACP.SessionMeta["claudeCode"].(map[string]any)["options"].(map[string]any)
	assert.Equal(t, true, options["strictMcpConfig"], "only the session's MCP servers")
	assert.Equal(t, MCPStatusInit, ClaudeAgentACP.MCPStatus)
	assert.Equal(t, []map[string]string{{"type": "system", "subtype": "init"}}, ClaudeAgentACP.SessionMeta["claudeCode"].(map[string]any)["emitRawSDKMessages"],
		"the init, and only the init, is forwarded")
	assert.Equal(t, MCPStatusStartupFailures, CodexACP.MCPStatus)
	assert.Equal(t, []string{"EnterPlanMode", "ExitPlanMode"}, options["disallowedTools"], "a plan-mode switch would leave the verified mode")
	assert.Equal(t, []string{}, options["settingSources"], "none of the host's settings")
	assert.Equal(t, false, options["allowDangerouslySkipPermissions"])
	assert.Equal(t, "true", CodexACP.SetEnv["DISABLE_MCP_CONFIG_FILTERING"], "the requested server is never dropped for a configured one")
	assert.NotNil(t, CodexACP.Preflight)
	assert.Equal(t, "0.78.0", ClaudeAgentACP.Version)
	assert.Equal(t, "1.12.0", CodexACP.Version)

	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	data, err := os.ReadFile(filepath.Join("adapters", "package.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &manifest))
	for _, a := range Adapters() {
		assert.Equal(t, a.Version, manifest.Dependencies[a.Package], "adapters/package.json pins what the driver checks")
	}
	var codexCfg map[string]any
	require.NoError(t, json.Unmarshal([]byte(CodexACP.SetEnv["CODEX_CONFIG"]), &codexCfg), "CODEX_CONFIG is JSON")

	_, ok := AdapterNamed("nobody")
	assert.False(t, ok)
	dir, err := DefaultAdaptersDir(func(name string) (string, bool) {
		return map[string]string{"HOME": "/home/agent"}[name], name == "HOME"
	})
	require.NoError(t, err)
	assert.Equal(t, "/home/agent/.local/share/basecamp/acp-adapters", dir)
}

// ---------------------------------------------------------------- hangs

func TestAnAgentThatStopsReadingCannotHoldCancelOrClose(t *testing.T) {
	h := newHarness(t)
	h.sc.StopReadingAfter = "session/set_config_option"
	h.grace = 300 * time.Millisecond
	s := h.open()

	prompted := make(chan error, 1)
	go func() {
		// Larger than the pipe and the agent's read buffer: the write sticks.
		_, err := s.Prompt(context.Background(), strings.Repeat("x", 8<<20))
		prompted <- err
	}()
	time.Sleep(200 * time.Millisecond)

	canceled := make(chan error, 1)
	go func() { canceled <- s.Cancel(context.Background()) }()
	select {
	case err := <-canceled:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Cancel waited on a stuck write")
	}
	closed := make(chan struct{})
	go func() { _ = s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		_ = syscall.Kill(-s.Process().PGID, syscall.SIGKILL)
		t.Fatal("Close waited on a stuck write")
	}
	select {
	case err := <-prompted:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the stuck prompt never returned")
	}
}

func TestALineTooLongEndsTheWorker(t *testing.T) {
	old := maxLine
	maxLine = 1 << 20
	t.Cleanup(func() { maxLine = old })
	h := newHarness(t)
	h.turns(turnScript{Steps: []step{{Update: raw(t, map[string]any{"sessionUpdate": "agent_message_chunk",
		"content": map[string]any{"type": "text", "text": strings.Repeat("y", 2<<20)}})}}, Hang: true})
	s := h.open()
	_, err := s.Prompt(context.Background(), "go")
	require.ErrorIs(t, err, driver.ErrSessionEnded)
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the worker outlived its unreadable stream")
	}
}

func TestAModeChangeFailsTheTurnBeforeTheWorkerIsGone(t *testing.T) {
	h := newHarness(t)
	h.turns(turnScript{Steps: []step{{ModeChange: "bypassPermissions"}}, Hang: true})
	s := h.open().(*session)
	release := make(chan struct{})
	ended := make(chan struct{})
	s.mu.Lock()
	s.endUnsafe = func() {
		<-release
		s.worker.Terminate(0)
		close(ended)
	}
	s.mu.Unlock()
	answers := make(chan error, 1)
	go func() {
		_, err := s.Prompt(context.Background(), "go")
		answers <- err
	}()
	select {
	case err := <-answers:
		require.ErrorIs(t, err, driver.ErrUnsafeMode, "the turn fails on the mode report, not on the worker's end")
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the turn waited for the worker to be ended")
	}
	close(release)
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker was not ended")
	}
	<-s.Done()
}

// ---------------------------------------------------------------- foreign MCP configuration

func TestCodexConfigThatDeclaresMCPServersRefusesTheSession(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cwd := filepath.Join(root, "repo", "sub")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex"), 0o700))
	require.NoError(t, os.MkdirAll(cwd, 0o700))
	lookup := func(name string) (string, bool) {
		if name == "HOME" {
			return home, true
		}
		return "", false
	}
	require.NoError(t, os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \"x\"\n[projects.\"/tmp\"]\ntrust_level = \"trusted\"\n"), 0o600))
	require.NoError(t, codexPreflight(cwd, lookup))

	require.NoError(t, os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("[mcp_servers.basecamp]\ncommand = \"/bin/evil\"\n"), 0o600))
	require.ErrorIs(t, codexPreflight(cwd, lookup), ErrForeignMCPConfig)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("['mcp_servers'.basecamp]\ncommand = \"/bin/evil\"\n"), 0o600))
	require.ErrorIs(t, codexPreflight(cwd, lookup), ErrForeignMCPConfig, "a quoted key declares them too")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("[\"mcp\\u005fservers\".basecamp]\ncommand = \"/bin/evil\"\n"), 0o600))
	require.ErrorIs(t, codexPreflight(cwd, lookup), ErrForeignMCPConfig, "a key with an escape is refused rather than read")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("[profiles.\"my profile\".mcp_servers.x]\ncommand = \"/bin/evil\"\n"), 0o600))
	require.ErrorIs(t, codexPreflight(cwd, lookup), ErrForeignMCPConfig, "a quoted table path declares them too")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("[profiles . demo . mcp_servers . basecamp]\ncommand = \"/bin/evil\"\n"), 0o600))
	require.ErrorIs(t, codexPreflight(cwd, lookup), ErrForeignMCPConfig, "TOML allows space around the dots")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("\ufeff[mcp_servers.basecamp]\ncommand = \"/bin/evil\"\n"), 0o600))
	require.ErrorIs(t, codexPreflight(cwd, lookup), ErrForeignMCPConfig, "a byte order mark does not hide the first line")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \"x\"\nwindows_path = \"C:\\\\codex\"\n"), 0o600))
	require.NoError(t, codexPreflight(cwd, lookup), "an escape in a value is not a key")
	codexHome := filepath.Join(root, "codex-home")
	require.NoError(t, os.MkdirAll(codexHome, 0o700))
	withCodexHome := func(name string) (string, bool) {
		if name == "CODEX_HOME" {
			return codexHome, true
		}
		return lookup(name)
	}
	require.NoError(t, codexPreflight(cwd, withCodexHome), "CODEX_HOME replaces ~/.codex")

	require.NoError(t, os.MkdirAll(filepath.Join(root, "repo", ".codex"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "repo", ".codex", "config.toml"), []byte("mcp_servers.basecamp.command = \"/bin/evil\"\n"), 0o600))
	require.ErrorIs(t, codexPreflight(cwd, withCodexHome), ErrForeignMCPConfig, "a project layer above the working directory counts")

	h := newHarness(t)
	d := h.driver()
	d.opts.Adapter.Preflight = func(string, func(string) (string, bool)) error { return ErrForeignMCPConfig }
	_, err := d.NewSession(context.Background(), h.config())
	require.ErrorIs(t, err, ErrForeignMCPConfig)
	require.ErrorIs(t, err, driver.ErrNotStarted)
	require.ErrorIs(t, err, driver.ErrUnusable)
	_, statErr := os.Stat(h.sc.Record)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "nothing was started")
}

func TestCloseGivesUpOnOutputAnEscapedDescendantHolds(t *testing.T) {
	h := newHarness(t)
	h.sc.EscapingChild, h.sc.IgnoreStdinEOF, h.sc.IgnoreTerminate = true, true, true
	h.grace = 300 * time.Millisecond
	s := h.open()
	rec := h.record()
	require.NotZero(t, rec.ChildPID)
	t.Cleanup(func() { _ = syscall.Kill(rec.ChildPID, syscall.SIGKILL) })

	closed := make(chan struct{})
	go func() { _ = s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close waited on output a process outside the worker's group holds")
	}
	waitGone(t, rec.PID)
	assert.False(t, gone(rec.ChildPID), "the escaped descendant is not this driver's to kill by name")
}

func TestAgentTextIsFitForALog(t *testing.T) {
	h := newHarness(t)
	h.turns(turnScript{ErrorMessage: "quota for person@example.com\u001b[31mred\u009b31mred\nsecond line\ttab"})
	s := h.open()
	_, err := s.Prompt(context.Background(), "go")
	require.Error(t, err)
	for _, bad := range []string{"person@example.com", "\u001b", "\u009b", "\n", "\t"} {
		assert.NotContains(t, err.Error(), bad)
	}
	assert.Contains(t, err.Error(), "quota for")
}

func TestAFloodOfPermissionRequestsIsBounded(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	var deciding atomic.Int32
	h.policy.allow = func(driver.PermissionRequest) bool {
		deciding.Add(1)
		defer deciding.Add(-1)
		<-release
		return true
	}
	const flood = 60
	h.turns(turnScript{
		FloodPermissions: flood,
		FloodCall:        permission(t, map[string]any{"kind": "edit"}, standardOptions()...),
		Stop:             "end_turn",
	})
	s := h.open()
	answers := make(chan driver.PromptResult, 1)
	go func() {
		res, err := s.Prompt(context.Background(), "go")
		assert.NoError(t, err)
		answers <- res
	}()
	require.Eventually(t, func() bool { return deciding.Load() == maxDecisions }, 20*time.Second, 10*time.Millisecond,
		"the session decides at most %d at once", maxDecisions)
	// Every request but the ones stuck in a decision has been answered.
	require.Eventually(t, func() bool { return len(h.record().Outcomes) >= flood-maxDecisions }, 30*time.Second, 20*time.Millisecond,
		"a flood is answered as it arrives")
	assert.LessOrEqual(t, deciding.Load(), int32(maxDecisions))
	answered := h.record().Outcomes
	close(release)
	var res driver.PromptResult
	select {
	case res = <-answers:
	case <-time.After(20 * time.Second):
		t.Fatal("the flooded turn never ended")
	}
	assert.NotEmpty(t, res.Refusals, "a request refused for want of room is still a refusal on the turn")
	canceled := 0
	for _, o := range answered {
		if len(o) == 0 || string(o) == "null" {
			continue
		}
		if outcome, _ := outcomeOf(t, o); outcome == outcomeCanceled {
			canceled++
		}
	}
	assert.Positive(t, canceled, "what reaches the policy past its bound is refused undecided")
	allowed := 0
	for _, o := range h.record().Outcomes {
		if len(o) == 0 || string(o) == "null" {
			continue
		}
		if _, option := outcomeOf(t, o); option == "allow-once" {
			allowed++
		}
	}
	assert.Positive(t, allowed, "while what fits is still decided")
}

// The connection answers at most maxHandlers requests at once, whatever the
// agent sends: the rest are refused as they are read, so no flood of requests
// becomes a flood of goroutines.
func TestTheConnectionBoundsRequestsInFlight(t *testing.T) {
	// What the client writes, the test reads; what the test writes, the
	// client reads.
	fromClient, toAgent := io.Pipe()
	toClient, fromAgent := io.Pipe()
	t.Cleanup(func() { _ = toAgent.Close(); _ = fromAgent.Close() })

	c := newConn(toAgent)
	var busy atomic.Int32
	c.onBusy = func(string, json.RawMessage) { busy.Add(1) }
	release := make(chan struct{})
	var inFlight, peak atomic.Int32
	c.onRequest = func(id json.RawMessage, _ string, _ json.RawMessage, _ any) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		c.reply(id, map[string]any{"outcome": map[string]any{"outcome": outcomeCanceled}})
	}
	go func() { _ = c.read(toClient) }()

	answers := make(chan int, 1)
	go func() {
		// Read what the client writes, so no reply of its own can block it.
		refused := 0
		scanner := bufio.NewScanner(fromClient)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "too many requests") {
				refused++
			}
			if strings.Contains(scanner.Text(), "outcome") {
				break
			}
		}
		answers <- refused
	}()
	for i := range 64 {
		_, err := fmt.Fprintf(fromAgent, `{"jsonrpc":"2.0","id":%d,"method":"session/request_permission","params":{}}`+"\n", i)
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool { return int(inFlight.Load()) == maxHandlers }, 10*time.Second, 5*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(maxHandlers), peak.Load(), "no more goroutines than the bound, whatever arrives")
	close(release)
	select {
	case refused := <-answers:
		assert.Positive(t, refused, "what does not fit is refused as it is read")
		assert.GreaterOrEqual(t, int(busy.Load()), refused, "and every one of those refusals is heard by the session")
	case <-time.After(10 * time.Second):
		t.Fatal("no answer reached the agent")
	}
}

func TestWhatOneToolCallMayCostTheSession(t *testing.T) {
	h := newHarness(t)
	s := h.open().(*session)
	long := strings.Repeat("c", maxToolCallID+1)
	locations := make([]string, maxLocations*4)
	for i := range locations {
		locations[i] = fmt.Sprintf("/work/%d", i)
	}
	info := s.noteTool(sessionUpdate{ToolCallID: long, Kind: "edit", Status: "pending", Locations: locations})
	assert.Len(t, info.locations, maxLocations, "a call names as many paths as the policy will look at, no more")
	s.mu.Lock()
	remembered := len(s.tools)
	s.mu.Unlock()
	assert.Zero(t, remembered, "an id past what an id can be is not a key to keep")

	for i := range maxTools + 10 {
		s.noteTool(sessionUpdate{ToolCallID: fmt.Sprintf("call-%d", i), Kind: "edit", Status: "pending"})
	}
	s.mu.Lock()
	remembered = len(s.tools)
	s.mu.Unlock()
	assert.Equal(t, maxTools, remembered)
}

// A permission being decided as the turn ends is still on the turn's result:
// the agent can answer the prompt before it hears the answer to its request.
func TestARefusalDecidedAsTheTurnEndsIsOnItsResult(t *testing.T) {
	h := newHarness(t)
	deciding := make(chan struct{})
	h.policy.allow = func(driver.PermissionRequest) bool {
		close(deciding)
		time.Sleep(300 * time.Millisecond)
		return false
	}
	h.turns(turnScript{
		FloodPermissions:   1,
		FloodCall:          permission(t, map[string]any{"kind": "edit"}, standardOptions()...),
		StopWithoutWaiting: true,
		Stop:               "end_turn",
	})
	s := h.open()
	res, err := s.Prompt(context.Background(), "go")
	require.NoError(t, err)
	select {
	case <-deciding:
	default:
		t.Fatal("the policy was never asked")
	}
	assert.Len(t, res.Refusals, 1)
}

// A handshake that fails after the adapter started leaves nothing of its
// process group behind by the time NewSession returns: the caller settles the
// attempt on that error.
func TestAFailedHandshakeLeavesNoGroupBehind(t *testing.T) {
	// Several runs: the window this closes is a matter of milliseconds.
	for run := range 4 {
		h := newHarness(t)
		h.sc.SpawnChild, h.sc.IgnoreTerminate = true, true
		// Past initialize, so the agent has surely started and said so.
		h.sc.Hang = "session/new"
		d := h.driver()
		d.opts.HandshakeTimeout = 3 * time.Second
		d.opts.CloseGrace = 2 * time.Second
		_, err := d.NewSession(context.Background(), h.config())
		require.Error(t, err)
		rec := h.record()
		require.NotZero(t, rec.ChildPID)
		assert.True(t, gone(rec.ChildPID) && gone(rec.PID),
			"run %d: the adapter's group is gone when NewSession returns, not a moment later", run)
	}
}

func TestARefusalRecordIsBounded(t *testing.T) {
	h := newHarness(t)
	s := h.open().(*session)
	tr := &turn{done: make(chan struct{})}
	s.mu.Lock()
	s.turn = tr
	s.mu.Unlock()
	for range maxRefusals + 50 {
		s.record(driver.PermissionRequest{ToolCallID: strings.Repeat("x", 4*maxToolCallID), Kind: driver.ToolEdit}, tr)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Len(t, tr.refusals, maxRefusals)
	assert.LessOrEqual(t, len(tr.refusals[0].ToolCallID), maxToolCallID, "a recorded id is cut, and then redacted")
	s.turn = nil
}

// A cancel that arrives once the agent has answered the prompt, while the
// session still waits on a decision, is not sent: that turn is over.
func TestACancelAfterTheAgentAnsweredIsNotSent(t *testing.T) {
	h := newHarness(t)
	deciding := make(chan struct{})
	h.policy.allow = func(driver.PermissionRequest) bool {
		close(deciding)
		time.Sleep(600 * time.Millisecond)
		return false
	}
	h.turns(turnScript{
		FloodPermissions:   1,
		FloodCall:          permission(t, map[string]any{"kind": "edit"}, standardOptions()...),
		StopWithoutWaiting: true,
		Stop:               "end_turn",
	})
	s := h.open()
	answers := make(chan driver.PromptResult, 1)
	go func() {
		res, err := s.Prompt(context.Background(), "go")
		assert.NoError(t, err)
		answers <- res
	}()
	<-deciding
	// The agent answers the prompt 150ms after asking; the decision takes 600.
	time.Sleep(350 * time.Millisecond)
	require.NoError(t, s.Cancel(context.Background()))
	res := <-answers
	assert.Equal(t, driver.TurnEndTurn, res.Stop)
	assert.NotContains(t, h.record().Methods, "session/cancel")
}

// A turn the agent has answered asks nothing more: a request that arrives
// while the session waits on a decision still in flight is refused, not put
// to the policy.
func TestARequestAfterTheAgentAnsweredIsNotAllowed(t *testing.T) {
	h := newHarness(t)
	var calls atomic.Int32
	h.policy.allow = func(req driver.PermissionRequest) bool {
		if calls.Add(1) == 1 {
			time.Sleep(800 * time.Millisecond)
		}
		return true
	}
	h.turns(turnScript{
		FloodPermissions:   1,
		FloodCall:          permission(t, map[string]any{"kind": "edit", "locations": []any{map[string]any{"path": "x"}}}, standardOptions()...),
		StopWithoutWaiting: true,
		Stop:               "end_turn",
		LateRequest:        permission(t, map[string]any{"toolCallId": "late", "kind": "edit"}, standardOptions()...),
	})
	s := h.open()
	_, err := s.Prompt(context.Background(), "go")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(h.record().Outcomes) == 2 }, 10*time.Second, 20*time.Millisecond)
	for _, r := range h.policy.requests() {
		assert.NotEqual(t, "late", r.ToolCallID, "a request after the answer is not put to the policy")
	}
	late := h.record().Outcomes
	_, lastOption := outcomeOf(t, late[len(late)-1])
	assert.NotEqual(t, "allow-once", lastOption)
}

// A cancel that arrives after the agent has answered does not turn the
// agent's own stop into one the connector asked for.
func TestACancelAfterTheAnswerDoesNotClaimTheStop(t *testing.T) {
	h := newHarness(t)
	h.policy.allow = func(driver.PermissionRequest) bool {
		time.Sleep(700 * time.Millisecond)
		return true
	}
	h.turns(turnScript{
		FloodPermissions:   1,
		FloodCall:          permission(t, map[string]any{"kind": "edit", "locations": []any{map[string]any{"path": "x"}}}, standardOptions()...),
		StopWithoutWaiting: true,
		Stop:               string(driver.TurnCanceled),
	})
	s := h.open()
	type answer struct {
		res driver.PromptResult
		err error
	}
	answers := make(chan answer, 1)
	go func() {
		res, err := s.Prompt(context.Background(), "go")
		answers <- answer{res, err}
	}()
	// The agent answers 150ms in; the decision runs to 700ms.
	time.Sleep(400 * time.Millisecond)
	require.NoError(t, s.Cancel(context.Background()))
	a := <-answers
	assert.NotEqual(t, driver.TurnCanceled, a.res.Stop, "the connector's cancel came after the agent had stopped")
	// The decision still in flight came back allowed after the agent had
	// answered, so it was refused; the agent's own canceled stop is that refusal.
	require.NoError(t, a.err)
	assert.Equal(t, driver.TurnRefusal, a.res.Stop)
	assert.NotContains(t, h.record().Methods, "session/cancel")
}

func TestTheTurnEndWaitsForRequestsAlreadyRead(t *testing.T) {
	h := newHarness(t)
	s := h.open().(*session)
	claimed := s.claim("session/request_permission")
	assert.Nil(t, claimed, "no turn in flight")
	go func() {
		time.Sleep(300 * time.Millisecond)
		s.mu.Lock()
		s.deciding--
		s.mu.Unlock()
	}()
	start := time.Now()
	s.drainDecisions()
	assert.GreaterOrEqual(t, time.Since(start), 250*time.Millisecond, "a request read but not yet decided holds the turn's end")
}

func TestUpdatesCarryBoundedIDs(t *testing.T) {
	h := newHarness(t)
	s := h.open().(*session)
	s.emit(driver.Update{Kind: driver.UpdateToolCall, ToolCallID: strings.Repeat("i", 10*maxToolCallID)})
	select {
	case u := <-s.Updates():
		assert.Len(t, u.ToolCallID, maxToolCallID)
	case <-time.After(2 * time.Second):
		t.Fatal("no update")
	}
}

// The driver asks for the group's confirmation with the worker it started,
// and an answer that the group outlived its leader is in the error the caller
// settles on.
func TestAFailedHandshakeAsksForTheGroupsConfirmation(t *testing.T) {
	h := newHarness(t)
	h.sc.FailInitialize = true
	var asked []driver.Process
	old := confirmGroupGone
	confirmGroupGone = func(p driver.Process, grace time.Duration) error {
		asked = append(asked, p)
		return driver.ErrGroupOutlivedLeader
	}
	t.Cleanup(func() { confirmGroupGone = old })
	_, err := h.driver().NewSession(context.Background(), h.config())
	require.ErrorIs(t, err, driver.ErrGroupOutlivedLeader)
	require.Len(t, asked, 1)
	assert.Equal(t, h.record().PID, asked[0].PGID, "the group of the adapter this session started")
}

// The prompt's answer settles its turn as it is read, on the reading
// goroutine, so a request read right after it is outside the turn whatever
// the turn's own goroutine has done yet.
func TestAnAnswerSettlesItsTurnAsItIsRead(t *testing.T) {
	h := newHarness(t)
	h.policy.allow = func(driver.PermissionRequest) bool { return true }
	s := h.open().(*session)
	tr := &turn{done: make(chan struct{}), call: s.conn.register("session/prompt")}
	s.mu.Lock()
	s.turn = tr
	s.mu.Unlock()
	t.Cleanup(func() {
		s.mu.Lock()
		s.turn = nil
		s.mu.Unlock()
	})

	s.onResponse(tr.call.id)
	params := raw(t, map[string]any{"sessionId": "sess-1", "toolCall": map[string]any{"toolCallId": "after", "kind": "edit"},
		"options": []any{map[string]any{"optionId": "ok", "kind": "allow_once"}, map[string]any{"optionId": "no", "kind": "reject_once"}}})
	s.onRequest(json.RawMessage(`98`), "session/request_permission", params, s.claim("session/request_permission"))
	assert.Empty(t, h.policy.requests(), "a request read after the answer is not put to the policy")
}

// The install fails on a Node version an adapter does not support, rather
// than leaving an installation Locate accepts and the first dispatch cannot
// run: npm only warns about engines without --engine-strict.
func TestTheAdapterInstallRefusesAnUnsupportedNode(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "Makefile"))
	require.NoError(t, err)
	var install string
	for _, line := range strings.Split(string(makefile), "\n") {
		if strings.Contains(line, "npm ci") && strings.Contains(line, "ACP_ADAPTERS_DIR") {
			install = line
		}
	}
	require.NotEmpty(t, install, "make acp-adapters installs with npm ci")
	assert.Contains(t, install, "--engine-strict")
	assert.Contains(t, install, "--ignore-scripts")

	var lock struct {
		Packages map[string]struct {
			Engines map[string]string `json:"engines"`
		} `json:"packages"`
	}
	raw, err := os.ReadFile(filepath.Join("adapters", "package-lock.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &lock))
	assert.NotEmpty(t, lock.Packages["node_modules/"+ClaudeAgentACP.Package].Engines["node"],
		"the pinned adapter states the Node it needs, which --engine-strict enforces")
}

// A session whose MCP server did not connect does not go on: the worker
// would run without the Basecamp tools and its task token, and a turn that
// ends without them would be settled as finished.
func TestASessionWhoseMCPServerDidNotConnectDoesNotGoOn(t *testing.T) {
	withStatus := func(h *harness, status MCPStatus) *Driver {
		d := h.driver()
		d.opts.Adapter.MCPStatus = status
		return d
	}
	t.Run("claude: the init reports every server connected", func(t *testing.T) {
		h := newHarness(t)
		h.turns(turnScript{Steps: []step{{MCPInit: map[string]string{"basecamp": "connected"}}}, Stop: "end_turn"}, turnScript{Stop: "end_turn"})
		s, err := withStatus(h, MCPStatusInit).NewSession(context.Background(), h.config())
		require.NoError(t, err)
		defer s.Close()
		for range 2 {
			res, err := s.Prompt(context.Background(), "go")
			require.NoError(t, err)
			assert.Equal(t, driver.TurnEndTurn, res.Stop)
		}
	})
	for name, init := range map[string]map[string]string{
		"claude: the server failed":     {"basecamp": "failed"},
		"claude: the server is pending": {"basecamp": "pending"},
		"claude: the server is missing": {"other": "connected"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.turns(turnScript{Steps: []step{{MCPInit: init}, {SleepMS: 3000}}, Stop: "end_turn"})
			s, err := withStatus(h, MCPStatusInit).NewSession(context.Background(), h.config())
			require.NoError(t, err)
			defer s.Close()
			_, err = s.Prompt(context.Background(), "go")
			require.ErrorIs(t, err, ErrMCPServerNotConnected)
			select {
			case <-s.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("the worker was not ended")
			}
		})
	}
	t.Run("claude: a turn that ends with no init at all", func(t *testing.T) {
		h := newHarness(t)
		h.turns(turnScript{Stop: "end_turn"})
		s, err := withStatus(h, MCPStatusInit).NewSession(context.Background(), h.config())
		require.NoError(t, err)
		defer s.Close()
		_, err = s.Prompt(context.Background(), "go")
		require.ErrorIs(t, err, ErrMCPServerNotConnected, "never told is not connected")
	})
	t.Run("codex: a startup failure", func(t *testing.T) {
		h := newHarness(t)
		h.turns(turnScript{Steps: []step{
			{Update: raw(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "mcp_startup.basecamp", "kind": "other",
				"title": "mcp__basecamp__startup", "status": "failed"})},
			{SleepMS: 3000},
		}, Stop: "end_turn"})
		s, err := withStatus(h, MCPStatusStartupFailures).NewSession(context.Background(), h.config())
		require.NoError(t, err)
		defer s.Close()
		_, err = s.Prompt(context.Background(), "go")
		require.ErrorIs(t, err, ErrMCPServerNotConnected)
	})
	t.Run("codex: no failure reported is no failure", func(t *testing.T) {
		h := newHarness(t)
		h.turns(turnScript{Stop: "end_turn"})
		s, err := withStatus(h, MCPStatusStartupFailures).NewSession(context.Background(), h.config())
		require.NoError(t, err)
		defer s.Close()
		_, err = s.Prompt(context.Background(), "go")
		require.NoError(t, err)
	})
}

// An agent that asks faster than its refusals can be written has stopped
// working with this client: the connection says so, and the session ends
// rather than leaving requests unanswered for ever.
func TestAnAgentThatOutrunsEvenItsRefusalsEndsTheSession(t *testing.T) {
	t.Run("the connection reports the overflow", func(t *testing.T) {
		oldBusy, oldHandlers := maxBusy, maxHandlers
		maxBusy, maxHandlers = 2, 2
		t.Cleanup(func() { maxBusy, maxHandlers = oldBusy, oldHandlers })

		// A writer nobody reads: refusals queue up rather than going out.
		_, toAgent := io.Pipe()
		toClient, fromAgent := io.Pipe()
		t.Cleanup(func() { _ = toAgent.Close(); _ = fromAgent.Close() })
		c := newConn(toAgent)
		release := make(chan struct{})
		defer close(release)
		c.onRequest = func(json.RawMessage, string, json.RawMessage, any) { <-release }
		overflowed := make(chan struct{})
		var once sync.Once
		c.onOverflow = func() { once.Do(func() { close(overflowed) }) }
		go func() { _ = c.read(toClient) }()

		go func() {
			for i := range 64 {
				if _, err := fmt.Fprintf(fromAgent, `{"jsonrpc":"2.0","id":%d,"method":"session/request_permission","params":{}}`+"\n", i); err != nil {
					return
				}
			}
		}()
		select {
		case <-overflowed:
		case <-time.After(20 * time.Second):
			t.Fatal("an agent outrunning every bound was never reported")
		}
	})

	t.Run("the session ends", func(t *testing.T) {
		h := newHarness(t)
		h.turns(turnScript{Hang: true})
		s := h.open()
		answers := make(chan error, 1)
		go func() {
			_, err := s.Prompt(context.Background(), "go")
			answers <- err
		}()
		require.Eventually(t, func() bool { return slices.Contains(h.record().Methods, "session/prompt") },
			10*time.Second, 50*time.Millisecond)
		s.(*session).conn.onOverflow()
		select {
		case err := <-answers:
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unanswered")
		case <-time.After(10 * time.Second):
			t.Fatal("the turn did not end")
		}
		select {
		case <-s.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("the worker was not ended")
		}
	})
}
