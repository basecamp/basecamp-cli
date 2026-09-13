package resilience

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHelperProcess is the re-exec target for the multi-process tests below:
// each child is one CLI invocation against the shared store in BH_STATE_DIR.
//
// BH_MODE=ops runs BH_OPS gated operations, each held for BH_HOLD, and prints
// OK or REJECT per operation plus PEAK, the most live slot holders it saw
// while holding one. BH_MODE=linger churns BH_OPS acquire/release pairs, prints
// DONE, then stays alive until stdin closes so the parent can check that its
// releases were not lost while the process still counts as a live holder.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("BH_HELPER") != "1" {
		return
	}
	store := NewStore(os.Getenv("BH_STATE_DIR"))
	ops, _ := strconv.Atoi(os.Getenv("BH_OPS"))
	hold, _ := time.ParseDuration(os.Getenv("BH_HOLD"))
	cfg := DefaultConfig()
	if tokens, err := strconv.ParseFloat(os.Getenv("BH_MAX_TOKENS"), 64); err == nil {
		cfg.RateLimiter.MaxTokens = tokens
	}

	switch os.Getenv("BH_MODE") {
	case "ops":
		hooks := NewGatingHooksFromConfig(store, cfg)
		op := basecamp.OperationInfo{Service: "Projects", Operation: "List"}
		peak := 0
		for range ops {
			ctx, err := hooks.OnOperationGate(context.Background(), op)
			if err != nil {
				fmt.Printf("REJECT %v\n", err)
				continue
			}
			if inUse, err := hooks.bulkhead.InUse(); err == nil {
				peak = max(peak, inUse)
			}
			time.Sleep(hold)
			hooks.OnOperationEnd(ctx, op, nil, hold)
			fmt.Println("OK")
		}
		fmt.Printf("PEAK %d\n", peak)
	case "linger":
		bh := NewBulkhead(store, cfg.Bulkhead)
		rl := NewRateLimiter(store, cfg.RateLimiter)
		for range ops {
			_, _ = rl.Allow()
			_, _ = bh.Acquire()
			_ = bh.Release()
		}
		fmt.Println("DONE")
		_, _ = io.ReadAll(os.Stdin)
	}
	os.Exit(0)
}

type helperEnv map[string]string

func helperCommand(t *testing.T, env helperEnv) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "BH_HELPER=1")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd
}

// runInvocation runs one child to completion and returns its report lines.
func runInvocation(t *testing.T, env helperEnv) []string {
	t.Helper()
	cmd := helperCommand(t, env)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	require.NoError(t, cmd.Run(), out.String())
	var lines []string
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(l, "OK") || strings.HasPrefix(l, "REJECT") || strings.HasPrefix(l, "PEAK") {
			lines = append(lines, l)
		}
	}
	return lines
}

type invocationTally struct {
	ok, rejected, peak int
	rejections         []string
}

func tally(lines []string) invocationTally {
	var tl invocationTally
	for _, l := range lines {
		switch {
		case l == "OK":
			tl.ok++
		case strings.HasPrefix(l, "REJECT"):
			tl.rejected++
			tl.rejections = append(tl.rejections, l)
		case strings.HasPrefix(l, "PEAK"):
			n, _ := strconv.Atoi(strings.TrimPrefix(l, "PEAK "))
			tl.peak = max(tl.peak, n)
		}
	}
	return tl
}

