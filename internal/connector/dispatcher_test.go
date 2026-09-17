package connector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/drivertest"
	"github.com/basecamp/basecamp-cli/internal/connector/ndjson"
)

// fakeDriver hands out fakeSessions and lets a test script each turn.
type fakeDriver struct {
	mu       sync.Mutex
	process  driver.Process
	startErr []error
	onStart  func(cfg driver.SessionConfig)
	sessions []*fakeSession
	// turn answers each prompt; nil means end_turn at once.
	turn func(s *fakeSession, n int, prompt string) (driver.PromptResult, error)
	made chan *fakeSession
}

func newFakeDriver() *fakeDriver { return &fakeDriver{made: make(chan *fakeSession, 16)} }

func (d *fakeDriver) Name() string { return "fake" }
func (d *fakeDriver) Capabilities() driver.Capabilities {
	return driver.Capabilities{FollowUpPrompts: true}
}

func (d *fakeDriver) NewSession(_ context.Context, cfg driver.SessionConfig) (driver.Session, error) {
	if d.onStart != nil {
		d.onStart(cfg)
	}
	d.mu.Lock()
	if len(d.startErr) > 0 {
		err := d.startErr[0]
		d.startErr = d.startErr[1:]
		d.mu.Unlock()
		return nil, err
	}
	s := &fakeSession{d: d, cfg: cfg, done: make(chan struct{}), updates: make(chan driver.Update), canceled: make(chan struct{}, 1)}
	d.sessions = append(d.sessions, s)
	d.mu.Unlock()
	d.made <- s
	return s, nil
}

func (d *fakeDriver) LoadSession(context.Context, driver.SessionConfig, string) (driver.Session, error) {
	return nil, errors.New("not supported")
}

type fakeSession struct {
	d        *fakeDriver
	cfg      driver.SessionConfig
	mu       sync.Mutex
	prompts  []string
	done     chan struct{}
	once     sync.Once
	updates  chan driver.Update
	canceled chan struct{}
	exit     driver.Exit
	closed   bool
}

func (s *fakeSession) ID() string { return "session-1" }
func (s *fakeSession) Process() driver.Process {
	if s.d.process.PGID != 0 {
		return s.d.process
	}
	return driver.Process{PID: 1 << 30, PGID: 1 << 30, StartedAt: time.Now()}
}

func (s *fakeSession) Prompt(_ context.Context, prompt string) (driver.PromptResult, error) {
	s.mu.Lock()
	s.prompts = append(s.prompts, prompt)
	n := len(s.prompts)
	s.mu.Unlock()
	if s.d.turn == nil {
		return driver.PromptResult{Stop: driver.TurnEndTurn}, nil
	}
	return s.d.turn(s, n, prompt)
}

func (s *fakeSession) Updates() <-chan driver.Update { return s.updates }

func (s *fakeSession) Cancel(context.Context) error {
	select {
	case s.canceled <- struct{}{}:
	default:
	}
	return nil
}

func (s *fakeSession) Close() error {
	s.mu.Lock()
	exit := s.exit
	s.mu.Unlock()
	s.exitWith(exit)
	return nil
}

func (s *fakeSession) exitWith(e driver.Exit) {
	s.once.Do(func() {
		s.mu.Lock()
		s.exit, s.closed = e, true
		s.mu.Unlock()
		close(s.updates)
		close(s.done)
	})
}

func (s *fakeSession) Done() <-chan struct{} { return s.done }
func (s *fakeSession) Exit() driver.Exit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exit
}

func (s *fakeSession) promptList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.prompts...)
}

type dispatchHarness struct {
	ledger *Ledger
	fake   *fakeDriver
	d      *Dispatcher
	routes map[int64]admission.Route
	mu     sync.Mutex
}

func newDispatchHarness(t *testing.T, fake *fakeDriver, tweak func(*DispatcherOptions)) *dispatchHarness {
	t.Helper()
	h := &dispatchHarness{ledger: newTestLedger(t), fake: fake, routes: map[int64]admission.Route{adapterBucketID: {Path: testRoute}}}
	// Session directories hold a unix socket, whose path the kernel keeps
	// short; a test's own temporary directory can be too long for one.
	private, err := os.MkdirTemp("/tmp", "bcc-test-")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(private, 0o700))
	t.Cleanup(func() { _ = os.RemoveAll(private) })
	opts := DispatcherOptions{
		Ledger: h.ledger,
		Driver: fake,
		Routes: func() map[int64]admission.Route {
			h.mu.Lock()
			defer h.mu.Unlock()
			out := map[int64]admission.Route{}
			for k, v := range h.routes {
				out[k] = v
			}
			return out
		},
		Concurrency: 2,
		Deadline:    time.Hour,
		MCP:         WorkerMCP{Command: "/usr/local/bin/basecamp", Profile: "agent", StateDir: "/state/2914079-52007412"},
		PrivateDir:  private,
		Lookup: func(k string) (string, bool) {
			switch k {
			case "HOME":
				return "/home/operator", true
			case "CLAUDE_CODE_MESSAGING_TOKEN", "BASECAMP_TOKEN":
				return "test-token-not-real-host", true
			}
			return "", false
		},
		Tick:        10 * time.Millisecond,
		CancelGrace: 200 * time.Millisecond,
	}
	if tweak != nil {
		tweak(&opts)
	}
	d, err := NewDispatcher(opts)
	require.NoError(t, err)
	h.d = d
	return h
}

// run runs the dispatcher until the returned stop is called, which waits for
// Run to return.
func (h *dispatchHarness) run(t *testing.T) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.d.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("the dispatcher did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func (h *dispatchHarness) attemptsEnded(t *testing.T, n int) []attemptRow {
	t.Helper()
	var rows []attemptRow
	require.Eventually(t, func() bool {
		r, err := h.ledger.db.QueryContext(context.Background(), `SELECT state, stop_reason, spawn_failed FROM attempts WHERE state = 'ended' ORDER BY launched_at, rowid`)
		if err != nil {
			return false
		}
		defer r.Close()
		rows = nil
		for r.Next() {
			var a attemptRow
			if r.Scan(&a.State, &a.StopReason, &a.SpawnFailed) != nil {
				return false
			}
			rows = append(rows, a)
		}
		return len(rows) >= n
	}, 10*time.Second, 10*time.Millisecond)
	return rows
}

// Dispatcher invariant 1: the ledger has the attempt launching and the event
// exposed before the driver is asked for anything.
func TestTheDriverIsAskedOnlyAfterTheLedgerSaysLaunching(t *testing.T) {
	fake := newFakeDriver()
	var h *dispatchHarness
	var sawLaunching, sawExposed bool
	fake.onStart = func(cfg driver.SessionConfig) {
		var state, delivery string
		_ = h.ledger.db.QueryRowContext(context.Background(), `SELECT state FROM attempts WHERE id = ?`, cfg.Scope.AttemptID).Scan(&state)
		_ = h.ledger.db.QueryRowContext(context.Background(), `SELECT delivery FROM task_events WHERE task_id = ? AND event_id = 1`, cfg.Scope.TaskID).Scan(&delivery)
		sawLaunching, sawExposed = state == "launching", delivery == "exposed"
	}
	h = newDispatchHarness(t, fake, nil)
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	rows := h.attemptsEnded(t, 1)
	assert.True(t, sawLaunching)
	assert.True(t, sawExposed)
	assert.Equal(t, "finished", rows[0].StopReason)
	assert.Equal(t, StateCompleted, getRecord(t, h.ledger, 1).State, "exposed and unreported is completed(unknown)")
}

