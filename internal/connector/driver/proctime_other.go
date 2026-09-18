//go:build unix && !linux && !darwin

package driver

import (
	"errors"
	"time"
)

// processStartTime is unknown here, so a recorded worker is never signaled:
// a pid cannot be told from a later process that reused it.
func processStartTime(int) (time.Time, error) {
	return time.Time{}, errors.New("driver: process start times are not readable on this platform")
}

// groupRunning cannot list a group here, so a group the kernel still has is
// never proven to hold only zombies.
func groupRunning(int) (bool, error) {
	return false, errors.New("driver: process groups are not listable on this platform")
}
