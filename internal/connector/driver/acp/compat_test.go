//go:build acpcompat

package acp

// The adapter-compatibility test: the card 23 spike's four checks, run through
// this driver against the real pinned adapters; a fifth, that the worker's own
// shell sees neither the task token nor the host's token; and a sixth, that an
// MCP server the working directory declares never runs beside or instead of
// the connector's; and a seventh, that the connector's token bridge reaches
// its one-use socket from where the adapter starts MCP servers, with the
// token in no process's environment or command line and in no file. It sends real prompts, so it
// spends model quota on whatever account each adapter is logged in to, and it
// is skipped unless the adapters are installed:
//
//	make acp-adapters      # npm ci the pinned adapters (once)
//	make test-acp-compat   # the seven checks against both
//
// Environment: BASECAMP_ACP_ADAPTERS_DIR (required; the npm prefix),
// BASECAMP_ACP_ADAPTER (one adapter name; both when unset),
// BASECAMP_ACP_CHECKS (e.g. "1,3"; all when unset), and
// BASECAMP_ACP_TRANSCRIPTS (a directory for redacted JSON-RPC transcripts).
//
// Credentials: the connector's task token is a dummy string throughout. The
// adapters authenticate as whatever account they are logged in to on this
// machine, which is what these prompts are billed to.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/drivertest"
)

const (
	compatProbeVar   = "BASECAMP_CONNECT_TASK_TOKEN"
	compatDummyToken = "test-token-not-real-0000"
	compatServer     = "basecamp"
	// hostTokenVar is a variable the host's Claude Code session carries and
	// no worker may see.
	hostTokenVar = "CLAUDE_CODE_MESSAGING_TOKEN"
)

func TestAdapterCompat(t *testing.T) {
	dir := os.Getenv("BASECAMP_ACP_ADAPTERS_DIR")
	if dir == "" {
		t.Skip("BASECAMP_ACP_ADAPTERS_DIR is not set; run make test-acp-compat")
	}
	stub := buildStub(t)
	checks := map[string]func(*testing.T, compatEnv){
		"1": checkMCPEnv, "2": checkLoadAfterRestart, "3": checkPolicyPermission, "4": checkCancel,
		"5": checkShellEnvironment, "6": checkDecoyMCPServer, "7": checkTokenBridge,
	}
	if only := os.Getenv("BASECAMP_ACP_ADAPTER"); only != "" {
		if _, ok := AdapterNamed(only); !ok {
			t.Fatalf("BASECAMP_ACP_ADAPTER %q names no pinned adapter", only)
		}
	}
	want := strings.Split(envOr("BASECAMP_ACP_CHECKS", "1,2,3,4,5,6,7"), ",")
	for _, adapter := range Adapters() {
		if only := os.Getenv("BASECAMP_ACP_ADAPTER"); only != "" && only != adapter.Name {
			continue
		}
		t.Run(adapter.Name, func(t *testing.T) {
			bin, err := Locate(dir, adapter)
			if errors.Is(err, ErrAdapterMissing) {
				t.Skipf("%v", err)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range want {
				check, ok := checks[strings.TrimSpace(n)]
				if !ok {
					t.Fatalf("BASECAMP_ACP_CHECKS names no check %q", n)
				}
				t.Run("check"+strings.TrimSpace(n), func(t *testing.T) {
					check(t, compatEnv{adapter: adapter, bin: bin, stub: stub, check: strings.TrimSpace(n)})
				})
			}
		})
	}
}

type compatEnv struct {
	adapter Adapter
	bin     string
	stub    string
	check   string
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func buildStub(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "stubmcp")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", out, "./testdata/stubmcp")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build stubmcp: %v", err)
	}
	return out
}

