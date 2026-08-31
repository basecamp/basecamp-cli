package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/harness"
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

// TestSuccessHeadline verifies the completion banner is honest when the
// agent-setup step left unresolved issues.
func TestSuccessHeadline(t *testing.T) {
	assert.Equal(t, "Setup complete!", successHeadline("complete", 0))
	assert.Equal(t, "Setup complete!", successHeadline("", 0))
	assert.Equal(t, "Setup finished — 1 step needs attention", successHeadline("incomplete", 1))
	assert.Equal(t, "Setup finished — 2 steps need attention", successHeadline("incomplete", 2))
}

// TestStatusFromOutcome verifies issues are authoritative: any observed failure
// marks the run incomplete, and Skipped never suppresses one. A deliberate skip
// records no issues, so it stays complete.
func TestStatusFromOutcome(t *testing.T) {
	assert.Equal(t, "complete", statusFromOutcome(agentSetupOutcome{}))
	assert.Equal(t, "complete", statusFromOutcome(agentSetupOutcome{Skipped: true}))
	// Skipped must not suppress a real failure.
	assert.Equal(t, "incomplete",
		statusFromOutcome(agentSetupOutcome{Skipped: true, Issues: []agentIssue{{Check: "x"}}}))
	assert.Equal(t, "incomplete",
		statusFromOutcome(agentSetupOutcome{Issues: []agentIssue{{Check: "Claude Code Plugin"}}}))
}

// fakeAgents builds a detected-agent set with one failing Claude plugin check,
// supplied directly so tests never mutate the global registry.
func fakeAgents() []harness.AgentInfo {
	return []harness.AgentInfo{
		{
			Name: "Claude Code",
			Checks: func() []*harness.StatusCheck {
				return []*harness.StatusCheck{
					{Name: "Claude Code Skill", Status: "pass"},
					{Name: "Claude Code Plugin", Status: "fail", Hint: "Run: basecamp setup claude"},
				}
			},
		},
		{
			Name:   "Codex",
			Checks: func() []*harness.StatusCheck { return []*harness.StatusCheck{{Name: "Codex Plugin", Status: "pass"}} },
		},
		{Name: "NoChecks", Checks: nil},
	}
}

// TestSnapshotAndIssues verifies one snapshot captures every check, and
// issuesFromChecks keeps only the non-passing ones with their agent + hint.
func TestSnapshotAndIssues(t *testing.T) {
	checks := snapshotAgentChecks(fakeAgents())
	require.Len(t, checks, 3) // skill(pass) + plugin(fail) + codex(pass); NoChecks contributes none

	issues := issuesFromChecks(checks)
	require.Len(t, issues, 1)
	assert.Equal(t, "Claude Code", issues[0].Agent)
	assert.Equal(t, "Claude Code Plugin", issues[0].Check)
	assert.Equal(t, "Run: basecamp setup claude", issues[0].Hint)
}

// TestShowSuccessIncomplete renders the real summary and proves it does not claim
// completion, surfaces the failing check's own hint, and points to doctor.
func TestShowSuccessIncomplete(t *testing.T) {
	styles := tui.NewStylesWithTheme(tui.ResolveTheme(false))
	checks := snapshotAgentChecks(fakeAgents())
	issues := issuesFromChecks(checks)

	var buf bytes.Buffer
	showSuccess(&buf, styles, WizardResult{Status: "incomplete", AccountID: "123"}, checks, issues, false, omarchyPluginOutcome{})
	out := buf.String()

	assert.NotContains(t, out, "Setup complete!")
	assert.Contains(t, out, "Setup finished")
	assert.Contains(t, out, "Run: basecamp setup claude") // agent-specific hint
	assert.Contains(t, out, "basecamp doctor")
}

func TestShowSuccessOmarchyFailure(t *testing.T) {
	styles := tui.NewStylesWithTheme(tui.NoColorTheme())
	var buf bytes.Buffer
	showSuccess(&buf, styles, WizardResult{Status: "incomplete", AccountID: "123"}, nil, nil, false, omarchyPluginOutcome{
		Detected: true,
		Status:   "failed",
		Detail:   "could not update the Basecamp plugin",
		Manual:   "omarchy plugin update 37signals.basecamp",
	})

	out := buf.String()
	assert.Contains(t, out, "1 step needs attention")
	assert.Contains(t, out, "Basecamp plugin setup needs attention")
	assert.Contains(t, out, "omarchy plugin update 37signals.basecamp")
	assert.NotContains(t, out, "basecamp doctor")
}

