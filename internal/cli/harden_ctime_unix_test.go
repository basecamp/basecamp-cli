//go:build unix && !linux && !darwin && !ios && !freebsd && !netbsd && !aix && !hurd

package cli

import "syscall"

// ctimespec returns the inode change time from a Stat_t. This catch-all covers
// the remaining unix GOOSes that name the field Ctim with a syscall.Timespec
// type (android, dragonfly, openbsd, solaris, illumos), so we need not
// enumerate each one. aix is excluded because its Ctim is an StTimespec_t
// rather than a syscall.Timespec, and hurd is excluded because it has no
// syscall.Stat_t; the cli package's tests do not build on those two targets.
func ctimespec(st *syscall.Stat_t) syscall.Timespec { return st.Ctim }