// Dispatcher invariant 3.
func TestNothingCrossesToTheWorkerThatItDoesNotNeed(t *testing.T) {
	fake := newFakeDriver()
	// The worker's group is this test's own, so this process may take the
	// token from the socket the way the worker's MCP server would.
	fake.process = driver.Process{PID: os.Getpid(), PGID: syscall.Getpgrp(), StartedAt: time.Now()}
	var cfg driver.SessionConfig
	token := make(chan string, 1)
	fake.turn = func(s *fakeSession, n int, _ string) (driver.PromptResult, error) {
		if n == 1 {
			socket := cfg.MCPServers[0].Args[len(cfg.MCPServers[0].Args)-1]
			dialer := net.Dialer{Timeout: 2 * time.Second}
			conn, err := dialer.DialContext(context.Background(), "unix", socket)
			if err == nil {
				data, _ := io.ReadAll(conn)
				_ = conn.Close()
				token <- strings.TrimSpace(string(data))
			} else {
				token <- ""
			}
		}
		return driver.PromptResult{Stop: driver.TurnEndTurn}, nil
	}
	fake.onStart = func(c driver.SessionConfig) { cfg = c }
	lines := &safeBuffer{}
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.Lines = ndjson.NewWriter(lines)
		// Unix socket paths are short.
		dir, err := os.MkdirTemp("/tmp", "bc-sess-")
		require.NoError(t, err)
		require.NoError(t, os.Chmod(dir, 0o700))
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		o.PrivateDir = dir
	})
	// The "worker's group" is this test's own: confirming it gone would kill
	// the test.
	h.d.confirmGroupGone = func(driver.Process, time.Duration) error { return nil }
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	h.attemptsEnded(t, 1)
	s := fake.sessions[0]
	prompt := s.promptList()[0]

	assert.NotContains(t, prompt, "please look", "no content")
	assert.NotContains(t, prompt, "A comment", "no title")
	assert.Contains(t, prompt, "https://app.basecamp.com/2914079/buckets/48699913/recordings/10304028972")
	t.Logf("production-sized prompt: %d tokens by the upper bound", estimateTokens(prompt))
	assert.Less(t, estimateTokens(prompt), MaxPromptTokens)

	// The token reaches the worker's MCP server only over its one-use socket.
	secret := <-token
	require.NotEmpty(t, secret, "the worker's own group was handed the token")
	require.Len(t, cfg.MCPServers, 1)
	assert.Equal(t, []string{"connect", "worker-mcp"}, cfg.MCPServers[0].Args[:2], "the agent starts the connector's bridge")
	for _, kv := range cfg.Env {
		assert.False(t, strings.HasPrefix(kv, "CLAUDE_CODE_MESSAGING_TOKEN="), "the host's tokens stay the host's")
		assert.False(t, strings.HasPrefix(kv, "BASECAMP_TOKEN="))
	}
	_, hostToken := cfg.MCPServers[0].Env["BASECAMP_TOKEN"]
	assert.False(t, hostToken)
	assert.Equal(t, testRoute, cfg.Cwd)
	assert.Equal(t, testRoute, cfg.Policy.Rules().WorkDir)
	serverEnv := make([]string, 0, len(cfg.MCPServers[0].Env))
	for k, v := range cfg.MCPServers[0].Env {
		serverEnv = append(serverEnv, k+"="+v)
	}
	drivertest.RequireNoSecret(t, secret, drivertest.Places{
		Env:   append(cfg.Env, serverEnv...),
		Args:  append([]string{prompt}, cfg.MCPServers[0].Args...),
		Texts: []string{lines.String()},
		Dirs:  []string{h.d.opts.PrivateDir},
	})
}

// estimateTokens is a deliberately pessimistic count: two characters a token,
// where English prose runs about four and the worst a real tokenizer reaches
// on text like this — ids, punctuation, tool names — is about two. It is a
// calibrated bound, not a proof: card 22 measured an 899-byte prompt at 322
// tokens with the real tokenizer, which this puts at 450, and the budget's
// margin is what absorbs the difference. A byte-per-token adversary would
// beat it, and nothing an agent writes reaches this prompt.
func estimateTokens(s string) int {
	return (len(s) + 1) / 2
}

func TestASpawnFailureIsRetriedOnceByTheDispatcher(t *testing.T) {
	fake := newFakeDriver()
	fake.startErr = []error{
		errors.Join(driver.ErrNotStarted, errors.New("no binary")),
		errors.Join(driver.ErrNotStarted, errors.New("no binary")),
	}
	h := newDispatchHarness(t, fake, nil)
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	rows := h.attemptsEnded(t, 2)
	assert.True(t, rows[0].SpawnFailed)
	assert.True(t, rows[1].SpawnFailed)
	require.Eventually(t, func() bool { return getRecord(t, h.ledger, 1).State == StateBlocked }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	var attempts int
	require.NoError(t, h.ledger.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM attempts`).Scan(&attempts))
	assert.Equal(t, 2, attempts, "no third try")
}

func TestAStartErrorThatMayHaveRunIsNotRetried(t *testing.T) {
	fake := newFakeDriver()
	fake.startErr = []error{errors.New("handshake failed after start")}
	h := newDispatchHarness(t, fake, nil)
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	rows := h.attemptsEnded(t, 1)
	assert.False(t, rows[0].SpawnFailed)
	assert.Equal(t, "failed", rows[0].StopReason)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, StateCompleted, getRecord(t, h.ledger, 1).State)
	var attempts int
	require.NoError(t, h.ledger.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM attempts`).Scan(&attempts))
	assert.Equal(t, 1, attempts)
}

// Dispatcher invariant 4.
func TestStopReasonsAreTheDispatchersOwnRecord(t *testing.T) {
	blockUntilCanceled := func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
		<-s.canceled
		return driver.PromptResult{Stop: driver.TurnCanceled}, nil
	}
	t.Run("deadline", func(t *testing.T) {
		fake := newFakeDriver()
		fake.turn = blockUntilCanceled
		h := newDispatchHarness(t, fake, func(o *DispatcherOptions) { o.Deadline = 100 * time.Millisecond })
		admitOn(t, h.ledger, 1, "recording:1")
		h.run(t)
		assert.Equal(t, "deadline", h.attemptsEnded(t, 1)[0].StopReason)
	})
	t.Run("shutdown", func(t *testing.T) {
		fake := newFakeDriver()
		fake.turn = blockUntilCanceled
		h := newDispatchHarness(t, fake, nil)
		admitOn(t, h.ledger, 1, "recording:1")
		stop := h.run(t)
		<-fake.made
		stop()
		assert.Equal(t, "shutdown", h.attemptsEnded(t, 1)[0].StopReason, "Run returns only once live attempts are settled")
	})
	t.Run("a cancel nobody asked for", func(t *testing.T) {
		fake := newFakeDriver()
		fake.turn = func(*fakeSession, int, string) (driver.PromptResult, error) {
			return driver.PromptResult{Stop: driver.TurnCanceled}, nil
		}
		h := newDispatchHarness(t, fake, nil)
		admitOn(t, h.ledger, 1, "recording:1")
		h.run(t)
		assert.Equal(t, "failed", h.attemptsEnded(t, 1)[0].StopReason)
	})
	t.Run("a worker gone mid-turn", func(t *testing.T) {
		fake := newFakeDriver()
		fake.turn = func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
			s.exitWith(driver.Exit{Code: -1, Signaled: true})
			select {}
		}
		h := newDispatchHarness(t, fake, nil)
		admitOn(t, h.ledger, 1, "recording:1")
		h.run(t)
		assert.Equal(t, "lost", h.attemptsEnded(t, 1)[0].StopReason)
	})
	t.Run("unsafe mode", func(t *testing.T) {
		fake := newFakeDriver()
		fake.turn = func(*fakeSession, int, string) (driver.PromptResult, error) {
			return driver.PromptResult{}, driver.ErrUnsafeMode
		}
		h := newDispatchHarness(t, fake, nil)
		admitOn(t, h.ledger, 1, "recording:1")
		h.run(t)
		assert.Equal(t, "failed", h.attemptsEnded(t, 1)[0].StopReason)
	})
	t.Run("a non-zero exit after a clean turn", func(t *testing.T) {
		fake := newFakeDriver()
		fake.turn = func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
			s.mu.Lock()
			s.exit = driver.Exit{Code: 2}
			s.mu.Unlock()
			return driver.PromptResult{Stop: driver.TurnEndTurn}, nil
		}
		h := newDispatchHarness(t, fake, nil)
		admitOn(t, h.ledger, 1, "recording:1")
		h.run(t)
		assert.Equal(t, "failed", h.attemptsEnded(t, 1)[0].StopReason)
	})
}