// TestClaudeStaleIssues verifies surviving stale plugin entries become an issue
// (so status goes incomplete) and a clean home yields none.
func TestClaudeStaleIssues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	assert.Empty(t, claudeStaleIssues(), "no stale file → no issues")

	pluginDir := filepath.Join(home, ".claude", "plugins")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(pluginDir, "installed_plugins.json"),
		[]byte(`{"version":2,"plugins":{"basecamp@basecamp":[{"scope":"user","version":"0.1.0"}]}}`),
		0o644))

	issues := claudeStaleIssues()
	require.Len(t, issues, 1)
	assert.Equal(t, "Claude Code", issues[0].Agent)
	assert.Equal(t, "incomplete", statusFromOutcome(agentSetupOutcome{Issues: issues}))
}

// TestShowSuccessSkipped verifies a deliberate skip renders coherently: the
// completion banner stays, agent setup shows as skipped (not red failing
// checks), and there is no remediation block.
func TestShowSuccessSkipped(t *testing.T) {
	styles := tui.NewStylesWithTheme(tui.ResolveTheme(false))
	// preChecks on a skip can include a failing plugin check (agent never set up).
	checks := snapshotAgentChecks(fakeAgents())

	var buf bytes.Buffer
	showSuccess(&buf, styles, WizardResult{Status: "complete", AccountID: "123"}, checks, nil, true, omarchyPluginOutcome{})
	out := buf.String()

	assert.Contains(t, out, "Setup complete!")
	assert.Contains(t, out, "skipped")
	assert.NotContains(t, out, "Claude Code Plugin") // no red failing check rendered
	assert.NotContains(t, out, "basecamp doctor")    // no remediation on a deliberate skip
}

// TestShowSuccessComplete verifies the healthy path keeps the completion banner
// and prints no remediation block.
func TestShowSuccessComplete(t *testing.T) {
	styles := tui.NewStylesWithTheme(tui.ResolveTheme(false))
	checks := []agentCheck{{Agent: "Claude Code", Name: "Claude Code Plugin", Status: "pass"}}

	var buf bytes.Buffer
	showSuccess(&buf, styles, WizardResult{Status: "complete", AccountID: "123"}, checks, nil, false, omarchyPluginOutcome{})
	out := buf.String()

	assert.Contains(t, out, "Setup complete!")
	assert.NotContains(t, out, "Some steps need attention")
	assert.NotContains(t, out, "basecamp doctor")
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

	customize := cmd.Flags().Lookup("customize")
	require.NotNil(t, customize)
	assert.Equal(t, "false", customize.DefValue)

	minimal := cmd.Flags().Lookup("minimal")
	require.NotNil(t, minimal)
	assert.Equal(t, "false", minimal.DefValue)
}

func TestFastSetupRejectsDefaultProjectFlag(t *testing.T) {
	app, _ := setupQuickstartTestApp(t, "", "")
	app.Flags.Project = "123"
	cmd := &cobra.Command{}
	cmd.SetContext(appctx.WithApp(context.Background(), app))

	err := runFastSetup(cmd, app, false)
	require.Error(t, err)
	assert.True(t, app.SuppressPostRunNotices)
	assert.Equal(t, output.CodeUsage, output.AsError(err).Code)
	assert.Contains(t, output.AsError(err).Hint, "--customize --project 123")
}

func TestChooseAutomaticAccount(t *testing.T) {
	accounts := []basecamp.AuthorizedAccount{
		{ID: 123, Name: "First Account"},
		{ID: 456, Name: "Second Account"},
	}

	id, name, err := chooseAutomaticAccount("", "", "", accounts)
	require.NoError(t, err)
	assert.Equal(t, "123", id)
	assert.Equal(t, "First Account", name)

	id, name, err = chooseAutomaticAccount("", "", "456", accounts)
	require.NoError(t, err)
	assert.Equal(t, "456", id)
	assert.Empty(t, name)

	id, name, err = chooseAutomaticAccount("", "789", "456", accounts)
	require.NoError(t, err)
	assert.Equal(t, "789", id)
	assert.Empty(t, name)

	id, name, err = chooseAutomaticAccount("789", "789", "456", accounts)
	require.NoError(t, err)
	assert.Equal(t, "789", id)
	assert.Empty(t, name)

	id, name, err = chooseAutomaticAccount("999", "", "456", accounts)
	require.NoError(t, err)
	assert.Equal(t, "999", id)
	assert.Empty(t, name)

	_, _, err = chooseAutomaticAccount("999", "789", "456", accounts)
	require.Error(t, err)
	assert.Equal(t, output.CodeUsage, output.AsError(err).Code)
	assert.Contains(t, output.AsError(err).Hint, "--account 789")

	_, _, err = chooseAutomaticAccount("", "", "", nil)
	require.Error(t, err)
	assert.Equal(t, output.CodeNotFound, output.AsError(err).Code)
}

