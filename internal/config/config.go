// Package config provides layered configuration loading.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds the resolved configuration.
type Config struct {
	// API settings
	BaseURL    string `json:"base_url"`
	AccountID  string `json:"account_id"`
	ProjectID  string `json:"project_id"`
	TodolistID string `json:"todolist_id"`

	// Profile settings (named identity+environment bundles)
	Profiles       map[string]*ProfileConfig `json:"profiles,omitempty"`
	DefaultProfile string                    `json:"default_profile,omitempty"`
	ActiveProfile  string                    `json:"-"` // Set at runtime, not persisted

	// Auth settings
	Scope string `json:"scope"`

	// Cache settings
	CacheDir     string `json:"cache_dir"`
	CacheEnabled bool   `json:"cache_enabled"`

	// Output settings
	Format string `json:"format"`

	// Sources tracks where each value came from (for debugging).
	Sources map[string]string `json:"-"`
}

// ProfileConfig holds configuration for a named profile.
type ProfileConfig struct {
	BaseURL    string `json:"base_url"`
	AccountID  string `json:"account_id,omitempty"`
	ProjectID  string `json:"project_id,omitempty"`
	TodolistID string `json:"todolist_id,omitempty"`
	Scope      string `json:"scope,omitempty"`
	ClientID   string `json:"client_id,omitempty"`
}

// Source indicates where a config value came from.
type Source string

const (
	SourceDefault Source = "default"
	SourceSystem  Source = "system"
	SourceGlobal  Source = "global"
	SourceRepo    Source = "repo"
	SourceLocal   Source = "local"
	SourceEnv     Source = "env"
	SourceFlag    Source = "flag"
	SourcePrompt  Source = "prompt"
)

// FlagOverrides holds command-line flag values.
type FlagOverrides struct {
	Account  string
	Project  string
	Todolist string
	Profile  string
	CacheDir string
	Format   string
}

// Default returns the default configuration.
func Default() *Config {
	cacheDir := os.Getenv("XDG_CACHE_HOME")
	if cacheDir == "" {
		home, _ := os.UserHomeDir()
		cacheDir = filepath.Join(home, ".cache")
	}

	return &Config{
		BaseURL:      "https://3.basecampapi.com",
		Scope:        "read",
		CacheDir:     filepath.Join(cacheDir, "bcq"),
		CacheEnabled: true,
		Format:       "auto",
		Sources:      make(map[string]string),
	}
}

// Load loads configuration from all sources with proper precedence.
// Precedence: flags > env > local > repo > global > system > defaults
func Load(overrides FlagOverrides) (*Config, error) {
	cfg := Default()

	// Load from file layers (system -> global -> repo -> local)
	loadFromFile(cfg, systemConfigPath(), SourceSystem)
	loadFromFile(cfg, globalConfigPath(), SourceGlobal)

	repoPath := repoConfigPath()
	if repoPath != "" {
		loadFromFile(cfg, repoPath, SourceRepo)
	}

	// Load all local configs from root to current (closer overrides)
	// This allows nested directories to override parent directories
	localPaths := localConfigPaths(repoPath)
	for _, path := range localPaths {
		loadFromFile(cfg, path, SourceLocal)
	}

	// Load from environment
	LoadFromEnv(cfg)

	// Apply flag overrides
	ApplyOverrides(cfg, overrides)

	return cfg, nil
}

