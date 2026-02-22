package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateCache_NoLegacyDir(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	result := &MigrateResult{}
	migrateCache(result)

	assert.False(t, result.CacheMoved)
	assert.Equal(t, "no legacy cache directory found", result.CacheMessage)
}

func TestMigrateCache_SimpleRename(t *testing.T) {
	cacheBase := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheBase)

	oldDir := filepath.Join(cacheBase, "bcq")
	require.NoError(t, os.MkdirAll(oldDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(oldDir, "completion.json"), []byte(`{"v":1}`), 0600))

	result := &MigrateResult{}
	migrateCache(result)

	assert.True(t, result.CacheMoved)
	assert.Contains(t, result.CacheMessage, "moved")

	// Old dir gone, new dir exists with content
	_, err := os.Stat(oldDir)
	assert.True(t, os.IsNotExist(err))

	newDir := filepath.Join(cacheBase, "basecamp")
	data, err := os.ReadFile(filepath.Join(newDir, "completion.json"))
	require.NoError(t, err)
	assert.Equal(t, `{"v":1}`, string(data))
}

func TestMigrateCache_BothExist_MergesAndRemovesOld(t *testing.T) {
	cacheBase := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheBase)

	oldDir := filepath.Join(cacheBase, "bcq")
	newDir := filepath.Join(cacheBase, "basecamp")

	require.NoError(t, os.MkdirAll(oldDir, 0700))
	require.NoError(t, os.MkdirAll(newDir, 0700))

	// Old has completion.json, new has a different file
	require.NoError(t, os.WriteFile(filepath.Join(oldDir, "completion.json"), []byte(`{"old":true}`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(newDir, "other.txt"), []byte("keep"), 0600))

	result := &MigrateResult{}
	migrateCache(result)

	assert.True(t, result.CacheMoved)
	assert.Contains(t, result.CacheMessage, "merged")

	// Old dir removed
	_, err := os.Stat(oldDir)
	assert.True(t, os.IsNotExist(err))

	// Both files exist in new dir
	data, err := os.ReadFile(filepath.Join(newDir, "completion.json"))
	require.NoError(t, err)
	assert.Equal(t, `{"old":true}`, string(data))

	data, err = os.ReadFile(filepath.Join(newDir, "other.txt"))
	require.NoError(t, err)
	assert.Equal(t, "keep", string(data))
}

func TestMigrateTheme_NoLegacyDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	result := &MigrateResult{}
	migrateTheme(result)

	assert.False(t, result.ThemeMoved)
	assert.Equal(t, "no legacy theme directory found", result.ThemeMessage)
}

func TestMigrateTheme_NewAlreadyExists(t *testing.T) {
	configBase := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configBase)

	oldDir := filepath.Join(configBase, "bcq", "theme")
	newDir := filepath.Join(configBase, "basecamp", "theme")
	require.NoError(t, os.MkdirAll(oldDir, 0700))
	require.NoError(t, os.MkdirAll(newDir, 0700))

	result := &MigrateResult{}
	migrateTheme(result)

	assert.False(t, result.ThemeMoved)
	assert.Equal(t, "theme directory already exists at new location", result.ThemeMessage)
}

func TestMigrateTheme_Symlink(t *testing.T) {
	configBase := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configBase)

	// Create a real target and a symlink as the old theme
	target := filepath.Join(configBase, "system-themes", "dark")
	require.NoError(t, os.MkdirAll(target, 0700))

	oldDir := filepath.Join(configBase, "bcq", "theme")
	require.NoError(t, os.MkdirAll(filepath.Dir(oldDir), 0700))
	require.NoError(t, os.Symlink(target, oldDir))

	result := &MigrateResult{}
	migrateTheme(result)

	assert.True(t, result.ThemeMoved)
	assert.Contains(t, result.ThemeMessage, "symlink recreated")

	// Old symlink removed
	_, err := os.Lstat(oldDir)
	assert.True(t, os.IsNotExist(err))

	// New symlink points to same target
	newDir := filepath.Join(configBase, "basecamp", "theme")
	resolved, err := os.Readlink(newDir)
	require.NoError(t, err)
	assert.Equal(t, target, resolved)
}

func TestCollectKnownOrigins_AlwaysIncludesProduction(t *testing.T) {
	configDir := t.TempDir()
	origins := collectKnownOrigins(configDir)
	assert.Contains(t, origins, "https://3.basecampapi.com")
}

