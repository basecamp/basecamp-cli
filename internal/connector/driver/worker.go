//go:build unix

package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// pipeWaitDelay bounds how long a worker that has exited is waited on for
// pipes a stray descendant still holds.
const pipeWaitDelay = 2 * time.Second

// drainWindow is how long a reader that has been asked to stop gives the
// pipe to produce what is already on its way, idleWindow is how often a
// reader with nothing looks up to see whether it has been asked, and
// drainBudget is the longest it goes on reading after being asked. The
// windows are opened by the reader for itself: a read that finds nothing in
// one is a pipe with nothing in it.
//
// The two bound different things, and both are needed.
//
// drainWindow bounds WAITING: how long a reader sits with nothing arriving
// before it calls the pipe empty. That is how a stop ordinarily ends, within
// tens of milliseconds, and it needs no clock beyond itself.
//
// drainBudget bounds FOLLOWING: how long a reader goes on taking output that
// keeps arriving. Only one thing produces that after a worker is dead — a
// descendant outside its group writing without stop — and it is the one case
// with no other ending, so a window that begins again after every read is no
// bound at all: a descendant writing a byte before each one expires would
// hold a session open for hours. This one is absolute from the asking.
//
// Absolute is only honest because a reader registered here does nothing slow
// between reads, so the clock cannot run while the reader is working rather
// than waiting. That is a promise, and it is the whole reason this package's
// drivers write to the ledger on a goroutine of their own: a reader that
// wrote to a database here would be inside a write when the clock ran out,
// and would come back to find its own pipe closed with the worker's next
// line still in it. See Worker.ReadingDone.
//
// A count of bytes cannot stand in for either. It is too permissive on time,
// because a descendant can drip them out forever, and too strict on volume,
// because a worker inherits the pipe descriptor and may enlarge it beyond
// any size assumed here.
const (
	drainWindow = 50 * time.Millisecond
	idleWindow  = 500 * time.Millisecond
	drainBudget = 2 * time.Second
)

// output is the read end of a worker's pipe, and it belongs to whoever reads
// it for as long as they are reading. Nothing closes it underneath them:
// closing discards whatever the worker wrote that nobody has parsed yet, and
// there is no size that could be assumed safe — a worker inherits the
// descriptor and may enlarge the pipe itself, up to the host's
// pipe-max-size.
//
// So a reader is asked to stop rather than cut off, and asking touches
// nothing but a flag. The deadline on the pipe has one owner, the reader,
// which is what makes a window mean what it says: nobody else can shorten
// one, and there is no moment in which a deadline is in force whose reason
// is not yet visible.
//
// A worker is ended before its reader is asked, so everything the worker
// wrote is in the pipe by then. What is given up, past the budget, is only
// what a descendant outside the worker's group goes on writing — which the
// connector had already decided not to wait for, and which is the one case
// that has no other ending.
type output struct {
	f *os.File
	// stopped is whether the reader has been asked to stop. One field,
	// written by one call, so there is no moment in which half the state is
	// visible.
	stopped atomic.Bool
	// stopAt is when the stop was asked for, written by stop before the flag
	// that publishes it. The reader takes it from here rather than from when
	// it next looks, so the budget runs from the asking as it says it does —
	// a reader inside an idle window when the asking comes would otherwise
	// start the clock up to that window late.
	//
	// It is kept as a time.Time under a mutex rather than as nanoseconds in
	// an atomic, because a time.Time carries a monotonic reading and a
	// number does not. Rebuilt from nanoseconds, the wall clock decides the
	// budget: a forward jump ends a drain early with the worker's output
	// still buffered, and a backward one holds the session open past it.
	stopMu sync.Mutex
	stopAt time.Time
}

func (o *output) Read(p []byte) (int, error) {
	for {
		stopped := o.stopped.Load()
		window := idleWindow
		if stopped {
			left := drainBudget - time.Since(o.askedAt())
			if left <= 0 {
				// Still arriving, and no longer waited for.
				return 0, io.EOF
			}
			// Never past the budget: the last window is whatever is left of
			// it, not a whole one begun at the end.
			window = min(drainWindow, left)
		}
		if err := o.f.SetReadDeadline(time.Now().Add(window)); err != nil {
			return 0, err
		}
		n, err := o.f.Read(p)
		switch {
		case n > 0:
			return n, nil
		case !errors.Is(err, os.ErrDeadlineExceeded):
			return n, err
		case stopped:
			// A window this read opened, and nothing came.
			return 0, io.EOF
		}
		// Nothing yet, and nobody has asked. Look again.
	}
}

