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

	done     chan struct{}
	exit     Exit
	killOnce sync.Once
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
		return nil, fmt.Errorf("%w: %w", ErrNotStarted, err)
	}
	ec.Stdout = writeEnd
	w.stdout = readEnd
	if err := ec.Start(); err != nil {
		// exec.Cmd.Start returns an error only when no process was created:
		// a missing binary, a bad directory, a failed fork.
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
func GroupMembersRemain(p Process) bool {
	return p.PGID > 1 && signalGroup(p.PGID, 0) == nil
}

// groupGone reports nil when the recorded group has no members left, and
// ErrGroupOutlivedLeader when it still has some: a leader that exited does
// not take its group with it.
func groupGone(pgid int) error {
	if err := signalGroup(pgid, 0); err == nil {
		return fmt.Errorf("%w: %d", ErrGroupOutlivedLeader, pgid)
	}
	return nil
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
