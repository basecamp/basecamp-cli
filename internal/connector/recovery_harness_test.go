//go:build unix

package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/acp"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/drivertest"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/spawn"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

// The integrated recovery harness (plan step 22).
//
// The connector under test is a real process: this test binary, started as
// TestRecoveryConnector, composed the way the run command composes it —
// intake on the feed's run loop, admission, the dispatcher with a real driver,
// and the outbox with the lifecycle hooks — over a ledger file. A test kills
// that process with SIGKILL at an injected point, starts it again over the
// same ledger, lets it run until the ledger is settled, and asserts on the
// ledger and on what reached the fake Basecamp.
//
// Nothing in the connector's own code knows about the harness, and the harness
// adds no seam to it. The kill points are:
//
//   - after a commit: the connector's own stdout lines are written after the
//     transaction they report, and the harness's line writer kills the process
//     once it has written the line named;
//   - inside a transaction: a ledger hook that kills before returning, so the
//     transaction never commits;
//   - in Basecamp: the fake poster kills before or after the message exists,
//     the fake reads kill mid-read, the fake feed kills in a repair walk;
//   - in the worker: the fake agent kills its parent, the connector, between
//     get_dispatch, ack_dispatch and complete_dispatch;
//   - in shadow promote and import: the named steps their own crash tests
//     already kill at (TestCrashHelper).
//
// # Drivers
//
// Each driver the connector can start workers with registers a row
// (registerHarnessDriver): how to build the driver with a fake agent as its
// binary, and the fake agent's side of the driver's wire. There are three —
// the Claude Code spawn driver, the Codex spawn driver and the acp driver,
// each in its own recovery_<driver>_test.go — and
// TestEveryDriverTheConnectorStartsHasAHarnessRow holds that list to the
// drivers the run command can actually start, so a fourth cannot be added
// without a row. The fake agent is this test binary behind a small exec
// wrapper, so the dispatcher's environment allowlist is never widened for
// the harness. Whatever the wire, the fake agent's work is the same
// fakeWorker: it binds to its task through the MCP server declaration the
// driver handed it (the state directory and the task token) and calls the
// ledger exactly as `basecamp mcp --connect-state` does. Every dispatch test
// runs once per registered driver.
//
// Recovery is where the drivers differ most — each leaves a different
// process tree behind, and each says the worker is gone in its own way — so
// each kill point is worth its three runs. A row costs seconds, not minutes:
// adding the Codex and acp rows took this package from 108 to 134 seconds.
//
// # What the harness does not cover
//
// A driver row builds its driver directly, where the run command goes through
// spawn.New(connect.json's worker): the registry that maps a worker name to a
// driver is that package's own test's. The fake agent is the driver's binary,
// which is what the registry would otherwise decide.
//
// A case about a second prompt on a live session runs only on the drivers
// whose sessions take one (requireFollowUps): a Codex process is one turn,
// and what the dispatcher does with a follow-up it cannot hand over is
// TestAFollowUpForAOneShotDriverStartsATaskOfItsOwn, in dispatcher_test.go.
//
// TestRecoveryAgainstRealAgents runs the Claude Code spawn driver and no
// other: its rows are registered in recovery_claude_test.go, and no real row
// is registered for Codex or for an ACP adapter. So what is proven against
// a real agent binary — a real `basecamp mcp` behind a real MCP client, a
// real model's turn — is proven for Claude Code alone; for the other two,
// what is proven is the connector's side of their wire.
//
// # Synchronization
//
// No test sleeps for an outcome. The connector runs until a predicate over the
// ledger holds; the fake agent waits on ledger state; the parent waits on
// process exit. Every wait has a deadline that fails the test rather than
// hanging it.

// harnessDriver is one row of the driver table.
type harnessDriver struct {
	// Name is the row's name in test names.
	Name string
	// New builds the driver under test with agent as its agent executable.
	New func(agent string) driver.Driver
	// Agent is the fake agent: it speaks the driver's wire on stdin and
	// stdout, binds w to the MCP server declaration it was given, calls
	// w.Turn for each prompt, and returns the process's exit code.
	Agent func(w *fakeWorker) int
	// FollowUps is the driver's driver.Capabilities.FollowUpPrompts: whether
	// a session takes a second prompt, which is what decides whether an event
	// arriving mid-turn is exposed to the worker in hand or waits for a task
	// of its own. TestHarnessRowsSayWhatTheirDriversDo holds it to the
	// driver's own answer.
	FollowUps bool
	// Real rows start the real agent binary with the real `basecamp mcp`:
	// they run only in TestRecoveryAgainstRealAgents, opted into locally.
	Real bool
}

var harnessDrivers []harnessDriver

// registerHarnessDriver adds a driver row. Call it from an init function in
// the driver's own recovery_<driver>_test.go.
func registerHarnessDriver(d harnessDriver) {
	for _, have := range harnessDrivers {
		if have.Name == d.Name {
			panic("recovery harness: driver " + d.Name + " registered twice")
		}
	}
	harnessDrivers = append(harnessDrivers, d)
}

