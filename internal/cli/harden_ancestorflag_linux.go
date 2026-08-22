//go:build linux

package cli

import "golang.org/x/sys/unix"

// ancestorAccessFlag is the access mode used to open the path components during
// the harden descent — every ancestor AND the final target. On Linux we use
// O_PATH so a traverse-only ancestor (execute/search but not read, e.g. /home at
// 0711 on hardened distros) can still be opened: O_PATH needs only search
// permission, not read, where O_RDONLY on a directory would fail with EACCES and
// abort the whole harden. It also lets the FINAL target be opened even when its
// owner-read bit is cleared (e.g. an euid-owned 0333/0377 dir), so an
// owner-unreadable but still group/world-writable config dir can be verified and
// tightened rather than failing the open. An O_PATH fd is still usable as the
// dirfd for the next Openat and can be Fstat'd. It cannot be Fchmod'd, so the
// target is tightened via unix.Fchmodat by name on the vetted parent fd, not by
// Fchmod on its own fd.
const ancestorAccessFlag = unix.O_PATH
