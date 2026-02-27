package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefault(t *testing.T) {
	cfg := Default()

	// Check default values
	assert.Equal(t, "https://3.basecampapi.com", cfg.BaseURL)
	assert.Equal(t, "read", cfg.Scope)
	assert.True(t, cfg.CacheEnabled)
	assert.Equal(t, "auto", cfg.Format)
	assert.NotNil(t, cfg.Sources)
}

func TestLoadFromFile(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// Write test config
	testConfig := map[string]any{
		"base_url":      "http://test.example.com",
		"account_id":    "12345",
		"project_id":    "67890",
		"todolist_id":   "11111",
		"scope":         "read,write",
		"cache_dir":     "/tmp/cache",
		"cache_enabled": false,
		"format":        "json",
	}
	data, err := json.Marshal(testConfig)
	require.NoError(t, err)
	err = os.WriteFile(configPath, data, 0644)
	require.NoError(t, err)

	cfg := Default()
	loadFromFile(cfg, configPath, SourceGlobal)

	// Verify values loaded
	assert.Equal(t, "http://test.example.com", cfg.BaseURL)
	assert.Equal(t, "12345", cfg.AccountID)
	assert.Equal(t, "67890", cfg.ProjectID)
	assert.Equal(t, "11111", cfg.TodolistID)
	assert.Equal(t, "read,write", cfg.Scope)
	assert.Equal(t, "/tmp/cache", cfg.CacheDir)
	assert.False(t, cfg.CacheEnabled)
	assert.Equal(t, "json", cfg.Format)

	// Verify source tracking
	assert.Equal(t, "global", cfg.Sources["base_url"])
	assert.Equal(t, "global", cfg.Sources["account_id"])
}

func TestLoadFromFileSkipsInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// Write invalid JSON
	err := os.WriteFile(configPath, []byte("not valid json"), 0644)
	require.NoError(t, err)

	cfg := Default()
	loadFromFile(cfg, configPath, SourceGlobal)

	// Should still have defaults
	assert.Equal(t, "https://3.basecampapi.com", cfg.BaseURL)
}

func TestLoadFromFileSkipsMissingFile(t *testing.T) {
	cfg := Default()
	loadFromFile(cfg, "/nonexistent/path/config.json", SourceGlobal)

	// Should still have defaults
	assert.Equal(t, "https://3.basecampapi.com", cfg.BaseURL)
}

func TestLoadFromEnv(t *testing.T) {
	// Save and clear env vars
	originalEnvVars := map[string]string{
		"BASECAMP_BASE_URL":      os.Getenv("BASECAMP_BASE_URL"),
		"BASECAMP_ACCOUNT_ID":    os.Getenv("BASECAMP_ACCOUNT_ID"),
		"BASECAMP_PROJECT_ID":    os.Getenv("BASECAMP_PROJECT_ID"),
		"BASECAMP_TODOLIST_ID":   os.Getenv("BASECAMP_TODOLIST_ID"),
		"BASECAMP_CACHE_DIR":     os.Getenv("BASECAMP_CACHE_DIR"),
		"BASECAMP_CACHE_ENABLED": os.Getenv("BASECAMP_CACHE_ENABLED"),
	}
	defer func() {
		for k, v := range originalEnvVars {
			if v == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, v)
			}
		}
	}()

	// Clear all relevant env vars first
	for k := range originalEnvVars {
		os.Unsetenv(k)
	}

	// Set test values
	os.Setenv("BASECAMP_BASE_URL", "http://env.example.com")
	os.Setenv("BASECAMP_ACCOUNT_ID", "env-account")
	os.Setenv("BASECAMP_PROJECT_ID", "env-project")
	os.Setenv("BASECAMP_TODOLIST_ID", "env-todolist")
	os.Setenv("BASECAMP_CACHE_DIR", "/env/cache")
	os.Setenv("BASECAMP_CACHE_ENABLED", "false")

	cfg := Default()
	LoadFromEnv(cfg)

	// Verify values loaded
	assert.Equal(t, "http://env.example.com", cfg.BaseURL)
	assert.Equal(t, "env-account", cfg.AccountID)
	assert.Equal(t, "env-project", cfg.ProjectID)
	assert.Equal(t, "env-todolist", cfg.TodolistID)
	assert.Equal(t, "/env/cache", cfg.CacheDir)
	assert.False(t, cfg.CacheEnabled)

	// Verify source tracking
	assert.Equal(t, "env", cfg.Sources["base_url"])
}

