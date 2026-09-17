//go:build unix

// Package drivertest is the shared way to test the connector's one-owner
// rule: a task's process tree, its working directory or worktree, and its
// ledger record have a single owner and a single release point (see the rule
// written out in internal/connector/driver/worker.go).
//
// Cards that start workers, remove worktrees or settle records use these
// helpers rather than each writing their own process fixtures.
package drivertest

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// StartTree starts a worker that forks a grandchild of its own inside the
// worker's process group, with dir as its working directory, and returns the
// worker and the grandchild's pid. Both are killed when the test ends.
//
// It is the fixture for the rule's hardest case: the leader can be gone while
// the tree it made still runs in the task's directory, so nothing may release
// that directory or settle that record until the group is confirmed gone.
func StartTree(t *testing.T, dir string) (*driver.Worker, int) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "grandchild")
	// The grandchild holds the working directory open and outlives its
	// parent, which exits at once.
	script := "cd " + dir + " && (sleep 300 & echo $! > " + pidFile + ") && exit 0"
	worker, err := driver.StartWorker(context.Background(), nil, driver.Scope{WorkDir: dir},
		driver.Command{Path: "/bin/sh", Args: []string{"-c", script}, Env: []string{"PATH=/bin:/usr/bin"}})
	if err != nil {
		t.Fatalf("start a worker tree: %v", err)
	}
	t.Cleanup(func() { worker.Terminate(time.Second) })

	var grandchild int
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, readErr := os.ReadFile(pidFile)
		if readErr == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				grandchild = pid
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the worker's grandchild never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })
	return worker, grandchild
}

// Alive reports whether a pid still names a live process.
func Alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// RequireGroupHeld fails the test unless the process group is still held,
// which is what keeps a task's directory and record its own.
func RequireGroupHeld(t *testing.T, p driver.Process) {
	t.Helper()
	if !driver.GroupMembersRemain(p) {
		t.Fatalf("process group %d is gone; the fixture cannot test the rule", p.PGID)
	}
}
