//go:build unix

package commands

import "golang.org/x/sys/unix"

// markCloseOnExec keeps fd out of the processes this one starts. Best effort:
// a descriptor that is not open, or not ours, is nothing to protect, and the
// command refuses it when it tries to read the token from it.
func markCloseOnExec(fd int) {
	handle := uintptr(fd) //nolint:gosec // G115: connectTokenFDArg only reports a descriptor in [3, math.MaxInt32], so this cannot wrap
	if flags, err := unix.FcntlInt(handle, unix.F_GETFD, 0); err == nil {
		_, _ = unix.FcntlInt(handle, unix.F_SETFD, flags|unix.FD_CLOEXEC)
	}
}