func TestAFollowUpIsExposedBeforeItsPromptInTheSameSession(t *testing.T) {
	fake := newFakeDriver()
	var h *dispatchHarness
	release := make(chan struct{})
	var followUpExposed bool
	fake.turn = func(s *fakeSession, n int, prompt string) (driver.PromptResult, error) {
		switch n {
		case 1:
			<-release
		case 2:
			var delivery string
			_ = h.ledger.db.QueryRowContext(context.Background(), `SELECT delivery FROM task_events WHERE task_id = ? AND event_id = 2`, s.cfg.Scope.TaskID).Scan(&delivery)
			followUpExposed = delivery == "exposed"
		}
		return driver.PromptResult{Stop: driver.TurnEndTurn}, nil
	}
	h = newDispatchHarness(t, fake, nil)
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	s := <-fake.made
	admitOn(t, h.ledger, 2, "recording:1")
	close(release)

	rows := h.attemptsEnded(t, 1)
	assert.Equal(t, "finished", rows[0].StopReason)
	prompts := s.promptList()
	require.Len(t, prompts, 2)
	assert.Equal(t, FollowUpPrompt(2), prompts[1])
	assert.True(t, followUpExposed)
	assert.Len(t, fake.sessions, 1, "one session for the conversation")
}

// Dispatcher invariant 2.
func TestARouteNoLongerApprovedIsNotDispatched(t *testing.T) {
	fake := newFakeDriver()
	h := newDispatchHarness(t, fake, nil)
	h.routes = map[int64]admission.Route{adapterBucketID: {Path: "/another/checkout"}}
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	time.Sleep(150 * time.Millisecond)
	assert.Empty(t, fake.sessions)
	assert.Equal(t, StateAdmitted, getRecord(t, h.ledger, 1).State)
}

func TestConcurrencyIsABound(t *testing.T) {
	fake := newFakeDriver()
	hold := make(chan struct{})
	fake.turn = func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
		select {
		case <-hold:
		case <-s.canceled:
			return driver.PromptResult{Stop: driver.TurnCanceled}, nil
		}
		return driver.PromptResult{Stop: driver.TurnEndTurn}, nil
	}
	h := newDispatchHarness(t, fake, nil)
	for i, id := range []int64{1, 2, 3} {
		route := "/work/r" + string(rune('a'+i))
		h.routes[adapterBucketID+int64(i)] = admission.Route{Path: route}
		seenRecord(t, h.ledger, id)
		v := admittedVerdict(id, 0, "recording:"+string(rune('a'+i)))
		v.Route = route
		_, err := h.ledger.ledgerCommitWithBucket(v, adapterBucketID+int64(i))
		require.NoError(t, err)
	}
	h.run(t)
	<-fake.made
	<-fake.made
	time.Sleep(150 * time.Millisecond)
	fake.mu.Lock()
	assert.Len(t, fake.sessions, 2)
	fake.mu.Unlock()
	close(hold)
	h.attemptsEnded(t, 3)
}

// ledgerCommitWithBucket admits v and moves its record to another bucket, so
// tests can have several routed projects.
func (l *Ledger) ledgerCommitWithBucket(v admission.Verdict, bucket int64) (admission.State, error) {
	state, err := l.Admission().Commit(context.Background(), v)
	if err != nil {
		return state, err
	}
	_, err = l.db.ExecContext(context.Background(), `UPDATE events SET bucket_id = ? WHERE id = ?`, bucket, v.EventID)
	return state, err
}

// Dispatcher invariant 5.
func TestARestartSettlesWhatAPreviousProcessLeftLive(t *testing.T) {
	fake := newFakeDriver()
	h := newDispatchHarness(t, fake, nil)
	admitOn(t, h.ledger, 1, "recording:1")
	l := launch(t, h.ledger, 1)
	// A pid above the kernel's maximum: no process, nothing to signal.
	require.NoError(t, h.ledger.MarkRunning(context.Background(), l.AttemptID, AttemptProcess{PID: 1 << 30, PGID: 1 << 30, StartedAt: time.Now(), SessionID: "s"}))
	leftover := filepath.Join(h.d.opts.PrivateDir, l.AttemptID)
	require.NoError(t, os.Mkdir(leftover, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(leftover, "mcp.json"), []byte(`{"env":"test-token-not-real"}`), 0o600))

	require.NoError(t, h.d.Recover(context.Background()))
	assert.Equal(t, "lost", readAttempt(t, h.ledger, l.AttemptID).StopReason)
	assert.Equal(t, "unknown", readTaskEvent(t, h.ledger, l.TaskID, 1).Outcome, "launching after a crash is read as running")
	_, err := os.Stat(leftover)
	assert.True(t, os.IsNotExist(err), "a session file that could hold a token is swept")
	assert.Empty(t, fake.sessions)
}

// A driver whose sessions take one prompt.
type oneShotDriver struct{ *fakeDriver }

func (oneShotDriver) Capabilities() driver.Capabilities { return driver.Capabilities{} }

