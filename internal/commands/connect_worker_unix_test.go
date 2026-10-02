//go:build unix

package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/basecamp/basecamp-cli/internal/connector"
)

// A token delivered to a process the connector could not name is the one
// case the zero taker cannot express, and status is where the person who
// must settle the held attempt reads it. "Nothing took the token" and "the
// token is out and nobody can say who has it" are opposite facts, and the
// removed dispatcher held the attempt on the difference; it must not be lost
// on the operator's side of it.
func TestStatusSaysWhenATokenHolderCannotBeAccountedFor(t *testing.T) {
	assert.Equal(t, workerUnaccounted,
		recordedTakerState(connector.TaskStatus{TakerUnaccounted: true}),
		"a delivery whose holder could not be named never reads as no delivery")
	assert.Equal(t, workerNotRecorded, recordedTakerState(connector.TaskStatus{}),
		"and nothing taking the token still reads as nothing")
}
