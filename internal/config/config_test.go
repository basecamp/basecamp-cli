package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()

	// Check default values
	if cfg.BaseURL != "https://3.basecampapi.com" {
		t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, "https://3.basecampapi.com")
	}
	if cfg.Scope != "read" {
		t.Errorf("Scope = %q, want %q", cfg.Scope, "read")
	}
	if cfg.CacheEnabled != true {
		t.Errorf("CacheEnabled = %v, want true", cfg.CacheEnabled)
	}
	if cfg.Format != "auto" {
		t.Errorf("Format = %q, want %q", cfg.Format, "auto")
	}
	if cfg.Sources == nil {
		t.Error("Sources map should be initialized, got nil")
	}
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
	data, _ := json.Marshal(testConfig)
	os.WriteFile(configPath, data, 0644)

	cfg := Default()
	loadFromFile(cfg, configPath, SourceGlobal)

	// Verify values loaded
	if cfg.BaseURL != "http://test.example.com" {
		t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, "http://test.example.com")
	}
	if cfg.AccountID != "12345" {
		t.Errorf("AccountID = %q, want %q", cfg.AccountID, "12345")
	}
	if cfg.ProjectID != "67890" {
		t.Errorf("ProjectID = %q, want %q", cfg.ProjectID, "67890")
	}
	if cfg.TodolistID != "11111" {
		t.Errorf("TodolistID = %q, want %q", cfg.TodolistID, "11111")
	}
	if cfg.Scope != "read,write" {
		t.Errorf("Scope = %q, want %q", cfg.Scope, "read,write")
	}
	if cfg.CacheDir != "/tmp/cache" {
		t.Errorf("CacheDir = %q, want %q", cfg.CacheDir, "/tmp/cache")
	}
	if cfg.CacheEnabled != false {
		t.Errorf("CacheEnabled = %v, want false", cfg.CacheEnabled)
	}
	if cfg.Format != "json" {
		t.Errorf("Format = %q, want %q", cfg.Format, "json")
	}

	// Verify source tracking
	if cfg.Sources["base_url"] != "global" {
		t.Errorf("Sources[base_url] = %q, want %q", cfg.Sources["base_url"], "global")
	}
	if cfg.Sources["account_id"] != "global" {
		t.Errorf("Sources[account_id] = %q, want %q", cfg.Sources["account_id"], "global")
	}
}

func TestLoadFromFileSkipsInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// Write invalid JSON
	os.WriteFile(configPath, []byte("not valid json"), 0644)

	cfg := Default()
	loadFromFile(cfg, configPath, SourceGlobal)

	// Should still have defaults
	if cfg.BaseURL != "https://3.basecampapi.com" {
		t.Errorf("BaseURL should remain default after invalid JSON, got %q", cfg.BaseURL)
	}
}

func TestLoadFromFileSkipsMissingFile(t *testing.T) {
	cfg := Default()
	loadFromFile(cfg, "/nonexistent/path/config.json", SourceGlobal)

	// Should still have defaults
	if cfg.BaseURL != "https://3.basecampapi.com" {
		t.Errorf("BaseURL should remain default after missing file, got %q", cfg.BaseURL)
	}
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
	if cfg.BaseURL != "http://env.example.com" {
		t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, "http://env.example.com")
	}
	if cfg.AccountID != "env-account" {
		t.Errorf("AccountID = %q, want %q", cfg.AccountID, "env-account")
	}
	if cfg.ProjectID != "env-project" {
		t.Errorf("ProjectID = %q, want %q", cfg.ProjectID, "env-project")
	}
	if cfg.TodolistID != "env-todolist" {
		t.Errorf("TodolistID = %q, want %q", cfg.TodolistID, "env-todolist")
	}
	if cfg.CacheDir != "/env/cache" {
		t.Errorf("CacheDir = %q, want %q", cfg.CacheDir, "/env/cache")
	}
	if cfg.CacheEnabled != false {
		t.Errorf("CacheEnabled = %v, want false", cfg.CacheEnabled)
	}

	// Verify source tracking
	if cfg.Sources["base_url"] != "env" {
		t.Errorf("Sources[base_url] = %q, want %q", cfg.Sources["base_url"], "env")
	}
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
	if cfg.BaseURL != "http://bcq.example.com" {
		t.Errorf("BaseURL = %q, want %q (BCQ_ should override BASECAMP_)", cfg.BaseURL, "http://bcq.example.com")
	}
}

