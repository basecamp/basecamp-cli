//go:build unix

package commands

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

// execWorkerMCP puts the token on a pipe the next program inherits and
// replaces this process with `basecamp mcp`, which reads it and closes the
// descriptor before it authenticates.
func execWorkerMCP(exe, profile, state, token string) error {
	read, write, err := os.Pipe()
	if err != nil {
		return err
	}
	if _, err := write.WriteString(token); err != nil {
		return err
	}
	if err := write.Close(); err != nil {
		return err
	}
	// os.Pipe marks its descriptors close-on-exec; this one must survive the
	// exec, and only this one. FcntlInt takes the descriptor as the uintptr
	// Fd already is, so nothing is converted to reach it.
	if _, err := unix.FcntlInt(read.Fd(), unix.F_SETFD, 0); err != nil {
		return fmt.Errorf("worker-mcp: keep the token descriptor across exec: %w", err)
	}
	// The number the next program is told to read. A descriptor is a small
	// non-negative index the kernel handed out, but it arrives as a uintptr,
	// so the range is checked rather than assumed.
	raw := read.Fd()
	if raw > math.MaxInt32 {
		return fmt.Errorf("worker-mcp: the token descriptor (%d) is not a number a process can be told", raw)
	}
	fd := int(int32(raw))
	err = syscall.Exec(exe, workerMCPArgs(exe, profile, state, fd), workerMCPEnv()) //nolint:gosec // G204: this binary, re-executed as `mcp`; no argument is a secret or content
	runtime.KeepAlive(read)
	return fmt.Errorf("worker-mcp: exec basecamp mcp: %w", err)
}
