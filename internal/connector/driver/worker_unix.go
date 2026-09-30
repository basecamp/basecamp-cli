//go:build unix

package driver

import (
	"os/exec"
	"syscall"
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
	return syscall.Kill(-pgid, sig)
}

// probeGroup is a probe run as the leader of a group of its own.
type probeGroup struct {
	ec      *exec.Cmd
	process Process
}

// probeInItsOwnGroup runs a probe as the leader of a group of its own, and
// ends the whole group when its context does: the probe is running then, not
// yet reaped, so its pid is still its own.
func probeInItsOwnGroup(ec *exec.Cmd) *probeGroup {
	ec.SysProcAttr = newProcessGroup()
	ec.Cancel = func() error {
		return signalGroup(ec.Process.Pid, syscall.SIGKILL)
	}
	return &probeGroup{ec: ec}
}

// started records who the probe is, by the kernel's start time for its pid,
// while it's alive.
func (g *probeGroup) started() {
	pid := g.ec.Process.Pid
	if at, err := processStartTime(pid); err == nil {
		g.process = Process{PID: pid, PGID: pid, StartedAt: at, StartedExact: true}
	}
}

// cleanup ends the probe's group once the probe is over, however it ended: a
// launcher that starts the real worker as a child must not leave that child
// behind. The probe has been reaped by then, so its pid may belong to someone
// else: the group is signaled only as signalRecordedGroup allows, which is
// while it is still the probe's (Codex on #794).
func (g *probeGroup) cleanup() {
	if g.process.PID > 0 {
		_ = signalRecordedGroup(g.process, syscall.SIGKILL)
	}
}
