//go:build windows

package resilience

import "golang.org/x/sys/windows"

// isProcessAlive checks if a process with the given PID is still running.
// On Windows, we attempt to open the process with minimal access rights.
func isProcessAlive(pid int) bool {
	// PROCESS_QUERY_LIMITED_INFORMATION is the minimum access right
	// that allows checking if a process exists
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// OpenProcess can fail with ERROR_ACCESS_DENIED even if the process is still alive
		// (e.g., other-user/system processes). Treat access denied as alive to avoid
		// incorrectly cleaning up PIDs and weakening the bulkhead limit.
		if errno, ok := err.(windows.Errno); ok && errno == windows.ERROR_ACCESS_DENIED {
			return true
		}
		return false
	}
	_ = windows.CloseHandle(handle)
	return true
}