func TestApplyOverrides(t *testing.T) {
	cfg := Default()
	cfg.AccountID = "from-file"
	cfg.ProjectID = "from-file"
	cfg.Sources["account_id"] = "global"
	cfg.Sources["project_id"] = "global"

	overrides := FlagOverrides{
		Account:    "from-flag",
		Project:    "from-flag",
		BaseURL:    "http://flag.example.com",
		CacheDir:   "/flag/cache",
		Format:     "json",
		Verbose:    true,
		VerboseSet: true,
	}

	applyOverrides(cfg, overrides)

	if cfg.AccountID != "from-flag" {
		t.Errorf("AccountID = %q, want %q", cfg.AccountID, "from-flag")
	}
	if cfg.ProjectID != "from-flag" {
		t.Errorf("ProjectID = %q, want %q", cfg.ProjectID, "from-flag")
	}
	if cfg.BaseURL != "http://flag.example.com" {
		t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, "http://flag.example.com")
	}
	if cfg.CacheDir != "/flag/cache" {
		t.Errorf("CacheDir = %q, want %q", cfg.CacheDir, "/flag/cache")
	}
	if cfg.Format != "json" {
		t.Errorf("Format = %q, want %q", cfg.Format, "json")
	}
	if cfg.Verbose != true {
		t.Errorf("Verbose = %v, want true", cfg.Verbose)
	}

	// Verify source tracking
	if cfg.Sources["account_id"] != "flag" {
		t.Errorf("Sources[account_id] = %q, want %q", cfg.Sources["account_id"], "flag")
	}
	if cfg.Sources["verbose"] != "flag" {
		t.Errorf("Sources[verbose] = %q, want %q", cfg.Sources["verbose"], "flag")
	}
}

func TestApplyOverridesSkipsEmpty(t *testing.T) {
	cfg := Default()
	cfg.AccountID = "original"
	cfg.Sources["account_id"] = "global"

	overrides := FlagOverrides{
		Account: "", // empty should not override
	}

	applyOverrides(cfg, overrides)

	if cfg.AccountID != "original" {
		t.Errorf("AccountID = %q, want %q (empty override should not change)", cfg.AccountID, "original")
	}
	if cfg.Sources["account_id"] != "global" {
		t.Errorf("Sources[account_id] = %q, want %q", cfg.Sources["account_id"], "global")
	}
}

func TestApplyOverridesVerboseRequiresSet(t *testing.T) {
	cfg := Default()
	cfg.Verbose = true

	overrides := FlagOverrides{
		Verbose:    false,
		VerboseSet: false, // Not explicitly set
	}

	applyOverrides(cfg, overrides)

	if cfg.Verbose != true {
		t.Errorf("Verbose = %v, want true (VerboseSet=false should not change)", cfg.Verbose)
	}
}

