package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewProfileCmd creates the profile command group.
func NewProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage named profiles",
		Long: `Manage named profiles that bundle identity (credentials) with environment (server + defaults).

Profiles allow you to switch between multiple Basecamp identities on the same server,
or maintain separate configurations for different environments.

Examples:
  basecamp profile list                    # List all profiles
  basecamp profile show                    # Show active profile details
  basecamp profile create personal         # Create a new profile
  basecamp profile delete old-profile      # Remove a profile
  basecamp profile set-default personal    # Set default profile`,
	}

	cmd.AddCommand(
		newProfileListCmd(),
		newProfileShowCmd(),
		newProfileCreateCmd(),
		newProfileDeleteCmd(),
		newProfileSetDefaultCmd(),
	)

	return cmd
}

func newProfileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all profiles",
		Long:  "List all configured profiles with their base URL and authentication status.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if len(app.Config.Profiles) == 0 {
				return app.OK([]any{}, output.WithSummary("No profiles configured"))
			}

			// Sort profile names
			names := make([]string, 0, len(app.Config.Profiles))
			for name := range app.Config.Profiles {
				names = append(names, name)
			}
			sort.Strings(names)

			profiles := make([]map[string]any, 0, len(names))
			for _, name := range names {
				p := app.Config.Profiles[name]
				entry := map[string]any{
					"name":     name,
					"base_url": p.BaseURL,
				}

				// Check auth status
				credKey := "profile:" + name
				store := app.Auth.GetStore()
				creds, err := store.Load(credKey)
				if err == nil && creds.AccessToken != "" {
					entry["authenticated"] = true
				} else {
					entry["authenticated"] = false
				}

				if app.Config.DefaultProfile == name {
					entry["default"] = true
				}
				if app.Config.ActiveProfile == name {
					entry["active"] = true
				}
				if p.AccountID != "" {
					entry["account_id"] = p.AccountID
				}

				profiles = append(profiles, entry)
			}

			return app.OK(profiles, output.WithSummary(fmt.Sprintf("%d profile(s)", len(profiles))))
		},
	}
}

func newProfileShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show [name]",
		Short: "Show profile details",
		Long:  "Show configuration and authentication details for a profile.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			var name string
			if len(args) > 0 {
				name = args[0]
			} else if app.Config.ActiveProfile != "" {
				name = app.Config.ActiveProfile
			} else if app.Config.DefaultProfile != "" {
				name = app.Config.DefaultProfile
			} else {
				return cmd.Help()
			}

			p, ok := app.Config.Profiles[name]
			if !ok {
				return output.ErrUsage(fmt.Sprintf("Profile %q not found", name))
			}

			result := map[string]any{
				"name":     name,
				"base_url": p.BaseURL,
			}
			if p.AccountID != "" {
				result["account_id"] = p.AccountID
			}
			if p.ProjectID != "" {
				result["project_id"] = p.ProjectID
			}
			if p.TodolistID != "" {
				result["todolist_id"] = p.TodolistID
			}
			if app.Config.DefaultProfile == name {
				result["default"] = true
			}

			// Check auth status
			credKey := "profile:" + name
			store := app.Auth.GetStore()
			creds, err := store.Load(credKey)
			isLaunchpad := false
			if err == nil && creds.AccessToken != "" {
				result["authenticated"] = true
				result["oauth_type"] = creds.OAuthType
				if creds.Source != "" {
					result["source"] = creds.Source
				}
				isLaunchpad = creds.OAuthType == "launchpad"

				// Suppress credential scope for Launchpad (scopes not supported)
				if !isLaunchpad && creds.Scope != "" {
					result["credential_scope"] = creds.Scope
				}
				if creds.UserID != "" {
					result["user_id"] = creds.UserID
				}
			} else {
				result["authenticated"] = false
			}

			// Show profile config scope only when not Launchpad-authenticated
			// (Launchpad scope is misleading; unauthenticated profiles show as-is)
			if p.Scope != "" && !isLaunchpad {
				result["scope"] = p.Scope
			}

			return app.OK(result, output.WithSummary(fmt.Sprintf("Profile: %s", name)))
		},
	}
}

