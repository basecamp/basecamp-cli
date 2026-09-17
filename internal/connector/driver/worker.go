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

// TerminateRecorded ends a worker a previous connector process started, by
// the process group it recorded, but only while the group's leader is still
// that process: a pid the kernel has since given to something else is left
// alone. It reports whether it signaled anything.
func TerminateRecorded(p Process, grace time.Duration) (bool, error) {
	if p.PID <= 0 || p.PGID <= 0 || p.StartedAt.IsZero() {
		return false, nil
	}
	started, err := processStartTime(p.PID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if d := started.Sub(p.StartedAt); d > startTolerance || d < -startTolerance {
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