func TestConfigLayering(t *testing.T) {
	// Create temp dirs for config files
	tmpDir := t.TempDir()

	// Create global config
	globalDir := filepath.Join(tmpDir, ".config", "basecamp")
	os.MkdirAll(globalDir, 0755)
	globalConfig := map[string]any{
		"account_id": "global-account",
		"project_id": "global-project",
	}
	data, _ := json.Marshal(globalConfig)
	os.WriteFile(filepath.Join(globalDir, "config.json"), data, 0644)

	// Create local config with different values
	localDir := filepath.Join(tmpDir, "project", ".basecamp")
	os.MkdirAll(localDir, 0755)
	localConfig := map[string]any{
		"project_id": "local-project", // overrides global
	}
	data, _ = json.Marshal(localConfig)
	os.WriteFile(filepath.Join(localDir, "config.json"), data, 0644)

	cfg := Default()

	// Load in order: global then local (local wins)
	loadFromFile(cfg, filepath.Join(globalDir, "config.json"), SourceGlobal)
	loadFromFile(cfg, filepath.Join(localDir, "config.json"), SourceLocal)

	// account_id from global (not in local)
	if cfg.AccountID != "global-account" {
		t.Errorf("AccountID = %q, want %q", cfg.AccountID, "global-account")
	}

	// project_id from local (overrides global)
	if cfg.ProjectID != "local-project" {
		t.Errorf("ProjectID = %q, want %q", cfg.ProjectID, "local-project")
	}

	// Source tracking
	if cfg.Sources["account_id"] != "global" {
		t.Errorf("Sources[account_id] = %q, want %q", cfg.Sources["account_id"], "global")
	}
	if cfg.Sources["project_id"] != "local" {
		t.Errorf("Sources[project_id] = %q, want %q", cfg.Sources["project_id"], "local")
	}
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
	data, _ := json.Marshal(map[string]any{
		"account_id":  "global",
		"project_id":  "global",
		"todolist_id": "global",
	})
	os.WriteFile(globalConfig, data, 0644)

	// Local: sets project_id and todolist_id (overrides global)
	data, _ = json.Marshal(map[string]any{
		"project_id":  "local",
		"todolist_id": "local",
	})
	os.WriteFile(localConfig, data, 0644)

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
	if cfg.AccountID != "global" {
		t.Errorf("AccountID = %q, want %q", cfg.AccountID, "global")
	}

	// project_id: local overrides global
	if cfg.ProjectID != "local" {
		t.Errorf("ProjectID = %q, want %q", cfg.ProjectID, "local")
	}

	// todolist_id: env overrides local
	if cfg.TodolistID != "env" {
		t.Errorf("TodolistID = %q, want %q", cfg.TodolistID, "env")
	}

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
			if result != tt.expected {
				t.Errorf("NormalizeBaseURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
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
	if result != "/custom/config/basecamp" {
		t.Errorf("GlobalConfigDir() = %q, want %q", result, "/custom/config/basecamp")
	}

	// Test without XDG_CONFIG_HOME (falls back to ~/.config)
	os.Unsetenv("XDG_CONFIG_HOME")
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".config", "basecamp")
	result = GlobalConfigDir()
	if result != expected {
		t.Errorf("GlobalConfigDir() = %q, want %q", result, expected)
	}
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

			if cfg.CacheEnabled != tt.expected {
				t.Errorf("CacheEnabled with env=%q: got %v, want %v", tt.envValue, cfg.CacheEnabled, tt.expected)
			}
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
	if cfg.CacheEnabled != true {
		t.Errorf("CacheEnabled should remain true when env not set, got %v", cfg.CacheEnabled)
	}
}

func TestLoadFromFilePartialConfig(t *testing.T) {
	// Test that partial configs don't reset other fields
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// Config that only sets one field
	partialConfig := map[string]any{
		"project_id": "only-project",
	}
	data, _ := json.Marshal(partialConfig)
	os.WriteFile(configPath, data, 0644)

	cfg := Default()
	cfg.AccountID = "pre-existing-account"
	cfg.Sources["account_id"] = "manual"

	loadFromFile(cfg, configPath, SourceLocal)

	// project_id should be set
	if cfg.ProjectID != "only-project" {
		t.Errorf("ProjectID = %q, want %q", cfg.ProjectID, "only-project")
	}

	// account_id should remain unchanged
	if cfg.AccountID != "pre-existing-account" {
		t.Errorf("AccountID = %q, want %q (should not be changed)", cfg.AccountID, "pre-existing-account")
	}

	// Source for account_id should remain unchanged
	if cfg.Sources["account_id"] != "manual" {
		t.Errorf("Sources[account_id] = %q, want %q", cfg.Sources["account_id"], "manual")
	}
}

func TestLoadFromFileEmptyValues(t *testing.T) {
	// Empty string values should not override existing values
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	configWithEmpty := map[string]any{
		"account_id": "", // Empty
		"project_id": "real-value",
	}
	data, _ := json.Marshal(configWithEmpty)
	os.WriteFile(configPath, data, 0644)

	cfg := Default()
	cfg.AccountID = "existing"
	cfg.Sources["account_id"] = "manual"

	loadFromFile(cfg, configPath, SourceLocal)

	// account_id should remain unchanged (empty value doesn't override)
	if cfg.AccountID != "existing" {
		t.Errorf("AccountID = %q, want %q (empty should not override)", cfg.AccountID, "existing")
	}

	// project_id should be set
	if cfg.ProjectID != "real-value" {
		t.Errorf("ProjectID = %q, want %q", cfg.ProjectID, "real-value")
	}
}
