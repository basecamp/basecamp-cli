//go:build unix

package commands

import (
	"errors"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/procid"
)

// Status asks about a worker an older build recorded only through the
// one-owner rule (procid): a pid is not an identity, so every question about
// a recorded worker is procid.OwnsWorker's, and nothing here tests a pid of
// its own.

// Worker states the operator commands report.
const (
	workerRunning     = "running"
	workerGone        = "gone"
	workerHeld        = "held"
	workerUnverified  = "unverified"
	workerNotRecorded = "not_recorded"
	// workerUnaccounted is the task token's holder alone: it was delivered
	// and the connector that delivered it could not name who has it.
	workerUnaccounted = "unaccounted"
)

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
// happened. The connector that delivered it held the attempt on that
// distinction, and this is where the person who must settle the held
// attempt reads it.
func recordedTakerState(t connector.TaskStatus) string {
	if t.TakerUnaccounted {
		return workerUnaccounted
	}
	return recordedProcessState(t.TakerIdentity())
}

// recordedProcessState asks the one-owner rule about a process the ledger
// recorded. It takes the identity the ledger builds (TaskStatus's own
// WorkerIdentity and TakerIdentity) rather than assembling one from parts:
// a procid.Process put together here would have no StartedExact, and the
// rule answers ErrIdentityUnknown to that — every live worker would read as
// "unverified".
func recordedProcessState(p procid.Process) string {
	if p.PID <= 0 || p.PGID <= 0 || p.StartedAt.IsZero() {
		return workerNotRecorded
	}
	switch owns, err := procid.OwnsWorker(p); {
	case errors.Is(err, procid.ErrGroupOutlivedLeader):
		return workerHeld
	case err != nil:
		return workerUnverified
	case owns:
		return workerRunning
	default:
		return workerGone
	}
}
