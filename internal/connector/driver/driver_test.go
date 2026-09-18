//go:build unix

package driver

import (
	"context"
	"errors"
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

func lookupFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestBuildEnvTakesExactNamesOnly(t *testing.T) {
	host := map[string]string{
		"HOME": "/home/x", "PATH": "/bin", "CLAUDE_CODE_MESSAGING_TOKEN": "test-token-not-real",
		"BASECAMP_TOKEN": "test-token-not-real", "HOMEBREW_PREFIX": "/opt",
	}
	env := BuildEnv(BaseEnv, lookupFrom(host), map[string]string{"PATH": "/usr/bin", "EXTRA": "1", "BAD=NAME": "x"})
	assert.Equal(t, []string{"EXTRA=1", "HOME=/home/x", "PATH=/usr/bin"}, env)
}

func TestStartWorkerNeverInheritsTheConnectorsEnvironment(t *testing.T) {
	t.Setenv("CONNECTOR_CANARY_NOT_REAL", "leaked")
	out := filepath.Join(t.TempDir(), "env.txt")
	w, err := StartWorker(context.Background(), SessionConfig{Scope: Scope{WorkDir: t.TempDir()}},
		Command{Path: "/bin/sh", Args: []string{"-c", "env > " + out}, Env: []string{"ONLY=this"}})
	require.NoError(t, err)
	<-w.Done()
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "CONNECTOR_CANARY_NOT_REAL")
	assert.Contains(t, string(data), "ONLY=this")

	// A nil Env is not "inherit".
	w, err = StartWorker(context.Background(), SessionConfig{Scope: Scope{WorkDir: t.TempDir()}},
		Command{Path: "/bin/sh", Args: []string{"-c", "env > " + out}})
	require.NoError(t, err)
	<-w.Done()
	data, err = os.ReadFile(out)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "CONNECTOR_CANARY_NOT_REAL")
}

type refusingLauncher struct{}

func (refusingLauncher) Launch(context.Context, LaunchRequest) (Launched, error) {
	return Launched{}, errors.New("scope refused")
}
func (refusingLauncher) Receipts(context.Context, string) ([]Receipt, error) { return nil, nil }

func TestAStartThatRanNothingIsErrNotStarted(t *testing.T) {
	_, err := StartWorker(context.Background(), SessionConfig{Scope: Scope{WorkDir: t.TempDir()}}, Command{Path: "/nonexistent/claude-not-here"})
	assert.ErrorIs(t, err, ErrNotStarted)
	_, err = StartWorker(context.Background(), SessionConfig{Launcher: refusingLauncher{}, Scope: Scope{WorkDir: t.TempDir()}}, Command{Path: "/bin/true"})
	assert.ErrorIs(t, err, ErrNotStarted)
	_, err = StartWorker(context.Background(), SessionConfig{}, Command{Path: "/bin/true"})
	assert.ErrorIs(t, err, ErrNotStarted, "the direct launcher needs the record's directory")
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// startWithChild starts a shell that starts a long child, and returns the
// worker and the child's pid.
func startWithChild(t *testing.T) (*Worker, int) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "child")
	w, err := StartWorker(context.Background(), SessionConfig{Scope: Scope{WorkDir: t.TempDir()}},
		Command{Path: "/bin/sh", Args: []string{"-c", "sleep 300 & echo $! > " + pidFile + "; wait"}, Env: []string{"PATH=/bin:/usr/bin"}})
	require.NoError(t, err)
	var child int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			return false
		}
		child, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	return w, child
}

func TestTerminateEndsTheWholeProcessGroup(t *testing.T) {
	w, child := startWithChild(t)
	assert.Equal(t, w.Process().PID, w.Process().PGID)
	w.Terminate(time.Second)
	assert.Eventually(t, func() bool { return !alive(child) }, 5*time.Second, 20*time.Millisecond, "the worker's own children go with it")
}

func TestTerminateRecordedLeavesAReusedPidAlone(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "/bin/sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	// The kernel's own identity for it, which is what a record carries.
	p, err := LookupProcess(cmd.Process.Pid)
	require.NoError(t, err)
	require.True(t, p.StartedExact)

	reused := p
	reused.StartedAt = p.StartedAt.Add(-time.Hour)
	signaled, err := TerminateRecorded(reused, time.Second)
	assert.False(t, signaled, "a recorded start time that does not match is another process")
	assert.ErrorIs(t, err, ErrGroupOutlivedLeader, "and a group still holding that id is not this worker's to end")
	assert.True(t, alive(cmd.Process.Pid))

	signaled, err = TerminateRecorded(p, 2*time.Second)
	require.NoError(t, err)
	assert.True(t, signaled)
	_ = cmd.Wait()
}