func TestLoadFromEnvPrecedence(t *testing.T) {
	// BASECAMP_* env vars are the canonical names
	originalEnvVars := map[string]string{
		"BASECAMP_BASE_URL": os.Getenv("BASECAMP_BASE_URL"),
	}
	defer func() {
		for k, v := range originalEnvVars {
			if v == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, v)
			}
		}
	}()

	os.Setenv("BASECAMP_BASE_URL", "http://basecamp.example.com")

	cfg := Default()
	LoadFromEnv(cfg)

	assert.Equal(t, "http://basecamp.example.com", cfg.BaseURL)
}

func TestApplyOverrides(t *testing.T) {
	cfg := Default()
	cfg.AccountID = "from-file"
	cfg.ProjectID = "from-file"
	cfg.Sources["account_id"] = "global"
	cfg.Sources["project_id"] = "global"

	overrides := FlagOverrides{
		Account:  "from-flag",
		Project:  "from-flag",
		CacheDir: "/flag/cache",
		Format:   "json",
	}

	ApplyOverrides(cfg, overrides)

	assert.Equal(t, "from-flag", cfg.AccountID)
	assert.Equal(t, "from-flag", cfg.ProjectID)
	assert.Equal(t, "/flag/cache", cfg.CacheDir)
	assert.Equal(t, "json", cfg.Format)

	// Verify source tracking
	assert.Equal(t, "flag", cfg.Sources["account_id"])
}

func TestApplyOverridesSkipsEmpty(t *testing.T) {
	cfg := Default()
	cfg.AccountID = "original"
	cfg.Sources["account_id"] = "global"

	overrides := FlagOverrides{
		Account: "", // empty should not override
	}

	ApplyOverrides(cfg, overrides)

	assert.Equal(t, "original", cfg.AccountID)
	assert.Equal(t, "global", cfg.Sources["account_id"])
}

func TestConfigLayering(t *testing.T) {
	// Create temp dirs for config files
	tmpDir := t.TempDir()

	// Create global config
	globalDir := filepath.Join(tmpDir, ".config", "basecamp")
	err := os.MkdirAll(globalDir, 0755)
	require.NoError(t, err)
	globalConfig := map[string]any{
		"account_id": "global-account",
		"project_id": "global-project",
	}
	data, err := json.Marshal(globalConfig)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(globalDir, "config.json"), data, 0644)
	require.NoError(t, err)

	// Create local config with different values
	localDir := filepath.Join(tmpDir, "project", ".basecamp")
	err = os.MkdirAll(localDir, 0755)
	require.NoError(t, err)
	localConfig := map[string]any{
		"project_id": "local-project", // overrides global
	}
	data, err = json.Marshal(localConfig)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(localDir, "config.json"), data, 0644)
	require.NoError(t, err)

	cfg := Default()

	// Load in order: global then local (local wins)
	loadFromFile(cfg, filepath.Join(globalDir, "config.json"), SourceGlobal)
	loadFromFile(cfg, filepath.Join(localDir, "config.json"), SourceLocal)

	// account_id from global (not in local)
	assert.Equal(t, "global-account", cfg.AccountID)

	// project_id from local (overrides global)
	assert.Equal(t, "local-project", cfg.ProjectID)

	// Source tracking
	assert.Equal(t, "global", cfg.Sources["account_id"])
	assert.Equal(t, "local", cfg.Sources["project_id"])
}

