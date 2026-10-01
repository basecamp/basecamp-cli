//go:build unix

package driver

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// newProcessGroup makes the child the leader of a new process group, so the
// whole tree it starts is signaled as one.
func newProcessGroup() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// OwnProcessGroup is the connector's own process group, which nothing of a
// worker's is ever in: every worker leads a group of its own.
func OwnProcessGroup() (int, bool) { return syscall.Getpgrp(), true }

// signalGroup signals every process in the group. A non-positive pgid is
// refused — kill(0) and kill(-1) mean this group and every process — and so
// is the connector's own group: every worker leads a group of its own
// (Setpgid), so a recorded group that is this process's own is a mistake, and
// signaling it would end the connector and everything it is supervising.
func signalGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 1 || pgid == syscall.Getpgrp() {
		return syscall.EINVAL
	}
	err := syscall.Kill(-pgid, sig)
	if errors.Is(err, syscall.EPERM) {
		// macOS refuses any signal to a group whose only members are
		// zombies, where Linux delivers it; nothing in such a group runs.
		// When the kernel's own listing says so, it is the absent group it
		// is everywhere else. A group that runs but may not be signaled
		// (another user's) still lists its members, and keeps the refusal.
		if running, listErr := groupRunning(pgid); listErr == nil && !running {
			return syscall.ESRCH
		}
	}
	return err
}

// probeGroup is a probe run as the leader of a group of its own.
type probeGroup struct {
	ec *exec.Cmd

	mu        sync.Mutex
	recorded  bool // started has run: the probe may be waited for, and reaped
	pid       int
	startedAt time.Time
	exact     bool // startedAt is the kernel's, for the pid while it was the probe
}

// probeInItsOwnGroup runs a probe as the leader of a group of its own, and
// ends the whole group when its context does.
func probeInItsOwnGroup(ec *exec.Cmd) *probeGroup {
	g := &probeGroup{ec: ec}
	ec.SysProcAttr = newProcessGroup()
	ec.Cancel = g.kill
	return g
}

// started records who the probe is while it can't have been reaped: RunProbe
// calls it after Start and before Wait. A launcher that has already exited is
// a zombie with no start time to read; its pid is recorded all the same.
func (g *probeGroup) started() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pid = g.ec.Process.Pid
	if at, err := processStartTime(g.pid); err == nil {
		g.startedAt, g.exact = at, true
	}
	g.recorded = true
}

// cleanup ends the probe's group once the probe is over, however it ended: a
// launcher that starts the real worker as a child must not leave that child
// behind.
func (g *probeGroup) cleanup() { _ = g.kill() }

// kill ends the probe's group, and only ever the probe's (Codex on #794). It
// runs from the context watcher, which can fire after Wait has reaped the
// probe, and from cleanup, which always does, so the pid may since have been
// given to someone else:
//   - not yet recorded, the probe hasn't been waited for, so its pid is still
//     its own;
//   - a leader still running is killed only while its start time says it is
//     the probe;
//   - once the leader is gone, only its group's leftovers are: while a group
//     has members the kernel won't give its id to another process (Linux and
//     the BSDs keep a pid in use as a process group id), so a pid nobody holds
//     with members left in its group is the probe's group, and a pid someone
//     holds belongs to whoever reused it.
func (g *probeGroup) kill() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.recorded {
		if g.ec.Process == nil {
			return nil // it never started
		}
		return signalGroup(g.ec.Process.Pid, syscall.SIGKILL)
	}
	if g.exact {
		gone, err := ProcessGone(Process{PID: g.pid, PGID: g.pid, StartedAt: g.startedAt, StartedExact: true})
		if err == nil && !gone {
			return signalGroup(g.pid, syscall.SIGKILL)
		}
	}
	if pidUnheld(g.pid) && GroupMembersRemain(Process{PID: g.pid, PGID: g.pid}) {
		return signalGroup(g.pid, syscall.SIGKILL)
	}
	return nil
}
