//go:build !unix

package commands

import (
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// The one-owner rule is Unix's: process groups, and a start time that makes a
// pid an identity, are what it rests on. Elsewhere the connector does not run
// (the run command refuses), and nothing here can say whether a recorded
// worker is still itself — so nothing is signaled and nothing is claimed.

const (
	workerStopped     = "stopped"
	workerHeld        = "held"
	workerUnverified  = "unverified"
	workerNotRecorded = "not_recorded"
)

type workerStop struct {
	signaled bool
	state    string
	note     string
}

func stopReplacedWorker(driver.Process, time.Duration) workerStop {
	return workerStop{state: workerUnverified,
		note: "this platform cannot establish a recorded worker's identity, so nothing was signaled; its token is retired"}
}

func recordedWorkerState(connector.TaskStatus) string { return workerUnverified }

func recordedTakerState(connector.TaskStatus) string { return workerUnverified }