func TestPersistRecommendedDefaultsClearsOnlyTheGlobalProject(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	globalPath := filepath.Join(configHome, "basecamp", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(globalPath), 0o700))
	require.NoError(t, os.WriteFile(globalPath, []byte(`{"account_id":"111","project_id":"222","hints":true}`), 0o600))

	workingDir := t.TempDir()
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workingDir))
	t.Cleanup(func() { require.NoError(t, os.Chdir(previousDir)) })
	localPath := filepath.Join(workingDir, ".basecamp", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(localPath), 0o700))
	require.NoError(t, os.WriteFile(localPath, []byte(`{"project_id":"333"}`), 0o600))

	app, _ := setupQuickstartTestApp(t, "111", "333")
	app.Config.Sources = map[string]string{"account_id": "global", "project_id": "local"}
	require.NoError(t, persistRecommendedDefaults(app, "456"))

	var global map[string]any
	data, err := os.ReadFile(globalPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &global))
	assert.Equal(t, "456", global["account_id"])
	assert.Equal(t, true, global["hints"])
	assert.NotContains(t, global, "project_id")

	localData, err := os.ReadFile(localPath)
	require.NoError(t, err)
	assert.JSONEq(t, `{"project_id":"333"}`, string(localData))
	assert.Equal(t, "456", app.Config.AccountID)
	assert.Empty(t, app.Config.ProjectID)
	assert.NotContains(t, app.Config.Sources, "project_id")
}

func TestShowWelcome(t *testing.T) {
	styles := tui.NewStylesWithTheme(tui.ResolveTheme(false))
	var buf bytes.Buffer
	wait := showWelcome(&buf, styles)
	wait()

	out := buf.String()
	assert.Contains(t, out, "Basecamp at your command (line).")
	assert.Equal(t, 1, strings.Count(out, "Basecamp"), "the logo should not repeat the product name")
	assert.NotContains(t, out, "Welcome to Basecamp")
	assert.Contains(t, out, "Let's get you set up. It’ll only take a moment.")
	assert.NotContains(t, out, "command-line interface for Basecamp")
}

func TestShowFastAuthenticationStart(t *testing.T) {
	styles := tui.NewStylesWithTheme(tui.NoColorTheme())
	var buf bytes.Buffer
	prefix := showAuthenticationStart(&buf, styles, false)
	log := authenticationLogger(&buf, prefix)
	log("Authenticating via launchpad (https://launchpad.37signals.com/authorization/new)")
	log("\nOpening browser for authentication...")

	assert.Empty(t, prefix)
	assert.Equal(t, "Opening browser for Basecamp login...\n", buf.String())
	assert.NotContains(t, buf.String(), "Step 1")
	assert.NotContains(t, buf.String(), "launchpad")
	assert.NotContains(t, buf.String(), "Opening browser for authentication")

	var deviceFlow bytes.Buffer
	deviceLog := authenticationLogger(&deviceFlow, "")
	deviceLog("Authenticating via https://3.basecampapi.com (device flow)")
	deviceLog("\nOpening browser for authentication...")
	assert.Contains(t, deviceFlow.String(), "Authenticating via https://3.basecampapi.com (device flow)")
	assert.Contains(t, deviceFlow.String(), "Opening browser for authentication")
}

