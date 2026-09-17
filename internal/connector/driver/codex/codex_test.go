//go:build unix

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/drivertest"
)

const (
	testThread = "01a0adfe-499c-7f63-9553-b9975a3c4b55"
	testToken  = "test-token-not-real"
	hostCanary = "host-canary-not-real"
	serverOnly = "declared-for-the-server-only"
)

// safeTurnContext is the policy the driver's flags ask for.
func safeTurnContext() map[string]any {
	return map[string]any{
		"approval_policy": "never",
		"sandbox_policy": map[string]any{
			"type": "workspace-write", "network_access": false,
			"exclude_tmpdir_env_var": true, "exclude_slash_tmp": true,
		},
		// "$CWD" is the fake's own working directory.
		"file_system_sandbox_policy": map[string]any{
			"kind": "restricted",
			"entries": []any{
				map[string]any{"path": map[string]any{"type": "special", "value": map[string]any{"kind": "root"}}, "access": "read"},
				map[string]any{"path": map[string]any{"type": "path", "path": "$CWD"}, "access": "write"},
				map[string]any{"path": map[string]any{"type": "path", "path": "$CWD/.git"}, "access": "read"},
			},
		},
	}
}

func fsEntries(tc map[string]any) []any {
	return tc["file_system_sandbox_policy"].(map[string]any)["entries"].([]any)
}

type harness struct {
	t       *testing.T
	home    string // CODEX_HOME
	workDir string
	private string
	mcpOut  string
	drv     *Driver
}

func newHarness(t *testing.T, sc scenario) *harness {
	t.Helper()
	root := t.TempDir()
	h := &harness{
		t:       t,
		home:    filepath.Join(root, "codex-home"),
		workDir: filepath.Join(root, "work"),
		private: filepath.Join(root, "private"),
		mcpOut:  filepath.Join(root, "mcp-env.txt"),
	}
	require.NoError(t, os.Mkdir(h.home, 0o700))
	require.NoError(t, os.Mkdir(h.workDir, 0o700))
	require.NoError(t, os.Mkdir(h.private, 0o700))
	if sc.Thread == "" {
		sc.Thread = testThread
	}
	h.scenario(sc)
	self, err := os.Executable()
	require.NoError(t, err)
	host := map[string]string{
		"CODEX_HOME":           h.home,
		"HOME":                 root,
		"PATH":                 os.Getenv("PATH"),
		"HOST_SECRET_NOT_REAL": hostCanary,
		"OPENAI_API_KEY":       hostCanary,
	}
	h.drv = New(Options{
		Binary:        self,
		Lookup:        func(k string) (string, bool) { v, ok := host[k]; return v, ok },
		CloseGrace:    2 * time.Second,
		VerifyTimeout: time.Second,
	})
	return h
}

func (h *harness) scenario(sc scenario) {
	if sc.Thread == "" {
		sc.Thread = testThread
	}
	data, err := json.Marshal(sc)
	require.NoError(h.t, err)
	require.NoError(h.t, os.WriteFile(filepath.Join(h.home, "scenario.json"), data, 0o600))
}

type testPolicy struct {
	workDir string
	kinds   []driver.ToolKind
	servers []string
	mode    driver.PermissionMode
}

func (p testPolicy) Decide(context.Context, driver.PermissionRequest) driver.PermissionDecision {
	return driver.PermissionDecision{}
}

func (p testPolicy) Rules() driver.PermissionRules {
	mode := p.mode
	if mode == "" {
		mode = driver.ModeEditsInWorkDir
	}
	return driver.PermissionRules{Mode: mode, WorkDir: p.workDir, AllowKinds: p.kinds, AllowMCPServers: p.servers}
}

func (h *harness) config() driver.SessionConfig {
	return driver.SessionConfig{
		Cwd: h.workDir,
		Env: []string{"HOME=" + filepath.Dir(h.home), "PATH=" + os.Getenv("PATH")},
		MCPServers: []driver.MCPServer{{
			Name:    "basecamp",
			Command: "/bin/sh",
			Args:    []string{"-c", `env > "$MCP_ENV_OUT"`},
			Env:     map[string]string{"MCP_ENV_OUT": h.mcpOut, "SERVER_ONLY_NOT_SECRET": serverOnly, "PATH": os.Getenv("PATH")},
		}},
		Policy:     connector.DefaultPolicy(h.workDir),
		Scope:      driver.Scope{WorkDir: h.workDir},
		PrivateDir: h.private,
	}
}

func (h *harness) observed() observed {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.home, "observed.json"))
	require.NoError(h.t, err)
	var obs observed
	require.NoError(h.t, json.Unmarshal(data, &obs))
	return obs
}

func (h *harness) run(ctx context.Context, cfg driver.SessionConfig) (driver.Session, driver.PromptResult, error) {
	h.t.Helper()
	s, err := h.drv.NewSession(ctx, cfg)
	require.NoError(h.t, err)
	h.t.Cleanup(func() { _ = s.Close() })
	result, err := s.Prompt(ctx, "Task 1. Event 2.")
	return s, result, err
}

func turnCompleted() string {
	return `{"type":"turn.completed","usage":{"input_tokens":120,"output_tokens":7}}`
}

