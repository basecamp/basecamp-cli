//go:build unix

package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed/feedtest"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/ndjson"
)

// The connector process the recovery harness starts and kills: its kill points,
// its composition, and the ledger predicates a surviving run stops at.

// killSpec is a run's kill point: "<point>", or "<point>#<n>" to kill at the
// n-th time the point is reached rather than the first. Line points are
// "line:<type>:<state>".
type killSpec struct {
	point string
	nth   int

	mu    sync.Mutex
	count int
}

func parseKill(raw string) *killSpec {
	if raw == "" {
		return &killSpec{}
	}
	k := &killSpec{point: raw, nth: 1}
	if i := strings.LastIndex(raw, "#"); i > 0 {
		if n, err := strconv.Atoi(raw[i+1:]); err == nil {
			k.point, k.nth = raw[:i], n
		}
	}
	return k
}

// at reports whether this is the time point is to kill.
func (k *killSpec) at(point string) bool {
	if k == nil || k.point != point {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.count++
	return k.count == k.nth
}

// die is SIGKILL, the one death nothing in the process can intercept.
func die() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	select {}
}

// killingLines is the connector's stdout: every line is kept for the parent,
// and the named line is the last thing the process does.
type killingLines struct {
	mu   sync.Mutex
	f    *os.File
	kill *killSpec
}

func (w *killingLines) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.f.Write(p)
	if err != nil {
		return n, err
	}
	var line struct {
		Type  string `json:"type"`
		State string `json:"state"`
	}
	if json.Unmarshal(p, &line) == nil {
		if line.Type == "" {
			line.Type = "pointer"
		}
		if w.kill.at("line:" + line.Type + ":" + line.State) {
			die()
		}
	}
	return n, nil
}

// harnessLine is one connector stdout line, as the parent reads it back.
type harnessLine struct {
	Type       string  `json:"type"`
	State      string  `json:"state"`
	EventID    int64   `json:"event_id"`
	TaskID     int64   `json:"task_id"`
	AttemptID  string  `json:"attempt_id"`
	EventIDs   []int64 `json:"event_ids"`
	StopReason string  `json:"stop_reason"`
	Kind       string  `json:"kind"`
	IntentID   int64   `json:"intent_id"`
}

func (h *harness) lines() []harnessLine {
	h.t.Helper()
	var out []harnessLine
	require.NoError(h.t, readJSONLines(filepath.Join(h.dir, linesFile), func(line []byte) error {
		var l harnessLine
		if err := json.Unmarshal(line, &l); err != nil {
			return err
		}
		out = append(out, l)
		return nil
	}))
	return out
}

// ---- the connector process ----

// TestRecoveryConnector is not a test: it is the connector the harness starts
// and kills.
func TestRecoveryConnector(t *testing.T) {
	if os.Getenv(harnessConnectorEnv) == "" {
		t.Skip("started by the recovery harness")
	}
	dir := os.Getenv(harnessDirEnv)
	if err := runHarnessConnector(dir); err != nil {
		t.Fatal(err)
	}
}

