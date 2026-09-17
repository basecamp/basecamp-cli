//go:build !unix

package commands

// Process presence, as much as a reader can say without taking a lock.
const (
	pidPresent = "present"
	pidAbsent  = "absent"
	pidUnknown = "unknown"
)

// processPresence cannot be answered here — the connector runs on macOS and
// Linux — so it says so rather than calling a pid absent.
func processPresence(int) string { return pidUnknown }