func TestAFollowUpForAOneShotDriverStartsATaskOfItsOwn(t *testing.T) {
	fake := newFakeDriver()
	release := make(chan struct{})
	var turns sync.Mutex
	started := 0
	fake.turn = func(s *fakeSession, n int, _ string) (driver.PromptResult, error) {
		turns.Lock()
		started++
		first := started == 1
		turns.Unlock()
		if first {
			<-release
		}
		return driver.PromptResult{Stop: driver.TurnEndTurn}, nil
	}
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) { o.Driver = oneShotDriver{fake} })
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	first := <-fake.made
	admitOn(t, h.ledger, 2, "recording:1")
	close(release)

	rows := h.attemptsEnded(t, 2)
	assert.Equal(t, "finished", rows[0].StopReason)
	assert.Len(t, first.promptList(), 1, "nothing more is prompted into a one-shot session")
	second := <-fake.made
	assert.Contains(t, second.promptList()[0], "Event 2:", "the follow-up is the originating event of a new task")
	var unknown int
	require.NoError(t, h.ledger.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM task_events WHERE task_id = ? AND event_id = 2 AND outcome <> ''`, first.cfg.Scope.TaskID).Scan(&unknown))
	assert.Zero(t, unknown, "never exposed on the first task, so not unknown there")
}

type fakeWorkspaces struct {
	perTask   bool
	mu        sync.Mutex
	n         int
	finished  int
	recovered bool
}

func (w *fakeWorkspaces) Prepare(_ context.Context, route string, eventID int64) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.n++
	return route + "-wt-" + string(rune('0'+w.n)), nil
}
func (w *fakeWorkspaces) Finish(context.Context, string, string) error {
	w.mu.Lock()
	w.finished++
	w.mu.Unlock()
	return nil
}
func (w *fakeWorkspaces) PerTaskDirs() bool { return w.perTask }
func (w *fakeWorkspaces) Recover(context.Context) error {
	w.mu.Lock()
	w.recovered = true
	w.mu.Unlock()
	return nil
}

func TestPerTaskWorkspacesLetTwoTasksShareARoute(t *testing.T) {
	fake := newFakeDriver()
	hold := make(chan struct{})
	fake.turn = func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
		select {
		case <-hold:
		case <-s.canceled:
			return driver.PromptResult{Stop: driver.TurnCanceled}, nil
		}
		return driver.PromptResult{Stop: driver.TurnEndTurn}, nil
	}
	ws := &fakeWorkspaces{perTask: true}
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) { o.Workspaces = ws })
	admitOn(t, h.ledger, 1, "recording:1")
	admitOn(t, h.ledger, 2, "recording:2")
	h.run(t)
	a, b := nextSession(t, fake), nextSession(t, fake)
	assert.NotEqual(t, a.cfg.Cwd, b.cfg.Cwd)
	close(hold)
	h.attemptsEnded(t, 2)
	assert.True(t, ws.recovered, "Recover runs on start")
}

func nextSession(t *testing.T, fake *fakeDriver) *fakeSession {
	t.Helper()
	select {
	case s := <-fake.made:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no session was started")
		return nil
	}
}

// admitRouted admits a record on its own conversation in bucket, routed to
// route.
func admitRouted(t *testing.T, ledger *Ledger, id, bucket int64, key, route string) {
	t.Helper()
	seenRecord(t, ledger, id)
	v := admittedVerdict(id, 0, key)
	v.Route = route
	_, err := ledger.ledgerCommitWithBucket(v, bucket)
	require.NoError(t, err)
}

// Review r1, blocking: records the dispatcher cannot start never fill the
// window ahead of one it can.
func TestRecordsTheDispatcherCannotStartDoNotStarveOthers(t *testing.T) {
	t.Run("a route no longer approved", func(t *testing.T) {
		fake := newFakeDriver()
		h := newDispatchHarness(t, fake, nil)
		for i := int64(1); i <= 12; i++ {
			admitRouted(t, h.ledger, i, 777, "recording:u"+string(rune('a'+i)), "/unrouted")
		}
		admitRouted(t, h.ledger, 50, adapterBucketID, "recording:ok", testRoute)
		h.run(t)
		s := nextSession(t, fake)
		assert.Equal(t, int64(50), s.cfg.Scope.EventIDs[0])
	})
	t.Run("a backlog on a busy route", func(t *testing.T) {
		fake := newFakeDriver()
		hold := make(chan struct{})
		fake.turn = func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
			select {
			case <-hold:
			case <-s.canceled:
				return driver.PromptResult{Stop: driver.TurnCanceled}, nil
			}
			return driver.PromptResult{Stop: driver.TurnEndTurn}, nil
		}
		h := newDispatchHarness(t, fake, nil)
		h.routes[888] = admission.Route{Path: "/work/other"}
		for i := int64(1); i <= 12; i++ {
			admitRouted(t, h.ledger, i, adapterBucketID, "recording:b"+string(rune('a'+i)), testRoute)
		}
		admitRouted(t, h.ledger, 50, 888, "recording:other", "/work/other")
		h.run(t)
		first, second := nextSession(t, fake), nextSession(t, fake)
		assert.ElementsMatch(t, []string{testRoute, "/work/other"}, []string{first.cfg.Cwd, second.cfg.Cwd})
		close(hold)
	})
}

func TestTheProjectScopeNarrowsDispatch(t *testing.T) {
	fake := newFakeDriver()
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) { o.Buckets = []int64{888} })
	h.routes[888] = admission.Route{Path: "/work/other"}
	admitRouted(t, h.ledger, 1, adapterBucketID, "recording:1", testRoute)
	admitRouted(t, h.ledger, 2, 888, "recording:2", "/work/other")
	h.run(t)
	s := nextSession(t, fake)
	assert.Equal(t, int64(2), s.cfg.Scope.EventIDs[0])
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, StateAdmitted, getRecord(t, h.ledger, 1).State, "a project outside --project is not dispatched")
}

// Review r1, 2: a stop asked for as a turn ends is still that stop.
func TestAShutdownAsATurnEndsIsRecordedAsShutdown(t *testing.T) {
	fake := newFakeDriver()
	h := newDispatchHarness(t, fake, nil)
	admitOn(t, h.ledger, 1, "recording:1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// The shutdown lands after the turn's clean answer, before a follow-up
	// is looked for.
	h.d.afterTurn = cancel
	go func() { done <- h.d.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	assert.Equal(t, "shutdown", h.attemptsEnded(t, 1)[0].StopReason)
}

// Review r1, 3 and 4.
func TestExitsTheDispatcherCausedAreNotFailures(t *testing.T) {
	t.Run("a worker signaled on close after a clean turn", func(t *testing.T) {
		fake := newFakeDriver()
		fake.turn = func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
			s.mu.Lock()
			s.exit = driver.Exit{Code: -1, Signaled: true}
			s.mu.Unlock()
			return driver.PromptResult{Stop: driver.TurnEndTurn}, nil
		}
		h := newDispatchHarness(t, fake, nil)
		admitOn(t, h.ledger, 1, "recording:1")
		h.run(t)
		assert.Equal(t, "finished", h.attemptsEnded(t, 1)[0].StopReason)
	})
	t.Run("an unsafe session the driver ended itself", func(t *testing.T) {
		for i := range 10 {
			t.Run(strconv.Itoa(i), func(t *testing.T) {
				fake := newFakeDriver()
				fake.turn = func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
					s.exitWith(driver.Exit{Code: -1, Signaled: true})
					return driver.PromptResult{}, driver.ErrUnsafeMode
				}
				h := newDispatchHarness(t, fake, nil)
				admitOn(t, h.ledger, 1, "recording:1")
				h.run(t)
				assert.Equal(t, "failed", h.attemptsEnded(t, 1)[0].StopReason, "not lost")
			})
		}
	})
}

// Copilot and review r1, 5: an unverifiable worker is not settled around.
func TestAWorkerThatCannotBeVerifiedKeepsItsAttemptLive(t *testing.T) {
	fake := newFakeDriver()
	h := newDispatchHarness(t, fake, nil)
	admitOn(t, h.ledger, 1, "recording:1")
	l := launch(t, h.ledger, 1)
	require.NoError(t, h.ledger.MarkRunning(context.Background(), l.AttemptID, AttemptProcess{PID: 4242, PGID: 4242, StartedAt: time.Now(), SessionID: "s"}))
	admitOn(t, h.ledger, 2, "recording:2")
	h.d.terminateRecorded = func(driver.Process, time.Duration) (bool, error) {
		return false, errors.New("start time unreadable")
	}

	require.NoError(t, h.d.Recover(context.Background()))
	assert.Equal(t, "running", readAttempt(t, h.ledger, l.AttemptID).State, "not settled")
	h.run(t)
	time.Sleep(150 * time.Millisecond)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	assert.Empty(t, fake.sessions, "its directory stays held")
}

// Review r1, 7.
func TestASettlementThatFailsIsRetried(t *testing.T) {
	fake := newFakeDriver()
	h := newDispatchHarness(t, fake, nil)
	var mu sync.Mutex
	failures := 2
	h.ledger.SetHooks(Hooks{AttemptEnded: func(context.Context, Tx, Settlement) error {
		mu.Lock()
		defer mu.Unlock()
		if failures > 0 {
			failures--
			return errors.New("busy outbox")
		}
		return nil
	}})
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	assert.Equal(t, "finished", h.attemptsEnded(t, 1)[0].StopReason)
}

// Copilot r2: a route revoked while a task runs stops follow-ups joining it.
func TestAFollowUpDoesNotJoinATaskWhoseRouteWasRevoked(t *testing.T) {
	fake := newFakeDriver()
	release := make(chan struct{})
	fake.turn = func(*fakeSession, int, string) (driver.PromptResult, error) {
		<-release
		return driver.PromptResult{Stop: driver.TurnEndTurn}, nil
	}
	h := newDispatchHarness(t, fake, nil)
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	s := nextSession(t, fake)

	h.mu.Lock()
	h.routes = map[int64]admission.Route{}
	h.mu.Unlock()
	admitOn(t, h.ledger, 2, "recording:1")
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, StateQueued, getRecord(t, h.ledger, 2).State, "not handed to a worker in a directory no longer approved")
	close(release)
	h.attemptsEnded(t, 1)
	assert.Len(t, s.promptList(), 1)
}

// Copilot r2: a crash mid-launch leaves a worker nobody can name.
func TestAnAttemptLeftMidLaunchKeepsItsDirectoryHeld(t *testing.T) {
	fake := newFakeDriver()
	h := newDispatchHarness(t, fake, nil)
	admitOn(t, h.ledger, 1, "recording:1")
	l := launch(t, h.ledger, 1)

	require.NoError(t, h.d.Recover(context.Background()))
	assert.Equal(t, "launching", readAttempt(t, h.ledger, l.AttemptID).State, "not settled around a worker that cannot be named")
	h.run(t)
	time.Sleep(150 * time.Millisecond)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	assert.Empty(t, fake.sessions)
}

// Review r2 and card 23's review: a configuration no retry can fix is not
// retried.
func TestAnUnusableConfigurationIsNotRetried(t *testing.T) {
	fake := newFakeDriver()
	fake.startErr = []error{errors.Join(driver.ErrNotStarted, driver.ErrUnusable)}
	h := newDispatchHarness(t, fake, nil)
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	rows := h.attemptsEnded(t, 1)
	assert.True(t, rows[0].SpawnFailed)
	require.Eventually(t, func() bool { return getRecord(t, h.ledger, 1).State == StateBlocked }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	var attempts int
	require.NoError(t, h.ledger.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM attempts`).Scan(&attempts))
	assert.Equal(t, 1, attempts, "no automatic retry of a configuration error")
}