// stop asks the reader to end once it has what is there, and no later than
// the drain budget. It writes one field and nothing else — in particular it
// does not reach for the deadline, so it cannot cut short a window a read
// has opened — and it says the same thing however many times it is called.
func (o *output) stop() {
	// The time before the flag, so a reader that sees the flag always finds
	// a time to measure from, and the budget runs from the asking. The first
	// asking is the one that counts.
	o.stopMu.Lock()
	if o.stopAt.IsZero() {
		o.stopAt = time.Now()
	}
	o.stopMu.Unlock()
	o.stopped.Store(true)
}

// askedAt is when the stop was asked for, monotonic.
func (o *output) askedAt() time.Time {
	o.stopMu.Lock()
	defer o.stopMu.Unlock()
	return o.stopAt
}

func (o *output) close() { _ = o.f.Close() }

// # One owner, one release point
//
// This is the connector's rule for a task's process tree and its ledger
// record. Both belong to one owner — the attempt — and are released at one
// point, in this order:
//
//  1. Every worker starts as the leader of its own process group
//     (StartWorker), so the tree it makes can be signaled as one.
//  2. A cancel, a deadline or a shutdown ends that group: SIGTERM, a bounded
//     wait, then SIGKILL, by process group id and never by name (Terminate).
//  3. The group is then CONFIRMED gone (ConfirmGroupGone). Only after that
//     may the attempt be settled and its record made terminal.
//  4. A group that cannot be confirmed gone — members left, a pid whose
//     identity cannot be established, a platform that cannot say — leaves the
//     record HELD: live in the ledger, its conversation and a worker slot
//     still its own, for a person to settle. Never terminal, never released.
//  5. A restart reaps by the same rule (TerminateRecorded, then the same
//     confirmation), and asks OwnsWorker first: a pid is not an identity, so
//     ownership is the pid AND the kernel's own start time for it, compared
//     exactly. Everything that acts on a recorded worker asks OwnsWorker
//     rather than testing a pid of its own: in this card, recovery (through
//     TerminateRecorded) and the release point's second confirmation; any
//     later one — status, redispatch, discard, hold — the same way. Every
//     group signal that follows the first asks again (signalRecordedGroup):
//     ownership established before a grace period is not ownership after it,
//     because a pid freed during the grace can be leading another group by
//     the time the kill goes out.
//
// The one thing this cannot cover is a descendant that leaves the group by
// calling setsid: it is outside every group signal, and the connector can
// only avoid waiting on it (WaitDelay, CloseStdout). Containment is the
// sandbox launcher's job, not this rule's.
//
// Cards that start workers or settle records use the functions here rather
// than writing their own.
//
// # What a driver promises, and where each promise can still be broken
//
// The rule above is about the release point. These are the promises the rest
// of the boundary makes, each with the paths that can still break it named,
// so a reader does not have to take "held everywhere" on trust.
//
// ## A worker's lifetime
//
//   - After a start returns a Session, a process group exists whose leader is
//     the worker, and the connector owns it: Process() names it, and nobody
//     else may signal it.
//   - After a start returns an ERROR, no process of that session exists.
//     Either none was started, or the driver ended the one it started, whole
//     group, before returning (Driver.NewSession). ErrNotStarted says more:
//     none ever existed, so the connector may retry the start once.
//   - Cancel ends the turn, not the worker, and never blocks on a worker that
//     has stopped reading its input: it gives up instead, and says so.
//   - Close ends the session and its group — signal, bounded wait, kill — and
//     is idempotent. It never waits on the worker's cooperation.
//   - A worker that goes with a turn in flight is classified by how it went:
//     one that exited on its own with a non-zero status FAILED, and one that
//     vanished — signaled by someone else, or gone with no status the
//     connector observed — is LOST.
//   - Descriptors have an owner too. A start that fails closes every
//     descriptor it opened; a terminated worker's output pipe is closed by
//     the Worker once its reader has had the same bound to drain it that Wait
//     gives a stray descendant, whether or not the reader closed it.
//   - After a crash of the connector, the group survives. A later process
//     identifies it by OwnsWorker (pid AND recorded start time), ends it with
//     TerminateRecorded, and confirms with ConfirmGroupGone before anything
//     is settled or released.
//
// Where this can still be broken: a descendant that calls setsid leaves the
// group and no signal reaches it (there is no portable way to see it, and
// containment is the sandbox launcher's); a driver that returns an error
// after leaving a process behind breaks the start promise, which is why it is
// written on the method rather than left to each driver; and on a platform
// where process start times cannot be read — or for a worker whose start
// time the kernel would not give — OwnsWorker refuses to answer and nothing
// may be settled; the run command refuses to start on such a platform at
// all.
//
// ## Credentials
//
// Two secrets exist around a worker, and each has one carriage.
//
//   - The agent's Basecamp credential stays in the CLI's credential store. It
//     is never in any environment, argv, file or log the connector writes;
//     the worker's MCP server, running as the agent's profile, reads it from
//     that store itself.
//   - A task token lives from LaunchTask to the end of its task. The ledger
//     keeps only its hash. It crosses only to the worker's MCP server, and
//     never to the agent process: the dispatcher serves it over a unix socket
//     in the attempt's owner-only runtime directory, once per start of that
//     server (an MCP host that restarts a stdio server re-runs it, so the
//     bridge asks again) and at most connector.MaxTokenHandoffs times,
//     only to a peer of this user in the worker's process group or descended
//     from its leader (connector.ServeTaskToken), and `basecamp connect
//     worker-mcp` passes it on to `basecamp mcp` over an inherited
//     descriptor. It is never in an environment, never in argv, never in a
//     file, and never in a log or a dispatch line.
//   - The agent's own credential (ANTHROPIC_API_KEY, where one is used) is in
//     the agent's environment because the agent needs it, and nowhere else
//     the connector writes.
//
// drivertest.RequireNoSecret and RequireNoSecretFilesDuring are the checks:
// the environment, argv, written text, and — watched continuously, so a file
// that lives milliseconds is still caught — every file under the working and
// session directories. What comes back OUT of a worker is the redaction
// rule's (redact.go), and drivertest.RequireRedacted is its check.
//
// Where this can still be broken: an agent may copy what it was handed
// anywhere its tools can write, and any process of this user in the worker's
// group could take the token first — the group is the agent's own tree.
//
// ## The environment a worker and its MCP servers get
//
//   - The connector owns both. SessionConfig.Env is the worker's whole
//     environment and MCPServer.Env is each server's, and each is an
//     allowlist the dispatcher built by name (BuildEnv over BaseEnv, plus the
//     variables a driver names for its own agent).
//   - No credential is in either: the agent's Basecamp credential stays in
//     the CLI's store, and the task token travels over the socket.
//   - No secret is ever in argv, which every process on the machine can read.
//
// Where this can still be broken: an agent may ADD to the environment it
// hands its MCP servers — Claude Code passes its own whole environment down,
// which carries the agent's own credentials — so the declared environment is
// a floor, not a ceiling. The bridge (`basecamp connect worker-mcp`) execs
// `basecamp mcp` with the declared environment only, so the connector's own
// server does not keep them; a third-party MCP server the operator adds to a
// worker would inherit them regardless, and the connector ships none.
//
// ## When an attempt may be adopted, settled or released
//
//   - Adoption links a reply to an event; it is never evidence that work
//     finished, and never makes an outcome succeeded. It needs exactly one
//     reply by the agent at that destination after the event's own
//     acknowledgement and before any later instruction's, it is never the
//     worker's own acknowledgement, and a listing the scan limit cut short
//     adopts nothing.
//   - An attempt is settled and its record made terminal at one point
//     (Dispatcher.release), and only after the group is confirmed gone and
//     the ledger has taken the settlement.
//   - An attempt that cannot be confirmed or cannot be settled stays live and
//     holds its conversation and one of the connector's worker slots, until a
//     person settles it.
//
// Where this can still be broken: adoption trusts Basecamp's ordering of
// replies against this machine's clock for "after the acknowledgement", so a
// clock far behind the server's could see a reply as later than it was — the
// exactly-one rule and the acknowledgement exclusion are what keep that from
// mattering; and a person who writes to the ledger by hand can of course
// strand anything.
//
// Worker is a process a spawn driver started: the leader of its own process
// group, with its stdin and stdout piped and its stderr kept, redacted, for
// diagnosis. Every spawn driver starts its agent through StartWorker, so the
// rules for processes (invariants 1, 4 and 5) live in one place.
type Worker struct {
	cmd     *exec.Cmd
	process Process
	stdin   io.WriteCloser
	stdout  *output
	stderr  *tailBuffer

	done        chan struct{}
	exit        Exit
	killOnce    sync.Once
	releaseOnce sync.Once

	readingMu sync.Mutex
	reading   <-chan struct{}
}

