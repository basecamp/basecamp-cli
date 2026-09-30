//go:build unix

package driver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
	g := &probeGroup{recorded: true, pid: pid, startedAt: time.Unix(1, 0), exact: true}
	g.cleanup()

	select {
	case <-exited:
		t.Fatal("the stranger was signaled")
	case <-time.After(300 * time.Millisecond):
	}
}

// A launcher can exit before the probe records it, leaving a child in its
// group: its start time is gone by then, and cleanup still ends the child
// (Codex on #794).
func TestProbeCleanupEndsTheChildOfALauncherThatExitedBeforeItWasRecorded(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	launcher := exec.CommandContext(t.Context(), "/bin/sh", "-c", "sleep 60 >/dev/null 2>&1 & echo $! > "+pidFile)
	g := probeInItsOwnGroup(launcher)
	require.NoError(t, launcher.Start())
	var child int
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(pidFile)
		if err != nil || !strings.HasSuffix(string(raw), "\n") {
			return false
		}
		child, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	require.Eventually(t, func() bool { _, err := processStartTime(launcher.Process.Pid); return err != nil },
		5*time.Second, 10*time.Millisecond, "the launcher has exited")

	g.started()
	_ = launcher.Wait()
	g.cleanup()

	assert.Eventually(t, func() bool { return syscall.Kill(child, 0) != nil }, 5*time.Second, 20*time.Millisecond, "the child is gone")
}
