package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/harness"
	"github.com/basecamp/basecamp-cli/internal/names"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/version"
)

func TestDoctorResultSummary(t *testing.T) {
	tests := []struct {
		name     string
		result   DoctorResult
		expected string
	}{
		{
			name: "all passed",
			result: DoctorResult{
				Passed: 5,
			},
			expected: "All 5 checks passed",
		},
		{
			name: "some failed",
			result: DoctorResult{
				Passed: 3,
				Failed: 2,
			},
			expected: "3 passed, 2 failed",
		},
		{
			name: "with warnings",
			result: DoctorResult{
				Passed: 4,
				Warned: 1,
			},
			expected: "4 passed, 1 warning",
		},
		{
			name: "with multiple warnings",
			result: DoctorResult{
				Passed: 4,
				Warned: 3,
			},
			expected: "4 passed, 3 warnings",
		},
		{
			name: "mixed results",
			result: DoctorResult{
				Passed:  3,
				Failed:  1,
				Warned:  1,
				Skipped: 2,
			},
			expected: "3 passed, 1 failed, 1 warning, 2 skipped",
		},
		{
			name: "only skipped",
			result: DoctorResult{
				Skipped: 3,
			},
			expected: "3 skipped",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.result.Summary())
		})
	}
}

func TestSummarizeChecks(t *testing.T) {
	checks := []Check{
		{Name: "Check1", Status: "pass"},
		{Name: "Check2", Status: "pass"},
		{Name: "Check3", Status: "fail"},
		{Name: "Check4", Status: "warn"},
		{Name: "Check5", Status: "skip"},
		{Name: "Check6", Status: "skip"},
	}

	result := summarizeChecks(checks)

	assert.Equal(t, 2, result.Passed)
	assert.Equal(t, 1, result.Failed)
	assert.Equal(t, 1, result.Warned)
	assert.Equal(t, 2, result.Skipped)
	assert.Len(t, result.Checks, 6)
}

func TestCheckVersion(t *testing.T) {
	// Non-verbose
	check := checkVersion(false)
	assert.Equal(t, "CLI Version", check.Name)
	assert.Equal(t, "pass", check.Status)
	assert.Contains(t, check.Message, "dev")

	// Verbose includes commit info
	checkVerbose := checkVersion(true)
	assert.Contains(t, checkVerbose.Message, "commit")
}

func TestCheckSDKProvenance(t *testing.T) {
	// Non-verbose: shows version string (works for both pseudo-versions and semver)
	check := checkSDKProvenance(false)
	assert.Equal(t, "SDK", check.Name)
	assert.Equal(t, "pass", check.Status)
	assert.NotEmpty(t, check.Message)
	assert.Regexp(t, `v\d+\.\d+\.\d+`, check.Message)

	// Verbose: shows detailed info
	checkVerbose := checkSDKProvenance(true)
	assert.Equal(t, "pass", checkVerbose.Status)
	assert.NotEmpty(t, checkVerbose.Message)
}

func TestFormatSDKProvenanceNil(t *testing.T) {
	check := formatSDKProvenance(nil, false)
	assert.Equal(t, "SDK", check.Name)
	assert.Equal(t, "warn", check.Status)
	assert.Equal(t, "Provenance data unavailable", check.Message)

	// Verbose nil also warns
	checkVerbose := formatSDKProvenance(nil, true)
	assert.Equal(t, "warn", checkVerbose.Status)
	assert.Equal(t, "Provenance data unavailable", checkVerbose.Message)
}

func TestFormatSDKProvenanceVersionOnly(t *testing.T) {
	p := &version.SDKProvenance{}
	p.SDK.Version = "v1.0.0"
	// No revision set

	check := formatSDKProvenance(p, false)
	assert.Equal(t, "pass", check.Status)
	assert.Equal(t, "v1.0.0", check.Message)

	checkVerbose := formatSDKProvenance(p, true)
	assert.Equal(t, "pass", checkVerbose.Status)
	assert.Equal(t, "v1.0.0", checkVerbose.Message)
}