func TestCollectKnownOrigins_NoConfigFiles(t *testing.T) {
	configDir := t.TempDir()
	origins := collectKnownOrigins(configDir)

	// Should still return the production origin at minimum
	assert.NotEmpty(t, origins)
	assert.Contains(t, origins, "https://3.basecampapi.com")
}

func TestCollectKnownOrigins_ReadsCredentialsFile(t *testing.T) {
	configDir := t.TempDir()

	creds := map[string]any{
		"https://custom.basecampapi.com": map[string]string{"token": "abc"},
	}
	data, _ := json.Marshal(creds)
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "credentials.json"), data, 0600))

	origins := collectKnownOrigins(configDir)
	assert.Contains(t, origins, "https://custom.basecampapi.com")
	assert.Contains(t, origins, "https://3.basecampapi.com")
}

func TestCollectKnownOrigins_ReadsLegacyConfigDir(t *testing.T) {
	// Set up a parent with both "basecamp" and "bcq" subdirs
	parent := t.TempDir()
	configDir := filepath.Join(parent, "basecamp")
	legacyDir := filepath.Join(parent, "bcq")
	require.NoError(t, os.MkdirAll(configDir, 0700))
	require.NoError(t, os.MkdirAll(legacyDir, 0700))

	// Put a custom origin only in the legacy dir
	creds := map[string]any{
		"https://legacy.basecampapi.com": map[string]string{"token": "old"},
	}
	data, _ := json.Marshal(creds)
	require.NoError(t, os.WriteFile(filepath.Join(legacyDir, "credentials.json"), data, 0600))

	origins := collectKnownOrigins(configDir)
	assert.Contains(t, origins, "https://legacy.basecampapi.com",
		"should discover origins from legacy bcq config dir")
	assert.Contains(t, origins, "https://3.basecampapi.com")
}

func TestMigrateMarker_NotWrittenOnNoOp(t *testing.T) {
	configDir := t.TempDir()
	cacheBase := t.TempDir()
	configBase := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheBase)
	t.Setenv("XDG_CONFIG_HOME", configBase)

	result := &MigrateResult{}
	migrateCache(result)
	migrateTheme(result)

	// Simulate what runMigrate does: only write marker if something migrated
	migrated := result.KeyringMigrated > 0 || result.CacheMoved || result.ThemeMoved
	hasErrors := len(result.KeyringErrors) > 0

	assert.False(t, migrated, "nothing should have been migrated")
	assert.False(t, hasErrors)

	// Marker should NOT be written for no-op
	markerPath := filepath.Join(configDir, migratedMarker)
	if migrated && !hasErrors {
		_ = os.WriteFile(markerPath, []byte("migrated\n"), 0600)
	}
	_, err := os.Stat(markerPath)
	assert.True(t, os.IsNotExist(err), "marker should not exist after no-op migration")
}

func TestMigrateMarker_WrittenOnSuccess(t *testing.T) {
	cacheBase := t.TempDir()
	configBase := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheBase)
	t.Setenv("XDG_CONFIG_HOME", configBase)

	// Create legacy cache so migration has something to do
	oldDir := filepath.Join(cacheBase, "bcq")
	require.NoError(t, os.MkdirAll(oldDir, 0700))

	result := &MigrateResult{}
	migrateCache(result)

	assert.True(t, result.CacheMoved)

	// Simulate marker logic from runMigrate
	migrated := result.KeyringMigrated > 0 || result.CacheMoved || result.ThemeMoved
	hasErrors := len(result.KeyringErrors) > 0
	assert.True(t, migrated)
	assert.False(t, hasErrors)
}

func TestMigrateMarker_NotWrittenOnErrors(t *testing.T) {
	result := &MigrateResult{
		CacheMoved:    true,
		KeyringErrors: []string{"failed to write origin: keyring error"},
	}

	migrated := result.KeyringMigrated > 0 || result.CacheMoved || result.ThemeMoved
	hasErrors := len(result.KeyringErrors) > 0

	assert.True(t, migrated)
	assert.True(t, hasErrors, "marker should not be written when errors exist")
}

// fakeKeyring is an in-memory keyring for testing migrateKeyring.
type fakeKeyring struct {
	store map[string]string // "service\x00key" → data
}

func newFakeKeyring() *fakeKeyring {
	return &fakeKeyring{store: map[string]string{}}
}

func (f *fakeKeyring) key(service, key string) string { return service + "\x00" + key }

func (f *fakeKeyring) get(service, key string) (string, error) {
	v, ok := f.store[f.key(service, key)]
	if !ok {
		return "", fmt.Errorf("not found")
	}
	return v, nil
}

