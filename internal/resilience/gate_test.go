package resilience

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
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
// OK or REJECT per operation, plus PEAK, the most live slot holders it saw
// while holding one, QUEUED, how many of its operations were turned away by
// the bucket and had to sleep for a refill, and SLOTWAIT, how many found
// every slot taken and had to poll for one. The last two are counted by the
// gate itself through its onWait seams, not inferred from outside: a look
// at the bucket before the call can be overtaken by a refill landing before
// the take, and would report a wait that never happened.
// BH_BARRIER=1 makes it print READY and hold before its first
// operation until the parent sends it a line, so the parent can set up the
// condition under test with every child already running.
// BH_MODE=linger churns BH_OPS acquire/release pairs, prints
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
		if os.Getenv("BH_BARRIER") == "1" {
			fmt.Println("READY")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		}
		// The gate says when it waits. Each operation raises its own flag at
		// most once, so the counts are operations that queued and not sleeps
		// they took getting through.
		var sleptOnBucket, sleptOnSlot bool
		hooks.rateLimiter.onWait = func() { sleptOnBucket = true }
		hooks.bulkhead.onWait = func() { sleptOnSlot = true }

		peak, queued, slotWaits := 0, 0, 0
		for range ops {
			sleptOnBucket, sleptOnSlot = false, false
			ctx, err := hooks.OnOperationGate(context.Background(), op)
			if err != nil {
				fmt.Printf("REJECT %v\n", err)
				continue
			}
			if sleptOnBucket {
				queued++
			}
			if sleptOnSlot {
				slotWaits++
			}
			if inUse, err := hooks.bulkhead.InUse(); err == nil {
				peak = max(peak, inUse)
			}
			time.Sleep(hold)
			hooks.OnOperationEnd(ctx, op, nil, hold)
			fmt.Println("OK")
		}
		fmt.Printf("PEAK %d\n", peak)
		fmt.Printf("QUEUED %d\n", queued)
		fmt.Printf("SLOTWAIT %d\n", slotWaits)
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

// isReportLine picks the helper's tallied output out of the test binary's
// own chatter.
func isReportLine(l string) bool {
	for _, prefix := range []string{"OK", "REJECT", "PEAK", "QUEUED", "SLOTWAIT"} {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
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

type invocationTally struct {
	ok, rejected, peak, queued, slotWaits int
	rejections                            []string
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
		case strings.HasPrefix(l, "QUEUED"):
			n, _ := strconv.Atoi(strings.TrimPrefix(l, "QUEUED "))
			tl.queued += n
		case strings.HasPrefix(l, "SLOTWAIT"):
			n, _ := strconv.Atoi(strings.TrimPrefix(l, "SLOTWAIT "))
			tl.slotWaits += n
		}
	}
	return tl
}

