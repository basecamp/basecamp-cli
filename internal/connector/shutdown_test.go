package connector

import (
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExitCodeForSignal(t *testing.T) {
	assert.Equal(t, ExitInterrupted, ExitCodeForSignal(os.Interrupt))
	assert.Equal(t, ExitInterrupted, ExitCodeForSignal(syscall.SIGINT))
	assert.Equal(t, ExitTerminated, ExitCodeForSignal(syscall.SIGTERM))
	assert.Equal(t, 1, ExitCodeForSignal(syscall.SIGHUP),
		"an unrecognized end is not a clean one")
}