func TestShowFastSuccess(t *testing.T) {
	styles := tui.NewStylesWithTheme(tui.ResolveTheme(false))
	result := WizardResult{AuthenticatedAs: "Jane Smith"}

	var buf bytes.Buffer
	showFastAuthenticated(&buf, styles, result.AuthenticatedAs, "123", "Acme")
	showFastAgentStatus(&buf, styles, agentSetupOutcome{Detected: 2})
	showFastCompletion(&buf, styles, agentSetupOutcome{}, omarchyPluginOutcome{}, false)
	out := buf.String()

	assert.NotContains(t, out, "Setup complete!")
	assert.Contains(t, out, "Authenticated as Jane Smith")
	assert.Contains(t, out, "Using account Acme")
	assert.Contains(t, out, "AI coding agents set up")
	assert.NotContains(t, out, "Account: Acme")
	assert.NotContains(t, out, "basecamp setup --customize")
	assert.NotContains(t, out, "Try these commands")
	for _, want := range []string{
		"Try it out!",
		"basecamp projects list",
		"basecamp assignments",
		"basecamp timeline",
		`basecamp search "quarterly planning"`,
	} {
		assert.Contains(t, out, want)
	}
}

func TestShowFastSuccessMinimal(t *testing.T) {
	styles := tui.NewStylesWithTheme(tui.DefaultTheme(false))
	var buf bytes.Buffer
	showFastAuthenticated(&buf, styles, "Jane Smith", "123", "Acme")
	showFastAgentStatus(&buf, styles, agentSetupOutcome{Detected: 2})
	showFastCompletion(&buf, styles, agentSetupOutcome{}, omarchyPluginOutcome{}, true)

	out := buf.String()
	assert.Contains(t, out, "Authenticated as Jane Smith")
	assert.Contains(t, out, "Using account Acme")
	assert.Contains(t, out, "AI coding agents set up")
	assert.Contains(t, out, fastSetupTitleStyle(styles).Render("SETUP COMPLETE"))
	assert.NotContains(t, out, "Try it out!")
	assert.NotContains(t, out, "basecamp projects list")
	assert.True(t, strings.HasSuffix(out, "\n\n"), "completion message should have a blank line below it")
}

func TestShowFastCompletionReportsIntegrationOutcomes(t *testing.T) {
	styles := tui.NewStylesWithTheme(tui.NoColorTheme())

	var installed bytes.Buffer
	showFastCompletion(&installed, styles, agentSetupOutcome{}, omarchyPluginOutcome{
		Detected: true,
		Status:   "installed",
	}, false)
	assert.Contains(t, installed.String(), "Basecamp plugin installed for Omarchy")

	var omarchyFailed bytes.Buffer
	showFastCompletion(&omarchyFailed, styles, agentSetupOutcome{}, omarchyPluginOutcome{
		Detected: true,
		Status:   "failed",
		Manual:   "omarchy plugin update 37signals.basecamp",
	}, true)
	assert.Contains(t, omarchyFailed.String(), "Basecamp plugin setup needs attention")
	assert.Contains(t, omarchyFailed.String(), "omarchy plugin update 37signals.basecamp")
	assert.Contains(t, omarchyFailed.String(), "SETUP NEEDS ATTENTION")
	assert.NotContains(t, omarchyFailed.String(), "SETUP COMPLETE")

	var agentsFailed bytes.Buffer
	showFastCompletion(&agentsFailed, styles, agentSetupOutcome{
		Issues: []agentIssue{{Check: "Claude Code Plugin"}},
	}, omarchyPluginOutcome{}, true)
	assert.Contains(t, agentsFailed.String(), "SETUP NEEDS ATTENTION")
	assert.NotContains(t, agentsFailed.String(), "SETUP COMPLETE")
}

func TestShowFastSetupExamplesUseTerminalColor(t *testing.T) {
	styles := tui.NewStylesWithTheme(tui.ResolveTheme(false))
	var buf bytes.Buffer
	showFastSetupExamples(&buf, styles)

	lines := strings.Split(buf.String(), "\n")
	for _, command := range []string{
		"basecamp projects list",
		"basecamp assignments",
		"basecamp timeline",
		`basecamp search "quarterly planning"`,
	} {
		var found bool
		for _, line := range lines {
			if strings.HasPrefix(line, command) {
				found = true
				break
			}
		}
		assert.True(t, found, "command should start at column zero in the terminal's default color: %s", command)
	}
}

func TestShowFastSuccessWithoutAgents(t *testing.T) {
	styles := tui.NewStylesWithTheme(tui.ResolveTheme(false))
	var buf bytes.Buffer
	showFastAgentStatus(&buf, styles, agentSetupOutcome{})

	assert.Contains(t, buf.String(), "AI coding agents: none detected")
}

