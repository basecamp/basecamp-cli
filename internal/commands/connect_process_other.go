//go:build !unix

package commands

import "errors"

// Process presence, as much as a reader can say without taking a lock.
const (
	pidPresent = "present"
	pidAbsent  = "absent"
	pidUnknown = "unknown"
)

// processPresence cannot be answered here — the connector runs on macOS and
// Linux — so it says so rather than calling a pid absent.
func processPresence(int) string { return pidUnknown }

// connectReexec cannot start the connector again here: it runs on macOS and
// Linux.
func connectReexec() error { return errors.New("the connector cannot restart itself on this platform") }
