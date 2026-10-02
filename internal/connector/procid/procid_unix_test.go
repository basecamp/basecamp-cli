//go:build unix

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

// startWithChild starts a process that leads a group of its own and leaves a
// child in it, the shape of a worker that started tools of its own. It gives
// back the leader's command, its identity, and the child's pid.
func startWithChild(t *testing.T) (*exec.Cmd, Process, int) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "child")
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "sleep 300 & echo $! > "+pidFile+"; exec sleep 300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	var child int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		child, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	p, err := LookupProcess(cmd.Process.Pid)
	require.NoError(t, err)
	return cmd, p, child
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// Copilot r3: a process group can outlive its leader, and its members may be
// the worker's own children.
func TestAGroupThatOutlivedItsLeaderIsNotSilenceAbsence(t *testing.T) {
	cmd, leader, child := startWithChild(t)

	// The leader alone goes, and is reaped; its child keeps the group.
	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()

	owns, err := OwnsWorker(leader)
	assert.False(t, owns)
	assert.ErrorIs(t, err, ErrGroupOutlivedLeader)
	assert.True(t, alive(child), "and the child is left alone for a person to decide about")
}

// A pid is not an identity.
func TestOwnsWorkerAnswersWhetherThisIsStillTheWorker(t *testing.T) {
	_, p, _ := startWithChild(t)

	owns, err := OwnsWorker(p)
	require.NoError(t, err)
	assert.True(t, owns, "the process that was recorded")

	reused := p
	reused.StartedAt = p.StartedAt.Add(-time.Hour)
	owns, err = OwnsWorker(reused)
	assert.False(t, owns, "the same pid with another start time is another process")
	assert.ErrorIs(t, err, ErrGroupOutlivedLeader, "and its group still has members")

	owns, err = OwnsWorker(Process{PID: 1 << 30, PGID: 1 << 30, StartedAt: time.Now()})
	assert.False(t, owns)
	assert.NoError(t, err, "a pid that names nothing, in a group with no members, is simply gone")

	owns, err = OwnsWorker(Process{})
	assert.False(t, owns)
	assert.NoError(t, err, "a record with no process is nothing to own")
}

// Copilot r4: only "no such process group" proves a group is gone; a probe
// that was refused is not absence.
func TestOnlyNoSuchProcessGroupProvesAbsence(t *testing.T) {
	assert.NoError(t, groupProbe(4242, syscall.ESRCH), "no such group: gone")
	assert.ErrorIs(t, groupProbe(4242, nil), ErrGroupOutlivedLeader, "answered: members remain")
	assert.ErrorIs(t, groupProbe(4242, syscall.EPERM), ErrGroupOutlivedLeader, "refused: not proven gone")
	assert.ErrorIs(t, groupProbe(4242, syscall.EINVAL), ErrGroupOutlivedLeader, "any other answer: not proven gone")
}

// sleepInItsOwnGroup starts a process that leads a group of its own, and
// gives back the kernel's identity for it. It stands in for whatever holds a
// pid now: a worker of a later attempt, or any process of this user the
// kernel gave a recycled id to.
func sleepInItsOwnGroup(t *testing.T) Process {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "/bin/sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	p, err := LookupProcess(cmd.Process.Pid)
	require.NoError(t, err)
	require.True(t, p.StartedExact, "the kernel's own start time is the identity")
	return p
}

// Copilot on #738: the kernel's start time was recorded when it could be
// read, but the comparison accepted anything within three seconds of it,
// so under fast pid reuse a stranger started just after the record was
// written passed as the worker.
func TestAProcessStartedJustAfterTheRecordIsNotTheRecordedOne(t *testing.T) {
	p := sleepInItsOwnGroup(t)

	stranger := p
	stranger.StartedAt = p.StartedAt.Add(-time.Second)
	gone, err := processGone(stranger)
	require.NoError(t, err)
	assert.True(t, gone, "a second between the record and the kernel is another process, not this one")

	owns, err := OwnsWorker(stranger)
	assert.False(t, owns)
	assert.ErrorIs(t, err, ErrGroupOutlivedLeader, "and its group is not settled around either")
}

// The wall-clock fallback is not an identity: where the kernel would not say
// when a process started, the rule refuses to answer rather than compare
// against a stamp taken around a fork.
func TestARecordWithNoKernelStartTimeIsRefused(t *testing.T) {
	p := sleepInItsOwnGroup(t)

	stamped := Process{PID: p.PID, PGID: p.PGID, StartedAt: time.Now()}
	_, err := processGone(stamped)
	assert.ErrorIs(t, err, ErrIdentityUnknown)

	owns, err := OwnsWorker(stamped)
	assert.False(t, owns)
	assert.ErrorIs(t, err, ErrIdentityUnknown, "neither owned nor gone: unanswerable")

	// And a record with a pid but no start time at all, a ledger row written
	// where the kernel could not be asked, is the same answer, not
	// "gone, settle it".
	owns, err = OwnsWorker(Process{PID: p.PID, PGID: p.PGID})
	assert.False(t, owns)
	assert.ErrorIs(t, err, ErrIdentityUnknown)
}
