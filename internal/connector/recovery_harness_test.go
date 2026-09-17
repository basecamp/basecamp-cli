//go:build unix

package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
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
// binary, and the fake agent's side of the driver's wire. The fake agent is
// this test binary behind a small exec wrapper, so the dispatcher's
// environment allowlist is never widened for the harness. Whatever the wire,
// the fake agent's work is the same fakeWorker: it binds to its task through
// the MCP server declaration the driver handed it (the state directory and the
// task token) and calls the ledger exactly as `basecamp mcp --connect-state`
// does. Every dispatch test runs once per registered driver.
//
// # What the harness does not cover
//
// A driver row builds its driver directly, where the run command goes through
// spawn.New(connect.json's worker): the registry that maps a worker name to a
// driver is that package's own test's. The fake agent is the driver's binary,
// which is what the registry would otherwise decide.
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
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sessions"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "work"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "work-other"), 0o700))
	sc.Driver = d.Name
	h := &harness{t: t, dir: dir, state: harnessStateDir(t, dir), driver: d, sc: sc}
	h.writeScenario()

	exe, err := os.Executable()
	require.NoError(t, err)
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

func (h *harness) start(r harnessRun) (*exec.Cmd, *lockedBuffer) {
	h.t.Helper()
	if r.Killed && r.Until == "" {
		// A run that is to die runs until it does.
		r.Until = "never"
	}
	if r.StateDir == "" {
		r.StateDir = h.state
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	)
	cmd.Env = append(cmd.Env, r.Env...)
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	require.NoError(h.t, cmd.Start())
	return cmd, out
}

// runUntilLog starts the connector, waits for a line of its log, and kills it:
// the way to watch a connector that is meant to keep running — one holding an
// attempt it cannot verify has nothing left to settle, so no ledger predicate
// can say it is done.
func (h *harness) runUntilLog(r harnessRun, substring string) {
	h.t.Helper()
	r.Until, r.Killed = "never", true
	cmd, out := h.start(r)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := waitFor(ctx, func() (bool, error) { return strings.Contains(out.String(), substring), nil }); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		h.t.Fatalf("the connector never said %q:\n%s", substring, out.String())
	}
	require.NoError(h.t, cmd.Process.Kill())
	h.wait(cmd, out, r)
}

func (h *harness) wait(cmd *exec.Cmd, out *lockedBuffer, r harnessRun) {
	h.t.Helper()
	err := cmd.Wait()
	if path := os.Getenv("BASECAMP_RECOVERY_DEBUG"); path != "" {
		_ = os.WriteFile(path, []byte(out.String()), 0o600)
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

// killAgents ends every fake agent a harness started that is still alive, so
// a failed test leaves nothing behind. It signals by the identity the agent
// recorded — pid, group and start time — through the same call the connector
// uses, so the harness never signals a pid the kernel has since reused.
func (h *harness) killAgents() {
	for _, entry := range h.agentLog() {
		if entry.Step != "start" || entry.PID <= 0 || entry.PGID <= 0 {
			continue
		}
		_, _ = driver.TerminateRecorded(driver.Process{PID: entry.PID, PGID: entry.PGID, StartedAt: entry.StartedAt}, time.Second)
	}
	// A worker's own children, which a group signal cannot reach once the
	// worker that led the group is gone.
	for _, child := range h.children() {
		if owns, err := driver.OwnsWorker(driver.Process{PID: child.PID, PGID: child.PID, StartedAt: child.StartedAt}); err == nil && owns {
			_ = syscall.Kill(child.PID, syscall.SIGKILL)
		}
	}
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
			out = append(out, driver.Process{PID: e.Child, PGID: e.PGID, StartedAt: e.StartedAt})
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
