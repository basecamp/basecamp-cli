//go:build linux

package cli

import (
	"math"
	"os"

	"golang.org/x/sys/unix"

	"github.com/basecamp/basecamp-cli/internal/sysfd"
)

// sealInheritedDescriptors keeps every descriptor this process inherited out
// of the processes it starts, by marking each one close-on-exec.
//
// It decides nothing. No flag, no argument and no environment variable says
// which descriptor is which, so there is nothing here that can disagree with
// the command cobra goes on to run: a connector-started worker is handed its
// task token on an inherited pipe, and by the time the command reads it, the
// descriptor has been out of reach of any child since the first line of the
// program — including the children the root command's persistent hooks may
// start while loading configuration or checking for an update.
//
// Marking a descriptor close-on-exec neither closes it nor disturbs what is
// buffered in it, so nothing is consumed for an invocation that never serves.
// Standard input, output and error are left alone: a child is meant to share
// those.
func sealInheritedDescriptors() {
	if err := unix.CloseRange(firstInheritedFD, math.MaxUint32, unix.CLOSE_RANGE_CLOEXEC); err == nil {
		return
	}
	// Kernels before 5.11 do not know CLOSE_RANGE_CLOEXEC. Ask the process
	// which descriptors it actually has and mark those.
	sealListedDescriptors()
}

// firstInheritedFD is the first descriptor that is not one of the standard
// three.
const firstInheritedFD = 3

func sealListedDescriptors() {
	dir, err := os.Open("/proc/self/fd")
	if err != nil {
		return
	}
	defer func() { _ = dir.Close() }()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return
	}
	listing, err := sysfd.Of(dir.Fd())
	if err != nil {
		return
	}
	for _, name := range names {
		fd, err := sysfd.Parse(name)
		if err != nil || fd.Int() < firstInheritedFD || fd == listing {
			continue
		}
		flags, err := unix.FcntlInt(fd.Uintptr(), unix.F_GETFD, 0)
		if err != nil {
			continue
		}
		_, _ = unix.FcntlInt(fd.Uintptr(), unix.F_SETFD, flags|unix.FD_CLOEXEC)
	}
}
