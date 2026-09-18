// Package sysfd carries a file descriptor between the three places that name
// one: a value a process is given on its command line, a *os.File or raw
// connection Go hands over as a uintptr, and the syscall wrappers that take a
// plain number. One type, so a descriptor is checked where it enters and
// passed on afterwards without every caller repeating the bounds.
package sysfd

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Descriptor is a file descriptor this process may act on: non-negative, and
// within int's range on every platform the CLI builds for. Parse and Of are
// the only ways to make one, so anything holding a Descriptor holds a number
// that converts safely.
//
// It says nothing about whether a descriptor is appropriate for a given job.
// Whether a task token may be read from a pipe rather than a file, for
// instance, belongs where the flag is read, not here.
type Descriptor int

// FirstNonStandard is the first descriptor that is not standard input,
// output or error — the one fact about which descriptors are which that the
// two sides of the credential handover both need, and are only ever right
// about together. Startup seals every descriptor from here up, because below
// it are the three a child is meant to share; the mcp command refuses a task
// token below it, because there the token would be arriving on the MCP wire
// or the log. One line, drawn once: a token accepted below where the seal
// starts would be a credential nothing had sealed.
const FirstNonStandard Descriptor = 3

// maxDescriptor is the portable ceiling: int is 32 bits on a 32-bit build, so
// a number that fits in int64 is not necessarily one this process can hold.
const maxDescriptor = math.MaxInt32

// Parse reads a descriptor a process was told about, e.g. the value of
// --connect-token-fd.
func Parse(value string) (Descriptor, error) {
	number, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a file descriptor", value)
	}
	if number < 0 || number > maxDescriptor {
		return 0, fmt.Errorf("file descriptor %d is out of range (0 to %d)", number, maxDescriptor)
	}
	return Descriptor(number), nil
}

// Of takes a descriptor Go reports as a uintptr, as os.File.Fd and the
// Control callback of a syscall.RawConn do.
func Of(fd uintptr) (Descriptor, error) {
	if fd > maxDescriptor {
		return 0, fmt.Errorf("file descriptor %d is out of range (0 to %d)", fd, maxDescriptor)
	}
	return Descriptor(fd), nil
}

// Int is what a syscall wrapper takes.
func (d Descriptor) Int() int { return int(d) }

// Uintptr is what os.NewFile and the fcntl wrappers take.
func (d Descriptor) Uintptr() uintptr {
	return uintptr(d) //nolint:gosec // G115: Parse and Of are the only ways to make a Descriptor, and both refuse a negative number
}

// String is what a process is told on a command line.
func (d Descriptor) String() string { return strconv.Itoa(int(d)) }
