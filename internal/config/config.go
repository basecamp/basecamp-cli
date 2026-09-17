// Package config provides layered configuration loading.
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
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
	Hints     *bool `json:"hints,omitempty"`
	Stats     *bool `json:"stats,omitempty"`
	Verbose   *int  `json:"verbose,omitempty"`
	Onboarded *bool `json:"onboarded,omitempty"`

	// LLM settings (for TUI smart zoom summarization)
	LLMProvider      string `json:"llm_provider,omitempty"`
	LLMModel         string `json:"llm_model,omitempty"`
	LLMAPIKey        string `json:"llm_api_key,omitempty"`
	LLMEndpoint      string `json:"llm_endpoint,omitempty"`
	LLMMaxConcurrent int    `json:"llm_max_concurrent,omitempty"`
	LLMTokenBudget   int    `json:"llm_token_budget,omitempty"`

	// Experimental feature flags (opt-in via "config set experimental.X true --global").
	Experimental map[string]bool `json:"experimental,omitempty"`

	// Sources tracks where each value came from (for debugging).
	Sources map[string]string `json:"-"`

	// ProfileOrigins records, per profile name, the config files its entry
	// in Profiles was made from. Set by the file layers only: a profile an
	// invocation adds to Profiles in memory has none.
	ProfileOrigins map[string]*ProfileOrigin `json:"-"`
}

// ProfileLayer is one config file's entry for a profile.
type ProfileLayer struct {
	Source  Source
	Path    string
	BaseURL string
}

// ProfileOrigin is where a profile's effective entry came from. See
// mergeProfile for how entries from different files combine.
type ProfileOrigin struct {
	// Layers are the files whose entries make up the profile, farthest
	// first: the first defines it, and each later one refines it.
	Layers []ProfileLayer

	// Fields maps each field set on the profile, by its JSON key, to the
	// path of the file that set it.
	Fields map[string]string

	// Replaced are the entries a closer one replaced whole, farthest
	// first, each with the entry that replaced it. Nothing of them
	// applies.
	Replaced []ReplacedProfileLayer
}

// ReplacedProfileLayer is a file's entry for a profile that a closer entry
// replaced whole, and the entry that replaced it.
type ReplacedProfileLayer struct {
	ProfileLayer

	// By is the entry that replaced it: for another Basecamp, or for
	// another account on the same one.
	By ProfileLayer
}

// Includes reports whether a file of the given source contributes to the
// profile.
func (o *ProfileOrigin) Includes(source Source) bool {
	for _, l := range o.Layers {
		if l.Source == source {
			return true
		}
	}
	return false
}

// ReplacedLayer returns the replaced entry from a file of the given source,
// nil when no such entry was replaced.
func (o *ProfileOrigin) ReplacedLayer(source Source) *ReplacedProfileLayer {
	for i := len(o.Replaced) - 1; i >= 0; i-- {
		if o.Replaced[i].Source == source {
			return &o.Replaced[i]
		}
	}
	return nil
}

// Closest is the closest file contributing to the profile: the one whose
// fields win.
func (o *ProfileOrigin) Closest() ProfileLayer {
	if len(o.Layers) == 0 {
		return ProfileLayer{}
	}
	return o.Layers[len(o.Layers)-1]
}

// IsExperimental returns true if the named experimental feature is enabled.
func (c *Config) IsExperimental(name string) bool {
	if c.Experimental == nil {
		return false
	}
	return c.Experimental[name]
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
		if home, _ := os.UserHomeDir(); home != "" {
			cacheDir = filepath.Join(filepath.Clean(home), ".cache")
		} else {
			cacheDir = os.TempDir()
		}
	} else {
		cacheDir = filepath.Clean(cacheDir)
	}

	return &Config{
		BaseURL:          "https://3.basecampapi.com",
		Scope:            "",
		CacheDir:         filepath.Join(cacheDir, "basecamp"),
		CacheEnabled:     true,
		Format:           "auto",
		LLMProvider:      "auto",
		LLMMaxConcurrent: 3,
		LLMTokenBudget:   2000,
		Sources:          make(map[string]string),
	}
}

