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
		"BCQ_BASE_URL":         os.Getenv("BCQ_BASE_URL"),
		"BASECAMP_BASE_URL":    os.Getenv("BASECAMP_BASE_URL"),
		"BCQ_ACCOUNT":          os.Getenv("BCQ_ACCOUNT"),
		"BASECAMP_ACCOUNT_ID":  os.Getenv("BASECAMP_ACCOUNT_ID"),
		"BCQ_PROJECT":          os.Getenv("BCQ_PROJECT"),
		"BASECAMP_PROJECT_ID":  os.Getenv("BASECAMP_PROJECT_ID"),
		"BASECAMP_TODOLIST_ID": os.Getenv("BASECAMP_TODOLIST_ID"),
		"BCQ_CACHE_DIR":        os.Getenv("BCQ_CACHE_DIR"),
		"BCQ_CACHE_ENABLED":    os.Getenv("BCQ_CACHE_ENABLED"),
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
	os.Setenv("BCQ_BASE_URL", "http://env.example.com")
	os.Setenv("BCQ_ACCOUNT", "env-account")
	os.Setenv("BCQ_PROJECT", "env-project")
	os.Setenv("BASECAMP_TODOLIST_ID", "env-todolist")
	os.Setenv("BCQ_CACHE_DIR", "/env/cache")
	os.Setenv("BCQ_CACHE_ENABLED", "false")

	cfg := Default()
	loadFromEnv(cfg)

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
	// BCQ_* should override BASECAMP_*
	originalEnvVars := map[string]string{
		"BCQ_BASE_URL":      os.Getenv("BCQ_BASE_URL"),
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
	os.Setenv("BCQ_BASE_URL", "http://bcq.example.com")

	cfg := Default()
	loadFromEnv(cfg)

	// BCQ_BASE_URL should win (it's loaded last)
	assert.Equal(t, "http://bcq.example.com", cfg.BaseURL)
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
		BaseURL:  "http://flag.example.com",
		CacheDir: "/flag/cache",
		Format:   "json",
	}

	applyOverrides(cfg, overrides)

	assert.Equal(t, "from-flag", cfg.AccountID)
	assert.Equal(t, "from-flag", cfg.ProjectID)
	assert.Equal(t, "http://flag.example.com", cfg.BaseURL)
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

	applyOverrides(cfg, overrides)

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
	originalAccountID := os.Getenv("BCQ_ACCOUNT")
	defer func() {
		if originalAccountID == "" {
			os.Unsetenv("BCQ_ACCOUNT")
		} else {
			os.Setenv("BCQ_ACCOUNT", originalAccountID)
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
	loadFromEnv(cfg)
	applyOverrides(cfg, FlagOverrides{
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
			original := os.Getenv("BCQ_CACHE_ENABLED")
			defer func() {
				if original == "" {
					os.Unsetenv("BCQ_CACHE_ENABLED")
				} else {
					os.Setenv("BCQ_CACHE_ENABLED", original)
				}
			}()

			os.Setenv("BCQ_CACHE_ENABLED", tt.envValue)

			cfg := Default()
			cfg.CacheEnabled = tt.startValue
			loadFromEnv(cfg)

			assert.Equal(t, tt.expected, cfg.CacheEnabled)
		})
	}
}

func TestCacheEnabledEnvEmpty(t *testing.T) {
	// Empty env var should not change the value
	original := os.Getenv("BCQ_CACHE_ENABLED")
	defer func() {
		if original == "" {
			os.Unsetenv("BCQ_CACHE_ENABLED")
		} else {
			os.Setenv("BCQ_CACHE_ENABLED", original)
		}
	}()

	os.Unsetenv("BCQ_CACHE_ENABLED")

	cfg := Default()
	cfg.CacheEnabled = true
	loadFromEnv(cfg)

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

func TestLoadFromFileWithHosts(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	testConfig := map[string]any{
		"default_host": "production",
		"hosts": map[string]any{
			"production": map[string]any{
				"base_url":  "https://3.basecampapi.com",
				"client_id": "prod-client-123",
			},
			"beta": map[string]any{
				"base_url":  "https://3.basecamp-beta.com",
				"client_id": "beta-client-456",
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

	// Verify default_host
	if cfg.DefaultHost != "production" {
		t.Errorf("DefaultHost = %q, want %q", cfg.DefaultHost, "production")
	}

	// Verify hosts map
	if cfg.Hosts == nil {
		t.Fatal("Hosts map should not be nil")
	}
	if len(cfg.Hosts) != 3 {
		t.Errorf("len(Hosts) = %d, want 3", len(cfg.Hosts))
	}

	// Verify production host
	if prod, ok := cfg.Hosts["production"]; ok {
		if prod.BaseURL != "https://3.basecampapi.com" {
			t.Errorf("Hosts[production].BaseURL = %q, want %q", prod.BaseURL, "https://3.basecampapi.com")
		}
		if prod.ClientID != "prod-client-123" {
			t.Errorf("Hosts[production].ClientID = %q, want %q", prod.ClientID, "prod-client-123")
		}
	} else {
		t.Error("Hosts[production] not found")
	}

	// Verify dev host (no client_id)
	if dev, ok := cfg.Hosts["dev"]; ok {
		if dev.BaseURL != "http://localhost:3000" {
			t.Errorf("Hosts[dev].BaseURL = %q, want %q", dev.BaseURL, "http://localhost:3000")
		}
		if dev.ClientID != "" {
			t.Errorf("Hosts[dev].ClientID = %q, want empty", dev.ClientID)
		}
	} else {
		t.Error("Hosts[dev] not found")
	}

	// Verify source tracking
	if cfg.Sources["default_host"] != "global" {
		t.Errorf("Sources[default_host] = %q, want %q", cfg.Sources["default_host"], "global")
	}
	if cfg.Sources["hosts"] != "global" {
		t.Errorf("Sources[hosts] = %q, want %q", cfg.Sources["hosts"], "global")
	}
}

func TestHostsConfigLayering(t *testing.T) {
	tmpDir := t.TempDir()
	globalPath := filepath.Join(tmpDir, "global.json")
	localPath := filepath.Join(tmpDir, "local.json")

	// Global config with production and beta
	globalConfig := map[string]any{
		"default_host": "production",
		"hosts": map[string]any{
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

	// Local config adds dev and overrides default_host
	localConfig := map[string]any{
		"default_host": "dev",
		"hosts": map[string]any{
			"dev": map[string]any{
				"base_url": "http://localhost:3000",
			},
		},
	}
	data, _ = json.Marshal(localConfig)
	os.WriteFile(localPath, data, 0644)

	cfg := Default()
	loadFromFile(cfg, globalPath, SourceGlobal)
	loadFromFile(cfg, localPath, SourceLocal)

	// default_host should be overridden by local
	if cfg.DefaultHost != "dev" {
		t.Errorf("DefaultHost = %q, want %q", cfg.DefaultHost, "dev")
	}

	// hosts should be merged (global + local)
	if len(cfg.Hosts) != 3 {
		t.Errorf("len(Hosts) = %d, want 3 (production + beta + dev)", len(cfg.Hosts))
	}

	// Verify all hosts are present
	if _, ok := cfg.Hosts["production"]; !ok {
		t.Error("Hosts[production] from global should be present")
	}
	if _, ok := cfg.Hosts["beta"]; !ok {
		t.Error("Hosts[beta] from global should be present")
	}
	if _, ok := cfg.Hosts["dev"]; !ok {
		t.Error("Hosts[dev] from local should be present")
	}
}
