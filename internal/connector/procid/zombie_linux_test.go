package procid

import (
	"context"
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

// startUnreaped starts script as the leader of its own group and never waits
// for it until the test ends, the way a worker sat between its exit and the
// Wait that reaped it. The script runs once stdin closes.
func startUnreaped(t *testing.T, script string) (*exec.Cmd, Process) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "read _; "+script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	started, err := processStartTime(cmd.Process.Pid)
	require.NoError(t, err)
	p := Process{PID: cmd.Process.Pid, PGID: cmd.Process.Pid, StartedAt: started}
	require.NoError(t, stdin.Close())
	require.Eventually(t, func() bool {
		st, err := readProcStat(p.PID)
		return err == nil && st.state == 'Z'
	}, 5*time.Second, 10*time.Millisecond, "the leader exits and is left unreaped")
	return cmd, p
}

// Coordinator: a zombie answers a zero-signal like a live process. A group
// whose only member is the connector's own unreaped child is gone.
func TestAGroupOfOnlyAnUnreapedLeaderIsGone(t *testing.T) {
	_, p := startUnreaped(t, "exit 0")

	assert.NoError(t, groupGone(p.PGID))

	owns, err := OwnsWorker(p)
	assert.False(t, owns, "a zombie is not the worker")
	assert.NoError(t, err)
}

// A zombie leader does not make a live member absent.
func TestAnUnreapedLeaderWithALiveChildIsStillHeld(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child")
	_, p := startUnreaped(t, "sleep 30 & echo $! > "+pidFile+"; exit 0")
	var child int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		child, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)

	assert.ErrorIs(t, groupGone(p.PGID), ErrGroupOutlivedLeader)
	owns, err := OwnsWorker(p)
	assert.False(t, owns)
	assert.ErrorIs(t, err, ErrGroupOutlivedLeader)
	assert.True(t, alive(child))
}
