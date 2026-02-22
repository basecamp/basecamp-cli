package commands

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

const (
	legacyServiceName = "bcq"
	migratedMarker    = ".migrated"
)

// MigrateResult holds the migration outcome.
type MigrateResult struct {
	KeyringMigrated int      `json:"keyring_migrated"`
	KeyringErrors   []string `json:"keyring_errors,omitempty"`
	CacheMoved      bool     `json:"cache_moved"`
	CacheMessage    string   `json:"cache_message,omitempty"`
	ThemeMoved      bool     `json:"theme_moved"`
	ThemeMessage    string   `json:"theme_message,omitempty"`
	AlreadyMigrated bool     `json:"already_migrated,omitempty"`
}

// NewMigrateCmd creates the migrate command.
func NewMigrateCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate data from legacy bcq installation",
		Long: `Migrate keyring entries, cache, and theme from a previous bcq installation.

This is a one-time command for users upgrading from bcq to basecamp.
It migrates:
  - Keyring credentials (bcq::* → basecamp::*)
  - Cache directory (~/.cache/bcq → ~/.cache/basecamp)
  - Theme directory (~/.config/bcq/theme → ~/.config/basecamp/theme)

Safe to run multiple times — skips already-migrated data.`,
		Example: "  basecamp migrate",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrate(cmd, force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Re-run migration even if already completed")

	return cmd
}

func runMigrate(cmd *cobra.Command, force bool) error {
	configDir := config.GlobalConfigDir()
	markerPath := filepath.Join(configDir, migratedMarker)

	// Check if already migrated
	if !force {
		if _, err := os.Stat(markerPath); err == nil {
			return output.ErrUsage("Migration already completed. Use --force to re-run.")
		}
	}

	result := &MigrateResult{}

	// 1. Migrate keyring entries
	migrateKeyring(result, configDir)

	// 2. Migrate cache directory
	migrateCache(result)

	// 3. Migrate theme directory
	migrateTheme(result)

	// Build summary
	parts := []string{}
	if result.KeyringMigrated > 0 {
		parts = append(parts, fmt.Sprintf("%d keyring entries migrated", result.KeyringMigrated))
	}
	if len(result.KeyringErrors) > 0 {
		parts = append(parts, fmt.Sprintf("%d keyring errors", len(result.KeyringErrors)))
	}
	if result.CacheMoved {
		parts = append(parts, "cache migrated")
	}
	if result.ThemeMoved {
		parts = append(parts, "theme migrated")
	}

	// Only write marker when something actually migrated and no errors occurred
	migrated := result.KeyringMigrated > 0 || result.CacheMoved || result.ThemeMoved
	hasErrors := len(result.KeyringErrors) > 0
	if migrated && !hasErrors {
		if err := os.MkdirAll(configDir, 0700); err == nil {
			_ = os.WriteFile(markerPath, []byte("migrated\n"), 0600)
		}
	}

	if len(parts) == 0 {
		parts = append(parts, "nothing to migrate")
	}

	summary := strings.Join(parts, ", ")

	breadcrumbs := []output.Breadcrumb{
		{Action: "doctor", Cmd: "basecamp doctor", Description: "Verify installation health"},
	}

	// Use appctx if available, otherwise output directly
	if app := getApp(cmd); app != nil {
		return app.OK(result,
			output.WithSummary(summary),
			output.WithBreadcrumbs(breadcrumbs...),
		)
	}

	// Fallback: print JSON directly
	data, _ := json.MarshalIndent(result, "", "  ")
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return nil
}

// keyringOps holds the keyring operations used by migrateKeyring.
// Replaceable in tests to avoid real keyring access.
var keyringOps = keyringFuncs{
	get:    keyring.Get,
	set:    keyring.Set,
	delete: keyring.Delete,
}

type keyringFuncs struct {
	get    func(service, key string) (string, error)
	set    func(service, key, data string) error
	delete func(service, key string) error
}

// migrateKeyring migrates credentials from legacy "bcq" service to "basecamp" service.
func migrateKeyring(result *MigrateResult, configDir string) {
	origins := collectKnownOrigins(configDir)

	for _, origin := range origins {
		legacyKey := fmt.Sprintf("bcq::%s", origin)
		newKey := fmt.Sprintf("basecamp::%s", origin)

		// Read from legacy service
		data, err := keyringOps.get(legacyServiceName, legacyKey)
		if err != nil {
			// No legacy entry for this origin — skip silently
			continue
		}

		// Check if new entry already exists
		if _, err := keyringOps.get("basecamp", newKey); err == nil {
			// Already migrated — just clean up the legacy key
			_ = keyringOps.delete(legacyServiceName, legacyKey)
			result.KeyringMigrated++
			continue
		}

		// Write to new service
		if err := keyringOps.set("basecamp", newKey, data); err != nil {
			result.KeyringErrors = append(result.KeyringErrors,
				fmt.Sprintf("failed to write %s: %v", origin, err))
			continue
		}

		// Delete old entry (best-effort)
		_ = keyringOps.delete(legacyServiceName, legacyKey)

		result.KeyringMigrated++
	}
}

// collectKnownOrigins gathers credential origins from config files and credentials.json.
// Scans both the current config dir and the legacy bcq config dir so that
// origins that exist only under the old layout are still discovered.
func collectKnownOrigins(configDir string) []string {
	seen := make(map[string]bool)

	// Determine legacy config dir (sibling "bcq" directory)
	legacyDir := filepath.Join(filepath.Dir(configDir), "bcq")

	// Scan both directories — current first, then legacy
	for _, dir := range []string{configDir, legacyDir} {
		scanConfigDir(dir, seen)
	}

	// Standard production URL — always probe
	seen["https://3.basecampapi.com"] = true

	origins := make([]string, 0, len(seen))
	for o := range seen {
		origins = append(origins, o)
	}
	return origins
}