// The flags hold the v1 policy as written: the host's configuration, rules,
// features and skills off; approvals never; the sandbox confined to the
// working directory; the MCP server required, its tools approved only when
// the policy allows its server; the prompt on stdin.
func TestArgsHoldThePolicy(t *testing.T) {
	cfg := driver.SessionConfig{
		Cwd:    "/work/app",
		Policy: connector.DefaultPolicy("/work/app"),
		MCPServers: []driver.MCPServer{
			{Name: "basecamp", Command: "/bin/basecamp", Args: []string{"connect", "worker-mcp", "--socket", "/run/token.sock"}, Env: map[string]string{"HOME": "/home/op", "BASECAMP_NO_KEYRING": `a"quoted\value`}},
			{Name: "other", Command: "/bin/other"},
		},
	}
	args, err := Args(cfg, "", "")
	require.NoError(t, err)

	joined := strings.Join(args, "\x00")
	for _, want := range [][]string{
		{"--json"}, {"--ignore-user-config"}, {"--ignore-rules"},
		{"-c", `approval_policy="never"`},
		{"-c", `sandbox_mode="workspace-write"`},
		{"-c", "sandbox_workspace_write.network_access=false"},
		{"-c", "sandbox_workspace_write.exclude_slash_tmp=true"},
		{"-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true"},
		{"-c", "sandbox_workspace_write.writable_roots=[]"},
		{"-c", `shell_environment_policy.inherit="core"`},
		{"-c", "skills.include_instructions=false"},
		{"-c", "skills.bundled.enabled=false"},
		{"--disable", "apps"}, {"--disable", "plugins"}, {"--disable", "hooks"},
		{"--strict-config"},
		{"-c", `mcp_servers.basecamp.command="/bin/basecamp"`},
		{"-c", `mcp_servers.basecamp.args=["connect","worker-mcp","--socket","/run/token.sock"]`},
		{"-c", `mcp_servers.basecamp.env={"BASECAMP_NO_KEYRING"="a\"quoted\\value","HOME"="/home/op"}`},
		{"-c", "mcp_servers.basecamp.required=true"},
		{"-c", `mcp_servers.basecamp.default_tools_approval_mode="approve"`},
		{"-c", "mcp_servers.other.required=true"},
		{"-c", `mcp_servers.other.default_tools_approval_mode="prompt"`},
	} {
		assert.Contains(t, joined, strings.Join(want, "\x00"))
	}
	assert.Equal(t, "exec", args[0])
	assert.Equal(t, "-", args[len(args)-1], "the prompt is read from stdin")

	resumed, err := Args(cfg, testThread, "gpt-test")
	require.NoError(t, err)
	assert.Equal(t, []string{"exec", "resume"}, resumed[:2])
	assert.Equal(t, []string{"--model", "gpt-test", testThread, "-"}, resumed[len(resumed)-4:])
}

// A policy Codex's flags cannot hold is refused before anything starts.
func TestArgsRefuseAPolicyCodexCannotHold(t *testing.T) {
	server := []driver.MCPServer{{Name: "basecamp", Command: "/bin/basecamp"}}
	for name, cfg := range map[string]driver.SessionConfig{
		"another mode":       {Cwd: "/w", Policy: testPolicy{workDir: "/w", mode: "anything"}, MCPServers: server},
		"another workdir":    {Cwd: "/w", Policy: testPolicy{workDir: "/elsewhere"}, MCPServers: server},
		"execute allowed":    {Cwd: "/w", Policy: testPolicy{workDir: "/w", kinds: []driver.ToolKind{driver.ToolExecute}}, MCPServers: server},
		"fetch allowed":      {Cwd: "/w", Policy: testPolicy{workDir: "/w", kinds: []driver.ToolKind{driver.ToolFetch}}, MCPServers: server},
		"unkeyable server":   {Cwd: "/w", Policy: testPolicy{workDir: "/w"}, MCPServers: []driver.MCPServer{{Name: "a.b", Command: "/bin/x"}}},
		"no command":         {Cwd: "/w", Policy: testPolicy{workDir: "/w"}, MCPServers: []driver.MCPServer{{Name: "other"}}},
		"unkeyable env name": {Cwd: "/w", Policy: testPolicy{workDir: "/w"}, MCPServers: []driver.MCPServer{{Name: "other", Command: "/bin/x", Env: map[string]string{"A=B": "x"}}}},
	} {
		_, err := Args(cfg, "", "")
		assert.ErrorIs(t, err, driver.ErrUnusable, name)
	}
}

// Invariants 1 and 2 under the connector's own token carriage: the MCP
// server gets exactly its declared environment, Codex's own environment gets
// none of it and nothing of the host's, and with a task token served on its
// one-use socket (as the dispatcher serves it) the token is in no
// environment, no argv, no log and no file, at any moment of the session.
func TestTheTaskTokenIsNowhereTheDriverTouches(t *testing.T) {
	h := newHarness(t, scenario{RunMCP: true, TurnContext: safeTurnContext(), Events: []string{`{"type":"turn.started"}`, turnCompleted()}})
	tokens, err := connector.ServeTaskToken(h.private, testToken, time.Minute)
	require.NoError(t, err)
	t.Cleanup(tokens.Close)
	cfg := h.config()
	cfg.MCPServers[0].Args = append(cfg.MCPServers[0].Args, "--socket", tokens.Path())

	var s driver.Session
	stop := drivertest.WatchForSecretFiles(testToken, h.private, h.workDir, h.home)
	s, result, err := h.run(context.Background(), cfg)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	assert.Empty(t, stop(), "no file ever held the token")
	assert.Equal(t, driver.TurnEndTurn, result.Stop)
	assert.Equal(t, testThread, s.ID())

	obs := h.observed()
	for _, kv := range obs.Env {
		assert.NotContains(t, kv, hostCanary, "nothing outside the allowlist is inherited")
		assert.NotContains(t, kv, serverOnly, "an MCP server's environment is not Codex's")
	}
	assert.Contains(t, obs.Env, "CODEX_HOME="+h.home)
	assert.Equal(t, "Task 1. Event 2.", obs.Prompt)
	serverEnv, err := os.ReadFile(h.mcpOut)
	require.NoError(t, err)
	assert.Contains(t, string(serverEnv), "SERVER_ONLY_NOT_SECRET="+serverOnly)

	drivertest.RequireNoSecret(t, testToken, drivertest.Places{
		Env:   obs.Env,
		Args:  obs.Args,
		Texts: []string{s.(*session).StderrTail(), string(serverEnv)},
		Dirs:  []string{h.workDir, h.private, h.home},
	})
}

