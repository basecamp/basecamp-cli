package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestRemoveProcessTreeHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "halcyon-remove-tree" {
		return
	}
	path := os.Args[len(os.Args)-1]
	if path == "child" {
		time.Sleep(time.Minute)
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestRemoveProcessTreeHelper$", "halcyon-remove-tree", "child") //nolint:gosec // test executable
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		os.Exit(3)
	}
	_ = child.Wait()
}

func TestRemoveCommandCancelsWindowsDescendants(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runAgentRemoveCommand(ctx, os.Args[0], "", "-test.run=^TestRemoveProcessTreeHelper$", "halcyon-remove-tree", pidFile)
		done <- err
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(pidFile); return err == nil }, 10*time.Second, 10*time.Millisecond)
	raw, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	process, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid)) //nolint:gosec // PID from this test's own child
	require.NoError(t, err)
	defer windows.CloseHandle(process)
	defer windows.TerminateProcess(process, 1)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("removal command did not return")
	}
	state, err := windows.WaitForSingleObject(process, 5000)
	require.NoError(t, err)
	require.Equal(t, uint32(windows.WAIT_OBJECT_0), state, "the wrapper's child must exit too")
}
