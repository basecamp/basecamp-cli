package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	"github.com/basecamp/basecamp-cli/internal/richtext"
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
	var expectIdentity string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new profile",
		Long: `Create a new named profile and optionally authenticate.

Examples:
  basecamp profile create personal
  basecamp profile create staging --base-url https://staging.example.com
  basecamp profile create triage-bot --scope full
  basecamp profile create bot --account 999 --expect-identity 12345   # Create nothing unless the login is this identity`,
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

			expect, err := parseExpectIdentity(expectIdentity)
			if err != nil {
				return err
			}
			if expect != 0 && os.Getenv("BASECAMP_TOKEN") != "" {
				return errEnvTokenShadows("--expect-identity cannot be checked while BASECAMP_TOKEN is set")
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

			if err := refuseMachineOutputLogin(app, "profile create"); err != nil {
				return err
			}
			if err := refuseNonInteractiveLogin(deviceCode); err != nil {
				return err
			}

			// The entry is written only after the login succeeds, so prove
			// the config file can take it before a credential exists to
			// orphan: a malformed file is refused here, not after OAuth.
			if err := globalConfigTakesProfiles(); err != nil {
				return err
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

			// Start OAuth login flow — must succeed before we persist anything.
			// With an expectation the credential is checked before it is
			// stored and a mismatch stores nothing; without one the
			// identity lookup stays informational.
			w := cmd.OutOrStdout()
			verifier := &loginVerifier{app: app, expectIdentity: expect, account: accountID, strict: expect != 0}
			ctx, stop := loginContext(cmd)
			loginResult, err := app.Auth.Login(ctx, auth.LoginOptions{
				Scope:     scope,
				NoBrowser: noBrowser,
				Remote:    remote,
				Local:     local,
				Logger:    func(msg string) { fmt.Fprintln(w, msg) },
				Progress:  w,
				Verify:    verifier.verify,
			})
			err = loginOutcome(ctx, err, w, output.NewRenderer(w, false))
			stop()
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
			if who := verifier.who; who != nil {
				if who.PersonID != 0 {
					_ = app.Auth.SetUserIdentity(cmd.Context(), strconv.FormatInt(who.PersonID, 10), who.Email)
				}
				result["identity"] = map[string]any{"id": who.IdentityID, "email": who.IdentityEmail}
				if who.PersonID != 0 {
					result["person"] = map[string]any{"id": who.PersonID, "name": who.Name, "email": who.Email}
				}
			}
			return app.OK(result, output.WithSummary(fmt.Sprintf("Created profile %q", name)))
		},
	}

	cmd.Flags().StringVar(&baseURL, "base-url", "", "Basecamp API base URL (default: https://3.basecampapi.com)")
	cmd.Flags().StringVar(&scope, "scope", "", "OAuth scope: 'read' or 'full' (default full; ignored by Launchpad)")
	cmd.Flags().StringVar(&accountID, "account", "", "Account ID")
	registerLoginFlowFlags(cmd, &noBrowser, &remote, &local, &deviceCode)
	cmd.Flags().StringVar(&expectIdentity, "expect-identity", "", "Identity ID the login must authenticate as; otherwise create nothing")
	cmd.MarkFlagsMutuallyExclusive("remote", "local")
	cmd.MarkFlagsMutuallyExclusive("device-code", "local")

	return cmd
}

func newProfileDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a profile",
		Long:  "Remove a profile configuration and its stored credentials, revoking the credential with the server when it can be.",
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

			// The credential delete is irreversible, so prove the config
			// file can take the entry's removal before it goes.
			if err := globalConfigTakesProfiles(); err != nil {
				return err
			}

			summary := fmt.Sprintf("Deleted profile %q", name)
			fields := map[string]any{"name": name, "status": "deleted"}
			// The revocation's egress policy is anchored on the deleted
			// profile's own saved base URL — the operator's configuration for
			// that profile — never on the active configuration, whose base URL
			// may belong to another profile or to a transient override. A
			// credential minted under an override at login time is not
			// recorded as such (the store must not choose its own policy), so
			// that case reaches the summary as a failed revocation, not a
			// silent success.
			result, err := app.Auth.LogoutCredential(cmd.Context(), "profile:"+name, app.Config.Profiles[name].BaseURL)
			switch {
			case errors.Is(err, auth.ErrNoCredential):
				// A profile that never logged in has nothing to revoke.
			case err != nil:
				// Written straight to the terminal, so the store's error text
				// (paths, endpoint hosts) is scrubbed here rather than at a sink.
				fmt.Fprintln(cmd.ErrOrStderr(), richtext.SanitizeSingleLine(fmt.Sprintf("Warning: could not delete credentials for profile %q: %v", name, err)))
			default:
				var outcome map[string]any
				summary, outcome = describeLogout(summary, result)
				maps.Copy(fields, outcome)
			}

			if err := unregisterProfile(name); err != nil {
				return err
			}

			return app.OK(fields, output.WithSummary(summary))
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
	configData, configPath, err := loadGlobalConfigFile()
	if err != nil {
		return false, err
	}
	profilesMap, err := globalProfilesMap(configData, configPath)
	if err != nil {
		return false, err
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

	isDefault = len(profilesMap) == 1
	if isDefault {
		configData["default_profile"] = name
	}

	return isDefault, atomicWriteJSON(configPath, configData)
}

