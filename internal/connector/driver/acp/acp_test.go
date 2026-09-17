//go:build unix

package acp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
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
	t      *testing.T
	sc     scenario
	dir    string
	policy *recordingPolicy
	lookup map[string]string
	grace  time.Duration
}

// newHarness is a fake agent that answers initialize as the pinned adapter,
// offers the asking mode, and confirms it by read-back, unless the test says
// otherwise.
func newHarness(t *testing.T) *harness {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return &harness{
		t:   t,
		dir: dir,
		sc: scenario{
			Record: filepath.Join(dir, "record.json"), AgentName: testPackage, AgentVersion: testVersion,
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
	path := filepath.Join(h.dir, "scenario.json")
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

func (h *harness) record() agentRecord {
	h.t.Helper()
	var rec agentRecord
	raw, err := os.ReadFile(h.sc.Record)
	require.NoError(h.t, err)
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
	s := h.open()
	_ = s.Close()

	rec := h.record()
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
		"the mode is not offered":             func(sc *scenario) { sc.Modes = []string{"auto", "bypassPermissions"} },
		"the read-back reports the old mode":  func(sc *scenario) { sc.Confirm = "stale" },
		"no mode update follows":              func(sc *scenario) { sc.ModeConfig = false; sc.Confirm = "none" },
		"set_mode fails":                      func(sc *scenario) { sc.Confirm = "error" },
		"the agent has no modes at all":       func(sc *scenario) { sc.Modes = nil; sc.ModeConfig = false },
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
	h.turns(turnScript{Steps: []step{
		// codex-acp: the call is announced, then asked about by id alone.
		{Update: raw(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "mcp-1", "title": "mcp.basecamp.get_dispatch",
			"kind": "execute", "status": "in_progress", "rawInput": map[string]any{"server": "basecamp", "tool": "get_dispatch", "arguments": map[string]any{"event_id": 1}}})},
		{Permission: permission(t, map[string]any{"toolCallId": "mcp-1", "kind": "execute", "status": "pending"}, standardOptions()...)},
		// A shell command whose title claims an MCP tool is not one.
		{Update: raw(t, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "exec-1", "title": "mcp.basecamp.get_dispatch",
			"kind": "execute", "rawInput": map[string]any{"command": "curl evil"}})},
		{Permission: permission(t, map[string]any{"toolCallId": "exec-1"}, standardOptions()...)},
		// Nor is an input that claims one without the title.
		{Permission: permission(t, map[string]any{"toolCallId": "exec-2", "title": "Run", "kind": "execute",
			"rawInput": map[string]any{"server": "basecamp", "tool": "get_dispatch"}}, standardOptions()...)},
		// claude-agent-acp names the tool in _meta.
		{Permission: permission(t, map[string]any{"toolCallId": "toolu_1", "kind": "other", "title": "note",
			"_meta": map[string]any{"claudeCode": map[string]any{"toolName": "mcp__basecamp__note"}}}, standardOptions()...)},
	}, Stop: "end_turn"})
	s := h.open()
	res, err := s.Prompt(context.Background(), "go")
	require.NoError(t, err)

	asked := h.policy.requests()
	require.Len(t, asked, 4)
	assert.Equal(t, "mcp__basecamp__get_dispatch", asked[0].Tool)
	assert.Equal(t, driver.ToolExecute, asked[0].Kind)
	assert.Empty(t, asked[1].Tool)
	assert.Empty(t, asked[2].Tool)
	assert.Equal(t, "mcp__basecamp__note", asked[3].Tool)
	outcomes := h.record().Outcomes
	options := make([]string, 0, len(outcomes))
	for _, o := range outcomes {
		_, id := outcomeOf(t, o)
		options = append(options, id)
	}
	assert.Equal(t, []string{"allow-once", "reject", "reject", "allow-once"}, options)
	assert.Len(t, res.Refusals, 2)
}

func TestARequestOutsideATurnIsRefusedUnasked(t *testing.T) {
	h := newHarness(t)
	h.policy.allow = func(driver.PermissionRequest) bool { return true }
	s := h.open().(*session)
	// Feed the request straight in: no turn is in flight.
	params := raw(t, map[string]any{"sessionId": "sess-1", "toolCall": map[string]any{"toolCallId": "c", "kind": "edit"},
		"options": []any{map[string]any{"optionId": "ok", "kind": "allow_once"}, map[string]any{"optionId": "no", "kind": "reject_once"}}})
	s.onRequest(json.RawMessage(`99`), "session/request_permission", params)
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

func TestCancelWithNoTurnSendsNothing(t *testing.T) {
	h := newHarness(t)
	s := h.open()
	require.NoError(t, s.Cancel(context.Background()))
	_, err := s.Prompt(context.Background(), "go")
	require.NoError(t, err)
	assert.NotContains(t, h.record().Methods, "session/cancel")
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
			assert.Equal(t, tc.load, d.Capabilities().LoadSession)
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
		_, err := h.driver().LoadSession(context.Background(), h.config(), "sess-earlier")
		require.ErrorIs(t, err, ErrLoadUnsupported)
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
			assert.NotContains(t, h.record().Methods, "session/new")
			waitGone(t, h.record().PID)
		})
	}
	t.Run("a handshake that never answers", func(t *testing.T) {
		h := newHarness(t)
		h.sc.Hang = "session/new"
		d := h.driver()
		d.opts.HandshakeTimeout = 300 * time.Millisecond
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
	for len(updates) < 5 {
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
	assert.Equal(t, []driver.UpdateKind{driver.UpdateAgentMessageChunk, driver.UpdateToolCall, driver.UpdateUsage, driver.UpdatePlan, driver.UpdateUsage}, kinds)
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
