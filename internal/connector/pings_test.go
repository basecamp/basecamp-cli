package connector

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

// A running connector says beside its lock whether it hears the agent's
// Pings, so another connector for the agent on this computer can see two
// answering them. Anything else that takes the lock hears none, and a holder
// an older build wrote says nothing either way.
func TestTheLockSaysWhetherItsConnectorHearsPings(t *testing.T) {
	const account, agent = "2914079", int64(52007412)
	now := time.Now()

	for _, hears := range []bool{true, false} {
		dir := t.TempDir()
		lock, err := AcquireInstanceLock(dir, account, agent, now)
		require.NoError(t, err)
		lock.NotePings(hears, "chosen")
		holder, ok := InstanceHolder(dir, account, agent)
		require.True(t, ok)
		require.NotNil(t, holder.Pings, "a connector always says")
		assert.Equal(t, hears, *holder.Pings)
		assert.Equal(t, "chosen", holder.PingsBecause)
		assert.Equal(t, os.Getpid(), holder.PID, "and still says who holds the lock")
		require.NoError(t, lock.Release())
		_, ok = InstanceHolder(dir, account, agent)
		assert.False(t, ok, "a released lock says nothing")
	}

	t.Run("a command that only takes the lock", func(t *testing.T) {
		dir := t.TempDir()
		lock, err := AcquireInstanceLock(dir, account, agent, now)
		require.NoError(t, err)
		t.Cleanup(func() { _ = lock.Release() })
		holder, ok := InstanceHolder(dir, account, agent)
		require.True(t, ok)
		require.NotNil(t, holder.Pings)
		assert.False(t, *holder.Pings, "it is no connector, and answers nothing")
	})

	t.Run("an older build's holder", func(t *testing.T) {
		dir := t.TempDir()
		lock, err := AcquireInstanceLock(dir, account, agent, now)
		require.NoError(t, err)
		t.Cleanup(func() { _ = lock.Release() })
		require.NoError(t, os.WriteFile(lock.Path()+".json", []byte(`{"pid":4242,"started_at":"2026-10-21T10:00:00Z","account_id":"2914079","agent_person_id":52007412}`+"\n"), 0o600))
		holder, ok := InstanceHolder(dir, account, agent)
		require.True(t, ok)
		assert.Equal(t, 4242, holder.PID)
		assert.Nil(t, holder.Pings, "an older connector does not say, and heard Pings unless narrowed to projects")
	})
}

// A profile keeps whether its connector hears Pings beside its ledger, so a
// restart keeps the choice: written privately, read back as it was, and
// refused rather than guessed at when it isn't this user's own file.
func TestAProfileKeepsItsPingsChoiceBesideItsLedger(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, setup.EnsurePrivateDir(dir))

	_, ok, err := LoadPingsChoice(dir)
	require.NoError(t, err)
	assert.False(t, ok, "nothing chosen yet")

	at := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	require.NoError(t, SavePingsChoice(dir, PingsChoice{Hears: true, Because: "first_connection", At: at}))
	got, ok, err := LoadPingsChoice(dir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, PingsChoice{Hears: true, Because: "first_connection", At: at}, got)
	info, err := os.Stat(filepath.Join(dir, PingsFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	require.NoError(t, SavePingsChoice(dir, PingsChoice{Hears: false, Because: "chosen", At: at.Add(time.Hour)}))
	got, ok, err = LoadPingsChoice(dir)
	require.NoError(t, err)
	require.True(t, ok)
	assert.False(t, got.Hears, "a later choice replaces the first")
	assert.Equal(t, "chosen", got.Because)

	t.Run("a file that says nothing", func(t *testing.T) {
		for name, body := range map[string]string{
			"not JSON":        "{hears",
			"no hears":        `{"because":"chosen"}`,
			"hears not true":  `{"hears":"yes","because":"chosen"}`,
			"two objects":     `{"hears":true}{"hears":false}`,
			"an empty object": `{}`,
		} {
			sub := filepath.Join(t.TempDir(), "state")
			require.NoError(t, setup.EnsurePrivateDir(sub))
			require.NoError(t, os.WriteFile(filepath.Join(sub, PingsFile), []byte(body), 0o600))
			_, _, err := LoadPingsChoice(sub)
			assert.Error(t, err, name)
		}
	})

	t.Run("a file this user does not keep alone", func(t *testing.T) {
		sub := filepath.Join(t.TempDir(), "state")
		require.NoError(t, setup.EnsurePrivateDir(sub))
		path := filepath.Join(sub, PingsFile)
		require.NoError(t, os.WriteFile(path, []byte(`{"hears":true,"because":"chosen"}`), 0o644))
		require.NoError(t, os.Chmod(path, 0o644))
		_, _, err := LoadPingsChoice(sub)
		assert.ErrorIs(t, err, setup.ErrNotPrivate, "a file others can read is refused")

		target := filepath.Join(t.TempDir(), "elsewhere.json")
		require.NoError(t, os.WriteFile(target, []byte(`{"hears":true,"because":"chosen"}`), 0o600))
		require.NoError(t, os.Remove(path))
		require.NoError(t, os.Symlink(target, path))
		_, _, err = LoadPingsChoice(sub)
		assert.Error(t, err, "a symlink is refused, not followed")
		assert.Error(t, SavePingsChoice(sub, PingsChoice{Hears: false, Because: "chosen"}), "and not written through")
		data, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"hears":true`, "the symlink's target is untouched")
	})
}