func TestFullLayeringPrecedence(t *testing.T) {
	// Test: flags > env > local > repo > global > system > defaults

	// Save original env
	originalAccountID := os.Getenv("BASECAMP_ACCOUNT_ID")
	defer func() {
		if originalAccountID == "" {
			os.Unsetenv("BASECAMP_ACCOUNT_ID")
		} else {
			os.Setenv("BASECAMP_ACCOUNT_ID", originalAccountID)
		}
	}()

	// Create temp config files
	tmpDir := t.TempDir()
	globalConfig := filepath.Join(tmpDir, "global.json")
	localConfig := filepath.Join(tmpDir, "local.json")

	// Global: sets all 3 values
	data, err := json.Marshal(map[string]any{
		"account_id":  "global",
		"project_id":  "global",
		"todolist_id": "global",
	})
	require.NoError(t, err)
	err = os.WriteFile(globalConfig, data, 0644)
	require.NoError(t, err)

	// Local: sets project_id and todolist_id (overrides global)
	data, err = json.Marshal(map[string]any{
		"project_id":  "local",
		"todolist_id": "local",
	})
	require.NoError(t, err)
	err = os.WriteFile(localConfig, data, 0644)
	require.NoError(t, err)

	// Env: sets todolist_id (overrides local)
	os.Setenv("BASECAMP_TODOLIST_ID", "env")

	// Start with defaults
	cfg := Default()

	// Apply layers in order
	loadFromFile(cfg, globalConfig, SourceGlobal)
	loadFromFile(cfg, localConfig, SourceLocal)
	LoadFromEnv(cfg)
	ApplyOverrides(cfg, FlagOverrides{
		// No flag overrides
	})

	// account_id: only global sets it
	assert.Equal(t, "global", cfg.AccountID)

	// project_id: local overrides global
	assert.Equal(t, "local", cfg.ProjectID)

	// todolist_id: env overrides local
	assert.Equal(t, "env", cfg.TodolistID)

	// Clean up
	os.Unsetenv("BASECAMP_TODOLIST_ID")
}

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://example.com/", "https://example.com"},
		{"https://example.com", "https://example.com"},
		{"https://example.com//", "https://example.com/"},
		{"http://localhost:3000/", "http://localhost:3000"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := NormalizeBaseURL(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGlobalConfigDir(t *testing.T) {
	// Save and restore XDG_CONFIG_HOME
	original := os.Getenv("XDG_CONFIG_HOME")
	defer func() {
		if original == "" {
			os.Unsetenv("XDG_CONFIG_HOME")
		} else {
			os.Setenv("XDG_CONFIG_HOME", original)
		}
	}()

	// Test with XDG_CONFIG_HOME set
	os.Setenv("XDG_CONFIG_HOME", "/custom/config")
	result := GlobalConfigDir()
	assert.Equal(t, "/custom/config/basecamp", result)

	// Test without XDG_CONFIG_HOME (falls back to ~/.config)
	os.Unsetenv("XDG_CONFIG_HOME")
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	expected := filepath.Join(home, ".config", "basecamp")
	result = GlobalConfigDir()
	assert.Equal(t, expected, result)
}

func TestCacheEnabledEnvParsing(t *testing.T) {
	tests := []struct {
		envValue     string
		startValue   bool
		expected     bool
		shouldChange bool
	}{
		{"true", false, true, true},
		{"TRUE", false, true, true},
		{"True", false, true, true},
		{"1", false, true, true},
		{"false", true, false, true},
		{"FALSE", true, false, true},
		{"0", true, false, true},
		{"invalid", true, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.envValue, func(t *testing.T) {
			// Save and restore
			original := os.Getenv("BASECAMP_CACHE_ENABLED")
			defer func() {
				if original == "" {
					os.Unsetenv("BASECAMP_CACHE_ENABLED")
				} else {
					os.Setenv("BASECAMP_CACHE_ENABLED", original)
				}
			}()

			os.Setenv("BASECAMP_CACHE_ENABLED", tt.envValue)

			cfg := Default()
			cfg.CacheEnabled = tt.startValue
			LoadFromEnv(cfg)

			assert.Equal(t, tt.expected, cfg.CacheEnabled)
		})
	}
}

func TestCacheEnabledEnvEmpty(t *testing.T) {
	// Empty env var should not change the value
	original := os.Getenv("BASECAMP_CACHE_ENABLED")
	defer func() {
		if original == "" {
			os.Unsetenv("BASECAMP_CACHE_ENABLED")
		} else {
			os.Setenv("BASECAMP_CACHE_ENABLED", original)
		}
	}()

	os.Unsetenv("BASECAMP_CACHE_ENABLED")

	cfg := Default()
	cfg.CacheEnabled = true
	LoadFromEnv(cfg)

	// Should remain true (env var not set, so doesn't change)
	assert.True(t, cfg.CacheEnabled)
}

func TestLoadFromFilePartialConfig(t *testing.T) {
	// Test that partial configs don't reset other fields
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// Config that only sets one field
	partialConfig := map[string]any{
		"project_id": "only-project",
	}
	data, err := json.Marshal(partialConfig)
	require.NoError(t, err)
	err = os.WriteFile(configPath, data, 0644)
	require.NoError(t, err)

	cfg := Default()
	cfg.AccountID = "pre-existing-account"
	cfg.Sources["account_id"] = "manual"

	loadFromFile(cfg, configPath, SourceLocal)

	// project_id should be set
	assert.Equal(t, "only-project", cfg.ProjectID)

	// account_id should remain unchanged
	assert.Equal(t, "pre-existing-account", cfg.AccountID)

	// Source for account_id should remain unchanged
	assert.Equal(t, "manual", cfg.Sources["account_id"])
}

func TestLoadFromFileEmptyValues(t *testing.T) {
	// Empty string values should not override existing values
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	configWithEmpty := map[string]any{
		"account_id": "", // Empty
		"project_id": "real-value",
	}
	data, err := json.Marshal(configWithEmpty)
	require.NoError(t, err)
	err = os.WriteFile(configPath, data, 0644)
	require.NoError(t, err)

	cfg := Default()
	cfg.AccountID = "existing"
	cfg.Sources["account_id"] = "manual"

	loadFromFile(cfg, configPath, SourceLocal)

	// account_id should remain unchanged (empty value doesn't override)
	assert.Equal(t, "existing", cfg.AccountID)

	// project_id should be set
	assert.Equal(t, "real-value", cfg.ProjectID)
}

func TestLoadFromFileWithProfiles(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	testConfig := map[string]any{
		"default_profile": "production",
		"profiles": map[string]any{
			"production": map[string]any{
				"base_url":   "https://3.basecampapi.com",
				"client_id":  "prod-client-123",
				"account_id": "12345",
			},
			"beta": map[string]any{
				"base_url":   "https://3.basecamp-beta.com",
				"client_id":  "beta-client-456",
				"account_id": 67890,
			},
			"dev": map[string]any{
				"base_url": "http://localhost:3000",
			},
		},
	}
	data, _ := json.Marshal(testConfig)
	os.WriteFile(configPath, data, 0644)

	cfg := Default()
	loadFromFile(cfg, configPath, SourceGlobal)

	// Verify default_profile
	if cfg.DefaultProfile != "production" {
		t.Errorf("DefaultProfile = %q, want %q", cfg.DefaultProfile, "production")
	}

	// Verify profiles map
	if cfg.Profiles == nil {
		t.Fatal("Profiles map should not be nil")
	}
	if len(cfg.Profiles) != 3 {
		t.Errorf("len(Profiles) = %d, want 3", len(cfg.Profiles))
	}

	// Verify production profile
	if prod, ok := cfg.Profiles["production"]; ok {
		if prod.BaseURL != "https://3.basecampapi.com" {
			t.Errorf("Profiles[production].BaseURL = %q, want %q", prod.BaseURL, "https://3.basecampapi.com")
		}
		if prod.ClientID != "prod-client-123" {
			t.Errorf("Profiles[production].ClientID = %q, want %q", prod.ClientID, "prod-client-123")
		}
		if prod.AccountID != "12345" {
			t.Errorf("Profiles[production].AccountID = %q, want %q", prod.AccountID, "12345")
		}
	} else {
		t.Error("Profiles[production] not found")
	}

	// Verify beta profile (with numeric account_id)
	if beta, ok := cfg.Profiles["beta"]; ok {
		if beta.AccountID != "67890" {
			t.Errorf("Profiles[beta].AccountID = %q, want %q", beta.AccountID, "67890")
		}
	} else {
		t.Error("Profiles[beta] not found")
	}

	// Verify dev profile (no client_id)
	if dev, ok := cfg.Profiles["dev"]; ok {
		if dev.BaseURL != "http://localhost:3000" {
			t.Errorf("Profiles[dev].BaseURL = %q, want %q", dev.BaseURL, "http://localhost:3000")
		}
		if dev.ClientID != "" {
			t.Errorf("Profiles[dev].ClientID = %q, want empty", dev.ClientID)
		}
	} else {
		t.Error("Profiles[dev] not found")
	}

	// Verify source tracking
	if cfg.Sources["default_profile"] != "global" {
		t.Errorf("Sources[default_profile] = %q, want %q", cfg.Sources["default_profile"], "global")
	}
	if cfg.Sources["profiles"] != "global" {
		t.Errorf("Sources[profiles] = %q, want %q", cfg.Sources["profiles"], "global")
	}
}

func TestProfilesConfigLayering(t *testing.T) {
	tmpDir := t.TempDir()
	globalPath := filepath.Join(tmpDir, "global.json")
	localPath := filepath.Join(tmpDir, "local.json")

	// Global config with production and beta
	globalConfig := map[string]any{
		"default_profile": "production",
		"profiles": map[string]any{
			"production": map[string]any{
				"base_url": "https://3.basecampapi.com",
			},
			"beta": map[string]any{
				"base_url": "https://3.basecamp-beta.com",
			},
		},
	}
	data, _ := json.Marshal(globalConfig)
	os.WriteFile(globalPath, data, 0644)

	// Local config tries to add dev profile and override default_profile —
	// both are authority keys and must be rejected from local sources.
	localConfig := map[string]any{
		"default_profile": "dev",
		"profiles": map[string]any{
			"dev": map[string]any{
				"base_url": "http://localhost:3000",
			},
		},
		"account_id": "99999", // Non-authority key, should be applied
	}
	data, _ = json.Marshal(localConfig)
	os.WriteFile(localPath, data, 0644)

	cfg := Default()
	loadFromFile(cfg, globalPath, SourceGlobal)
	loadFromFile(cfg, localPath, SourceLocal)

	// default_profile from local must be rejected — global value persists
	if cfg.DefaultProfile != "production" {
		t.Errorf("DefaultProfile = %q, want %q (local override must be rejected)", cfg.DefaultProfile, "production")
	}

	// profiles from local must be rejected — only global profiles persist
	if len(cfg.Profiles) != 2 {
		t.Errorf("len(Profiles) = %d, want 2 (production + beta only)", len(cfg.Profiles))
	}

	if _, ok := cfg.Profiles["production"]; !ok {
		t.Error("Profiles[production] from global should be present")
	}
	if _, ok := cfg.Profiles["beta"]; !ok {
		t.Error("Profiles[beta] from global should be present")
	}
	if _, ok := cfg.Profiles["dev"]; ok {
		t.Error("Profiles[dev] from local should be rejected")
	}

	// Non-authority keys from local config should still be applied
	assert.Equal(t, "99999", cfg.AccountID, "non-authority key account_id from local should be applied")
}

func TestApplyProfile(t *testing.T) {
	cfg := Default()
	cfg.Profiles = map[string]*ProfileConfig{
		"personal": {
			BaseURL:   "https://3.basecampapi.com",
			AccountID: "12345",
			Scope:     "full",
		},
	}

	err := cfg.ApplyProfile("personal")
	assert.NoError(t, err)
	assert.Equal(t, "personal", cfg.ActiveProfile)
	assert.Equal(t, "https://3.basecampapi.com", cfg.BaseURL)
	assert.Equal(t, "12345", cfg.AccountID)
	assert.Equal(t, "full", cfg.Scope)
	assert.Equal(t, "profile", cfg.Sources["base_url"])
	assert.Equal(t, "profile", cfg.Sources["account_id"])
	assert.Equal(t, "profile", cfg.Sources["scope"])
}

func TestApplyProfileNotFound(t *testing.T) {
	cfg := Default()
	cfg.Profiles = map[string]*ProfileConfig{
		"personal": {BaseURL: "https://3.basecampapi.com"},
	}

	err := cfg.ApplyProfile("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestLocalConfigPaths_StopsAtRepoRoot(t *testing.T) {
	// Create a directory structure:
	//   tmpDir/
	//     .basecamp/config.json  ← ABOVE repo root, should NOT be loaded
	//     repo/
	//       .git/                ← repo root marker
	//       .basecamp/config.json ← repo config (loaded as SourceRepo, excluded from local)
	//       sub/
	//         .basecamp/config.json ← local config INSIDE repo, should be loaded
	tmpDir, _ := filepath.EvalSymlinks(t.TempDir())

	aboveRepo := filepath.Join(tmpDir, ".basecamp")
	require.NoError(t, os.MkdirAll(aboveRepo, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(aboveRepo, "config.json"), []byte(`{"account_id":"evil"}`), 0644))

	repoRoot := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, ".git"), 0755))
	repoConfig := filepath.Join(repoRoot, ".basecamp")
	require.NoError(t, os.MkdirAll(repoConfig, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoConfig, "config.json"), []byte(`{"account_id":"repo"}`), 0644))

	subDir := filepath.Join(repoRoot, "sub")
	subConfig := filepath.Join(subDir, ".basecamp")
	require.NoError(t, os.MkdirAll(subConfig, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(subConfig, "config.json"), []byte(`{"account_id":"local"}`), 0644))

	// Change to sub dir
	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(subDir))
	defer os.Chdir(origDir)

	repoCfgPath := filepath.Join(repoConfig, "config.json")
	paths := localConfigPaths(repoCfgPath)

	// Should only contain the sub dir config, NOT the above-repo config
	assert.Len(t, paths, 1, "should find exactly 1 local config (sub dir)")
	assert.Equal(t, filepath.Join(subConfig, "config.json"), paths[0])
}

