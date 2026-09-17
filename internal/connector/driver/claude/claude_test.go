//go:build unix

package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// The test binary doubles as a fake claude: run with FAKE_CLAUDE set, it
// speaks the stream-json protocol according to the scenario it names and
// writes what it was started with to FAKE_CLAUDE_REPORT.
func TestMain(m *testing.M) {
	if scenario := os.Getenv("FAKE_CLAUDE"); scenario != "" {
		fakeClaude(scenario)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fakeReport struct {
	Args      []string          `json:"args"`
	Env       []string          `json:"env"`
	MCPConfig string            `json:"mcp_config"`
	MCPMode   os.FileMode       `json:"mcp_mode"`
	Extra     map[string]string `json:"extra"`
}

func argAfter(args []string, flag string) string {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return ""
	}
	return args[i+1]
}

func fakeClaude(scenario string) {
	args := os.Args[1:]
	report := fakeReport{Args: args, Env: os.Environ(), Extra: map[string]string{}}
	mcpPath := argAfter(args, "--mcp-config")
	var servers []string
	if info, err := os.Stat(mcpPath); err == nil {
		report.MCPMode = info.Mode().Perm()
		data, _ := os.ReadFile(mcpPath)
		report.MCPConfig = string(data)
		var cfg struct {
			MCPServers map[string]any `json:"mcpServers"`
		}
		_ = json.Unmarshal(data, &cfg)
		for name := range cfg.MCPServers {
			servers = append(servers, name)
		}
	}
	writeReport := func() {
		data, _ := json.Marshal(report)
		_ = os.WriteFile(os.Getenv("FAKE_CLAUDE_REPORT"), data, 0o600)
	}
	writeReport()

	out := bufio.NewWriter(os.Stdout)
	emit := func(v any) {
		data, _ := json.Marshal(v)
		_, _ = out.Write(append(data, '\n'))
		_ = out.Flush()
	}
	sessionID := argAfter(args, "--session-id")
	if sessionID == "" {
		sessionID = argAfter(args, "--resume")
	}
	mode := argAfter(args, "--permission-mode")
	if scenario == "badmode" {
		mode = "bypassPermissions"
	}
	status := "connected"
	if scenario == "mcpfailed" {
		status = "failed"
	}

	in := bufio.NewScanner(os.Stdin)
	inited := false
	for in.Scan() {
		var msg map[string]any
		if json.Unmarshal(in.Bytes(), &msg) != nil {
			continue
		}
		switch msg["type"] {
		case "control_request":
			// Like Claude Code, an interrupt with no turn running does
			// nothing.
			if inited && (scenario == "hang" || scenario == "child") {
				emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "session_id": sessionID})
			}
			continue
		case "user":
		default:
			continue
		}
		if !inited {
			inited = true
			mcp := make([]map[string]string, 0, len(servers))
			for _, s := range servers {
				mcp = append(mcp, map[string]string{"name": s, "status": status})
			}
			emit(map[string]any{"type": "system", "subtype": "init", "session_id": sessionID, "permissionMode": mode, "mcp_servers": mcp})
			if _, err := os.Stat(mcpPath); err == nil {
				report.Extra["mcp_after_init"] = "present"
			}
		}
		switch scenario {
		case "hang":
			continue
		case "child":
			// A grandchild in the worker's group.
			cmd := execSleep()
			report.Extra["child"] = fmt.Sprint(cmd)
			writeReport()
			continue
		case "die":
			os.Exit(3)
		case "late-denial":
			// A denial the stream never announced, only the result.
			emit(map[string]any{"type": "result", "subtype": "success", "stop_reason": "end_turn", "is_error": false, "session_id": sessionID,
				"permission_denials": []any{map[string]any{"tool_name": "Bash", "tool_use_id": "toolu_late"}}})
			continue
		case "escape":
			// A descendant in a session of its own, holding stdout.
			pid, _ := syscall.ForkExec("/bin/sleep", []string{"sleep", "300"}, &syscall.ProcAttr{
				Env: []string{}, Files: []uintptr{0, 1, 2}, Sys: &syscall.SysProcAttr{Setsid: true},
			})
			report.Extra["escaped"] = fmt.Sprint(pid)
			writeReport()
		}
		emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "secret words the connector never keeps"},
			map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{"command": "rm -rf /"}},
		}}})
		emit(map[string]any{"type": "system", "subtype": "permission_denied", "tool_name": "Bash", "tool_use_id": "toolu_1"})
		emit(map[string]any{"type": "result", "subtype": "success", "stop_reason": "end_turn", "is_error": false, "session_id": sessionID,
			"usage":              map[string]any{"input_tokens": 12, "output_tokens": 34},
			"permission_denials": []any{map[string]any{"tool_name": "Bash", "tool_use_id": "toolu_1", "tool_input": map[string]any{"command": "rm -rf /"}}}})
		writeReport()
	}
	writeReport()
}

