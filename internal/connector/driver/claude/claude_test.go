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
	"github.com/basecamp/basecamp-cli/internal/connector/driver/drivertest"
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
		// Written whole and renamed into place: a test reading the report
		// while it is rewritten must never see half of it.
		data, _ := json.Marshal(report)
		path := os.Getenv("FAKE_CLAUDE_REPORT")
		_ = os.WriteFile(path+".tmp", data, 0o600)
		_ = os.Rename(path+".tmp", path)
	}
	writeReport()

	// A worker that writes a secret it was handed to its own stderr, which
	// the connector reads and may log.
	secret := os.Getenv("FAKE_CLAUDE_SECRET")
	if secret != "" {
		// The secret first, then the noise that would bury it: a driver that
		// reads only the LAST line would miss it, and one that reads the
		// lines raw would pass it on.
		fmt.Fprintln(os.Stderr, "claude: failed while using "+secret)
		fmt.Fprintln(os.Stderr, "claude: retrying in 2s")
		fmt.Fprintln(os.Stderr, "claude: giving up")
	}

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
	if scenario == "handshake-secret" {
		// An agent that reports a mode carrying what it was handed.
		mode = secret
	}
	if scenario == "badmode" {
		mode = "bypassPermissions"
	}
	status := "connected"
	if scenario == "mcpfailed" {
		status = "failed"
	}

	if scenario == "deaf" || scenario == "deaf-secret" {
		// Reads nothing, ever: the pipe fills and a write blocks.
		select {}
	}
	if scenario == "badmode-eager" {
		// An init before any prompt, in a mode the policy did not ask for.
		emit(map[string]any{"type": "system", "subtype": "init", "session_id": sessionID, "permissionMode": "bypassPermissions", "mcp_servers": []any{}})
		select {}
	}

	in := bufio.NewScanner(os.Stdin)
	inited := false
	for in.Scan() {
		var msg map[string]any
		if json.Unmarshal(in.Bytes(), &msg) != nil {
			continue
		}
		switch msg["type"] {
		case "control_request", "user":
			// The order messages reach the agent is what a cancel's
			// correctness rests on.
			kind, _ := msg["type"].(string)
			report.Extra["wire"] += kind + " "
			writeReport()
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
		if scenario == "denial-secret" {
			// A refusal and a failed turn, both named after the secret.
			emit(map[string]any{"type": "system", "subtype": "permission_denied", "tool_name": secret, "tool_use_id": secret})
			emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
				map[string]any{"type": "tool_use", "id": secret, "name": secret},
			}}})
			emit(map[string]any{"type": "result", "subtype": "error_" + secret, "is_error": true, "session_id": sessionID,
				"permission_denials": []any{map[string]any{"tool_name": secret, "tool_use_id": secret + "-late"}}})
			continue
		}
		if scenario == "die-secret" {
			os.Exit(3)
		}
		if scenario == "nameless-result-denials" {
			// Three denials in the result, none with a call id: three
			// refusals, not one.
			emit(map[string]any{"type": "result", "subtype": "success", "stop_reason": "end_turn", "is_error": false, "session_id": sessionID,
				"permission_denials": []any{
					map[string]any{"tool_name": "Bash"},
					map[string]any{"tool_name": "Write"},
					map[string]any{"tool_name": "WebFetch"},
				}})
			continue
		}
		if scenario == "two-nameless-refusals" {
			// Two refusals of the same tool with no call id between them:
			// two refusals, not one (card 19's Codex accounting).
			for range 2 {
				emit(map[string]any{"type": "system", "subtype": "permission_denied", "tool_name": "Bash"})
			}
			emit(map[string]any{"type": "result", "subtype": "success", "stop_reason": "end_turn", "is_error": false, "session_id": sessionID})
			continue
		}
		if scenario == "denied-twice" {
			// One refusal the stream announces twice and the result repeats.
			for range 2 {
				emit(map[string]any{"type": "system", "subtype": "permission_denied", "tool_name": "Bash", "tool_use_id": "toolu_twice"})
			}
			emit(map[string]any{"type": "result", "subtype": "success", "stop_reason": "end_turn", "is_error": false, "session_id": sessionID,
				"permission_denials": []any{map[string]any{"tool_name": "Bash", "tool_use_id": "toolu_twice"}}})
			continue
		}
		if scenario == "deny-then-die" {
			// Refused, and gone before any result could repeat it.
			emit(map[string]any{"type": "system", "subtype": "permission_denied", "tool_name": "Bash", "tool_use_id": "toolu_dead"})
			os.Exit(3)
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
	r, err := f.report_()
	require.NoError(t, err)
	return r
}

