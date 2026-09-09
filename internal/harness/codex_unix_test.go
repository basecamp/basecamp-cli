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
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}

	// The grandchild has to outlive the deadline by a wide margin, or the test
	// passes on the sleep ending rather than on the kill working. The cleanup
	// reaps it if the kill did not, so a failing run leaves no orphan behind.
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	script := "sleep 120 & echo $! > " + pidFile + "; exit 0"

	grandchild := func() (int, bool) {
		raw, readErr := os.ReadFile(pidFile) //nolint:gosec // G304: path is this test's own TempDir
		if readErr != nil {
			return 0, false
		}
		pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
		return pid, convErr == nil
	}
	t.Cleanup(func() {
		if pid, ok := grandchild(); ok {
			if proc, findErr := os.FindProcess(pid); findErr == nil {
				_ = proc.Kill()
			}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := runCodexCommand(ctx, sh, "-c", script)
		done <- err
	}()

	select {
	case err := <-done:
		assert.Less(t, time.Since(start), 30*time.Second,
			"runCodexCommand blocked on a pipe held open by a surviving grandchild")
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(30 * time.Second):
		t.Fatal("runCodexCommand did not return: the deadline is not bounding the call")
	}

	pid, ok := grandchild()
	require.True(t, ok, "wrapper did not record the grandchild's pid")
	assert.Eventually(t, func() bool {
		return syscall.Kill(pid, 0) == syscall.ESRCH
	}, 5*time.Second, 50*time.Millisecond,
		"grandchild %d outlived the deadline: the process group was not killed", pid)
}
