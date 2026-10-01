//go:build darwin

package cli

// sealInheritedDescriptors keeps every descriptor this process inherited out
// of the processes it starts, by marking each one close-on-exec. It is the
// Linux one's (inherited_fds_linux.go) for macOS, where there is no
// close_range: the process's own list of descriptors is /dev/fd, and each is
// marked as the Linux fallback marks /proc/self/fd's.
//
// /dev/fd is a filesystem macOS always mounts. Where it cannot be read this
// fails closed: no other source says which descriptors are open, and the
// descriptor limit is no bound, since a parent can lower it after opening one
// above it (Copilot on #812).
func sealInheritedDescriptors() error {
	return sealListedDescriptors(devFD)
}

// devFD is where macOS lists the descriptors a process holds.
const devFD = "/dev/fd"
