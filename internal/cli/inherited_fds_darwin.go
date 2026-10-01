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
	// process may hold instead.
	return sealEveryDescriptorUpTo(descriptorLimit())
}

// devFD is where macOS lists the descriptors a process holds.
const devFD = "/dev/fd"

// descriptorLimit is the highest number a descriptor of this process can have,
// plus one.
func descriptorLimit() uint64 {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim); err != nil || lim.Cur == 0 || lim.Cur > 1<<20 {
		return 1 << 16
	}
	return lim.Cur
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
