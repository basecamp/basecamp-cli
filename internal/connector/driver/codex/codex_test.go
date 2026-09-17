//go:build unix

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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
)

const (
	testThread = "01a0adfe-499c-7f63-9553-b9975a3c4b55"
	testToken  = "test-token-not-real"
	hostCanary = "host-canary-not-real"
)

// safeTurnContext is the policy the driver's flags ask for.
func safeTurnContext() map[string]any {
	return map[string]any{
		"approval_policy": "never",
		"sandbox_policy": map[string]any{
			"type": "workspace-write", "network_access": false,
			"exclude_tmpdir_env_var": true, "exclude_slash_tmp": true,
		},
	}
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
			Env:     map[string]string{"MCP_ENV_OUT": h.mcpOut, connector.TaskTokenEnv: testToken, "PATH": os.Getenv("PATH")},
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
			{Name: "basecamp", Command: "/bin/basecamp", Args: []string{"mcp"}, Env: map[string]string{connector.TaskTokenEnv: testToken}},
			{Name: "other", Command: "/bin/other"},
		},
	}
	files := map[string]string{"basecamp": "/private/mcp-basecamp.env", "other": "/private/mcp-other.env"}
	args, err := Args(cfg, "", files, "")
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
		{"-c", `mcp_servers.basecamp.command="/bin/sh"`},
		{"-c", "mcp_servers.basecamp.required=true"},
		{"-c", `mcp_servers.basecamp.default_tools_approval_mode="approve"`},
		{"-c", "mcp_servers.other.required=true"},
		{"-c", `mcp_servers.other.default_tools_approval_mode="prompt"`},
	} {
		assert.Contains(t, joined, strings.Join(want, "\x00"))
	}
	assert.Equal(t, "exec", args[0])
	assert.Equal(t, "-", args[len(args)-1], "the prompt is read from stdin")
	assert.NotContains(t, joined, testToken, "no secret in argv")

	var serverArgs []string
	for i, a := range args {
		if a == "-c" && strings.HasPrefix(args[i+1], "mcp_servers.basecamp.args=") {
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(args[i+1], "mcp_servers.basecamp.args=")), &serverArgs))
		}
	}
	assert.Equal(t, []string{"-c", mcpWrapper, "/private/mcp-basecamp.env", "/bin/basecamp", "mcp"}, serverArgs)

	resumed, err := Args(cfg, testThread, files, "gpt-test")
	require.NoError(t, err)
	assert.Equal(t, []string{"exec", "resume"}, resumed[:2])
	assert.Equal(t, []string{"--model", "gpt-test", testThread, "-"}, resumed[len(resumed)-4:])
}

// A policy Codex's flags cannot hold is refused before anything starts.
func TestArgsRefuseAPolicyCodexCannotHold(t *testing.T) {
	files := map[string]string{"basecamp": "/private/mcp-basecamp.env"}
	server := []driver.MCPServer{{Name: "basecamp", Command: "/bin/basecamp"}}
	for name, cfg := range map[string]driver.SessionConfig{
		"another mode":        {Cwd: "/w", Policy: testPolicy{workDir: "/w", mode: "anything"}, MCPServers: server},
		"another workdir":     {Cwd: "/w", Policy: testPolicy{workDir: "/elsewhere"}, MCPServers: server},
		"execute allowed":     {Cwd: "/w", Policy: testPolicy{workDir: "/w", kinds: []driver.ToolKind{driver.ToolExecute}}, MCPServers: server},
		"fetch allowed":       {Cwd: "/w", Policy: testPolicy{workDir: "/w", kinds: []driver.ToolKind{driver.ToolFetch}}, MCPServers: server},
		"unkeyable server":    {Cwd: "/w", Policy: testPolicy{workDir: "/w"}, MCPServers: []driver.MCPServer{{Name: "a.b", Command: "/bin/x"}}},
		"no environment file": {Cwd: "/w", Policy: testPolicy{workDir: "/w"}, MCPServers: []driver.MCPServer{{Name: "other", Command: "/bin/x"}}},
	} {
		_, err := Args(cfg, "", files, "")
		assert.Error(t, err, name)
	}
}