func TestLocalConfigPaths_NoRepo_OnlyCWD(t *testing.T) {
	// Without a git repo, only CWD's config should be loaded.
	//   tmpDir/
	//     parent/
	//       .basecamp/config.json ← should NOT be loaded
	//       child/
	//         .basecamp/config.json ← should be loaded (CWD)
	tmpDir, _ := filepath.EvalSymlinks(t.TempDir())

	parentConfig := filepath.Join(tmpDir, "parent", ".basecamp")
	require.NoError(t, os.MkdirAll(parentConfig, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(parentConfig, "config.json"), []byte(`{"account_id":"parent"}`), 0644))

	childDir := filepath.Join(tmpDir, "parent", "child")
	childConfig := filepath.Join(childDir, ".basecamp")
	require.NoError(t, os.MkdirAll(childConfig, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(childConfig, "config.json"), []byte(`{"account_id":"child"}`), 0644))

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(childDir))
	defer os.Chdir(origDir)

	// No repo config
	paths := localConfigPaths("")

	// Should only contain child config, not parent
	assert.Len(t, paths, 1, "should find exactly 1 local config (CWD)")
	assert.Equal(t, filepath.Join(childConfig, "config.json"), paths[0])
}

func TestRepoConfigPath_StopsAtHome(t *testing.T) {
	// Create a fake .git above $HOME (simulating /tmp/.git attack).
	// repoConfigPath should not walk above $HOME.
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	// We can't actually create a .git in a parent of $HOME without root,
	// but we can verify the boundary by working in a subdirectory of tmpDir
	// where we control the HOME env var.
	tmpDir, _ := filepath.EvalSymlinks(t.TempDir())
	fakeHome := filepath.Join(tmpDir, "home", "user")
	require.NoError(t, os.MkdirAll(fakeHome, 0755))

	// Put a .git and .basecamp/config.json ABOVE fake home
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".basecamp"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".basecamp", "config.json"), []byte(`{"base_url":"https://evil.com"}`), 0644))

	// Work inside fake home
	workDir := filepath.Join(fakeHome, "projects", "myapp")
	require.NoError(t, os.MkdirAll(workDir, 0755))

	origDir, _ := os.Getwd()
	origHome := os.Getenv("HOME")
	require.NoError(t, os.Chdir(workDir))
	os.Setenv("HOME", fakeHome)
	defer func() {
		os.Chdir(origDir)
		os.Setenv("HOME", origHome)
	}()
	_ = home // suppress unused

	// repoConfigPath should NOT find the .git above fake home
	result := repoConfigPath()
	assert.Empty(t, result, "should not find repo config above HOME")
}