// requireFollowUps skips a case that is about a second prompt on a live
// session, for a driver whose sessions take one. The case is not a weaker
// guarantee on such a driver, it is a different one: the event arriving
// mid-turn waits for a task of its own, and which state it waits in, and for
// how long, is the dispatcher's to decide, not something the worker can be
// scripted around. That path is TestAFollowUpForAOneShotDriverStartsATaskOfItsOwn
// in dispatcher_test.go, against a one-shot driver in this process.
func requireFollowUps(t *testing.T, d harnessDriver) {
	t.Helper()
	if !d.FollowUps {
		t.Skip("a session of this driver takes one prompt: no follow-up is ever handed to a live worker")
	}
}

func harnessDriverNamed(name string) (harnessDriver, bool) {
	for _, d := range harnessDrivers {
		if d.Name == name {
			return d, true
		}
	}
	return harnessDriver{}, false
}

// forEachDriver runs fn as a subtest per registered driver.
func forEachDriver(t *testing.T, fn func(t *testing.T, d harnessDriver)) {
	t.Helper()
	require.NotEmpty(t, harnessDrivers, "no driver registered with the recovery harness")
	for _, d := range harnessDrivers {
		if d.Real {
			continue
		}
		t.Run(d.Name, func(t *testing.T) {
			if testing.Short() {
				t.Skip("starts processes")
			}
			fn(t, d)
		})
	}
}

// The harness runs every driver the connector can start a worker with: each
// spawn driver connect.json's workers resolve to, and the acp driver. A
// driver with no row is a driver whose recovery — the process trees it
// leaves, and what a restart can read back about them — nothing proves.
func TestEveryDriverTheConnectorStartsHasAHarnessRow(t *testing.T) {
	want := map[string]bool{acp.Name: false}
	for _, worker := range setup.Workers {
		d, err := spawn.New(worker, spawn.Options{})
		require.NoError(t, err, worker)
		want[d.Name()] = false
	}
	for _, d := range harnessDrivers {
		if d.Real {
			continue
		}
		if _, ok := want[d.Name]; ok {
			want[d.Name] = true
		}
	}
	for name, has := range want {
		assert.True(t, has, "the recovery harness has no row for the %q driver", name)
	}
}

// A row says what its driver does. FollowUps decides the shape the follow-up
// cases expect, so a row that drifts from its driver would quietly stop
// proving anything about either shape.
func TestHarnessRowsSayWhatTheirDriversDo(t *testing.T) {
	agent := filepath.Join(t.TempDir(), "agent")
	require.NoError(t, os.WriteFile(agent, []byte("#!/bin/sh\nexit 0\n"), 0o700)) //nolint:gosec // an executable stub
	for _, d := range harnessDrivers {
		if d.Real {
			continue
		}
		t.Run(d.Name, func(t *testing.T) {
			built := d.New(agent)
			require.NotNil(t, built, "the row builds its driver")
			assert.Equal(t, d.Name, built.Name())
			assert.Equal(t, d.FollowUps, built.Capabilities().FollowUpPrompts)
		})
	}
}

// raceSubset skips a harness case under the race detector unless it is one of
// the representative few. A race-instrumented test binary takes over a second
// to start, and the harness starts one per connector run and per worker, so
// the whole table runs in the ordinary test job and the race job runs enough
// of it to race-check the composed connector across a kill and a restart.
func raceSubset(t *testing.T, representative bool) {
	t.Helper()
	if harnessUnderRace && !representative {
		t.Skip("under -race the recovery harness runs its representative cases; the full table runs without it")
	}
}

// Environment of the harness's processes.
const (
	harnessConnectorEnv  = "BASECAMP_RECOVERY_CONNECTOR"
	harnessAgentEnv      = "BASECAMP_RECOVERY_AGENT"
	harnessDirEnv        = "BASECAMP_RECOVERY_DIR"
	harnessKillEnv       = "BASECAMP_RECOVERY_KILL"
	harnessUntilEnv      = "BASECAMP_RECOVERY_UNTIL"
	harnessSpawnFailEnv  = "BASECAMP_RECOVERY_SPAWN_FAIL"
	harnessFiltersEnv    = "BASECAMP_RECOVERY_FILTERS"
	harnessStateEnv      = "BASECAMP_RECOVERY_STATE"
	harnessShadowEnv     = "BASECAMP_RECOVERY_SHADOW"
	harnessFaultEnv      = "BASECAMP_RECOVERY_FAULT"
	harnessNoDispatchEnv = "BASECAMP_RECOVERY_NO_DISPATCH"
	harnessNoOutboxEnv   = "BASECAMP_RECOVERY_NO_OUTBOX"
	harnessScanEnv       = "BASECAMP_RECOVERY_SECRET_SCAN"
	// harnessRealEnv opts into the run against the real agent binaries, and
	// harnessRealBasecampEnv names the basecamp binary built from this tree
	// whose `mcp` the real workers start.
	harnessRealEnv         = "BASECAMP_RECOVERY_REAL_AGENTS"
	harnessRealBasecampEnv = "BASECAMP_RECOVERY_BASECAMP"
)

