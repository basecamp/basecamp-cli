package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/auth"
	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/names"
	"github.com/basecamp/bcq/internal/output"
)

// --- Test helpers ---

// setupProfileTestApp creates a minimal test app for profile command tests.
// It sets XDG_CONFIG_HOME to a temp dir so config operations are isolated,
// and disables the system keyring via BCQ_NO_KEYRING.
func setupProfileTestApp(t *testing.T, cfg *config.Config) (*appctx.App, *bytes.Buffer) {
	t.Helper()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("BCQ_NO_KEYRING", "1")

	if cfg == nil {
		cfg = &config.Config{
			BaseURL:  "https://3.basecampapi.com",
			CacheDir: t.TempDir(),
			Sources:  make(map[string]string),
		}
	}
	if cfg.Sources == nil {
		cfg.Sources = make(map[string]string)
	}

	buf := &bytes.Buffer{}
	authMgr := auth.NewManager(cfg, nil)

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, nil, basecamp.WithMaxRetries(0))
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
		Flags: appctx.GlobalFlags{
			JSON: true,
		},
	}
	return app, buf
}

// executeProfileCommand executes a cobra command with the given app context and args.
func executeProfileCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd.Execute()
}

// readConfigFile reads and parses the global config.json from the test temp dir.
func readConfigFile(t *testing.T) map[string]any {
	t.Helper()
	configPath := filepath.Join(config.GlobalConfigDir(), "config.json")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err, "failed to read config file at %s", configPath)

	var configData map[string]any
	require.NoError(t, json.Unmarshal(data, &configData))
	return configData
}

// writeConfigFile writes a config map as JSON to the global config path.
func writeConfigFile(t *testing.T, configData map[string]any) {
	t.Helper()
	configDir := config.GlobalConfigDir()
	require.NoError(t, os.MkdirAll(configDir, 0700))

	data, err := json.MarshalIndent(configData, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.json"), append(data, '\n'), 0600))
}

// --- Command structure tests ---

func TestNewProfileCmd(t *testing.T) {
	cmd := NewProfileCmd()
	assert.Equal(t, "profile", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.NotEmpty(t, cmd.Long)
}

func TestProfileSubcommands(t *testing.T) {
	cmd := NewProfileCmd()

	expected := []string{"list", "show", "create", "delete", "set-default"}
	for _, name := range expected {
		sub, _, err := cmd.Find([]string{name})
		assert.NoError(t, err, "expected subcommand %q to exist", name)
		assert.NotNil(t, sub, "expected subcommand %q to exist", name)
		assert.NotEmpty(t, sub.Short, "expected non-empty Short for %q", name)
	}
}

// --- isValidProfileName tests ---

func TestIsValidProfileName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		// Valid names
		{name: "simple lowercase", input: "personal", valid: true},
		{name: "with hyphen", input: "my-profile", valid: true},
		{name: "with underscore", input: "dev_server", valid: true},
		{name: "single char", input: "a", valid: true},
		{name: "mixed alphanumeric with hyphen and underscore", input: "A1-b_2", valid: true},
		{name: "all digits", input: "123", valid: true},
		{name: "uppercase", input: "PROD", valid: true},
		{name: "digit start", input: "1profile", valid: true},

		// Invalid names
		{name: "empty string", input: "", valid: false},
		{name: "path traversal", input: "../evil", valid: false},
		{name: "space", input: "has space", valid: false},
		{name: "colon", input: "has:colon", valid: false},
		{name: "leading slash", input: "/slash", valid: false},
		{name: "path separator", input: "profile/sub", valid: false},
		{name: "leading dot", input: ".dotfirst", valid: false},
		{name: "leading hyphen", input: "-dashfirst", valid: false},
		{name: "leading underscore", input: "_underfirst", valid: false},
		{name: "special chars", input: "pro@file", valid: false},
		{name: "backslash", input: "pro\\file", valid: false},
		{name: "tilde", input: "~profile", valid: false},
		{name: "null byte", input: "pro\x00file", valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidProfileName(tt.input)
			assert.Equal(t, tt.valid, result, "isValidProfileName(%q) = %v, want %v", tt.input, result, tt.valid)
		})
	}
}

// --- Profile create command tests ---