func TestFormatSDKProvenanceEmptyVersion(t *testing.T) {
	p := &version.SDKProvenance{}
	// Version is empty

	check := formatSDKProvenance(p, false)
	assert.Equal(t, "warn", check.Status)
	assert.Contains(t, check.Message, "missing version")

	checkVerbose := formatSDKProvenance(p, true)
	assert.Equal(t, "warn", checkVerbose.Status)
	assert.Contains(t, checkVerbose.Message, "missing version")
}

func TestFormatSDKProvenanceUpdatedAtWithoutRevision(t *testing.T) {
	p := &version.SDKProvenance{}
	p.SDK.Version = "v1.0.0"
	p.SDK.UpdatedAt = "2026-02-05T08:16:32Z"
	// No revision set

	checkVerbose := formatSDKProvenance(p, true)
	assert.Equal(t, "pass", checkVerbose.Status)
	assert.Contains(t, checkVerbose.Message, "updated: 2026-02-05")
	assert.NotContains(t, checkVerbose.Message, "revision")
}

func TestFormatSDKProvenanceWithRevision(t *testing.T) {
	p := &version.SDKProvenance{}
	p.SDK.Version = "v0.0.0-20260205081632-0362dcaf3950"
	p.SDK.Revision = "0362dcaf3950"
	p.SDK.UpdatedAt = "2026-02-05T08:16:32Z"

	check := formatSDKProvenance(p, false)
	assert.Equal(t, "pass", check.Status)
	assert.Equal(t, "v0.0.0-20260205081632-0362dcaf3950 (0362dcaf3950)", check.Message)

	checkVerbose := formatSDKProvenance(p, true)
	assert.Equal(t, "pass", checkVerbose.Status)
	assert.Contains(t, checkVerbose.Message, "revision: 0362dcaf3950")
	assert.Contains(t, checkVerbose.Message, "updated: 2026-02-05")
}

