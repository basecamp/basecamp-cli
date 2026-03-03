package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrustStore_EmptyByDefault(t *testing.T) {
	ts := NewTrustStore(t.TempDir())
	assert.Empty(t, ts.List())
}

func TestTrustStore_TrustAndCheck(t *testing.T) {
	dir := t.TempDir()
	ts := NewTrustStore(dir)

	configPath := filepath.Join(dir, ".basecamp", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0755))
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0644))

	assert.False(t, ts.IsTrusted(configPath))

	require.NoError(t, ts.Trust(configPath))
	assert.True(t, ts.IsTrusted(configPath))

	entries := ts.List()
	assert.Len(t, entries, 1)
	assert.NotEmpty(t, entries[0].TrustedAt)
}

func TestTrustStore_TrustIdempotent(t *testing.T) {
	dir := t.TempDir()
	ts := NewTrustStore(dir)

	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0644))

	require.NoError(t, ts.Trust(configPath))
	require.NoError(t, ts.Trust(configPath))

	assert.Len(t, ts.List(), 1, "re-trusting should not create duplicates")
}

func TestTrustStore_Untrust(t *testing.T) {
	dir := t.TempDir()
	ts := NewTrustStore(dir)

	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0644))

	require.NoError(t, ts.Trust(configPath))
	assert.True(t, ts.IsTrusted(configPath))

	removed, err := ts.Untrust(configPath)
	require.NoError(t, err)
	assert.True(t, removed)
	assert.False(t, ts.IsTrusted(configPath))
	assert.Empty(t, ts.List())
}

func TestTrustStore_UntrustNotPresent(t *testing.T) {
	dir := t.TempDir()
	ts := NewTrustStore(dir)

	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0644))

	removed, err := ts.Untrust(configPath)
	require.NoError(t, err)
	assert.False(t, removed)
}

func TestTrustStore_Persistence(t *testing.T) {
	dir := t.TempDir()

	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0644))

	// Trust with one store instance
	ts1 := NewTrustStore(dir)
	require.NoError(t, ts1.Trust(configPath))

	// Check with a new instance (simulates new process)
	ts2 := NewTrustStore(dir)
	assert.True(t, ts2.IsTrusted(configPath))
}

func TestTrustStore_FileFormat(t *testing.T) {
	dir := t.TempDir()
	ts := NewTrustStore(dir)

	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0644))
	require.NoError(t, ts.Trust(configPath))

	// Read the raw file and verify structure
	data, err := os.ReadFile(filepath.Join(dir, "trusted-configs.json"))
	require.NoError(t, err)

	var tf struct {
		Trusted []TrustEntry `json:"trusted"`
	}
	require.NoError(t, json.Unmarshal(data, &tf))
	assert.Len(t, tf.Trusted, 1)
	assert.NotEmpty(t, tf.Trusted[0].Path)
	assert.NotEmpty(t, tf.Trusted[0].TrustedAt)
}

func TestTrustStore_CanonicalPaths(t *testing.T) {
	dir := t.TempDir()
	ts := NewTrustStore(dir)

	// Create a file with a non-canonical path (extra ..)
	subDir := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(subDir, 0755))
	configPath := filepath.Join(subDir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0644))

	// Trust using canonical path
	require.NoError(t, ts.Trust(configPath))

	// Check using non-canonical path (with ..)
	nonCanonical := filepath.Join(dir, "sub", "..", "sub", "config.json")
	assert.True(t, ts.IsTrusted(nonCanonical), "should match via canonical path")
}

func TestTrustStore_MultipleEntries(t *testing.T) {
	dir := t.TempDir()
	ts := NewTrustStore(dir)

	path1 := filepath.Join(dir, "a.json")
	path2 := filepath.Join(dir, "b.json")
	require.NoError(t, os.WriteFile(path1, []byte("{}"), 0644))
	require.NoError(t, os.WriteFile(path2, []byte("{}"), 0644))

	require.NoError(t, ts.Trust(path1))
	require.NoError(t, ts.Trust(path2))

	assert.True(t, ts.IsTrusted(path1))
	assert.True(t, ts.IsTrusted(path2))
	assert.Len(t, ts.List(), 2)

	// Untrust one, other remains
	_, err := ts.Untrust(path1)
	require.NoError(t, err)
	assert.False(t, ts.IsTrusted(path1))
	assert.True(t, ts.IsTrusted(path2))
}

func TestLoadFromFile_TrustedLocalConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"base_url": "https://custom.example.com",
		"default_profile": "work",
		"account_id": "12345"
	}`), 0644))

	ts := NewTrustStore(dir)
	require.NoError(t, ts.Trust(configPath))

	cfg := Default()
	loadFromFile(cfg, configPath, SourceLocal, ts)

	// Authority keys should be applied when trusted
	assert.Equal(t, "https://custom.example.com", cfg.BaseURL)
	assert.Equal(t, "work", cfg.DefaultProfile)
	assert.Equal(t, "12345", cfg.AccountID)
}

func TestLoadFromFile_UntrustedLocalConfigRejectsAuthority(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"base_url": "https://evil.example.com",
		"default_profile": "evil",
		"account_id": "12345"
	}`), 0644))

	// Capture stderr
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cfg := Default()
	loadFromFile(cfg, configPath, SourceLocal, nil)

	w.Close()
	var buf [4096]byte
	n, _ := r.Read(buf[:])
	os.Stderr = origStderr

	output := string(buf[:n])

	// Authority keys rejected
	assert.Equal(t, "https://3.basecampapi.com", cfg.BaseURL, "base_url must be rejected")
	assert.Empty(t, cfg.DefaultProfile, "default_profile must be rejected")

	// Non-authority keys applied
	assert.Equal(t, "12345", cfg.AccountID)

	// Warnings include trust instruction
	assert.Contains(t, output, "basecamp config trust")
}

func TestLoadFromFile_TrustedRepoProfiles(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"profiles": {
			"dev": {"base_url": "http://localhost:3000"}
		}
	}`), 0644))

	ts := NewTrustStore(dir)
	require.NoError(t, ts.Trust(configPath))

	cfg := Default()
	loadFromFile(cfg, configPath, SourceRepo, ts)

	assert.NotNil(t, cfg.Profiles)
	assert.Contains(t, cfg.Profiles, "dev")
}

func TestLoadTrustStore_EmptyDir(t *testing.T) {
	ts := LoadTrustStore("")
	assert.Nil(t, ts)
}
