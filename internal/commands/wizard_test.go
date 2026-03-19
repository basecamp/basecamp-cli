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

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/skills"
)

// TestIsFirstRunUnauthenticated verifies isFirstRun returns true for unauthenticated,
// non-TTY apps (isFirstRun also checks IsInteractive, which requires a real TTY).
// Since tests don't run in a TTY, isFirstRun returns false even when unauthenticated.
func TestIsFirstRunUnauthenticated(t *testing.T) {
	app, _ := setupQuickstartTestApp(t, "", "")

	// Not a TTY in test environment, so isFirstRun returns false
	assert.False(t, isFirstRun(app), "isFirstRun should be false in non-TTY test")
}

// TestIsFirstRunWithBasecampToken verifies isFirstRun returns false when BASECAMP_TOKEN is set.
func TestIsFirstRunWithBasecampToken(t *testing.T) {
	app, _ := setupQuickstartTestApp(t, "", "")
	t.Setenv("BASECAMP_TOKEN", "test-token-123")

	assert.False(t, isFirstRun(app), "isFirstRun should be false when BASECAMP_TOKEN is set")
}

// TestIsFirstRunAuthenticated verifies isFirstRun returns false when already authenticated.
func TestIsFirstRunAuthenticated(t *testing.T) {
	// BASECAMP_TOKEN makes IsAuthenticated() return true
	t.Setenv("BASECAMP_TOKEN", "test-token-123")
	app, _ := setupQuickstartTestApp(t, "12345", "")

	assert.False(t, isFirstRun(app), "isFirstRun should be false when authenticated")
}

// TestWizardResultJSON verifies the WizardResult struct serializes correctly.
func TestWizardResultJSON(t *testing.T) {
	app, buf := setupQuickstartTestApp(t, "", "")

	result := WizardResult{
		Version:     "1.0.0",
		Status:      "complete",
		AccountID:   "12345",
		AccountName: "Test Company",
		ProjectID:   "67890",
		ProjectName: "My Project",
		ConfigScope: "global",
	}

	err := app.OK(result, output.WithSummary("Setup complete"))
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"account_id": "12345"`)
	assert.Contains(t, out, `"project_id": "67890"`)
	assert.Contains(t, out, `"config_scope": "global"`)
}

// TestWizardSummaryLine verifies summary generation.
func TestWizardSummaryLine(t *testing.T) {
	tests := []struct {
		name     string
		result   WizardResult
		expected string
	}{
		{
			name:     "with account name",
			result:   WizardResult{AccountName: "Test Co"},
			expected: "Setup complete - Test Co",
		},
		{
			name:     "without account name",
			result:   WizardResult{AccountID: "123"},
			expected: "Setup complete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, wizardSummaryLine(tt.result))
		})
	}
}

// TestWizardBreadcrumbs verifies breadcrumb generation based on wizard outcome.
func TestWizardBreadcrumbs(t *testing.T) {
	t.Run("with project", func(t *testing.T) {
		result := WizardResult{ProjectID: "123"}
		crumbs := wizardBreadcrumbs(result)

		assert.True(t, len(crumbs) >= 2)
		assert.Equal(t, "list_projects", crumbs[0].Action)

		// Should have todos breadcrumb when project is set
		var hasTodos bool
		for _, c := range crumbs {
			if c.Action == "list_todos" {
				hasTodos = true
			}
		}
		assert.True(t, hasTodos, "expected list_todos breadcrumb when project is set")
	})

	t.Run("without project", func(t *testing.T) {
		result := WizardResult{}
		crumbs := wizardBreadcrumbs(result)

		// Should suggest setting a project
		var hasSetProject bool
		for _, c := range crumbs {
			if c.Action == "set_project" {
				hasSetProject = true
			}
		}
		assert.True(t, hasSetProject, "expected set_project breadcrumb when no project")
	})
}

// TestIsFirstRunOnboarded verifies isFirstRun returns false when onboarded flag is set.
func TestIsFirstRunOnboarded(t *testing.T) {
	app, _ := setupQuickstartTestApp(t, "", "")
	onboarded := true
	app.Config.Onboarded = &onboarded

	assert.False(t, isFirstRun(app), "isFirstRun should be false when onboarded is true")
}

// TestNewSetupCmd verifies the setup command is created correctly.
func TestNewSetupCmd(t *testing.T) {
	cmd := NewSetupCmd()
	assert.Equal(t, "setup", cmd.Use)
	assert.Contains(t, cmd.Short, "setup")
}

// TestNewSetupCmdHasClaudeSubcommand verifies setup has the claude subcommand.
func TestNewSetupCmdHasClaudeSubcommand(t *testing.T) {
	cmd := NewSetupCmd()

	var found bool
	for _, sub := range cmd.Commands() {
		if sub.Name() == "claude" {
			found = true
			break
		}
	}
	assert.True(t, found, "setup should have a 'claude' subcommand")
}

// TestSetupClaudeJSONOutputPurity verifies setup claude --json emits only
// valid JSON with no interleaved prose.
func TestSetupClaudeJSONOutputPurity(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir()) // no claude binary

	buf := &bytes.Buffer{}
	app, _ := setupQuickstartTestApp(t, "", "")
	app.Flags.JSON = true // makes IsInteractive() return false

	cmd := NewSetupCmd()
	cmd.SetArgs([]string{"claude"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(buf)
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	// The output buffer (app.Output) receives app.OK data;
	// cmd stdout (buf) should have no prose since IsInteractive is false.
	out := buf.String()
	if out != "" {
		// If anything landed on cmd stdout, it must be valid JSON
		assert.True(t, json.Valid([]byte(out)),
			"setup claude --json stdout should be empty or valid JSON, got: %s", out)
	}
}

// TestSetupClaudeSummaryStates verifies the three summary states.
func TestSetupClaudeSummaryStates(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir()) // no claude binary

	app, appBuf := setupQuickstartTestApp(t, "", "")
	app.Flags.JSON = true

	cmd := NewSetupCmd()
	cmd.SetArgs([]string{"claude"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	out := appBuf.String()

	// Parse the JSON envelope to check summary and data
	var envelope struct {
		Summary string         `json:"summary"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &envelope))

	assert.Contains(t, envelope.Data, "agent_detected")
	assert.Contains(t, envelope.Data, "plugin_installed")

	detected, _ := envelope.Data["agent_detected"].(bool)
	if !detected {
		assert.Equal(t, "Claude Code not detected", envelope.Summary)
	} else {
		installed, _ := envelope.Data["plugin_installed"].(bool)
		if installed {
			assert.Equal(t, "Claude Code plugin installed", envelope.Summary)
		} else {
			assert.Equal(t, "Claude Code plugin not installed", envelope.Summary)
		}
	}
}

