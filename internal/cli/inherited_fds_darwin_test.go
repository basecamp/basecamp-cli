//go:build darwin

package cli

import (
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// The descriptor number is high on purpose: a child picks the lowest free one
// for its own files.
const inheritedFDDarwin = 200

// inheritDarwin puts a readable pipe at fd, the way an exec'd child receives
// one: open, with no close-on-exec flag of its own.
func inheritDarwin(t *testing.T, fd int) {
	t.Helper()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	_, err = writer.WriteString("a-task-token\n")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	t.Cleanup(func() { _ = reader.Close() })

	require.NoError(t, unix.Dup2(int(reader.Fd()), fd))
	t.Cleanup(func() { _ = unix.Close(fd) })
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	require.NoError(t, err)
	require.Zero(t, flags&unix.FD_CLOEXEC, "an inherited descriptor arrives without it")
}

func cloexec(t *testing.T, fd int) bool {
	t.Helper()
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	require.NoError(t, err)
	return flags&unix.FD_CLOEXEC != 0
}

// On macOS an inherited descriptor does not reach the processes this one
// starts, and stays readable here.
func TestSealedDescriptorsDoNotReachChildrenOnMacOS(t *testing.T) {
	inheritDarwin(t, inheritedFDDarwin)

	require.NoError(t, sealInheritedDescriptors())

	assert.True(t, cloexec(t, inheritedFDDarwin))
	fd := strconv.Itoa(inheritedFDDarwin)
	out, err := exec.CommandContext(t.Context(), "/bin/sh", "-c",
		"if (: <&"+fd+") 2>/dev/null; then echo inherited; else echo sealed; fi").Output() //nolint:gosec // G204: the descriptor number is the test's own constant
	require.NoError(t, err)
	assert.Equal(t, "sealed\n", string(out), "the child inherited the descriptor")

	buffer := make([]byte, len("a-task-token\n"))
	read, err := unix.Read(inheritedFDDarwin, buffer)
	require.NoError(t, err)
	assert.Equal(t, "a-task-token\n", string(buffer[:read]), "marked, not closed or consumed")
}

// /dev/fd is how macOS lists a process's descriptors, and the walk seals what
// it names.
func TestTheDevFDWalkSealsWhatItNames(t *testing.T) {
	inheritDarwin(t, inheritedFDDarwin)
	require.NoError(t, sealListedDescriptors(devFD))
	assert.True(t, cloexec(t, inheritedFDDarwin))
}

// Without /dev/fd the fallback tries every number up to the descriptor limit,
// and seals the same descriptor.
func TestTheFallbackSealsEveryDescriptorUpToTheLimit(t *testing.T) {
	inheritDarwin(t, inheritedFDDarwin)
	limit, err := descriptorLimit()
	require.NoError(t, err)
	require.Greater(t, limit, uint64(inheritedFDDarwin))

	require.NoError(t, sealEveryDescriptorUpTo(limit))
	assert.True(t, cloexec(t, inheritedFDDarwin))
	for _, fd := range []int{0, 1, 2} {
		_, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		assert.NoError(t, err, "standard descriptor %d is left open", fd)
	}
}

// A listing that cannot be read is an error, never a quiet success.
func TestTheDevFDWalkRefusesAListingItCannotRead(t *testing.T) {
	require.Error(t, sealListedDescriptors(t.TempDir()+"/missing"))
}
