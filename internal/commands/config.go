package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewConfigCmd creates the config command for managing configuration.
func NewConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
		Long: `Manage basecamp configuration.

Configuration is loaded from multiple sources with the following precedence:
  flags > env > local > repo > global > system > defaults

Config locations:
  - System: /etc/basecamp/config.json
  - Global: ~/.config/basecamp/config.json
  - Repo:   <git-root>/.basecamp/config.json
  - Local:  .basecamp/config.json`,
		Annotations: map[string]string{"agent_notes": "config init creates .basecamp/config.json in the current directory\nconfig project interactively selects a project and saves it\nPer-repo config is committed to git — share project defaults with your team\nbasecamp api is an escape hatch for endpoints not yet wrapped by a dedicated command"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigShow(cmd)
		},
	}

	cmd.AddCommand(
		newConfigShowCmd(),
		newConfigInitCmd(),
		newConfigSetCmd(),
		newConfigUnsetCmd(),
		newConfigProjectCmd(),
		newConfigTrustCmd(),
		newConfigUntrustCmd(),
	)

	return cmd
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show effective configuration",
		Long:  "Display the current effective configuration with source information.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigShow(cmd)
		},
	}
}

func runConfigShow(cmd *cobra.Command) error {
	app := appctx.FromContext(cmd.Context())

	// Build config with sources
	configData := make(map[string]any)

	keys := []struct {
		key     string
		value   string
		include bool
	}{
		{"account_id", app.Config.AccountID, app.Config.AccountID != ""},
		{"project_id", app.Config.ProjectID, app.Config.ProjectID != ""},
		{"todolist_id", app.Config.TodolistID, app.Config.TodolistID != ""},
		{"base_url", app.Config.BaseURL, app.Config.BaseURL != ""},
		{"cache_dir", app.Config.CacheDir, app.Config.CacheDir != ""},
		{"cache_enabled", fmt.Sprintf("%t", app.Config.CacheEnabled), app.Config.Sources["cache_enabled"] != "" || !app.Config.CacheEnabled},
		{"format", app.Config.Format, app.Config.Format != ""},
		{"hints", fmt.Sprintf("%t", app.Config.Hints != nil && *app.Config.Hints), app.Config.Hints != nil},
		{"stats", fmt.Sprintf("%t", app.Config.Stats != nil && *app.Config.Stats), app.Config.Stats != nil},
		{"verbose", fmt.Sprintf("%d", derefInt(app.Config.Verbose)), app.Config.Verbose != nil},
	}

	for _, k := range keys {
		if k.include {
			source := app.Config.Sources[k.key]
			if source == "" {
				source = "default"
			}
			configData[k.key] = map[string]string{
				"value":  k.value,
				"source": source,
			}
		}
	}

	return app.OK(configData,
		output.WithSummary("Effective configuration"),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "set",
				Cmd:         "basecamp config set <key> <value>",
				Description: "Set config value",
			},
			output.Breadcrumb{
				Action:      "project",
				Cmd:         "basecamp config project",
				Description: "Select project",
			},
		),
	)
}

func newConfigInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize local config file",
		Long:  "Create a local .basecamp/config.json file in the current directory.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			configDir := ".basecamp"
			configFile := filepath.Join(configDir, "config.json")

			// Check if already exists
			if _, err := os.Stat(configFile); err == nil {
				return app.OK(map[string]any{
					"exists": true,
					"path":   configFile,
				}, output.WithSummary(fmt.Sprintf("Config file already exists: %s", configFile)))
			}

			// Create directory
			if err := os.MkdirAll(configDir, 0700); err != nil {
				return fmt.Errorf("failed to create config directory: %w", err)
			}

			// Create empty config file
			if err := os.WriteFile(configFile, []byte("{}\n"), 0600); err != nil {
				return fmt.Errorf("failed to create config file: %w", err)
			}

			return app.OK(map[string]any{
				"created": true,
				"path":    configFile,
			},
				output.WithSummary(fmt.Sprintf("Created: %s", configFile)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "set",
						Cmd:         "basecamp config set project_id <id>",
						Description: "Set project",
					},
				),
			)
		},
	}
}

