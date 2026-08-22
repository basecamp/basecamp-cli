//go:build darwin || ios || freebsd || netbsd

package cli

import "syscall"

// ctimespec returns the inode change time from a Stat_t. The BSD-derived unix
// GOOSes (darwin/ios/freebsd/netbsd) name the field Ctimespec rather than
// Linux's Ctim, so this helper is build-tagged to keep the idempotence test
// portable.
func ctimespec(st *syscall.Stat_t) syscall.Timespec { return st.Ctimespec }
