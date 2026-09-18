//go:build linux

package cli

import (
	"errors"
	"fmt"
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
//
// It fails closed. This is the credential handover boundary, and a process
// that cannot say every inherited descriptor is sealed cannot say the token
// descriptor is; the error it returns stops startup rather than letting the
// program run on with a promise it did not keep.
func sealInheritedDescriptors() error {
	if err := unix.CloseRange(uint(sysfd.FirstNonStandard), math.MaxUint32, unix.CLOSE_RANGE_CLOEXEC); err == nil {
		return nil
	}
	// Kernels before 5.11 do not know CLOSE_RANGE_CLOEXEC. Ask the process
	// which descriptors it actually has and mark those.
	return sealListedDescriptors(procSelfFD)
}

// procSelfFD is where Linux lists the descriptors a process holds. It is a
// parameter of sealListedDescriptors only so a test can watch the walk refuse
// a listing it cannot trust.
const procSelfFD = "/proc/self/fd"

func sealListedDescriptors(listingDir string) error {
	dir, err := os.Open(listingDir)
	if err != nil {
		return fmt.Errorf("could not open %s to seal the descriptors this process inherited: %w", listingDir, err)
	}
	defer func() { _ = dir.Close() }()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return fmt.Errorf("could not read %s to seal the descriptors this process inherited: %w", listingDir, err)
	}
	listing, err := sysfd.Of(dir.Fd())
	if err != nil {
		return fmt.Errorf("could not seal the descriptors this process inherited: %w", err)
	}
	for _, name := range names {
		fd, err := sysfd.Parse(name)
		if err != nil {
			return fmt.Errorf("could not seal the descriptors this process inherited: %s holds %q, which is not a descriptor", listingDir, name)
		}
		if fd < sysfd.FirstNonStandard || fd == listing {
			continue
		}
		if err := sealDescriptor(fd); err != nil {
			return err
		}
	}
	return nil
}

// sealDescriptor marks one descriptor close-on-exec.
//
// A descriptor that is no longer open is the one failure that is not a
// failure: the listing is a snapshot, and a number that has been closed since
// it was taken is already out of reach of every child. Anything else means
// the descriptor is open and still inheritable, which is the thing this
// refuses to let happen quietly.
func sealDescriptor(fd sysfd.Descriptor) error {
	flags, err := unix.FcntlInt(fd.Uintptr(), unix.F_GETFD, 0)
	if err != nil {
		if errors.Is(err, unix.EBADF) {
			return nil
		}
		return fmt.Errorf("could not read the flags of inherited descriptor %s: %w", fd, err)
	}
	if flags&unix.FD_CLOEXEC != 0 {
		return nil
	}
	if _, err := unix.FcntlInt(fd.Uintptr(), unix.F_SETFD, flags|unix.FD_CLOEXEC); err != nil {
		if errors.Is(err, unix.EBADF) {
			return nil
		}
		return fmt.Errorf("could not keep inherited descriptor %s from the processes this one starts: %w", fd, err)
	}
	return nil
}