// scanConfigDir reads credentials.json and config.json from a directory,
// adding discovered origins to the seen map.
func scanConfigDir(dir string, seen map[string]bool) {
	// From credentials.json (file-based storage)
	credsPath := filepath.Join(dir, "credentials.json")
	if data, err := os.ReadFile(credsPath); err == nil {
		var creds map[string]any
		if json.Unmarshal(data, &creds) == nil {
			for origin := range creds {
				seen[origin] = true
			}
		}
	}

	// From config.json profiles (each profile's base_url is an origin)
	cfgPath := filepath.Join(dir, "config.json")
	if data, err := os.ReadFile(cfgPath); err == nil {
		var cfg struct {
			BaseURL  string                       `json:"base_url"`
			Profiles map[string]map[string]string `json:"profiles"`
		}
		if json.Unmarshal(data, &cfg) == nil {
			if cfg.BaseURL != "" {
				seen[config.NormalizeBaseURL(cfg.BaseURL)] = true
			}
			for name, p := range cfg.Profiles {
				if baseURL, ok := p["base_url"]; ok && baseURL != "" {
					seen[config.NormalizeBaseURL(baseURL)] = true
				}
				// Profile-based credential keys
				seen["profile:"+name] = true
			}
		}
	}
}

// migrateCache migrates ~/.cache/bcq to ~/.cache/basecamp.
func migrateCache(result *MigrateResult) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	cacheBase := os.Getenv("XDG_CACHE_HOME")
	if cacheBase == "" {
		cacheBase = filepath.Join(home, ".cache")
	}

	oldDir := filepath.Join(cacheBase, "bcq")
	newDir := filepath.Join(cacheBase, "basecamp")

	oldInfo, oldErr := os.Stat(oldDir)
	if oldErr != nil || !oldInfo.IsDir() {
		result.CacheMessage = "no legacy cache directory found"
		return
	}

	_, newErr := os.Stat(newDir)

	if newErr != nil {
		// New dir doesn't exist — simple rename
		if err := os.Rename(oldDir, newDir); err != nil {
			result.CacheMessage = fmt.Sprintf("rename failed: %v", err)
			return
		}
		result.CacheMoved = true
		result.CacheMessage = fmt.Sprintf("moved %s → %s", oldDir, newDir)
		return
	}

	// Both exist — merge non-critical files (old wins on conflict)
	mergeFiles := []string{
		"completion.json",
		filepath.Join("resilience", "state.json"),
	}

	merged := 0
	for _, rel := range mergeFiles {
		src := filepath.Join(oldDir, rel)
		dst := filepath.Join(newDir, rel)

		if _, err := os.Stat(src); err != nil {
			continue // Source doesn't exist
		}

		// Overwrite destination — old wins (preserves accumulated state)
		// Ensure destination directory exists
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			continue
		}

		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}

		if err := os.WriteFile(dst, data, 0600); err != nil {
			continue
		}

		merged++
	}

	// Remove old dir after merge
	_ = os.RemoveAll(oldDir)

	result.CacheMoved = true
	result.CacheMessage = fmt.Sprintf("merged %d files from %s into %s", merged, oldDir, newDir)
}

// migrateTheme migrates ~/.config/bcq/theme to ~/.config/basecamp/theme.
func migrateTheme(result *MigrateResult) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	configBase := os.Getenv("XDG_CONFIG_HOME")
	if configBase == "" {
		configBase = filepath.Join(home, ".config")
	}

	oldDir := filepath.Join(configBase, "bcq", "theme")
	newDir := filepath.Join(configBase, "basecamp", "theme")

	oldInfo, oldErr := os.Stat(oldDir)
	if oldErr != nil || !oldInfo.IsDir() {
		result.ThemeMessage = "no legacy theme directory found"
		return
	}

	if _, err := os.Stat(newDir); err == nil {
		result.ThemeMessage = "theme directory already exists at new location"
		return
	}

	// Ensure parent exists
	if err := os.MkdirAll(filepath.Dir(newDir), 0700); err != nil {
		result.ThemeMessage = fmt.Sprintf("failed to create parent: %v", err)
		return
	}

	// If old theme is a symlink, recreate it at the new path
	if oldInfo.Mode()&fs.ModeSymlink != 0 || isSymlink(oldDir) {
		target, err := os.Readlink(oldDir)
		if err != nil {
			result.ThemeMessage = fmt.Sprintf("failed to read symlink: %v", err)
			return
		}
		if err := os.Symlink(target, newDir); err != nil {
			result.ThemeMessage = fmt.Sprintf("failed to create symlink: %v", err)
			return
		}
		_ = os.Remove(oldDir)
		result.ThemeMoved = true
		result.ThemeMessage = fmt.Sprintf("symlink recreated: %s → %s", newDir, target)
		return
	}

	if err := os.Rename(oldDir, newDir); err != nil {
		result.ThemeMessage = fmt.Sprintf("rename failed: %v", err)
		return
	}
	result.ThemeMoved = true
	result.ThemeMessage = fmt.Sprintf("moved %s → %s", oldDir, newDir)
}

// isSymlink checks if path is a symbolic link (os.Stat follows symlinks, so use Lstat).
func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.Mode()&fs.ModeSymlink != 0
}

// getApp retrieves app context from command, returning nil if unavailable.
func getApp(cmd *cobra.Command) *appctx.App {
	if cmd == nil || cmd.Context() == nil {
		return nil
	}
	return appctx.FromContext(cmd.Context())
}