func TestLoadFromFile_BaseURLRejectedFromLocal(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	require.NoError(t, os.WriteFile(configPath, []byte(`{"base_url":"https://custom.example.com"}`), 0644))

	// Capture stderr
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cfg := Default()
	loadFromFile(cfg, configPath, SourceLocal)

	w.Close()
	var buf [1024]byte
	n, _ := r.Read(buf[:])
	os.Stderr = origStderr

	output := string(buf[:n])
	assert.Contains(t, output, "warning: ignoring base_url")
	assert.Contains(t, output, "https://custom.example.com")
	assert.Contains(t, output, "local")

	// Value must NOT be applied
	assert.Equal(t, "https://3.basecampapi.com", cfg.BaseURL, "local config should not override base_url")
}

func TestLoadFromFile_BaseURLNoWarningForGlobal(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	require.NoError(t, os.WriteFile(configPath, []byte(`{"base_url":"https://custom.example.com"}`), 0644))

	// Capture stderr
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cfg := Default()
	loadFromFile(cfg, configPath, SourceGlobal)

	w.Close()
	var buf [1024]byte
	n, _ := r.Read(buf[:])
	os.Stderr = origStderr

	output := string(buf[:n])
	assert.Empty(t, output, "global config should not emit base_url warning")
}

func TestLoadFromFile_MalformedJSONWarning(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	require.NoError(t, os.WriteFile(configPath, []byte(`{not valid json}`), 0644))

	// Capture stderr
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cfg := Default()
	loadFromFile(cfg, configPath, SourceGlobal)

	w.Close()
	var buf [1024]byte
	n, _ := r.Read(buf[:])
	os.Stderr = origStderr

	output := string(buf[:n])
	assert.Contains(t, output, "warning: skipping malformed config")
	assert.Contains(t, output, configPath)

	// Should still have defaults
	assert.Equal(t, "https://3.basecampapi.com", cfg.BaseURL)
}