func newConfigSetCmd() *cobra.Command {
	var global bool

	cmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value",
		Long: `Set a configuration value in the local or global config file.

Valid keys: account_id, project_id, todolist_id, base_url, cache_dir, cache_enabled,
            format, scope, default_profile, hints, stats, verbose`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			key := args[0]
			value := args[1]

			// Validate key
			validKeys := map[string]bool{
				"account_id":      true,
				"project_id":      true,
				"todolist_id":     true,
				"base_url":        true,
				"cache_dir":       true,
				"cache_enabled":   true,
				"format":          true,
				"scope":           true,
				"default_profile": true,
				"hints":           true,
				"stats":           true,
				"verbose":         true,
			}
			if !validKeys[key] {
				names := make([]string, 0, len(validKeys))
				for k := range validKeys {
					names = append(names, k)
				}
				sort.Strings(names)
				return output.ErrUsage(fmt.Sprintf("Invalid config key %q. Valid keys: %s", key, strings.Join(names, ", ")))
			}

			var configPath string
			var scope string

			if global {
				scope = "global"
				configPath = config.GlobalConfigDir()
				configPath = filepath.Join(configPath, "config.json")
			} else {
				scope = "local"
				configPath = filepath.Join(".basecamp", "config.json")
			}

			// Ensure directory exists
			configDir := filepath.Dir(configPath)
			if err := os.MkdirAll(configDir, 0700); err != nil {
				return fmt.Errorf("failed to create config directory: %w", err)
			}

			// Load existing config or create new
			configData := make(map[string]any)
			if data, err := os.ReadFile(configPath); err == nil { //nolint:gosec // G304: Path is from trusted config location
				_ = json.Unmarshal(data, &configData) // Ignore error - start fresh if invalid
			}

			// Validate default_profile against configured profiles
			if key == "default_profile" {
				profiles, _ := configData["profiles"].(map[string]any)
				if len(profiles) > 0 {
					if _, ok := profiles[value]; !ok {
						names := make([]string, 0, len(profiles))
						for name := range profiles {
							names = append(names, name)
						}
						return output.ErrUsage(fmt.Sprintf("profile %q not found (available: %s)", value, strings.Join(names, ", ")))
					}
				}
			}

			// Set value with type-specific validation
			valueOut := value
			switch key {
			case "cache_enabled", "hints", "stats":
				boolVal, ok := parseBoolFlag(value)
				if !ok {
					return output.ErrUsage(fmt.Sprintf("%s must be true/false (or 1/0)", key))
				}
				configData[key] = boolVal
				valueOut = fmt.Sprintf("%t", boolVal)
			case "verbose":
				level, err := strconv.Atoi(value)
				if err != nil || level < 0 || level > 2 {
					return output.ErrUsage("verbose must be 0, 1, or 2")
				}
				configData[key] = level
				valueOut = value
			default:
				configData[key] = value
			}

			// Write back (atomic: temp + rename)
			data, err := json.MarshalIndent(configData, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to marshal config: %w", err)
			}

			if err := atomicWriteFile(configPath, append(data, '\n')); err != nil {
				return fmt.Errorf("failed to write config: %w", err)
			}

			// Warn when writing authority keys to local config without trust
			if !global && isAuthorityKey(key) {
				absPath, _ := filepath.Abs(configPath)
				ts := config.LoadTrustStore(config.GlobalConfigDir())
				if ts == nil || !ts.IsTrusted(configPath) {
					fmt.Fprintf(os.Stderr, "warning: authority key %q in local config requires trust to take effect; run:\n  basecamp config trust %s\n", key, config.ShellQuote(absPath))
				}
			}

			return app.OK(map[string]any{
				"key":    key,
				"value":  valueOut,
				"scope":  scope,
				"path":   configPath,
				"status": "set",
			},
				output.WithSummary(fmt.Sprintf("Set %s = %s (%s)", key, value, scope)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp config show",
						Description: "View config",
					},
				),
			)
		},
	}

	cmd.Flags().BoolVar(&global, "global", false, "Set in global config (~/.config/basecamp/)")
	// Note: local is the default, so no --local flag needed

	return cmd
}