// Load loads configuration from all sources with proper precedence.
// Precedence: flags > env > local > repo > global > system > defaults
func Load(overrides FlagOverrides) (*Config, error) {
	cfg := Default()
	trust := LoadTrustStore(GlobalConfigDir())

	// Load from file layers (system -> global -> repo -> local)
	loadFromFile(cfg, systemConfigPath(), SourceSystem, trust)
	loadFromFile(cfg, globalConfigPath(), SourceGlobal, trust)

	repoPath := RepoConfigPath()
	if repoPath != "" {
		loadFromFile(cfg, repoPath, SourceRepo, trust)
	}

	// Load all local configs from root to current (closer overrides)
	// This allows nested directories to override parent directories
	localPaths := localConfigPaths(repoPath)
	for _, path := range localPaths {
		loadFromFile(cfg, path, SourceLocal, trust)
	}

	// Load from environment
	if err := LoadFromEnv(cfg); err != nil {
		return nil, err
	}

	// Apply flag overrides
	ApplyOverrides(cfg, overrides)

	return cfg, nil
}

func loadFromFile(cfg *Config, path string, source Source, trust *TrustStore) {
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
	// are sent. Local/repo config must NOT set these unless explicitly trusted
	// via `basecamp config trust` — a malicious config in a cloned repo or
	// parent directory could redirect authenticated traffic.
	untrusted := (source == SourceLocal || source == SourceRepo) &&
		(trust == nil || !trust.IsTrusted(path))

	if v, ok := fileCfg["base_url"].(string); ok && v != "" {
		if untrusted {
			fmt.Fprintf(os.Stderr, "warning: ignoring base_url %q from %s config at %s\n  (authority key from local/repo config; run `basecamp config trust %s` to allow)\n", v, source, path, ShellQuote(path))
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
		// cache_dir redirects every cache write (completion, resilience, TUI
		// workspace, recents, traces). An untrusted local/repo config could
		// point it at any user-writable path, so gate it like other authority
		// keys. filepath.Clean normalizes the accepted value.
		if untrusted {
			fmt.Fprintf(os.Stderr, "warning: ignoring cache_dir %q from %s config at %s\n  (trust-gated key from local/repo config; run `basecamp config trust %s` to allow)\n", v, source, path, ShellQuote(path))
		} else {
			cfg.CacheDir = filepath.Clean(v)
			cfg.Sources["cache_dir"] = string(source)
		}
	}
	if v, ok := fileCfg["cache_enabled"].(bool); ok {
		if untrusted {
			fmt.Fprintf(os.Stderr, "warning: ignoring cache_enabled from %s config at %s\n  (trust-gated key from local/repo config; run `basecamp config trust %s` to allow)\n", source, path, ShellQuote(path))
		} else {
			cfg.CacheEnabled = v
			cfg.Sources["cache_enabled"] = string(source)
		}
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
	if v, ok := fileCfg["onboarded"].(bool); ok {
		cfg.Onboarded = &v
		cfg.Sources["onboarded"] = string(source)
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
	if v, ok := fileCfg["llm_provider"].(string); ok && v != "" {
		if untrusted {
			fmt.Fprintf(os.Stderr, "warning: ignoring llm_provider %q from %s config at %s\n  (authority key from local/repo config; run `basecamp config trust %s` to allow)\n", v, source, path, ShellQuote(path))
		} else {
			cfg.LLMProvider = v
			cfg.Sources["llm_provider"] = string(source)
		}
	}
	if v, ok := fileCfg["llm_model"].(string); ok && v != "" {
		// Gate like other LLM authority keys: an untrusted config could
		// silently substitute a costlier paid model.
		if untrusted {
			fmt.Fprintf(os.Stderr, "warning: ignoring llm_model %q from %s config at %s\n  (trust-gated key from local/repo config; run `basecamp config trust %s` to allow)\n", v, source, path, ShellQuote(path))
		} else {
			cfg.LLMModel = v
			cfg.Sources["llm_model"] = string(source)
		}
	}
	if v, ok := fileCfg["llm_api_key"].(string); ok && v != "" {
		// Secret: only from global/system config, never local/repo
		if source != SourceLocal && source != SourceRepo {
			cfg.LLMAPIKey = v
			cfg.Sources["llm_api_key"] = string(source)
		} else {
			fmt.Fprintf(os.Stderr, "warning: ignoring llm_api_key from %s config at %s (use --global or BASECAMP_LLM_API_KEY env var)\n", source, path)
		}
	}
	if v, ok := fileCfg["llm_endpoint"].(string); ok && v != "" {
		if untrusted {
			fmt.Fprintf(os.Stderr, "warning: ignoring llm_endpoint %q from %s config at %s\n  (authority key from local/repo config; run `basecamp config trust %s` to allow)\n", v, source, path, ShellQuote(path))
		} else {
			// Keep the value even if malformed (non-http(s)/hostless):
			// summarize.ValidateEndpoint rejects it at the point of
			// consumption (the TUI's LLM path) when the provider sends
			// llm_api_key to the endpoint, failing closed rather than
			// silently dropping it and falling back to the default provider.
			// Other commands never consume it, so `config unset llm_endpoint`
			// can always repair.
			cfg.LLMEndpoint = v
			cfg.Sources["llm_endpoint"] = string(source)
		}
	}
	if v, ok := fileCfg["llm_max_concurrent"]; ok {
		if fv, ok := v.(float64); ok {
			iv := int(fv)
			// Gate like other LLM authority keys: block a malicious repo from
			// inflating paid-LLM concurrency (cost amplification).
			if untrusted {
				fmt.Fprintf(os.Stderr, "warning: ignoring llm_max_concurrent from %s config at %s\n  (trust-gated key from local/repo config; run `basecamp config trust %s` to allow)\n", source, path, ShellQuote(path))
			} else if iv >= 1 && iv <= 10 && fv == float64(iv) {
				cfg.LLMMaxConcurrent = iv
				cfg.Sources["llm_max_concurrent"] = string(source)
			}
		}
	}
	if v, ok := fileCfg["llm_token_budget"]; ok {
		if fv, ok := v.(float64); ok {
			iv := int(fv)
			// Gate like other LLM authority keys (cost amplification).
			if untrusted {
				fmt.Fprintf(os.Stderr, "warning: ignoring llm_token_budget from %s config at %s\n  (trust-gated key from local/repo config; run `basecamp config trust %s` to allow)\n", source, path, ShellQuote(path))
			} else if iv >= 100 && iv <= 100000 && fv == float64(iv) {
				cfg.LLMTokenBudget = iv
				cfg.Sources["llm_token_budget"] = string(source)
			}
		}
	}
	if v, ok := fileCfg["experimental"].(map[string]any); ok {
		if cfg.Experimental == nil {
			cfg.Experimental = make(map[string]bool)
		}
		for feature, val := range v {
			if enabled, ok := val.(bool); ok {
				cfg.Experimental[feature] = enabled
				cfg.Sources["experimental."+feature] = string(source)
			}
		}
	}
	if v, ok := fileCfg["default_profile"].(string); ok && v != "" {
		if untrusted {
			fmt.Fprintf(os.Stderr, "warning: ignoring default_profile %q from %s config at %s\n  (authority key from local/repo config; run `basecamp config trust %s` to allow)\n", v, source, path, ShellQuote(path))
		} else {
			cfg.DefaultProfile = v
			cfg.Sources["default_profile"] = string(source)
		}
	}
	if v, ok := fileCfg["profiles"].(map[string]any); ok {
		if untrusted {
			fmt.Fprintf(os.Stderr, "warning: ignoring profiles from %s config at %s\n  (authority key from local/repo config; run `basecamp config trust %s` to allow)\n", source, path, ShellQuote(path))
		} else {
			for name, profileData := range v {
				if profileMap, ok := profileData.(map[string]any); ok {
					mergeProfile(cfg, name, profileMap, ProfileLayer{Source: source, Path: path})
				}
			}
			// An aggregate: the last file that contributed any profile.
			// Which file each profile and field came from is recorded in
			// ProfileOrigins, and nothing is decided from this.
			cfg.Sources["profiles"] = string(source)
		}
	}
}

// mergeProfile layers one config file's entry for a profile over the entry
// the farther files made of it. This is the one place profile entries from
// different files meet, and the rule is:
//
//   - An entry with no base_url is skipped, as if the file did not name the
//     profile.
//   - An entry for the same Basecamp as the one it layers over — the same
//     base_url, normalized — refines it field by field. A field the closer
//     entry sets wins; a field it leaves unset keeps the farther file's
//     value. So a trusted repo or local config that names a profile's
//     project does not hide the account the global config binds it to,
//     just as an unset top-level key never hides a farther file's.
//   - An entry for a different Basecamp, or for a different account on the
//     same one (both name an account_id, and they differ), replaces the
//     farther one whole. A project, todolist or client from one account
//     means nothing in another, so nothing is carried across; the replaced
//     files are kept in the origin, since an account bound there no longer
//     applies.
//
// Unset means absent: for the IDs also empty (as for the top-level IDs),
// while a present scope or client_id sets the field even when empty.
//
// One field is dropped rather than kept: a todolist belongs to a project, so
// an entry that names another project and no todolist inherits none.
//
// Every field set records the file that set it, in cfg.ProfileOrigins.
func mergeProfile(cfg *Config, name string, entry map[string]any, layer ProfileLayer) {
	baseURL, _ := entry["base_url"].(string)
	if baseURL == "" {
		return
	}
	layer.BaseURL = baseURL
	if cfg.Profiles == nil {
		cfg.Profiles = make(map[string]*ProfileConfig)
	}
	if cfg.ProfileOrigins == nil {
		cfg.ProfileOrigins = make(map[string]*ProfileOrigin)
	}

	p, origin := cfg.Profiles[name], cfg.ProfileOrigins[name]
	account := getStringOrNumber(entry, "account_id")
	otherAccount := p != nil && p.AccountID != "" && account != "" && !sameID(p.AccountID, account)
	if p == nil || origin == nil || otherAccount || NormalizeBaseURL(p.BaseURL) != NormalizeBaseURL(baseURL) {
		replaced := []ReplacedProfileLayer(nil)
		if p != nil && origin != nil {
			replaced = append(replaced, origin.Replaced...)
			for _, l := range origin.Layers {
				replaced = append(replaced, ReplacedProfileLayer{ProfileLayer: l, By: layer})
			}
		}
		p = &ProfileConfig{}
		origin = &ProfileOrigin{Replaced: replaced, Fields: make(map[string]string)}
		cfg.Profiles[name] = p
		cfg.ProfileOrigins[name] = origin
	}
	origin.Layers = append(origin.Layers, layer)

	set := func(field string, dst *string, value string) {
		*dst = value
		origin.Fields[field] = layer.Path
	}
	set("base_url", &p.BaseURL, baseURL)
	if account != "" {
		set("account_id", &p.AccountID, account)
	}
	if v := getStringOrNumber(entry, "project_id"); v != "" {
		if !sameID(p.ProjectID, v) && p.TodolistID != "" {
			// The inherited todolist is in the project being replaced.
			p.TodolistID = ""
			delete(origin.Fields, "todolist_id")
		}
		set("project_id", &p.ProjectID, v)
	}
	if v := getStringOrNumber(entry, "todolist_id"); v != "" {
		set("todolist_id", &p.TodolistID, v)
	}
	if v, ok := entry["scope"].(string); ok {
		set("scope", &p.Scope, v)
	}
	if v, ok := entry["client_id"].(string); ok {
		set("client_id", &p.ClientID, v)
	}
}

// sameID reports whether two IDs name the same record: equal as decimal
// numbers, so "0999" is "999", however many digits they run to — the
// comparison the commands and the name resolver make. A value that is not
// all digits is the same only as its exact spelling.
func sameID(a, b string) bool {
	if a == b {
		return true
	}
	canonical := func(id string) (string, bool) {
		if id == "" || strings.TrimLeft(id, "0123456789") != "" {
			return "", false
		}
		return strings.TrimLeft(id, "0"), true
	}
	ca, okA := canonical(a)
	cb, okB := canonical(b)
	return okA && okB && ca == cb
}

// LoadFromEnv loads configuration from environment variables.
// Exported so root.go can re-apply after profile overlay.
//
// Values are stored as-is, malformed or not — mirroring the trusted-file
// path in loadFromFile. Validation happens at use-time in
// summarize.ValidateEndpoint (the TUI's LLM path, the only consumer of
// llm_endpoint), which fails closed for providers that send llm_api_key
// to the endpoint. Other commands never consume the value, so
// `basecamp config unset llm_endpoint` (or unsetting the env var) can
// always repair a bad value. Erroring here would block every command —
// including the repair path — for all providers, even ones that never
// consume the endpoint.
//
// Always returns nil today; the error return is kept so validation can be
// reintroduced for a specific variable without churning every caller.
func LoadFromEnv(cfg *Config) error {
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
	if v := os.Getenv("BASECAMP_LLM_PROVIDER"); v != "" {
		cfg.LLMProvider = v
		cfg.Sources["llm_provider"] = string(SourceEnv)
	}
	if v := os.Getenv("BASECAMP_LLM_MODEL"); v != "" {
		cfg.LLMModel = v
		cfg.Sources["llm_model"] = string(SourceEnv)
	}
	if v := os.Getenv("BASECAMP_LLM_API_KEY"); v != "" {
		cfg.LLMAPIKey = v
		cfg.Sources["llm_api_key"] = string(SourceEnv)
	}
	if v := os.Getenv("BASECAMP_LLM_ENDPOINT"); v != "" {
		// Keep the value even if malformed (non-http(s)/hostless), mirroring
		// the trusted-file path in loadFromFile: summarize.ValidateEndpoint
		// rejects it at the point of consumption (the TUI's LLM path) for
		// providers that send llm_api_key to the endpoint, failing closed
		// rather than silently dropping it and falling back to the default
		// provider. Other commands never consume it, so
		// `config unset llm_endpoint` can always repair.
		cfg.LLMEndpoint = v
		cfg.Sources["llm_endpoint"] = string(SourceEnv)
	}
	return nil
}

// NonInteractiveEnv reports whether BASECAMP_NONINTERACTIVE is set to a truthy
// value. When true, the CLI must not show interactive prompts regardless of TTY
// detection. This is an escape hatch for agents and harnesses that run the CLI
// under an allocated PTY (where stdout looks like a terminal) and want to avoid
// a selection prompt wedging the session — without forcing a machine output
// format the way --agent does.
func NonInteractiveEnv() bool {
	if v := os.Getenv("BASECAMP_NONINTERACTIVE"); v != "" {
		if b, ok := parseEnvBool(v); ok {
			return b
		}
	}
	return false
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
func (c *Config) ApplyProfile(name string) error {
	if c.Profiles == nil {
		return fmt.Errorf("no profiles configured")
	}
	p, ok := c.Profiles[name]
	if !ok {
		return fmt.Errorf("profile %q not found", name)
	}

	c.ActiveProfile = name

	// Unconditionally set profile values. Env/flag overrides are re-applied
	// by the caller afterward to restore correct precedence.
	if p.BaseURL != "" {
		c.BaseURL = p.BaseURL
		c.Sources["base_url"] = "profile"
	}
	if p.AccountID != "" {
		c.AccountID = p.AccountID
		c.Sources["account_id"] = "profile"
	}
	if p.ProjectID != "" {
		c.ProjectID = p.ProjectID
		c.Sources["project_id"] = "profile"
	}
	if p.TodolistID != "" {
		c.TodolistID = p.TodolistID
		c.Sources["todolist_id"] = "profile"
	}
	if p.Scope != "" {
		c.Scope = p.Scope
		c.Sources["scope"] = "profile"
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
		if home, _ := os.UserHomeDir(); home != "" {
			configDir = filepath.Join(filepath.Clean(home), ".config")
		} else {
			configDir = os.TempDir()
		}
	} else {
		configDir = filepath.Clean(configDir)
	}
	return filepath.Join(configDir, "basecamp", "config.json")
}

// RepoConfigPath walks up from CWD to find .basecamp/config.json at the
// git repo root. Returns empty string if not found or outside $HOME.
func RepoConfigPath() string {
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
		if home, _ := os.UserHomeDir(); home != "" {
			configDir = filepath.Join(filepath.Clean(home), ".config")
		} else {
			configDir = os.TempDir()
		}
	} else {
		configDir = filepath.Clean(configDir)
	}
	return filepath.Join(configDir, "basecamp")
}

// NormalizeBaseURL ensures consistent URL format (no trailing slash).
func NormalizeBaseURL(url string) string {
	return strings.TrimSuffix(url, "/")
}

// IsHTTPURL reports whether rawURL is an absolute http(s) URL with a host.
// Used to reject non-web schemes (file://, etc.) and hostless forms
// (e.g. "https:example.com", which url.Parse accepts with an empty Host)
// for llm_endpoint, since those would later be concatenated into a request
// URL and produce confusing/invalid requests.
func IsHTTPURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
}

// ShellQuote returns a POSIX single-quoted string safe for copy-paste into
// a shell. Single quotes inside the value are escaped as '\” (end quote,
// escaped literal quote, resume quote).
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