// TestSetupClaudeNonInteractiveInstallsSkill verifies that setup claude --json
// installs the baseline skill even in non-interactive mode.
func TestSetupClaudeNonInteractiveInstallsSkill(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	home := t.TempDir()
	t.Setenv("HOME", home)
	// No claude binary on PATH, so the agent-specific steps are skipped,
	// but the baseline skill should still be installed.
	t.Setenv("PATH", home)

	app, _ := setupQuickstartTestApp(t, "", "")
	app.Flags.JSON = true

	cmd := NewSetupCmd()
	cmd.SetArgs([]string{"claude"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	// Baseline skill should exist
	skillFile := filepath.Join(home, ".agents", "skills", "basecamp", "SKILL.md")
	got, readErr := os.ReadFile(skillFile)
	require.NoError(t, readErr, "baseline skill file should be installed")
	embedded, readErr := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, readErr)
	assert.Equal(t, string(embedded), string(got))
}

// TestBaselineSkillInstalled verifies the helper function.
func TestBaselineSkillInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	assert.False(t, baselineSkillInstalled(), "should be false when skill not present")

	// Install it
	skillDir := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("test"), 0o644))

	assert.True(t, baselineSkillInstalled(), "should be true when SKILL.md exists")
}

// TestSetupClaudeNonInteractiveRepairsSkillLink verifies that non-interactive
// setup repairs a missing skill link even when the plugin is already installed
// and no claude binary is on PATH.
func TestSetupClaudeNonInteractiveRepairsSkillLink(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home) // no claude binary

	// Create ~/.claude with plugin installed
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", "plugins"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
		[]byte(`[{"name":"basecamp","version":"1.0.0"}]`), 0o644))

	app, appBuf := setupQuickstartTestApp(t, "", "")
	app.Flags.JSON = true

	cmd := NewSetupCmd()
	cmd.SetArgs([]string{"claude"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	// Skill link should exist after setup
	skillLinkPath := filepath.Join(home, ".claude", "skills", "basecamp", "SKILL.md")
	_, statErr := os.Stat(skillLinkPath)
	assert.NoError(t, statErr, "skill link should be repaired by non-interactive setup")

	// Result should report success since both checks now pass
	var envelope struct {
		Summary string         `json:"summary"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(appBuf.Bytes(), &envelope))
	installed, _ := envelope.Data["plugin_installed"].(bool)
	assert.True(t, installed, "plugin_installed should be true after successful repair")
}

// TestRunClaudeSetupRepairsSkillLink verifies that interactive setup repairs a
// missing skill link even when the plugin is already installed and no claude
// binary is on PATH.
func TestRunClaudeSetupRepairsSkillLink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home) // no claude binary

	// Plugin is installed (check will pass)
	pluginDir := filepath.Join(home, ".claude", "plugins")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "installed_plugins.json"),
		[]byte(`[{"name":"basecamp","version":"1.0.0"}]`), 0o644))

	// Install baseline skill files (source for the symlink)
	_, err := installSkillFiles()
	require.NoError(t, err)

	// Skill link does NOT exist yet
	skillLinkPath := filepath.Join(home, ".claude", "skills", "basecamp", "SKILL.md")
	_, statErr := os.Stat(skillLinkPath)
	require.True(t, os.IsNotExist(statErr), "skill link should not exist before setup")

	// Run the interactive handler
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetContext(context.Background())

	styles := tui.NewStylesWithTheme(tui.ResolveTheme(false))
	require.NoError(t, runClaudeSetup(cmd, styles))

	// Skill link should now exist
	_, statErr = os.Stat(skillLinkPath)
	assert.NoError(t, statErr, "skill link should exist after setup repairs it")
}

// TestSetupClaudeNonInteractiveRemovesStalePlugins verifies that non-interactive
// setup detects and removes stale plugin entries from old marketplaces.
func TestSetupClaudeNonInteractiveRemovesStalePlugins(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Seed installed_plugins.json with stale + correct entries
	pluginDir := filepath.Join(home, ".claude", "plugins")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(pluginDir, "installed_plugins.json"),
		[]byte(`{"version":2,"plugins":{`+
			`"basecamp@basecamp":[{"scope":"user","version":"0.1.0"},{"scope":"project","version":"0.1.0"}],`+
			`"basecamp@37signals":[{"scope":"user","version":"0.1.0"}]}}`),
		0o644))

	// Create stub claude binary that logs invocations.
	// Succeeds once per uninstall key (marker file), fails on repeat.
	binDir := filepath.Join(home, "bin")
	markerDir := filepath.Join(home, "markers")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	require.NoError(t, os.MkdirAll(markerDir, 0o755))
	logFile := filepath.Join(home, "claude-calls.log")
	stubScript := "#!/bin/sh\n" +
		"echo \"$*\" >> \"" + logFile + "\"\n" +
		"case \"$1 $2\" in\n" +
		"  \"plugin uninstall\")\n" +
		"    MARKER=\"" + markerDir + "/$3_$5.removed\"\n" +
		"    if [ ! -f \"$MARKER\" ]; then\n" +
		"      > \"$MARKER\"\n" +
		"      exit 0\n" +
		"    fi\n" +
		"    exit 1\n" +
		"    ;;\n" +
		"  *) exit 0 ;;\n" +
		"esac\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "claude"), []byte(stubScript), 0o755)) //nolint:gosec // G306: test helper
	t.Setenv("PATH", binDir)

	app, _ := setupQuickstartTestApp(t, "", "")
	app.Flags.JSON = true

	cmd := NewSetupCmd()
	cmd.SetArgs([]string{"claude"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	// Verify stub was called with uninstall for the stale key
	calls, readErr := os.ReadFile(logFile)
	require.NoError(t, readErr)
	assert.Contains(t, string(calls), "plugin uninstall basecamp@basecamp --scope user")
	assert.Contains(t, string(calls), "plugin uninstall basecamp@basecamp --scope project")
}

// TestSetupClaudeNonInteractiveScopeAwareReinstall verifies that reinstall
// preserves the scopes from stale entries removed during migration.
func TestSetupClaudeNonInteractiveScopeAwareReinstall(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Seed installed_plugins.json with ONLY stale entries (no correct entry)
	pluginDir := filepath.Join(home, ".claude", "plugins")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(pluginDir, "installed_plugins.json"),
		[]byte(`{"version":2,"plugins":{`+
			`"basecamp@basecamp":[{"scope":"user","version":"0.1.0"},{"scope":"project","version":"0.1.0"}]}}`),
		0o644))

	// Create stub claude binary that logs invocations
	binDir := filepath.Join(home, "bin")
	markerDir := filepath.Join(home, "markers")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	require.NoError(t, os.MkdirAll(markerDir, 0o755))
	logFile := filepath.Join(home, "claude-calls.log")
	stubScript := "#!/bin/sh\n" +
		"echo \"$*\" >> \"" + logFile + "\"\n" +
		"case \"$1 $2\" in\n" +
		"  \"plugin uninstall\")\n" +
		"    MARKER=\"" + markerDir + "/$3_$5.removed\"\n" +
		"    if [ ! -f \"$MARKER\" ]; then\n" +
		"      > \"$MARKER\"\n" +
		"      exit 0\n" +
		"    fi\n" +
		"    exit 1\n" +
		"    ;;\n" +
		"  *) exit 0 ;;\n" +
		"esac\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "claude"), []byte(stubScript), 0o755)) //nolint:gosec // G306: test helper
	t.Setenv("PATH", binDir)

	app, _ := setupQuickstartTestApp(t, "", "")
	app.Flags.JSON = true

	cmd := NewSetupCmd()
	cmd.SetArgs([]string{"claude"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	// Verify install calls preserve scopes from stale entries
	calls, readErr := os.ReadFile(logFile)
	require.NoError(t, readErr)
	assert.Contains(t, string(calls), "plugin install basecamp@37signals --scope user")
	assert.Contains(t, string(calls), "plugin install basecamp@37signals --scope project")
}

// TestJoinNames verifies name joining with commas and "and".
func TestJoinNames(t *testing.T) {
	assert.Equal(t, "", joinNames(nil))
	assert.Equal(t, "Claude Code", joinNames([]string{"Claude Code"}))
	assert.Equal(t, "Claude Code and Cursor", joinNames([]string{"Claude Code", "Cursor"}))
	assert.Equal(t, "A, B, and C", joinNames([]string{"A", "B", "C"}))
}
