//go:build darwin

package driver

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// macOS refuses any signal to a process group whose only members are
// zombies. That refusal is the absent group it is on Linux: signalGroup
// answers ESRCH, so the group can be confirmed gone.
func TestAZombieOnlyGroupIsNoSuchGroupOnMacOS(t *testing.T) {
	leader := exec.CommandContext(t.Context(), "/bin/sleep", "300")
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, leader.Start())
	pgid := leader.Process.Pid
	t.Cleanup(func() { _ = leader.Wait() })
	require.NoError(t, syscall.Kill(pgid, syscall.SIGKILL))
	// Dead, and not yet waited for: a zombie, alone in its group.
	require.Eventually(t, func() bool { _, err := processStartTime(pgid); return err != nil }, 5*time.Second, 10*time.Millisecond)
	require.ErrorIs(t, syscall.Kill(-pgid, 0), syscall.EPERM, "macOS refuses a zombie-only group")

	assert.ErrorIs(t, signalGroup(pgid, 0), syscall.ESRCH)
	assert.NoError(t, groupGone(pgid))
}

// A group whose leader is a zombie but whose child still runs is not gone:
// the signal reaches the child.
func TestAGroupWithALiveMemberIsStillSignaledOnMacOS(t *testing.T) {
	leader := exec.CommandContext(t.Context(), "/bin/sh", "-c", "/bin/sleep 300 & exit 0")
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, leader.Start())
	pgid := leader.Process.Pid
	t.Cleanup(func() { _ = signalGroup(pgid, syscall.SIGKILL); _ = leader.Wait() })
	require.Eventually(t, func() bool { _, err := processStartTime(pgid); return err != nil }, 5*time.Second, 10*time.Millisecond, "the leader has exited")

	err := signalGroup(pgid, 0)
	assert.NoError(t, err, "the live child receives it")
	assert.Error(t, groupGone(pgid), "a group with a live member is not gone")
}
