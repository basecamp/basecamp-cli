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

// probeInItsOwnGroup runs a probe as the leader of a group of its own, and
// ends the whole group when its context does. The cleanup it returns ends
// the group once the probe is over, however it ended: a launcher that starts
// the real worker as a child must not leave that child behind, whether the
// probe timed out or the launcher exited first.
func probeInItsOwnGroup(ec *exec.Cmd) (cleanup func()) {
	ec.SysProcAttr = newProcessGroup()
	ec.Cancel = func() error {
		return signalGroup(ec.Process.Pid, syscall.SIGKILL)
	}
	return func() {
		if ec.Process != nil {
			_ = signalGroup(ec.Process.Pid, syscall.SIGKILL)
		}
	}
}
