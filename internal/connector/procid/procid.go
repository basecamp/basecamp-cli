// Package procid tells a process the ledger or a lock file recorded from a
// later process the kernel gave the same pid.
//
// A pid is not an identity: the kernel reuses them. An identity is the pid
// AND the kernel's own start time for it, compared exactly, and the questions
// here answer only on that:
//
//   - LookupProcess takes the identity of a live process, such as the one
//     named in the instance lock's metadata.
//   - OwnsWorker answers whether a worker an older build recorded in the
//     ledger is still that worker, and whether anything of its process group
//     is left. Status reads a worker-era task's worker and taker through it.
//
// Where the kernel will not give a start time, or a record carries none,
// nothing here guesses: the answer is ErrIdentityUnknown, never "gone".
package procid

import (
	"errors"
	"time"
)

// Process is a process the connector recorded.
type Process struct {
	// PID is the process's id; zero when there is none.
	PID int
	// PGID is its process group. A worker led a group of its own, so for a
	// worker PGID == PID.
	PGID int
	// StartedAt is when the process started, to tell it from a later one
	// that reused its id.
	StartedAt time.Time
	// StartedExact is true when StartedAt is the kernel's own start time for
	// the pid, which is what an identity is compared by. False is a
	// wall-clock stamp, or no stamp at all: readable, but not an identity,
	// and OwnsWorker refuses to answer on one (ErrIdentityUnknown).
	StartedExact bool
}

// ErrGroupOutlivedLeader is a recorded process group whose leader is gone,
// or is a pid the kernel has since reused, while the group still has
// members. They may be the worker's own children, so the caller must not
// treat the worker as finished.
var ErrGroupOutlivedLeader = errors.New("procid: the recorded process group outlived its leader")

// ErrIdentityUnknown is a record the connector cannot tell from a later
// process that reused its pid, because no kernel start time was ever
// recorded for it. It is not "gone" and it is not "still running": it is
// unanswerable.
var ErrIdentityUnknown = errors.New("procid: the recorded process has no kernel start time, so it cannot be told from a later process that reused its pid")
