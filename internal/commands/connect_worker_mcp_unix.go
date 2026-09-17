//go:build unix

package commands

import (
	"fmt"
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
	fd := int(read.Fd())
	// os.Pipe marks its descriptors close-on-exec; this one must survive the
	// exec, and only this one.
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
		return fmt.Errorf("worker-mcp: keep the token descriptor across exec: %w", err)
	}
	err = syscall.Exec(exe, workerMCPArgs(exe, profile, state, fd), workerMCPEnv()) //nolint:gosec // G204: this binary, re-executed as `mcp`; no argument is a secret or content
	runtime.KeepAlive(read)
	return fmt.Errorf("worker-mcp: exec basecamp mcp: %w", err)
}
