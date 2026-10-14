package connector

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stateParent(t *testing.T) string {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "connect")
	require.NoError(t, os.Mkdir(parent, 0o700))
	return parent
}

func legacyStateDir(t *testing.T, parent string) string {
	t.Helper()
	legacy := filepath.Join(parent, LegacyStateDirName("2914079", 52007412))
	require.NoError(t, os.Mkdir(legacy, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, LedgerFile), []byte("the old ledger"), 0o600))
	return legacy
}

func TestStateDirNamesAreOnePerProfile(t *testing.T) {
	assert.Equal(t, "2914079-52007412", LegacyStateDirName("2914079", 52007412))
	assert.Equal(t, "2914079-52007412-rob-agent", StateDirName("2914079", 52007412, "rob-agent"))
	assert.Equal(t, StateDirName("02914079", 52007412, "a"), StateDirName("2914079", 52007412, "a"))
	assert.NotEqual(t, StateDirName("2914079", 52007412, "a"), StateDirName("2914079", 52007412, "b"))
	assert.NotEqual(t, StateDirName("2914079", 52007412, "1-a"), StateDirName("2914079", 520074121, "a"))
}

func TestAdoptMovesTheLegacyDirectoryToTheProfile(t *testing.T) {
	parent := stateParent(t)
	legacy := legacyStateDir(t, parent)

	adopted, err := AdoptLegacyStateDir(parent, "2914079", 52007412, "agent-a")
	require.NoError(t, err)
	assert.True(t, adopted)

	dir := filepath.Join(parent, StateDirName("2914079", 52007412, "agent-a"))
	data, err := os.ReadFile(filepath.Join(dir, LedgerFile))
	require.NoError(t, err)
	assert.Equal(t, "the old ledger", string(data))
	_, err = os.Lstat(legacy)
	assert.ErrorIs(t, err, os.ErrNotExist)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotEqual(t, ".json", filepath.Ext(e.Name()), "no holder is left behind for a lock nobody holds")
	}

	again, err := AdoptLegacyStateDir(parent, "2914079", 52007412, "agent-a")
	require.NoError(t, err)
	assert.False(t, again, "adopting twice moves nothing")
	data, err = os.ReadFile(filepath.Join(dir, LedgerFile))
	require.NoError(t, err)
	assert.Equal(t, "the old ledger", string(data))
}

func TestOnlyOneProfileAdoptsTheLegacyDirectory(t *testing.T) {
	parent := stateParent(t)
	legacyStateDir(t, parent)

	first, err := AdoptLegacyStateDir(parent, "2914079", 52007412, "agent-a")
	require.NoError(t, err)
	require.True(t, first)
	second, err := AdoptLegacyStateDir(parent, "2914079", 52007412, "agent-b")
	require.NoError(t, err)
	assert.False(t, second)

	_, err = os.Lstat(filepath.Join(parent, StateDirName("2914079", 52007412, "agent-b")))
	assert.ErrorIs(t, err, os.ErrNotExist, "the second profile starts a directory of its own")
	_, err = os.Lstat(filepath.Join(parent, StateDirName("2914079", 52007412, "agent-a"), LedgerFile))
	require.NoError(t, err)
}

func TestAdoptLeavesAProfileThatAlreadyHasADirectoryAlone(t *testing.T) {
	parent := stateParent(t)
	legacy := legacyStateDir(t, parent)
	dir := filepath.Join(parent, StateDirName("2914079", 52007412, "agent-a"))
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, LedgerFile), []byte("the new ledger"), 0o600))

	adopted, err := AdoptLegacyStateDir(parent, "2914079", 52007412, "agent-a")
	require.NoError(t, err)
	assert.False(t, adopted)

	data, err := os.ReadFile(filepath.Join(dir, LedgerFile))
	require.NoError(t, err)
	assert.Equal(t, "the new ledger", string(data))
	data, err = os.ReadFile(filepath.Join(legacy, LedgerFile))
	require.NoError(t, err)
	assert.Equal(t, "the old ledger", string(data))
}

func TestAdoptWithNoLegacyDirectoryCreatesNothing(t *testing.T) {
	parent := stateParent(t)

	adopted, err := AdoptLegacyStateDir(parent, "2914079", 52007412, "agent-a")
	require.NoError(t, err)
	assert.False(t, adopted)
	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestAdoptRefusesWhileAConnectorRunsInTheLegacyDirectory(t *testing.T) {
	parent := stateParent(t)
	legacy := legacyStateDir(t, parent)
	lock, err := AcquireInstanceLock(legacy, "2914079", 52007412, time.Now())
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Release() })

	adopted, err := AdoptLegacyStateDir(parent, "2914079", 52007412, "agent-a")
	require.ErrorIs(t, err, ErrAlreadyRunning)
	assert.False(t, adopted)
	_, err = os.Lstat(filepath.Join(legacy, LedgerFile))
	require.NoError(t, err)
	_, err = os.Lstat(filepath.Join(parent, StateDirName("2914079", 52007412, "agent-a")))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestTwoProfilesHoldTheirLocksAtOnceAndOneProfileOnlyOnce(t *testing.T) {
	parent := stateParent(t)
	now := time.Now()
	a := filepath.Join(parent, StateDirName("2914079", 52007412, "agent-a"))
	b := filepath.Join(parent, StateDirName("2914079", 52007412, "agent-b"))

	first, err := AcquireInstanceLock(a, "2914079", 52007412, now)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Release() })
	other, err := AcquireInstanceLock(b, "2914079", 52007412, now)
	require.NoError(t, err, "another profile for the same agent runs beside it")
	t.Cleanup(func() { _ = other.Release() })

	_, err = AcquireInstanceLock(a, "2914079", 52007412, now)
	require.ErrorIs(t, err, ErrAlreadyRunning)
}