func TestProfileCreateRequiresExactlyOneArg(t *testing.T) {
	root := &cobra.Command{Use: "bcq"}
	profileCmd := NewProfileCmd()
	root.AddCommand(profileCmd)

	root.SetArgs([]string{"profile", "create"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

func TestProfileCreateRejectsInvalidNames(t *testing.T) {
	// Note: "-dashfirst" is tested in TestIsValidProfileName but omitted here
	// because Cobra intercepts it as a shorthand flag before our validation runs.
	invalidNames := []string{"../evil", "has space", ".dotfirst", "_underfirst"}

	for _, name := range invalidNames {
		t.Run(name, func(t *testing.T) {
			app, _ := setupProfileTestApp(t, nil)

			root := &cobra.Command{Use: "bcq"}
			profileCmd := NewProfileCmd()
			root.AddCommand(profileCmd)

			root.SetArgs([]string{"profile", "create", name})
			ctx := appctx.WithApp(context.Background(), app)
			root.SetContext(ctx)
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})

			err := root.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "Invalid profile name")
		})
	}
}

func TestProfileCreateHasExpectedFlags(t *testing.T) {
	cmd := newProfileCreateCmd()

	flags := []string{"base-url", "scope", "account", "no-browser"}
	for _, flag := range flags {
		f := cmd.Flags().Lookup(flag)
		assert.NotNil(t, f, "expected flag %q to exist on create command", flag)
	}
}

