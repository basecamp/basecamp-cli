//go:build unix

package commands

import (
	"errors"
	"syscall"
)

// Process presence, as much as a reader can say without taking a lock.
const (
	pidPresent = "present"
	pidAbsent  = "absent"
	pidUnknown = "unknown"
)

// processPresence reports whether a process with pid exists. It signals
// nothing: signal 0 only asks. A pid that exists is not proof it is the same
// process that wrote the pid down.
func processPresence(pid int) string {
	if pid <= 1 {
		return pidUnknown
	}
	switch err := syscall.Kill(pid, 0); {
	case err == nil, errors.Is(err, syscall.EPERM):
		return pidPresent
	case errors.Is(err, syscall.ESRCH):
		return pidAbsent
	default:
		return pidUnknown
	}
}