// The credential rule, while the session runs, in drivertest's own form: no
// file under the session's directories ever carries the token.
func TestNoTokenFileEverExists(t *testing.T) {
	h := newHarness(t, scenario{RunMCP: true, TurnContext: safeTurnContext(), Events: []string{turnCompleted()}})
	tokens, err := connector.ServeTaskToken(h.private, testToken, time.Minute)
	require.NoError(t, err)
	t.Cleanup(tokens.Close)
	cfg := h.config()
	cfg.MCPServers[0].Args = append(cfg.MCPServers[0].Args, "--socket", tokens.Path())
	drivertest.RequireNoSecretFilesDuring(t, testToken, []string{h.private, h.workDir, h.home}, func() {
		s, _, err := h.run(context.Background(), cfg)
		require.NoError(t, err)
		require.NoError(t, s.Close())
	})
}

// Invariant 3: a turn is finished only once the rollout shows the policy the
// flags asked for; any other policy, or none, ends the session as unsafe.
func TestTheAppliedPolicyIsVerified(t *testing.T) {
	events := []string{`{"type":"turn.started"}`, turnCompleted()}
	unsafe := map[string]func(tc map[string]any){
		"approvals on request": func(tc map[string]any) { tc["approval_policy"] = "on-request" },
		"full access":          func(tc map[string]any) { tc["sandbox_policy"].(map[string]any)["type"] = "danger-full-access" },
		"network":              func(tc map[string]any) { tc["sandbox_policy"].(map[string]any)["network_access"] = true },
		"slash tmp":            func(tc map[string]any) { tc["sandbox_policy"].(map[string]any)["exclude_slash_tmp"] = false },
		"writable roots":       func(tc map[string]any) { tc["sandbox_policy"].(map[string]any)["writable_roots"] = []string{"/"} },
		"another directory":    func(tc map[string]any) { tc["cwd"] = "/" },
		"no filesystem policy": func(tc map[string]any) { delete(tc, "file_system_sandbox_policy") },
		"root writable": func(tc map[string]any) {
			fsEntries(tc)[0].(map[string]any)["access"] = "write"
		},
		"another path writable": func(tc map[string]any) {
			tc["file_system_sandbox_policy"].(map[string]any)["entries"] = append(fsEntries(tc),
				map[string]any{"path": map[string]any{"type": "path", "path": "/tmp"}, "access": "write"})
		},
		"unrestricted": func(tc map[string]any) { tc["file_system_sandbox_policy"].(map[string]any)["kind"] = "unrestricted" },
	}
	for name, mutate := range unsafe {
		t.Run(name, func(t *testing.T) {
			tc := safeTurnContext()
			mutate(tc)
			h := newHarness(t, scenario{TurnContext: tc, Events: events})
			s, _, err := h.run(context.Background(), h.config())
			require.ErrorIs(t, err, driver.ErrUnsafeMode)
			waitDone(t, s)
		})
	}
	t.Run("no policy record", func(t *testing.T) {
		h := newHarness(t, scenario{Events: events})
		s, _, err := h.run(context.Background(), h.config())
		require.ErrorIs(t, err, driver.ErrUnsafeMode)
		waitDone(t, s)
	})
	t.Run("no thread", func(t *testing.T) {
		h := newHarness(t, scenario{NoThread: true, TurnContext: safeTurnContext(), Events: events})
		s, _, err := h.run(context.Background(), h.config())
		require.ErrorIs(t, err, driver.ErrUnsafeMode)
		waitDone(t, s)
	})
	t.Run("the policy asked for", func(t *testing.T) {
		h := newHarness(t, scenario{TurnContext: safeTurnContext(), Events: events})
		_, result, err := h.run(context.Background(), h.config())
		require.NoError(t, err)
		assert.Equal(t, driver.TurnEndTurn, result.Stop)
		assert.Equal(t, driver.Usage{InputTokens: 120, OutputTokens: 7}, result.Usage)
	})
}

// An unsafe session is ended while its turn is still running, not when the
// turn ends.
func TestAnUnsafeSessionIsEndedMidTurn(t *testing.T) {
	tc := safeTurnContext()
	tc["approval_policy"] = "untrusted"
	h := newHarness(t, scenario{TurnContext: tc, Hang: true, Child: true, Events: []string{`{"type":"turn.started"}`}})
	start := time.Now()
	s, _, err := h.run(context.Background(), h.config())
	require.ErrorIs(t, err, driver.ErrUnsafeMode)
	waitDone(t, s)
	assert.Less(t, time.Since(start), 30*time.Second)
	assertGone(t, h.observed().ChildPID)
}

// A resumed thread is judged by the turn it runs now, not by an earlier turn
// already in its rollout.
func TestAResumedThreadIsJudgedByItsNewTurn(t *testing.T) {
	bad := safeTurnContext()
	bad["approval_policy"] = "on-request"
	h := newHarness(t, scenario{OldTurnContext: nil})
	// An earlier, safe turn is on disk before the resume.
	rollout := filepath.Join(h.home, "sessions", "2026", "09", "16", "rollout-2026-09-16T08-00-00-"+testThread+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(rollout), 0o700))
	old := safeTurnContext()
	old["cwd"] = h.workDir
	line, err := json.Marshal(map[string]any{"type": "turn_context", "payload": old})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(rollout, append(line, '\n'), 0o600))
	h.scenario(scenario{TurnContext: bad, Events: []string{turnCompleted()}})
	// The fake appends to the rollout it finds under today's name; point it at
	// the same file.
	require.NoError(t, os.MkdirAll(filepath.Join(h.home, "sessions", "2026", "09", "17"), 0o700))
	require.NoError(t, os.Rename(rollout, filepath.Join(h.home, "sessions", "2026", "09", "17", "rollout-2026-09-17T08-00-00-"+testThread+".jsonl")))

	s, err := h.drv.LoadSession(context.Background(), h.config(), testThread)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.Prompt(context.Background(), "Event 3.")
	require.ErrorIs(t, err, driver.ErrUnsafeMode)
	assert.Equal(t, []string{"exec", "resume"}, h.observed().Args[:2])
}