// StartWorker launches cmd through cfg's launcher, in cfg's scope, as a new
// process group. An error wrapping ErrNotStarted means no process exists;
// StartWorker returns no other error.
//
// The whole of cfg is taken rather than its launcher and scope so that the
// announcement of the worker (cfg.Started, driver invariant 7) is made here,
// once, for every driver: the connector cannot arm the task token's socket
// until it knows the process group, and a driver that announced it only when
// its handshake returned would deadlock an agent whose handshake waits on
// its MCP servers.
func StartWorker(ctx context.Context, cfg SessionConfig, cmd Command) (*Worker, error) {
	launcher := cfg.Launcher
	if launcher == nil {
		launcher = DirectLauncher{}
	}
	launched, err := launcher.Launch(ctx, LaunchRequest{Scope: cfg.Scope, Command: cmd})
	if err != nil {
		return nil, fmt.Errorf("%w: launcher: %w", ErrNotStarted, err)
	}
	c := launched.Command
	if c.Path == "" {
		return nil, fmt.Errorf("%w: no command", ErrNotStarted)
	}
	if c.Env == nil {
		// exec.Cmd reads a nil Env as "inherit the connector's". A worker
		// never does (invariant 1); an empty environment is written as one.
		c.Env = []string{}
	}
	// The worker outlives the call that starts it; Terminate ends it, never
	// a context.
	ec := exec.CommandContext(context.WithoutCancel(ctx), c.Path, c.Args...) //nolint:gosec // G204: the driver's own binary and flags, never content
	ec.Dir = c.Dir
	ec.Env = c.Env
	ec.SysProcAttr = newProcessGroup()
	// A descendant that left the group (a daemon that called setsid) can
	// hold the worker's stdout or stderr open after the worker is gone. Wait
	// would block on it, and with it Terminate and every shutdown behind
	// it; past this delay the pipes are closed and the worker counts as
	// exited.
	ec.WaitDelay = pipeWaitDelay
	w := &Worker{cmd: ec, stderr: &tailBuffer{max: 8 << 10}, done: make(chan struct{})}
	ec.Stderr = w.stderr
	if w.stdin, err = ec.StdinPipe(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotStarted, err)
	}
	// Stdout is a pipe of the Worker's own, not exec's StdoutPipe: Wait
	// closes an exec pipe when the process exits, which can drop the last
	// lines a worker wrote before exiting while they are still being read.
	// This one closes only when the reader has everything, or CloseStdout.
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		// Descriptors are owned too: a start that fails closes every one it
		// opened.
		_ = w.stdin.Close()
		return nil, fmt.Errorf("%w: %w", ErrNotStarted, err)
	}
	ec.Stdout = writeEnd
	w.stdout = &output{f: readEnd}
	if err := ec.Start(); err != nil {
		// exec.Cmd.Start returns an error only when no process was created:
		// a missing binary, a bad directory, a failed fork.
		_ = w.stdin.Close()
		_ = readEnd.Close()
		_ = writeEnd.Close()
		return nil, fmt.Errorf("%w: %w", ErrNotStarted, err)
	}
	// The child has its copy; this process keeps none, so the reader sees
	// end of file once the worker and everything it started have closed it.
	_ = writeEnd.Close()
	// The kernel's own start time for this pid, not the clock: it is what
	// tells this worker from a later process the kernel gives the same pid,
	// and OwnsWorker compares against it exactly. Where the kernel cannot be
	// asked, the wall-clock stamp is kept for a person to read and the
	// identity is marked inexact: no tolerance stands in for it, because a
	// tolerance wide enough to cover a stamp taken around a fork is wide
	// enough to accept a stranger under fast pid reuse (Copilot). Nothing is
	// signaled on an inexact identity and nothing of its attempt is released.
	started, exact := time.Now(), false
	if kernel, err := processStartTime(ec.Process.Pid); err == nil {
		started, exact = kernel, true
	}
	w.process = Process{PID: ec.Process.Pid, PGID: ec.Process.Pid, StartedAt: started, StartedExact: exact}
	go func() {
		err := ec.Wait()
		w.exit = exitOf(ec, err)
		close(w.done)
	}()
	// The worker exists, and nothing has been asked of it yet: this is the
	// earliest the connector can be told its process group, and the latest it
	// can be told without a handshake that waits on the token socket waiting
	// on itself (driver invariant 7).
	if cfg.Started != nil {
		cfg.Started(w.process)
	}
	return w, nil
}