// runWorkers runs `workers` goroutines that each perform `calls` sequential
// child invocations, the shape of a parallel smoke test, and tallies the lot.
func runWorkers(t *testing.T, workers, calls int, env helperEnv) invocationTally {
	t.Helper()
	var mu sync.Mutex
	var all []string
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range calls {
				lines := runInvocation(t, env)
				mu.Lock()
				all = append(all, lines...)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return tally(all)
}

// The smoke-test shape: ten workers each making eight sequential calls
// through the production defaults. Before queueing, the shared 50-token
// bucket drained in the first half second and every later call failed with
// "rate limit exceeded" while the server had never answered 429.
func TestGateQueuesTenParallelWorkersThroughTheDefaults(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	tl := runWorkers(t, 10, 8, helperEnv{"BH_STATE_DIR": dir, "BH_MODE": "ops", "BH_OPS": "1", "BH_HOLD": "10ms"})

	assert.Equal(t, 80, tl.ok, "every call succeeds: %v", tl.rejections)
	assert.Zero(t, tl.rejected)
	assert.LessOrEqual(t, tl.peak, 10, "never more than MaxConcurrent live holders")
	assert.Less(t, time.Since(start), DefaultMaxWait)
}

// Fifteen simultaneous invocations against ten slots: nobody fails, nobody
// sees more than ten holders, and the wall clock shows the overflow waited
// for a second round rather than being admitted alongside the first.
func TestGateQueuesOversubscribedInvocationsWithinTheSlotLimit(t *testing.T) {
	dir := t.TempDir()
	hold := 150 * time.Millisecond
	start := time.Now()
	tl := runWorkers(t, 15, 1, helperEnv{
		"BH_STATE_DIR": dir, "BH_MODE": "ops", "BH_OPS": "1",
		"BH_HOLD": hold.String(), "BH_MAX_TOKENS": "100",
	})
	elapsed := time.Since(start)

	assert.Equal(t, 15, tl.ok, "every invocation succeeds: %v", tl.rejections)
	assert.LessOrEqual(t, tl.peak, 10, "never more than MaxConcurrent live holders")
	assert.GreaterOrEqual(t, elapsed, 2*hold, "the overflow waited for a slot")
	assert.Less(t, elapsed, DefaultMaxWait)

	state, err := NewStore(dir).Load()
	require.NoError(t, err)
	assert.Empty(t, state.Bulkhead.ActivePIDs, "every slot released")
}

// A dozen processes churning the lock must not lose each other's releases:
// while every child is still alive (so a leaked PID would not be swept as
// dead) the slot table is empty.
func TestStoreLockContentionDoesNotLoseReleases(t *testing.T) {
	dir := t.TempDir()
	const children = 12
	type child struct {
		cmd   *exec.Cmd
		stdin io.WriteCloser
	}
	kids := make([]child, 0, children)
	done := make(chan error, children)
	for range children {
		cmd := helperCommand(t, helperEnv{"BH_STATE_DIR": dir, "BH_MODE": "linger", "BH_OPS": "40"})
		stdin, err := cmd.StdinPipe()
		require.NoError(t, err)
		stdout, err := cmd.StdoutPipe()
		require.NoError(t, err)
		require.NoError(t, cmd.Start())
		kids = append(kids, child{cmd, stdin})
		go func() {
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				if scanner.Text() == "DONE" {
					done <- nil
					return
				}
			}
			done <- errors.New("child exited without DONE")
		}()
	}
	for range children {
		require.NoError(t, <-done)
	}

	state, err := NewStore(dir).Load()
	require.NoError(t, err)
	assert.Empty(t, state.Bulkhead.ActivePIDs, "a live process still holds a slot it released")

	for _, k := range kids {
		_ = k.stdin.Close()
		_ = k.cmd.Wait()
	}
}

func TestRateLimiterWaitSleepsForARefill(t *testing.T) {
	rl := NewRateLimiter(NewStore(t.TempDir()), RateLimiterConfig{MaxTokens: 1, RefillRate: 100, TokensPerRequest: 1})
	allowed, err := rl.Allow()
	require.NoError(t, err)
	require.True(t, allowed)

	start := time.Now()
	require.NoError(t, rl.Wait(context.Background(), start.Add(time.Second)))
	assert.GreaterOrEqual(t, time.Since(start), 5*time.Millisecond, "waited for the token to refill")
}

func TestRateLimiterWaitGivesUpWhenTheRefillOutlastsTheDeadline(t *testing.T) {
	rl := NewRateLimiter(NewStore(t.TempDir()), RateLimiterConfig{MaxTokens: 1, RefillRate: 10, TokensPerRequest: 1})
	_, _ = rl.Allow()

	err := rl.Wait(context.Background(), time.Now().Add(20*time.Millisecond))

	var gateErr *GateError
	require.ErrorAs(t, err, &gateErr)
	assert.ErrorIs(t, err, basecamp.ErrRateLimited)
	assert.Equal(t, "Too many requests (client limit 10/s); waited 0s", gateErr.Message)
	assert.Equal(t, "Re-run, or lower parallelism.", gateErr.Hint)
}

func TestRateLimiterWaitReportsTheEffectiveRequestRate(t *testing.T) {
	rl := NewRateLimiter(NewStore(t.TempDir()), RateLimiterConfig{MaxTokens: 5, RefillRate: 10, TokensPerRequest: 5})
	_, _ = rl.Allow()

	err := rl.Wait(context.Background(), time.Now().Add(20*time.Millisecond))

	var gateErr *GateError
	require.ErrorAs(t, err, &gateErr)
	assert.Equal(t, "Too many requests (client limit 2/s); waited 0s", gateErr.Message)
}