// Card 23's review: a session the driver says has ended is lost, not failed.
func TestASessionTheDriverSaysHasEndedIsLost(t *testing.T) {
	fake := newFakeDriver()
	fake.turn = func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
		refusal := driver.Refusal{ToolCallID: "t1", Tool: "Bash"}
		_ = s.cfg.Refusals.RecordRefusal(context.Background(), refusal)
		return driver.PromptResult{Refusals: []driver.Refusal{refusal}}, driver.ErrSessionEnded
	}
	h := newDispatchHarness(t, fake, nil)
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	assert.Equal(t, "lost", h.attemptsEnded(t, 1)[0].StopReason)
	var refusals int
	require.NoError(t, h.ledger.db.QueryRowContext(context.Background(), `SELECT refusals FROM attempts`).Scan(&refusals))
	assert.Equal(t, 1, refusals, "refusals are counted whatever ended the turn")
}

// Copilot r3: an attempt recovery left live holds a worker slot.
func TestAnAttemptLeftLiveHoldsAWorkerSlot(t *testing.T) {
	fake := newFakeDriver()
	hold := make(chan struct{})
	fake.turn = func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
		select {
		case <-hold:
		case <-s.canceled:
			return driver.PromptResult{Stop: driver.TurnCanceled}, nil
		}
		return driver.PromptResult{Stop: driver.TurnEndTurn}, nil
	}
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) { o.Concurrency = 2 })
	// One attempt whose worker cannot be identified, on its own route.
	h.routes[900] = admission.Route{Path: "/work/held"}
	admitRouted(t, h.ledger, 1, 900, "recording:held", "/work/held")
	_, err := h.ledger.LaunchTask(context.Background(), LaunchSpec{EventID: 1, Route: "/work/held", Driver: "fake"})
	require.NoError(t, err)
	// Two more conversations, each with a route of its own.
	h.routes[901] = admission.Route{Path: "/work/a"}
	h.routes[902] = admission.Route{Path: "/work/b"}
	admitRouted(t, h.ledger, 2, 901, "recording:a", "/work/a")
	admitRouted(t, h.ledger, 3, 902, "recording:b", "/work/b")

	require.NoError(t, h.d.Recover(context.Background()))
	h.run(t)
	nextSession(t, fake)
	time.Sleep(200 * time.Millisecond)
	fake.mu.Lock()
	live := len(fake.sessions)
	fake.mu.Unlock()
	assert.Equal(t, 1, live, "the held attempt's worker may still exist, so only one more starts")
	close(hold)
}

// The one-owner rule (see internal/connector/driver/worker.go): a task whose
// process tree is still alive never has its directory released or its record
// settled.
func TestATaskWithASurvivingGrandchildNeverReleasesItsDirectory(t *testing.T) {
	work := t.TempDir()
	worker, grandchild := drivertest.StartTree(t, work)
	<-worker.Done() // the leader is gone; its grandchild is not

	fake := newFakeDriver()
	// The session reports the worker's group, which still has a member, and
	// closing it kills nothing.
	fake.process = worker.Process()
	ws := &fakeWorkspaces{}
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.Workspaces = ws
		o.CancelGrace = 200 * time.Millisecond
	})
	// Confirmation without signaling, so the fixture's tree survives the
	// check as a tree that ignored every signal would.
	h.d.confirmGroupGone = func(p driver.Process, _ time.Duration) error {
		if driver.GroupMembersRemain(p) {
			return driver.ErrGroupOutlivedLeader
		}
		return nil
	}
	h.routes[adapterBucketID] = admission.Route{Path: work}
	admitRouted(t, h.ledger, 1, adapterBucketID, "recording:1", work)
	h.run(t)

	require.Eventually(t, func() bool {
		attempts, err := h.ledger.LiveAttempts(context.Background())
		return err == nil && len(attempts) == 1 && attempts[0].State == AttemptRunning
	}, 5*time.Second, 20*time.Millisecond)
	time.Sleep(500 * time.Millisecond)
	drivertest.RequireGroupHeld(t, worker.Process())
	assert.True(t, drivertest.Alive(grandchild))

	attempt := liveAttemptID(t, h.ledger)
	assert.Equal(t, "running", readAttempt(t, h.ledger, attempt).State, "the record is not terminal")
	assert.Equal(t, StateDispatched, getRecord(t, h.ledger, 1).State)
	ws.mu.Lock()
	defer ws.mu.Unlock()
	assert.Zero(t, ws.finished, "the working directory is not released")
}

// liveAttemptID is the id of the one attempt that has not ended.
func liveAttemptID(t *testing.T, ledger *Ledger) string {
	t.Helper()
	attempts, err := ledger.LiveAttempts(context.Background())
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	return attempts[0].AttemptID
}

// Review r3: a turn a stop cut short still refused what it refused.
func TestAStoppedTurnStillCountsItsRefusals(t *testing.T) {
	fake := newFakeDriver()
	fake.turn = func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
		refusals := []driver.Refusal{{ToolCallID: "t1", Tool: "Bash"}, {ToolCallID: "t2", Tool: "WebFetch"}}
		for _, r := range refusals {
			_ = s.cfg.Refusals.RecordRefusal(context.Background(), r)
		}
		<-s.canceled
		return driver.PromptResult{Stop: driver.TurnCanceled, Refusals: refusals}, nil
	}
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) { o.Deadline = 100 * time.Millisecond })
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	assert.Equal(t, "deadline", h.attemptsEnded(t, 1)[0].StopReason)
	var refusals int
	require.NoError(t, h.ledger.db.QueryRowContext(context.Background(), `SELECT refusals FROM attempts`).Scan(&refusals))
	assert.Equal(t, 2, refusals)
}

// Copilot r4: recovery releases nothing until the recorded group is confirmed
// gone, whatever the terminate step reported.
func TestRecoveryReleasesNothingWhileTheRecordedGroupSurvives(t *testing.T) {
	work := t.TempDir()
	worker, grandchild := drivertest.SurvivingWorker(t, work)

	fake := newFakeDriver()
	ws := &fakeWorkspaces{}
	lines := &safeBuffer{}
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.Workspaces = ws
		o.Lines = ndjson.NewWriter(lines)
		o.CancelGrace = 100 * time.Millisecond
	})
	h.routes[adapterBucketID] = admission.Route{Path: work}
	admitRouted(t, h.ledger, 1, adapterBucketID, "recording:1", work)
	l, err := h.ledger.LaunchTask(context.Background(), LaunchSpec{EventID: 1, Route: work, Driver: "fake"})
	require.NoError(t, err)
	require.NoError(t, h.ledger.MarkRunning(context.Background(), l.AttemptID, AttemptProcess{
		PID: worker.PID, PGID: worker.PGID, StartedAt: worker.StartedAt, SessionID: "s",
	}))
	// The terminate step reports it signaled the group, as it does for a
	// worker that ignores every signal.
	h.d.terminateRecorded = func(driver.Process, time.Duration) (bool, error) { return true, nil }
	h.d.confirmGroupGone = func(p driver.Process, _ time.Duration) error {
		if driver.GroupMembersRemain(p) {
			return driver.ErrGroupOutlivedLeader
		}
		return nil
	}

	require.NoError(t, h.d.Recover(context.Background()))
	assert.Equal(t, "running", readAttempt(t, h.ledger, l.AttemptID).State, "the record is not terminal")
	assert.Equal(t, StateDispatched, getRecord(t, h.ledger, 1).State)
	assert.True(t, drivertest.Alive(grandchild))
	ws.mu.Lock()
	assert.Zero(t, ws.finished, "the working directory is not released")
	ws.mu.Unlock()
	assert.NotContains(t, lines.String(), `"state":"ended"`, "and no end is reported")
}