func exitOf(cmd *exec.Cmd, err error) Exit {
	state := cmd.ProcessState
	if state == nil {
		return Exit{Code: -1, Err: err}
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return Exit{Code: -1, Signaled: true}
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return Exit{Code: state.ExitCode(), Err: err}
	}
	return Exit{Code: state.ExitCode()}
}

// Process is the worker's process.
func (w *Worker) Process() Process { return w.process }

// Stdin is the worker's standard input.
func (w *Worker) Stdin() io.WriteCloser { return w.stdin }

// Stdout is the worker's standard output. Read it to end of file.
func (w *Worker) Stdout() io.Reader { return w.stdout }

// ReadingDone hands the Worker the signal that its output has been read to
// the end, so the read end of the pipe is released with the reading rather
// than on a clock.
//
// The descriptor is the Worker's to release; whether the reading is over is
// the reader's to say. Without this the Worker has to guess, and a guess is
// destructive: closing the read end discards whatever the worker wrote that
// nobody has parsed yet.
//
// A Worker with no reader registered keeps the clock, because nothing else
// would release it.
//
// Registering is two promises about the goroutine that reads: that it closes
// the channel, and that it does nothing slow between reads. The second is
// what lets the drain budget be a clock — see drainBudget — and it is why a
// driver's ledger writes belong on a goroutine of their own.
func (w *Worker) ReadingDone(done <-chan struct{}) {
	w.readingMu.Lock()
	defer w.readingMu.Unlock()
	w.reading = done
}

