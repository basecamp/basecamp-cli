//go:build unix

package driver

import "syscall"

// newProcessGroup makes the child the leader of a new process group, so the
// whole tree it starts is signaled as one.
func newProcessGroup() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup signals every process in the group. A non-positive pgid is
// refused: kill(0) and kill(-1) mean this group and every process.
func signalGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 1 {
		return syscall.EINVAL
	}
	return syscall.Kill(-pgid, sig)
}