// Copilot r4: a settlement that cannot be written releases nothing either.
func TestASettlementThatCannotBeWrittenReleasesNothing(t *testing.T) {
	fake := newFakeDriver()
	ws := &fakeWorkspaces{}
	lines := &safeBuffer{}
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.Workspaces = ws
		o.Lines = ndjson.NewWriter(lines)
	})
	h.ledger.SetHooks(Hooks{AttemptEnded: func(context.Context, Tx, Settlement) error {
		return errors.New("the outbox refuses every time")
	}})
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)

	// The run gives up on the settlement and lets the attempt go, still live.
	require.Eventually(t, func() bool {
		return strings.Contains(lines.String(), `"state":"running"`) && liveRuns(h) == 0
	}, 10*time.Second, 50*time.Millisecond)
	attempts, err := h.ledger.LiveAttempts(context.Background())
	require.NoError(t, err)
	require.Len(t, attempts, 1, "the attempt stays live")
	assert.Zero(t, ws.finishedCount(), "its directory is not released")
	assert.NotContains(t, lines.String(), `"state":"ended"`, "and no end is reported")
	assert.Equal(t, StateDispatched, getRecord(t, h.ledger, 1).State)
}

func (w *fakeWorkspaces) finishedCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.finished
}

func liveRuns(h *dispatchHarness) int {
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	return len(h.d.live)
}

// Card 23: a start whose handshake failed after it launched a process
// releases nothing until that group is confirmed gone.
func TestAStartThatFailedAfterLaunchingReleasesNothingWhileItsGroupLives(t *testing.T) {
	work := t.TempDir()
	worker, grandchild := drivertest.SurvivingWorker(t, work)

	fake := newFakeDriver()
	fake.startErr = []error{&driver.StartError{Process: worker, Err: errors.New("handshake timed out")}}
	ws := &fakeWorkspaces{}
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) { o.Workspaces = ws; o.CancelGrace = 100 * time.Millisecond })
	h.d.confirmGroupGone = func(p driver.Process, _ time.Duration) error {
		if driver.GroupMembersRemain(p) {
			return driver.ErrGroupOutlivedLeader
		}
		return nil
	}
	h.routes[adapterBucketID] = admission.Route{Path: work}
	admitRouted(t, h.ledger, 1, adapterBucketID, "recording:1", work)
	h.run(t)

	require.Eventually(t, func() bool {
		attempts, err := h.ledger.LiveAttempts(context.Background())
		return err == nil && len(attempts) == 1 && liveRuns(h) == 0 && h.d.heldCount() == 1
	}, 5*time.Second, 20*time.Millisecond)
	assert.True(t, drivertest.Alive(grandchild))
	assert.Zero(t, ws.finishedCount(), "the directory is not released")
	assert.Equal(t, StateDispatched, getRecord(t, h.ledger, 1).State, "the record is not terminal")
}

// Card 19: how a worker went decides its stop. Exiting on its own with a
// non-zero status is failed; vanishing is lost.
func TestAWorkerThatExitsNonZeroMidTurnFailedAndOneThatVanishedIsLost(t *testing.T) {
	for name, tc := range map[string]struct {
		exit driver.Exit
		want string
	}{
		"exited 2 on its own":      {driver.Exit{Code: 2}, "failed"},
		"killed by someone else":   {driver.Exit{Code: -1, Signaled: true}, "lost"},
		"gone with no status seen": {driver.Exit{Code: -1, Err: errors.New("wait failed")}, "lost"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeDriver()
			fake.turn = func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
				s.exitWith(tc.exit)
				return driver.PromptResult{}, driver.ErrSessionEnded
			}
			h := newDispatchHarness(t, fake, nil)
			admitOn(t, h.ledger, 1, "recording:1")
			h.run(t)
			assert.Equal(t, tc.want, h.attemptsEnded(t, 1)[0].StopReason)
		})
	}
}

type waitingWorkspaces struct {
	fakeWorkspaces
	waiting []string
}

func (w *waitingWorkspaces) Prepare(_ context.Context, route string, _ int64) (string, error) {
	if slices.Contains(w.waiting, route) {
		return "", errors.New("the repository cannot take a worktree")
	}
	return route, nil
}

func (w *waitingWorkspaces) RoutesWaiting() []string { return w.waiting }

// Card 19: a route that cannot take a task must not starve the others.
func TestAFailingRouteDoesNotStarveTheOthers(t *testing.T) {
	fake := newFakeDriver()
	ws := &waitingWorkspaces{waiting: []string{"/work/broken"}}
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) { o.Workspaces = ws })
	h.routes[700] = admission.Route{Path: "/work/broken"}
	for i := int64(1); i <= 12; i++ {
		admitRouted(t, h.ledger, i, 700, "recording:broken"+strconv.FormatInt(i, 10), "/work/broken")
	}
	admitRouted(t, h.ledger, 50, adapterBucketID, "recording:ok", testRoute)
	h.run(t)
	s := nextSession(t, fake)
	assert.Equal(t, int64(50), s.cfg.Scope.EventIDs[0])
}

// The redaction rule at the connector's end (driver's redact.go): the task's
// own token, taken from the socket by the worker, comes back in what the
// driver reports, and nothing the dispatcher writes carries it.
func TestNothingTheDispatcherWritesCarriesASecret(t *testing.T) {
	fake := newFakeDriver()
	fake.process = driver.Process{PID: os.Getpid(), PGID: syscall.Getpgrp(), StartedAt: time.Now()}
	var cfg driver.SessionConfig
	fake.onStart = func(c driver.SessionConfig) { cfg = c }
	got := make(chan string, 1)
	fake.turn = func(s *fakeSession, n int, _ string) (driver.PromptResult, error) {
		socket := cfg.MCPServers[0].Args[len(cfg.MCPServers[0].Args)-1]
		dialer := net.Dialer{Timeout: 2 * time.Second}
		conn, err := dialer.DialContext(context.Background(), "unix", socket)
		require.NoError(t, err)
		data, _ := io.ReadAll(conn)
		_ = conn.Close()
		token := strings.TrimSpace(string(data))
		got <- token
		s.updates <- driver.Update{Kind: driver.UpdatePermission, Tool: "mcp__basecamp__" + token, Allowed: false}
		// Everything the rule names, the way an agent reports a failure.
		return driver.PromptResult{}, fmt.Errorf("agent failed: token %s, ledger %s, as someone@example.com",
			token, filepath.Join("/state/2914079-52007412", "ledger.db"))
	}
	var logs safeBuffer
	lines := &safeBuffer{}
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		o.Lines = ndjson.NewWriter(lines)
		dir, err := os.MkdirTemp("/tmp", "bc-sess-")
		require.NoError(t, err)
		require.NoError(t, os.Chmod(dir, 0o700))
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		o.PrivateDir = dir
	})
	// The worker's group is this test's own: confirming it gone would kill
	// the test.
	h.d.confirmGroupGone = func(driver.Process, time.Duration) error { return nil }
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	h.attemptsEnded(t, 1)

	token := <-got
	require.NotEmpty(t, token)
	written := logs.String() + lines.String()
	require.Contains(t, written, "prompt failed", "the failure was logged at all")
	assert.NotContains(t, written, token, "the task token")
	assert.NotContains(t, written, "/state/2914079-52007412", "a path under the state directory")
	assert.NotContains(t, written, "someone@example.com", "an address the agent volunteered")
	assert.NotContains(t, written, h.d.opts.PrivateDir, "a path under the runtime directory")
}

