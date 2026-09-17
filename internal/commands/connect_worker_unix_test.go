//go:build unix

package commands

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/drivertest"
)

func taskOf(p driver.Process) connector.TaskStatus {
	started := p.StartedAt
	return connector.TaskStatus{PID: p.PID, PGID: p.PGID, ProcessStartedAt: &started}
}

// runningTree starts a worker whose leader keeps running beside a child in
// its group, and returns it and the child's pid.
func runningTree(t *testing.T) (*driver.Worker, int) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "child")
	worker, err := driver.StartWorker(context.Background(), nil, driver.Scope{WorkDir: t.TempDir()},
		driver.Command{Path: "/bin/sh", Args: []string{"-c", "sleep 300 & echo $! > " + pidFile + "; wait"}, Env: []string{"PATH=/bin:/usr/bin"}})
	if err != nil {
		t.Fatalf("start a worker: %v", err)
	}
	t.Cleanup(func() { worker.Terminate(time.Second) })
	var child int
	deadline := time.Now().Add(5 * time.Second)
	for child == 0 {
		if data, err := os.ReadFile(pidFile); err == nil {
			child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		if time.Now().After(deadline) {
			t.Fatal("the worker's child never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	return worker, child
}

// A redispatch stops the worker it replaces, its whole tree, and says so.
func TestRedispatchStopsTheReplacedWorkersTree(t *testing.T) {
	worker, grandchild := runningTree(t)
	p := worker.Process()
	assert.Equal(t, workerRunning, recordedWorkerState(taskOf(p)))

	got := stopReplacedWorker(p, 2*time.Second)
	assert.Equal(t, workerStopped, got.state, got.note)
	assert.True(t, got.signaled)
	assert.Eventually(t, func() bool { return !drivertest.Alive(grandchild) }, 5*time.Second, 20*time.Millisecond, "the grandchild went with its group")
	assert.Equal(t, workerGone, recordedWorkerState(taskOf(p)))
}

// A tree that outlived its leader is not proven this task's to signal: it is
// held, left running, and reported.
func TestRedispatchLeavesATreeThatOutlivedItsLeaderHeld(t *testing.T) {
	p, grandchild := drivertest.SurvivingWorker(t, t.TempDir())
	assert.Equal(t, workerHeld, recordedWorkerState(taskOf(p)))

	got := stopReplacedWorker(p, time.Second)
	assert.Equal(t, workerHeld, got.state)
	assert.False(t, got.signaled)
	assert.True(t, drivertest.Alive(grandchild), "nothing was signaled")
	drivertest.RequireGroupHeld(t, p)
}

// A recorded start time that is not the process's own is not this worker,
// whatever the pid says.
func TestAPidAloneIsNotTheWorker(t *testing.T) {
	worker, grandchild := runningTree(t)
	p := worker.Process()
	p.StartedAt = p.StartedAt.Add(-time.Hour)

	assert.NotEqual(t, workerRunning, recordedWorkerState(taskOf(p)))
	got := stopReplacedWorker(p, time.Second)
	assert.False(t, got.signaled)
	assert.True(t, drivertest.Alive(grandchild))
}

// A worker still launching has recorded no process: nothing is signaled, and
// it is not called gone.
func TestRedispatchDoesNotCallAnUnrecordedWorkerGone(t *testing.T) {
	got := stopReplacedWorker(driver.Process{}, time.Second)
	assert.Equal(t, workerNotRecorded, got.state)
	assert.False(t, got.signaled)
}

// A worker whose recorded group holds nothing is not called stopped while it
// still runs.
func TestRedispatchDoesNotCallAWorkerOutsideItsGroupStopped(t *testing.T) {
	worker, _ := runningTree(t)
	p := worker.Process()
	// A group with no members: one this test started and has already reaped,
	// never an arbitrary number that could name someone else's group.
	gone, err := driver.StartWorker(context.Background(), nil, driver.Scope{WorkDir: t.TempDir()},
		driver.Command{Path: "/bin/sh", Args: []string{"-c", "exit 0"}, Env: []string{"PATH=/bin:/usr/bin"}})
	if err != nil {
		t.Fatalf("start a short worker: %v", err)
	}
	<-gone.Done()
	if driver.GroupMembersRemain(gone.Process()) {
		t.Skip("the short worker's group is still in use")
	}
	p.PGID = gone.Process().PGID
	got := stopReplacedWorker(p, 200*time.Millisecond)
	assert.NotEqual(t, workerStopped, got.state, got.note)
	assert.True(t, drivertest.Alive(p.PID))
}
