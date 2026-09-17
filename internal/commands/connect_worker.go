package commands

import (
	"errors"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// The operator commands act on a recorded worker only through the driver's
// one-owner rule (driver/worker.go): a pid is not an identity, so every
// question about a recorded worker is driver.OwnsWorker's, and nothing here
// tests a pid of its own.

// Worker states the operator commands report.
const (
	workerRunning     = "running"
	workerStopped     = "stopped"
	workerGone        = "gone"
	workerHeld        = "held"
	workerUnverified  = "unverified"
	workerNotRecorded = "not_recorded"
)

// workerStop is what stopping a replaced worker did.
type workerStop struct {
	signaled bool
	state    string
	note     string
}

// stopReplacedWorker ends the worker a redispatch replaced, first, as the
// spec asks: only while OwnsWorker says the recorded process is still that
// worker, and then confirms its group is gone. A group that outlived its
// leader, or an identity that cannot be established, is left alone and said
// so: the task's end — which admits the redispatched record — waits for the
// owner to confirm the group gone, so nothing runs twice meanwhile.
func stopReplacedWorker(p driver.Process, grace time.Duration) workerStop {
	if p.PID <= 0 || p.PGID <= 0 || p.StartedAt.IsZero() {
		// A worker still launching has no recorded process yet: there is
		// nothing this command can prove is it, so nothing is signaled.
		return workerStop{state: workerNotRecorded,
			note: "the replaced attempt had not recorded its worker process yet, so nothing was signaled; its token is retired, and the redispatch waits until that task ends"}
	}
	switch owns, err := driver.OwnsWorker(p); {
	case errors.Is(err, driver.ErrGroupOutlivedLeader):
		return workerStop{state: workerHeld,
			note: "its leader is gone but its process group still runs, so it was not signaled; its token is retired, and the redispatch waits until that group is gone"}
	case err != nil:
		return workerStop{state: workerUnverified,
			note: "the recorded worker's identity could not be established, so nothing was signaled; its token is retired: " + richtext.SanitizeSingleLine(err.Error())}
	case !owns:
		return workerStop{state: workerGone, note: "the recorded worker had already gone; nothing was signaled, and its token is retired"}
	}
	signaled, err := driver.TerminateRecorded(p, grace)
	if err != nil {
		return workerStop{state: workerUnverified,
			note: "the recorded worker could not be signaled; its token is retired: " + richtext.SanitizeSingleLine(err.Error())}
	}
	if err := driver.ConfirmGroupGone(p, grace); err != nil {
		return workerStop{signaled: signaled, state: workerHeld,
			note: "the worker was signaled but its process group did not go; the redispatch waits until it has"}
	}
	// The group is gone, but "stopped" is only said of the worker itself.
	if owns, err := driver.OwnsWorker(p); owns || err != nil {
		return workerStop{signaled: signaled, state: workerUnverified,
			note: "the recorded worker is still running outside its recorded process group, so it was not stopped; its token is retired, and the redispatch waits until that task ends"}
	}
	return workerStop{signaled: signaled, state: workerStopped}
}

// recordedWorkerState is status's answer for a live attempt's worker. It
// signals nothing.
func recordedWorkerState(t connector.TaskStatus) string {
	if t.PID <= 0 || t.PGID <= 0 || t.ProcessStartedAt == nil {
		return workerNotRecorded
	}
	switch owns, err := driver.OwnsWorker(driver.Process{PID: t.PID, PGID: t.PGID, StartedAt: *t.ProcessStartedAt}); {
	case errors.Is(err, driver.ErrGroupOutlivedLeader):
		return workerHeld
	case err != nil:
		return workerUnverified
	case owns:
		return workerRunning
	default:
		return workerGone
	}
}
