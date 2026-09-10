//go:build unix

package harness

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

// codexWrapper runs script through sh as the probe's command, with a
// deadline well short of the sleep the script backgrounds, and returns the
// pid the script recorded and the error.
func codexWrapper(t *testing.T, script string, deadline time.Duration) (int, error) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	// TempDir follows TMPDIR, which may hold a space or a shell metacharacter,
	// so the path goes into the script single-quoted.
	script = strings.ReplaceAll(script, "PIDFILE", "'"+strings.ReplaceAll(pidFile, "'", `'\''`)+"'")

	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := runCodexCommand(ctx, sh, "-c", script)
		done <- err
	}()
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("runCodexCommand did not return: the deadline is not bounding the call")
	}

	raw, readErr := os.ReadFile(pidFile) //nolint:gosec // G304: path is this test's own TempDir
	require.NoError(t, readErr, "wrapper did not record the descendant's pid")
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, convErr)
	return pid, err
}

// killIfAlive reaps a descendant the probe was expected to leave behind, or
// failed to kill. It is only ever called within seconds of the spawn, for a
// process that sleeps two minutes: one that kill(pid, 0) still finds is that
// process, not a recycled pid, so this can act only on our own.
func killIfAlive(pid int) {
	if syscall.Kill(pid, 0) == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// terminated reports whether pid is gone, or is a zombie: killed, but not yet
// collected. Where this suite runs, whoever adopts the orphan reaps it at
// once, and kill(pid, 0) answers ESRCH. Under a PID 1 that does not reap —
// a container running go test as PID 1 — the group kill still worked, and
// the state field of /proc/<pid>/stat is the only place that says so; it
// follows the parenthesized comm, which may itself hold spaces or parens,
// so the last ')' ends it. Outside Linux there is no /proc, and no init that
// leaves orphans uncollected.
func terminated(pid int) bool {
	if syscall.Kill(pid, 0) == syscall.ESRCH {
		return true
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat")) //nolint:gosec // G304: /proc/<pid>/stat for a pid this test spawned
	if err != nil {
		return false
	}
	fields := strings.Fields(string(stat)[strings.LastIndexByte(string(stat), ')')+1:])
	return len(fields) > 0 && fields[0] == "Z"
}

// TestRunCodexCommandOutlivingGrandchild pins two things ten minutes of a
// hung `basecamp doctor` proved were not being enforced: the deadline, and
// that nothing survives it.
//
// The stub above replaces runCodexCommand, so nothing else here exercises the
// real one. This does. It stands in for the shape codex actually ships as on
// some machines — a wrapper script that backgrounds a longer-lived process and
// exits at once — where the grandchild keeps the inherited stdout pipe open.
// The call has to return on its own deadline rather than the grandchild's,
// and the grandchild has to be dead when it does: the wrapper exited long
// before the deadline, so only a kill aimed at the process group reaches it.
func TestRunCodexCommandOutlivingGrandchild(t *testing.T) {
	// The grandchild has to outlive the deadline by a wide margin, or the test
	// passes on the sleep ending rather than on the kill working.
	start := time.Now()
	pid, err := codexWrapper(t, "sleep 120 & echo $! > PIDFILE; exit 0", 500*time.Millisecond)
	// Only a failing run has a grandchild left to reap; a passing one has
	// already seen it gone, and a pid seen gone is nobody's to signal.
	t.Cleanup(func() {
		if t.Failed() {
			killIfAlive(pid)
		}
	})

	assert.Less(t, time.Since(start), 30*time.Second,
		"runCodexCommand blocked on a pipe held open by a surviving grandchild")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Eventually(t, func() bool { return terminated(pid) }, 5*time.Second, 50*time.Millisecond,
		"grandchild %d outlived the deadline: the process group was not killed", pid)
}

// TestRunCodexCommandEscapedDescendant covers the descendant a group kill
// cannot reach: one that started its own session and still holds the
// inherited stdout. The read has to give up on its own — the pipe is
// closed after codexWaitDelay — so the call returns on the deadline plus
// that grace, and the descendant is left alive, as documented.
func TestRunCodexCommandEscapedDescendant(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available")
	}

	start := time.Now()
	pid, err := codexWrapper(t, "setsid sleep 120 & echo $! > PIDFILE; exit 0", 500*time.Millisecond)
	t.Cleanup(func() { killIfAlive(pid) })

	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 10*time.Second,
		"runCodexCommand waited on a pipe held by a descendant outside the group")
	assert.NoError(t, syscall.Kill(pid, 0), "an escaped descendant is out of the group kill's reach by design")
}