func newProfileCreateCmd() *cobra.Command {
	var baseURL string
	var scope string
	var accountID string
	var noBrowser bool
	var remote bool
	var local bool
	var deviceCode bool

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new profile",
		Long: `Create a new named profile and optionally authenticate.

Examples:
  basecamp profile create personal
  basecamp profile create staging --base-url https://staging.example.com
  basecamp profile create triage-bot --scope full`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			name := args[0]

			// Validate profile name (used in credential keys and cache paths)
			if !isValidProfileName(name) {
				return output.ErrUsage(fmt.Sprintf("Invalid profile name %q: use only letters, numbers, hyphens, and underscores", name))
			}

			// Check if profile already exists
			if app.Config.Profiles != nil {
				if _, exists := app.Config.Profiles[name]; exists {
					return output.ErrUsage(fmt.Sprintf("Profile %q already exists", name))
				}
			}

			// Defaults
			if baseURL == "" {
				baseURL = "https://3.basecampapi.com"
			}

			// Build profile config (scope unknown until after discovery)
			profileCfg := &config.ProfileConfig{
				BaseURL: baseURL,
			}
			if accountID != "" {
				profileCfg.AccountID = accountID
			}

			// Snapshot in-memory config before mutation
			prevActiveProfile := app.Config.ActiveProfile
			prevBaseURL := app.Config.BaseURL

			// Set up in-memory config for the login flow (no persistence yet)
			if app.Config.Profiles == nil {
				app.Config.Profiles = make(map[string]*config.ProfileConfig)
			}
			app.Config.Profiles[name] = profileCfg
			app.Config.ActiveProfile = name
			app.Config.BaseURL = profileCfg.BaseURL

			if deviceCode {
				remote = true
			}

			// Start OAuth login flow — must succeed before we persist anything
			loginResult, err := app.Auth.Login(cmd.Context(), auth.LoginOptions{
				Scope:     scope,
				NoBrowser: noBrowser,
				Remote:    remote,
				Local:     local,
				Logger:    func(msg string) { fmt.Println(msg) },
			})
			if err != nil {
				// Restore in-memory state
				delete(app.Config.Profiles, name)
				app.Config.ActiveProfile = prevActiveProfile
				app.Config.BaseURL = prevBaseURL
				return err
			}

			// Login succeeded — persist profile to config
			if loginResult.Scope != "" {
				profileCfg.Scope = loginResult.Scope
			}

			isDefault, err := registerProfile(name, profileCfg)
			if err != nil {
				return err
			}

			// Try to fetch and store user profile
			resp, profileErr := app.SDK.Get(cmd.Context(), "/my/profile.json")
			if profileErr == nil {
				var profile struct {
					ID    int    `json:"id"`
					Name  string `json:"name"`
					Email string `json:"email_address"`
				}
				if err := resp.UnmarshalData(&profile); err == nil {
					_ = app.Auth.SetUserIdentity(fmt.Sprintf("%d", profile.ID), profile.Email)
				}
			}

			result := map[string]any{
				"name":     name,
				"base_url": baseURL,
			}
			if loginResult.Scope != "" {
				result["scope"] = loginResult.Scope
			}
			if isDefault {
				result["default"] = true
			}
			return app.OK(result, output.WithSummary(fmt.Sprintf("Created profile %q", name)))
		},
	}

	cmd.Flags().StringVar(&baseURL, "base-url", "", "Basecamp API base URL (default: https://3.basecampapi.com)")
	cmd.Flags().StringVar(&scope, "scope", "", "OAuth scope: 'read' or 'full' (default full; ignored by Launchpad)")
	cmd.Flags().StringVar(&accountID, "account", "", "Account ID")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Don't open browser automatically")
	cmd.Flags().BoolVar(&remote, "remote", false, "Force remote/headless mode (paste callback URL instead of local listener)")
	cmd.Flags().BoolVar(&local, "local", false, "Force local mode (override SSH auto-detection)")
	cmd.Flags().BoolVar(&deviceCode, "device-code", false, "Headless authentication with manual browser instructions")
	cmd.MarkFlagsMutuallyExclusive("remote", "local")
	cmd.MarkFlagsMutuallyExclusive("device-code", "local")

	return cmd
}

func newProfileDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a profile",
		Long:  "Remove a profile configuration and its stored credentials.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			name := args[0]

			// Verify profile exists
			if app.Config.Profiles == nil {
				return output.ErrUsage(fmt.Sprintf("Profile %q not found", name))
			}
			if _, ok := app.Config.Profiles[name]; !ok {
				return output.ErrUsage(fmt.Sprintf("Profile %q not found", name))
			}

			// Remove credentials
			credKey := "profile:" + name
			store := app.Auth.GetStore()
			if err := store.Delete(credKey); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not delete credentials for profile %q: %v\n", name, err)
			}

			if err := unregisterProfile(name); err != nil {
				return err
			}

			return app.OK(map[string]any{
				"name":   name,
				"status": "deleted",
			}, output.WithSummary(fmt.Sprintf("Deleted profile %q", name)))
		},
	}
}

func newProfileSetDefaultCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-default <name>",
		Short: "Set the default profile",
		Long:  "Set which profile is used when no --profile flag is specified.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			name := args[0]

			// Verify profile exists
			if app.Config.Profiles == nil {
				return output.ErrUsage(fmt.Sprintf("Profile %q not found", name))
			}
			if _, ok := app.Config.Profiles[name]; !ok {
				return output.ErrUsage(fmt.Sprintf("Profile %q not found", name))
			}

			configData, configPath, err := loadGlobalConfigFile()
			if err != nil {
				return err
			}
			configData["default_profile"] = name

			if err := atomicWriteJSON(configPath, configData); err != nil {
				return err
			}

			return app.OK(map[string]any{
				"name":   name,
				"status": "set_default",
			}, output.WithSummary(fmt.Sprintf("Default profile set to %q", name)))
		},
	}
}