func loadFromFile(cfg *Config, path string, source Source) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: Path is from trusted config locations
	if err != nil {
		return // File doesn't exist, skip
	}

	var fileCfg map[string]any
	if err := json.Unmarshal(data, &fileCfg); err != nil {
		return // Invalid JSON, skip
	}

	if v, ok := fileCfg["base_url"].(string); ok && v != "" {
		cfg.BaseURL = v
		cfg.Sources["base_url"] = string(source)
	}
	if v := getStringOrNumber(fileCfg, "account_id"); v != "" {
		cfg.AccountID = v
		cfg.Sources["account_id"] = string(source)
	}
	if v := getStringOrNumber(fileCfg, "project_id"); v != "" {
		cfg.ProjectID = v
		cfg.Sources["project_id"] = string(source)
	}
	if v := getStringOrNumber(fileCfg, "todolist_id"); v != "" {
		cfg.TodolistID = v
		cfg.Sources["todolist_id"] = string(source)
	}
	if v, ok := fileCfg["scope"].(string); ok && v != "" {
		cfg.Scope = v
		cfg.Sources["scope"] = string(source)
	}
	if v, ok := fileCfg["cache_dir"].(string); ok && v != "" {
		cfg.CacheDir = v
		cfg.Sources["cache_dir"] = string(source)
	}
	if v, ok := fileCfg["cache_enabled"].(bool); ok {
		cfg.CacheEnabled = v
		cfg.Sources["cache_enabled"] = string(source)
	}
	if v, ok := fileCfg["format"].(string); ok && v != "" {
		cfg.Format = v
		cfg.Sources["format"] = string(source)
	}
	if v, ok := fileCfg["default_profile"].(string); ok && v != "" {
		cfg.DefaultProfile = v
		cfg.Sources["default_profile"] = string(source)
	}
	if v, ok := fileCfg["profiles"].(map[string]any); ok {
		if cfg.Profiles == nil {
			cfg.Profiles = make(map[string]*ProfileConfig)
		}
		for name, profileData := range v {
			if profileMap, ok := profileData.(map[string]any); ok {
				profileCfg := &ProfileConfig{}
				if baseURL, ok := profileMap["base_url"].(string); ok && baseURL != "" {
					profileCfg.BaseURL = baseURL
				} else {
					// Skip profiles with empty or missing base_url
					continue
				}
				if accountID := getStringOrNumber(profileMap, "account_id"); accountID != "" {
					profileCfg.AccountID = accountID
				}
				if projectID := getStringOrNumber(profileMap, "project_id"); projectID != "" {
					profileCfg.ProjectID = projectID
				}
				if todolistID := getStringOrNumber(profileMap, "todolist_id"); todolistID != "" {
					profileCfg.TodolistID = todolistID
				}
				if scope, ok := profileMap["scope"].(string); ok {
					profileCfg.Scope = scope
				}
				if clientID, ok := profileMap["client_id"].(string); ok {
					profileCfg.ClientID = clientID
				}
				cfg.Profiles[name] = profileCfg
			}
		}
		cfg.Sources["profiles"] = string(source)
	}
}

// LoadFromEnv loads configuration from environment variables.
// Exported so root.go can re-apply after profile overlay.
func LoadFromEnv(cfg *Config) {
	if v := os.Getenv("BASECAMP_BASE_URL"); v != "" {
		cfg.BaseURL = v
		cfg.Sources["base_url"] = string(SourceEnv)
	}
	if v := os.Getenv("BCQ_BASE_URL"); v != "" {
		cfg.BaseURL = v
		cfg.Sources["base_url"] = string(SourceEnv)
	}
	if v := os.Getenv("BASECAMP_ACCOUNT_ID"); v != "" {
		cfg.AccountID = v
		cfg.Sources["account_id"] = string(SourceEnv)
	}
	if v := os.Getenv("BCQ_ACCOUNT"); v != "" {
		cfg.AccountID = v
		cfg.Sources["account_id"] = string(SourceEnv)
	}
	if v := os.Getenv("BASECAMP_PROJECT_ID"); v != "" {
		cfg.ProjectID = v
		cfg.Sources["project_id"] = string(SourceEnv)
	}
	if v := os.Getenv("BCQ_PROJECT"); v != "" {
		cfg.ProjectID = v
		cfg.Sources["project_id"] = string(SourceEnv)
	}
	if v := os.Getenv("BASECAMP_TODOLIST_ID"); v != "" {
		cfg.TodolistID = v
		cfg.Sources["todolist_id"] = string(SourceEnv)
	}
	if v := os.Getenv("BASECAMP_CACHE_DIR"); v != "" {
		cfg.CacheDir = v
		cfg.Sources["cache_dir"] = string(SourceEnv)
	}
	if v := os.Getenv("BCQ_CACHE_DIR"); v != "" {
		cfg.CacheDir = v
		cfg.Sources["cache_dir"] = string(SourceEnv)
	}
	if v := os.Getenv("BCQ_CACHE_ENABLED"); v != "" {
		cfg.CacheEnabled = strings.ToLower(v) == "true" || v == "1"
		cfg.Sources["cache_enabled"] = string(SourceEnv)
	}
}

// getStringOrNumber extracts a value that may be either a string or number in JSON.
func getStringOrNumber(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		// JSON numbers are unmarshaled as float64
		return strings.TrimSuffix(strings.TrimSuffix(
			strings.TrimSuffix(fmt.Sprintf("%.0f", val), ".0"),
			".00"),
			".")
	case int:
		return fmt.Sprintf("%d", val)
	case int64:
		return fmt.Sprintf("%d", val)
	default:
		return ""
	}
}

