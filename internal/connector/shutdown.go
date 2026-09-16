package connector

import (
	"os"
	"os/signal"
	"syscall"
)

// Exit codes for a signaled shutdown. 128+n is the shell's convention, and a
// supervisor reading the connector's exit status needs to tell an interrupt
// from a termination from a crash.
const (
	// ExitInterrupted is 128 + SIGINT.
	ExitInterrupted = 130
	// ExitTerminated is 128 + SIGTERM.
	ExitTerminated = 143
)

// ExitCodeForSignal maps a shutdown signal to the exit code the connector
// leaves behind. An unknown signal reports 1: the process ended, and pretending
// it ended cleanly would be a lie to whatever restarts it.
func ExitCodeForSignal(sig os.Signal) int {
	switch sig {
	case os.Interrupt, syscall.SIGINT:
		return ExitInterrupted
	case syscall.SIGTERM:
		return ExitTerminated
	default:
		return 1
	}
}

// NotifyShutdown returns a channel carrying the first shutdown signal, and a
// stop function. Separated from the exit-code mapping so the mapping can be
// tested without sending real signals to the test binary.
func NotifyShutdown() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	return ch, func() { signal.Stop(ch) }
}