// isAuthorityKey reports whether key controls where tokens are sent.
func isAuthorityKey(key string) bool {
	switch key {
	case "base_url", "default_profile", "profiles":
		return true
	}
	return false
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func parseBoolFlag(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "on":
		return true, true
	case "false", "0", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func newConfigUnsetCmd() *cobra.Command {
	var global bool

	cmd := &cobra.Command{
		Use:   "unset <key>",
		Short: "Unset a configuration value",
		Long:  "Remove a configuration value from the local or global config file.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			key := args[0]

			var configPath string
			var scope string

			if global {
				scope = "global"
				configPath = filepath.Join(config.GlobalConfigDir(), "config.json")
			} else {
				scope = "local"
				configPath = filepath.Join(".basecamp", "config.json")
			}

			// Load existing config
			configData := make(map[string]any)
			if data, err := os.ReadFile(configPath); err == nil { //nolint:gosec // G304: Path is from trusted config location
				_ = json.Unmarshal(data, &configData) // Ignore error - treat as empty
			} else {
				return app.OK(map[string]any{
					"key":    key,
					"status": "not_found",
				}, output.WithSummary(fmt.Sprintf("Config file not found: %s", configPath)))
			}

			// Check if key exists
			if _, exists := configData[key]; !exists {
				return app.OK(map[string]any{
					"key":    key,
					"status": "not_set",
				}, output.WithSummary(fmt.Sprintf("Key not set: %s", key)))
			}

			// Remove key
			delete(configData, key)

			// Write back (atomic: temp + rename)
			data, err := json.MarshalIndent(configData, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to marshal config: %w", err)
			}

			if err := atomicWriteFile(configPath, append(data, '\n')); err != nil {
				return fmt.Errorf("failed to write config: %w", err)
			}

			return app.OK(map[string]any{
				"key":    key,
				"scope":  scope,
				"status": "unset",
			},
				output.WithSummary(fmt.Sprintf("Unset %s (%s)", key, scope)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp config show",
						Description: "View config",
					},
				),
			)
		},
	}

	cmd.Flags().BoolVar(&global, "global", false, "Unset from global config")

	return cmd
}

// atomicWriteFile writes data to a file atomically using temp+rename.
// Files are always created with 0600 permissions (owner read/write only).
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmpFile.Chmod(0600); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	// Unix: rename atomically replaces the destination.
	// Windows: rename fails when destination exists. Try rename first to
	// preserve the old file on unrelated errors; only remove+retry on failure.
	if err := os.Rename(tmpPath, path); err != nil {
		if runtime.GOOS == "windows" {
			_ = os.Remove(path)
			return os.Rename(tmpPath, path)
		}
		os.Remove(tmpPath) // Clean up stale temp on failure
		return err
	}
	return nil
}

func newConfigTrustCmd() *cobra.Command {
	var list bool

	cmd := &cobra.Command{
		Use:   "trust [path]",
		Short: "Trust a local config file",
		Long: `Trust a local or repo .basecamp/config.json to allow authority keys
(base_url, default_profile, profiles).

Without arguments, trusts the nearest .basecamp/config.json (CWD or repo root).
With --list, shows all trusted config paths.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if list {
				if len(args) > 0 {
					return output.ErrUsage("--list does not accept a path argument")
				}
				ts := config.LoadTrustStore(config.GlobalConfigDir())
				if ts == nil {
					return app.OK([]any{}, output.WithSummary("No trusted configs"))
				}
				entries := ts.List()
				if len(entries) == 0 {
					return app.OK([]any{}, output.WithSummary("No trusted configs"))
				}
				result := make([]map[string]string, len(entries))
				for i, e := range entries {
					result[i] = map[string]string{
						"path":       e.Path,
						"trusted_at": e.TrustedAt,
					}
				}
				return app.OK(result, output.WithSummary(fmt.Sprintf("%d trusted config(s)", len(entries))))
			}

			path, err := resolveConfigTrustPath(args)
			if err != nil {
				return err
			}

			ts := config.NewTrustStore(config.GlobalConfigDir())
			if err := ts.Trust(path); err != nil {
				return fmt.Errorf("failed to trust config: %w", err)
			}

			return app.OK(map[string]any{
				"path":   path,
				"status": "trusted",
			},
				output.WithSummary(fmt.Sprintf("Trusted: %s", path)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp config show",
						Description: "View config (authority keys now active)",
					},
					output.Breadcrumb{
						Action:      "untrust",
						Cmd:         "basecamp config untrust",
						Description: "Revoke trust",
					},
				),
			)
		},
	}

	cmd.Flags().BoolVar(&list, "list", false, "List all trusted config paths")

	return cmd
}

func newConfigUntrustCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "untrust [path]",
		Short: "Untrust a local config file",
		Long: `Revoke trust from a local or repo .basecamp/config.json.
Authority keys (base_url, default_profile, profiles) will be rejected again.

Without arguments, untrusts the nearest .basecamp/config.json.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			path, err := resolveUntrustPath(args)
			if err != nil {
				return err
			}

			ts := config.NewTrustStore(config.GlobalConfigDir())
			removed, err := ts.Untrust(path)
			if err != nil {
				return fmt.Errorf("failed to untrust config: %w", err)
			}

			status := "not_trusted"
			summary := fmt.Sprintf("Not trusted: %s (was not in trust store)", path)
			if removed {
				status = "untrusted"
				summary = fmt.Sprintf("Untrusted: %s", path)
			}

			return app.OK(map[string]any{
				"path":   path,
				"status": status,
			},
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp config show",
						Description: "View config",
					},
				),
			)
		},
	}
}