func (w *Worker) readingSignal() <-chan struct{} {
	w.readingMu.Lock()
	defer w.readingMu.Unlock()
	return w.reading
}

// StopReading asks the worker's reader to end once it has read what is in
// the pipe, and no later than the drain budget. For a worker that is gone
// while a descendant that left its group still holds the output open, so the
// end of file never comes: the reader is asked rather than having the pipe
// taken from under it, which would discard what the worker wrote and nobody
// has parsed. It says the same thing however many times it is called.
func (w *Worker) StopReading() { w.stdout.stop() }

// CloseStdout closes the worker's output at once, discarding whatever is in
// the pipe: for releasing the descriptor once nobody is reading it. A reader
// that is still reading is asked to stop instead.
func (w *Worker) CloseStdout() { w.stdout.close() }

// Done is closed once the process has exited and been reaped.
func (w *Worker) Done() <-chan struct{} { return w.done }

// Exit is how it exited; meaningful once Done is closed.
func (w *Worker) Exit() Exit {
	<-w.done
	return w.exit
}

// StderrTail is what may be passed on of the worker's stderr, through r
// (Redactor.Stderr): never the text verbatim.
func (w *Worker) StderrTail(r *Redactor) string { return r.Stderr(w.stderr.String()) }

// StderrLines is what may be passed on of the worker's stderr when its last
// line is not enough — a refusal the agent wrote before it wrote anything
// else — through r (Redactor.Lines): bounded in lines and in bytes, each
// sanitized, never the text verbatim.
func (w *Worker) StderrLines(r *Redactor) []string { return r.Lines(w.stderr.String()) }