// The test binary doubles as a fake agent: started through the wrapper a
// harness writes, it speaks the wire of the driver it names and exits.
func TestMain(m *testing.M) {
	if name := os.Getenv(harnessAgentEnv); name != "" {
		os.Exit(runFakeAgent(name))
	}
	if os.Getenv(harnessScanEnv) != "" {
		os.Exit(runSecretScan(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func runFakeAgent(name string) int {
	d, ok := harnessDriverNamed(name)
	if !ok {
		fmt.Fprintln(os.Stderr, "recovery harness: no fake agent for driver", name)
		return 97
	}
	w, err := newFakeWorker(os.Getenv(harnessDirEnv))
	if err != nil {
		fmt.Fprintln(os.Stderr, "recovery harness:", err)
		return 98
	}
	defer w.close()
	return d.Agent(w)
}

// Scenario constants: one account, one agent, one operator, one routed project.
const (
	harnessAccount  = "2914079"
	harnessAgent    = adapterAgentID
	harnessOperator = adapterOperatorID
	harnessBucket   = adapterBucketID
	// harnessOtherBucket is a second routed project with its own directory.
	harnessOtherBucket = int64(48929974)
	harnessOrigin      = "https://3.basecampapi.com"
	harnessNamespace   = "basecamp-connect-recovery"
)

// harnessScenario is what every process of one harness reads: the connector,
// the fake agent and the parent.
type harnessScenario struct {
	Driver string `json:"driver"`
	// Plans script the fake worker per event and per time it was prompted
	// with that event: "<event id>#<n>", n from 1. A missing plan is the
	// ordinary worker: get, ack, reply, complete.
	Plans map[string][]string `json:"plans"`
	// BadModeStarts is how many agent processes report a permission mode
	// other than the one asked for.
	BadModeStarts int `json:"bad_mode_starts"`
	// QueueWarn and QueuePause size the backlog; the defaults when zero.
	QueueWarn  int `json:"queue_warn"`
	QueuePause int `json:"queue_pause"`
	// ReadGate names recordings whose admission read waits for the parent.
	ReadGate []int64 `json:"read_gate"`
	// GuardDelay is how long a worker has to call get_dispatch before the
	// guard acknowledges; an hour when zero, so no guard fires in a test that
	// is not about it.
	GuardDelay time.Duration `json:"guard_delay"`
	// RepairWindow is the loss window; a minute when zero.
	RepairWindow time.Duration `json:"repair_window"`
	// OverflowLosses is how many losses the scenario's overflow records: the
	// feed's catch-up and the repair walks wait for all of them.
	OverflowLosses int `json:"overflow_losses"`
}

// harness is one scenario's directory: the connector's state directory, the
// fake Basecamp, the feed, and every process's log.
type harness struct {
	t   *testing.T
	dir string
	// watching is the parent's watch for each task token, for the run in
	// flight; watchStop ends it.
	watchMu   sync.Mutex
	watching  map[string]func() []string
	watchStop chan struct{}
	// state is the connector's state directory, under this harness's own
	// XDG_STATE_HOME and named as the connector names it, so a worker's MCP
	// server resolves it exactly as `basecamp mcp --connect-state` does.
	state  string
	agent  string
	driver harnessDriver
	sc     harnessScenario
}

func newHarness(t *testing.T, d harnessDriver, sc harnessScenario) *harness {
	t.Helper()
	// Not t.TempDir: its name carries the test's, and the attempt's token
	// socket lives under it — a unix socket path is 103 characters, and the
	// connector refuses a longer one.
	dir, err := os.MkdirTemp("", "bcrh")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	require.NoError(t, os.Chmod(dir, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sessions"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "work"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "work-other"), 0o700))
	sc.Driver = d.Name
	h := &harness{t: t, dir: dir, state: harnessStateDir(t, dir), driver: d, sc: sc}
	h.writeScenario()

	exe, exeErr := os.Executable()
	require.NoError(t, exeErr)
	h.agent = filepath.Join(dir, "agent")
	wrapper := "#!/bin/sh\n" +
		harnessAgentEnv + "=" + shellQuote(d.Name) + " " + harnessDirEnv + "=" + shellQuote(dir) + " exec " + shellQuote(exe) + ` "$@"` + "\n"
	require.NoError(t, os.WriteFile(h.agent, []byte(wrapper), 0o700)) //nolint:gosec // the fake agent's wrapper must be executable
	for _, name := range []string{feedFile, storeFile, linesFile, pollsFile, agentLogFile, liveFile, workspaceFile} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
	}
	t.Cleanup(h.killAgents)
	return h
}

// harnessStateDir is the connector's state directory for this harness:
// <dir>/state/basecamp/connect/<account>-<agent>, which is what
// connector.StateRoot resolves to with XDG_STATE_HOME set to <dir>/state.
// The location and the name are both part of what a worker's MCP server
// checks, so the harness's directory is the real shape, not a temp name.
func harnessStateDir(t *testing.T, dir string) string {
	t.Helper()
	state := filepath.Join(dir, "state", "basecamp", "connect", StateDirName(harnessAccount, harnessAgent))
	require.NoError(t, os.MkdirAll(state, 0o700))
	for d := state; d != dir; d = filepath.Dir(d) {
		require.NoError(t, os.Chmod(d, 0o700))
	}
	return state
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (h *harness) writeScenario() {
	data, err := json.Marshal(h.sc)
	require.NoError(h.t, err)
	require.NoError(h.t, os.WriteFile(filepath.Join(h.dir, scenarioFile), data, 0o600))
}

// Files in a harness directory.
const (
	scenarioFile  = "scenario.json"
	feedFile      = "feed.jsonl"
	storeFile     = "basecamp.jsonl"
	linesFile     = "lines.jsonl"
	pollsFile     = "polls.jsonl"
	agentLogFile  = "agent.jsonl"
	liveFile      = "live.jsonl"
	workspaceFile = "workspaces.jsonl"
	// tokensDir holds the task tokens the fake workers were handed, so the
	// parent can look for them everywhere a token must not be.
	tokensDir = "tokens"
	// connectorFile is the running connector's own identity: the pid a fake
	// worker kills, so no process this harness did not start is signaled.
	connectorFile = "connector.json"
)

func readScenario(dir string) (harnessScenario, error) {
	var sc harnessScenario
	data, err := os.ReadFile(filepath.Join(dir, scenarioFile))
	if err != nil {
		return sc, err
	}
	return sc, json.Unmarshal(data, &sc)
}

// harnessRun is one start of the connector.
type harnessRun struct {
	// Kill names where the connector kills itself; empty runs it to Until.
	Kill string
	// Until is the ledger predicate a surviving run stops at: "settled"
	// when empty.
	Until string
	// SpawnFail is how many of this run's worker starts fail before any
	// process exists.
	SpawnFail int
	// Filters is the feed filter set, as JSON; none when empty.
	Filters string
	// Killed says the run is expected to die by SIGKILL, from the connector
	// itself or from the fake agent.
	Killed bool
	// StateDir is the connector's state directory; the harness's own
	// (harness.state) when empty.
	StateDir string
	// Fault is a standing misbehavior of the fake Basecamp for the run:
	// "stall-catch-up" holds the feed's first poll until the socket has
	// served every live event; "repair-stall" never answers a repair poll.
	Fault string
	// Env is added to the connector's environment.
	Env []string
	// NoDispatch runs intake, admission and hooks but no dispatcher or
	// outbox: a connector that dies after admitting, before its dispatcher
	// could have seen the record, without racing one that might.
	NoDispatch bool
	// NoOutbox runs the dispatcher without the outbox: a connector that dies
	// after settling an attempt, before anything could have claimed its
	// notice.
	NoOutbox bool
	// RequireLog is a line the connector must have written by the end of the
	// run: what it decided, where the ledger cannot show that it decided
	// anything (a held attempt is indistinguishable from one recovery never
	// looked at).
	RequireLog string
	// Shadow runs intake and admission only, and installs no hooks: a
	// `--shadow` run.
	Shadow bool
}

// run starts the connector and waits for it to end as expected.
func (h *harness) run(r harnessRun) {
	h.t.Helper()
	cmd, out := h.start(r)
	h.wait(cmd, out, r)
}

// defaults fills a run in the same way for whoever starts it and whoever
// waits for it.
func (h *harness) defaults(r harnessRun) harnessRun {
	if r.Killed && r.Until == "" {
		// A run that is to die runs until it does.
		r.Until = "never"
	}
	if r.StateDir == "" {
		r.StateDir = h.state
	}
	return r
}

func (h *harness) start(r harnessRun) (*exec.Cmd, *lockedBuffer) {
	h.t.Helper()
	r = h.defaults(r)
	// Longer than any run's own deadline (runHarnessConnector's runFor), so
	// a connector that overruns fails saying the ledger never got there
	// rather than being killed by this timeout — which wait would otherwise
	// be unable to tell from the kill a row asked for.
	ctx, cancel := context.WithTimeout(context.Background(), harnessRunCap)
	h.t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecoveryConnector$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(),
		harnessConnectorEnv+"=1",
		harnessDirEnv+"="+h.dir,
		harnessKillEnv+"="+r.Kill,
		harnessUntilEnv+"="+r.Until,
		harnessSpawnFailEnv+"="+strconv.Itoa(r.SpawnFail),
		harnessFiltersEnv+"="+r.Filters,
		harnessStateEnv+"="+r.StateDir,
		"XDG_STATE_HOME="+filepath.Join(h.dir, "state"),
		harnessShadowEnv+"="+strconv.FormatBool(r.Shadow),
		harnessFaultEnv+"="+r.Fault,
		harnessNoDispatchEnv+"="+strconv.FormatBool(r.NoDispatch),
		harnessNoOutboxEnv+"="+strconv.FormatBool(r.NoOutbox),
	)
	cmd.Env = append(cmd.Env, r.Env...)
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	require.NoError(h.t, cmd.Start())
	h.watchForTokenFiles()
	return cmd, out
}

func (h *harness) wait(cmd *exec.Cmd, out *lockedBuffer, r harnessRun) {
	h.t.Helper()
	r = h.defaults(r)
	started := time.Now()
	err := cmd.Wait()
	// exec.CommandContext kills with SIGKILL as well, and wait must not read
	// that as the kill a row asked for.
	require.Less(h.t, time.Since(started), harnessRunCap, "the connector outran the harness's own deadline\n%s", out.String())
	defer h.requireNoTaskTokenLeaked(out, r.StateDir)
	if path := os.Getenv("BASECAMP_RECOVERY_DEBUG"); path != "" {
		// Appended: a test is several runs, and the one that matters is
		// rarely the last.
		if f, openErr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); openErr == nil {
			_, _ = f.WriteString(out.String())
			_ = f.Close()
		}
	}
	if r.Killed {
		var exit *exec.ExitError
		require.True(h.t, errors.As(err, &exit), "the connector must die (kill %q): %v\n%s", r.Kill, err, out.String())
		status, ok := exit.Sys().(syscall.WaitStatus)
		require.True(h.t, ok)
		require.True(h.t, status.Signaled() && status.Signal() == syscall.SIGKILL, "killed at %q, got %v\n%s", r.Kill, err, out.String())
		return
	}
	require.NoError(h.t, err, "the connector must run to %q and stop cleanly\n%s", r.Until, out.String())
	if r.RequireLog != "" {
		require.Contains(h.t, out.String(), r.RequireLog, "the connector said what it decided")
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// identityOf is a process the agent log recorded, as the one-owner rule
// takes it. The stamp written there is the kernel's (kernelStart) or nothing
// at all, exactly as the ledger writes a worker's, so the rule can tell the
// process recorded from a later one the kernel gave the same pid — and
// without that bit it would answer neither, and the harness would signal
// nothing it started.
func identityOf(pid, pgid int, started time.Time) driver.Process {
	return driver.Process{PID: pid, PGID: pgid, StartedAt: started, StartedExact: !started.IsZero()}
}

// killAgents ends every fake agent a harness started that is still alive, so
// a failed test leaves nothing behind. It signals by the identity the agent
// recorded — pid, group and start time — through the same call the connector
// uses, so the harness never signals a pid the kernel has since reused.
func (h *harness) killAgents() {
	for _, entry := range h.agentLog() {
		if entry.Step != "start" || entry.PID <= 0 || entry.PGID <= 0 {
			continue
		}
		_, _ = driver.TerminateRecorded(identityOf(entry.PID, entry.PGID, entry.StartedAt), time.Second)
	}
	// A worker's own children, which a group signal cannot reach once the
	// worker that led the group is gone.
	for _, child := range h.children() {
		if owns, err := driver.OwnsWorker(identityOf(child.PID, child.PID, child.StartedAt)); err == nil && owns {
			_ = syscall.Kill(child.PID, syscall.SIGKILL)
		}
	}
}

// watchForTokenFiles watches, for the rest of this run, every place a task
// token must never be written, for every token a worker takes while it runs.
// The workers watch too, but a worker the connector ends never reports; this
// watcher is the parent's, and always does.
func (h *harness) watchForTokenFiles() {
	h.t.Helper()
	h.watchMu.Lock()
	defer h.watchMu.Unlock()
	if h.watching == nil {
		h.watching = map[string]func() []string{}
	}
	stop := make(chan struct{})
	h.watchStop = stop
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			for _, token := range h.knownTokens() {
				h.watchMu.Lock()
				if _, ok := h.watching[token]; !ok {
					h.watching[token] = drivertest.WatchForSecretFiles(token,
						h.workDir(), filepath.Join(h.dir, "work-other"), filepath.Join(h.dir, "sessions"))
				}
				h.watchMu.Unlock()
			}
		}
	}()
}

// knownTokens reads the tokens the workers have taken so far, ignoring a
// directory that does not exist yet.
func (h *harness) knownTokens() []string {
	entries, err := os.ReadDir(filepath.Join(h.dir, tokensDir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if token, err := os.ReadFile(filepath.Join(h.dir, tokensDir, e.Name())); err == nil && len(token) > 0 {
			out = append(out, string(token))
		}
	}
	return out
}

// stopWatchingForTokenFiles ends the watchers and reports what they saw.
func (h *harness) stopWatchingForTokenFiles() int {
	h.watchMu.Lock()
	defer h.watchMu.Unlock()
	if h.watchStop != nil {
		close(h.watchStop)
		h.watchStop = nil
	}
	watched := len(h.watching)
	for token, stop := range h.watching {
		for _, found := range stop() {
			h.t.Errorf("a task token was written to %s while the connector ran", found)
		}
		delete(h.watching, token)
	}
	return watched
}

// requireNoTaskTokenLeaked holds every run to the credential rule, for every
// task token any worker was handed so far: not in an agent's argv or
// environment, not in anything the connector wrote (its stdout lines, its
// log, the lifecycle messages it posted, the polls it made, the workspace
// records), not in any file under a working directory or the state directory
// — and no worker saw one appear in those files while it ran.
//
// It runs when the connector has exited, and the workers' side of it is read
// once the workers have said their word (awaitWorkersWord): a worker's word
// is due by its own exit, not by the connector's. The watch for a token in a
// file runs until then too, since a worker still running is a worker that
// can still write one.
func (h *harness) requireNoTaskTokenLeaked(out *lockedBuffer, stateDir string) {
	t := h.t
	t.Helper()
	log := h.awaitWorkersWord(stateDir)
	watched := h.stopWatchingForTokenFiles()
	tokens := h.taskTokens()
	// A worker that bound to its task took a token, and the harness kept it.
	// If it did not, this check has nothing to look for, and says so rather
	// than passing.
	started, bound, unbound := 0, 0, 0
	var places drivertest.Places
	for _, e := range log {
		places.Env = append(places.Env, e.Env...)
		places.Args = append(places.Args, e.Args...)
		switch {
		case e.Step == "start":
			started++
		case e.Step == "bound":
			bound++
		case strings.HasPrefix(e.Step, "bind-failed:"):
			unbound++
		case strings.HasPrefix(e.Step, "secret-file:"):
			t.Errorf("a worker saw a task token written to %s", strings.TrimPrefix(e.Step, "secret-file:"))
		case strings.HasPrefix(e.Step, "secret-declared:"):
			t.Errorf("a worker's MCP server declaration carried the task token in %s", strings.TrimPrefix(e.Step, "secret-declared:"))
		}
	}
	// Every task the connector launched minted a token and is checked here,
	// whoever its worker was — a fake one, or a real agent through the
	// bridge, which leaves no agent log at all.
	require.Len(t, tokens, h.tasksLaunched(stateDir), "every task the connector launched left its token for this check")
	require.Equal(t, started, bound+unbound,
		"every worker that started either took its task's token or said why it could not")
	for _, token := range h.takenTokens() {
		require.Contains(t, tokens, token, "a worker took a token the connector did not mint for its task")
	}
	if len(tokens) == 0 {
		require.Zero(t, watched, "nothing was watched, because no task was launched")
		// Nothing to check is a fact about the run, not a pass: a run with
		// no worker (a kill before the spawn, a start that ran nothing) is
		// the only way here.
		require.Equal(t, started, unbound,
			"a worker that started either took a token or said why it could not")
		return
	}
	places.Texts = append(places.Texts, out.String())
	for _, name := range []string{linesFile, storeFile, pollsFile, workspaceFile, agentLogFile} {
		data, err := os.ReadFile(filepath.Join(h.dir, name))
		require.NoError(t, err)
		places.Texts = append(places.Texts, string(data))
	}
	files := 0
	for _, token := range tokens {
		// Env, argv and everything the connector wrote, in this process.
		drivertest.RequireNoSecret(t, token, places)
		// Every file under the working, session and state directories, read
		// by a process of its own, which reports what it could not read. The
		// state directory holds a ledger this test may have open.
		found, read := scanForSecret(t, token, h.workDir(), filepath.Join(h.dir, "work-other"),
			filepath.Join(h.dir, "sessions"), filepath.Join(h.dir, "state"))
		for _, path := range found {
			t.Errorf("a task token is in a file: %s", path)
		}
		require.Positive(t, read, "the scan read files; a scan that read nothing has cleared nothing")
		files += read
	}
	t.Logf("credential check: %d task tokens, %d files read, %d watched while the run went on; %d workers started, %d bound, %d said why not",
		len(tokens), files, watched, started, bound, unbound)
}

// taskTokens is every token the connector minted for a task, as its launch
// hook recorded it.
func (h *harness) taskTokens() []string { return h.tokenFiles("*.token", "taken-") }

// takenTokens is every token a fake worker took over the socket.
func (h *harness) takenTokens() []string { return h.tokenFiles("taken-*.token", "") }

func (h *harness) tokenFiles(pattern, exclude string) []string {
	h.t.Helper()
	paths, err := filepath.Glob(filepath.Join(h.dir, tokensDir, pattern))
	require.NoError(h.t, err)
	var out []string
	for _, path := range paths {
		if exclude != "" && strings.HasPrefix(filepath.Base(path), exclude) {
			continue
		}
		token, err := os.ReadFile(path)
		require.NoError(h.t, err)
		require.NotEmpty(h.t, token)
		out = append(out, string(token))
	}
	return out
}

// tasksLaunched is how many tasks the ledger in dir says were launched, each
// with a token of its own. The ledger is read as it is: a run that made none
// (a shadow, a crash before the first launch) leaves none to open, and this
// must not be what creates one.
func (h *harness) tasksLaunched(dir string) int {
	h.t.Helper()
	ctx := context.Background()
	l, err := OpenLedgerReadOnly(ctx, filepath.Join(dir, LedgerFile))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	require.NoError(h.t, err)
	defer func() { _ = l.Close() }()
	var n int
	require.NoError(h.t, l.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&n))
	return n
}

// awaitWorkersWord waits for every worker of the run to have said what became
// of its token — "bound", or "bind-failed:" and why — and returns the agent
// log once it has. The word is the worker's, written from its own process at
// its own pace, and it is owed by the time the worker exits: not by the time
// the connector does, which is when the credential check runs.
//
// The two are not one moment. A crash row kills the connector at its
// "running" line, written once the socket is armed and the attempt recorded,
// while the handoff itself is still in flight on the socket's own goroutine.
// The acp row's worker began the bind that dials it as the last act of its
// handshake, since the socket cannot be armed before NewSession returns; it
// learns of the death as a reset on the socket and says so a fraction of a
// millisecond after the parent has seen the connector exit. Read at the exit,
// the log counted that worker as one that said nothing (card 10317728733).
// The spawn rows' workers have not even reached their agent by then — the
// connector dies a millisecond after starting them — so read at the exit, the
// check there ran over nobody.
//
// The workers owed a word are the processes the ledger recorded for the run's
// attempts and the ones that reached their agent, each by its identity — pid
// and kernel start time — so a pid the kernel has since given to a stranger
// is not waited on as if it were the worker. A real agent row leaves no
// agent log and is owed none. Both halves are held: a worker still alive
// without a word when the wait runs out fails, and so does one that is gone
// without ever saying it — a process that could have been handed a token
// and left no account of it is not a pass.
func (h *harness) awaitWorkersWord(stateDir string) []agentLogEntry {
	t := h.t
	t.Helper()
	var recorded []driver.Process
	if !h.driver.Real {
		recorded = h.recordedWorkerProcesses(stateDir)
	}
	var log []agentLogEntry
	var alive []int
	err := waitFor(context.Background(), func() (bool, error) {
		log = h.agentLog()
		alive = alive[:0]
		for _, p := range workersOwingAWord(log, recorded) {
			gone, err := driver.ProcessGone(p)
			if err != nil {
				return false, fmt.Errorf("worker pid %d: %w", p.PID, err)
			}
			if !gone {
				alive = append(alive, p.PID)
			}
		}
		if len(alive) > 0 {
			return false, nil
		}
		// Whoever still owes a word is gone, and a process that is gone has
		// written all it ever will: this reading is the one to count.
		log = h.agentLog()
		return true, nil
	})
	require.NoError(t, err, "a worker still owes its word after the connector exited: pids %v", alive)
	owing := workersOwingAWord(log, recorded)
	silent := make([]int, 0, len(owing))
	for _, p := range owing {
		silent = append(silent, p.PID)
	}
	require.Empty(t, silent, "a worker is gone without saying whether it took its task's token: pids %v", silent)
	return log
}

// workerIdentity is a worker as the log and the ledger both name it: its pid
// and the kernel's start time for it, which is what tells it from a later
// process the kernel gave the same pid. Both write the same instant — the
// ledger from the process the driver started, the fake from its own — so
// one worker's entries match each other and nobody else's.
type workerIdentity struct {
	pid     int
	started int64
}

func identityKey(p driver.Process) workerIdentity {
	return workerIdentity{pid: p.PID, started: p.StartedAt.UnixNano()}
}

// workersOwingAWord is every worker, recorded by the ledger or started as far
// as its agent, whose word is not in log — by its identity, so it can be
// waited on and told from a later process with its pid, and so a word an
// earlier worker said cannot discharge a later one the kernel named alike:
// the log and the ledger both accumulate across a harness's connector runs.
func workersOwingAWord(log []agentLogEntry, recorded []driver.Process) []driver.Process {
	said := map[workerIdentity]bool{}
	for _, e := range log {
		if e.Step == "bound" || strings.HasPrefix(e.Step, "bind-failed:") {
			said[identityKey(identityOf(e.PID, e.PGID, e.StartedAt))] = true
		}
	}
	var owing []driver.Process
	seen := map[workerIdentity]bool{}
	owe := func(p driver.Process) {
		key := identityKey(p)
		if p.PID > 0 && !said[key] && !seen[key] {
			seen[key] = true
			owing = append(owing, p)
		}
	}
	for _, p := range recorded {
		owe(p)
	}
	for _, e := range log {
		if e.Step == "start" {
			owe(identityOf(e.PID, e.PGID, e.StartedAt))
		}
	}
	return owing
}

// recordedWorkerProcesses is every worker process the ledger in dir recorded
// for an attempt, with the identity it recorded, read as tasksLaunched reads
// the ledger: as it is, or not at all.
func (h *harness) recordedWorkerProcesses(dir string) []driver.Process {
	h.t.Helper()
	ctx := context.Background()
	l, err := OpenLedgerReadOnly(ctx, filepath.Join(dir, LedgerFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(h.t, err)
	defer func() { _ = l.Close() }()
	var out []driver.Process
	for _, a := range recordedAttempts(h.t, l) {
		out = append(out, a.process)
	}
	return out
}

// scanForSecret reads every file under dirs, in a process of its own, and
// reports what it found and how much it read. A scan that could not read
// something says so, and the caller fails on it: a check that skips is not a
// check that passed.
//
// A process of its own because reading a SQLite database's files by another
// descriptor in a process that holds the database open drops SQLite's POSIX
// advisory locks on them; another process closing the database then resets
// the WAL under the held handle, which reads stale or fails. The secret goes
// over stdin, never argv.
func scanForSecret(t *testing.T, secret string, dirs ...string) (found []string, read int) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), os.Args[0], dirs...)
	cmd.Env = append(os.Environ(), harnessScanEnv+"=1")
	cmd.Stdin = strings.NewReader(secret)
	out, err := cmd.Output()
	require.NoError(t, err, "the secret scan ran")
	read = -1
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		kind, rest, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		switch kind {
		case "found":
			found = append(found, rest)
		case "unreadable":
			t.Errorf("the token scan could not read %s, so it cleared nothing there", rest)
		case "read":
			n, convErr := strconv.Atoi(rest)
			require.NoError(t, convErr)
			read = n
		}
	}
	require.GreaterOrEqual(t, read, 0, "the scan reported what it read")
	return found, read
}

// runSecretScan is the scanning process: the secret on stdin, the directories
// as arguments, a path per line for every file that contains the secret.
func runSecretScan(dirs []string) int {
	secret, err := io.ReadAll(os.Stdin)
	if err != nil || len(secret) == 0 {
		return 2
	}
	read := 0
	for _, dir := range dirs {
		if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			switch {
			case errors.Is(err, fs.ErrNotExist):
				// It was there when the directory was read and gone when the
				// walk reached it. The parent's watcher is what covers a file
				// that only exists for a moment.
				return nil
			case err != nil:
				// A place the scan could not look is not a place it has
				// cleared: the caller is told, and fails. The walk goes on,
				// so one unreadable entry does not hide the rest.
				fmt.Println("unreadable\t" + path + ": " + err.Error())
				return nil //nolint:nilerr // reported to the caller, which fails on it
			case d.Type().IsRegular():
				data, err := os.ReadFile(path)
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				} else if err != nil {
					fmt.Println("unreadable\t" + path + ": " + err.Error())
					return nil //nolint:nilerr // reported to the caller, which fails on it
				}
				read++
				if bytes.Contains(data, secret) {
					fmt.Println("found\t" + path)
				}
			}
			return nil
		}); err != nil {
			fmt.Println("unreadable\t" + dir + ": " + err.Error())
		}
	}
	fmt.Println("read\t" + strconv.Itoa(read))
	return 0
}