// loadGlobalConfigFile returns the global config file's contents and path,
// with the config directory in place for the write every caller is about
// to make. A missing file is an empty config; a file that cannot be read or
// parsed is an error, since writing the map back would otherwise replace
// whatever the operator had with a partial decode.
func loadGlobalConfigFile() (map[string]any, string, error) {
	if err := os.MkdirAll(config.GlobalConfigDir(), 0700); err != nil {
		return nil, "", fmt.Errorf("failed to create config directory: %w", err)
	}
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
	// A top-level null decodes into a nil map that every writer would
	// then assign into; it is refused like any other non-object value.
	if configData == nil {
		return nil, configPath, fmt.Errorf("config file %s is not a JSON object, refusing to rewrite it", configPath)
	}
	return configData, configPath, nil
}

// globalProfilesMap returns the config's "profiles" object, creating it in
// the map when absent. A present value of any other shape is refused for
// the same reason a parse failure is: the caller is about to write the map
// back, and replacing an unexpected value would destroy operator config.
func globalProfilesMap(configData map[string]any, configPath string) (map[string]any, error) {
	raw, present := configData["profiles"]
	if !present {
		profilesMap := make(map[string]any)
		configData["profiles"] = profilesMap
		return profilesMap, nil
	}
	profilesMap, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("config file %s has a \"profiles\" value that is not an object, refusing to rewrite it", configPath)
	}
	return profilesMap, nil
}

// globalConfigTakesProfiles reports whether the global config file's shape
// can take a profile entry — readable, parseable, with an object (or
// absent) profiles value — without writing anything. It is the clobber
// refusal run early: a file the writers would refuse to rewrite is refused
// before a step that cannot be taken back. It does not probe permissions;
// an unwritable directory fails at the write with the OS error.
func globalConfigTakesProfiles() error {
	configData, configPath, err := loadGlobalConfigFile()
	if err != nil {
		return err
	}
	_, err = globalProfilesMap(configData, configPath)
	return err
}

// globalProfileEntry returns the named profile's entry in the global config
// file, or nil when the file has none (or its profiles value is not an
// object — which the writers refuse separately).
func globalProfileEntry(configData map[string]any, name string) map[string]any {
	profilesMap, _ := configData["profiles"].(map[string]any)
	entry, _ := profilesMap[name].(map[string]any)
	return entry
}

// globalBindingBlocker says why binding an accountless profile would not
// take effect, and what to change instead; "" when it would.
//
// Every command that binds a profile's account (`auth agent connect`, the
// headless logins) does it one way: bindProfileAccount writes account_id
// into the profile's entry in the global config file. So this is the one
// question they ask before binding, and the one the hints that send an
// operator to them ask first.
//
// The write takes effect exactly when the global file is one of the layers
// the profile is made of (config.mergeProfile has the rule): the profile has
// no account, so no closer layer sets one, and the global file's shows
// through. When the global file is not one of them, either a closer entry
// for another Basecamp replaced its entry whole, or it has no entry, and the
// account has to go into a file that is.
func globalBindingBlocker(cfg *config.Config, name string) string {
	if globalConfigUnusable() {
		// A global config the loader could not read or parse was skipped,
		// so the layers say nothing about which file defines the profile —
		// the one that does may be the file that was skipped. Say nothing
		// about files: the commands that bind report the unusable file
		// itself, in full, before they write.
		return ""
	}
	origin := cfg.ProfileOrigins[name]
	if origin == nil || len(origin.Layers) == 0 {
		// Only a config file defines a profile; an entry with no origin was
		// made in memory, and the global file is where it belongs.
		return "It comes from no config file, so add it, with account_id, to " +
			richtext.SanitizeSingleLine(filepath.Join(config.GlobalConfigDir(), "config.json"))
	}
	if origin.Includes(config.SourceGlobal) {
		return ""
	}
	closest := origin.Closest()
	if hidden := origin.ReplacedLayer(config.SourceGlobal); hidden != nil {
		// The entry that replaced it did so for being on another Basecamp,
		// or for naming another account there.
		why := fmt.Sprintf("is for %s, not %s", richtext.SanitizeSingleLine(hidden.By.BaseURL), richtext.SanitizeSingleLine(hidden.BaseURL))
		if config.NormalizeBaseURL(hidden.By.BaseURL) == config.NormalizeBaseURL(hidden.BaseURL) {
			why = "names another account"
		}
		return fmt.Sprintf("Its entry in %s %s, so it replaces the global config's entry in %s and any account bound there. Add account_id to the profile's entry in %s",
			richtext.SanitizeSingleLine(hidden.By.Path), why, richtext.SanitizeSingleLine(hidden.Path), richtext.SanitizeSingleLine(closest.Path))
	}
	// The global config has no entry for this profile, so there is nothing
	// to bind. An entry added there is refined by the ones that do define
	// it — and the account in it holds — but only while nothing closer
	// than the global config replaces it. Where something does, the global
	// config is no remedy at all: a new entry would be replaced the same
	// way. A replaced entry the system config made is no such evidence: it
	// is farther than the global config, and a new entry there replaces it
	// rather than the other way round.
	if replaced := firstReplacedAfterGlobal(origin); replaced != nil {
		return fmt.Sprintf("Its entry comes from %s, not the global config, and the entry in %s, for %s, replaces everything farther — an entry added to the global config among them. Add account_id to the profile's entry in %s",
			richtext.SanitizeSingleLine(closest.Path), richtext.SanitizeSingleLine(replaced.By.Path),
			richtext.SanitizeSingleLine(replaced.By.BaseURL), richtext.SanitizeSingleLine(closest.Path))
	}
	// Naming the global config first: a repo config is shared, and an
	// operator's account does not belong in it (nor can they always
	// write /etc).
	return fmt.Sprintf("Its entry comes from %s, not the global config, so no command can bind it. Give it an entry in %s with base_url %s and an account_id — or add account_id to the entry in %s",
		richtext.SanitizeSingleLine(closest.Path),
		richtext.SanitizeSingleLine(filepath.Join(config.GlobalConfigDir(), "config.json")),
		richtext.SanitizeSingleLine(closest.BaseURL),
		richtext.SanitizeSingleLine(closest.Path))
}