// driverFor builds a driver whose wire goes, redacted, to a transcript.
func (e compatEnv) driverFor(t *testing.T, part string) *Driver {
	t.Helper()
	d, err := New(Options{Adapter: e.adapter, Binary: e.bin, CloseGrace: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	redactor := driver.NewRedactor(driver.Redaction{})
	if tdir := os.Getenv("BASECAMP_ACP_TRANSCRIPTS"); tdir != "" {
		if err := os.MkdirAll(tdir, 0o700); err != nil {
			t.Fatal(err)
		}
		// The transcripts hold prompts, tool text and host paths; only emails
		// and credential-shaped runs are redacted. Owner-only, even when the
		// directory was there before.
		if err := os.Chmod(tdir, 0o700); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("%s-check%s%s.jsonl", e.adapter.Name, e.check, part)
		f, err := os.OpenFile(filepath.Join(tdir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		var mu sync.Mutex
		d.opts.trace = func(dir string, line []byte) {
			mu.Lock()
			defer mu.Unlock()
			// Redacted at the sink: the adapters volunteer the account email.
			_, _ = fmt.Fprintf(f, "{\"t\":%q,\"dir\":%q,\"msg\":%s}\n", time.Now().UTC().Format("15:04:05.000"), dir, redactor.Sanitize(string(line)))
		}
	}
	return d
}

// compatPolicy is the v1 policy's shape with a switch for allowing what lies
// outside the working directory, and a log of what it was asked.
type compatPolicy struct {
	workDir      string
	allowOutside atomic.Bool

	mu    sync.Mutex
	asked []driver.PermissionRequest
}

func (p *compatPolicy) Rules() driver.PermissionRules {
	return driver.PermissionRules{
		Mode: driver.ModeEditsInWorkDir, WorkDir: p.workDir,
		AllowKinds:      []driver.ToolKind{driver.ToolRead, driver.ToolSearch, driver.ToolThink},
		AllowMCPServers: []string{compatServer},
	}
}

func (p *compatPolicy) Decide(_ context.Context, req driver.PermissionRequest) driver.PermissionDecision {
	p.mu.Lock()
	p.asked = append(p.asked, req)
	p.mu.Unlock()
	if strings.HasPrefix(req.Tool, "mcp__"+compatServer+"__") || p.allowOutside.Load() {
		return driver.PermissionDecision{Allow: true}
	}
	inside := len(req.Locations) > 0
	for _, loc := range req.Locations {
		rel, err := filepath.Rel(p.workDir, loc)
		inside = inside && err == nil && !strings.HasPrefix(rel, "..")
	}
	return driver.PermissionDecision{Allow: inside && (req.Kind == driver.ToolEdit || req.Kind == driver.ToolRead)}
}

func (p *compatPolicy) log(t *testing.T) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range p.asked {
		t.Logf("asked: tool=%q kind=%s locations=%d options=%v", r.Tool, r.Kind, len(r.Locations), r.Options)
	}
}

func (e compatEnv) config(t *testing.T, workDir, record string, policy driver.PermissionPolicy) driver.SessionConfig {
	t.Helper()
	serverEnv := driver.EnvMap(driver.BuildEnv(driver.BaseEnv, os.LookupEnv, map[string]string{compatProbeVar: compatDummyToken}))
	return driver.SessionConfig{
		Cwd: workDir,
		Env: driver.BuildEnv(driver.BaseEnv, os.LookupEnv, nil),
		MCPServers: []driver.MCPServer{{
			Name: compatServer, Command: e.stub,
			Args: []string{"--record", record, "--probe", compatProbeVar, "--fingerprint", hostTokenVar},
			Env:  serverEnv,
		}},
		Policy:     policy,
		Scope:      driver.Scope{WorkDir: workDir},
		PrivateDir: t.TempDir(),
	}
}

type stubRecord struct {
	PID          int               `json:"pid"`
	ProbeVars    map[string]string `json:"probe_vars"`
	Fingerprints map[string]string `json:"fingerprints"`
	EnvVarNames  []string          `json:"env_var_names"`
	Methods      []string          `json:"methods"`
	Notes        []string          `json:"notes"`
}

func readRecord(t *testing.T, path string, until func(stubRecord) bool, wait time.Duration) stubRecord {
	t.Helper()
	deadline := time.Now().Add(wait)
	var rec stubRecord
	for {
		if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &rec) == nil && until(rec) {
			return rec
		}
		if time.Now().After(deadline) {
			return rec
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func workDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func outsideTmp(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(cache, "basecamp-acp-compat-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func turnCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// Check 1: mcpServers[].env carries the token to the server, the server
// connects, the host's own token does not reach it, the session is in its
// asking mode, and closing the session ends the server with the adapter.
func checkMCPEnv(t *testing.T, e compatEnv) {
	wd := workDir(t)
	record := filepath.Join(t.TempDir(), "record.json")
	policy := &compatPolicy{workDir: wd}
	d := e.driverFor(t, "")
	s, err := d.NewSession(turnCtx(t), e.config(t, wd, record, policy))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	rec := readRecord(t, record, func(r stubRecord) bool { return slices.Contains(r.Methods, "tools/list") }, 60*time.Second)
	_ = s.Close()

	if got := rec.ProbeVars[compatProbeVar]; got != compatDummyToken {
		t.Errorf("the MCP server did not get %s from mcpServers[].env (got %q)", compatProbeVar, got)
	}
	if !slices.Contains(rec.Methods, "initialize") || !slices.Contains(rec.Methods, "tools/list") {
		t.Errorf("the agent did not complete the MCP handshake: %v", rec.Methods)
	}
	// Claude Code gives every process it starts a messaging token of its own
	// session; what must never arrive is the host's.
	if host, ok := os.LookupEnv(hostTokenVar); ok {
		sum := sha256.Sum256([]byte(host))
		if rec.Fingerprints[hostTokenVar] == hex.EncodeToString(sum[:]) {
			t.Errorf("the host's %s reached the MCP server", hostTokenVar)
		}
	} else {
		t.Logf("%s is not set in this environment; the host-token half of check 1 proves nothing here", hostTokenVar)
	}
	t.Logf("MCP server env: %d variables", len(rec.EnvVarNames))
	if rec.PID > 0 {
		if err := syscall.Kill(rec.PID, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("the MCP server (pid %d) outlived Close: %v", rec.PID, err)
		}
	}
	if !d.Capabilities().PermissionCallback {
		t.Error("the driver does not report the permission callback")
	}
}

// Check 2: the session survives the connector: a fresh adapter process loads
// it by id and it still knows what the first process's turn was told.
func checkLoadAfterRestart(t *testing.T, e compatEnv) {
	wd := workDir(t)
	passphrase := "COMPAT-PASSPHRASE-4417"
	policy := &compatPolicy{workDir: wd}

	first := e.driverFor(t, "a")
	s1, err := first.NewSession(turnCtx(t), e.config(t, wd, filepath.Join(t.TempDir(), "a.json"), policy))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	id := s1.ID()
	res, err := s1.Prompt(turnCtx(t), "Remember this passphrase for later: "+passphrase+". Reply with just the word OK. Do not use any tools.")
	if err != nil || res.Stop != driver.TurnEndTurn {
		_ = s1.Close()
		t.Fatalf("seed prompt: %+v %v", res, err)
	}
	_ = s1.Close()
	if !first.Capabilities().LoadSession {
		t.Fatal("the adapter advertises no session/load or resume")
	}

	second := e.driverFor(t, "b")
	record := filepath.Join(t.TempDir(), "b.json")
	s2, err := second.LoadSession(turnCtx(t), e.config(t, wd, record, policy), id)
	if err != nil {
		t.Fatalf("LoadSession in a fresh process: %v", err)
	}
	defer s2.Close()
	if s2.ID() != id {
		t.Fatalf("loaded session id %q, want %q", s2.ID(), id)
	}
	res, err = s2.Prompt(turnCtx(t), "Call the note tool of the "+compatServer+" MCP server once, with the passphrase I asked you to remember as its text. Then stop.")
	policy.log(t)
	if err != nil {
		t.Fatalf("prompt after load: %v", err)
	}
	rec := readRecord(t, record, func(r stubRecord) bool { return len(r.Notes) > 0 }, 10*time.Second)
	if !slices.ContainsFunc(rec.Notes, func(n string) bool { return strings.Contains(n, passphrase) }) {
		t.Fatalf("the loaded session did not recall the passphrase through the MCP tool (stop %s, %d notes, refusals %v)", res.Stop, len(rec.Notes), res.Refusals)
	}
}

// Check 3: a permission is put to the policy and its answer holds both ways:
// refused, the write does not happen and the turn is not reported canceled;
// allowed, it does.
func checkPolicyPermission(t *testing.T, e compatEnv) {
	wd := workDir(t)
	// Outside means outside /tmp too: codex-acp's modes leave /tmp writable
	// unasked, so a refusal there is never put to the policy.
	outside := outsideTmp(t)
	refused := filepath.Join(outside, "refused.txt")
	allowed := filepath.Join(outside, "allowed.txt")
	policy := &compatPolicy{workDir: wd}
	d := e.driverFor(t, "")
	s, err := d.NewSession(turnCtx(t), e.config(t, wd, filepath.Join(t.TempDir(), "r.json"), policy))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	// A live model may decline to attempt the write at all, which asks the
	// policy nothing and proves nothing; the attempt is what is under test,
	// so it is asked for again before the check gives a verdict.
	var res driver.PromptResult
	for attempt := range 2 {
		ask := "Create a file at the absolute path " + refused + " containing the single word NO. Then stop."
		if attempt > 0 {
			ask = "Try again, and actually attempt the write this time: create a file at the absolute path " + refused +
				" containing the single word NO, then stop. If a permission is refused, stop there."
		}
		res, err = s.Prompt(turnCtx(t), ask)
		policy.log(t)
		if err != nil {
			t.Fatalf("refused phase: %v", err)
		}
		if _, err := os.Stat(refused); err == nil {
			t.Fatalf("the policy refused, and the file was written anyway")
		}
		if len(res.Refusals) > 0 {
			break
		}
		t.Logf("refused phase attempt %d: the agent asked nothing (stop %s)", attempt+1, res.Stop)
	}
	if len(res.Refusals) == 0 {
		t.Fatalf("the agent never asked, or the refusal was not recorded (stop %s)", res.Stop)
	}
	if res.Stop == driver.TurnCanceled {
		t.Fatalf("a policy refusal was reported as a cancel")
	}
	t.Logf("refused phase: stop %s, %d refusals", res.Stop, len(res.Refusals))

	policy.allowOutside.Store(true)
	res, err = s.Prompt(turnCtx(t), "Create a file at the absolute path "+allowed+" containing the single word YES. Then stop.")
	policy.log(t)
	if err != nil {
		t.Fatalf("allowed phase: %v", err)
	}
	if _, err := os.Stat(allowed); err != nil {
		t.Fatalf("the policy allowed, and the file was not written (stop %s, refusals %v)", res.Stop, res.Refusals)
	}
}

// Check 4: session/cancel ends the turn in flight with a canceled stop.
func checkCancel(t *testing.T, e compatEnv) {
	wd := workDir(t)
	policy := &compatPolicy{workDir: wd}
	d := e.driverFor(t, "")
	s, err := d.NewSession(turnCtx(t), e.config(t, wd, filepath.Join(t.TempDir(), "c.json"), policy))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	type answer struct {
		res driver.PromptResult
		err error
	}
	answers := make(chan answer, 1)
	go func() {
		res, err := s.Prompt(turnCtx(t), "Write a very long essay, at least three thousand words, about the history of the typewriter. Do not use any tools.")
		answers <- answer{res, err}
	}()
	select {
	case <-s.Updates():
	case <-time.After(90 * time.Second):
		t.Fatal("no progress within 90s")
	}
	time.Sleep(1500 * time.Millisecond)
	if err := s.Cancel(context.Background()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case a := <-answers:
		if a.err != nil {
			t.Fatalf("the canceled prompt errored: %v", a.err)
		}
		if a.res.Stop != driver.TurnCanceled {
			t.Fatalf("stop %q after session/cancel, want %q", a.res.Stop, driver.TurnCanceled)
		}
	case <-time.After(90 * time.Second):
		t.Fatal("the prompt did not return within 90s of session/cancel")
	}
}

// Check 5: what the MCP server is given stays with the MCP server. The model's
// shell sees neither the task token nor the host's Claude Code token.
func checkShellEnvironment(t *testing.T, e compatEnv) {
	wd := workDir(t)
	policy := &compatPolicy{workDir: wd}
	// The probe is a shell command, which claude-agent-acp asks about.
	policy.allowOutside.Store(true)
	d := e.driverFor(t, "")
	s, err := d.NewSession(turnCtx(t), e.config(t, wd, filepath.Join(t.TempDir(), "s.json"), policy))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()
	// A script, not a one-liner: what is under test is what the worker's
	// shell holds, not how well a model retypes a pipeline.
	script := "#!/bin/sh\n" +
		"if [ -n \"$" + compatProbeVar + "\" ]; then echo PRESENT; else echo ABSENT; fi > token-probe.txt\n" +
		"if command -v sha256sum >/dev/null 2>&1; then H=sha256sum; elif command -v shasum >/dev/null 2>&1; then H=\"shasum -a 256\"; else H=; fi\n" +
		"if [ -n \"$H\" ]; then printf %s \"$" + hostTokenVar + "\" | $H | cut -c1-64 > host-probe.txt; else echo NOHASH > host-probe.txt; fi\n"
	if err := os.WriteFile(filepath.Join(wd, "probe.sh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := s.Prompt(turnCtx(t), "Run `sh probe.sh` in the current working directory, once, and then stop. Do not read or change the script.")
	policy.log(t)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	probe, err := os.ReadFile(filepath.Join(wd, "token-probe.txt"))
	if err != nil {
		t.Fatalf("the probe did not run (stop %s, refusals %v): %v", res.Stop, res.Refusals, err)
	}
	if strings.TrimSpace(string(probe)) != "ABSENT" {
		t.Errorf("the model's shell sees %s", compatProbeVar)
	}
	if host, ok := os.LookupEnv(hostTokenVar); ok {
		digest, err := os.ReadFile(filepath.Join(wd, "host-probe.txt"))
		if err != nil {
			t.Fatalf("the host probe did not run: %v", err)
		}
		seen, err := hostDigestShowsToken(string(digest), host)
		if err != nil {
			t.Fatalf("the host probe proves nothing: %v", err)
		}
		if seen {
			t.Errorf("the model's shell sees the host's %s", hostTokenVar)
		}
	}
}

// Check 6: an MCP server the project declares (Claude's .mcp.json, Codex's
// .codex/config.toml), named like the connector's, never runs. Claude runs
// the session with the connector's server alone; the driver refuses a Codex
// session before anything starts.
func checkDecoyMCPServer(t *testing.T, e compatEnv) {
	wd := workDir(t)
	decoy := filepath.Join(t.TempDir(), "decoy.json")
	record := filepath.Join(t.TempDir(), "real.json")
	claudeDecoy := `{"mcpServers":{"` + compatServer + `":{"type":"stdio","command":"` + e.stub + `","args":["--record","` + decoy + `"]},` +
		`"extra":{"type":"stdio","command":"` + e.stub + `","args":["--record","` + decoy + `"]}}}`
	if err := os.WriteFile(filepath.Join(wd, ".mcp.json"), []byte(claudeDecoy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wd, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	codexDecoy := "[mcp_servers." + compatServer + "]\ncommand = \"" + e.stub + "\"\nargs = [\"--record\", \"" + decoy + "\"]\n"
	if err := os.WriteFile(filepath.Join(wd, ".codex", "config.toml"), []byte(codexDecoy), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := &compatPolicy{workDir: wd}
	d := e.driverFor(t, "")
	s, err := d.NewSession(turnCtx(t), e.config(t, wd, record, policy))
	if e.adapter.Name == CodexACP.Name {
		if !errors.Is(err, ErrForeignMCPConfig) || !errors.Is(err, driver.ErrNotStarted) {
			if s != nil {
				_ = s.Close()
			}
			t.Fatalf("a Codex session with a project MCP server was not refused before it started: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()
	res, err := s.Prompt(turnCtx(t), "Call the note tool of the "+compatServer+" MCP server once, with the text decoy-check. Then stop.")
	policy.log(t)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	rec := readRecord(t, record, func(r stubRecord) bool { return len(r.Notes) > 0 }, 10*time.Second)
	if len(rec.Notes) == 0 {
		t.Errorf("the connector's MCP server was not the one called (stop %s, refusals %v)", res.Stop, res.Refusals)
	}
	if _, err := os.Stat(decoy); err == nil {
		t.Errorf("an MCP server from the working directory's .mcp.json ran")
	}
}

// Check 7: the task token's carriage, as the dispatcher builds it. The MCP
// server is the connector's bridge (`basecamp connect worker-mcp`), the token
// is served once on a socket in the attempt's private directory, and the
// socket is told the worker's process group only once NewSession returns —
// the order the dispatcher uses. The bridge must reach the socket from
// wherever the adapter starts it, the handoff must be delivered, and the
// token must not be in any environment, command line or file of the worker's
// processes. No Basecamp account is involved: the bridge's profile is a dummy
// in a private config, so the `basecamp mcp` it becomes cannot authenticate —
// which is also how this checks that a session whose MCP server did not
// connect is refused rather than run.
func checkTokenBridge(t *testing.T, e compatEnv) {
	if runtime.GOOS != "linux" {
		t.Skip("the process walk reads /proc")
	}
	wd := workDir(t)
	bin := filepath.Join(t.TempDir(), "basecamp")
	build := exec.CommandContext(context.Background(), "go", "build", "-o", bin, "github.com/basecamp/basecamp-cli/cmd/basecamp")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build basecamp: %v", err)
	}
	config := t.TempDir()
	if err := os.MkdirAll(filepath.Join(config, "basecamp"), 0o700); err != nil {
		t.Fatal(err)
	}
	profile := `{"profiles":{"compat-dummy":{"base_url":"https://example.invalid","account_id":"1"}}}`
	if err := os.WriteFile(filepath.Join(config, "basecamp", "config.json"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	private, err := os.MkdirTemp(os.TempDir(), "acp-bridge-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(private) })
	state := t.TempDir()
	token := "test-token-not-real-" + strings.Repeat("b", 23)
	tokens, err := connector.ServeTaskToken(private, token, 2*time.Minute)
	if err != nil {
		t.Fatalf("ServeTaskToken: %v", err)
	}
	defer tokens.Close()

	serverEnv := driver.EnvMap(driver.BuildEnv(driver.BaseEnv, os.LookupEnv, map[string]string{
		"XDG_CONFIG_HOME": config, "BASECAMP_NO_KEYRING": "1",
	}))
	policy := &compatPolicy{workDir: wd}
	cfg := driver.SessionConfig{
		Cwd: wd,
		Env: driver.BuildEnv(driver.BaseEnv, os.LookupEnv, nil),
		MCPServers: []driver.MCPServer{{
			Name: compatServer, Command: bin,
			Args: []string{"connect", "worker-mcp", "--profile", "compat-dummy", "--connect-state", state, "--socket", tokens.Path()},
			Env:  serverEnv,
		}},
		Policy:     policy,
		Scope:      driver.Scope{WorkDir: wd},
		PrivateDir: private,
	}
	d := e.driverFor(t, "")
	var s driver.Session
	var places drivertest.Places
	drivertest.RequireNoSecretFilesDuring(t, token, []string{wd, private, state}, func() {
		started := time.Now()
		s, err = d.NewSession(turnCtx(t), cfg)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		t.Logf("NewSession took %s", time.Since(started).Round(time.Millisecond))
		tokens.AllowGroup(s.Process().PGID)
		handed := make(chan connector.Handoff, 1)
		go func() { handed <- tokens.Result() }()
		deadline := time.After(90 * time.Second)
		for {
			places = addWorkerProcesses(places, s.Process().PID)
			select {
			case h := <-handed:
				if h != connector.HandoffDelivered {
					_ = s.Close()
					t.Fatalf("the bridge did not take the token: %s", h)
				}
				t.Logf("handoff %s %s after NewSession began", h, time.Since(started).Round(time.Millisecond))
				// The bridge execs `basecamp mcp` once it has the token: walk
				// the tree again only when that process is there, so the
				// server that holds the token is among what is checked.
				mcpSeen := false
				for wait := time.Now().Add(30 * time.Second); time.Now().Before(wait); time.Sleep(100 * time.Millisecond) {
					places = addWorkerProcesses(places, s.Process().PID)
					for _, args := range places.Args {
						if strings.Contains(args, " mcp ") && strings.Contains(args, "--connect-token-fd") {
							mcpSeen = true
						}
					}
					if mcpSeen {
						break
					}
				}
				if !mcpSeen {
					_ = s.Close()
					t.Fatal("the bridge never became basecamp mcp")
				}
				// And the agent's own account of the server. The bridge's
				// `basecamp mcp` cannot serve here — its profile is a dummy
				// with no credentials — so the agent reports the server
				// failed, and the driver must refuse to go on with a session
				// whose MCP server did not connect (invariant 8). A session
				// whose server does serve is the live end-to-end proof.
				_, err := s.Prompt(turnCtx(t), "Reply with just the word OK. Do not use any tools.")
				if !errors.Is(err, ErrMCPServerNotConnected) {
					_ = s.Close()
					t.Fatalf("a turn ran with an MCP server that did not connect: %v", err)
				}
				t.Logf("the turn was refused: %v; %d worker processes seen", err, len(places.Args))
				_ = s.Close()
				return
			case <-deadline:
				_ = s.Close()
				t.Fatal("no handoff within 90s")
			case <-time.After(100 * time.Millisecond):
			}
		}
	})
	drivertest.RequireNoSecret(t, token, places)
}

// addWorkerProcesses adds the environment and command line of every process
// descended from root, root included, to places.
func addWorkerProcesses(places drivertest.Places, root int) drivertest.Places {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return places
	}
	parent := map[int]int{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		fields := strings.Fields(string(raw)[strings.LastIndexByte(string(raw), ')')+1:])
		if len(fields) > 1 {
			ppid, _ := strconv.Atoi(fields[1])
			parent[pid] = ppid
		}
	}
	for pid := range parent {
		for p, n := pid, 0; p > 1 && n < 64; p, n = parent[p], n+1 {
			if p != root {
				continue
			}
			dir := "/proc/" + strconv.Itoa(pid)
			if cmdline, err := os.ReadFile(dir + "/cmdline"); err == nil {
				places.Args = append(places.Args, strings.ReplaceAll(string(cmdline), "\x00", " "))
			}
			if environ, err := os.ReadFile(dir + "/environ"); err == nil {
				places.Env = append(places.Env, strings.Split(string(environ), "\x00")...)
			}
			break
		}
	}
	return places
}