func TestShowFastSuccessHidesAgentDetails(t *testing.T) {
	styles := tui.NewStylesWithTheme(tui.ResolveTheme(false))
	var buf bytes.Buffer
	showFastAgentStatus(&buf, styles, agentSetupOutcome{
		Detected: 1,
		Issues: []agentIssue{{
			Check: "Claude Code Plugin",
			Hint:  "Plugin version mismatch",
		}},
	})

	out := buf.String()
	assert.Contains(t, out, "AI coding agents need attention — run: basecamp doctor")
	assert.NotContains(t, out, "Claude Code Plugin")
	assert.NotContains(t, out, "version mismatch")
}

func TestInstallDetectedAgentsRunsEveryHandler(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	originalHandlers := agentSetupHandlers
	var ran []string
	agentSetupHandlers = map[string]agentSetupHandler{
		"alpha": {Run: func(*cobra.Command, *tui.Styles) error {
			ran = append(ran, "alpha")
			return nil
		}},
		"beta": {Run: func(*cobra.Command, *tui.Styles) error {
			ran = append(ran, "beta")
			return nil
		}},
	}
	t.Cleanup(func() { agentSetupHandlers = originalHandlers })

	agents := []harness.AgentInfo{
		{ID: "alpha", Name: "Alpha", Checks: func() []*harness.StatusCheck { return nil }},
		{ID: "beta", Name: "Beta", Checks: func() []*harness.StatusCheck { return nil }},
	}
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetContext(context.Background())

	outcome, err := installDetectedAgents(cmd, tui.NewStylesWithTheme(tui.ResolveTheme(false)), agents)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "beta"}, ran)
	assert.Equal(t, 2, outcome.Detected)
}

func TestInstallDetectedAgentsQuietlySuppressesChatter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	originalHandlers := agentSetupHandlers
	agentSetupHandlers = map[string]agentSetupHandler{
		"alpha": {Run: func(cmd *cobra.Command, _ *tui.Styles) error {
			fmt.Fprintln(cmd.OutOrStdout(), "plugin mismatch")
			fmt.Fprintln(cmd.ErrOrStderr(), "agent skill updated")
			return nil
		}},
	}
	t.Cleanup(func() { agentSetupHandlers = originalHandlers })

	agents := []harness.AgentInfo{
		{ID: "alpha", Name: "Alpha", Checks: func() []*harness.StatusCheck { return nil }},
	}
	var stdout, stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetContext(context.Background())

	_, err := installDetectedAgentsQuietly(cmd, tui.NewStylesWithTheme(tui.ResolveTheme(false)), agents)
	require.NoError(t, err)
	assert.Empty(t, stdout.String())
	assert.Empty(t, stderr.String())
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
	assert.Contains(t, string(calls), "plugin install basecamp@37signals --scope project")
	// The reinstall path must also refresh before installing: add → update →
	// scoped install, so a stale SSH-shorthand entry is replaced with the current
	// HTTPS source first (issue #417).
	assertCallOrder(t, string(calls),
		"plugin marketplace add basecamp/claude-plugins",
		"plugin marketplace update 37signals",
		"plugin install basecamp@37signals --scope user")
}