// resolveConfigTrustPath resolves the config file path for `config trust`.
// Requires the file to exist (you can't trust a nonexistent config).
func resolveConfigTrustPath(args []string) (string, error) {
	if len(args) > 0 {
		absPath, err := filepath.Abs(args[0])
		if err != nil {
			return "", fmt.Errorf("cannot resolve path: %w", err)
		}
		if _, err := os.Stat(absPath); err != nil {
			return "", fmt.Errorf("config file not found: %s", absPath)
		}
		return absPath, nil
	}

	// Try CWD first
	cwdPath := filepath.Join(".basecamp", "config.json")
	if _, err := os.Stat(cwdPath); err == nil {
		absPath, err := filepath.Abs(cwdPath)
		if err != nil {
			return "", fmt.Errorf("cannot resolve path: %w", err)
		}
		return absPath, nil
	}

	// Fall back to repo root
	repoPath := config.RepoConfigPath()
	if repoPath != "" {
		return repoPath, nil
	}

	return "", output.ErrUsage("no .basecamp/config.json found in current directory or repo root")
}

// resolveUntrustPath resolves the config file path for `config untrust`.
// An explicit path argument does NOT require the file to still exist —
// you need to be able to revoke trust for deleted/moved configs.
// Without arguments, auto-discovery still requires the file to exist.
func resolveUntrustPath(args []string) (string, error) {
	if len(args) > 0 {
		absPath, err := filepath.Abs(args[0])
		if err != nil {
			return "", fmt.Errorf("cannot resolve path: %w", err)
		}
		return absPath, nil
	}

	// Auto-discovery: same as trust (file must exist to discover it)
	return resolveConfigTrustPath(nil)
}

func newConfigProjectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "project",
		Short: "Select default project",
		Long:  "Interactively select a project and set it as the default in local config.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Fetch projects
			resp, err := app.Account().Get(cmd.Context(), "/projects.json")
			if err != nil {
				return convertSDKError(err)
			}

			var projects []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(resp.Data, &projects); err != nil {
				return fmt.Errorf("failed to parse projects: %w", err)
			}

			if len(projects) == 0 {
				return output.ErrNotFound("project", "any")
			}

			// Display projects
			fmt.Println("Available projects:")
			fmt.Println()
			for i, p := range projects {
				fmt.Printf("%d. %s (#%d)\n", i+1, p.Name, p.ID)
			}
			fmt.Println()

			// Read selection
			fmt.Printf("Select project (1-%d): ", len(projects))
			var selection int
			if _, err := fmt.Scanf("%d", &selection); err != nil || selection < 1 || selection > len(projects) {
				return output.ErrUsage("Invalid selection")
			}

			selected := projects[selection-1]

			// Save to local config
			configPath := filepath.Join(".basecamp", "config.json")

			// Ensure directory exists
			if err := os.MkdirAll(".basecamp", 0700); err != nil {
				return fmt.Errorf("failed to create config directory: %w", err)
			}

			// Load or create config
			configData := make(map[string]any)
			if data, err := os.ReadFile(configPath); err == nil { //nolint:gosec // G304: Path is from trusted config location
				_ = json.Unmarshal(data, &configData) // Ignore error - start fresh if invalid
			}

			// Set project_id
			configData["project_id"] = fmt.Sprintf("%d", selected.ID)

			// Write back (atomic: temp + rename)
			data, err := json.MarshalIndent(configData, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to marshal config: %w", err)
			}

			if err := atomicWriteFile(configPath, append(data, '\n')); err != nil {
				return fmt.Errorf("failed to write config: %w", err)
			}

			return app.OK(map[string]any{
				"project_id":   selected.ID,
				"project_name": selected.Name,
				"status":       "set",
			},
				output.WithSummary(fmt.Sprintf("Set project_id = %d (%s)", selected.ID, selected.Name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp config show",
						Description: "View config",
					},
					output.Breadcrumb{
						Action:      "project",
						Cmd:         fmt.Sprintf("basecamp projects show %d", selected.ID),
						Description: "View project",
					},
				),
			)
		},
	}
}
