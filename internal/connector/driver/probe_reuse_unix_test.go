//go:build unix

package driver

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A probe's cleanup runs after the probe has been reaped, when its pid may
// already lead someone else's group. That group is left alone (Codex on #794).
func TestProbeCleanupLeavesAReusedPidAlone(t *testing.T) {
	stranger := exec.CommandContext(t.Context(), "sleep", "30")
	stranger.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, stranger.Start())
	exited := make(chan struct{})
	go func() { _ = stranger.Wait(); close(exited) }()
	t.Cleanup(func() { _ = stranger.Process.Kill(); <-exited })
	pid := stranger.Process.Pid

	// The probe that had this pid started earlier: its recorded start time is
	// not the stranger's.
	g := &probeGroup{process: Process{PID: pid, PGID: pid, StartedAt: time.Unix(1, 0), StartedExact: true}}
	g.cleanup()

	select {
	case <-exited:
		t.Fatal("the stranger was signaled")
	case <-time.After(300 * time.Millisecond):
	}
}