// TestSetupClaudeNonInteractiveRefreshesMarketplace verifies the fresh-install
// path refreshes the marketplace cache before `plugin install`, so a stale
// `source: github` entry (which would clone over SSH) is replaced with the
// current HTTPS `source: url` first (issue #417).
func TestSetupClaudeNonInteractiveRefreshesMarketplace(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	home := t.TempDir()
	t.Setenv("HOME", home)

	// ~/.claude exists (Claude detected) but no plugin installed → fresh install.
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	binDir := filepath.Join(home, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	logFile := filepath.Join(home, "claude-calls.log")
	stubScript := "#!/bin/sh\n" +
		"echo \"$*\" >> \"" + logFile + "\"\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "claude"), []byte(stubScript), 0o755)) //nolint:gosec // G306: test helper
	t.Setenv("PATH", binDir)

	app, _ := setupQuickstartTestApp(t, "", "")
	app.Flags.JSON = true

	cmd := NewSetupCmd()
	cmd.SetArgs([]string{"claude"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	require.NoError(t, cmd.Execute())

	calls, readErr := os.ReadFile(logFile)
	require.NoError(t, readErr)
	// The refresh must land between registering and installing: add → update →
	// install. `add` no-ops on a stale cache, so only the update refreshes it to
	// the HTTPS source before install (issue #417).
	assertCallOrder(t, string(calls),
		"plugin marketplace add basecamp/claude-plugins",
		"plugin marketplace update 37signals",
		"plugin install basecamp@37signals")
}

// TestRunClaudeSetupInteractiveRefreshOrder covers the interactive install path
// (runClaudeSetup) — a separate set of call sites from the non-interactive path —
// and asserts the same add → update → install ordering (issue #417).
func TestRunClaudeSetupInteractiveRefreshOrder(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	home := t.TempDir()
	t.Setenv("HOME", home)

	// ~/.claude exists (Claude detected), no plugin installed → fresh install.
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	binDir := filepath.Join(home, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	logFile := filepath.Join(home, "claude-calls.log")
	stubScript := "#!/bin/sh\n" +
		"echo \"$*\" >> \"" + logFile + "\"\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "claude"), []byte(stubScript), 0o755)) //nolint:gosec // G306: test helper
	t.Setenv("PATH", binDir)

	styles := tui.NewStylesWithTheme(tui.ResolveTheme(false))
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	require.NoError(t, runClaudeSetup(cmd, styles))

	calls, readErr := os.ReadFile(logFile)
	require.NoError(t, readErr)
	assertCallOrder(t, string(calls),
		"plugin marketplace add basecamp/claude-plugins",
		"plugin marketplace update 37signals",
		"plugin install basecamp@37signals")
}

// TestJoinNames verifies name joining with commas and "and".
func TestJoinNames(t *testing.T) {
	assert.Equal(t, "", joinNames(nil))
	assert.Equal(t, "Claude Code", joinNames([]string{"Claude Code"}))
	assert.Equal(t, "Claude Code and Cursor", joinNames([]string{"Claude Code", "Cursor"}))
	assert.Equal(t, "A, B, and C", joinNames([]string{"A", "B", "C"}))
}

// terminalOutputs points both os.Stdout and os.Stderr at a pseudo-terminal so
// stdin is the only thing left disqualifying a prompt. Both are needed: the
// setup gate asks IsInteractive (stdin+stdout) and InteractivePrompt
// (stdin+stderr), and go test pipes both — so without this the assertions below
// would hold for any stdin at all and prove nothing.
//
// Best-effort: where no pty is available the test still runs, less specifically.
func terminalOutputs(t *testing.T) {
	t.Helper()

	for _, stream := range []**os.File{&os.Stdout, &os.Stderr} {
		if runtime.GOOS == "windows" {
			return
		}
		pty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
		if err != nil {
			return
		}

		orig, target := *stream, stream
		*stream = pty
		t.Cleanup(func() {
			*target = orig
			_ = pty.Close()
		})
	}
}

// nonInteractiveStdin points os.Stdin at the named non-terminal for the
// duration of the test, so the assertions hold no matter how the test binary
// was invoked — running it straight from a terminal would otherwise leave stdin
// a TTY and prove nothing. "devnull" is the case that used to slip through: a
// character device that is not a terminal.
func nonInteractiveStdin(t *testing.T, kind string) {
	t.Helper()

	terminalOutputs(t)

	var replacement *os.File
	switch kind {
	case "pipe":
		r, w, err := os.Pipe()
		require.NoError(t, err)
		t.Cleanup(func() { _ = w.Close() })
		replacement = r
	case "devnull":
		f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		require.NoError(t, err)
		replacement = f
	default:
		t.Fatalf("unknown stdin kind %q", kind)
	}

	orig := os.Stdin
	os.Stdin = replacement
	t.Cleanup(func() {
		os.Stdin = orig
		_ = replacement.Close()
	})
}

// runSetupWithin executes the setup command and fails if it has not returned
// within the timeout. The timeout guards against setup reaching a browser or
// prompt in a context that cannot complete it.
func runSetupWithin(t *testing.T, cmd *cobra.Command, timeout time.Duration) error {
	t.Helper()

	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatal("setup blocked instead of returning; it reached a prompt it cannot drive")
		return nil
	}
}

// TestSetupRefusesNonInteractiveStdio covers the hang this gate exists for:
// `basecamp setup` off a terminal used to walk into a huh prompt, which falls
// back to /dev/tty instead of failing. It must return a usage error instead,
// and the hint must name a path that actually works without a terminal.
func TestSetupRefusesNonInteractiveStdio(t *testing.T) {
	for _, tc := range []struct {
		name string
		json bool
		kind string
	}{
		{"plain/pipe", false, "pipe"},
		{"plain/devnull", false, "devnull"},
		{"json/pipe", true, "pipe"},
		{"json/devnull", true, "devnull"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			nonInteractiveStdin(t, tc.kind)

			app, _ := setupQuickstartTestApp(t, "", "")
			app.Flags.JSON = tc.json

			cmd := NewSetupCmd()
			cmd.SetArgs(nil)
			cmd.SetContext(appctx.WithApp(context.Background(), app))
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})

			err := runSetupWithin(t, cmd, 10*time.Second)
			require.Error(t, err)

			outErr := output.AsError(err)
			require.NotNil(t, outErr)
			assert.Equal(t, output.CodeUsage, outErr.Code)
			assert.Contains(t, outErr.Hint, "basecamp setup agents",
				"the hint must name a non-interactive alternative, not just restate the problem")
		})
	}
}

