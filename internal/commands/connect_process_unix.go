//go:build unix

package commands

import (
	"errors"
	"syscall"
)

// processAlive reports whether a process with pid exists. It signals nothing:
// signal 0 only checks.
func processAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
