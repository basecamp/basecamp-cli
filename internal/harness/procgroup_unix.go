//go:build unix

package harness

import (
	"os/exec"
	"syscall"
)

// startInOwnProcessGroup makes the child a process group leader, so that it
// and every descendant that stays in the group can be signaled as one unit.
func startInOwnProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the child and every descendant still in its group.
// The group ID is the child's PID and stays reserved only while a member of
// the group exists, so this must run before Wait reaps the child.
func killProcessGroup(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