// report_ reads the report without failing the test, for polling.
func (f fixture) report_() (fakeReport, error) {
	var r fakeReport
	data, err := os.ReadFile(f.report)
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(data, &r)
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

	// The credential rule, from the moment the MCP servers started: no file
	// under the working directory or the session's own directory carries the
	// task token, however briefly, through a follow-up and the close.
	drivertest.RequireNoSecretFilesDuring(t, "test-token-not-real", []string{f.cfg.Cwd, f.cfg.PrivateDir}, func() {
		// A follow-up in the same session.
		result, err = s.Prompt(context.Background(), "again")
		require.NoError(t, err)
		assert.Equal(t, driver.TurnEndTurn, result.Stop)
		require.NoError(t, s.Close())
	})
	<-done

	for _, u := range updates {
		encoded, _ := json.Marshal(u)
		assert.NotContains(t, string(encoded), "secret words", "updates carry no content")
		assert.NotContains(t, string(encoded), "rm -rf", "updates carry no tool input")
	}
	assert.True(t, slices.ContainsFunc(updates, func(u driver.Update) bool { return u.Kind == driver.UpdatePermission && !u.Allowed }))

	r := f.readReport(t)
	// The token is in neither the agent's own environment nor its argv.
	drivertest.RequireNoSecret(t, "test-token-not-real", drivertest.Places{Env: r.Env, Args: r.Args, Dirs: []string{f.cfg.Cwd}})
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
		if err := ss.takeSlot(context.Background(), time.Second); err == nil {
			_ = ss.writeHeld(map[string]any{"type": "control_request", "request_id": "x", "request": map[string]any{"subtype": "interrupt"}})
			ss.releaseSlot()
		}
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

// Copilot on #739: the interrupt goes to the turn Cancel observed, never to a
// prompt that registered after it.
func TestACancelNeverInterruptsALaterTurn(t *testing.T) {
	f := newFixture(t, "hang")
	s := start(t, f)
	ss := s.(*session)
	first := make(chan driver.PromptResult, 1)
	go func() {
		result, _ := s.Prompt(context.Background(), "one")
		first <- result
	}()
	require.Eventually(t, func() bool {
		ss.mu.Lock()
		defer ss.mu.Unlock()
		return ss.turn != nil
	}, 5*time.Second, 10*time.Millisecond)

	second := make(chan driver.PromptResult, 1)
	ss.beforeCancelWrite = func() {
		// The turn Cancel observed finishes, and another prompt tries to take
		// its place before the interrupt is written.
		ss.mu.Lock()
		t := ss.turn
		ss.mu.Unlock()
		ss.finish(t, driver.PromptResult{Stop: driver.TurnEndTurn}, nil)
		asking := make(chan struct{})
		go func() {
			close(asking)
			result, _ := s.Prompt(context.Background(), "two")
			second <- result
		}()
		// The second prompt is asking to write; whether it may is what this
		// test is about, and nothing here waits on a clock to find out.
		<-asking
	}
	require.NoError(t, s.Cancel(context.Background()))
	<-first

	select {
	case <-second:
	case <-time.After(5 * time.Second):
	}
	// The fake writes its record after it reads each line, so the wire is
	// read until it settles rather than sampled once.
	require.Eventually(t, func() bool {
		r, err := f.report_()
		return err == nil && r.Extra["wire"] == "user control_request user "
	}, 10*time.Second, 50*time.Millisecond,
		"the interrupt follows the turn it was asked for, and never the prompt that came after it")
}

// Review r3: an unsafe mode found before the first turn registers is still a
// failure, not a session that merely ended.
func TestAnUnsafeModeBeforeTheFirstTurnIsStillUnsafe(t *testing.T) {
	f := newFixture(t, "badmode-eager")
	s := start(t, f)
	require.Eventually(t, func() bool {
		select {
		case <-s.Done():
			return true
		default:
			return false
		}
	}, 5*time.Second, 10*time.Millisecond)
	_, err := s.Prompt(context.Background(), "hello")
	assert.ErrorIs(t, err, driver.ErrUnsafeMode, "the reason the session ended, not a bare session-ended")
}

// Card 23's review: a worker that stops reading its input must not be able to
// hold a cancel or a close.
func ss(s driver.Session) *session { return s.(*session) }

func TestAnAgentThatStopsReadingCannotHoldCancelOrClose(t *testing.T) {
	f := newFixture(t, "deaf")
	f.driver.opts.CloseGrace = 300 * time.Millisecond
	s := start(t, f)
	// Enough to fill the pipe, so the write blocks on a worker that reads
	// nothing.
	go func() { _, _ = s.Prompt(context.Background(), strings.Repeat("x", 1<<20)) }()
	// Wait for that prompt to hold the write slot, rather than for a clock.
	require.Eventually(t, func() bool { return len(ss(s).slot) == 1 }, 10*time.Second, 5*time.Millisecond)

	canceled := make(chan error, 1)
	go func() { canceled <- s.Cancel(context.Background()) }()
	select {
	case err := <-canceled:
		assert.Error(t, err, "the cancel gives up rather than waiting on a worker that is not reading")
	case <-time.After(5 * time.Second):
		t.Fatal("Cancel waited on a worker that stopped reading")
	}

	closed := make(chan struct{})
	go func() {
		_ = s.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close waited on a worker that stopped reading")
	}
}

// redactionSecret is the value fed through every error path. It is obviously
// fake, and is planted everywhere a real secret would be: in the worker's
// environment, in its MCP server's environment, in the name of its private
// directory, and in what the agent writes back.
const redactionSecret = "test-token-not-real-c9f2b1"

func redactionFixture(t *testing.T, scenario string) fixture {
	t.Helper()
	f := newFixture(t, scenario)
	private := filepath.Join(t.TempDir(), redactionSecret)
	require.NoError(t, os.Mkdir(private, 0o700))
	f.cfg.PrivateDir = private
	f.cfg.Env = append(f.cfg.Env, "FAKE_CLAUDE_SECRET="+redactionSecret)
	f.cfg.MCPServers[0].Env["BASECAMP_CONNECT_TASK_TOKEN"] = redactionSecret
	f.cfg.Redaction = driver.Redaction{Secrets: []string{redactionSecret}}
	return f
}

// stderrText is everything of a session's stderr a driver would pass on: the
// tail and every bounded line, which is where a refusal written before the
// noise is read (driver's "Refusals").
func stderrText(s driver.Session) []string {
	var out []string
	if tail, ok := s.(interface{ StderrTail() string }); ok {
		out = append(out, tail.StderrTail())
	}
	if lines, ok := s.(interface{ StderrLines() []string }); ok {
		out = append(out, lines.StderrLines()...)
	}
	return out
}

// The redaction rule (driver's redact.go): nothing the driver hands back
// carries the secret, whichever way the session fails.
func TestNoErrorPathCarriesTheSecretOut(t *testing.T) {
	drivertest.RequireRedacted(t, redactionSecret, []drivertest.RedactionPath{
		{Name: "start", Run: func(t *testing.T) drivertest.Crossing {
			f := redactionFixture(t, "ok")
			// A private directory the driver cannot write its MCP config in:
			// the failure names the path, and the path carries the secret.
			require.NoError(t, os.Remove(f.cfg.PrivateDir))
			_, err := f.driver.NewSession(context.Background(), f.cfg)
			require.Error(t, err)
			return drivertest.Crossing{Errors: []error{err}}
		}},
		{Name: "handshake", Run: func(t *testing.T) drivertest.Crossing {
			f := redactionFixture(t, "handshake-secret")
			s := start(t, f)
			result, err := s.Prompt(context.Background(), "hello")
			require.ErrorIs(t, err, driver.ErrUnsafeMode)
			<-s.Done()
			return drivertest.Crossing{Errors: []error{err}, Results: []driver.PromptResult{result},
				Updates: drain(s), Texts: stderrText(s)}
		}},
		{Name: "prompt", Run: func(t *testing.T) drivertest.Crossing {
			f := redactionFixture(t, "denial-secret")
			s := start(t, f)
			result, err := s.Prompt(context.Background(), "hello")
			require.Error(t, err)
			updates := make(chan []driver.Update, 1)
			go func() { updates <- drain(s) }()
			require.NoError(t, s.Close())
			return drivertest.Crossing{Errors: []error{err}, Results: []driver.PromptResult{result},
				Updates: <-updates, Texts: stderrText(s)}
		}},
		{Name: "cancel", Run: func(t *testing.T) drivertest.Crossing {
			f := redactionFixture(t, "deaf-secret")
			f.driver.opts.CloseGrace = 300 * time.Millisecond
			s := start(t, f)
			go func() { _, _ = s.Prompt(context.Background(), strings.Repeat("x", 1<<20)) }()
			require.Eventually(t, func() bool { return len(ss(s).slot) == 1 }, 10*time.Second, 5*time.Millisecond)
			err := s.Cancel(context.Background())
			require.Error(t, err)
			return drivertest.Crossing{Errors: []error{err}, Texts: stderrText(s)}
		}},
		{Name: "close", Run: func(t *testing.T) drivertest.Crossing {
			f := redactionFixture(t, "die-secret")
			s := start(t, f)
			_, err := s.Prompt(context.Background(), "hello")
			require.Error(t, err, "the worker died in the turn")
			closeErr := s.Close()
			after, afterErr := s.Prompt(context.Background(), "again")
			return drivertest.Crossing{Errors: []error{err, closeErr, afterErr}, Results: []driver.PromptResult{after},
				Updates: drain(s), Texts: stderrText(s)}
		}},
	})
}

// drain is every update a closed session emitted.
func drain(s driver.Session) []driver.Update {
	var updates []driver.Update
	for u := range s.Updates() {
		updates = append(updates, u)
	}
	return updates
}

// The refusal rule (driver's "Refusals"): each refusal is recorded once, as
// it is read, whether the result repeats it, announces it late, or never
// comes.
func TestEveryRefusalIsRecordedOnceAsItIsRead(t *testing.T) {
	for _, tc := range []struct {
		scenario string
		want     []driver.Refusal
	}{
		{"ok", []driver.Refusal{{ToolCallID: "toolu_1", Tool: "Bash"}}},
		{"late-denial", []driver.Refusal{{ToolCallID: "toolu_late", Tool: "Bash"}}},
		{"deny-then-die", []driver.Refusal{{ToolCallID: "toolu_dead", Tool: "Bash"}}},
		{"denied-twice", []driver.Refusal{{ToolCallID: "toolu_twice", Tool: "Bash"}}},
		{"two-nameless-refusals", []driver.Refusal{{Tool: "Bash"}, {Tool: "Bash"}}},
		{"nameless-result-denials", []driver.Refusal{{Tool: "Bash"}, {Tool: "Write"}, {Tool: "WebFetch"}}},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			f := newFixture(t, tc.scenario)
			recorder := &drivertest.Refusals{}
			f.cfg.Refusals = recorder
			s := start(t, f)
			go func() {
				for range s.Updates() {
				}
			}()
			result, _ := s.Prompt(context.Background(), "hello")
			require.NoError(t, s.Close())
			assert.Equal(t, tc.want, recorder.Recorded())
			// Copilot: a turn the worker's exit ended still reports what it
			// refused.
			assert.Equal(t, tc.want, result.Refusals)
		})
	}
}

// Card 23's review: a worker whose Basecamp MCP server never connected can
// neither read its dispatch nor report it, so the driver ends the session
// with the sentinel the dispatcher settles as failed.
func TestAnMCPServerThatDidNotConnectIsAnUnverifiedSession(t *testing.T) {
	f := newFixture(t, "mcpfailed")
	s := start(t, f)
	_, err := s.Prompt(context.Background(), "hello")
	assert.ErrorIs(t, err, driver.ErrSessionUnverified)
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a session with no Basecamp tools was left running")
	}
}
