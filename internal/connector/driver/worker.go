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
	"syscall"
	"time"
)

// startTolerance is how far a process's start time, as the kernel reports it,
// may be from the time the driver recorded for it and still be the same
// process. The driver stamps the time just after the fork returns.
const startTolerance = 3 * time.Second

// pipeWaitDelay bounds how long a worker that has exited is waited on for
// pipes a stray descendant still holds.
const pipeWaitDelay = 2 * time.Second

// # One owner, one release point
//
// This is the connector's rule for a task's process tree, its working
// directory (or worktree), and its ledger record. All three belong to one
// owner — the attempt — and are released at one point, in this order:
//
//  1. Every worker starts as the leader of its own process group
//     (StartWorker), so the tree it makes can be signaled as one.
//  2. A cancel, a deadline or a shutdown ends that group: SIGTERM, a bounded
//     wait, then SIGKILL, by process group id and never by name (Terminate).
//  3. The group is then CONFIRMED gone (ConfirmGroupGone). Only after that
//     may the attempt be settled, its directory or worktree released, and its
//     record made terminal.
//  4. A group that cannot be confirmed gone — members left, a pid whose
//     identity cannot be established, a platform that cannot say — leaves the
//     record HELD: live in the ledger, its conversation and directory still
//     its own, for a person to settle. Never terminal, never released.
//  5. A restart reaps by the same rule (TerminateRecorded, then the same
//     confirmation), and asks OwnsWorker first: a pid is not an identity, so
//     ownership is the pid AND the start time recorded with it. Everything
//     that acts on a recorded worker — recovery, status, redispatch, discard,
//     hold — asks OwnsWorker rather than testing a pid of its own.
//
// The one thing this cannot cover is a descendant that leaves the group by
// calling setsid: it is outside every group signal, and the connector can
// only avoid waiting on it (WaitDelay, CloseStdout). Containment is the
// sandbox launcher's job, not this rule's.
//
// Cards that start workers, remove worktrees or settle records use the
// functions here rather than writing their own.
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
// where process start times cannot be read, OwnsWorker refuses to answer and
// nothing may be settled — the run command refuses to start there at all.
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
//     keeps only its hash. It crosses to exactly one process, the worker's
//     MCP server, and never to the agent process where that can be avoided:
//     not in the agent's environment, never in argv, never in a log or a
//     dispatch line, and never in a file under a working directory or the
//     connector's state directory. The one file that carries it today is the
//     MCP configuration the agent reads at start, written owner-only under
//     the per-user runtime directory (never the state or working directory),
//     removed as soon as the agent reports its servers started and again on
//     Close, and swept when the connector starts. When `basecamp mcp` takes
//     the token over an inherited descriptor (#736), that file stops carrying
//     it at all.
//   - The agent's own credential (ANTHROPIC_API_KEY, where one is used) is in
//     the agent's environment because the agent needs it, and nowhere else
//     the connector writes.
//
// drivertest.RequireNoSecret and RequireNoSecretFilesDuring are the checks:
// the environment, argv, written text, and — watched continuously, so a file
// that lives milliseconds is still caught — every file under the working and
// session directories after the agent's servers start.
//
// Where this can still be broken: until #736's descriptor carriage lands, the
// token is in a file for the moments between the MCP configuration being
// written and the agent's init message; and an agent may copy what it was
// handed anywhere its tools can write.
//
// ## The environment a worker and its MCP servers get
//
//   - The connector owns both. SessionConfig.Env is the worker's whole
//     environment and MCPServer.Env is each server's, and each is an
//     allowlist the dispatcher built by name (BuildEnv over BaseEnv, plus the
//     variables a driver names for its own agent).
//   - No credential of the connector's is in either: the agent's Basecamp
//     token stays in the connector, and the only secret that crosses is the
//     task token, in the MCP server's declared environment.
//   - No secret is ever in argv, which every process on the machine can read.
//
// Where this can still be broken: an agent may ADD to the environment it
// hands its MCP servers — Claude Code passes its own whole environment down,
// which carries the agent's own credentials — so the declared environment is
// a floor, not a ceiling. connector.SanitizeWorkerServerEnv is how the
// connector's own server drops everything it did not declare on arrival,
// before it authenticates or starts a helper; `basecamp mcp` (#736, which owns
// that command and is changing how it takes the task token) is where it is
// called. Until it is, the agent's own credentials reach the connector's MCP
// server by that inheritance. A third-party MCP server the operator adds to a
// worker would inherit them regardless; the connector ships none.
//
// ## When an attempt may be adopted, settled or released
//
//   - Adoption links a reply to an event; it is never evidence that work
//     finished, and never makes an outcome succeeded. It needs exactly one
//     reply by the agent at that destination after the event's own
//     acknowledgement and before any later instruction's, it is never the
//     worker's own acknowledgement, and a listing the scan limit cut short
//     adopts nothing.
//   - An attempt is settled, its directory released and its record made
//     terminal at one point (Dispatcher.release), and only after the group is
//     confirmed gone and the ledger has taken the settlement.
//   - An attempt that cannot be confirmed or cannot be settled stays live and
//     holds its conversation, its directory and one of the connector's worker
//     slots, until a person settles it.
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
	stdout  *os.File
	stderr  *tailBuffer

	done        chan struct{}
	exit        Exit
	killOnce    sync.Once
	releaseOnce sync.Once
}

