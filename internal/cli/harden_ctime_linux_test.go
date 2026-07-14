//go:build linux

package cli

import "syscall"

// ctimespec returns the inode change time from a Stat_t. The field is named
// Ctim on Linux and Ctimespec on the BSD-derived unix GOOSes, so this helper is
// build-tagged to keep the idempotence test portable.
func ctimespec(st *syscall.Stat_t) syscall.Timespec { return st.Ctim }