func TestPreferenceFieldsNilByDefault(t *testing.T) {
	cfg := Default()
	assert.Nil(t, cfg.Hints, "Hints should be nil by default")
	assert.Nil(t, cfg.Stats, "Stats should be nil by default")
	assert.Nil(t, cfg.Verbose, "Verbose should be nil by default")
}

func TestLoadPreferencesFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	testConfig := map[string]any{
		"hints":   true,
		"stats":   false,
		"verbose": 2,
	}
	data, err := json.Marshal(testConfig)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0644))

	cfg := Default()
	loadFromFile(cfg, configPath, SourceGlobal)

	require.NotNil(t, cfg.Hints)
	assert.True(t, *cfg.Hints)
	assert.Equal(t, "global", cfg.Sources["hints"])

	require.NotNil(t, cfg.Stats)
	assert.False(t, *cfg.Stats)
	assert.Equal(t, "global", cfg.Sources["stats"])

	require.NotNil(t, cfg.Verbose)
	assert.Equal(t, 2, *cfg.Verbose)
	assert.Equal(t, "global", cfg.Sources["verbose"])
}

func TestPreferenceLayering(t *testing.T) {
	tmpDir := t.TempDir()
	globalPath := filepath.Join(tmpDir, "global.json")
	localPath := filepath.Join(tmpDir, "local.json")

	// Global sets hints=true, stats=true
	globalConfig := map[string]any{"hints": true, "stats": true}
	data, _ := json.Marshal(globalConfig)
	os.WriteFile(globalPath, data, 0644)

	// Local overrides hints=false
	localConfig := map[string]any{"hints": false}
	data, _ = json.Marshal(localConfig)
	os.WriteFile(localPath, data, 0644)

	cfg := Default()
	loadFromFile(cfg, globalPath, SourceGlobal)
	loadFromFile(cfg, localPath, SourceLocal)

	// hints overridden by local
	require.NotNil(t, cfg.Hints)
	assert.False(t, *cfg.Hints)
	assert.Equal(t, "local", cfg.Sources["hints"])

	// stats from global (not in local)
	require.NotNil(t, cfg.Stats)
	assert.True(t, *cfg.Stats)
	assert.Equal(t, "global", cfg.Sources["stats"])
}