func execSleep() int {
	pid, err := syscall.ForkExec("/bin/sleep", []string{"sleep", "300"}, &syscall.ProcAttr{Env: []string{}})
	if err != nil {
		return 0
	}
	return pid
}

type fixture struct {
	driver *Driver
	cfg    driver.SessionConfig
	report string
}

func newFixture(t *testing.T, scenario string) fixture {
	t.Helper()
	work := t.TempDir()
	private := filepath.Join(t.TempDir(), "session")
	require.NoError(t, os.Mkdir(private, 0o700))
	report := filepath.Join(t.TempDir(), "report.json")
	exe, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("CONNECTOR_CANARY_NOT_REAL", "leaked")
	return fixture{
		driver: New(Options{Binary: exe, CloseGrace: time.Second, Lookup: func(k string) (string, bool) {
			if k == "ANTHROPIC_API_KEY" {
				return "test-key-not-real", true
			}
			return "", false
		}}),
		cfg: driver.SessionConfig{
			Cwd: work,
			Env: []string{"FAKE_CLAUDE=" + scenario, "FAKE_CLAUDE_REPORT=" + report, "HOME=" + work},
			MCPServers: []driver.MCPServer{{
				Name: "basecamp", Command: "/usr/local/bin/basecamp", Args: []string{"mcp", "--profile", "agent"},
				Env: map[string]string{"BASECAMP_CONNECT_TASK_TOKEN": "test-token-not-real"},
			}},
			Policy:     policy{workDir: work},
			Scope:      driver.Scope{WorkDir: work},
			PrivateDir: private,
		},
		report: report,
	}
}

func (f fixture) readReport(t *testing.T) fakeReport {
	t.Helper()
	var r fakeReport
	data, err := os.ReadFile(f.report)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &r))
	return r
}

type policy struct{ workDir string }

func (p policy) Decide(context.Context, driver.PermissionRequest) driver.PermissionDecision {
	return driver.PermissionDecision{}
}

func (p policy) Rules() driver.PermissionRules {
	return driver.PermissionRules{
		Mode: driver.ModeEditsInWorkDir, WorkDir: p.workDir,
		AllowKinds: []driver.ToolKind{driver.ToolRead, driver.ToolSearch}, AllowMCPServers: []string{"basecamp"},
	}
}

