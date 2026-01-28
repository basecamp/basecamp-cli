//go:build !windows

package resilience

import "syscall"

// isProcessAlive checks if a process with the given PID is still running.
// On Unix systems, sending signal 0 checks if the process exists.
// EPERM means the process exists but we can't signal it (still alive).
func isProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	// err == nil: process exists and we can signal it
	// err == EPERM: process exists but we can't signal it (different user)
	// err == ESRCH: process does not exist
	return err == nil || err == syscall.EPERM
}