// TestSetupRefusesUnderNonInteractiveEnv verifies BASECAMP_NONINTERACTIVE is
// honored even where stdio would pass. The env var that disables human setup
// applies to both recommended and customized setup.
func TestSetupRefusesUnderNonInteractiveEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")

	app, _ := setupQuickstartTestApp(t, "", "")

	cmd := NewSetupCmd()
	cmd.SetArgs(nil)
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := runSetupWithin(t, cmd, 10*time.Second)
	require.Error(t, err)
	assert.Equal(t, output.CodeUsage, output.AsError(err).Code)
}

// TestSetupRefusesMachineOutputOnATerminal covers the other half of the gate.
// Terminal stdio is not enough: a caller that requested machine output is not
// running human first-time setup. Config-driven json/quiet counts too —
// app.IsInteractive() does not look at it, which is why the gate also asks
// IsMachineOutput(). An explicit --styled/--md override restores human output,
// so that pairing can run setup like any human invocation.
func TestSetupRefusesMachineOutputOnATerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /dev/ptmx on Windows")
	}
	pty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("open /dev/ptmx: %v", err)
	}
	origOut, origIn, origErr := os.Stdout, os.Stdin, os.Stderr
	os.Stdout, os.Stdin, os.Stderr = pty, pty, pty
	t.Cleanup(func() {
		os.Stdout, os.Stdin, os.Stderr = origOut, origIn, origErr
		pty.Close()
	})

	for _, tc := range []struct {
		name  string
		apply func(*appctx.App)
	}{
		{"json flag", func(a *appctx.App) { a.Flags.JSON = true }},
		{"agent flag", func(a *appctx.App) { a.Flags.Agent = true }},
		{"quiet flag", func(a *appctx.App) { a.Flags.Quiet = true }},
		{"config format json", func(a *appctx.App) { a.Config.Format = "json" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())

			app, _ := setupQuickstartTestApp(t, "", "")
			tc.apply(app)

			cmd := NewSetupCmd()
			cmd.SetArgs(nil)
			cmd.SetContext(appctx.WithApp(context.Background(), app))
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})

			err := runSetupWithin(t, cmd, 10*time.Second)
			require.Error(t, err)
			assert.Equal(t, output.CodeUsage, output.AsError(err).Code)
			assert.Contains(t, output.AsError(err).Hint, "basecamp setup agents")
		})
	}
}

// TestSetupSubcommandsSurviveTheGate is the other half of the gate: it belongs
// to the parent's RunE only. `setup agents`, `setup claude` and `setup codex`
// are the supported non-interactive paths and must keep working off a terminal
// — a persistent hook here would have broken all three.
func TestSetupSubcommandsSurviveTheGate(t *testing.T) {
	for _, sub := range []string{"agents", "claude", "codex"} {
		t.Run(sub, func(t *testing.T) {
			t.Setenv("BASECAMP_NO_KEYRING", "1")
			t.Setenv("HOME", t.TempDir())
			t.Setenv("PATH", t.TempDir()) // no agent binaries
			t.Setenv("BASECAMP_SETUP_AGENT", "none")
			nonInteractiveStdin(t, "devnull")

			app, _ := setupQuickstartTestApp(t, "", "")
			app.Flags.JSON = true

			cmd := NewSetupCmd()
			cmd.SetArgs([]string{sub}) // --json is a root persistent flag; app.Flags carries it here
			cmd.SetContext(appctx.WithApp(context.Background(), app))
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})

			err := runSetupWithin(t, cmd, 10*time.Second)
			require.NoError(t, err, "setup %s must still run without a terminal", sub)
		})
	}
}