// A task's redaction knows the task's token, whatever else it knows.
func TestATasksRedactionCarriesItsToken(t *testing.T) {
	h := newDispatchHarness(t, newFakeDriver(), nil)
	r := h.d.taskRedaction(Launch{Token: "test-token-not-real"}, driver.SessionConfig{Env: []string{"A=alpha-not-real"}})
	assert.Contains(t, r.Secrets, "test-token-not-real")
	assert.Contains(t, r.Env, "A=alpha-not-real")
	assert.Contains(t, r.Dirs, h.d.opts.PrivateDir)
	assert.Contains(t, r.Dirs, h.d.opts.MCP.StateDir)
}

// The refusal rule (driver's "Refusals"): a refusal is in the ledger while
// the worker still runs, and a worker that exits before its result keeps it.
// The result's own list is not counted again.
func TestARefusalIsInTheLedgerBeforeTheWorkerGoes(t *testing.T) {
	fake := newFakeDriver()
	recorded := make(chan struct{})
	exit := make(chan struct{})
	fake.turn = func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
		_ = s.cfg.Refusals.RecordRefusal(context.Background(), driver.Refusal{ToolCallID: "t1", Tool: "Bash"})
		close(recorded)
		<-exit
		s.exitWith(driver.Exit{Code: 3})
		return driver.PromptResult{Refusals: []driver.Refusal{{ToolCallID: "t1", Tool: "Bash"}}}, driver.ErrSessionEnded
	}
	h := newDispatchHarness(t, fake, nil)
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)

	<-recorded
	var refusals int
	var state string
	require.NoError(t, h.ledger.db.QueryRowContext(context.Background(), `SELECT refusals, state FROM attempts`).Scan(&refusals, &state))
	assert.Equal(t, 1, refusals, "recorded at the moment, not at the end")
	assert.NotEqual(t, "ended", state)

	close(exit)
	h.attemptsEnded(t, 1)
	require.NoError(t, h.ledger.db.QueryRowContext(context.Background(), `SELECT refusals FROM attempts`).Scan(&refusals))
	assert.Equal(t, 1, refusals, "settled with the attempt, once")
}

// A refusal the ledger will not take is kept for the attempt's settlement.
func TestARefusalTheLedgerRefusedIsCarriedToTheSettlement(t *testing.T) {
	ledger := newTestLedger(t)
	r := &refusalRecorder{ledger: ledger, attemptID: "no-such-attempt", log: slog.New(slog.DiscardHandler)}
	assert.Error(t, r.RecordRefusal(context.Background(), driver.Refusal{ToolCallID: "t1", Tool: "Bash"}))
	assert.Equal(t, 1, r.unrecorded())
}

// Card 23's review: an agent may start the connector's own MCP server in a
// process group of its own (Codex does), so the release point ends the
// process that took the task token as well as the worker's group.
func TestTheProcessThatTookTheTokenIsEndedWithTheWorker(t *testing.T) {
	// A process of its own, standing in for the bridge an agent started
	// outside the worker's group.
	bridge := exec.CommandContext(context.Background(), "/bin/sleep", "300")
	bridge.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, bridge.Start())
	t.Cleanup(func() {
		_ = bridge.Process.Kill()
		_ = bridge.Wait()
	})
	taker, err := driver.LookupProcess(bridge.Process.Pid)
	require.NoError(t, err)

	h := newDispatchHarness(t, newFakeDriver(), nil)
	socket, err := ServeTaskToken(tokenDir(t), "test-token-not-real", time.Second)
	require.NoError(t, err)
	defer socket.Close()
	socket.mu.Lock()
	socket.taker = taker
	socket.mu.Unlock()
	// A worker in another group entirely, already confirmed gone.
	worker := driver.Process{PID: 1 << 30, PGID: 1 << 30}
	require.NoError(t, h.d.confirmTakerGone(worker, takerOf(socket)))
	// Alive() counts a zombie, and this test is the process that has not
	// reaped it; the rule's own question is whether anything of the group
	// still runs.
	assert.False(t, driver.GroupMembersRemain(taker), "the process holding the task token is ended with its worker")

	// Asked again, with nothing of it left, it is still gone.
	assert.NoError(t, h.d.confirmTakerGone(worker, takerOf(socket)))
}

// A token taken inside the worker's own group is already covered by the
// worker's own confirmation, and is not signaled twice.
func TestATakerInTheWorkersGroupIsNotEndedTwice(t *testing.T) {
	h := newDispatchHarness(t, newFakeDriver(), nil)
	socket, err := ServeTaskToken(tokenDir(t), "test-token-not-real", time.Second)
	require.NoError(t, err)
	defer socket.Close()
	socket.mu.Lock()
	socket.taker = driver.Process{PID: os.Getpid(), PGID: syscall.Getpgrp(), StartedAt: time.Now()}
	socket.mu.Unlock()
	require.NoError(t, h.d.confirmTakerGone(driver.Process{PID: os.Getpid(), PGID: syscall.Getpgrp()}, takerOf(socket)))
	assert.NoError(t, h.d.confirmTakerGone(driver.Process{PID: 1 << 30, PGID: 1 << 30}, takerOf(socket)),
		"this process's own group is never signaled, whatever a record says")
}

// Card 23's review: a session the driver ended because it was not the one the
// connector asked for — an MCP server that never connected — is failed, not
// lost. Lost is for a worker that went away.
func TestASessionThatIsNotTheOneAskedForIsFailed(t *testing.T) {
	fake := newFakeDriver()
	fake.turn = func(s *fakeSession, _ int, _ string) (driver.PromptResult, error) {
		// As the driver does: it ends the worker itself, so without the
		// sentinel this reads as a worker that was signaled and went.
		s.exitWith(driver.Exit{Signaled: true})
		return driver.PromptResult{}, fmt.Errorf("%w: MCP server %q did not connect", driver.ErrSessionUnverified, MCPServerName)
	}
	h := newDispatchHarness(t, fake, nil)
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	assert.Equal(t, "failed", h.attemptsEnded(t, 1)[0].StopReason)
}

// Card 23's review, across a restart: the process that took the task token is
// recorded with the attempt, so a connector that comes back ends it rather
// than leave a process of its own holding a superseded token.
func TestARestartEndsTheProcessThatTookTheToken(t *testing.T) {
	bridge := exec.CommandContext(context.Background(), "/bin/sleep", "300")
	bridge.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, bridge.Start())
	t.Cleanup(func() {
		_ = bridge.Process.Kill()
		_ = bridge.Wait()
	})
	taker, err := driver.LookupProcess(bridge.Process.Pid)
	require.NoError(t, err)

	h := newDispatchHarness(t, newFakeDriver(), nil)
	admitOn(t, h.ledger, 1, "recording:1")
	l := launch(t, h.ledger, 1)
	ctx := context.Background()
	// A worker whose pid is above the kernel's maximum: gone, nothing to
	// signal. Its MCP server is the one still running.
	require.NoError(t, h.ledger.MarkRunning(ctx, l.AttemptID, AttemptProcess{PID: 1 << 30, PGID: 1 << 30, StartedAt: time.Now(), SessionID: "s"}))
	require.NoError(t, h.ledger.RecordTaker(ctx, l.AttemptID, AttemptProcess{PID: taker.PID, PGID: taker.PGID, StartedAt: taker.StartedAt}))

	live, err := h.ledger.LiveAttempts(ctx)
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, taker.PID, live[0].Taker.PID, "the ledger carries it across the restart")

	require.NoError(t, h.d.Recover(ctx))
	assert.Equal(t, "lost", readAttempt(t, h.ledger, l.AttemptID).StopReason)
	assert.False(t, driver.GroupMembersRemain(taker), "the process holding the token is ended by the restart")
}