// firstReplacedAfterGlobal is the farthest replaced entry a new global
// entry would be replaced along with: one a repo or local config made,
// which is closer than the global config. A system entry is farther, so a
// replacement of one says nothing about an entry added to the global
// config. Nil when there is none.
func firstReplacedAfterGlobal(origin *config.ProfileOrigin) *config.ReplacedProfileLayer {
	for i := range origin.Replaced {
		if origin.Replaced[i].Source == config.SourceRepo || origin.Replaced[i].Source == config.SourceLocal {
			return &origin.Replaced[i]
		}
	}
	return nil
}

// globalConfigUnusable reports whether the global config file exists but
// cannot be read, cannot be parsed, or holds a "profiles" value that is not
// an object. The loader skips such a file's profiles, so what it recorded
// about where a profile comes from is not evidence — the entry that defines
// it may be in the file that was skipped — and every writer refuses the
// file, which is what the operator has to hear instead.
func globalConfigUnusable() bool {
	path := filepath.Join(config.GlobalConfigDir(), "config.json")
	data, err := os.ReadFile(path) //nolint:gosec // G304: the global config path
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		return true
	}
	var parsed map[string]any
	if json.Unmarshal(data, &parsed) != nil || parsed == nil {
		return true
	}
	profiles, present := parsed["profiles"]
	if !present || profiles == nil {
		return false
	}
	_, isObject := profiles.(map[string]any)
	return !isObject
}

// boundIn names the config file a profile's account came from, as " (bound
// in <path>)" for a message about that account, and is empty for an entry
// this invocation made rather than a file.
func boundIn(cfg *config.Config, name string) string {
	if path := profileFieldFile(cfg, name, "account_id"); path != "" {
		return " (bound in " + richtext.SanitizeSingleLine(path) + ")"
	}
	return ""
}

// profileFieldFile is the path of the config file that set a field on a
// profile, "" when the profile did not come from files.
func profileFieldFile(cfg *config.Config, name, field string) string {
	origin := cfg.ProfileOrigins[name]
	if origin == nil {
		return ""
	}
	return origin.Fields[field]
}

// bindProfileAccount sets the account on an existing profile entry in the
// global config file, leaving every other field of the entry as it is.
func bindProfileAccount(name, account string) error {
	configData, configPath, err := loadGlobalConfigFile()
	if err != nil {
		return err
	}
	if _, err := globalProfilesMap(configData, configPath); err != nil {
		return err
	}
	entry := globalProfileEntry(configData, name)
	if entry == nil {
		return output.ErrUsage(fmt.Sprintf("Profile %q not found in %s", name, configPath))
	}
	entry["account_id"] = account
	return atomicWriteJSON(configPath, configData)
}

// unregisterProfile removes a profile entry from the global config file,
// clearing default_profile when it named this profile. Credentials are the
// caller's to remove.
func unregisterProfile(name string) error {
	configData, configPath, err := loadGlobalConfigFile()
	if err != nil {
		return err
	}
	profilesMap, err := globalProfilesMap(configData, configPath)
	if err != nil {
		return err
	}
	delete(profilesMap, name)
	if len(profilesMap) == 0 {
		delete(configData, "profiles")
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