func runHarnessConnector(dir string) error {
	sc, err := readScenario(dir)
	if err != nil {
		return err
	}
	d, ok := harnessDriverNamed(sc.Driver)
	if !ok {
		return fmt.Errorf("no driver %q registered", sc.Driver)
	}
	kill := parseKill(os.Getenv(harnessKillEnv))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	stateDir := os.Getenv(harnessStateEnv)
	if stateDir == "" {
		stateDir = dir
	}
	shadow := os.Getenv(harnessShadowEnv) == "true"

	// One connector per account and agent, as the run command takes it.
	lock, err := AcquireInstanceLock(stateDir, harnessAccount, harnessAgent, time.Now())
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	ledger, err := OpenLedger(filepath.Join(stateDir, LedgerFile))
	if err != nil {
		return err
	}
	defer func() { _ = ledger.Close() }()
	// The guard is an hour out unless a scenario asks for it: a test that is
	// not about the guard must not have one fire in the middle of it.
	guardDelay := sc.GuardDelay
	if guardDelay <= 0 {
		guardDelay = time.Hour
	}
	hooks := LifecycleHooks(ledger, LifecycleOptions{GuardDelay: guardDelay})
	ended := hooks.AttemptEnded
	hooks.AttemptEnded = func(ctx context.Context, tx Tx, s Settlement) error {
		if err := ended(ctx, tx, s); err != nil {
			return err
		}
		if kill.at("tx:attempt-ended") {
			die()
		}
		return nil
	}
	verdict := hooks.VerdictCommitted
	hooks.VerdictCommitted = func(ctx context.Context, tx Tx, v CommittedVerdict) error {
		if err := verdict(ctx, tx, v); err != nil {
			return err
		}
		if kill.at("tx:verdict") {
			die()
		}
		return nil
	}
	if !shadow {
		// A shadow run posts nothing, so it writes no intents.
		ledger.SetHooks(hooks)
	}

	out, err := os.OpenFile(filepath.Join(dir, linesFile), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	lines := ndjson.NewWriter(&killingLines{f: out, kill: kill})

	warn, pause := sc.QueueWarn, sc.QueuePause
	if warn <= 0 {
		warn = DefaultBacklogWarn
	}
	if pause <= 0 {
		pause = DefaultBacklogPause
	}
	queue, err := NewQueue(warn, pause)
	if err != nil {
		return err
	}
	transport := feedtest.NewTransport()
	minter := feedtest.NewMinter()
	for range 100 {
		minter.ScriptTicket(eventfeed.StreamTicket{Ticket: "test-ticket-not-real", ExpiresIn: 120, URL: "wss://cable.basecamp.com/cable?ticket=test-ticket-not-real"})
	}
	var filters eventfeed.Filters
	if raw := os.Getenv(harnessFiltersEnv); raw != "" {
		if err := json.Unmarshal([]byte(raw), &filters); err != nil {
			return err
		}
	}
	window := sc.RepairWindow
	if window <= 0 {
		window = time.Minute
	}
	intake, err := New(Options{
		Origin: harnessOrigin, AccountID: harnessAccount, ConsumerNamespace: harnessNamespace,
		Filters: filters, Ledger: ledger, Queue: queue, Minter: minter,
		PollsFor:       pollsFor(dir, ledger, kill, os.Getenv(harnessFaultEnv)),
		Lines:          lines,
		Logger:         logger,
		Transport:      transport,
		RepairInterval: 50 * time.Millisecond,
		RepairWindow:   window,
	})
	if err != nil {
		return err
	}
	intake.repairSweep = 50 * time.Millisecond

	work := filepath.Join(dir, "work")
	routes := map[int64]admission.Route{harnessBucket: {Path: work, Class: "internal"}}
	reads := storeReads{dir: dir, gate: sc.ReadGate, kill: kill}
	admitter, err := admission.NewAdmitter(admission.Policy{
		AgentID:  harnessAgent,
		Trust:    admission.Trust{Mode: admission.TrustOperator, OperatorID: harnessOperator},
		Projects: routes,
	}, admission.Reads{Summaries: reads, Subscriptions: reads, Assignments: reads})
	if err != nil {
		return err
	}

	mcp := WorkerMCP{Command: filepath.Join(dir, "basecamp"), Profile: "agent", StateDir: stateDir}
	runFor := 60 * time.Second
	if d.Real {
		// The real `basecamp mcp`, holding a token that reaches no Basecamp:
		// the worker's basecamp_connect calls are real, its Basecamp calls
		// fail.
		mcp.Command, mcp.Env = os.Getenv(harnessRealBasecampEnv), []string{"BASECAMP_TOKEN"}
		runFor = 5 * time.Minute
	}
	failures, _ := strconv.Atoi(os.Getenv(harnessSpawnFailEnv))
	working := d.New(filepath.Join(dir, "agent"))
	worker := &failingSpawns{Driver: working, broken: d.New(filepath.Join(dir, "no-such-agent")), failures: failures}
	dispatcher, err := NewDispatcher(DispatcherOptions{
		Ledger: ledger, Driver: worker,
		Routes:             func() map[int64]admission.Route { return routes },
		Concurrency:        2,
		Deadline:           time.Hour,
		MCP:                mcp,
		PrivateDir:         filepath.Join(dir, "sessions"),
		Replies:            storeReplies{dir: dir},
		Workspaces:         &harnessWorkspaces{dir: dir},
		IsLifecycleMessage: IsLifecycleMessageIn(ledger),
		Lines:              lines,
		Logger:             logger,
		Tick:               20 * time.Millisecond,
		CancelGrace:        5 * time.Second,
	})
	if err != nil {
		return err
	}
	outbox, err := NewOutbox(OutboxOptions{
		Ledger: ledger, Poster: storePoster{dir: dir, kill: kill},
		Paused: ledger.Held, Lines: lines, Logger: logger,
		// A sending intent a previous process left is reconciled once it is
		// this old, so a restart settles it rather than waiting out the
		// production minute.
		Tick: 20 * time.Millisecond, ReconcileAfter: 200 * time.Millisecond,
	})
	if err != nil {
		return err
	}

	// Who is running, for a fake worker that is to kill it: written before
	// anything can be dispatched, removed when this process leaves cleanly.
	identity, err := json.Marshal(map[string]any{"pid": os.Getpid(), "started_at": time.Now().UTC()})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, connectorFile), identity, 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(filepath.Join(dir, connectorFile)) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushed := map[int64]bool{}
	if err := readJSONLines(filepath.Join(dir, liveFile), func(line []byte) error {
		var ids []int64
		if err := json.Unmarshal(line, &ids); err != nil {
			return err
		}
		for _, id := range ids {
			pushed[id] = true
		}
		return nil
	}); err != nil {
		return err
	}
	go (&cable{transport: transport, dir: dir, served: pushed}).run(ctx)

	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)
	part := func(name string, fn func(context.Context) error) {
		wg.Go(func() {
			if err := fn(ctx); err != nil && ctx.Err() == nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", name, err)
				}
				errMu.Unlock()
			}
			cancel()
		})
	}
	part("intake", intake.Run)
	part("admission", func(ctx context.Context) error {
		return RunAdmission(ctx, AdmissionOptions{Ledger: ledger, Queue: queue, Admitter: admitter, Lines: lines, Logger: logger})
	})
	if !shadow {
		part("dispatch", dispatcher.Run)
		part("outbox", outbox.Run)
	}

	until := os.Getenv(harnessUntilEnv)
	deadline := time.Now().Add(runFor)
	for ctx.Err() == nil {
		if kill.point == "paused" && queue.Paused() {
			die()
		}
		if kill.point == "get-dispatch" {
			// A worker has called get_dispatch: it cancels the guard.
			var n int
			if err := ledger.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_events WHERE guard = 'canceled'`).Scan(&n); err == nil && n > 0 {
				die()
			}
		}
		done, err := harnessPredicate(ctx, dir, ledger, until)
		if err != nil {
			logger.Warn("recovery harness: predicate", "error", err)
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the ledger never reached %q; %s", until, unsettled(ctx, ledger))
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	wg.Wait()
	flushCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if !shadow {
		if err := outbox.Flush(flushCtx); err != nil {
			return err
		}
	}
	return firstErr
}

// failingSpawns starts its first workers with an agent binary that does not
// exist, so the driver itself reports a start that ran nothing.
type failingSpawns struct {
	driver.Driver
	broken   driver.Driver
	mu       sync.Mutex
	failures int
}

func (f *failingSpawns) NewSession(ctx context.Context, cfg driver.SessionConfig) (driver.Session, error) {
	f.mu.Lock()
	fail := f.failures > 0
	if fail {
		f.failures--
	}
	f.mu.Unlock()
	if fail {
		return f.broken.NewSession(ctx, cfg)
	}
	return f.Driver.NewSession(ctx, cfg)
}

// harnessPredicate is the ledger state a surviving run stops at; predicates
// joined by commas must all hold.
func harnessPredicate(ctx context.Context, dir string, l *Ledger, until string) (bool, error) {
	if strings.Contains(until, ",") {
		for _, one := range strings.Split(until, ",") {
			ok, err := harnessPredicate(ctx, dir, l, one)
			if err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	}
	switch until {
	case "", "settled":
		return ledgerSettled(ctx, dir, l)
	case "never":
		return false, nil
	}
	if arg, ok := strings.CutPrefix(until, "state:"); ok {
		// "state:<id>=<state>", or "state:<id>" for any state.
		id, state, _ := strings.Cut(arg, "=")
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return false, err
		}
		r, found, err := l.Get(ctx, n)
		return found && (state == "" || string(r.State) == state), err
	}
	if until == "losses-closed" {
		settled, err := ledgerSettled(ctx, dir, l)
		if err != nil || !settled {
			return false, err
		}
		open, err := l.OpenLosses(ctx)
		return len(open) == 0, err
	}
	return false, fmt.Errorf("unknown predicate %q", until)
}

// harnessWorkspaces is the working directory a task gets: the route itself,
// as the run command's default does. It records every preparation and every
// release, so a test can say whether a directory was released — which the
// one-owner rule allows only once a worker's process group is gone.
type harnessWorkspaces struct{ dir string }

type workspaceEvent struct {
	Step    string `json:"step"`
	Route   string `json:"route"`
	WorkDir string `json:"work_dir"`
	EventID int64  `json:"event_id,omitempty"`
}

func (w *harnessWorkspaces) Prepare(_ context.Context, route string, eventID int64) (string, error) {
	return route, appendJSONLine(filepath.Join(w.dir, workspaceFile), workspaceEvent{Step: "prepare", Route: route, WorkDir: route, EventID: eventID})
}

func (w *harnessWorkspaces) Finish(_ context.Context, route, workDir string) error {
	return appendJSONLine(filepath.Join(w.dir, workspaceFile), workspaceEvent{Step: "finish", Route: route, WorkDir: workDir})
}

// unsettled says what a run that never reached its predicate was still
// holding, so a failure names it rather than the timeout alone.
func unsettled(ctx context.Context, l *Ledger) string {
	var out []string
	rows, err := l.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM events GROUP BY state`)
	if err != nil {
		return "the ledger could not be read: " + err.Error()
	}
	for rows.Next() {
		var state string
		var n int
		if rows.Scan(&state, &n) == nil {
			out = append(out, fmt.Sprintf("%s=%d", state, n))
		}
	}
	_ = rows.Close()
	attempts, _ := l.LiveAttempts(ctx)
	intents, _ := l.Intents(ctx, IntentFilter{States: []IntentState{IntentPending, IntentSending}})
	for _, in := range intents {
		out = append(out, fmt.Sprintf("intent %d %s %s not_before=%s", in.ID, in.Kind, in.State, in.NotBefore.Format(time.RFC3339)))
	}
	return fmt.Sprintf("records %v, live attempts %d", out, len(attempts))
}

