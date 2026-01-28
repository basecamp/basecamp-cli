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
		return false
	}
	_ = windows.CloseHandle(handle)
	return true
}
