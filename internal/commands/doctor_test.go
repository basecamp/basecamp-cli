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

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/auth"
	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/names"
	"github.com/basecamp/bcq/internal/output"
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
	tmpDir := t.TempDir()
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
	assert.Equal(t, "bcq auth login", breadcrumbs[0].Cmd)
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
	t.Setenv("BCQ_NO_KEYRING", "1")

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
