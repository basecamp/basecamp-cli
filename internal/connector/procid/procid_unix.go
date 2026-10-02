//go:build unix

package procid

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
)

// OwnsWorker answers whether the process a record names is still the worker
// the record was written for. Everything that asks about a recorded worker
// asks this rather than testing a pid of its own:
//
//   - (true, nil): the process is still that worker.
//   - (false, nil): it is gone, and its group has no members left.
//   - (false, ErrGroupOutlivedLeader): the leader is gone or is now some other
//     process, and the recorded group still has members, which may be the
//     worker's children.
//   - (false, err): the identity cannot be established here (an unreadable
//     process table, a record with no kernel start time, a platform that
//     cannot say).
//
// It signals nothing: the only signal it sends is the zero signal, which asks
// whether a group exists.
func OwnsWorker(p Process) (bool, error) {
	if p.PID <= 0 || p.PGID <= 0 {
		// There is no process here to own.
		return false, nil
	}
	gone, err := processGone(p)
	if err != nil {
		return false, err
	}
	if gone {
		// The leader is gone, or its pid is somebody else's now: what is left
		// of the group decides whether anything of this worker remains.
		return false, groupGone(p.PGID)
	}
	return true, nil
}

// processGone reports whether the process a record names is gone: no process
// by that pid, a zombie, or a later process the kernel gave the same pid. It
// asks only about that process and says nothing about its group.
//
// The comparison is exact. A kernel start time is read the same way every
// time it is read, so the process that was recorded answers with the value
// recorded for it and anything else is another process. A record whose start
// time the kernel never gave (StartedExact false) is ErrIdentityUnknown
// rather than a comparison against a tolerance: under fast pid reuse a window
// wide enough to cover a wall-clock stamp is wide enough to accept a
// stranger.
func processGone(p Process) (bool, error) {
	if p.PID <= 0 {
		return true, nil
	}
	started, err := processStartTime(p.PID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No process by that pid at all: nothing of it is left, whatever
			// the record says about when it started.
			return true, nil
		}
		return false, err
	}
	if !p.StartedExact {
		return false, fmt.Errorf("%w: pid %d", ErrIdentityUnknown, p.PID)
	}
	if !started.Equal(p.StartedAt) {
		return true, nil
	}
	return false, nil
}

// LookupProcess is a live process's identity: its pid, the process group it
// leads or belongs to, and the start time that tells it from a later process
// the kernel gave the same pid. A process that is gone, or a zombie, which
// runs nothing, is os.ErrNotExist.
func LookupProcess(pid int) (Process, error) {
	if pid <= 0 {
		return Process{}, os.ErrNotExist
	}
	started, err := processStartTime(pid)
	if err != nil {
		return Process{}, err
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return Process{}, err
	}
	return Process{PID: pid, PGID: pgid, StartedAt: started, StartedExact: true}, nil
}

// groupGone reports nil only when the kernel says there is no such process
// group, or when every member it still lists is a zombie. Anything else, a
// member that runs, a listing that could not be read, or a probe that was
// refused, is not absence.
//
// A zombie answers a zero signal like a live process, and one stays a member
// until its parent waits for it, so a probe that counted zombies would see a
// finished worker as still there for as long as nobody reaped it.
func groupGone(pgid int) error {
	err := zeroSignalGroup(pgid)
	if err == nil {
		running, listErr := groupRunning(pgid)
		switch {
		case listErr != nil:
			return fmt.Errorf("%w: %d: %w", ErrGroupOutlivedLeader, pgid, listErr)
		case !running:
			return nil
		}
	}
	return groupProbe(pgid, err)
}

// groupProbe reads what a zero signal to a process group said. Only ESRCH,
// "no such process group", is proof of absence; a refusal (EPERM, from a
// group this process may not signal) is a group that is probably there and
// certainly not proven gone.
func groupProbe(pgid int, err error) error {
	switch {
	case err == nil:
		return fmt.Errorf("%w: %d", ErrGroupOutlivedLeader, pgid)
	case errors.Is(err, syscall.ESRCH):
		return nil
	default:
		return fmt.Errorf("%w: %d: %w", ErrGroupOutlivedLeader, pgid, err)
	}
}

// zeroSignalGroup sends the zero signal to every process in the group, which
// delivers nothing and says whether the group exists. A non-positive pgid is
// refused, since kill(0) and kill(-1) mean this group and every process, and
// so is the connector's own group: a worker always led a group of its own,
// so a recorded group that is this process's own is a mistake.
func zeroSignalGroup(pgid int) error {
	if pgid <= 1 || pgid == syscall.Getpgrp() {
		return syscall.EINVAL
	}
	err := syscall.Kill(-pgid, 0)
	// macOS refuses any signal to a group whose only members are zombies,
	// where Linux delivers it; nothing in such a group runs. When the
	// kernel's own listing says so, it is the absent group it is everywhere
	// else. A group that runs but may not be signaled (another user's) still
	// lists its members, and keeps the refusal. On Linux EPERM is a group
	// this process may not signal, and with /proc mounted hidepid its
	// listing cannot see that group's members to say otherwise.
	if runtime.GOOS == "darwin" && errors.Is(err, syscall.EPERM) {
		if running, listErr := groupRunning(pgid); listErr == nil && !running {
			return syscall.ESRCH
		}
	}
	return err
}