// workspaces is every preparation and release of a task's working directory.
func (h *harness) workspaces() []workspaceEvent {
	h.t.Helper()
	var out []workspaceEvent
	require.NoError(h.t, readJSONLines(filepath.Join(h.dir, workspaceFile), func(line []byte) error {
		var e workspaceEvent
		if err := json.Unmarshal(line, &e); err != nil {
			return err
		}
		out = append(out, e)
		return nil
	}))
	return out
}

// releasedDir says a task's working directory was handed back, which the
// one-owner rule allows only once its worker's process group is gone.
func (h *harness) releasedDir(dir string) bool {
	for _, e := range h.workspaces() {
		if e.Step == "finish" && e.WorkDir == dir {
			return true
		}
	}
	return false
}

// workDir is the first routed project's working directory.
func (h *harness) workDir() string { return filepath.Join(h.dir, "work") }

// children is every process a fake worker started of its own, with the time
// it started, so it can be signaled only while it is still that process.
func (h *harness) children() []driver.Process {
	var out []driver.Process
	for _, e := range h.agentLog() {
		if e.Step == "grandchild" && e.Child > 0 {
			out = append(out, identityOf(e.Child, e.PGID, e.ChildStartedAt))
		}
	}
	return out
}

// ledger opens the harness's ledger. The connector need not be stopped.
func (h *harness) ledger() *Ledger {
	h.t.Helper()
	l, err := OpenLedger(filepath.Join(h.state, LedgerFile))
	require.NoError(h.t, err)
	h.t.Cleanup(func() { _ = l.Close() })
	return l
}
