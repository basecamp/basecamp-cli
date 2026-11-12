package config

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/richtext"
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

// The untrusted-config warning names a `basecamp config trust <path>` a
// person pastes, so the path in it is shell-quoted. An apostrophe in the
// path is the case that tells quoting apart from wrapping — it has to be
// spliced out of the quotes and back in, since no escape reaches inside
// single quotes — and that holds wherever this builds.
//
// The fixture is a relative path under a directory this test changes into,
// so the expectation is an exact string rather than a substring of whatever
// the host spells its temporary directory as. TMPDIR may itself hold
// apostrophes or spaces, and a quoting test rooted in it would be asserting
// something about the machine (Copilot on #769).
//
// The other half of the change lives in trust_unix_test.go: this package's
// deleted copy wrapped every value whether or not it needed it, and the
// shared richtext.ShellQuote leaves an inert one alone. That is a statement
// about POSIX paths only. A Windows path carries backslashes, which mean
// something to a shell, so there is no bare path to assert there — and the
// Go suite runs on Linux, so an ungated assertion about it would have gone
// on passing while being wrong for everyone who runs the tests on Windows.
func TestTheTrustWarningSplicesAnApostropheInThePath(t *testing.T) {
	t.Chdir(t.TempDir())
	const configPath = "o'brien/config.json"
	require.NoError(t, os.MkdirAll("o'brien", 0o755))
	require.NoError(t, os.WriteFile(configPath, []byte(`{"base_url": "https://evil.example.com"}`), 0o644))

	// quote, backslash, quote, quote — the splice, not a wrapper.
	require.Equal(t, `'o'\''brien/config.json'`, richtext.ShellQuote(configPath))

	assert.Contains(t, captureStderr(t, func() {
		loadFromFile(Default(), configPath, SourceLocal, nil)
	}), "basecamp config trust "+`'o'\''brien/config.json'`+"`")
}

// captureStderr runs fn with os.Stderr redirected and returns what it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	require.NoError(t, w.Close())
	os.Stderr = orig
	return <-done
}

// writeTrustedConfig writes contents to a config file and trusts it.
func writeTrustedConfig(t *testing.T, contents string) (string, *TrustStore) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(contents), 0o600))
	ts := NewTrustStore(t.TempDir())
	require.NoError(t, ts.Trust(configPath))
	return configPath, ts
}

// Trust is given to a file's contents, not its path. A pull request that
// rewrites a trusted repo config to send a profile to another host leaves
// the file untrusted, so the profile it names is not taken from it.
func TestTrustStore_ChangedAuthorityKeysAreUntrusted(t *testing.T) {
	configPath, ts := writeTrustedConfig(t, `{"default_profile": "work"}`)
	assert.True(t, ts.IsTrusted(configPath))

	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"default_profile": "work",
		"profiles": {"work": {"base_url": "https://evil.example.com"}}
	}`), 0o600))
	assert.False(t, ts.IsTrusted(configPath))

	cfg := Default()
	cfg.Profiles = map[string]*ProfileConfig{"work": {BaseURL: "https://3.basecampapi.com"}}
	loadFromFile(cfg, configPath, SourceRepo, ts)
	assert.Equal(t, "https://3.basecampapi.com", cfg.Profiles["work"].BaseURL, "the rewritten profile is ignored")
	assert.Empty(t, cfg.DefaultProfile, "and so is everything else trust-gated in the file")

	require.NoError(t, ts.Trust(configPath))
	cfg = Default()
	loadFromFile(cfg, configPath, SourceRepo, ts)
	assert.Equal(t, "https://evil.example.com", cfg.Profiles["work"].BaseURL, "until it is trusted again")
}

// Changing what an untrusted config may set anyway, or the file's layout,
// keeps its trust: `basecamp config set project_id` does not undo it.
func TestTrustStore_UngatedChangesKeepTrust(t *testing.T) {
	configPath, ts := writeTrustedConfig(t, `{"base_url": "https://custom.example.com", "project_id": "1"}`)

	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"project_id": "2",
		"todolist_id": "3",
		"base_url":   "https://custom.example.com"
	}`), 0o600))
	assert.True(t, ts.IsTrusted(configPath))
}

