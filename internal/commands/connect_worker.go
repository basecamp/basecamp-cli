//go:build unix

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
	// workerUnaccounted is the task token's holder alone: it was delivered
	// and the connector cannot name who has it (connector.TokenHolder).
	workerUnaccounted = "unaccounted"
)

// workerOps are the driver's one-owner functions stopReplacedWorker uses. A
// test seam, so the branches that must not signal can be exercised without a
// real process group whose id nothing reserves. Production only reads it; a
// test that replaces it must not run in parallel.
var workerOps = struct {
	owns      func(driver.Process) (bool, error)
	terminate func(driver.Process, time.Duration) (bool, error)
	confirm   func(driver.Process, time.Duration) error
}{driver.OwnsWorker, driver.TerminateRecorded, driver.ConfirmGroupGone}

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
	switch owns, err := workerOps.owns(p); {
	case errors.Is(err, driver.ErrGroupOutlivedLeader):
		return workerStop{state: workerHeld,
			note: "its leader is gone but its process group still runs, so it was not signaled; its token is retired, and the redispatch waits until that group is gone"}
	case err != nil:
		return workerStop{state: workerUnverified,
			note: "the recorded worker's identity could not be established, so nothing was signaled; its token is retired: " + richtext.SanitizeSingleLine(err.Error())}
	case !owns:
		return workerStop{state: workerGone, note: "the recorded worker had already gone; nothing was signaled, and its token is retired"}
	}
	signaled, err := workerOps.terminate(p, grace)
	if err != nil {
		return workerStop{state: workerUnverified,
			note: "the recorded worker could not be signaled; its token is retired: " + richtext.SanitizeSingleLine(err.Error())}
	}
	if err := workerOps.confirm(p, grace); err != nil {
		return workerStop{signaled: signaled, state: workerHeld,
			note: "the worker was signaled but its process group did not go; the redispatch waits until it has"}
	}
	// The group is gone, but "stopped" is only said of the worker itself.
	if owns, err := workerOps.owns(p); owns || err != nil {
		return workerStop{signaled: signaled, state: workerUnverified,
			note: "the recorded worker is still running outside its recorded process group, so it was not stopped; its token is retired, and the redispatch waits until that task ends"}
	}
	return workerStop{signaled: signaled, state: workerStopped}
}

// recordedWorkerState is status's answer for a live attempt's worker. It
// signals nothing.
func recordedWorkerState(t connector.TaskStatus) string {
	return recordedProcessState(t.WorkerIdentity())
}

// recordedTakerState is the same answer for the process the task token went
// to — a worker's MCP server, which lives in a group of its own, so it can
// outlive the worker that started it and still hold the task's token.
//
// A holder the connector could not account for is answered before the kernel
// is asked anything: there is no pid to ask about, and "not recorded" would
// read as nothing having taken the token, which is the opposite of what
// happened. It is the same distinction the release point acts on
// (connector.TokenHolder), and this is where the person who must settle the
// held attempt reads it.
func recordedTakerState(t connector.TaskStatus) string {
	if t.TakerUnaccounted {
		return workerUnaccounted
	}
	return recordedProcessState(t.TakerIdentity())
}

// recordedProcessState asks the one-owner rule about a process the ledger
// recorded. It takes the identity the ledger builds (TaskStatus's own
// WorkerIdentity and TakerIdentity) rather than assembling one from parts:
// a driver.Process put together here would have no StartedExact, and the
// rule answers ErrIdentityUnknown to that — every live worker would read as
// "unverified".
func recordedProcessState(p driver.Process) string {
	if p.PID <= 0 || p.PGID <= 0 || p.StartedAt.IsZero() {
		return workerNotRecorded
	}
	switch owns, err := driver.OwnsWorker(p); {
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