// TestBareBasecampNeverReportsASetupError covers the difference between asking
// for setup and receiving first-run behavior implicitly. Explicit setup refuses
// when it cannot run safely. Bare `basecamp` falls through to its normal output.
//
// Both rows reach RunQuickStartDefault's first-run decision and would have hit
// the setup gate when isFirstRun only checked stdin and stdout:
//
//   - stderr redirected: stdin/stdout are terminals, so first-run fires, but
//     huh draws to stderr and could not have shown anything.
//   - config-driven json: IsInteractive does not read Config.Format, so
//     first-run fires, while quickstart.go documents this shape as preserving
//     the quick-start envelope.
func TestBareBasecampNeverReportsASetupError(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(t *testing.T, app *appctx.App)
	}{
		{
			name: "stderr redirected on a terminal",
			apply: func(t *testing.T, _ *appctx.App) {
				terminalStdio(t)        // stdin+stdout+stderr all terminals...
				redirectStderrToPipe(t) // ...then take away the one huh draws to
			},
		},
		{
			name: "config-driven json output",
			apply: func(t *testing.T, app *appctx.App) {
				terminalStdio(t)
				app.Config.Format = "json"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())

			app, _ := setupQuickstartTestApp(t, "", "")
			tc.apply(t, app)

			// Without this the test proves nothing: isFirstRun bails on
			// IsInteractive before it ever reaches setup, and every
			// assertion below passes for the wrong reason. An earlier version
			// of this test did exactly that — it set stdout and stderr but left
			// stdin on go test's /dev/null, so it passed against the bug.
			require.True(t, app.IsInteractive(),
				"precondition: stdin and stdout must look interactive, or first-run never fires")

			cmd := NewQuickStartCmd()
			cmd.SetArgs(nil)
			cmd.SetContext(appctx.WithApp(context.Background(), app))
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})

			done := make(chan error, 1)
			go func() { done <- RunQuickStartDefault(cmd, nil) }()

			select {
			case err := <-done:
				if err != nil {
					assert.NotContains(t, err.Error(), "basecamp setup",
						"bare basecamp must not fail with an error about a command the user never typed")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("bare basecamp blocked; it reached a prompt it cannot drive")
			}
		})
	}
}

// TestExplicitSetupStillRefuses is the other side of the same predicate: naming
// the command still gets the usage error, in exactly the contexts where bare
// `basecamp` must not.
func TestExplicitSetupStillRefuses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	terminalStdio(t)
	redirectStderrToPipe(t)

	app, _ := setupQuickstartTestApp(t, "", "")
	require.True(t, app.IsInteractive(),
		"precondition: only stderr should be disqualifying here")

	cmd := NewSetupCmd()
	cmd.SetArgs(nil)
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := runSetupWithin(t, cmd, 10*time.Second)
	require.Error(t, err)
	assert.Equal(t, output.CodeUsage, output.AsError(err).Code)
}

// terminalStdio points stdin, stdout and stderr at pseudo-terminals, so an
// invocation looks fully interactive before a test takes one of them away.
func terminalStdio(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("no /dev/ptmx on Windows")
	}
	pty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("open /dev/ptmx: %v", err)
	}
	origIn, origOut, origErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = pty, pty, pty
	t.Cleanup(func() {
		os.Stdin, os.Stdout, os.Stderr = origIn, origOut, origErr
		pty.Close()
	})
}

// redirectStderrToPipe makes stderr a pipe — huh's render target, so a prompt
// could not be seen even though stdin and stdout are terminals.
func redirectStderrToPipe(t *testing.T) {
	t.Helper()

	r, w, err := os.Pipe()
	require.NoError(t, err)

	orig := os.Stderr
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = orig
		_ = r.Close()
		_ = w.Close()
	})
}