func TestDetectShell(t *testing.T) {
	// Save and restore SHELL env
	originalShell := os.Getenv("SHELL")
	defer os.Setenv("SHELL", originalShell)

	tests := []struct {
		shell    string
		expected string
	}{
		{"/bin/bash", "bash"},
		{"/bin/zsh", "zsh"},
		{"/usr/bin/fish", "fish"},
		{"/bin/sh", ""},   // Not supported
		{"/bin/tcsh", ""}, // Not supported
	}

	for _, tt := range tests {
		t.Run(tt.shell, func(t *testing.T) {
			os.Setenv("SHELL", tt.shell)
			result := detectShell()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFindRepoConfig(t *testing.T) {
	// Create a temp directory structure with git repo
	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	gitDir := filepath.Join(tmpDir, ".git")
	require.NoError(t, os.Mkdir(gitDir, 0755))

	// No config initially
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tmpDir)

	result := findRepoConfig()
	assert.Empty(t, result, "should not find config when none exists")

	// Create .basecamp/config.json
	basecampDir := filepath.Join(tmpDir, ".basecamp")
	require.NoError(t, os.Mkdir(basecampDir, 0755))
	configPath := filepath.Join(basecampDir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"project_id": "123"}`), 0644))

	result = findRepoConfig()
	assert.Equal(t, configPath, result, "should find repo config")
}

func TestValidateConfigFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Valid JSON
	validPath := filepath.Join(tmpDir, "valid.json")
	require.NoError(t, os.WriteFile(validPath, []byte(`{"key": "value"}`), 0644))

	check := validateConfigFile(validPath, "Test Config", false)
	assert.Equal(t, "pass", check.Status)
	assert.Equal(t, validPath, check.Message)

	// Verbose shows key count
	checkVerbose := validateConfigFile(validPath, "Test Config", true)
	assert.Contains(t, checkVerbose.Message, "1 keys")

	// Invalid JSON
	invalidPath := filepath.Join(tmpDir, "invalid.json")
	require.NoError(t, os.WriteFile(invalidPath, []byte(`{invalid`), 0644))

	checkInvalid := validateConfigFile(invalidPath, "Test Config", false)
	assert.Equal(t, "fail", checkInvalid.Status)
	assert.Contains(t, checkInvalid.Message, "Invalid JSON")

	// Non-existent file
	checkMissing := validateConfigFile(filepath.Join(tmpDir, "missing.json"), "Test Config", false)
	assert.Equal(t, "fail", checkMissing.Status)
	assert.Contains(t, checkMissing.Message, "Cannot read")
}

func TestBuildDoctorBreadcrumbs(t *testing.T) {
	checks := []Check{
		{Name: "Credentials", Status: "fail"},
		{Name: "Authentication", Status: "fail"},
		{Name: "API Connectivity", Status: "pass"},
	}

	breadcrumbs := buildDoctorBreadcrumbs(checks)

	// Should have one breadcrumb for auth login (deduplicated)
	assert.Len(t, breadcrumbs, 1)
	assert.Equal(t, "basecamp auth login", breadcrumbs[0].Cmd)
}

func TestBuildDoctorBreadcrumbsDeduplication(t *testing.T) {
	// Both Credentials and Authentication fail - should only suggest login once
	checks := []Check{
		{Name: "Credentials", Status: "fail"},
		{Name: "Authentication", Status: "fail"},
	}

	breadcrumbs := buildDoctorBreadcrumbs(checks)
	assert.Len(t, breadcrumbs, 1, "should deduplicate identical suggestions")
}

func TestBuildDoctorBreadcrumbsNoFailures(t *testing.T) {
	checks := []Check{
		{Name: "Credentials", Status: "pass"},
		{Name: "Authentication", Status: "pass"},
	}

	breadcrumbs := buildDoctorBreadcrumbs(checks)
	assert.Empty(t, breadcrumbs, "should have no breadcrumbs when all pass")
}

// setupDoctorTestApp creates a minimal test app for doctor command tests.
func setupDoctorTestApp(t *testing.T, accountID string) (*appctx.App, *bytes.Buffer) {
	t.Helper()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: accountID,
		BaseURL:   "https://3.basecampapi.com",
		CacheDir:  t.TempDir(),
		Sources:   make(map[string]string),
	}

	authMgr := auth.NewManager(cfg, nil)

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, nil,
		basecamp.WithMaxRetries(0),
	)
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

func executeDoctorCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd.Execute()
}

func TestDoctorCommandCreation(t *testing.T) {
	cmd := NewDoctorCmd()
	assert.Equal(t, "doctor", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.NotEmpty(t, cmd.Long)
}

func TestDoctorCommandWithNoAuth(t *testing.T) {
	app, buf := setupDoctorTestApp(t, "12345")

	cmd := NewDoctorCmd()
	err := executeDoctorCommand(cmd, app)
	require.NoError(t, err)

	// Should have output JSON
	output := buf.String()
	assert.Contains(t, output, `"ok": true`)
	assert.Contains(t, output, `"checks"`)
	// Credentials should fail
	assert.Contains(t, output, `"No credentials found"`)
}

func TestCheckCacheHealth(t *testing.T) {
	tmpDir := t.TempDir()

	app := &appctx.App{
		Config: &config.Config{
			CacheDir:     tmpDir,
			CacheEnabled: true,
		},
	}

	// Empty cache dir
	check := checkCacheHealth(app, false)
	assert.Equal(t, "pass", check.Status)

	// Create some cache files
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "file1.cache"), []byte("data"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "file2.cache"), []byte("more data"), 0644))

	checkWithFiles := checkCacheHealth(app, true)
	assert.Equal(t, "pass", checkWithFiles.Status)
	assert.Contains(t, checkWithFiles.Message, "2 entries")
}

func TestCheckAccountAccessInvalidAccountID(t *testing.T) {
	app, _ := setupDoctorTestApp(t, "not-a-number")

	check := checkAccountAccess(context.Background(), app, false)
	assert.Equal(t, "Account Access", check.Name)
	assert.Equal(t, "fail", check.Status)
	assert.Equal(t, "Invalid account configuration", check.Message)
	assert.NotEmpty(t, check.Hint)
}

func TestCheckAccountAccessEmptyAccountID(t *testing.T) {
	app, _ := setupDoctorTestApp(t, "")

	check := checkAccountAccess(context.Background(), app, false)
	assert.Equal(t, "Account Access", check.Name)
	assert.Equal(t, "fail", check.Status)
	assert.Equal(t, "Invalid account configuration", check.Message)
}

func TestCheckCacheHealthDisabled(t *testing.T) {
	app := &appctx.App{
		Config: &config.Config{
			CacheDir:     "/some/path",
			CacheEnabled: false,
		},
	}

	check := checkCacheHealth(app, false)
	assert.Equal(t, "pass", check.Status)
	assert.Equal(t, "Disabled", check.Message)
}

func TestCheckLegacyInstall_DetectsLegacyCache(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	cacheBase := t.TempDir()
	configBase := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheBase)
	t.Setenv("XDG_CONFIG_HOME", configBase)

	// Create legacy cache dir
	require.NoError(t, os.MkdirAll(filepath.Join(cacheBase, "bcq"), 0700))

	check := checkLegacyInstall()
	require.NotNil(t, check, "should detect legacy cache dir")
	assert.Equal(t, "warn", check.Status)
	assert.Contains(t, check.Message, filepath.Join(cacheBase, "bcq"))
	assert.Contains(t, check.Hint, "basecamp migrate")
}

func TestCheckLegacyInstall_DetectsLegacyTheme(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	cacheBase := t.TempDir()
	configBase := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheBase)
	t.Setenv("XDG_CONFIG_HOME", configBase)

	// Create legacy theme dir
	require.NoError(t, os.MkdirAll(filepath.Join(configBase, "bcq", "theme"), 0700))

	check := checkLegacyInstall()
	require.NotNil(t, check, "should detect legacy theme dir")
	assert.Equal(t, "warn", check.Status)
	assert.Contains(t, check.Message, filepath.Join(configBase, "bcq", "theme"))
}

func TestCheckLegacyInstall_DetectsBothArtifacts(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	cacheBase := t.TempDir()
	configBase := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheBase)
	t.Setenv("XDG_CONFIG_HOME", configBase)

	require.NoError(t, os.MkdirAll(filepath.Join(cacheBase, "bcq"), 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(configBase, "bcq", "theme"), 0700))

	check := checkLegacyInstall()
	require.NotNil(t, check)
	assert.Contains(t, check.Message, "bcq")
	assert.Contains(t, check.Message, "theme")
}

func TestCheckLegacyInstall_NilWhenClean(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	check := checkLegacyInstall()
	assert.Nil(t, check, "should return nil when no legacy artifacts exist")
}

func TestCheckLegacyInstall_NilWhenAlreadyMigrated(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	cacheBase := t.TempDir()
	configBase := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheBase)
	t.Setenv("XDG_CONFIG_HOME", configBase)

	// Legacy artifacts exist
	require.NoError(t, os.MkdirAll(filepath.Join(cacheBase, "bcq"), 0700))

	// But migration marker is present
	markerDir := filepath.Join(configBase, "basecamp")
	require.NoError(t, os.MkdirAll(markerDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(markerDir, ".migrated"), []byte("migrated\n"), 0600))

	check := checkLegacyInstall()
	assert.Nil(t, check, "should return nil when .migrated marker exists")
}

func TestCheckClaudeIntegration(t *testing.T) {
	// Claude registers via init() in the harness package. Its Checks function
	// calls harness.CheckClaudePlugin which reads the plugin file.
	agent := harness.FindAgent("claude")
	require.NotNil(t, agent, "claude agent should be registered")
	require.NotNil(t, agent.Checks)

	checks := agent.Checks()
	require.NotEmpty(t, checks, "should return at least one check")
	assert.Equal(t, "Claude Code Plugin", checks[0].Name)
	// Status depends on environment — in CI there's no ~/.claude so it'll be "fail"
	assert.Contains(t, []string{"pass", "fail", "warn"}, checks[0].Status)
}

func TestCheckClaudeIntegrationIncludesSkillLink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home) // no claude binary

	// Create ~/.claude so the skill link check is included
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	agent := harness.FindAgent("claude")
	require.NotNil(t, agent)

	checks := agent.Checks()
	require.True(t, len(checks) >= 2, "expected at least plugin + skill checks, got %d", len(checks))

	// Find the skill check
	var skillCheck *harness.StatusCheck
	for _, c := range checks {
		if c.Name == "Claude Code Skill" {
			skillCheck = c
			break
		}
	}
	require.NotNil(t, skillCheck, "expected Claude Code Skill check")
	assert.Equal(t, "fail", skillCheck.Status, "skill link should fail when not present")

	// Now create the skill link and verify it passes
	skillDir := filepath.Join(home, ".claude", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("test"), 0o644))

	checks = agent.Checks()
	for _, c := range checks {
		if c.Name == "Claude Code Skill" {
			assert.Equal(t, "pass", c.Status, "skill link should pass when present")
		}
	}
}

func TestCheckSkillVersion_UpToDate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("skill"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".installed-version"), []byte(version.Version), 0o644))

	check := checkSkillVersion()
	assert.Equal(t, "pass", check.Status)
	if version.IsDev() {
		assert.Contains(t, check.Message, "dev build")
	} else {
		assert.Contains(t, check.Message, "Up to date")
	}
}

func TestCheckSkillVersion_Stale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	origVersion := version.Version
	version.Version = "2.0.0"
	defer func() { version.Version = origVersion }()

	dir := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("skill"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".installed-version"), []byte("1.0.0"), 0o644))

	check := checkSkillVersion()
	assert.Equal(t, "warn", check.Status)
	assert.Contains(t, check.Message, "Stale")
	assert.Contains(t, check.Message, "1.0.0")
	assert.Contains(t, check.Message, "2.0.0")
	assert.Contains(t, check.Hint, "basecamp skill install")
}

func TestCheckSkillVersion_Untracked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("skill"), 0o644))
	// No .installed-version file

	check := checkSkillVersion()
	assert.Equal(t, "pass", check.Status)
	assert.Contains(t, check.Message, "version not tracked")
}

func TestCheckSkillVersion_DevBuild(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	origVersion := version.Version
	version.Version = "dev"
	defer func() { version.Version = origVersion }()

	dir := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("skill"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".installed-version"), []byte("1.0.0"), 0o644))

	check := checkSkillVersion()
	assert.Equal(t, "pass", check.Status)
	assert.Contains(t, check.Message, "dev build")
	assert.Contains(t, check.Message, "1.0.0")
}

func TestBuildDoctorBreadcrumbs_SkillVersionWarn(t *testing.T) {
	checks := []Check{
		{Name: "Skill Version", Status: "warn"},
	}

	breadcrumbs := buildDoctorBreadcrumbs(checks)
	require.Len(t, breadcrumbs, 1)
	assert.Equal(t, "basecamp skill install", breadcrumbs[0].Cmd)
}

func TestCheckLegacyInstall_SkipsKeyringWhenNoKeyring(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// With BASECAMP_NO_KEYRING set, even if legacy keyring entries existed,
	// the function should not probe the keyring and should return nil
	check := checkLegacyInstall()
	assert.Nil(t, check)
}

func TestDoctorVerboseHidesLaunchpadScope(t *testing.T) {
	app, _ := setupDoctorTestApp(t, "12345")

	// Store Launchpad credentials with a legacy "read" scope
	store := app.Auth.GetStore()
	require.NoError(t, store.Save("https://3.basecampapi.com", &auth.Credentials{
		AccessToken: "tok",
		OAuthType:   "launchpad",
		Scope:       "read",
	}))

	check := checkCredentials(app, true)
	assert.Equal(t, "pass", check.Status)
	assert.NotContains(t, check.Message, "scope:", "Launchpad scope should not appear in verbose output")
	assert.Contains(t, check.Message, "type: launchpad")
}

func TestDoctorVerboseShowsBC3Scope(t *testing.T) {
	app, _ := setupDoctorTestApp(t, "12345")

	// Store BC3 credentials with scope
	store := app.Auth.GetStore()
	require.NoError(t, store.Save("https://3.basecampapi.com", &auth.Credentials{
		AccessToken: "tok",
		OAuthType:   "bc3",
		Scope:       "read",
	}))

	check := checkCredentials(app, true)
	assert.Equal(t, "pass", check.Status)
	assert.Contains(t, check.Message, "scope: read", "BC3 scope should appear in verbose output")
	assert.Contains(t, check.Message, "type: bc3")
}
