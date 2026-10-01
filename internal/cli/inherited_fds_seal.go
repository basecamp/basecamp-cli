//go:build linux || darwin

package cli

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"github.com/basecamp/basecamp-cli/internal/sysfd"
)

// sealListedDescriptors marks close-on-exec every descriptor listingDir names:
// /proc/self/fd on Linux, /dev/fd on macOS, each the process's own list of
// what it holds.
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