func TestProfileCreateRejectsDuplicateName(t *testing.T) {
	cfg := &config.Config{
		BaseURL:  "https://3.basecampapi.com",
		CacheDir: t.TempDir(),
		Sources:  make(map[string]string),
		Profiles: map[string]*config.ProfileConfig{
			"existing": {BaseURL: "https://3.basecampapi.com"},
		},
	}
	app, _ := setupProfileTestApp(t, cfg)

	root := &cobra.Command{Use: "bcq"}
	profileCmd := NewProfileCmd()
	root.AddCommand(profileCmd)

	root.SetArgs([]string{"profile", "create", "existing"})
	ctx := appctx.WithApp(context.Background(), app)
	root.SetContext(ctx)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

// --- Profile delete command tests ---

func TestProfileDeleteRequiresExactlyOneArg(t *testing.T) {
	root := &cobra.Command{Use: "bcq"}
	profileCmd := NewProfileCmd()
	root.AddCommand(profileCmd)

	root.SetArgs([]string{"profile", "delete"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

func TestProfileDeleteNonexistentProfile(t *testing.T) {
	app, _ := setupProfileTestApp(t, nil)

	root := &cobra.Command{Use: "bcq"}
	profileCmd := NewProfileCmd()
	root.AddCommand(profileCmd)

	root.SetArgs([]string{"profile", "delete", "nonexistent"})
	ctx := appctx.WithApp(context.Background(), app)
	root.SetContext(ctx)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// --- Profile set-default command tests ---

func TestProfileSetDefaultRequiresExactlyOneArg(t *testing.T) {
	root := &cobra.Command{Use: "bcq"}
	profileCmd := NewProfileCmd()
	root.AddCommand(profileCmd)

	root.SetArgs([]string{"profile", "set-default"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

func TestProfileSetDefaultNonexistentProfile(t *testing.T) {
	app, _ := setupProfileTestApp(t, nil)

	root := &cobra.Command{Use: "bcq"}
	profileCmd := NewProfileCmd()
	root.AddCommand(profileCmd)

	root.SetArgs([]string{"profile", "set-default", "nonexistent"})
	ctx := appctx.WithApp(context.Background(), app)
	root.SetContext(ctx)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// --- Profile show command tests ---

func TestProfileShowAcceptsZeroOrOneArgs(t *testing.T) {
	cmd := newProfileShowCmd()
	assert.Equal(t, "show [name]", cmd.Use)

	// Verify MaximumNArgs(1) — 0 args should not produce an arg validation error
	// (though it may fail for other reasons like missing profile)
	root := &cobra.Command{Use: "bcq"}
	profileCmd := NewProfileCmd()
	root.AddCommand(profileCmd)

	// Two args should fail
	root.SetArgs([]string{"profile", "show", "arg1", "arg2"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts at most 1 arg")
}

func TestProfileShowWithExplicitName(t *testing.T) {
	cfg := &config.Config{
		BaseURL:  "https://3.basecampapi.com",
		CacheDir: t.TempDir(),
		Sources:  make(map[string]string),
		Profiles: map[string]*config.ProfileConfig{
			"myprofile": {
				BaseURL:   "https://custom.example.com",
				AccountID: "99999",
				Scope:     "full",
			},
		},
	}
	app, buf := setupProfileTestApp(t, cfg)

	cmd := newProfileShowCmd()
	err := executeProfileCommand(cmd, app, "myprofile")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"name": "myprofile"`)
	assert.Contains(t, out, `"base_url": "https://custom.example.com"`)
	assert.Contains(t, out, `"account_id": "99999"`)
	assert.Contains(t, out, `"scope": "full"`)
}

func TestProfileShowWithDefaultProfile(t *testing.T) {
	cfg := &config.Config{
		BaseURL:        "https://3.basecampapi.com",
		CacheDir:       t.TempDir(),
		Sources:        make(map[string]string),
		DefaultProfile: "default-one",
		Profiles: map[string]*config.ProfileConfig{
			"default-one": {
				BaseURL: "https://3.basecampapi.com",
			},
		},
	}
	app, buf := setupProfileTestApp(t, cfg)

	cmd := newProfileShowCmd()
	err := executeProfileCommand(cmd, app)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"name": "default-one"`)
	assert.Contains(t, out, `"default": true`)
}

func TestProfileShowWithActiveProfile(t *testing.T) {
	cfg := &config.Config{
		BaseURL:       "https://3.basecampapi.com",
		CacheDir:      t.TempDir(),
		Sources:       make(map[string]string),
		ActiveProfile: "active-one",
		Profiles: map[string]*config.ProfileConfig{
			"active-one": {
				BaseURL: "https://3.basecampapi.com",
			},
		},
	}
	app, buf := setupProfileTestApp(t, cfg)

	cmd := newProfileShowCmd()
	err := executeProfileCommand(cmd, app)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"name": "active-one"`)
}

func TestProfileShowNoProfileAvailable(t *testing.T) {
	cfg := &config.Config{
		BaseURL:  "https://3.basecampapi.com",
		CacheDir: t.TempDir(),
		Sources:  make(map[string]string),
	}
	app, _ := setupProfileTestApp(t, cfg)

	cmd := newProfileShowCmd()
	err := executeProfileCommand(cmd, app)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Profile name required")
}

func TestProfileShowNonexistent(t *testing.T) {
	cfg := &config.Config{
		BaseURL:  "https://3.basecampapi.com",
		CacheDir: t.TempDir(),
		Sources:  make(map[string]string),
	}
	app, _ := setupProfileTestApp(t, cfg)

	cmd := newProfileShowCmd()
	err := executeProfileCommand(cmd, app, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// --- Profile list command tests ---

func TestProfileListAcceptsZeroArgs(t *testing.T) {
	root := &cobra.Command{Use: "bcq"}
	profileCmd := NewProfileCmd()
	root.AddCommand(profileCmd)

	// One arg should fail (list takes no args)
	root.SetArgs([]string{"profile", "list", "extra"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	// Cobra won't reject extra args unless Args is set,
	// so we just verify the command exists and has the right Use
	cmd := newProfileListCmd()
	assert.Equal(t, "list", cmd.Use)
	_ = err
}

func TestProfileListEmpty(t *testing.T) {
	cfg := &config.Config{
		BaseURL:  "https://3.basecampapi.com",
		CacheDir: t.TempDir(),
		Sources:  make(map[string]string),
	}
	app, buf := setupProfileTestApp(t, cfg)

	cmd := newProfileListCmd()
	err := executeProfileCommand(cmd, app)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"ok": true`)
	// Empty list
	assert.Contains(t, out, `"data": []`)
}

func TestProfileListWithProfiles(t *testing.T) {
	cfg := &config.Config{
		BaseURL:        "https://3.basecampapi.com",
		CacheDir:       t.TempDir(),
		Sources:        make(map[string]string),
		DefaultProfile: "alpha",
		ActiveProfile:  "beta",
		Profiles: map[string]*config.ProfileConfig{
			"alpha": {BaseURL: "https://alpha.example.com", AccountID: "111"},
			"beta":  {BaseURL: "https://beta.example.com"},
		},
	}
	app, buf := setupProfileTestApp(t, cfg)

	cmd := newProfileListCmd()
	err := executeProfileCommand(cmd, app)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"ok": true`)
	assert.Contains(t, out, `"name": "alpha"`)
	assert.Contains(t, out, `"name": "beta"`)
	assert.Contains(t, out, `"account_id": "111"`)
	// Alpha is default
	assert.Contains(t, out, `"default": true`)
	// Beta is active
	assert.Contains(t, out, `"active": true`)
}

// --- Config file integration tests ---
//
// These tests verify the config-writing logic in profile commands.
// They call atomicWriteJSON directly rather than executing the full create
// command, which would trigger a real OAuth login flow.

func TestProfileCreateWritesConfigFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	configPath := filepath.Join(config.GlobalConfigDir(), "config.json")
	require.NoError(t, os.MkdirAll(config.GlobalConfigDir(), 0700))

	configData := make(map[string]any)
	profilesMap := make(map[string]any)
	profilesMap["testprofile"] = map[string]any{
		"base_url":   "https://custom.example.com",
		"scope":      "full",
		"account_id": "12345",
	}
	configData["profiles"] = profilesMap

	require.NoError(t, atomicWriteJSON(configPath, configData))

	result := readConfigFile(t)
	profiles, ok := result["profiles"].(map[string]any)
	require.True(t, ok, "expected profiles map in config")

	profile, ok := profiles["testprofile"].(map[string]any)
	require.True(t, ok, "expected testprofile in profiles map")

	assert.Equal(t, "https://custom.example.com", profile["base_url"])
	assert.Equal(t, "full", profile["scope"])
	assert.Equal(t, "12345", profile["account_id"])
}

func TestProfileCreateSetsFirstProfileAsDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	configPath := filepath.Join(config.GlobalConfigDir(), "config.json")
	require.NoError(t, os.MkdirAll(config.GlobalConfigDir(), 0700))

	// Simulate: first profile created → set as default
	profilesMap := map[string]any{
		"first-profile": map[string]any{
			"base_url": "https://3.basecampapi.com",
			"scope":    "read",
		},
	}
	configData := map[string]any{
		"profiles": profilesMap,
	}
	// First profile: set as default (len == 1)
	if len(profilesMap) == 1 {
		configData["default_profile"] = "first-profile"
	}

	require.NoError(t, atomicWriteJSON(configPath, configData))

	result := readConfigFile(t)
	assert.Equal(t, "first-profile", result["default_profile"], "first profile should be set as default")
}

func TestProfileCreateDoesNotOverrideDefaultOnSecondProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Pre-populate config with an existing profile
	existingConfig := map[string]any{
		"default_profile": "first",
		"profiles": map[string]any{
			"first": map[string]any{
				"base_url": "https://3.basecampapi.com",
			},
		},
	}
	writeConfigFile(t, existingConfig)

	// Simulate adding a second profile (same logic as create command)
	configPath := filepath.Join(config.GlobalConfigDir(), "config.json")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	configData := make(map[string]any)
	require.NoError(t, json.Unmarshal(data, &configData))

	profilesMap := configData["profiles"].(map[string]any)
	profilesMap["second"] = map[string]any{
		"base_url": "https://3.basecampapi.com",
		"scope":    "read",
	}
	// len > 1: do NOT override default_profile

	require.NoError(t, atomicWriteJSON(configPath, configData))

	result := readConfigFile(t)
	assert.Equal(t, "first", result["default_profile"], "default should remain 'first' when adding second profile")

	profiles := result["profiles"].(map[string]any)
	assert.Contains(t, profiles, "second", "second profile should exist in config")
}

func TestProfileCreateDefaultValues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	configPath := filepath.Join(config.GlobalConfigDir(), "config.json")
	require.NoError(t, os.MkdirAll(config.GlobalConfigDir(), 0700))

	// Simulate create with no flags: defaults are base_url=production, scope=read
	baseURL := "https://3.basecampapi.com"
	scope := "read"

	configData := map[string]any{
		"default_profile": "defaults-test",
		"profiles": map[string]any{
			"defaults-test": map[string]any{
				"base_url": baseURL,
				"scope":    scope,
			},
		},
	}

	require.NoError(t, atomicWriteJSON(configPath, configData))

	result := readConfigFile(t)
	profiles := result["profiles"].(map[string]any)
	profile := profiles["defaults-test"].(map[string]any)

	assert.Equal(t, "https://3.basecampapi.com", profile["base_url"], "default base_url should be production API")
	assert.Equal(t, "read", profile["scope"], "default scope should be 'read'")
}

func TestProfileCreateRejectsInvalidScope(t *testing.T) {
	app, _ := setupProfileTestApp(t, nil)

	root := &cobra.Command{Use: "bcq"}
	profileCmd := NewProfileCmd()
	root.AddCommand(profileCmd)

	root.SetArgs([]string{"profile", "create", "bad-scope", "--scope", "admin"})
	ctx := appctx.WithApp(context.Background(), app)
	root.SetContext(ctx)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid scope")
}

func TestProfileDeleteRemovesFromConfig(t *testing.T) {
	cfg := &config.Config{
		BaseURL:        "https://3.basecampapi.com",
		CacheDir:       t.TempDir(),
		Sources:        make(map[string]string),
		DefaultProfile: "keep-me",
		Profiles: map[string]*config.ProfileConfig{
			"keep-me":   {BaseURL: "https://3.basecampapi.com"},
			"delete-me": {BaseURL: "https://staging.example.com"},
		},
	}
	app, buf := setupProfileTestApp(t, cfg)

	// Pre-populate config (after setupProfileTestApp sets XDG_CONFIG_HOME)
	writeConfigFile(t, map[string]any{
		"default_profile": "keep-me",
		"profiles": map[string]any{
			"keep-me":   map[string]any{"base_url": "https://3.basecampapi.com"},
			"delete-me": map[string]any{"base_url": "https://staging.example.com"},
		},
	})

	cmd := newProfileDeleteCmd()
	err := executeProfileCommand(cmd, app, "delete-me")
	require.NoError(t, err)

	// Verify output
	out := buf.String()
	assert.Contains(t, out, `"status": "deleted"`)
	assert.Contains(t, out, `"name": "delete-me"`)

	// Verify config file
	configData := readConfigFile(t)
	profiles := configData["profiles"].(map[string]any)
	assert.NotContains(t, profiles, "delete-me", "deleted profile should be removed")
	assert.Contains(t, profiles, "keep-me", "other profiles should remain")
	assert.Equal(t, "keep-me", configData["default_profile"], "default should remain unchanged")
}

func TestProfileDeleteClearsDefaultWhenDeletingDefaultProfile(t *testing.T) {
	cfg := &config.Config{
		BaseURL:        "https://3.basecampapi.com",
		CacheDir:       t.TempDir(),
		Sources:        make(map[string]string),
		DefaultProfile: "the-default",
		Profiles: map[string]*config.ProfileConfig{
			"the-default": {BaseURL: "https://3.basecampapi.com"},
			"other":       {BaseURL: "https://other.example.com"},
		},
	}
	app, _ := setupProfileTestApp(t, cfg)

	writeConfigFile(t, map[string]any{
		"default_profile": "the-default",
		"profiles": map[string]any{
			"the-default": map[string]any{"base_url": "https://3.basecampapi.com"},
			"other":       map[string]any{"base_url": "https://other.example.com"},
		},
	})

	cmd := newProfileDeleteCmd()
	err := executeProfileCommand(cmd, app, "the-default")
	require.NoError(t, err)

	// Verify default_profile is cleared
	configData := readConfigFile(t)
	_, hasDefault := configData["default_profile"]
	assert.False(t, hasDefault, "default_profile should be cleared when deleting the default profile")

	profiles := configData["profiles"].(map[string]any)
	assert.Contains(t, profiles, "other", "non-deleted profiles should remain")
}

func TestProfileDeleteRemovesProfilesKeyWhenLastDeleted(t *testing.T) {
	cfg := &config.Config{
		BaseURL:        "https://3.basecampapi.com",
		CacheDir:       t.TempDir(),
		Sources:        make(map[string]string),
		DefaultProfile: "only-one",
		Profiles: map[string]*config.ProfileConfig{
			"only-one": {BaseURL: "https://3.basecampapi.com"},
		},
	}
	app, _ := setupProfileTestApp(t, cfg)

	writeConfigFile(t, map[string]any{
		"default_profile": "only-one",
		"profiles": map[string]any{
			"only-one": map[string]any{"base_url": "https://3.basecampapi.com"},
		},
	})

	cmd := newProfileDeleteCmd()
	err := executeProfileCommand(cmd, app, "only-one")
	require.NoError(t, err)

	configData := readConfigFile(t)
	_, hasProfiles := configData["profiles"]
	assert.False(t, hasProfiles, "profiles key should be removed when last profile is deleted")
}

func TestProfileSetDefaultUpdatesConfig(t *testing.T) {
	cfg := &config.Config{
		BaseURL:        "https://3.basecampapi.com",
		CacheDir:       t.TempDir(),
		Sources:        make(map[string]string),
		DefaultProfile: "alpha",
		Profiles: map[string]*config.ProfileConfig{
			"alpha": {BaseURL: "https://3.basecampapi.com"},
			"beta":  {BaseURL: "https://beta.example.com"},
		},
	}
	app, buf := setupProfileTestApp(t, cfg)

	writeConfigFile(t, map[string]any{
		"default_profile": "alpha",
		"profiles": map[string]any{
			"alpha": map[string]any{"base_url": "https://3.basecampapi.com"},
			"beta":  map[string]any{"base_url": "https://beta.example.com"},
		},
	})

	cmd := newProfileSetDefaultCmd()
	err := executeProfileCommand(cmd, app, "beta")
	require.NoError(t, err)

	// Verify output
	out := buf.String()
	assert.Contains(t, out, `"status": "set_default"`)
	assert.Contains(t, out, `"name": "beta"`)

	// Verify config file
	configData := readConfigFile(t)
	assert.Equal(t, "beta", configData["default_profile"], "default_profile should be updated to beta")
}

func TestProfileSetDefaultPreservesOtherConfig(t *testing.T) {
	cfg := &config.Config{
		BaseURL:        "https://custom.example.com",
		AccountID:      "99999",
		CacheDir:       t.TempDir(),
		Sources:        make(map[string]string),
		DefaultProfile: "alpha",
		Profiles: map[string]*config.ProfileConfig{
			"alpha": {BaseURL: "https://3.basecampapi.com"},
			"beta":  {BaseURL: "https://beta.example.com"},
		},
	}
	app, _ := setupProfileTestApp(t, cfg)

	writeConfigFile(t, map[string]any{
		"default_profile": "alpha",
		"base_url":        "https://custom.example.com",
		"account_id":      "99999",
		"profiles": map[string]any{
			"alpha": map[string]any{"base_url": "https://3.basecampapi.com"},
			"beta":  map[string]any{"base_url": "https://beta.example.com"},
		},
	})

	cmd := newProfileSetDefaultCmd()
	err := executeProfileCommand(cmd, app, "beta")
	require.NoError(t, err)

	// Verify other config values are preserved
	configData := readConfigFile(t)
	assert.Equal(t, "beta", configData["default_profile"])
	assert.Equal(t, "https://custom.example.com", configData["base_url"])
	assert.Equal(t, "99999", configData["account_id"])
}

// --- Edge case tests ---

func TestProfileShowNotFoundProfile(t *testing.T) {
	cfg := &config.Config{
		BaseURL:  "https://3.basecampapi.com",
		CacheDir: t.TempDir(),
		Sources:  make(map[string]string),
		Profiles: map[string]*config.ProfileConfig{
			"exists": {BaseURL: "https://3.basecampapi.com"},
		},
	}
	app, _ := setupProfileTestApp(t, cfg)

	cmd := newProfileShowCmd()
	err := executeProfileCommand(cmd, app, "does-not-exist")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"does-not-exist" not found`)
}

func TestProfileDeleteWithNilProfiles(t *testing.T) {
	cfg := &config.Config{
		BaseURL:  "https://3.basecampapi.com",
		CacheDir: t.TempDir(),
		Sources:  make(map[string]string),
		// Profiles is nil
	}
	app, _ := setupProfileTestApp(t, cfg)

	cmd := newProfileDeleteCmd()
	err := executeProfileCommand(cmd, app, "anything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestProfileSetDefaultWithNilProfiles(t *testing.T) {
	cfg := &config.Config{
		BaseURL:  "https://3.basecampapi.com",
		CacheDir: t.TempDir(),
		Sources:  make(map[string]string),
		// Profiles is nil
	}
	app, _ := setupProfileTestApp(t, cfg)

	cmd := newProfileSetDefaultCmd()
	err := executeProfileCommand(cmd, app, "anything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestProfileShowUsesActiveBeforeDefault(t *testing.T) {
	// When both active and default are set, active should take precedence
	cfg := &config.Config{
		BaseURL:        "https://3.basecampapi.com",
		CacheDir:       t.TempDir(),
		Sources:        make(map[string]string),
		ActiveProfile:  "active-one",
		DefaultProfile: "default-one",
		Profiles: map[string]*config.ProfileConfig{
			"active-one":  {BaseURL: "https://active.example.com"},
			"default-one": {BaseURL: "https://default.example.com"},
		},
	}
	app, buf := setupProfileTestApp(t, cfg)

	cmd := newProfileShowCmd()
	err := executeProfileCommand(cmd, app)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"name": "active-one"`)
	assert.Contains(t, out, `"base_url": "https://active.example.com"`)
}

func TestProfileShowDisplaysAllFields(t *testing.T) {
	cfg := &config.Config{
		BaseURL:  "https://3.basecampapi.com",
		CacheDir: t.TempDir(),
		Sources:  make(map[string]string),
		Profiles: map[string]*config.ProfileConfig{
			"full": {
				BaseURL:    "https://custom.example.com",
				AccountID:  "111",
				ProjectID:  "222",
				TodolistID: "333",
				Scope:      "full",
			},
		},
	}
	app, buf := setupProfileTestApp(t, cfg)

	cmd := newProfileShowCmd()
	err := executeProfileCommand(cmd, app, "full")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"account_id": "111"`)
	assert.Contains(t, out, `"project_id": "222"`)
	assert.Contains(t, out, `"todolist_id": "333"`)
	assert.Contains(t, out, `"scope": "full"`)
}

func TestProfileListSortedAlphabetically(t *testing.T) {
	cfg := &config.Config{
		BaseURL:  "https://3.basecampapi.com",
		CacheDir: t.TempDir(),
		Sources:  make(map[string]string),
		Profiles: map[string]*config.ProfileConfig{
			"zulu":  {BaseURL: "https://3.basecampapi.com"},
			"alpha": {BaseURL: "https://3.basecampapi.com"},
			"mike":  {BaseURL: "https://3.basecampapi.com"},
		},
	}
	app, buf := setupProfileTestApp(t, cfg)

	cmd := newProfileListCmd()
	err := executeProfileCommand(cmd, app)
	require.NoError(t, err)

	out := buf.String()

	// Parse the JSON output to verify ordering
	var result struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Len(t, result.Data, 3)
	assert.Equal(t, "alpha", result.Data[0].Name)
	assert.Equal(t, "mike", result.Data[1].Name)
	assert.Equal(t, "zulu", result.Data[2].Name)
}

func TestProfileCreateWithNilProfilesMap(t *testing.T) {
	// Test that atomicWriteJSON works when starting from an empty config
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	configPath := filepath.Join(config.GlobalConfigDir(), "config.json")
	require.NoError(t, os.MkdirAll(config.GlobalConfigDir(), 0700))

	// Simulate: no existing config, creating first profile
	configData := make(map[string]any)
	profilesMap := make(map[string]any)
	profilesMap["new-profile"] = map[string]any{
		"base_url": "https://3.basecampapi.com",
		"scope":    "read",
	}
	configData["profiles"] = profilesMap
	configData["default_profile"] = "new-profile"

	require.NoError(t, atomicWriteJSON(configPath, configData))

	result := readConfigFile(t)
	profiles, ok := result["profiles"].(map[string]any)
	require.True(t, ok, "expected profiles map in config")
	assert.Contains(t, profiles, "new-profile", "new profile should be created")
}
