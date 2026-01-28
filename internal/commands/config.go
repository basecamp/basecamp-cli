package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/output"
)

// NewConfigCmd creates the config command for managing configuration.
func NewConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
		Long: `Manage bcq configuration.

Configuration is loaded from multiple sources with the following precedence:
  flags > env > local > repo > global > system > defaults

Config locations:
  - System: /etc/basecamp/config.json
  - Global: ~/.config/basecamp/config.json
  - Repo:   <git-root>/.basecamp/config.json
  - Local:  .basecamp/config.json`,
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

	return app.Output.OK(configData,
		output.WithSummary("Effective configuration"),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "set",
				Cmd:         "bcq config set <key> <value>",
				Description: "Set config value",
			},
			output.Breadcrumb{
				Action:      "project",
				Cmd:         "bcq config project",
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
				return app.Output.OK(map[string]any{
					"exists": true,
					"path":   configFile,
				}, output.WithSummary(fmt.Sprintf("Config file already exists: %s", configFile)))
			}

			// Create directory
			if err := os.MkdirAll(configDir, 0750); err != nil {
				return fmt.Errorf("failed to create config directory: %w", err)
			}

			// Create empty config file
			if err := os.WriteFile(configFile, []byte("{}\n"), 0600); err != nil {
				return fmt.Errorf("failed to create config file: %w", err)
			}

			return app.Output.OK(map[string]any{
				"created": true,
				"path":    configFile,
			},
				output.WithSummary(fmt.Sprintf("Created: %s", configFile)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "set",
						Cmd:         "bcq config set project_id <id>",
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

Valid keys: account_id, project_id, todolist_id, base_url, cache_dir, cache_enabled, format, scope`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			key := args[0]
			value := args[1]

			// Validate key
			validKeys := map[string]bool{
				"account_id":    true,
				"project_id":    true,
				"todolist_id":   true,
				"base_url":      true,
				"cache_dir":     true,
				"cache_enabled": true,
				"format":        true,
				"scope":         true,
			}
			if !validKeys[key] {
				return output.ErrUsage(fmt.Sprintf("Invalid config key: %s", key))
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
			if err := os.MkdirAll(configDir, 0750); err != nil {
				return fmt.Errorf("failed to create config directory: %w", err)
			}

			// Load existing config or create new
			configData := make(map[string]any)
			if data, err := os.ReadFile(configPath); err == nil { //nolint:gosec // G304: Path is from trusted config location
				_ = json.Unmarshal(data, &configData) // Ignore error - start fresh if invalid
			}

			// Set value
			valueOut := value
			if key == "cache_enabled" {
				boolVal, ok := parseBoolFlag(value)
				if !ok {
					return output.ErrUsage("cache_enabled must be true/false (or 1/0)")
				}
				configData[key] = boolVal
				valueOut = fmt.Sprintf("%t", boolVal)
			} else {
				configData[key] = value
			}

			// Write back
			data, err := json.MarshalIndent(configData, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to marshal config: %w", err)
			}

			if err := os.WriteFile(configPath, append(data, '\n'), 0600); err != nil {
				return fmt.Errorf("failed to write config: %w", err)
			}

			return app.Output.OK(map[string]any{
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
						Cmd:         "bcq config show",
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
				return app.Output.OK(map[string]any{
					"key":    key,
					"status": "not_found",
				}, output.WithSummary(fmt.Sprintf("Config file not found: %s", configPath)))
			}

			// Check if key exists
			if _, exists := configData[key]; !exists {
				return app.Output.OK(map[string]any{
					"key":    key,
					"status": "not_set",
				}, output.WithSummary(fmt.Sprintf("Key not set: %s", key)))
			}

			// Remove key
			delete(configData, key)

			// Write back
			data, err := json.MarshalIndent(configData, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to marshal config: %w", err)
			}

			if err := os.WriteFile(configPath, append(data, '\n'), 0600); err != nil {
				return fmt.Errorf("failed to write config: %w", err)
			}

			return app.Output.OK(map[string]any{
				"key":    key,
				"scope":  scope,
				"status": "unset",
			},
				output.WithSummary(fmt.Sprintf("Unset %s (%s)", key, scope)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "bcq config show",
						Description: "View config",
					},
				),
			)
		},
	}

	cmd.Flags().BoolVar(&global, "global", false, "Unset from global config")

	return cmd
}

func newConfigProjectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "project",
		Short: "Select default project",
		Long:  "Interactively select a project and set it as the default in local config.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.SDK.RequireAccount(); err != nil {
				return err
			}

			// Fetch projects
			resp, err := app.SDK.Get(cmd.Context(), "/projects.json")
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
			if err := os.MkdirAll(".basecamp", 0750); err != nil {
				return fmt.Errorf("failed to create config directory: %w", err)
			}

			// Load or create config
			configData := make(map[string]any)
			if data, err := os.ReadFile(configPath); err == nil { //nolint:gosec // G304: Path is from trusted config location
				_ = json.Unmarshal(data, &configData) // Ignore error - start fresh if invalid
			}

			// Set project_id
			configData["project_id"] = fmt.Sprintf("%d", selected.ID)

			// Write back
			data, err := json.MarshalIndent(configData, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to marshal config: %w", err)
			}

			if err := os.WriteFile(configPath, append(data, '\n'), 0600); err != nil {
				return fmt.Errorf("failed to write config: %w", err)
			}

			return app.Output.OK(map[string]any{
				"project_id":   selected.ID,
				"project_name": selected.Name,
				"status":       "set",
			},
				output.WithSummary(fmt.Sprintf("Set project_id = %d (%s)", selected.ID, selected.Name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "bcq config show",
						Description: "View config",
					},
					output.Breadcrumb{
						Action:      "project",
						Cmd:         fmt.Sprintf("bcq projects show %d", selected.ID),
						Description: "View project",
					},
				),
			)
		},
	}
}