func TestTerminateReturnsWhenADescendantLeftTheGroupHoldingTheOutput(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is needed to start a descendant in a new session")
	}
	pidFile := filepath.Join(t.TempDir(), "escaped")
	script := "import os,sys,time\nif os.fork()==0:\n    os.setsid()\n    open(sys.argv[1],'w').write(str(os.getpid()))\n    time.sleep(300)\nelse:\n    time.sleep(300)\n"
	w, err := StartWorker(context.Background(), SessionConfig{Scope: Scope{WorkDir: t.TempDir()}},
		Command{Path: python, Args: []string{"-c", script, pidFile}, Env: []string{"PATH=/bin:/usr/bin"}})
	require.NoError(t, err)
	var escaped int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		escaped, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	t.Cleanup(func() { _ = syscall.Kill(escaped, syscall.SIGKILL) })

	done := make(chan struct{})
	go func() {
		w.Terminate(100 * time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Terminate waited on a descendant outside the worker's group")
	}
}

// Copilot r3: a process group can outlive its leader, and its members may be
// the worker's own children.
func TestAGroupThatOutlivedItsLeaderIsNotSilenceAbsence(t *testing.T) {
	w, child := startWithChild(t)
	leader := w.Process()
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })

	// The leader alone goes; its child keeps the group.
	require.NoError(t, syscall.Kill(leader.PID, syscall.SIGKILL))
	<-w.Done()
	require.Eventually(t, func() bool { return processStartTimeGone(leader.PID) }, 5*time.Second, 20*time.Millisecond)

	signaled, err := TerminateRecorded(leader, time.Second)
	assert.False(t, signaled)
	assert.ErrorIs(t, err, ErrGroupOutlivedLeader)
	assert.True(t, alive(child), "and the child is left alone for a person to decide about")
}

// processStartTimeGone reports whether the kernel has no process by that pid.
func processStartTimeGone(pid int) bool {
	_, err := processStartTime(pid)
	return errors.Is(err, os.ErrNotExist)
}

// The one-owner rule's identity question: a pid is not an identity.
func TestOwnsWorkerAnswersWhetherThisIsStillTheWorker(t *testing.T) {
	w, child := startWithChild(t)
	p := w.Process()
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })

	owns, err := OwnsWorker(p)
	require.NoError(t, err)
	assert.True(t, owns, "the worker it started")

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
	assert.NoError(t, err, "a session with no process here is nothing to own")
}

// Copilot r4: only "no such process group" proves a group is gone; a probe
// that was refused is not absence.
func TestOnlyNoSuchProcessGroupProvesAbsence(t *testing.T) {
	assert.NoError(t, groupProbe(4242, syscall.ESRCH), "no such group: gone")
	assert.ErrorIs(t, groupProbe(4242, nil), ErrGroupOutlivedLeader, "answered: members remain")
	assert.ErrorIs(t, groupProbe(4242, syscall.EPERM), ErrGroupOutlivedLeader, "refused: not proven gone")
	assert.ErrorIs(t, groupProbe(4242, syscall.EINVAL), ErrGroupOutlivedLeader, "any other answer: not proven gone")
}

// openDescriptors counts this process's open file descriptors.
func openDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd here")
	}
	return len(entries)
}

// Copilot via card 22: descriptors have an owner too. A failed start closes
// what it opened, and a terminated worker's output is released.
func TestWorkersDoNotLeakDescriptors(t *testing.T) {
	before := openDescriptors(t)
	for range 50 {
		_, err := StartWorker(context.Background(), SessionConfig{Scope: Scope{WorkDir: t.TempDir()}}, Command{Path: "/nonexistent/claude-not-here"})
		require.ErrorIs(t, err, ErrNotStarted)
	}
	// At most: an earlier test's worker may release its pipes meanwhile, but
	// fifty failed starts that each leaked would be fifty more.
	assert.LessOrEqual(t, openDescriptors(t), before, "fifty failed starts leave no descriptor open")

	for range 5 {
		w, err := StartWorker(context.Background(), SessionConfig{Scope: Scope{WorkDir: t.TempDir()}}, Command{Path: "/bin/true", Env: []string{}})
		require.NoError(t, err)
		w.Terminate(time.Second)
	}
	assert.Eventually(t, func() bool { return openDescriptors(t) <= before }, 2*pipeWaitDelay+2*time.Second, 50*time.Millisecond,
		"a terminated worker's pipes are released without anyone else closing them")
}