func TestLoadSessionRefusesAThreadItCannotFind(t *testing.T) {
	h := newHarness(t, scenario{})
	_, err := h.drv.LoadSession(context.Background(), h.config(), testThread)
	require.ErrorIs(t, err, driver.ErrNotStarted)
	_, err = h.drv.LoadSession(context.Background(), h.config(), "not-a-thread")
	require.ErrorIs(t, err, driver.ErrNotStarted)
	entries, err := os.ReadDir(h.private)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing is written for a session that never starts")
}

// Invariant 4: an MCP server that fails leaves no turn: Codex refuses to start
// one, and the driver reports the session ended, never not-started, because
// a process existed.
func TestAFailedMCPServerEndsTheSession(t *testing.T) {
	h := newHarness(t, scenario{RunMCP: true, TurnContext: safeTurnContext(), Events: []string{turnCompleted()}})
	cfg := h.config()
	cfg.MCPServers[0].Args = []string{"-c", "exit 1"}
	_, _, err := h.run(context.Background(), cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, driver.ErrSessionEnded)
	assert.NotErrorIs(t, err, driver.ErrNotStarted)
}

// Invariant 5: Cancel ends the whole process group, and only a cancel the
// connector asked for reads as canceled.
func TestCancelEndsTheProcessGroup(t *testing.T) {
	h := newHarness(t, scenario{TurnContext: safeTurnContext(), Hang: true, Child: true, Events: []string{`{"type":"turn.started"}`}})
	s, err := h.drv.NewSession(context.Background(), h.config())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	type answer struct {
		result driver.PromptResult
		err    error
	}
	answers := make(chan answer, 1)
	go func() {
		r, err := s.Prompt(context.Background(), "Event 1.")
		answers <- answer{r, err}
	}()
	pid := waitChild(t, h)
	require.NoError(t, s.Cancel(context.Background()))

	select {
	case a := <-answers:
		require.NoError(t, a.err)
		assert.Equal(t, driver.TurnCanceled, a.result.Stop)
	case <-time.After(20 * time.Second):
		t.Fatal("the canceled turn did not end")
	}
	waitDone(t, s)
	assertGone(t, pid)
}

func TestAWorkerThatExitsMidTurnIsNotCanceled(t *testing.T) {
	h := newHarness(t, scenario{TurnContext: safeTurnContext(), Events: []string{`{"type":"turn.started"}`}, Exit: 0})
	_, result, err := h.run(context.Background(), h.config())
	require.ErrorIs(t, err, driver.ErrSessionEnded)
	assert.NotEqual(t, driver.TurnCanceled, result.Stop)
}

func TestAFailedTurnIsAnError(t *testing.T) {
	h := newHarness(t, scenario{TurnContext: safeTurnContext(), Events: []string{`{"type":"turn.failed","error":{"message":"someone@example.com"}}`}, Exit: 1})
	_, _, err := h.run(context.Background(), h.config())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "example.com")
}

func TestASessionTakesOnePrompt(t *testing.T) {
	h := newHarness(t, scenario{TurnContext: safeTurnContext(), Events: []string{turnCompleted()}})
	s, _, err := h.run(context.Background(), h.config())
	require.NoError(t, err)
	_, err = s.Prompt(context.Background(), "Event 4.")
	require.ErrorIs(t, err, driver.ErrSessionEnded)
	assert.ErrorIs(t, err, errOnePrompt, "refused as a second prompt, not as a write to a closed pipe")
	assert.False(t, h.drv.Capabilities().FollowUpPrompts)
	assert.False(t, h.drv.Capabilities().LoadSession, "no ledger record can name a Codex thread to resume yet")
}

// Invariant 6: updates carry kinds, ids and counts. A refusal Codex's
// approval policy made is the driver's own record, and does not read as a
// cancel.
func TestUpdatesCarryNoContentAndRefusalsAreRecorded(t *testing.T) {
	secret := "SECRET-CONTENT-not-real"
	events := []string{
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"` + secret + `"}}`,
		`{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"cat ` + secret + `","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"cat ` + secret + `","aggregated_output":"` + secret + `","exit_code":0,"status":"completed"}}`,
		`{"type":"item.started","item":{"id":"item_2","type":"file_change","changes":[{"path":"/` + secret + `","kind":"add"}],"status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"item_3","type":"mcp_tool_call","server":"other","tool":"write","arguments":{"x":"` + secret + `"},"error":{"message":"MCP tool call requires approval, but approval policy is never"},"status":"failed"}}`,
		turnCompleted(),
	}
	h := newHarness(t, scenario{TurnContext: safeTurnContext(), Events: events})
	s, err := h.drv.NewSession(context.Background(), h.config())
	require.NoError(t, err)
	var updates []driver.Update
	collected := make(chan struct{})
	go func() {
		for u := range s.Updates() {
			updates = append(updates, u)
		}
		close(collected)
	}()
	result, err := s.Prompt(context.Background(), "Event 1.")
	require.NoError(t, err)
	require.NoError(t, s.Close())
	<-collected

	assert.Equal(t, driver.TurnEndTurn, result.Stop)
	require.Len(t, result.Refusals, 1)
	assert.Equal(t, driver.Refusal{ToolCallID: "item_3", Tool: "mcp__other__write"}, result.Refusals[0])

	data, err := json.Marshal(updates)
	require.NoError(t, err)
	assert.NotContains(t, string(data), secret)
	kinds := make([]driver.UpdateKind, 0, len(updates))
	for _, u := range updates {
		kinds = append(kinds, u.Kind)
	}
	for _, want := range []driver.UpdateKind{driver.UpdateAgentMessageChunk, driver.UpdateToolCall, driver.UpdateToolCallUpdate, driver.UpdatePermission, driver.UpdateUsage} {
		assert.Contains(t, kinds, want)
	}
	i := slices.IndexFunc(updates, func(u driver.Update) bool { return u.Kind == driver.UpdateAgentMessageChunk })
	assert.Equal(t, len(secret), updates[i].Chars)
}

