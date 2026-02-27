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

	// Behavior preferences (persisted via config set, overridable by flags)
	Hints   *bool `json:"hints,omitempty"`
	Stats   *bool `json:"stats,omitempty"`
	Verbose *int  `json:"verbose,omitempty"`

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
		CacheDir:     filepath.Join(cacheDir, "basecamp"),
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
		fmt.Fprintf(os.Stderr, "warning: skipping malformed config at %s: %v\n", path, err)
		return
	}

	// Authority keys (base_url, profiles, default_profile) control where tokens
	// are sent. Local/repo config must NOT set these — a malicious config in a
	// cloned repo or parent directory could redirect authenticated traffic.
	untrusted := source == SourceLocal || source == SourceRepo

	if v, ok := fileCfg["base_url"].(string); ok && v != "" {
		if untrusted {
			fmt.Fprintf(os.Stderr, "warning: ignoring base_url %q from %s config at %s (authority keys are not trusted from local/repo config)\n", v, source, path)
		} else {
			cfg.BaseURL = v
			cfg.Sources["base_url"] = string(source)
		}
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
	if v, ok := fileCfg["hints"].(bool); ok {
		cfg.Hints = &v
		cfg.Sources["hints"] = string(source)
	}
	if v, ok := fileCfg["stats"].(bool); ok {
		cfg.Stats = &v
		cfg.Sources["stats"] = string(source)
	}
	if v, ok := fileCfg["verbose"]; ok {
		if fv, ok := v.(float64); ok {
			iv := int(fv)
			if iv >= 0 && iv <= 2 && fv == float64(iv) {
				cfg.Verbose = &iv
				cfg.Sources["verbose"] = string(source)
			}
		}
	}
	if v, ok := fileCfg["default_profile"].(string); ok && v != "" {
		if untrusted {
			fmt.Fprintf(os.Stderr, "warning: ignoring default_profile %q from %s config at %s (authority keys are not trusted from local/repo config)\n", v, source, path)
		} else {
			cfg.DefaultProfile = v
			cfg.Sources["default_profile"] = string(source)
		}
	}
	if v, ok := fileCfg["profiles"].(map[string]any); ok {
		if untrusted {
			fmt.Fprintf(os.Stderr, "warning: ignoring profiles from %s config at %s (authority keys are not trusted from local/repo config)\n", source, path)
		} else {
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
}

// LoadFromEnv loads configuration from environment variables.
// Exported so root.go can re-apply after profile overlay.
func LoadFromEnv(cfg *Config) {
	if v := os.Getenv("BASECAMP_BASE_URL"); v != "" {
		cfg.BaseURL = v
		cfg.Sources["base_url"] = string(SourceEnv)
	}
	if v := os.Getenv("BASECAMP_ACCOUNT_ID"); v != "" {
		cfg.AccountID = v
		cfg.Sources["account_id"] = string(SourceEnv)
	}
	if v := os.Getenv("BASECAMP_PROJECT_ID"); v != "" {
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
	if v := os.Getenv("BASECAMP_CACHE_ENABLED"); v != "" {
		cfg.CacheEnabled = strings.ToLower(v) == "true" || v == "1"
		cfg.Sources["cache_enabled"] = string(SourceEnv)
	}
	if v := os.Getenv("BASECAMP_HINTS"); v != "" {
		if b, ok := parseEnvBool(v); ok {
			cfg.Hints = &b
			cfg.Sources["hints"] = string(SourceEnv)
		}
	}
	if v := os.Getenv("BASECAMP_STATS"); v != "" {
		if b, ok := parseEnvBool(v); ok {
			cfg.Stats = &b
			cfg.Sources["stats"] = string(SourceEnv)
		}
	}
}

// parseEnvBool parses a boolean environment variable strictly.
// Returns (value, true) for recognized values, (false, false) for unrecognized.
// Unrecognized values are ignored to preserve three-state pointer semantics.
func parseEnvBool(v string) (bool, bool) {
	switch strings.ToLower(v) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	default:
		return false, false
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
	// Walk up to find .git directory, then look for .basecamp/config.json.
	// Bounded by $HOME: only search within the home directory tree.
	// If CWD is outside $HOME (e.g., /tmp), no repo config is trusted.
	dir, err := os.Getwd()
	if err != nil {
		return "" // fail closed: can't determine CWD
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "" // fail closed: can't resolve symlinks for trust boundary
	}
	dir = resolved
	home, _ := os.UserHomeDir()
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}

	// If CWD is not inside $HOME, don't trust any repo config.
	// This prevents a malicious .git in /tmp/ from anchoring the repo root.
	if home != "" && !isInsideDir(dir, home) {
		return ""
	}

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
		// Don't walk above home directory
		if home != "" && dir == home {
			return ""
		}
		dir = parent
	}
}

// isInsideDir reports whether child is the same as or a subdirectory of parent.
// Both paths must be absolute and already cleaned/resolved.
func isInsideDir(child, parent string) bool {
	if child == parent {
		return true
	}
	// Ensure parent has a trailing separator for prefix matching
	prefix := parent
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(child, prefix)
}

// localConfigPaths returns .basecamp/config.json paths within the trust boundary,
// excluding the repo config path (already loaded as SourceRepo).
// Paths are returned in order from furthest ancestor to closest, so closer configs override.
//
// Trust boundary:
//   - Inside a git repo: only paths at or below the repo root
//   - Outside a git repo: only the current working directory (no parent traversal)
func localConfigPaths(repoConfigPath string) []string {
	dir, err := os.Getwd()
	if err != nil {
		return nil // fail closed: can't determine CWD
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil // fail closed: can't resolve symlinks for trust boundary
	}
	dir = resolved
	var paths []string

	// Determine trust boundary (resolve symlinks for reliable comparison
	// since os.Getwd returns the resolved path on platforms like macOS)
	var boundary string
	if repoConfigPath != "" {
		// Inside a repo: trust boundary is the repo root
		boundary = filepath.Dir(filepath.Dir(repoConfigPath)) // .basecamp/config.json -> repo root
	} else {
		// No repo: only trust current directory
		boundary = dir
	}
	if resolved, err := filepath.EvalSymlinks(boundary); err == nil {
		boundary = resolved
	}

	// Collect paths walking up, stopping at the trust boundary
	for {
		cfgPath := filepath.Join(dir, ".basecamp", "config.json")
		if _, err := os.Stat(cfgPath); err == nil {
			// Skip if this is the repo config (already loaded)
			if cfgPath != repoConfigPath {
				paths = append(paths, cfgPath)
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir || dir == boundary {
			break
		}
		dir = parent
	}

	// Reverse so paths go from boundary to current (closer overrides)
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