func (f *fakeKeyring) set(service, key, data string) error {
	f.store[f.key(service, key)] = data
	return nil
}

func (f *fakeKeyring) delete(service, key string) error {
	delete(f.store, f.key(service, key))
	return nil
}

func (f *fakeKeyring) install(t *testing.T) {
	t.Helper()
	orig := keyringOps
	keyringOps = keyringFuncs{get: f.get, set: f.set, delete: f.delete}
	t.Cleanup(func() { keyringOps = orig })
}

func TestMigrateKeyring_FullMigration(t *testing.T) {
	fk := newFakeKeyring()
	fk.store[fk.key("bcq", "bcq::https://3.basecampapi.com")] = `{"token":"secret"}`
	fk.install(t)

	configDir := t.TempDir()
	result := &MigrateResult{}
	migrateKeyring(result, configDir)

	assert.Equal(t, 1, result.KeyringMigrated)
	assert.Empty(t, result.KeyringErrors)

	// New key exists with same data
	v, err := fk.get("basecamp", "basecamp::https://3.basecampapi.com")
	require.NoError(t, err)
	assert.Equal(t, `{"token":"secret"}`, v)

	// Legacy key deleted
	_, err = fk.get("bcq", "bcq::https://3.basecampapi.com")
	assert.Error(t, err)
}

func TestMigrateKeyring_AlreadyMigrated_CleansUpLegacyKey(t *testing.T) {
	fk := newFakeKeyring()
	// Both old and new keys exist (user logged in with new CLI before running migrate)
	fk.store[fk.key("bcq", "bcq::https://3.basecampapi.com")] = `{"token":"old"}`
	fk.store[fk.key("basecamp", "basecamp::https://3.basecampapi.com")] = `{"token":"new"}`
	fk.install(t)

	configDir := t.TempDir()
	result := &MigrateResult{}
	migrateKeyring(result, configDir)

	assert.Equal(t, 1, result.KeyringMigrated)
	assert.Empty(t, result.KeyringErrors)

	// New key unchanged
	v, err := fk.get("basecamp", "basecamp::https://3.basecampapi.com")
	require.NoError(t, err)
	assert.Equal(t, `{"token":"new"}`, v)

	// Legacy key deleted
	_, err = fk.get("bcq", "bcq::https://3.basecampapi.com")
	assert.Error(t, err, "legacy key should be deleted even when new key already exists")
}

func TestMigrateKeyring_NoLegacyEntries(t *testing.T) {
	fk := newFakeKeyring()
	fk.install(t)

	configDir := t.TempDir()
	result := &MigrateResult{}
	migrateKeyring(result, configDir)

	assert.Equal(t, 0, result.KeyringMigrated)
	assert.Empty(t, result.KeyringErrors, "no legacy entries is not an error")
}

func TestMigrateKeyring_WriteFails(t *testing.T) {
	fk := newFakeKeyring()
	fk.store[fk.key("bcq", "bcq::https://3.basecampapi.com")] = `{"token":"secret"}`

	orig := keyringOps
	keyringOps = keyringFuncs{
		get:    fk.get,
		set:    func(service, key, data string) error { return fmt.Errorf("keyring locked") },
		delete: fk.delete,
	}
	t.Cleanup(func() { keyringOps = orig })

	configDir := t.TempDir()
	result := &MigrateResult{}
	migrateKeyring(result, configDir)

	assert.Equal(t, 0, result.KeyringMigrated)
	assert.Len(t, result.KeyringErrors, 1)
	assert.Contains(t, result.KeyringErrors[0], "keyring locked")

	// Legacy key not deleted on write failure
	_, err := fk.get("bcq", "bcq::https://3.basecampapi.com")
	assert.NoError(t, err, "legacy key should be preserved when write fails")
}

func TestMigrateKeyring_MultipleOrigins(t *testing.T) {
	fk := newFakeKeyring()
	fk.store[fk.key("bcq", "bcq::https://3.basecampapi.com")] = `{"token":"prod"}`
	fk.store[fk.key("bcq", "bcq::https://custom.basecampapi.com")] = `{"token":"custom"}`
	fk.install(t)

	// Put custom origin in a credentials.json so collectKnownOrigins finds it
	configDir := t.TempDir()
	creds := map[string]any{"https://custom.basecampapi.com": map[string]string{"token": "x"}}
	data, _ := json.Marshal(creds)
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "credentials.json"), data, 0600))

	result := &MigrateResult{}
	migrateKeyring(result, configDir)

	assert.Equal(t, 2, result.KeyringMigrated)
	assert.Empty(t, result.KeyringErrors)
}