func TestPreferencesFromEnv(t *testing.T) {
	envVars := []string{"BASECAMP_HINTS", "BASECAMP_STATS"}
	originals := make(map[string]string)
	for _, k := range envVars {
		originals[k] = os.Getenv(k)
	}
	defer func() {
		for k, v := range originals {
			if v == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, v)
			}
		}
	}()

	os.Setenv("BASECAMP_HINTS", "true")
	os.Setenv("BASECAMP_STATS", "0")

	cfg := Default()
	LoadFromEnv(cfg)

	require.NotNil(t, cfg.Hints)
	assert.True(t, *cfg.Hints)
	assert.Equal(t, "env", cfg.Sources["hints"])

	require.NotNil(t, cfg.Stats)
	assert.False(t, *cfg.Stats)
	assert.Equal(t, "env", cfg.Sources["stats"])
}

func TestPreferencesEnvOverridesFile(t *testing.T) {
	// Save/restore env
	original := os.Getenv("BASECAMP_HINTS")
	defer func() {
		if original == "" {
			os.Unsetenv("BASECAMP_HINTS")
		} else {
			os.Setenv("BASECAMP_HINTS", original)
		}
	}()

	// File sets hints=true
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	data, _ := json.Marshal(map[string]any{"hints": true})
	os.WriteFile(configPath, data, 0644)

	// Env sets hints=false
	os.Setenv("BASECAMP_HINTS", "false")

	cfg := Default()
	loadFromFile(cfg, configPath, SourceGlobal)
	LoadFromEnv(cfg)

	require.NotNil(t, cfg.Hints)
	assert.False(t, *cfg.Hints, "env should override file")
	assert.Equal(t, "env", cfg.Sources["hints"])
}