// An entry recorded before trust covered content names only a path. It
// trusts nothing, so a file trusted then is confirmed again before its
// authority keys apply.
func TestTrustStore_PathOnlyEntryIsUntrusted(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"base_url": "https://custom.example.com"}`), 0o600))
	storeDir := t.TempDir()
	legacy, err := json.Marshal(map[string]any{"trusted": []map[string]string{
		{"path": canonicalizePath(configPath), "trusted_at": "2026-03-03T00:00:00Z"},
	}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(storeDir, "trusted-configs.json"), legacy, 0o600))

	ts := NewTrustStore(storeDir)
	require.Len(t, ts.List(), 1)
	assert.False(t, ts.IsTrusted(configPath))

	cfg := Default()
	loadFromFile(cfg, configPath, SourceLocal, ts)
	assert.Equal(t, "https://3.basecampapi.com", cfg.BaseURL)

	require.NoError(t, ts.Trust(configPath))
	assert.True(t, ts.IsTrusted(configPath))
	assert.Len(t, ts.List(), 1, "trusting again updates the entry")
}

// The digest leaves out the keys an untrusted config sets anyway. A key that
// trust gates must not be among them, or changing it would keep the trust.
func TestTrustStore_DigestOmitsOnlyUngatedKeys(t *testing.T) {
	values := map[string]any{
		"account_id": "1", "project_id": "2", "todolist_id": "3", "scope": "read",
		"format": "json", "hints": true, "stats": true, "onboarded": true, "verbose": 1,
		"experimental": map[string]any{"feature": true},
	}
	require.Len(t, values, len(untrustedKeys))
	for _, key := range untrustedKeys {
		data, err := json.Marshal(map[string]any{key: values[key]})
		require.NoError(t, err)
		path := filepath.Join(t.TempDir(), "config.json")
		require.NoError(t, os.WriteFile(path, data, 0o600))

		cfg := Default()
		loadFromFile(cfg, path, SourceRepo, nil)
		source := key
		if key == "experimental" {
			source = "experimental.feature"
		}
		assert.Equal(t, string(SourceRepo), cfg.Sources[source], "%s is taken from an untrusted config", key)
	}
}

// The loader must check the bytes it parsed, not read the path a second
// time: the file could have changed between the two reads.
func TestTrustStore_ChecksTheSuppliedContent(t *testing.T) {
	original := []byte(`{"base_url":"https://original.example.com"}`)
	configPath, ts := writeTrustedConfig(t, string(original))
	changed := []byte(`{"base_url":"https://changed.example.com"}`)
	require.NoError(t, os.WriteFile(configPath, changed, 0o600))
	assert.True(t, ts.IsTrustedContent(configPath, original))
	assert.False(t, ts.IsTrustedContent(configPath, changed))
	assert.False(t, ts.IsTrusted(configPath))
}

func TestTrustStore_GatedAndUnknownKeysChangeTrust(t *testing.T) {
	values := map[string]any{
		"base_url":           "https://other.example.com",
		"default_profile":    "work",
		"profiles":           map[string]any{"work": map[string]any{"base_url": "https://other.example.com"}},
		"cache_dir":          "other-cache",
		"cache_enabled":      false,
		"llm_provider":       "openai",
		"llm_model":          "other-model",
		"llm_endpoint":       "https://other.example.com",
		"llm_max_concurrent": 5,
		"llm_token_budget":   4000,
		"future_setting":     "unknown keys fail closed too",
	}
	for key, value := range values {
		t.Run(key, func(t *testing.T) {
			configPath, ts := writeTrustedConfig(t, `{}`)
			data, err := json.Marshal(map[string]any{key: value})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(configPath, data, 0o600))
			assert.False(t, ts.IsTrusted(configPath))
		})
	}
}

func TestTrustStore_NestedKeyOrderKeepsTrust(t *testing.T) {
	configPath, ts := writeTrustedConfig(t, `{"profiles":{"work":{"base_url":"https://original.example.com","account_id":"1"}}}`)
	require.NoError(t, os.WriteFile(configPath, []byte(`{
		"profiles": {"work": {"account_id": "1", "base_url": "https://original.example.com"}}
	}`), 0o600))
	assert.True(t, ts.IsTrusted(configPath))
}

func TestTrustStore_TrustRefusesAFileThatIsNotAConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`not json`), 0o600))
	ts := NewTrustStore(dir)
	assert.Error(t, ts.Trust(configPath))
	assert.Empty(t, ts.List())
}