// StartWorker launches cmd through launcher, in scope, as a new process group.
// An error wrapping ErrNotStarted means no process exists; StartWorker returns
// no other error.
func StartWorker(ctx context.Context, launcher Launcher, scope Scope, cmd Command) (*Worker, error) {
	if launcher == nil {
		launcher = DirectLauncher{}
	}
	launched, err := launcher.Launch(ctx, LaunchRequest{Scope: scope, Command: cmd})
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
	w.stdout = readEnd
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
	w.process = Process{PID: ec.Process.Pid, PGID: ec.Process.Pid, StartedAt: time.Now()}
	go func() {
		err := ec.Wait()
		w.exit = exitOf(ec, err)
		close(w.done)
	}()
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

// CloseStdout closes the worker's output: a reader blocked on it returns, and
// the descriptor is released.
// For a worker that is gone while a descendant that left its group still
// holds the pipe.
func (w *Worker) CloseStdout() { _ = w.stdout.Close() }

// Done is closed once the process has exited and been reaped.
func (w *Worker) Done() <-chan struct{} { return w.done }

// Exit is how it exited; meaningful once Done is closed.
func (w *Worker) Exit() Exit {
	<-w.done
	return w.exit
}

// StderrTail is the end of the worker's stderr, redacted.
func (w *Worker) StderrTail() string { return Redact(w.stderr.String()) }

// Terminate ends the process group: SIGTERM, grace, SIGKILL. It returns once
// the leader is reaped. Idempotent.
func (w *Worker) Terminate(grace time.Duration) {
	w.killOnce.Do(func() {
		_ = w.stdin.Close()
		select {
		case <-w.done:
			// The leader is gone; its group may not be.
			_ = signalGroup(w.process.PGID, syscall.SIGKILL)
			return
		default:
		}
		_ = signalGroup(w.process.PGID, syscall.SIGTERM)
		select {
		case <-w.done:
		case <-time.After(grace):
		}
		_ = signalGroup(w.process.PGID, syscall.SIGKILL)
		// The leader by its own pid as well: were it not a group leader, the
		// group signal would reach nothing and Terminate would wait forever.
		_ = w.cmd.Process.Kill()
	})
	<-w.done
	// The output pipe is the Worker's to release as well. Its reader gets the
	// same bound Wait gives a stray descendant to finish draining what the
	// worker wrote before it went, and then the descriptor is closed whether
	// or not the reader closed it.
	w.releaseOnce.Do(func() {
		time.AfterFunc(pipeWaitDelay, w.CloseStdout)
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
//     may be settled and its directory released.
//   - (false, ErrGroupOutlivedLeader): the leader is gone or is now some other
//     process, and the recorded group still has members — they may be the
//     worker's children. Nothing may be settled or released.
//   - (false, err): the identity cannot be established here (an unreadable
//     process table, a platform that cannot say). Nothing may be settled or
//     released either.
func OwnsWorker(p Process) (bool, error) {
	if p.PID <= 0 || p.PGID <= 0 || p.StartedAt.IsZero() {
		return false, nil
	}
	started, err := processStartTime(p.PID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, groupGone(p.PGID)
		}
		return false, err
	}
	if d := started.Sub(p.StartedAt); d > startTolerance || d < -startTolerance {
		return false, groupGone(p.PGID)
	}
	return true, nil
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
		if errors.Is(signalGroup(p.PGID, 0), syscall.ESRCH) {
			return true, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = signalGroup(p.PGID, syscall.SIGKILL)
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
// group. Anything else — members left, or a probe that was refused — is not
// absence, and the rule holds rather than releases.
func groupGone(pgid int) error {
	return groupProbe(pgid, signalGroup(pgid, 0))
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
// settling an attempt, releasing a working directory or removing a worktree.
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
	_ = signalGroup(p.PGID, syscall.SIGKILL)
	deadline := time.Now().Add(grace)
	for {
		err := groupGone(p.PGID)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
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