// registerProfile adds a profile entry to the global config file. The first
// profile registered becomes the default; isDefault reports whether this one
// did. The in-memory config is the caller's to update.
func registerProfile(name string, p *config.ProfileConfig) (isDefault bool, err error) {
	if err := os.MkdirAll(config.GlobalConfigDir(), 0700); err != nil {
		return false, fmt.Errorf("failed to create config directory: %w", err)
	}
	configData, configPath, err := loadGlobalConfigFile()
	if err != nil {
		return false, err
	}

	profilesMap, _ := configData["profiles"].(map[string]any)
	if profilesMap == nil {
		profilesMap = make(map[string]any)
	}

	entry := map[string]any{
		"base_url": p.BaseURL,
	}
	if p.AccountID != "" {
		entry["account_id"] = p.AccountID
	}
	if p.Scope != "" {
		entry["scope"] = p.Scope
	}
	profilesMap[name] = entry
	configData["profiles"] = profilesMap

	isDefault = len(profilesMap) == 1
	if isDefault {
		configData["default_profile"] = name
	}

	return isDefault, atomicWriteJSON(configPath, configData)
}

// loadGlobalConfigFile returns the global config file's contents and path.
// A missing file is an empty config; a file that cannot be read or parsed
// is an error, since every caller is about to write the map back and would
// otherwise replace whatever the operator had with a partial decode.
func loadGlobalConfigFile() (map[string]any, string, error) {
	configPath := filepath.Join(config.GlobalConfigDir(), "config.json")
	configData := make(map[string]any)
	data, err := os.ReadFile(configPath) //nolint:gosec // G304: Path is from trusted config location
	if os.IsNotExist(err) {
		return configData, configPath, nil
	}
	if err != nil {
		return nil, configPath, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}
	if err := json.Unmarshal(data, &configData); err != nil {
		return nil, configPath, fmt.Errorf("config file %s is not valid JSON, refusing to rewrite it: %w", configPath, err)
	}
	return configData, configPath, nil
}

// globalProfileEntry returns the named profile's entry in the global config
// file, or nil when the file has none.
func globalProfileEntry(configData map[string]any, name string) map[string]any {
	profilesMap, _ := configData["profiles"].(map[string]any)
	entry, _ := profilesMap[name].(map[string]any)
	return entry
}

// globalProfileIsUnbound reports whether the global config file defines the
// profile without an account — the one shape bindProfileAccount can act on.
// Config layers merge per profile name, so the effective profile being
// accountless says nothing about which file it came from; the global entry
// itself is the evidence.
func globalProfileIsUnbound(name string) (bool, error) {
	configData, _, err := loadGlobalConfigFile()
	if err != nil {
		return false, err
	}
	entry := globalProfileEntry(configData, name)
	return entry != nil && getStringOrNumber(entry, "account_id") == "", nil
}

// bindProfileAccount sets the account on an existing profile entry in the
// global config file, leaving every other field of the entry as it is.
func bindProfileAccount(name, account string) error {
	configData, configPath, err := loadGlobalConfigFile()
	if err != nil {
		return err
	}
	entry := globalProfileEntry(configData, name)
	if entry == nil {
		return output.ErrUsage(fmt.Sprintf("Profile %q not found in %s", name, configPath))
	}
	entry["account_id"] = account
	return atomicWriteJSON(configPath, configData)
}

// getStringOrNumber reads a config value that may be stored as a string or
// a JSON number, as config.loadFromFile accepts for IDs.
func getStringOrNumber(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return ""
	}
}

// unregisterProfile removes a profile entry from the global config file,
// clearing default_profile when it named this profile. Credentials are the
// caller's to remove.
func unregisterProfile(name string) error {
	configData, configPath, err := loadGlobalConfigFile()
	if err != nil {
		return err
	}

	if profilesMap, ok := configData["profiles"].(map[string]any); ok {
		delete(profilesMap, name)
		if len(profilesMap) == 0 {
			delete(configData, "profiles")
		}
	}

	if dp, ok := configData["default_profile"].(string); ok && dp == name {
		delete(configData, "default_profile")
	}

	return atomicWriteJSON(configPath, configData)
}

// validProfileName matches letters, numbers, hyphens, and underscores.
var validProfileName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func isValidProfileName(name string) bool {
	return validProfileName.MatchString(name)
}

// atomicWriteJSON writes configData as indented JSON to path using a temp file + rename.
func atomicWriteJSON(path string, configData map[string]any) error {
	data, err := json.MarshalIndent(configData, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename config file: %w", err)
	}
	return nil
}
