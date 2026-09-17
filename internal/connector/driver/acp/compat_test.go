//go:build acpcompat

package acp

// The adapter-compatibility test: the card 23 spike's four checks, run through
// this driver against the real pinned adapters, and a fifth that the worker's
// own shell sees neither the task token nor the host's token. It sends real prompts, so it
// spends model quota on whatever account each adapter is logged in to, and it
// is skipped unless the adapters are installed:
//
//	make acp-adapters      # npm ci the pinned adapters (once)
//	make test-acp-compat   # the four checks against both
//
// Environment: BASECAMP_ACP_ADAPTERS_DIR (required; the npm prefix),
// BASECAMP_ACP_ADAPTER (one adapter name; both when unset),
// BASECAMP_ACP_CHECKS (e.g. "1,3"; all when unset), and
// BASECAMP_ACP_TRANSCRIPTS (a directory for redacted JSON-RPC transcripts).
//
// No credential is used: check 1's token is a dummy string.

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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
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
		"5": checkShellEnvironment,
	}
	want := strings.Split(envOr("BASECAMP_ACP_CHECKS", "1,2,3,4,5"), ",")
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
					continue
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
	if tdir := os.Getenv("BASECAMP_ACP_TRANSCRIPTS"); tdir != "" {
		if err := os.MkdirAll(tdir, 0o700); err != nil {
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
			_, _ = fmt.Fprintf(f, "{\"t\":%q,\"dir\":%q,\"msg\":%s}\n", time.Now().UTC().Format("15:04:05.000"), dir, driver.Redact(string(line)))
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

	res, err := s.Prompt(turnCtx(t), "Create a file at the absolute path "+refused+" containing the single word NO. Then stop.")
	policy.log(t)
	if err != nil {
		t.Fatalf("refused phase: %v", err)
	}
	if _, err := os.Stat(refused); err == nil {
		t.Fatalf("the policy refused, and the file was written anyway")
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
	command := `sh -c 'if [ -n "$` + compatProbeVar + `" ]; then echo PRESENT; else echo ABSENT; fi > token-probe.txt; ` +
		`printf %s "$` + hostTokenVar + `" | sha256sum | cut -c1-64 > host-probe.txt'`
	res, err := s.Prompt(turnCtx(t), "Run exactly this shell command in the current working directory, once, and then stop: "+command)
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
		sum := sha256.Sum256([]byte(host))
		if strings.TrimSpace(string(digest)) == hex.EncodeToString(sum[:]) {
			t.Errorf("the model's shell sees the host's %s", hostTokenVar)
		}
	}
}