// holdStoreLock takes the store's lock from a second file description and
// keeps it for d, the way a busy neighboring process would.
func holdStoreLock(t *testing.T, store *Store, d time.Duration) {
	t.Helper()
	require.NoError(t, os.MkdirAll(store.Dir(), 0o700))
	held := flock.New(store.lockPath())
	require.NoError(t, held.Lock())
	go func() {
		time.Sleep(d)
		_ = held.Unlock()
	}()
}

// An Update that meets a held lock waits for it within LockTimeout and lands
// on the state the holder left, where a budget shorter than the hold falls
// open and writes over it. The production budget is the wide one.
func TestStoreLockTimeoutCoversAHeldLock(t *testing.T) {
	previous := LockTimeout
	t.Cleanup(func() { LockTimeout = previous })
	const hold = 150 * time.Millisecond

	update := func(dir string) (waited time.Duration) {
		store := NewStore(dir)
		require.NoError(t, store.Save(&State{Bulkhead: BulkheadState{ActivePIDs: []int{os.Getppid()}}}))
		holdStoreLock(t, store, hold)
		start := time.Now()
		require.NoError(t, store.Update(func(state *State) error {
			state.Bulkhead.AddPID(os.Getpid())
			return nil
		}))
		return time.Since(start)
	}

	LockTimeout = 2 * time.Second
	assert.GreaterOrEqual(t, update(t.TempDir()), hold, "waited for the holder")

	LockTimeout = 20 * time.Millisecond
	assert.Less(t, update(t.TempDir()), hold, "fell open before the holder released")
}

func TestRateLimiterWaitOutlastsAShortRetryAfter(t *testing.T) {
	rl := NewRateLimiter(NewStore(t.TempDir()), RateLimiterConfig{})
	require.NoError(t, rl.SetRetryAfterDuration(30*time.Millisecond))

	start := time.Now()
	require.NoError(t, rl.Wait(context.Background(), start.Add(time.Second)))
	assert.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond, "sent nothing before the block lifted")
}

func TestRateLimiterWaitReportsALongRetryAfterImmediately(t *testing.T) {
	rl := NewRateLimiter(NewStore(t.TempDir()), RateLimiterConfig{})
	require.NoError(t, rl.SetRetryAfterDuration(30*time.Second))

	start := time.Now()
	err := rl.Wait(context.Background(), start.Add(100*time.Millisecond))

	var gateErr *GateError
	require.ErrorAs(t, err, &gateErr)
	assert.ErrorIs(t, err, basecamp.ErrRateLimited)
	assert.Equal(t, "Rate limited by the server; retry after 30s", gateErr.Message)
	assert.Equal(t, "Wait 30s, then re-run.", gateErr.Hint)
	assert.Less(t, time.Since(start), 100*time.Millisecond, "did not burn the budget on a block it cannot outlast")
}

// The jitter that spreads retries must not stretch a sleep to or past the
// budget: a block that lifts just inside the deadline is waited out and the
// token taken, not overshot or rejected at the wire. Pinned to the maximum
// jitter, which unchecked would overshoot by half.
func TestRateLimiterWaitNeverSleepsPastTheDeadline(t *testing.T) {
	previous := jitter
	jitter = func(d time.Duration) time.Duration { return d / 2 }
	t.Cleanup(func() { jitter = previous })

	rl := NewRateLimiter(NewStore(t.TempDir()), RateLimiterConfig{})
	require.NoError(t, rl.SetRetryAfterDuration(200*time.Millisecond))

	start := time.Now()
	budget := 220 * time.Millisecond
	require.NoError(t, rl.Wait(context.Background(), start.Add(budget)))
	assert.Less(t, time.Since(start), budget+60*time.Millisecond)
}

func TestRateLimiterWaitRoundsTheRetryAfterUp(t *testing.T) {
	rl := NewRateLimiter(NewStore(t.TempDir()), RateLimiterConfig{})
	require.NoError(t, rl.SetRetryAfterDuration(40*time.Second+400*time.Millisecond))

	err := rl.Wait(context.Background(), time.Now().Add(time.Second))

	var gateErr *GateError
	require.ErrorAs(t, err, &gateErr)
	assert.Equal(t, "Rate limited by the server; retry after 41s", gateErr.Message)
	assert.Equal(t, "Wait 41s, then re-run.", gateErr.Hint)
}

