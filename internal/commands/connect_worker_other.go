//go:build !unix

package commands

import (
	"github.com/basecamp/basecamp-cli/internal/connector"
)

// The one-owner rule is Unix's: process groups, and a start time that makes a
// pid an identity, are what it rests on. Elsewhere the connector does not run
// (the run command refuses), and nothing here can say whether a recorded
// worker is still itself, so nothing is claimed.

const (
	workerUnverified  = "unverified"
	workerUnaccounted = "unaccounted"
)

func recordedWorkerState(connector.TaskStatus) string { return workerUnverified }

func recordedTakerState(t connector.TaskStatus) string {
	if t.TakerUnaccounted {
		return workerUnaccounted
	}
	return workerUnverified
}