// Invariants 1 and 2: the worker's environment is the allowlist and Codex's
// own variables; the token reaches the MCP server through an owner-only file
// the wrapper deletes before the server starts, never Codex's environment or
// argv; and Close leaves no file behind.
func TestTheTokenReachesOnlyTheMCPServer(t *testing.T) {
	h := newHarness(t, scenario{RunMCP: true, TurnContext: safeTurnContext(), Events: []string{`{"type":"turn.started"}`, turnCompleted()}})
	s, result, err := h.run(context.Background(), h.config())
	require.NoError(t, err)
	assert.Equal(t, driver.TurnEndTurn, result.Stop)
	assert.Equal(t, testThread, s.ID())

	obs := h.observed()
	for _, kv := range obs.Env {
		assert.NotContains(t, kv, testToken, "the token is not in Codex's environment")
		assert.NotContains(t, kv, hostCanary, "nothing outside the allowlist is inherited")
	}
	assert.Contains(t, obs.Env, "CODEX_HOME="+h.home)
	assert.NotContains(t, strings.Join(obs.Args, " "), testToken)
	assert.Equal(t, "Task 1. Event 2.", obs.Prompt)

	require.Len(t, obs.EnvFile, 1)
	for file, mode := range obs.EnvFile {
		assert.Equal(t, "600", mode)
		assert.Equal(t, h.private, filepath.Dir(file))
	}
	assert.False(t, obs.FileAfter, "the wrapper deletes the environment file before the server runs")

	serverEnv, err := os.ReadFile(h.mcpOut)
	require.NoError(t, err)
	assert.Contains(t, string(serverEnv), connector.TaskTokenEnv+"="+testToken)

	require.NoError(t, s.Close())
	entries, err := os.ReadDir(h.private)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// Close removes an environment file the server never consumed.
func TestCloseRemovesAnUnconsumedEnvironmentFile(t *testing.T) {
	h := newHarness(t, scenario{Hang: true})
	s, err := h.drv.NewSession(context.Background(), h.config())
	require.NoError(t, err)
	entries, err := os.ReadDir(h.private)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	info, err := entries[0].Info()
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	require.NoError(t, s.Close())
	entries, err = os.ReadDir(h.private)
	require.NoError(t, err)
	assert.Empty(t, entries)
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
	assert.False(t, h.drv.Capabilities().FollowUpPrompts)
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
	kinds := []driver.UpdateKind{}
	for _, u := range updates {
		kinds = append(kinds, u.Kind)
	}
	for _, want := range []driver.UpdateKind{driver.UpdateAgentMessageChunk, driver.UpdateToolCall, driver.UpdateToolCallUpdate, driver.UpdatePermission, driver.UpdateUsage} {
		assert.Contains(t, kinds, want)
	}
	i := slices.IndexFunc(updates, func(u driver.Update) bool { return u.Kind == driver.UpdateAgentMessageChunk })
	assert.Equal(t, len(secret), updates[i].Chars)
}

// ErrNotStarted means no process: a missing binary is one, and leaves no
// environment file behind.
func TestAMissingBinaryIsNotStarted(t *testing.T) {
	h := newHarness(t, scenario{})
	h.drv.opts.Binary = filepath.Join(t.TempDir(), "no-codex")
	_, err := h.drv.NewSession(context.Background(), h.config())
	require.ErrorIs(t, err, driver.ErrNotStarted)
	entries, err := os.ReadDir(h.private)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestEnvironmentFilesAreShellSafe(t *testing.T) {
	dir := t.TempDir()
	value := `it's $(touch pwned) "quoted" ` + "`x`\nline"
	files, err := writeEnvFiles(dir, []driver.MCPServer{{Name: "basecamp", Env: map[string]string{"V": value}}})
	require.NoError(t, err)
	out := filepath.Join(dir, "out")
	script := `set -a && . "$0" && set +a && printf %s "$V" > "` + out + `"`
	cmd := execCommand("/bin/sh", "-c", script, files["basecamp"])
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, value, string(got))
	_, err = os.Stat(filepath.Join(dir, "pwned"))
	assert.True(t, errors.Is(err, os.ErrNotExist))

	_, err = writeEnvFiles(t.TempDir(), []driver.MCPServer{{Name: "basecamp", Env: map[string]string{"BAD-NAME": "x"}}})
	assert.Error(t, err)
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

func execCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...) //nolint:gosec // test helper
}

func itoa(n int) string { return strconv.Itoa(n) }

// zombie reports whether a /proc/<pid>/stat line is a zombie's.
func zombie(stat string) bool {
	_, rest, ok := strings.Cut(stat, ") ")
	return ok && strings.HasPrefix(rest, "Z")
}
