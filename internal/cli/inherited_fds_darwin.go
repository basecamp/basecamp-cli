//go:build darwin

package cli

import (
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/basecamp/basecamp-cli/internal/sysfd"
)

// sealInheritedDescriptors keeps every descriptor this process inherited out
// of the processes it starts, by marking each one close-on-exec. It is the
// Linux one's (inherited_fds_linux.go) for macOS, where there is no
// close_range: the process's own list of descriptors is /dev/fd, and each is
// marked as the Linux fallback marks /proc/self/fd's. It fails closed in the
// same way, for the same reason.
func sealInheritedDescriptors() error {
	if err := sealListedDescriptors(devFD); err == nil {
		return nil
	}
	// /dev/fd is a filesystem macOS mounts by default, and one that is not
	// there must not leave a descriptor unsealed: try every number the
	// process may hold instead, or fail closed when that number is not known.
	limit, err := descriptorLimit()
	if err != nil {
		return err
	}
	return sealEveryDescriptorUpTo(limit)
}

// devFD is where macOS lists the descriptors a process holds.
const devFD = "/dev/fd"

// maxDescriptorScan is the most descriptor numbers the fallback will try one
// by one. A limit above it is not scanned partly: that would report every
// descriptor sealed while one above the scan could still be inherited.
const maxDescriptorScan = 1 << 20

// descriptorLimit is the highest number a descriptor of this process can have,
// plus one, or an error when it cannot be known or is too large to scan.
func descriptorLimit() (uint64, error) {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		return 0, fmt.Errorf("could not seal the descriptors this process inherited: /dev/fd could not be read, and neither could the descriptor limit: %w", err)
	}
	if lim.Cur == 0 || lim.Cur > maxDescriptorScan {
		return 0, fmt.Errorf("could not seal the descriptors this process inherited: /dev/fd could not be read, and the descriptor limit (%d) is too large to check one by one", lim.Cur)
	}
	return lim.Cur, nil
}

// sealEveryDescriptorUpTo marks close-on-exec every open descriptor above
// standard error and below limit, asking each number in turn.
func sealEveryDescriptorUpTo(limit uint64) error {
	for fd := uint64(sysfd.FirstNonStandard); fd < limit; fd++ {
		d, err := sysfd.Of(uintptr(fd))
		if err != nil {
			return fmt.Errorf("could not seal the descriptors this process inherited: %w", err)
		}
		if err := sealDescriptor(d); err != nil {
			return err
		}
	}
	return nil
}
