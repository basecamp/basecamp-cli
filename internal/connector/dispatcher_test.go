package connector

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// fakeDriver hands out fakeSessions and lets a test script each turn.
type fakeDriver struct {
	mu       sync.Mutex
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
	return driver.Process{PID: 999999, PGID: 999999, StartedAt: time.Now()}
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
	private := filepath.Join(t.TempDir(), "sessions")
	require.NoError(t, os.Mkdir(private, 0o700))
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
	var cfg driver.SessionConfig
	fake.onStart = func(c driver.SessionConfig) { cfg = c }
	h := newDispatchHarness(t, fake, nil)
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	h.attemptsEnded(t, 1)
	s := fake.sessions[0]
	prompt := s.promptList()[0]

	assert.NotContains(t, prompt, "please look", "no content")
	assert.NotContains(t, prompt, "A comment", "no title")
	assert.Contains(t, prompt, "https://app.basecamp.com/2914079/buckets/48699913/recordings/10304028972")
	assert.Less(t, estimateTokens(prompt), MaxPromptTokens)

	require.Len(t, cfg.MCPServers, 1)
	token := cfg.MCPServers[0].Env[TaskTokenEnv]
	require.NotEmpty(t, token)
	assert.NotContains(t, prompt, token)
	assert.NotContains(t, strings.Join(cfg.MCPServers[0].Args, " "), token, "no token in argv")
	for _, kv := range cfg.Env {
		assert.NotContains(t, kv, token, "the worker's own environment has no token")
		assert.False(t, strings.HasPrefix(kv, "CLAUDE_CODE_MESSAGING_TOKEN="), "the host's tokens stay the host's")
		assert.False(t, strings.HasPrefix(kv, "BASECAMP_TOKEN="))
	}
	_, hostToken := cfg.MCPServers[0].Env["BASECAMP_TOKEN"]
	assert.False(t, hostToken)
	assert.Equal(t, testRoute, cfg.Cwd)
	assert.Equal(t, testRoute, cfg.Policy.Rules().WorkDir)
}

// estimateTokens is a deliberately pessimistic count: every run of letters or
// digits, every other non-space character, and one extra per eight characters
// of a long run.
func estimateTokens(s string) int {
	n := 0
	run := 0
	flush := func() {
		if run > 0 {
			n += 1 + run/8
		}
		run = 0
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			run++
		case r == ' ' || r == '\n':
			flush()
		default:
			flush()
			n++
		}
	}
	flush()
	return n
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
	recovered bool
}

func (w *fakeWorkspaces) Prepare(_ context.Context, route string, eventID int64) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.n++
	return route + "-wt-" + string(rune('0'+w.n)), nil
}
func (w *fakeWorkspaces) Finish(context.Context, string, string) error { return nil }
func (w *fakeWorkspaces) PerTaskDirs() bool                            { return w.perTask }
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
	fake.turn = func(*fakeSession, int, string) (driver.PromptResult, error) {
		return driver.PromptResult{Refusals: []driver.Refusal{{ToolCallID: "t1", Tool: "Bash"}}}, driver.ErrSessionEnded
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