// ApplyOverrides applies non-empty flag overrides to cfg.
// Exported so root.go can re-apply after profile overlay.
func ApplyOverrides(cfg *Config, o FlagOverrides) {
	if o.Account != "" {
		cfg.AccountID = o.Account
		cfg.Sources["account_id"] = string(SourceFlag)
	}
	if o.Project != "" {
		cfg.ProjectID = o.Project
		cfg.Sources["project_id"] = string(SourceFlag)
	}
	if o.Todolist != "" {
		cfg.TodolistID = o.Todolist
		cfg.Sources["todolist_id"] = string(SourceFlag)
	}
	if o.CacheDir != "" {
		cfg.CacheDir = o.CacheDir
		cfg.Sources["cache_dir"] = string(SourceFlag)
	}
	if o.Format != "" {
		cfg.Format = o.Format
		cfg.Sources["format"] = string(SourceFlag)
	}
}

// ApplyProfile overlays profile values onto the config.
//
// This is the first pass of a two-pass precedence system:
//
//	Pass 1 (this method): Profile values unconditionally overwrite config fields.
//	Pass 2 (caller):      LoadFromEnv + ApplyOverrides re-apply env vars and CLI
//	                       flags, which take final precedence over profile values.
//
// The caller in root.go MUST call LoadFromEnv and ApplyOverrides after this
// method to maintain the precedence chain: flags > env > profile > file > defaults.
func (cfg *Config) ApplyProfile(name string) error {
	if cfg.Profiles == nil {
		return fmt.Errorf("no profiles configured")
	}
	p, ok := cfg.Profiles[name]
	if !ok {
		return fmt.Errorf("profile %q not found", name)
	}

	cfg.ActiveProfile = name

	// Unconditionally set profile values. Env/flag overrides are re-applied
	// by the caller afterward to restore correct precedence.
	if p.BaseURL != "" {
		cfg.BaseURL = p.BaseURL
		cfg.Sources["base_url"] = "profile"
	}
	if p.AccountID != "" {
		cfg.AccountID = p.AccountID
		cfg.Sources["account_id"] = "profile"
	}
	if p.ProjectID != "" {
		cfg.ProjectID = p.ProjectID
		cfg.Sources["project_id"] = "profile"
	}
	if p.TodolistID != "" {
		cfg.TodolistID = p.TodolistID
		cfg.Sources["todolist_id"] = "profile"
	}
	if p.Scope != "" {
		cfg.Scope = p.Scope
		cfg.Sources["scope"] = "profile"
	}

	return nil
}

// Path helpers

func systemConfigPath() string {
	return "/etc/basecamp/config.json"
}

func globalConfigPath() string {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, _ := os.UserHomeDir()
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "basecamp", "config.json")
}

func repoConfigPath() string {
	// Walk up to find .git directory, then look for .basecamp/config.json
	dir, _ := os.Getwd()
	for {
		gitPath := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			cfgPath := filepath.Join(dir, ".basecamp", "config.json")
			if _, err := os.Stat(cfgPath); err == nil {
				return cfgPath
			}
			return ""
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// localConfigPaths returns all .basecamp/config.json paths from root to current directory,
// excluding the repo config path (already loaded as SourceRepo).
// Paths are returned in order from furthest ancestor to closest, so closer configs override.
func localConfigPaths(repoConfigPath string) []string {
	dir, _ := os.Getwd()
	var paths []string

	// Collect all paths walking up
	for {
		cfgPath := filepath.Join(dir, ".basecamp", "config.json")
		if _, err := os.Stat(cfgPath); err == nil {
			// Skip if this is the repo config (already loaded)
			if cfgPath != repoConfigPath {
				paths = append(paths, cfgPath)
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// Reverse so paths go from root to current (closer overrides)
	for i, j := 0, len(paths)-1; i < j; i, j = i+1, j-1 {
		paths[i], paths[j] = paths[j], paths[i]
	}

	return paths
}

// GlobalConfigDir returns the global config directory path.
func GlobalConfigDir() string {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, _ := os.UserHomeDir()
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "basecamp")
}

// NormalizeBaseURL ensures consistent URL format (no trailing slash).
func NormalizeBaseURL(url string) string {
	return strings.TrimSuffix(url, "/")
}