// Terminate ends the process group: SIGTERM, grace, SIGKILL. It returns once
// the leader is reaped. Idempotent.
func (w *Worker) Terminate(grace time.Duration) {
	w.killOnce.Do(func() {
		_ = w.stdin.Close()
		select {
		case <-w.done:
			// The leader is gone and reaped, so its pid — which is its
			// group's id — may be the kernel's to give away: the group is
			// signaled only while it is still provably this worker's.
			_ = signalRecordedGroup(w.process, syscall.SIGKILL)
			return
		default:
		}
		_ = signalRecordedGroup(w.process, syscall.SIGTERM)
		select {
		case <-w.done:
		case <-time.After(grace):
		}
		// The worker may have exited and been reaped during the grace, so
		// ownership is established again rather than assumed from before it.
		_ = signalRecordedGroup(w.process, syscall.SIGKILL)
		// The leader by its own pid as well: were it not a group leader, the
		// group signal would reach nothing and Terminate would wait forever.
		_ = w.cmd.Process.Kill()
	})
	<-w.done
	// The output pipe is the Worker's to release as well, once its reader is
	// through it: a reader that says when it is done is waited for, because
	// closing the descriptor under it would discard what the worker wrote
	// and nobody has parsed. A worker nobody reads this way gets the same
	// bound Wait gives a stray descendant, and then the descriptor is closed
	// regardless — nothing else would release it.
	w.releaseOnce.Do(func() {
		reading := w.readingSignal()
		if reading == nil {
			time.AfterFunc(pipeWaitDelay, w.CloseStdout)
			return
		}
		// Asked before it is waited for: a descendant outside the group can
		// hold the write end open, so the end of file never comes and a
		// reader left to itself never ends — which would hold this
		// worker's descriptor, and everything waiting on the reading, for
		// as long as that descendant lived.
		w.stdout.stop()
		go func() {
			<-reading
			w.CloseStdout()
		}()
	})
}

// ErrGroupOutlivedLeader is a recorded process group whose leader is gone —
// or is a pid the kernel has since reused — while the group still has
// members. They may be the worker's own children, so the caller must not
// treat the worker as finished.
var ErrGroupOutlivedLeader = errors.New("driver: the recorded process group outlived its leader")

// OwnsWorker answers the one-owner rule's identity question: is the process
// this record names still the worker the task owns?
//
// A pid is not an identity — the kernel reuses them — so ownership is the pid
// AND the start time the owner recorded for it. Everything that acts on a
// recorded worker (recovery, status, redispatch, discard, hold) asks this
// before it acts, rather than writing its own pid check:
//
//   - (true, nil): the process is still that worker. It may be signaled.
//   - (false, nil): it is gone, and its group has no members left. Its record
//     may be settled.
//   - (false, ErrGroupOutlivedLeader): the leader is gone or is now some other
//     process, and the recorded group still has members — they may be the
//     worker's children. Nothing may be settled or released.
//   - (false, err): the identity cannot be established here (an unreadable
//     process table, a record with no kernel start time, a platform that
//     cannot say). Nothing may be settled or released either.
func OwnsWorker(p Process) (bool, error) {
	if p.PID <= 0 || p.PGID <= 0 {
		// There is no process here to own.
		return false, nil
	}
	gone, err := ProcessGone(p)
	if err != nil {
		return false, err
	}
	if gone {
		// The leader is gone, or its pid is somebody else's now: what is left
		// of the group decides whether anything of this worker remains.
		return false, groupGone(p.PGID)
	}
	return true, nil
}

// ErrIdentityUnknown is a record the connector cannot tell from a later
// process that reused its pid, because no kernel start time was ever
// recorded for it. It is not "gone" and it is not "still running": it is
// unanswerable, and the one-owner rule signals nothing and releases nothing
// on an unanswerable identity.
var ErrIdentityUnknown = errors.New("driver: the recorded process has no kernel start time, so it cannot be told from a later process that reused its pid")