// ledgerSettled is a connector with nothing left to do: every event the feed
// serves is in the ledger, nothing waits for admission or a worker, no
// attempt is live, and no due lifecycle message is unsent.
func ledgerSettled(ctx context.Context, dir string, l *Ledger) (bool, error) {
	entries, err := readFeed(dir)
	if err != nil {
		return false, err
	}
	// One query for the whole feed rather than a read per event: the overflow
	// scenario publishes ten thousand of them, and this runs on a timer.
	repairPolls := countRepairPolls(dir)
	var want, lowest, highest int64
	for _, e := range entries {
		if e.FromRepairPoll > repairPolls || e.Never {
			continue
		}
		want++
		if lowest == 0 || e.Event.ID < lowest {
			lowest = e.Event.ID
		}
		highest = max(highest, e.Event.ID)
	}
	if want > 0 {
		var have int64
		if err := l.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE id BETWEEN ? AND ?`, lowest, highest).Scan(&have); err != nil {
			return false, err
		}
		if have < want {
			return false, nil
		}
	}
	var busy int
	err = l.db.QueryRowContext(ctx, `
SELECT (SELECT COUNT(*) FROM events WHERE state IN ('seen', 'admitted', 'queued', 'dispatched')
          AND NOT (state IN ('admitted', 'queued') AND EXISTS (SELECT 1 FROM hold_marker)))
     + (SELECT COUNT(*) FROM attempts WHERE state <> 'ended')
     + (SELECT COUNT(*) FROM outbox WHERE state = 'sending'
          OR (state = 'pending' AND not_before <= ? AND NOT EXISTS (SELECT 1 FROM hold_marker)))`, l.timestamp()).Scan(&busy)
	if err != nil {
		return false, err
	}
	return busy == 0, nil
}
