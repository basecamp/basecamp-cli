package connector

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two connectors on one agent identity dispatch every mention twice. The Ruby
// connector's own notes record that happening.
func TestSecondConnectorOnTheSameAgentIsRefused(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	first, err := AcquireInstanceLock(dir, "2914079", 52007412, now)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Release() })

	_, err = AcquireInstanceLock(dir, "2914079", 52007412, now)
	require.ErrorIs(t, err, ErrAlreadyRunning)
	assert.Contains(t, err.Error(), "pid", "the refusal says who is holding it")
}

// The key is the identity, not the profile that names it: two profiles can
// hold credentials for one agent, and it is the agent that gets double-served.
func TestTheLockIsKeyedOnAccountAndAgent(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	first, err := AcquireInstanceLock(dir, "2914079", 52007412, now)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Release() })

	otherAgent, err := AcquireInstanceLock(dir, "2914079", 26909558, now)
	require.NoError(t, err, "a different agent in the same account is a different connector")
	t.Cleanup(func() { _ = otherAgent.Release() })

	otherAccount, err := AcquireInstanceLock(dir, "5951425", 52007412, now)
	require.NoError(t, err, "the same agent id in another account is a different connector")
	t.Cleanup(func() { _ = otherAccount.Release() })
}

func TestReleasedLockCanBeRetaken(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	first, err := AcquireInstanceLock(dir, "2914079", 52007412, now)
	require.NoError(t, err)
	require.NoError(t, first.Release())

	second, err := AcquireInstanceLock(dir, "2914079", 52007412, now)
	require.NoError(t, err)
	require.NoError(t, second.Release())
}

func TestLockRefusesAnIncompleteIdentity(t *testing.T) {
	dir := t.TempDir()
	_, err := AcquireInstanceLock(dir, "", 52007412, time.Now())
	assert.Error(t, err)
	_, err = AcquireInstanceLock(dir, "2914079", 0, time.Now())
	assert.Error(t, err)
}