func TestVerboseOutOfRangeIgnored(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// verbose: 5 is out of range (0-2) — should be ignored
	data, _ := json.Marshal(map[string]any{"verbose": 5})
	os.WriteFile(configPath, data, 0644)

	cfg := Default()
	loadFromFile(cfg, configPath, SourceGlobal)

	assert.Nil(t, cfg.Verbose, "out-of-range verbose should be ignored")
}

func TestVerboseFloatIgnored(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// verbose: 1.5 is not an integer — should be ignored
	data, _ := json.Marshal(map[string]any{"verbose": 1.5})
	os.WriteFile(configPath, data, 0644)

	cfg := Default()
	loadFromFile(cfg, configPath, SourceGlobal)

	assert.Nil(t, cfg.Verbose, "non-integer verbose should be ignored")
}

func TestEnvInvalidBoolIgnored(t *testing.T) {
	original := os.Getenv("BASECAMP_HINTS")
	defer func() {
		if original == "" {
			os.Unsetenv("BASECAMP_HINTS")
		} else {
			os.Setenv("BASECAMP_HINTS", original)
		}
	}()

	os.Setenv("BASECAMP_HINTS", "banana")

	cfg := Default()
	LoadFromEnv(cfg)

	assert.Nil(t, cfg.Hints, "invalid env bool should leave pointer nil")
}

func TestPreferencesUnsetInFile(t *testing.T) {
	// File without preference keys should leave them nil
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	data, _ := json.Marshal(map[string]any{"account_id": "12345"})
	os.WriteFile(configPath, data, 0644)

	cfg := Default()
	loadFromFile(cfg, configPath, SourceGlobal)

	assert.Nil(t, cfg.Hints)
	assert.Nil(t, cfg.Stats)
	assert.Nil(t, cfg.Verbose)
}