func start(t *testing.T, f fixture) driver.Session {
	t.Helper()
	s, err := f.driver.NewSession(context.Background(), f.cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// Driver invariants 1 and 2 as written on the command line: an explicit mode,
// no host settings, no other MCP servers, only the allowed tools, and no
// token in argv.
func TestArgsFreezeThePolicyAndCarryNoSecret(t *testing.T) {
	f := newFixture(t, "ok")
	args, err := Args(f.cfg, "11111111-2222-4333-8444-555555555555", false, "/private/mcp.json", "")
	require.NoError(t, err)
	assert.Equal(t, "acceptEdits", argAfter(args, "--permission-mode"))
	assert.Equal(t, "none", argAfter(args, "--permission-prompts"))
	require.Contains(t, args, "--setting-sources")
	assert.Equal(t, "", argAfter(args, "--setting-sources"), "no user, project or local settings")
	assert.Contains(t, args, "--strict-mcp-config")
	tools := strings.Split(argAfter(args, "--tools"), ",")
	assert.NotContains(t, tools, "Bash")
	assert.NotContains(t, tools, "WebFetch")
	assert.Equal(t, "mcp__basecamp", argAfter(args, "--allowed-tools"), "no read tool is an allow rule: that would allow reads anywhere")
	assert.Contains(t, tools, "Read", "the tool exists; the mode confines it to the working directory")
	assert.NotContains(t, strings.Join(args, " "), "test-token-not-real")

	f.cfg.Cwd = "/elsewhere"
	_, err = Args(f.cfg, "11111111-2222-4333-8444-555555555555", false, "/private/mcp.json", "")
	assert.Error(t, err, "a policy for another directory is not this session's")
}

func TestASessionRunsAVerifiedTurnAndRecordsRefusals(t *testing.T) {
	f := newFixture(t, "ok")
	s := start(t, f)
	var updates []driver.Update
	done := make(chan struct{})
	go func() {
		for u := range s.Updates() {
			updates = append(updates, u)
		}
		close(done)
	}()

	result, err := s.Prompt(context.Background(), "hello")
	require.NoError(t, err)
	assert.Equal(t, driver.TurnEndTurn, result.Stop)
	assert.Equal(t, []driver.Refusal{{ToolCallID: "toolu_1", Tool: "Bash"}}, result.Refusals)
	assert.Equal(t, int64(12), result.Usage.InputTokens)

	// A follow-up in the same session.
	result, err = s.Prompt(context.Background(), "again")
	require.NoError(t, err)
	assert.Equal(t, driver.TurnEndTurn, result.Stop)
	require.NoError(t, s.Close())
	<-done

	for _, u := range updates {
		encoded, _ := json.Marshal(u)
		assert.NotContains(t, string(encoded), "secret words", "updates carry no content")
		assert.NotContains(t, string(encoded), "rm -rf", "updates carry no tool input")
	}
	assert.True(t, slices.ContainsFunc(updates, func(u driver.Update) bool { return u.Kind == driver.UpdatePermission && !u.Allowed }))

	r := f.readReport(t)
	assert.NotContains(t, strings.Join(r.Env, "\n"), "CONNECTOR_CANARY_NOT_REAL")
	assert.Contains(t, r.Env, "ANTHROPIC_API_KEY=test-key-not-real", "the driver's own named variables are added")
	assert.Equal(t, os.FileMode(0o600), r.MCPMode)
	assert.Contains(t, r.MCPConfig, "test-token-not-real", "the token reaches the MCP server's declared environment")
	_, statErr := os.Stat(filepath.Join(f.cfg.PrivateDir, "mcp.json"))
	assert.True(t, os.IsNotExist(statErr), "the config file holding the token is removed")
}

func TestTheConfigFileIsRemovedOnceTheServersStart(t *testing.T) {
	f := newFixture(t, "hang")
	s := start(t, f)
	go func() { _, _ = s.Prompt(context.Background(), "hello") }()
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(f.cfg.PrivateDir, "mcp.json"))
		return os.IsNotExist(err)
	}, 5*time.Second, 10*time.Millisecond)
}

// Driver invariant 2.
func TestAnUnconfirmedModeIsUnsafe(t *testing.T) {
	f := newFixture(t, "badmode")
	s := start(t, f)
	_, err := s.Prompt(context.Background(), "hello")
	assert.ErrorIs(t, err, driver.ErrUnsafeMode)
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("an unsafe session's worker was left running")
	}
}

func TestAnMCPServerThatDidNotConnectEndsTheSession(t *testing.T) {
	f := newFixture(t, "mcpfailed")
	s := start(t, f)
	_, err := s.Prompt(context.Background(), "hello")
	assert.ErrorContains(t, err, "did not connect")
}

// Driver invariant 3.
func TestOnlyAnAskedForCancelReadsAsCanceled(t *testing.T) {
	f := newFixture(t, "hang")
	s := start(t, f)
	answers := make(chan driver.PromptResult, 1)
	go func() {
		result, _ := s.Prompt(context.Background(), "hello")
		answers <- result
	}()
	time.Sleep(200 * time.Millisecond)
	require.NoError(t, s.Cancel(context.Background()))
	select {
	case result := <-answers:
		assert.Equal(t, driver.TurnCanceled, result.Stop)
	case <-time.After(5 * time.Second):
		t.Fatal("the cancel did not end the turn")
	}

	// The same error result with no cancel asked for is not a cancel.
	f = newFixture(t, "hang")
	s = start(t, f)
	go func() {
		time.Sleep(300 * time.Millisecond)
		// A cancel written by someone else, not through Cancel.
		ss := s.(*session)
		_ = ss.write(map[string]any{"type": "control_request", "request_id": "x", "request": map[string]any{"subtype": "interrupt"}})
	}()
	result, err := s.Prompt(context.Background(), "hello")
	assert.Error(t, err)
	assert.NotEqual(t, driver.TurnCanceled, result.Stop)
}

func TestAWorkerThatDiesMidTurnEndsTheSession(t *testing.T) {
	f := newFixture(t, "die")
	s := start(t, f)
	_, err := s.Prompt(context.Background(), "hello")
	assert.ErrorIs(t, err, driver.ErrSessionEnded)
	<-s.Done()
	assert.Equal(t, 3, s.Exit().Code)
}