// ProcessGone reports whether the process a record names is gone: no process
// by that pid, a zombie, or a later process the kernel gave the same pid. It
// asks only about that process and says nothing about its group, which is
// what a caller wants to know about a worker's MCP server — the group is the
// agent's and outlives its servers.
//
// The comparison is exact. A kernel start time is read the same way every
// time it is read, in ticks since boot, so the process that was recorded
// answers with the value recorded for it and anything else is another
// process. A record whose start time the kernel never gave (StartedExact
// false) is ErrIdentityUnknown rather than a comparison against a tolerance:
// under fast pid reuse a window wide enough to cover a wall-clock stamp is
// wide enough to accept a stranger.
//
// It is the one place the question "is this still that process?" is answered;
// OwnsWorker asks it too, and adds the group.
func ProcessGone(p Process) (bool, error) {
	if p.PID <= 0 {
		return true, nil
	}
	started, err := processStartTime(p.PID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No process by that pid at all: nothing of it is left, whatever
			// the record says about when it started.
			return true, nil
		}
		return false, err
	}
	if !p.StartedExact {
		return false, fmt.Errorf("%w: pid %d", ErrIdentityUnknown, p.PID)
	}
	if !started.Equal(p.StartedAt) {
		return true, nil
	}
	return false, nil
}

// LookupProcess is a live process's identity: its pid, the process group it
// leads or belongs to, and the start time that tells it from a later process
// the kernel gave the same pid. A process that is gone — or a zombie, which
// runs nothing — is os.ErrNotExist.
//
// It is how the connector takes the identity of a process it did not start
// but knows about, such as the MCP server that took a task token from the
// socket, which an agent may have started in a process group of its own.
func LookupProcess(pid int) (Process, error) {
	if pid <= 0 {
		return Process{}, os.ErrNotExist
	}
	started, err := processStartTime(pid)
	if err != nil {
		return Process{}, err
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return Process{}, err
	}
	return Process{PID: pid, PGID: pgid, StartedAt: started, StartedExact: true}, nil
}

// signalRecordedGroup is the one place a recorded worker's process group is
// signaled, and it establishes that the group is still that worker's every
// time — not once, before a grace period, for every signal that follows it.
// A group signal is sent by the LEADER's pid, and a pid the kernel has taken
// back can lead a group of its own: the worker this connector started a
// minute later, say, which every signal held over from the last one would
// then end.
//
// It signals in two cases and neither is an assumption:
//
//   - the recorded process is alive and is still that worker (OwnsWorker), or
//   - the worker LED the group, no live process holds its pid any more, and
//     the group still has members. Those members are the worker's own
//     orphaned children: the kernel keeps a pid allocated for as long as a
//     live process uses it as its process group id, so a group id cannot
//     change hands while anything is still in the group.
//
// Everything else signals nothing. A pid that is alive and is NOT the
// recorded process is the case this exists for: the id has changed hands,
// and any group under it is a stranger's — the worker this connector started
// a minute later, say. An identity that cannot be established at all
// (ErrIdentityUnknown, an unreadable process table) is an error the caller
// holds on rather than a signal. And a record that names a process which
// only belonged to the group (a taker, whose pid is not the group's id)
// proves nothing about the group once that process is gone.
//
// Where this can still be broken: between the observation and the signal the
// last member can exit and the kernel can give the pid away. There is no
// portable way to signal a group as one atomic act — pidfd is per process,
// not per group — so that window is the syscall pair's, and it is the reason
// the connector confirms rather than assumes.
func signalRecordedGroup(p Process, sig syscall.Signal) error {
	switch owns, err := OwnsWorker(p); {
	case owns:
	case err != nil && !errors.Is(err, ErrGroupOutlivedLeader):
		return err
	case p.PID != p.PGID || !pidUnheld(p.PID) || !GroupMembersRemain(p):
		return nil
	}
	return signalGroup(p.PGID, sig)
}

// pidUnheld reports whether no live process holds the pid: there is none, or
// what is left of one is a zombie, which runs nothing and keeps the id from
// being given away until its parent reaps it.
func pidUnheld(pid int) bool {
	_, err := processStartTime(pid)
	return errors.Is(err, os.ErrNotExist)
}

