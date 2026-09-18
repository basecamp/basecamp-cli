//go:build linux

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// standardDescriptors are stdin, stdout and stderr by number, written out
// here so these tests do not agree with the code they check about which
// descriptors those are.
var standardDescriptors = []int{0, 1, 2}

// The descriptor number is high on purpose: a child picks the lowest free one
// for its own files, so a low number could be in a listing for a reason that
// has nothing to do with inheritance.
const inheritedFD = 200

// An inherited descriptor does not reach the processes this one starts.
func TestSealedDescriptorsDoNotReachChildren(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no shell to start a child with")
	}
	inherit(t, inheritedFD)

	require.NoError(t, sealInheritedDescriptors())

	out, err := exec.CommandContext(t.Context(), shell, "-c", "ls /proc/self/fd").Output() //nolint:gosec // G204: a listing of the child's own descriptors
	require.NoError(t, err)
	assert.NotContains(t, strings.Fields(string(out)), "200", "the child inherited the descriptor")
	assert.True(t, fdIsOpen(inheritedFD), "and this process still has it")
}

// It is marked, not closed, and nothing buffered in it is consumed: the
// command that the descriptor is for still reads what was written to it.
func TestSealingLeavesTheDescriptorReadable(t *testing.T) {
	inherit(t, inheritedFD)

	require.NoError(t, sealInheritedDescriptors())

	flags, err := unix.FcntlInt(uintptr(inheritedFD), unix.F_GETFD, 0)
	require.NoError(t, err)
	assert.NotZero(t, flags&unix.FD_CLOEXEC)
	buffer := make([]byte, len("a-task-token\n"))
	read, err := unix.Read(inheritedFD, buffer)
	require.NoError(t, err)
	assert.Equal(t, "a-task-token\n", string(buffer[:read]))
}

// Kernels before 5.11 have no CLOSE_RANGE_CLOEXEC, and the walk that stands in
// for it there is the same promise.
func TestSealingWithoutCloseRange(t *testing.T) {
	inherit(t, inheritedFD)

	require.NoError(t, sealListedDescriptors(procSelfFD))

	flags, err := unix.FcntlInt(uintptr(inheritedFD), unix.F_GETFD, 0)
	require.NoError(t, err)
	assert.NotZero(t, flags&unix.FD_CLOEXEC)
}

// Standard input, output and error are a child's to share.
func TestSealingLeavesTheStandardDescriptors(t *testing.T) {
	shareStandardDescriptors(t)
	before := standardFlags(t)

	require.NoError(t, sealInheritedDescriptors())

	assert.Equal(t, before, standardFlags(t))
}

// inherit puts a readable pipe at fd, the way an exec'd child receives one:
// open, with no close-on-exec flag of its own.
func inherit(t *testing.T, fd int) {
	t.Helper()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	_, err = writer.WriteString("a-task-token\n")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	t.Cleanup(func() { _ = reader.Close() })

	require.NoError(t, unix.Dup3(int(reader.Fd()), fd, 0))
	t.Cleanup(func() { _ = unix.Close(fd) })
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	require.NoError(t, err)
	require.Zero(t, flags&unix.FD_CLOEXEC, "an inherited descriptor arrives without it")
}

func fdIsOpen(fd int) bool {
	_, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	return err == nil
}

// shareStandardDescriptors puts stdin, stdout and stderr in the state a child
// inherits them in, so the test is about what sealing leaves alone rather than
// about how the test binary happened to be started.
func shareStandardDescriptors(t *testing.T) {
	t.Helper()
	for _, fd := range standardDescriptors {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		require.NoError(t, err)
		_, err = unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags&^unix.FD_CLOEXEC)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags) })
	}
}

func standardFlags(t *testing.T) []int {
	t.Helper()
	flags := make([]int, 0, len(standardDescriptors))
	for _, fd := range standardDescriptors {
		got, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		require.NoError(t, err)
		flags = append(flags, got)
	}
	return flags
}

// The walk that stands in for CLOSE_RANGE_CLOEXEC says so when it cannot read
// the listing it works from. Returning quietly would leave the caller
// believing every inherited descriptor was sealed when none of them were.
func TestSealingRefusesWhenItCannotListTheDescriptors(t *testing.T) {
	err := sealListedDescriptors(filepath.Join(t.TempDir(), "no-such-listing"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "seal the descriptors this process inherited")
}

// A listing this process cannot make sense of is not a listing it may skip:
// an entry that is not a descriptor number means the walk cannot say which
// descriptors it covered.
func TestSealingRefusesAListingItCannotRead(t *testing.T) {
	listing := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(listing, "not-a-descriptor"), nil, 0o600))

	err := sealListedDescriptors(listing)

	require.Error(t, err)
	assert.Contains(t, err.Error(), `"not-a-descriptor"`)
}

// A descriptor named by the listing but no longer open is the one failure
// that is not one: it is already out of reach of every child. The listing is
// a snapshot, so this happens whenever anything closes a descriptor while the
// walk runs.
func TestSealingAcceptsADescriptorThatHasSinceBeenClosed(t *testing.T) {
	listing := t.TempDir()
	closedFD := inheritedFD + 1
	require.False(t, fdIsOpen(closedFD), "the test needs a descriptor number nothing holds")
	require.NoError(t, os.WriteFile(filepath.Join(listing, strconv.Itoa(closedFD)), nil, 0o600))

	assert.NoError(t, sealListedDescriptors(listing))
}

// The walk seals what the listing names, so a listing standing in for
// /proc/self/fd reaches the same descriptor the real one would.
func TestSealingSealsWhatTheListingNames(t *testing.T) {
	inherit(t, inheritedFD)
	listing := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(listing, strconv.Itoa(inheritedFD)), nil, 0o600))

	require.NoError(t, sealListedDescriptors(listing))

	flags, err := unix.FcntlInt(uintptr(inheritedFD), unix.F_GETFD, 0)
	require.NoError(t, err)
	assert.NotZero(t, flags&unix.FD_CLOEXEC)
}
