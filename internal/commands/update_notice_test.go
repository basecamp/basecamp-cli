package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/version"
)

func TestStartUpdateCheck_SkipsDevBuild(t *testing.T) {
	origVersion := version.Version
	version.Version = "dev"
	defer func() { version.Version = origVersion }()

	uc := StartUpdateCheck()
	assert.Nil(t, uc)
}

func TestStartUpdateCheck_SkipsWhenEnvSet(t *testing.T) {
	origVersion := version.Version
	version.Version = "1.0.0"
	defer func() { version.Version = origVersion }()

	t.Setenv("BASECAMP_NO_UPDATE_CHECK", "1")

	uc := StartUpdateCheck()
	assert.Nil(t, uc)
}

func TestStartUpdateCheck_SkipsNonInteractive(t *testing.T) {
	origVersion := version.Version
	version.Version = "1.0.0"
	defer func() { version.Version = origVersion }()

	origTTY := stdoutIsTerminal
	stdoutIsTerminal = func() bool { return false }
	defer func() { stdoutIsTerminal = origTTY }()

	uc := StartUpdateCheck()
	assert.Nil(t, uc)
}

func TestStartUpdateCheck_UsesFreshCache(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	origVersion := version.Version
	version.Version = "1.0.0"
	defer func() { version.Version = origVersion }()

	origTTY := stdoutIsTerminal
	stdoutIsTerminal = func() bool { return true }
	defer func() { stdoutIsTerminal = origTTY }()

	// Write a fresh cache entry
	cache := updateCache{
		LatestVersion: "2.0.0",
		CheckedAt:     time.Now().UTC(),
	}
	cacheDir := filepath.Join(configDir, "basecamp")
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	data, _ := json.Marshal(cache)
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, ".update-check"), data, 0o644))

	uc := StartUpdateCheck()
	require.NotNil(t, uc)

	notice := uc.Notice()
	assert.Contains(t, notice, "2.0.0")
	assert.Contains(t, notice, "1.0.0")
	assert.Contains(t, notice, "basecamp upgrade")
}

func TestStartUpdateCheck_CacheHitSameVersion(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	origVersion := version.Version
	version.Version = "1.0.0"
	defer func() { version.Version = origVersion }()

	origTTY := stdoutIsTerminal
	stdoutIsTerminal = func() bool { return true }
	defer func() { stdoutIsTerminal = origTTY }()

	cache := updateCache{
		LatestVersion: "1.0.0",
		CheckedAt:     time.Now().UTC(),
	}
	cacheDir := filepath.Join(configDir, "basecamp")
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	data, _ := json.Marshal(cache)
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, ".update-check"), data, 0o644))

	uc := StartUpdateCheck()
	require.NotNil(t, uc)

	assert.Equal(t, "", uc.Notice())
}

func TestStartUpdateCheck_StaleCacheFetchesInBackground(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	origVersion := version.Version
	version.Version = "1.0.0"
	defer func() { version.Version = origVersion }()

	origTTY := stdoutIsTerminal
	stdoutIsTerminal = func() bool { return true }
	defer func() { stdoutIsTerminal = origTTY }()

	origChecker := versionChecker
	versionChecker = func() (string, error) { return "3.0.0", nil }
	defer func() { versionChecker = origChecker }()

	// Write a stale cache entry (25 hours old)
	cache := updateCache{
		LatestVersion: "2.0.0",
		CheckedAt:     time.Now().UTC().Add(-25 * time.Hour),
	}
	cacheDir := filepath.Join(configDir, "basecamp")
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	data, _ := json.Marshal(cache)
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, ".update-check"), data, 0o644))

	uc := StartUpdateCheck()
	require.NotNil(t, uc)

	// Wait for goroutine to finish
	<-uc.done

	notice := uc.Notice()
	assert.Contains(t, notice, "3.0.0")

	// Cache should be updated
	updated := readUpdateCache()
	require.NotNil(t, updated)
	assert.Equal(t, "3.0.0", updated.LatestVersion)
}

func TestStartUpdateCheck_NoCacheFetchesInBackground(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	origVersion := version.Version
	version.Version = "1.0.0"
	defer func() { version.Version = origVersion }()

	origTTY := stdoutIsTerminal
	stdoutIsTerminal = func() bool { return true }
	defer func() { stdoutIsTerminal = origTTY }()

	origChecker := versionChecker
	versionChecker = func() (string, error) { return "1.5.0", nil }
	defer func() { versionChecker = origChecker }()

	uc := StartUpdateCheck()
	require.NotNil(t, uc)

	// Wait for goroutine to finish
	<-uc.done

	assert.Contains(t, uc.Notice(), "1.5.0")
}

func TestNotice_NilReceiver(t *testing.T) {
	var uc *UpdateCheck
	assert.Equal(t, "", uc.Notice())
}

func TestNotice_NonBlocking(t *testing.T) {
	// Create an UpdateCheck with a channel that never closes
	uc := &UpdateCheck{
		done: make(chan struct{}),
	}

	// Should return empty immediately without blocking
	assert.Equal(t, "", uc.Notice())
}

func TestNotice_SuppressesOlderLatestVersion(t *testing.T) {
	origVersion := version.Version
	version.Version = "0.4.1-0.20260313174735-243815fa23b2"
	defer func() { version.Version = origVersion }()

	uc := &UpdateCheck{
		latest: "0.4.0",
		done:   make(chan struct{}),
	}
	close(uc.done)

	assert.Equal(t, "", uc.Notice())
}

func TestUpdateCacheRoundTrip(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	writeUpdateCache("4.0.0")

	cached := readUpdateCache()
	require.NotNil(t, cached)
	assert.Equal(t, "4.0.0", cached.LatestVersion)
	assert.WithinDuration(t, time.Now().UTC(), cached.CheckedAt, 5*time.Second)
}

func TestReadUpdateCache_Invalid(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	cacheDir := filepath.Join(configDir, "basecamp")
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, ".update-check"), []byte("not json"), 0o644))

	assert.Nil(t, readUpdateCache())
}

func TestReadUpdateCache_Missing(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	assert.Nil(t, readUpdateCache())
}