// Driver invariant 5.
func TestCloseLeavesNoProcessOfTheSessionBehind(t *testing.T) {
	f := newFixture(t, "child")
	s := start(t, f)
	go func() { _, _ = s.Prompt(context.Background(), "hello") }()
	var child int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(f.report)
		if err != nil {
			return false
		}
		var r fakeReport
		if json.Unmarshal(data, &r) != nil || r.Extra["child"] == "" {
			return false
		}
		_, err = fmt.Sscan(r.Extra["child"], &child)
		return err == nil && child > 0
	}, 5*time.Second, 20*time.Millisecond)
	require.NoError(t, s.Close())
	assert.Eventually(t, func() bool {
		return syscall.Kill(child, 0) != nil
	}, 5*time.Second, 20*time.Millisecond)
	require.NoError(t, s.Close(), "Close is idempotent")
}

func TestAMissingBinaryIsNotStarted(t *testing.T) {
	f := newFixture(t, "ok")
	f.driver.opts.Binary = "/nonexistent/claude"
	_, err := f.driver.NewSession(context.Background(), f.cfg)
	assert.ErrorIs(t, err, driver.ErrNotStarted)
	entries, _ := os.ReadDir(f.cfg.PrivateDir)
	assert.Empty(t, entries, "nothing holding the token is left behind")
}

func TestACancelRightAfterPromptStillInterruptsThatTurn(t *testing.T) {
	f := newFixture(t, "hang")
	s := start(t, f)
	ss := s.(*session)
	ss.beforePromptWrite = func() {
		go func() { _ = s.Cancel(context.Background()) }()
		time.Sleep(200 * time.Millisecond)
	}
	answers := make(chan driver.PromptResult, 1)
	go func() {
		result, _ := s.Prompt(context.Background(), "hello")
		answers <- result
	}()
	select {
	case result := <-answers:
		assert.Equal(t, driver.TurnCanceled, result.Stop)
	case <-time.After(5 * time.Second):
		t.Fatal("the interrupt went out before the prompt and interrupted nothing")
	}
}

func TestCloseReturnsWhenADescendantOutsideTheGroupHoldsTheOutput(t *testing.T) {
	f := newFixture(t, "escape")
	f.driver.opts.CloseGrace = 200 * time.Millisecond
	s := start(t, f)
	go func() { _, _ = s.Prompt(context.Background(), "hello") }()
	var escaped int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(f.report)
		if err != nil {
			return false
		}
		var r fakeReport
		if json.Unmarshal(data, &r) != nil || r.Extra["escaped"] == "" {
			return false
		}
		_, err = fmt.Sscan(r.Extra["escaped"], &escaped)
		return err == nil && escaped > 0
	}, 5*time.Second, 20*time.Millisecond)
	t.Cleanup(func() { _ = syscall.Kill(escaped, syscall.SIGKILL) })

	closed := make(chan struct{})
	go func() {
		_ = s.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close waited on output held by a process outside the worker's group")
	}
}

// Copilot r2: a refusal only the result reports is still reported both ways.
func TestARefusalOnlyTheResultReportsIsAlsoAnUpdate(t *testing.T) {
	f := newFixture(t, "late-denial")
	s := start(t, f)
	var updates []driver.Update
	done := make(chan struct{})
	go func() {
		for u := range s.Updates() {
			updates = append(updates, u)
		}
		close(done)
	}()
	result, err := s.Prompt(context.Background(), "hello")
	require.NoError(t, err)
	assert.Equal(t, []driver.Refusal{{ToolCallID: "toolu_late", Tool: "Bash"}}, result.Refusals)
	require.NoError(t, s.Close())
	<-done
	assert.True(t, slices.ContainsFunc(updates, func(u driver.Update) bool {
		return u.Kind == driver.UpdatePermission && u.ToolCallID == "toolu_late" && !u.Allowed
	}), "the refusal is an update too")
}

// Review r2: a cancel that arrives before the turn cancels that turn.
func TestACancelBeforeAnyTurnCancelsTheNextOne(t *testing.T) {
	f := newFixture(t, "hang")
	s := start(t, f)
	require.NoError(t, s.Cancel(context.Background()))
	result, err := s.Prompt(context.Background(), "hello")
	require.NoError(t, err)
	assert.Equal(t, driver.TurnCanceled, result.Stop)
}