// sleepInItsOwnGroup starts a process that leads a group of its own, and
// gives back the kernel's identity for it. It stands in for whatever holds a
// pid now: a worker of a later attempt, or any process of this user the
// kernel gave a recycled id to.
func sleepInItsOwnGroup(t *testing.T) (*exec.Cmd, Process) {
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
	return cmd, p
}

// Copilot on #738: the driver records the kernel's start time when it can
// get one, but the comparison accepted anything within three seconds of it,
// so under fast pid reuse a stranger started just after the record was
// written passed as the worker.
func TestAProcessStartedJustAfterTheRecordIsNotTheRecordedOne(t *testing.T) {
	cmd, p := sleepInItsOwnGroup(t)

	stranger := p
	stranger.StartedAt = p.StartedAt.Add(-time.Second)
	gone, err := ProcessGone(stranger)
	require.NoError(t, err)
	assert.True(t, gone, "a second between the record and the kernel is another process, not this one")

	owns, err := OwnsWorker(stranger)
	assert.False(t, owns)
	assert.ErrorIs(t, err, ErrGroupOutlivedLeader, "and its group is not settled around either")

	signaled, err := TerminateRecorded(stranger, 100*time.Millisecond)
	assert.False(t, signaled)
	assert.ErrorIs(t, err, ErrGroupOutlivedLeader)
	assert.True(t, alive(cmd.Process.Pid), "nothing is signaled on a record that does not match")
}

// The wall-clock fallback is not an identity: where the kernel would not say
// when a process started, the rule refuses to answer rather than compare
// against a stamp taken around a fork.
func TestARecordWithNoKernelStartTimeIsRefused(t *testing.T) {
	cmd, p := sleepInItsOwnGroup(t)

	stamped := Process{PID: p.PID, PGID: p.PGID, StartedAt: time.Now()}
	_, err := ProcessGone(stamped)
	assert.ErrorIs(t, err, ErrIdentityUnknown)

	owns, err := OwnsWorker(stamped)
	assert.False(t, owns)
	assert.ErrorIs(t, err, ErrIdentityUnknown, "neither owned nor gone: unanswerable")

	signaled, err := TerminateRecorded(stamped, 100*time.Millisecond)
	assert.False(t, signaled)
	assert.ErrorIs(t, err, ErrIdentityUnknown)
	assert.True(t, alive(cmd.Process.Pid))

	// And a record with a pid but no start time at all — a ledger row
	// written where the kernel could not be asked — is the same answer, not
	// "gone, settle it".
	owns, err = OwnsWorker(Process{PID: p.PID, PGID: p.PGID})
	assert.False(t, owns)
	assert.ErrorIs(t, err, ErrIdentityUnknown)
}

// Copilot on #738: group identity was checked once, before the grace period,
// and the SIGKILL that followed went out by the saved negative pgid however
// long the wait had been. This is what that costs once the id has changed
// hands: the record names a pid that now leads somebody else's group.
func TestALaterGroupSignalIsNotSentToAGroupTheRecordNoLongerOwns(t *testing.T) {
	cmd, p := sleepInItsOwnGroup(t)
	// What the connector recorded a while ago for the worker that had this
	// pid before the kernel gave it away.
	recorded := p
	recorded.StartedAt = p.StartedAt.Add(-time.Hour)

	err := ConfirmGroupGone(recorded, 100*time.Millisecond)
	assert.ErrorIs(t, err, ErrGroupOutlivedLeader, "the group is not proven gone")
	// Not alive(), which counts the zombie this test has not reaped: the
	// question is whether anything of that group still runs.
	assert.True(t, GroupMembersRemain(p), "and the group that holds the id now is left running")
	_ = cmd

	// The same rule, asked directly: nothing is signaled on a record whose
	// identity cannot be established either.
	assert.ErrorIs(t, signalRecordedGroup(Process{PID: p.PID, PGID: p.PGID, StartedAt: time.Now()}, syscall.SIGKILL), ErrIdentityUnknown)
	assert.True(t, alive(cmd.Process.Pid))

	// And the worker it really is may still be ended by its group.
	require.NoError(t, signalRecordedGroup(p, syscall.SIGKILL))
	assert.Eventually(t, func() bool { return !GroupMembersRemain(p) }, 5*time.Second, 20*time.Millisecond)
}
