//go:build unix && !linux

package cli

import "syscall"

// ancestorAccessFlag is the access mode used to open the path components during
// the harden descent — every ancestor and the final target. unix.O_PATH is a
// Linux-only flag and is not defined for every non-Linux unix GOOS by
// golang.org/x/sys/unix, so on those platforms we fall back to O_RDONLY. This
// means a traverse-only ancestor (execute but not read) will fail to open and
// abort the harden on non-Linux, and an owner-unreadable target (owner-read bit
// cleared) likewise fails its open — acceptable limitations, since Linux is the
// primary/CI platform and the one where 0711 ancestors (e.g. /home) are common.
const ancestorAccessFlag = syscall.O_RDONLY