func TestCeilSeconds(t *testing.T) {
	assert.Equal(t, 41*time.Second, ceilSeconds(40*time.Second+time.Millisecond))
	assert.Equal(t, 40*time.Second, ceilSeconds(40*time.Second))
	assert.Equal(t, time.Second, ceilSeconds(time.Millisecond))
}

func TestRateLimiterWaitStopsWhenTheContextIsCancelled(t *testing.T) {
	rl := NewRateLimiter(NewStore(t.TempDir()), RateLimiterConfig{})
	require.NoError(t, rl.SetRetryAfterDuration(5*time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := rl.Wait(ctx, time.Now().Add(time.Minute))
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// occupyBulkhead fills every slot with the test runner's PID: alive, and not
// ours, so Acquire cannot reuse it.
func occupyBulkhead(t *testing.T, store *Store, slots int) {
	t.Helper()
	require.NoError(t, store.Update(func(state *State) error {
		for range slots {
			state.Bulkhead.ActivePIDs = append(state.Bulkhead.ActivePIDs, os.Getppid())
		}
		return nil
	}))
}

func TestBulkheadWaitQueuesUntilASlotFrees(t *testing.T) {
	store := NewStore(t.TempDir())
	occupyBulkhead(t, store, 1)
	bh := NewBulkhead(store, BulkheadConfig{MaxConcurrent: 1})

	go func() {
		time.Sleep(40 * time.Millisecond)
		_ = store.Update(func(state *State) error {
			state.Bulkhead.RemovePID(os.Getppid())
			return nil
		})
	}()

	start := time.Now()
	require.NoError(t, bh.Wait(context.Background(), start.Add(2*time.Second)))
	assert.GreaterOrEqual(t, time.Since(start), 40*time.Millisecond)

	state, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, []int{os.Getpid()}, state.Bulkhead.ActivePIDs)
}

func TestBulkheadWaitGivesUpWithTheLimitAndTheWait(t *testing.T) {
	store := NewStore(t.TempDir())
	occupyBulkhead(t, store, 1)
	bh := NewBulkhead(store, BulkheadConfig{MaxConcurrent: 1})

	err := bh.Wait(context.Background(), time.Now().Add(60*time.Millisecond))

	var gateErr *GateError
	require.ErrorAs(t, err, &gateErr)
	assert.ErrorIs(t, err, basecamp.ErrBulkheadFull)
	assert.Equal(t, "Too many concurrent basecamp processes (limit 1); waited 0s", gateErr.Message)
	assert.Equal(t, "Re-run, or lower parallelism.", gateErr.Hint)
}

func TestGatingHooksGateSharesOneWaitBudgetAndLeaksNoSlot(t *testing.T) {
	store := NewStore(t.TempDir())
	occupyBulkhead(t, store, 1)
	cfg := DefaultConfig()
	cfg.MaxWait = 60 * time.Millisecond
	cfg.Bulkhead.MaxConcurrent = 1
	hooks := NewGatingHooksFromConfig(store, cfg)

	start := time.Now()
	_, err := hooks.OnOperationGate(context.Background(), basecamp.OperationInfo{Service: "Todos", Operation: "Complete"})
	elapsed := time.Since(start)

	var gateErr *GateError
	require.ErrorAs(t, err, &gateErr)
	assert.ErrorIs(t, err, basecamp.ErrBulkheadFull)
	assert.GreaterOrEqual(t, elapsed, cfg.MaxWait, "queued for the whole budget")
	assert.Less(t, elapsed, time.Second)

	state, err := store.Load()
	require.NoError(t, err)
	assert.False(t, state.Bulkhead.HasPID(os.Getpid()), "a rejected gate holds no slot")
}

func TestBulkheadWaitReturnsCancellationWithoutReservingASlot(t *testing.T) {
	store := NewStore(t.TempDir())
	bh := NewBulkhead(store, BulkheadConfig{MaxConcurrent: 1})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := bh.Wait(ctx, time.Now().Add(time.Second))

	assert.ErrorIs(t, err, context.Canceled)
	state, loadErr := store.Load()
	require.NoError(t, loadErr)
	assert.Empty(t, state.Bulkhead.ActivePIDs)
}

func TestRateLimiterWaitReturnsCancellationWithoutConsumingAToken(t *testing.T) {
	rl := NewRateLimiter(NewStore(t.TempDir()), RateLimiterConfig{MaxTokens: 5, RefillRate: 0.001})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := rl.Wait(ctx, time.Now().Add(time.Second))

	assert.ErrorIs(t, err, context.Canceled)
	tokens, tokensErr := rl.Tokens()
	require.NoError(t, tokensErr)
	assert.Equal(t, float64(5), tokens)
}

func TestCircuitBreakerTripped(t *testing.T) {
	store := NewStore(t.TempDir())
	cb := NewCircuitBreaker(store, CircuitBreakerConfig{OpenTimeout: time.Minute})
	clock := newFakeClock()
	cb.nowFn = clock.Now
	assert.False(t, cb.Tripped(), "closed")

	require.NoError(t, store.Update(func(state *State) error {
		state.CircuitBreaker.State = CircuitOpen
		state.CircuitBreaker.OpenedAt = clock.Now()
		return nil
	}))
	assert.True(t, cb.Tripped(), "open, timeout running")

	clock.Advance(2 * time.Minute)
	assert.False(t, cb.Tripped(), "open, timeout expired: Allow decides")
}

// With the circuit open, an invocation must not spend its queue budget
// waiting for a token it would only be refused with.
func TestGatingHooksFailsFastOnAnOpenCircuitBeforeQueueing(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.Update(func(state *State) error {
		state.CircuitBreaker.State = CircuitOpen
		state.CircuitBreaker.OpenedAt = time.Now()
		return nil
	}))
	cfg := DefaultConfig()
	cfg.MaxWait = 2 * time.Second
	cfg.RateLimiter = RateLimiterConfig{MaxTokens: 1, RefillRate: 2, TokensPerRequest: 1}
	hooks := NewGatingHooksFromConfig(store, cfg)
	allowed, err := hooks.rateLimiter.Allow()
	require.NoError(t, err)
	require.True(t, allowed, "bucket drained")

	start := time.Now()
	_, err = hooks.OnOperationGate(context.Background(), basecamp.OperationInfo{Service: "Todos", Operation: "Complete"})

	assert.ErrorIs(t, err, basecamp.ErrCircuitOpen)
	assert.Less(t, time.Since(start), 100*time.Millisecond, "no queueing for a refill")
	tokens, tokensErr := hooks.rateLimiter.Tokens()
	require.NoError(t, tokensErr)
	assert.Less(t, tokens, 1.0, "no token consumed")
}

// An expired budget rejects before the attempt, so a slot or token that
// happens to be free at the deadline is not taken past it.
func TestWaitsRejectAnExpiredDeadlineWithoutReserving(t *testing.T) {
	store := NewStore(t.TempDir())
	past := time.Now().Add(-time.Millisecond)

	bh := NewBulkhead(store, BulkheadConfig{MaxConcurrent: 1})
	assert.ErrorIs(t, bh.Wait(context.Background(), past), basecamp.ErrBulkheadFull)
	state, err := store.Load()
	require.NoError(t, err)
	assert.Empty(t, state.Bulkhead.ActivePIDs, "free slot left untaken")

	rl := NewRateLimiter(store, RateLimiterConfig{MaxTokens: 5, RefillRate: 0.001})
	assert.ErrorIs(t, rl.Wait(context.Background(), past), basecamp.ErrRateLimited)
	tokens, err := rl.Tokens()
	require.NoError(t, err)
	assert.Equal(t, float64(5), tokens, "no token consumed")
}

func TestGatingHooksAnswersCancellationBeforeTheCircuit(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.Update(func(state *State) error {
		state.CircuitBreaker.State = CircuitOpen
		state.CircuitBreaker.OpenedAt = time.Now()
		return nil
	}))
	hooks := NewGatingHooksFromConfig(store, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := hooks.OnOperationGate(ctx, basecamp.OperationInfo{Service: "Todos", Operation: "Complete"})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestGateErrorUnwrapsToTheSDKSentinel(t *testing.T) {
	err := fmt.Errorf("listing projects: %w", &GateError{Message: "m", Hint: "h", sentinel: basecamp.ErrBulkheadFull})
	assert.ErrorIs(t, err, basecamp.ErrBulkheadFull)
	assert.NotErrorIs(t, err, basecamp.ErrRateLimited)
	assert.Equal(t, "listing projects: m", err.Error())
}