// runBarrieredInvocations starts n children and waits for every one of them
// to be up and holding before releasing them together.
//
// Spawning a process takes as long as the machine takes, so a test that
// lets the children race each other into the gate is really measuring how
// fast they start: on a slow box they arrive one at a time, the bucket
// refills between them, and the gate is never asked to queue anything. The
// barrier removes the machine from that question. Every child is already
// running when the first one gates, so the load the gate sees is the load
// the test asked for and not the load the box could deliver.
func runBarrieredInvocations(t *testing.T, n int, env helperEnv) invocationTally {
	t.Helper()
	barriered := helperEnv{"BH_BARRIER": "1"}
	maps.Copy(barriered, env)

	type child struct {
		cmd    *exec.Cmd
		stdin  io.WriteCloser
		stdout io.ReadCloser
		stderr *bytes.Buffer
	}
	kids := make([]child, 0, n)
	ready := make(chan error, n)
	for range n {
		cmd := helperCommand(t, barriered)
		stdin, err := cmd.StdinPipe()
		require.NoError(t, err)
		stdout, err := cmd.StdoutPipe()
		require.NoError(t, err)
		// Kept so that a child which dies says why: its panic or its
		// testing output is the only account of what went wrong there.
		stderr := &bytes.Buffer{}
		cmd.Stderr = stderr
		require.NoError(t, cmd.Start())
		kids = append(kids, child{cmd, stdin, stdout, stderr})
	}

	// Each child announces itself on its own line; nobody is released until
	// the last one has.
	scanners := make([]*bufio.Scanner, len(kids))
	for i, k := range kids {
		scanners[i] = bufio.NewScanner(k.stdout)
		go func(sc *bufio.Scanner) {
			for sc.Scan() {
				if sc.Text() == "READY" {
					ready <- nil
					return
				}
			}
			ready <- errors.New("a child exited before reaching the barrier")
		}(scanners[i])
	}
	for range kids {
		require.NoError(t, <-ready)
	}

	for _, k := range kids {
		_, err := io.WriteString(k.stdin, "go\n")
		require.NoError(t, err)
		require.NoError(t, k.stdin.Close())
	}

	var mu sync.Mutex
	var all []string
	var wg sync.WaitGroup
	for i := range kids {
		wg.Add(1)
		go func(sc *bufio.Scanner) {
			defer wg.Done()
			var lines []string
			for sc.Scan() {
				if l := sc.Text(); isReportLine(l) {
					lines = append(lines, l)
				}
			}
			mu.Lock()
			all = append(all, lines...)
			mu.Unlock()
		}(scanners[i])
	}
	wg.Wait()
	for _, k := range kids {
		require.NoError(t, k.cmd.Wait(), k.stderr.String())
	}
	return tally(all)
}

// The smoke-test shape: ten parallel workers making eighty calls between
// them through the production defaults. Before queueing, the shared 50-token
// bucket drained in the first half second and every later call failed with
// "rate limit exceeded" while the server had never answered 429.
//
// The claim is that the gate queues, and the gate is what says so. Its
// onWait seam fires when a caller is turned away by the bucket and settles
// in to sleep for a refill, and each child counts the operations that had
// to. Thirty of the eighty calls cannot be served from the starting bucket,
// so that is roughly what the count comes to, and the run cannot pass with
// it at zero — which is the case that matters, a run where the wait path
// was never entered and every assertion here was free.
//
// Inferring the same thing from outside does not work, and the first
// attempt at this did: it read the bucket before each call and counted the
// ones that found it empty. A refill landing between the look and the take
// makes that a wait that never happened. Only the gate knows.
//
// The children are released from a barrier so they issue their calls at the
// rate the test asked for rather than at the rate the machine can fork
// processes; left to race each other in, on a slow box they arrive spread
// out, the bucket refills between them, and nothing ever queues. The
// barrier only lines up the first of each child's eight calls, so a box
// slow enough to pace all eighty across the three seconds of refill would
// still see nothing queue — and would fail here on the zero count rather
// than pass over it.
//
// What is deliberately not asserted is how long the whole thing took. It
// used to require the run to finish inside DefaultMaxWait, which is one
// operation's queueing budget and no statement at all about a run of eighty:
// bounded below by three seconds of deliberate refill and above by nothing
// but the box. On a contended one that margin goes to the machine, and it
// failed on CI at 10.73s and again at 10.97s with the gate having behaved
// perfectly both times.
func TestGateQueuesTenParallelWorkersThroughTheDefaults(t *testing.T) {
	const workers, callsEach = 10, 8
	cfg := DefaultConfig()
	require.Greater(t, float64(workers*callsEach), cfg.RateLimiter.MaxTokens,
		"the run has to outlast the bucket or nothing queues")

	dir := t.TempDir()
	tl := runBarrieredInvocations(t, workers, helperEnv{
		"BH_STATE_DIR": dir, "BH_MODE": "ops",
		"BH_OPS": strconv.Itoa(callsEach), "BH_HOLD": "10ms",
	})

	assert.Equal(t, workers*callsEach, tl.ok, "every call succeeds: %v", tl.rejections)
	assert.Zero(t, tl.rejected)
	// Thirty calls are not covered by the starting bucket. Nearly all of
	// them sleep; the odd one arrives in the instant after a refill lands
	// and is served without waiting, so the count comes in a little under —
	// 29 idle here, 28 on a saturated core. One free pickup per worker is
	// the allowance. A zero is a run that never entered the wait path.
	uncovered := workers*callsEach - int(cfg.RateLimiter.MaxTokens)
	assert.GreaterOrEqual(t, tl.queued, uncovered-workers,
		"the calls the starting bucket could not cover slept for a refill; %d of %d did", tl.queued, uncovered)
	assert.LessOrEqual(t, tl.peak, cfg.Bulkhead.MaxConcurrent, "never more than MaxConcurrent live holders")
}