// A worker whose Basecamp MCP server dies mid-session cannot report what it
// was given; nothing in Claude Code's stream says so, so the end of a clean
// turn with an unreported event is logged for a person to find.
func TestACleanFinishWithAnUnreportedEventIsLogged(t *testing.T) {
	var logs safeBuffer
	fake := newFakeDriver()
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	})
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	require.Equal(t, "finished", h.attemptsEnded(t, 1)[0].StopReason)
	require.Eventually(t, func() bool { return strings.Contains(logs.String(), UnreportedFinishLine) },
		5*time.Second, 10*time.Millisecond, "a clean finish that reported nothing is named in the log")
	assert.Contains(t, logs.String(), `"event_id":1`)
}

// Card 22's review: a unix socket path is 103 bytes at most, and a long home
// or deep state directory puts a session directory past it. That would fail
// every dispatch, not one, so the socket moves rather than the task failing.
func TestADeepSessionDirectoryStillGetsItsTokenAcross(t *testing.T) {
	deep, err := os.MkdirTemp("/tmp", "bcc-deep-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(deep) })
	// Long enough that a socket in an attempt's own directory cannot fit.
	deep = filepath.Join(deep, strings.Repeat("d", 40), strings.Repeat("e", 40))
	require.NoError(t, os.MkdirAll(deep, 0o700))
	require.False(t, TokenSocketFits(filepath.Join(deep, "att_000000000000000000000000")),
		"the fixture must be past the limit for this test to mean anything")

	fake := newFakeDriver()
	fake.process = driver.Process{PID: os.Getpid(), PGID: syscall.Getpgrp(), StartedAt: time.Now()}
	var cfg driver.SessionConfig
	fake.onStart = func(c driver.SessionConfig) { cfg = c }
	token := make(chan string, 1)
	fake.turn = func(*fakeSession, int, string) (driver.PromptResult, error) {
		socket := cfg.MCPServers[0].Args[len(cfg.MCPServers[0].Args)-1]
		dialer := net.Dialer{Timeout: 2 * time.Second}
		conn, dialErr := dialer.DialContext(context.Background(), "unix", socket)
		if dialErr != nil {
			token <- ""
			return driver.PromptResult{Stop: driver.TurnEndTurn}, nil //nolint:nilerr // the failure is reported through the channel the test reads
		}
		data, _ := io.ReadAll(conn)
		_ = conn.Close()
		token <- strings.TrimSpace(string(data))
		return driver.PromptResult{Stop: driver.TurnEndTurn}, nil
	}
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) { o.PrivateDir = deep })
	// The worker's group is this test's own: confirming it gone would kill
	// the test.
	h.d.confirmGroupGone = func(driver.Process, time.Duration) error { return nil }
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	h.attemptsEnded(t, 1)

	assert.NotEmpty(t, <-token, "the worker's MCP server was handed its token from a socket that fits")
	socket := cfg.MCPServers[0].Args[len(cfg.MCPServers[0].Args)-1]
	assert.LessOrEqual(t, len(socket), 103)
	_, err = os.Stat(filepath.Dir(socket))
	assert.True(t, os.IsNotExist(err), "and the directory it was moved to is removed with the attempt")
}

// Opus r6: a socket directory the connector had to make elsewhere is its own
// to sweep, or a crash leaves one behind on every dispatch.
func TestAShortSocketDirectoryIsSweptOnStart(t *testing.T) {
	runtimeDir, err := os.MkdirTemp("/tmp", "bcrt-")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(runtimeDir, 0o700))
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })

	deep, err := os.MkdirTemp("/tmp", "bcc-deep-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(deep) })
	deep = filepath.Join(deep, strings.Repeat("d", 40), strings.Repeat("e", 40))
	require.NoError(t, os.MkdirAll(deep, 0o700))

	h := newDispatchHarness(t, newFakeDriver(), func(o *DispatcherOptions) {
		o.PrivateDir = deep
		o.Lookup = func(k string) (string, bool) {
			if k == "XDG_RUNTIME_DIR" {
				return runtimeDir, true
			}
			return "", false
		}
	})
	base := h.d.shortSocketBase(filepath.Join(deep, strings.Repeat("a", AttemptIDLength)))
	require.NotEmpty(t, base)
	assert.True(t, strings.HasPrefix(base, runtimeDir), "under the runtime directory this connector was given: %s vs %s", base, runtimeDir)

	// What a crashed run left behind.
	leftover := filepath.Join(base, "s-from-a-crash")
	require.NoError(t, os.Mkdir(leftover, 0o700))
	require.NoError(t, h.d.Recover(context.Background()))
	_, err = os.Stat(leftover)
	assert.True(t, os.IsNotExist(err), "a start sweeps what a crash left in it")
}

// Copilot: a start that failed can leave its attempt held, and a held
// attempt takes a worker slot. Capacity is asked again for every record in
// the pass, not counted down from what it was at the top.
func TestAHeldAttemptTakesASlotWithinTheSamePass(t *testing.T) {
	fake := newFakeDriver()
	// Every start fails after a process existed, and no group can be
	// confirmed gone: each attempt is held.
	for range 3 {
		fake.startErr = append(fake.startErr,
			&driver.StartError{Process: driver.Process{PID: 1 << 30, PGID: 1 << 30}, Err: errors.New("handshake failed")})
	}
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) { o.Concurrency = 2 })
	h.d.confirmGroupGone = func(driver.Process, time.Duration) error { return driver.ErrGroupOutlivedLeader }
	// Three records on three directories, so nothing but the bound stops them.
	for i, id := range []int64{1, 2, 3} {
		route := "/work/held" + string(rune('a'+i))
		h.routes[adapterBucketID+int64(i)] = admission.Route{Path: route}
		seenRecord(t, h.ledger, id)
		v := admittedVerdict(id, 0, "recording:held"+string(rune('a'+i)))
		v.Route = route
		_, err := h.ledger.ledgerCommitWithBucket(v, adapterBucketID+int64(i))
		require.NoError(t, err)
	}
	h.run(t)

	require.Eventually(t, func() bool { return h.d.heldCount() >= 2 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 2, h.d.heldCount(), "two held attempts fill the window, and the third record waits")
	var attempts int
	require.NoError(t, h.ledger.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM attempts`).Scan(&attempts))
	assert.Equal(t, 2, attempts, "no third worker while two are unaccounted for")
	assert.LessOrEqual(t, h.d.free(), 0)
}

// An agent hands its MCP servers its own whole environment, so a name the
// connector leaves unset arrives carrying the agent's value — and
// BASECAMP_BASE_URL is where the agent's Basecamp credential would be sent.
// Every name the server may have is pinned to this connector's value or to
// nothing.
func TestTheWorkersServerEnvironmentPinsEveryNameItMayHave(t *testing.T) {
	fake := newFakeDriver()
	var cfg driver.SessionConfig
	fake.onStart = func(c driver.SessionConfig) { cfg = c }
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.MCP.Env = []string{"BASECAMP_EXTRA_NOT_REAL"}
		o.Lookup = func(k string) (string, bool) {
			if k == "BASECAMP_CACHE_DIR" {
				return "/var/cache/connector", true
			}
			return "", false
		}
	})
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	h.attemptsEnded(t, 1)

	env := cfg.MCPServers[0].Env
	require.NotEmpty(t, env)
	for _, name := range append(append([]string{}, MCPServerEnv...), "BASECAMP_EXTRA_NOT_REAL") {
		value, ok := env[name]
		assert.Truef(t, ok, "%s is not pinned, so the agent's own value would reach the server", name)
		if name == "BASECAMP_CACHE_DIR" {
			assert.Equal(t, "/var/cache/connector", value)
		} else {
			assert.Empty(t, value, "%s", name)
		}
	}
}