// ErrNotStarted means no process: a missing binary is one, and leaves the
// session's private directory as it found it.
func TestAMissingBinaryIsNotStarted(t *testing.T) {
	h := newHarness(t, scenario{})
	h.drv.opts.Binary = filepath.Join(t.TempDir(), "no-codex")
	_, err := h.drv.NewSession(context.Background(), h.config())
	require.ErrorIs(t, err, driver.ErrNotStarted)
	entries, err := os.ReadDir(h.private)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func waitDone(t *testing.T, s driver.Session) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the worker did not exit")
	}
}

func waitChild(t *testing.T, h *harness) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(filepath.Join(h.home, "observed.json")); err == nil {
			var obs observed
			if json.Unmarshal(data, &obs) == nil && obs.ChildPID > 0 {
				return obs.ChildPID
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the fake never started its child")
	return 0
}

func assertGone(t *testing.T, pid int) {
	t.Helper()
	require.Positive(t, pid)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		// A zombie still answers kill(0); its state is Z.
		if stat, err := os.ReadFile(filepath.Join("/proc", itoa(pid), "stat")); err == nil && zombie(string(stat)) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process %d outlived its group's end", pid)
}

func itoa(n int) string { return strconv.Itoa(n) }

// zombie reports whether a /proc/<pid>/stat line is a zombie's.
func zombie(stat string) bool {
	_, rest, ok := strings.Cut(stat, ") ")
	return ok && strings.HasPrefix(rest, "Z")
}

// Invariant 3: a turn that fails, or loses its process, before the policy
// check has spoken waits for it, so an unsafe session reads as unsafe.
func TestAFailedTurnWaitsForThePolicyCheck(t *testing.T) {
	for name, sc := range map[string]scenario{
		"turn failed":  {Events: []string{`{"type":"turn.started"}`, `{"type":"turn.failed","error":{"message":"x"}}`}, Exit: 1},
		"process gone": {Events: []string{`{"type":"turn.started"}`}, Exit: 1},
	} {
		t.Run(name, func(t *testing.T) {
			// No policy record: the check only fails when its timeout passes,
			// well after the turn ended.
			h := newHarness(t, sc)
			_, _, err := h.run(context.Background(), h.config())
			require.ErrorIs(t, err, driver.ErrUnsafeMode)
		})
	}
}

// Invariant 5: a Cancel that comes before the prompt it races cancels that
// prompt; nothing is sent and the worker is ended.
func TestACancelBeforeThePromptCancelsIt(t *testing.T) {
	h := newHarness(t, scenario{TurnContext: safeTurnContext(), Hang: true, Events: []string{`{"type":"turn.started"}`}})
	s, err := h.drv.NewSession(context.Background(), h.config())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.Cancel(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := s.Prompt(ctx, "Event 1.")
	require.NoError(t, err)
	assert.Equal(t, driver.TurnCanceled, result.Stop)
	waitDone(t, s)
}

// A canceled turn whose policy check has already failed is reported unsafe,
// not canceled; one whose check is still running is canceled at once.
func TestACanceledTurnReportsAFailedPolicyCheck(t *testing.T) {
	for name, tc := range map[string]struct {
		done bool
		err  error
		want error
	}{
		"check failed":  {done: true, err: driver.ErrUnsafeMode, want: driver.ErrUnsafeMode},
		"check passed":  {done: true},
		"check running": {},
	} {
		t.Run(name, func(t *testing.T) {
			s := &session{verifyDone: make(chan struct{}), verifyErr: tc.err}
			if tc.done {
				close(s.verifyDone)
			}
			turn := &turn{done: make(chan struct{})}
			s.turn = turn
			s.finishCanceled(turn, nil)
			<-turn.done
			if tc.want != nil {
				require.ErrorIs(t, turn.err, tc.want)
				return
			}
			require.NoError(t, turn.err)
			assert.Equal(t, driver.TurnCanceled, turn.result.Stop)
		})
	}
}

// Invariant 5: Close does not wait forever on a descendant that left the
// worker's process group and still holds its output.
func TestCloseDoesNotWaitForAnEscapedChild(t *testing.T) {
	h := newHarness(t, scenario{TurnContext: safeTurnContext(), Escape: true, Events: []string{turnCompleted()}})
	s, err := h.drv.NewSession(context.Background(), h.config())
	require.NoError(t, err)
	_, err = s.Prompt(context.Background(), "Event 1.")
	require.NoError(t, err)
	t.Cleanup(func() {
		if pid := h.observed().EscapedPID; pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	done := make(chan struct{})
	go func() { _ = s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Close waited on an escaped child")
	}
}

// Invariant 6 and 3 together: a refusal Codex logs on stderr after the turn's
// last stdout line is still counted, and emitting it as the session ends does
// not send on a closed channel.
func TestARefusalLoggedAtTheVeryEndIsCounted(t *testing.T) {
	h := newHarness(t, scenario{
		TurnContext: safeTurnContext(),
		Events:      []string{`{"type":"turn.started"}`, turnCompleted()},
		Stderr:      "patch rejected: writing outside of the project; rejected by user approval settings",
	})
	s, err := h.drv.NewSession(context.Background(), h.config())
	require.NoError(t, err)
	drained := make(chan int, 1)
	go func() {
		n := 0
		for u := range s.Updates() {
			if u.Kind == driver.UpdatePermission {
				n++
			}
		}
		drained <- n
	}()
	result, err := s.Prompt(context.Background(), "Event 1.")
	require.NoError(t, err)
	require.NoError(t, s.Close())
	assert.Len(t, result.Refusals, 1)
	assert.Positive(t, <-drained)
}

// The same, when the process dies without completing its turn: the refusal is
// still emitted, and emitting it as the reader ends is not a send on a closed
// channel.
func TestARefusalLoggedAsTheWorkerDiesIsCounted(t *testing.T) {
	h := newHarness(t, scenario{
		TurnContext: safeTurnContext(),
		Events:      []string{`{"type":"turn.started"}`},
		Stderr:      "patch rejected: writing outside of the project; rejected by user approval settings",
		Exit:        1,
	})
	s, err := h.drv.NewSession(context.Background(), h.config())
	require.NoError(t, err)
	drained := make(chan int, 1)
	go func() {
		n := 0
		for u := range s.Updates() {
			if u.Kind == driver.UpdatePermission {
				n++
			}
		}
		drained <- n
	}()
	result, err := s.Prompt(context.Background(), "Event 1.")
	require.ErrorIs(t, err, driver.ErrSessionEnded)
	assert.Len(t, result.Refusals, 1)
	assert.Positive(t, <-drained)
	require.NoError(t, s.Close())
}

// A worker that stops reading its input cannot hold Close or Cancel: the
// prompt's write waits on the worker, and nothing else waits on the write.
func TestAWorkerThatStopsReadingHoldsNothing(t *testing.T) {
	h := newHarness(t, scenario{TurnContext: safeTurnContext(), Deaf: true, Hang: true, Events: []string{`{"type":"turn.started"}`}})
	s, err := h.drv.NewSession(context.Background(), h.config())
	require.NoError(t, err)
	go func() { _, _ = s.Prompt(context.Background(), strings.Repeat("Event 1. ", 200_000)) }()
	waitDeaf(t, h)

	canceled := make(chan struct{})
	go func() {
		require.NoError(t, s.Cancel(context.Background()))
		close(canceled)
	}()
	select {
	case <-canceled:
	case <-time.After(20 * time.Second):
		t.Fatal("a worker that stopped reading held Cancel")
	}

	// And Close on its own, with no cancel to end the process first.
	closed := make(chan struct{})
	go func() {
		_ = s.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(30 * time.Second):
		t.Fatal("a worker that stopped reading held Close")
	}
	waitDone(t, s)
}

func waitDeaf(t *testing.T, h *harness) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(filepath.Join(h.home, "observed.json")); err == nil {
			var obs observed
			if json.Unmarshal(data, &obs) == nil && obs.Deaf {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the fake never stopped reading")
}

// The same for Close on its own: a prompt still blocked writing to a worker
// that stopped reading does not hold it.
func TestCloseSurvivesAWorkerThatStoppedReading(t *testing.T) {
	h := newHarness(t, scenario{TurnContext: safeTurnContext(), Deaf: true, Hang: true, Events: []string{`{"type":"turn.started"}`}})
	s, err := h.drv.NewSession(context.Background(), h.config())
	require.NoError(t, err)
	go func() { _, _ = s.Prompt(context.Background(), strings.Repeat("Event 1. ", 200_000)) }()
	waitDeaf(t, h)

	closed := make(chan struct{})
	go func() {
		_ = s.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(30 * time.Second):
		t.Fatal("a worker that stopped reading held Close")
	}
	waitDone(t, s)
}

// A turn that completes while a cancel is pending ends canceled at once, not
// after the policy check's whole timeout.
func TestACompletedTurnThatWasCanceledDoesNotWaitForTheCheck(t *testing.T) {
	s := &session{verifyDone: make(chan struct{}), verifyAfter: time.Hour}
	turn := &turn{done: make(chan struct{}), canceled: true}
	s.turn = turn
	done := make(chan struct{})
	go func() { s.turnCompleted(event{}); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a canceled turn waited for the policy check")
	}
	require.NoError(t, turn.err)
	assert.Equal(t, driver.TurnCanceled, turn.result.Stop)
}

// A cancel with no prompt yet ends the worker at once, whether the prompt ever
// comes or not.
func TestACancelWithNoPromptEndsTheWorker(t *testing.T) {
	h := newHarness(t, scenario{TurnContext: safeTurnContext(), Hang: true})
	s, err := h.drv.NewSession(context.Background(), h.config())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.Cancel(context.Background()))
	waitDone(t, s)
	result, err := s.Prompt(context.Background(), "Event 1.")
	require.NoError(t, err)
	assert.Equal(t, driver.TurnCanceled, result.Stop, "canceled, not ended, though the worker is gone")
}

// A refusal Codex logs after a failed turn's event is still counted.
func TestARefusalLoggedAfterAFailedTurnIsCounted(t *testing.T) {
	h := newHarness(t, scenario{
		TurnContext: safeTurnContext(),
		Events:      []string{`{"type":"turn.started"}`, `{"type":"turn.failed","error":{"message":"x"}}`},
		Stderr:      "patch rejected: writing outside of the project; rejected by user approval settings",
		Exit:        1,
	})
	_, result, err := h.run(context.Background(), h.config())
	require.Error(t, err)
	assert.Len(t, result.Refusals, 1)
}

// Prompt honors its context even while its write is blocked on a worker that
// stopped reading.
func TestAPromptBlockedWritingHonorsItsContext(t *testing.T) {
	h := newHarness(t, scenario{TurnContext: safeTurnContext(), Deaf: true, Hang: true})
	s, err := h.drv.NewSession(context.Background(), h.config())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	returned := make(chan error, 1)
	go func() {
		_, err := s.Prompt(ctx, strings.Repeat("Event 1. ", 200_000))
		returned <- err
	}()
	select {
	case err := <-returned:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(20 * time.Second):
		t.Fatal("a blocked write held Prompt past its context")
	}
}

// redactionSecret is the value fed through every error path. It is obviously
// fake, and is planted everywhere a real secret would be: in the worker's
// environment, in its MCP server's environment, in the name of its private
// directory, and in what the agent writes back.
const redactionSecret = "test-token-not-real-c9f2b1"

func redactionHarness(t *testing.T, sc scenario) (*harness, driver.SessionConfig) {
	t.Helper()
	h := newHarness(t, sc)
	private := filepath.Join(t.TempDir(), redactionSecret)
	require.NoError(t, os.Mkdir(private, 0o700))
	cfg := h.config()
	cfg.PrivateDir = private
	cfg.Env = append(cfg.Env, "FAKE_CODEX_SECRET="+redactionSecret)
	cfg.MCPServers[0].Env["BASECAMP_CONNECT_TASK_TOKEN"] = redactionSecret
	cfg.Redaction = driver.Redaction{Secrets: []string{redactionSecret}}
	return h, cfg
}

func drain(s driver.Session) []driver.Update {
	var updates []driver.Update
	for u := range s.Updates() {
		updates = append(updates, u)
	}
	return updates
}

// The redaction rule (driver's redact.go): nothing this driver hands back
// carries the secret, whichever way the session fails.
func TestNoErrorPathCarriesTheSecretOut(t *testing.T) {
	secretEvents := []string{
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"` + redactionSecret + `","type":"mcp_tool_call","server":"` + redactionSecret + `","tool":"` + redactionSecret + `","status":"failed","error":{"message":"MCP tool call requires approval, but approval policy is never"}}}`,
	}
	stderr := "fatal: writing " + redactionSecret + ": patch rejected: writing outside of the project; rejected by user approval settings"
	drivertest.RequireRedacted(t, redactionSecret, []drivertest.RedactionPath{
		{Name: "start", Run: func(t *testing.T) drivertest.Crossing {
			h, cfg := redactionHarness(t, scenario{})
			h.drv.opts.Binary = filepath.Join(cfg.PrivateDir, "no-codex")
			_, err := h.drv.NewSession(context.Background(), cfg)
			require.ErrorIs(t, err, driver.ErrNotStarted)
			return drivertest.Crossing{Errors: []error{err}}
		}},
		{Name: "handshake", Run: func(t *testing.T) drivertest.Crossing {
			tc := safeTurnContext()
			tc["approval_policy"] = "on-request"
			h, cfg := redactionHarness(t, scenario{TurnContext: tc, Events: append(secretEvents, turnCompleted()), Stderr: stderr})
			s, result, err := h.run(context.Background(), cfg)
			require.ErrorIs(t, err, driver.ErrUnsafeMode)
			updates := make(chan []driver.Update, 1)
			go func() { updates <- drain(s) }()
			require.NoError(t, s.Close())
			return drivertest.Crossing{Errors: []error{err}, Results: []driver.PromptResult{result},
				Updates: <-updates, Texts: []string{s.(*session).StderrTail()}}
		}},
		{Name: "prompt", Run: func(t *testing.T) drivertest.Crossing {
			h, cfg := redactionHarness(t, scenario{TurnContext: safeTurnContext(),
				Events: append(secretEvents, `{"type":"turn.failed","error":{"message":"`+redactionSecret+`"}}`), Stderr: stderr, Exit: 1})
			s, result, err := h.run(context.Background(), cfg)
			require.Error(t, err)
			updates := make(chan []driver.Update, 1)
			go func() { updates <- drain(s) }()
			require.NoError(t, s.Close())
			return drivertest.Crossing{Errors: []error{err}, Results: []driver.PromptResult{result},
				Updates: <-updates, Texts: []string{s.(*session).StderrTail()}}
		}},
		{Name: "cancel", Run: func(t *testing.T) drivertest.Crossing {
			h, cfg := redactionHarness(t, scenario{TurnContext: safeTurnContext(), Deaf: true, Hang: true, Stderr: stderr})
			s, err := h.drv.NewSession(context.Background(), cfg)
			require.NoError(t, err)
			go func() { _, _ = s.Prompt(context.Background(), strings.Repeat("x", 1<<20)) }()
			waitDeaf(t, h)
			cancelErr := s.Cancel(context.Background())
			closeErr := s.Close()
			return drivertest.Crossing{Errors: []error{cancelErr, closeErr}, Texts: []string{s.(*session).StderrTail()}}
		}},
		{Name: "close", Run: func(t *testing.T) drivertest.Crossing {
			h, cfg := redactionHarness(t, scenario{TurnContext: safeTurnContext(), Events: secretEvents, Stderr: stderr, Exit: 1})
			s, result, err := h.run(context.Background(), cfg)
			require.Error(t, err, "the worker died in the turn")
			updates := make(chan []driver.Update, 1)
			go func() { updates <- drain(s) }()
			closeErr := s.Close()
			after, afterErr := s.Prompt(context.Background(), "again")
			return drivertest.Crossing{Errors: []error{err, closeErr, afterErr},
				Results: []driver.PromptResult{result, after}, Updates: <-updates, Texts: []string{s.(*session).StderrTail()}}
		}},
	})
}

// The refusal rule (driver's "Refusals"): every refusal is recorded as it is
// read, once per call, whichever way the turn ends — a canceled turn included,
// where a refusal Codex only logged would otherwise go with the session.
func TestEveryRefusalIsRecordedOnce(t *testing.T) {
	denial := `{"type":"item.completed","item":{"id":"item_7","type":"mcp_tool_call","server":"other","tool":"write","error":{"message":"MCP tool call requires approval, but approval policy is never"},"status":"failed"}}`
	stderr := "patch rejected: writing outside of the project; rejected by user approval settings"
	for name, tc := range map[string]struct {
		events   []string
		exit     int
		cancel   bool
		wantErr  bool
		wantSeen int
	}{
		"a completed turn": {events: []string{`{"type":"turn.started"}`, denial, turnCompleted()}, wantSeen: 2},
		"a failed turn":    {events: []string{`{"type":"turn.started"}`, denial, `{"type":"turn.failed","error":{"message":"x"}}`}, exit: 1, wantErr: true, wantSeen: 2},
		"a lost worker":    {events: []string{`{"type":"turn.started"}`, denial}, exit: 1, wantErr: true, wantSeen: 2},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := &drivertest.Refusals{}
			h := newHarness(t, scenario{TurnContext: safeTurnContext(), Events: tc.events, Stderr: stderr, Exit: tc.exit})
			cfg := h.config()
			cfg.Refusals = recorder
			s, result, err := h.run(context.Background(), cfg)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, s.Close())
			assert.Len(t, recorder.Recorded(), tc.wantSeen, "each refusal recorded once")
			assert.Len(t, result.Refusals, tc.wantSeen)
		})
	}
}

// A worker whose output ends before it does: the refusal it logs on its way
// out is still read, because the reader waits for the process, not for its
// stdout.
func TestARefusalLoggedAfterTheOutputEndsIsStillRecorded(t *testing.T) {
	recorder := &drivertest.Refusals{}
	h := newHarness(t, scenario{
		TurnContext: safeTurnContext(),
		Events:      []string{`{"type":"turn.started"}`},
		CloseStdout: true,
		Stderr:      "patch rejected: writing outside of the project; rejected by user approval settings",
		Exit:        1,
	})
	cfg := h.config()
	cfg.Refusals = recorder
	s, result, err := h.run(context.Background(), cfg)
	require.Error(t, err)
	require.NoError(t, s.Close())
	assert.Len(t, recorder.Recorded(), 1, "the refusal Codex logged after closing its output")
	assert.Len(t, result.Refusals, 1)
}

// Codex logs its sandbox refusals and keeps writing: each one is recorded,
// not only whatever it said last.
func TestEveryRefusalCodexOnlyLogsIsRecorded(t *testing.T) {
	recorder := &drivertest.Refusals{}
	h := newHarness(t, scenario{
		TurnContext: safeTurnContext(),
		Events:      []string{`{"type":"turn.started"}`, turnCompleted()},
		Stderr: strings.Join([]string{
			"patch rejected: writing outside of the project; rejected by user approval settings",
			"ERROR: command failed because the approval policy is never",
			"thinking about the next step",
			// The same diagnostic twice is two refusals, not one.
			"ERROR: command failed because the approval policy is never",
		}, "\n"),
	})
	cfg := h.config()
	cfg.Refusals = recorder
	s, result, err := h.run(context.Background(), cfg)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	assert.Len(t, recorder.Recorded(), 3, "every refusal, wherever it is and however it reads")
	assert.Len(t, result.Refusals, 3)
}

// A refusal Codex logged is recorded even when the turn it belonged to has
// already ended: the reader reads the stderr of a worker that is gone, with
// no turn left to hang it on.
func TestARefusalIsRecordedEvenWithNoTurnLeft(t *testing.T) {
	recorder := &drivertest.Refusals{}
	h := newHarness(t, scenario{
		TurnContext: safeTurnContext(),
		Deaf:        true,
		Hang:        true,
		Events:      []string{`{"type":"turn.started"}`},
		Stderr:      "patch rejected: writing outside of the project; rejected by user approval settings",
	})
	cfg := h.config()
	cfg.Refusals = recorder
	s, err := h.drv.NewSession(context.Background(), cfg)
	require.NoError(t, err)
	// A worker that never reads its input: the prompt's write blocks, and the
	// cancel that closes its stdin ends the turn from the write's side, not
	// the reader's.
	go func() { _, _ = s.Prompt(context.Background(), strings.Repeat("Event 1. ", 200_000)) }()
	waitDeaf(t, h)
	require.Eventually(t, func() bool { return strings.Contains(s.(*session).StderrTail(), "rejected") }, 10*time.Second, 20*time.Millisecond)
	require.NoError(t, s.Cancel(context.Background()))
	require.NoError(t, s.Close())
	waitDone(t, s)

	assert.Len(t, recorder.Recorded(), 1, "the refusal is recorded, turn or no turn")
}

// A canceled turn records what Codex logged before it went.
func TestACanceledTurnRecordsItsRefusals(t *testing.T) {
	recorder := &drivertest.Refusals{}
	h := newHarness(t, scenario{
		TurnContext: safeTurnContext(),
		Events:      []string{`{"type":"turn.started"}`},
		Stderr:      "patch rejected: writing outside of the project; rejected by user approval settings",
		Hang:        true,
	})
	cfg := h.config()
	cfg.Refusals = recorder
	s, err := h.drv.NewSession(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	answers := make(chan driver.PromptResult, 1)
	go func() {
		result, _ := s.Prompt(context.Background(), "Event 1.")
		answers <- result
	}()
	require.Eventually(t, func() bool { return strings.Contains(s.(*session).StderrTail(), "rejected") }, 10*time.Second, 20*time.Millisecond)
	require.NoError(t, s.Cancel(context.Background()))
	select {
	case result := <-answers:
		assert.Equal(t, driver.TurnCanceled, result.Stop)
		assert.Len(t, result.Refusals, 1)
	case <-time.After(20 * time.Second):
		t.Fatal("the canceled turn did not end")
	}
	assert.Len(t, recorder.Recorded(), 1, "the refusal Codex logged is recorded, not lost with the cancel")
}