// TerminateRecorded ends a worker a previous connector process started, by
// the process group it recorded, and only while OwnsWorker says that group is
// still this task's worker: a pid the kernel has since given to something
// else is left alone. It reports whether it signaled anything.
func TerminateRecorded(p Process, grace time.Duration) (bool, error) {
	switch owns, err := OwnsWorker(p); {
	case err != nil:
		return false, err
	case !owns:
		return false, nil
	}
	if err := signalGroup(p.PGID, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		return false, err
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if groupGone(p.PGID) == nil {
			return true, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The worker may have gone during the grace and its pid been given to a
	// new group leader, so this signal asks again whose group it is.
	_ = signalRecordedGroup(p, syscall.SIGKILL)
	return true, nil
}

// GroupMembersRemain reports whether the process group still has members. It
// signals nothing: it is the observation the one-owner rule's step 3 and 4
// rest on, and what a caller asks when it must not disturb the group.
//
// A probe that cannot answer — the group exists but is not ours to signal —
// counts as members remaining, because the rule releases nothing it cannot
// prove gone.
func GroupMembersRemain(p Process) bool {
	return p.PGID > 1 && groupGone(p.PGID) != nil
}

// groupGone reports nil only when the kernel says there is no such process
// group, or when every member it still lists is a zombie. Anything else —
// a member that runs, a listing that could not be read, or a probe that was
// refused — is not absence, and the rule holds rather than releases.
//
// A zombie answers a zero-signal like a live process, and one stays a member
// until its parent waits for it. The connector's own worker is such a child
// between its exit and the Wait that reaps it, so a probe that counted
// zombies could hold a finished worker for as long as that Wait is late.
func groupGone(pgid int) error {
	err := signalGroup(pgid, 0)
	if err == nil {
		running, listErr := groupRunning(pgid)
		switch {
		case listErr != nil:
			return fmt.Errorf("%w: %d: %w", ErrGroupOutlivedLeader, pgid, listErr)
		case !running:
			return nil
		}
	}
	return groupProbe(pgid, err)
}

// groupProbe reads what a zero-signal to a process group said. Only ESRCH —
// "no such process group" — is proof of absence; a refusal (EPERM, from a
// group this process may not signal) is a group that is probably there and
// certainly not proven gone.
func groupProbe(pgid int, err error) error {
	switch {
	case err == nil:
		return fmt.Errorf("%w: %d", ErrGroupOutlivedLeader, pgid)
	case errors.Is(err, syscall.ESRCH):
		return nil
	default:
		return fmt.Errorf("%w: %d: %w", ErrGroupOutlivedLeader, pgid, err)
	}
}

// ConfirmGroupGone is step 3 of the one-owner rule: it answers whether a
// worker's process group is gone, and it is what every caller asks before
// settling an attempt.
//
// It signals the group once more — a worker that ignored SIGTERM gets SIGKILL
// — then waits up to grace for the last member to go. A group with members
// left is ErrGroupOutlivedLeader, and the zero Process (a session the
// connector cannot signal at all) is gone as far as this rule goes, since
// there is nothing of it here to own.
func ConfirmGroupGone(p Process, grace time.Duration) error {
	if p.PGID <= 0 {
		return nil
	}
	if err := groupGone(p.PGID); err == nil {
		return nil
	}
	if err := signalRecordedGroup(p, syscall.SIGKILL); err != nil {
		// The group is not proven gone and whose it is cannot be
		// established, so it is neither signaled nor confirmed: the attempt
		// is held for a person.
		return err
	}
	deadline := time.Now().Add(grace)
	// The wait backs off: each probe of a group that still has members reads
	// every process's state, and a stubborn worker must not cost a busy host
	// a full process listing twenty times a second for the whole grace.
	for wait := 50 * time.Millisecond; ; {
		err := groupGone(p.PGID)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(wait)
		if wait < 500*time.Millisecond {
			wait *= 2
		}
	}
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.max; over > 0 {
		b.buf = b.buf[over:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.ToValidUTF8(string(b.buf), "")
}