// The other side of the queue counter: with a bucket nobody can exhaust and
// a slot for every caller, the same eighty calls go through without the
// gate ever sleeping, and the counters say zero.
//
// Without this, a counter wired to fire on every call would satisfy the
// test above and prove nothing — the witness has to be able to say no.
func TestGateReportsNoQueueingWhenNothingHadToWait(t *testing.T) {
	const workers, callsEach = 10, 8
	cfg := DefaultConfig()
	require.LessOrEqual(t, workers, cfg.Bulkhead.MaxConcurrent, "nobody may have to wait for a slot")

	dir := t.TempDir()
	tl := runBarrieredInvocations(t, workers, helperEnv{
		"BH_STATE_DIR": dir, "BH_MODE": "ops",
		"BH_OPS": strconv.Itoa(callsEach), "BH_HOLD": "10ms",
		"BH_MAX_TOKENS": strconv.Itoa(workers * callsEach * 10),
	})

	assert.Equal(t, workers*callsEach, tl.ok, "every call succeeds: %v", tl.rejections)
	assert.Zero(t, tl.queued, "the bucket never ran out, so nothing should have slept for a refill")
	assert.Zero(t, tl.slotWaits, "there was a slot for everyone, so nobody should have polled for one")
}

// Fifteen simultaneous invocations against ten slots: nobody fails, and the
// slot table fills to exactly ten and no further, so five of them were made
// to wait for a second round rather than being admitted alongside the first.
//
// "Simultaneous" is why the children come off a barrier. Left to race each
// other into the gate they arrive as fast as the box can fork, and on a slow
// one the first ten are done before the last five start — at which point
// nothing is oversubscribed and the test passes having measured nothing. The
// old evidence that the overflow waited was that the run took at least two
// holds, which slow spawning satisfies all by itself. The bulkhead reports
// it directly now: five of the fifteen found every slot taken and polled
// for one.
func TestGateQueuesOversubscribedInvocationsWithinTheSlotLimit(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	const invocations = 15
	tl := runBarrieredInvocations(t, invocations, helperEnv{
		"BH_STATE_DIR": dir, "BH_MODE": "ops", "BH_OPS": "1",
		"BH_HOLD": (150 * time.Millisecond).String(), "BH_MAX_TOKENS": "100",
	})

	assert.Equal(t, invocations, tl.ok, "every invocation succeeds: %v", tl.rejections)
	assert.Zero(t, tl.rejected)
	assert.Equal(t, cfg.Bulkhead.MaxConcurrent, tl.peak,
		"the slot table filled to exactly MaxConcurrent")
	assert.Equal(t, invocations-cfg.Bulkhead.MaxConcurrent, tl.slotWaits,
		"the overflow — and only the overflow — found every slot taken and polled for one")

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

// The jitter that spreads retries must not stretch a sleep past the budget: a
// block that lifts just inside the deadline is waited out and the token taken,
// not overshot and rejected at the wire. Pinned to the maximum jitter, which
// unchecked would overshoot by half.
//
// What is checked is the sleep the gate asks for, not the wall clock around a
// real one. This test used to run Wait against a 200ms block with a 220ms
// budget and time it: a sleep has no upper bound on a loaded machine, so a
// wake-up 20ms late spent the budget and Wait returned the client-limit error
// it is not about. Waiting out a block end to end is covered, with a budget
// that is not a stopwatch, by TestRateLimiterWaitOutlastsAShortRetryAfter.
func TestRateLimiterWaitNeverSleepsPastTheDeadline(t *testing.T) {
	previous := jitter
	jitter = func(d time.Duration) time.Duration { return d / 2 }
	t.Cleanup(func() { jitter = previous })

	assert.Equal(t, 300*time.Millisecond, sleepWithin(200*time.Millisecond, time.Second),
		"room to spare: the jitter is added")
	assert.Equal(t, 200*time.Millisecond, sleepWithin(200*time.Millisecond, 220*time.Millisecond),
		"no room: the bare wait, not the 300ms that would wake past the deadline")
	assert.Equal(t, minRefillWait*3/2, sleepWithin(time.Microsecond, time.Second),
		"a deficit of microseconds still waits out minRefillWait, jitter and all")
	assert.Equal(t, time.Microsecond, sleepWithin(time.Microsecond, time.Millisecond),
		"nor does the floor itself overshoot a budget shorter than it")
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

// heldClock is a clock a test moves, and the gate's own sleeps move it:
// pause does not wait, it advances the clock by the sleep it was asked for
// plus the lateness every real wake-up has. A gate only ever reaches a spent
// budget by waking past its deadline, and that is the one thing a test
// cannot ask a real timer for — the 20ms margin measured on an idle box and
// spent by the scheduler on a loaded one is the defect this card came from.
// Here the overshoot is a fact of the test.
type heldClock struct {
	mu sync.Mutex
	t  time.Time
}

// holdClock freezes time and hands every sleep the given lateness. The
// jitter goes to zero with it, so a sleep for a wait is exactly that wait.
func holdClock(t *testing.T, lateness time.Duration) *heldClock {
	t.Helper()
	clock := &heldClock{t: time.Now()}

	previousJitter, previousPause := jitter, pause
	jitter = func(time.Duration) time.Duration { return 0 }
	pause = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		clock.advance(d + lateness)
		return nil
	}
	t.Cleanup(func() { jitter, pause = previousJitter, previousPause })

	return clock
}

func (c *heldClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *heldClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// A budget spent on the server's block says so. Blaming the client limit
// here reads as "lower your parallelism", which is advice about a knob that
// had nothing to do with a wait the server asked of everybody.
//
// The gate sleeps a 10s block out against a 10s budget and wakes a
// millisecond late, which is the shape the CI failure in #763 had and the
// only way a spent budget is ever reached on a block.
func TestRateLimiterBudgetSpentOnTheServersBlockNamesTheServer(t *testing.T) {
	clock := holdClock(t, time.Millisecond)
	rl := NewRateLimiter(NewStore(t.TempDir()), RateLimiterConfig{})
	rl.clock = clock.Now
	start := clock.Now()
	require.NoError(t, rl.SetRetryAfter(start.Add(DefaultMaxWait)))

	err := rl.waitSince(context.Background(), start, start.Add(DefaultMaxWait))

	var gateErr *GateError
	require.ErrorAs(t, err, &gateErr)
	assert.ErrorIs(t, err, basecamp.ErrRateLimited)
	assert.Equal(t, "Rate limited by the server; waited 10s", gateErr.Message)
	assert.Equal(t, "Re-run.", gateErr.Hint)
	assert.Equal(t, start.Add(DefaultMaxWait+time.Millisecond), clock.Now(), "slept the block out and woke late")
}

// The store is shared, so the Retry-After it holds when a gate gives up is
// no evidence that this gate waited on one. Here another invocation's 429
// lands while this gate sleeps for a refill of its own — the write happens
// inside the sleep, so the order is the test's and not the scheduler's — and
// the wait our own bucket imposed keeps its own name, and the advice that
// goes with it.
func TestRateLimiterBudgetSpentOnOurOwnRefillNamesTheClientLimit(t *testing.T) {
	clock := holdClock(t, time.Millisecond)
	store := NewStore(t.TempDir())
	rl := NewRateLimiter(store, RateLimiterConfig{MaxTokens: 1, RefillRate: 1, TokensPerRequest: 1})
	rl.clock = clock.Now
	start := clock.Now()

	allowed, err := rl.Allow()
	require.NoError(t, err)
	require.True(t, allowed, "the bucket's one token")

	sleep := pause
	pause = func(ctx context.Context, d time.Duration) error {
		//nolint:contextcheck // lock acquisition is context-independent by design
		require.NoError(t, rl.SetRetryAfterDuration(30*time.Second), "someone else's 429, mid-sleep")
		return sleep(ctx, d)
	}

	// The budget ends with the refill of the token this gate is waiting for.
	err = rl.waitSince(context.Background(), start, start.Add(time.Second))

	var gateErr *GateError
	require.ErrorAs(t, err, &gateErr)
	assert.ErrorIs(t, err, basecamp.ErrRateLimited)
	assert.Equal(t, "Too many requests (client limit 1/s); waited 1s", gateErr.Message)
	assert.Equal(t, "Re-run, or lower parallelism.", gateErr.Hint)

	state, err := store.Load()
	require.NoError(t, err)
	require.True(t, state.RateLimiter.RetryAfterUntil.After(start),
		"the block really did land, and a gate reading the store back would have called this the server's")
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

// The bulkhead's give-up message counts the whole gate wait, not only the
// slot phase: a gate that spent its budget on the rate limiter and then met
// a full bulkhead reports the total.
func TestBulkheadWaitReportsTheWholeGateWait(t *testing.T) {
	store := NewStore(t.TempDir())
	occupyBulkhead(t, store, 1)
	bh := NewBulkhead(store, BulkheadConfig{MaxConcurrent: 1})
	start := time.Now().Add(-9 * time.Second)

	err := bh.waitSince(context.Background(), start, time.Now().Add(20*time.Millisecond))

	var gateErr *GateError
	require.ErrorAs(t, err, &gateErr)
	assert.Equal(t, "Too many concurrent basecamp processes (limit 1); waited 9s", gateErr.Message)
}

// slotHeldCancel is a context that reports cancellation from the moment this
// process holds a bulkhead slot: the narrow window between the slot phase
// succeeding and the circuit breaker reserving an attempt.
type slotHeldCancel struct {
	context.Context
	store *Store
}

func (c slotHeldCancel) Err() error {
	state, err := c.store.Load()
	if err == nil && state.Bulkhead.HasPID(os.Getpid()) {
		return context.Canceled
	}
	return nil
}

func TestGatingHooksReleasesTheSlotWhenCancellationLandsBeforeTheCircuit(t *testing.T) {
	store := NewStore(t.TempDir())
	require.NoError(t, store.Update(func(state *State) error {
		state.CircuitBreaker.State = CircuitHalfOpen
		return nil
	}))
	hooks := NewGatingHooksFromConfig(store, DefaultConfig())

	_, err := hooks.OnOperationGate(slotHeldCancel{context.Background(), store}, basecamp.OperationInfo{Service: "Todos", Operation: "Complete"})

	assert.ErrorIs(t, err, context.Canceled)
	state, loadErr := store.Load()
	require.NoError(t, loadErr)
	assert.Empty(t, state.Bulkhead.ActivePIDs, "slot released")
	assert.Zero(t, state.CircuitBreaker.HalfOpenAttempts, "no half-open attempt reserved")
}

func TestGateErrorUnwrapsToTheSDKSentinel(t *testing.T) {
	err := fmt.Errorf("listing projects: %w", &GateError{Message: "m", Hint: "h", sentinel: basecamp.ErrBulkheadFull})
	assert.ErrorIs(t, err, basecamp.ErrBulkheadFull)
	assert.NotErrorIs(t, err, basecamp.ErrRateLimited)
	assert.Equal(t, "listing projects: m", err.Error())
}
