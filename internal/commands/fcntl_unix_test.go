//go:build unix

package commands

import "golang.org/x/sys/unix"

func fcntlGetFD(fd int) (int, error) { return unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0) }
