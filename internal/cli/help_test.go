package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/commands"
)

// isolateHelpTest sets env vars for hermetic help tests: disables keyring
// and isolates config/cache dirs to a temp directory.
func isolateHelpTest(t *testing.T) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func TestRootHelpContainsCategoryHeaders(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewProjectsCmd())
	cmd.AddCommand(commands.NewTodosCmd())
	cmd.AddCommand(commands.NewSearchCmd())
	cmd.AddCommand(commands.NewAuthCmd())
	cmd.AddCommand(commands.NewConfigCmd())
	cmd.AddCommand(commands.NewSetupCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	err := cmd.Execute()
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "CORE COMMANDS")
	assert.Contains(t, out, "SEARCH & BROWSE")
	assert.Contains(t, out, "AUTH & CONFIG")
	assert.Contains(t, out, "FLAGS")
}

func TestRootHelpContainsExamples(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	_ = cmd.Execute()

	out := buf.String()
	assert.Contains(t, out, "EXAMPLES")
	assert.Contains(t, out, "basecamp projects")
	assert.Contains(t, out, "LEARN MORE")
}

func TestRootHelpContainsLearnMore(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	_ = cmd.Execute()

	out := buf.String()
	assert.Contains(t, out, "basecamp commands")
	assert.Contains(t, out, "basecamp <command> -h")
}

func TestSubcommandGetsStyledHelp(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	todos := commands.NewTodosCmd()
	cmd.AddCommand(todos)
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"todos", "--help"})
	_ = cmd.Execute()

	out := buf.String()
	assert.Contains(t, out, "USAGE")
	assert.Contains(t, out, "COMMANDS")
	assert.NotContains(t, out, "CORE COMMANDS")
}

func TestCommandHelpRendersExample(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewProjectsCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"projects", "--help"})
	_ = cmd.Execute()

	out := buf.String()
	assert.Contains(t, out, "EXAMPLES")
	assert.Contains(t, out, "basecamp projects list")
	assert.Contains(t, out, "INHERITED FLAGS")
}

func TestLeafCommandHelp(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewProjectsCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"projects", "list", "--help"})
	_ = cmd.Execute()

	out := buf.String()
	assert.Contains(t, out, "USAGE")
	assert.Contains(t, out, "FLAGS")
	assert.NotContains(t, out, "COMMANDS")
	// Inherited flags are curated: salient root flags shown, noise hidden
	assert.Contains(t, out, "--json")
	assert.Contains(t, out, "--quiet")
	assert.NotContains(t, out, "--verbose")
	assert.NotContains(t, out, "--styled")
	// Leaf LEARN MORE points back to parent
	assert.Contains(t, out, "basecamp projects --help")
}

func TestGroupCommandShowsPersistentLocalFlags(t *testing.T) {
	// Commands that define their own persistent flags (--project, --in, etc.)
	// must show them in FLAGS. This catches regressions where LocalFlags() is
	// accidentally replaced with LocalNonPersistentFlags().
	isolateHelpTest(t)

	tests := []struct {
		name     string
		command  string
		addCmd   func() *cobra.Command
		wantFlag string
	}{
		{"messages --project", "messages", commands.NewMessagesCmd, "--project"},
		{"messages --in", "messages", commands.NewMessagesCmd, "--in"},
		{"messages --message-board", "messages", commands.NewMessagesCmd, "--message-board"},
		{"campfire --project", "campfire", commands.NewCampfireCmd, "--project"},
		{"campfire --campfire", "campfire", commands.NewCampfireCmd, "--campfire"},
		{"campfire --content-type", "campfire", commands.NewCampfireCmd, "--content-type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			cmd := NewRootCmd()
			cmd.AddCommand(tt.addCmd())
			cmd.SetOut(&buf)
			cmd.SetArgs([]string{tt.command, "--help"})
			_ = cmd.Execute()

			assert.Contains(t, buf.String(), tt.wantFlag)
		})
	}
}

func TestRootLevelLeafCommandHelp(t *testing.T) {
	// Root-level leaf commands (no subcommands, parent is root) must still
	// render a complete LEARN MORE section pointing to the root.
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewDoneCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"done", "--help"})
	_ = cmd.Execute()

	out := buf.String()
	assert.Contains(t, out, "USAGE")
	assert.Contains(t, out, "INHERITED FLAGS")
	assert.Contains(t, out, "LEARN MORE")
	assert.Contains(t, out, "basecamp --help")
	assert.NotContains(t, out, "COMMANDS")
}

func TestLeafCommandInheritsParentPersistentFlags(t *testing.T) {
	// Leaf commands must show parent-defined persistent flags in INHERITED
	// FLAGS. These flags carry required context (--project, --campfire, etc.)
	// and hiding them breaks discoverability.
	isolateHelpTest(t)

	tests := []struct {
		name      string
		args      []string
		addCmd    func() *cobra.Command
		wantFlags []string
	}{
		{
			"messages create inherits --project",
			[]string{"messages", "create", "--help"},
			commands.NewMessagesCmd,
			[]string{"--project", "--in", "--message-board"},
		},
		{
			"campfire post inherits --project and --campfire",
			[]string{"campfire", "post", "--help"},
			commands.NewCampfireCmd,
			[]string{"--project", "--campfire", "--content-type"},
		},
		{
			"timesheet report inherits date and person flags",
			[]string{"timesheet", "report", "--help"},
			commands.NewTimesheetCmd,
			[]string{"--project", "--start", "--end", "--person"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			cmd := NewRootCmd()
			cmd.AddCommand(tt.addCmd())
			cmd.SetOut(&buf)
			cmd.SetArgs(tt.args)
			_ = cmd.Execute()

			out := buf.String()
			for _, flag := range tt.wantFlags {
				assert.Contains(t, out, flag)
			}
		})
	}
}

func TestAgentHelpProducesJSON(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help", "--agent"})
	_ = cmd.Execute()

	out := buf.String()
	assert.True(t, strings.HasPrefix(out, "{"), "agent help should produce JSON, got: %s", out)
	assert.Contains(t, out, `"command"`)
}

func TestBareRootToleratesBadProfile(t *testing.T) {
	// Bare basecamp should not error when profile env is broken.
	// In non-TTY test environments the bare root falls through to quickstart
	// (not help), so we only assert no error — the important thing is that
	// a bad profile doesn't crash.
	isolateHelpTest(t)
	t.Setenv("BASECAMP_PROFILE", "nonexistent")

	cmd := NewRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestBareRootWithJSONFlagDoesNotShowHelp(t *testing.T) {
	// basecamp --json should NOT show help text — it runs quickstart which
	// writes JSON to app.Output (stdout). We verify help is not rendered;
	// JSON correctness is covered by e2e tests.
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewQuickStartCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--json"})
	err := cmd.Execute()
	require.NoError(t, err)

	assert.NotContains(t, buf.String(), "CORE COMMANDS")
}

func TestBareRootWithAgentFlagDoesNotShowHelp(t *testing.T) {
	// basecamp --agent should NOT show help text — it runs quickstart
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewQuickStartCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--agent"})
	err := cmd.Execute()
	require.NoError(t, err)

	assert.NotContains(t, buf.String(), "CORE COMMANDS")
}

func TestBareRootWithConfigFormatJSONDoesNotShowHelp(t *testing.T) {
	// Config-driven format=json should route to quickstart, not help.
	// This exercises the IsMachineOutput() check through PersistentPreRunE.
	isolateHelpTest(t)
	tmpDir := t.TempDir()
	bcDir := filepath.Join(tmpDir, "basecamp")
	require.NoError(t, os.MkdirAll(bcDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(bcDir, "config.json"),
		[]byte(`{"format":"json","onboarded":true}`),
		0o644,
	))
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewQuickStartCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	require.NoError(t, err)

	// Help text should not appear — quickstart ran instead
	assert.NotContains(t, buf.String(), "CORE COMMANDS")
}

func TestBareRootBadProfileResolvesPreferences(t *testing.T) {
	// When bareRoot tolerates a bad profile, it should still resolve
	// preferences from config (hints, stats). Verify by checking that
	// PersistentPreRunE doesn't error and help text is suppressed
	// (quickstart ran with config-driven format).
	isolateHelpTest(t)
	tmpDir := t.TempDir()
	bcDir := filepath.Join(tmpDir, "basecamp")
	require.NoError(t, os.MkdirAll(bcDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(bcDir, "config.json"),
		[]byte(`{"format":"json","hints":true,"stats":true,"onboarded":true,"profiles":{"work":{}}}`),
		0o644,
	))
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("BASECAMP_PROFILE", "nonexistent")

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewQuickStartCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	require.NoError(t, err)

	// Should not show help — config-driven JSON mode routes to quickstart
	assert.NotContains(t, buf.String(), "CORE COMMANDS")
}

func TestRootHelpUsesLiveCommandDescriptions(t *testing.T) {
	// Help descriptions should come from the registered commands' Short field,
	// not from the catalog. This catches drift between catalog copy and actual
	// command metadata.
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	search := commands.NewSearchCmd()
	cmd.AddCommand(search)
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	_ = cmd.Execute()

	out := buf.String()
	// The help screen should show the command's actual Short, not the catalog's
	assert.Contains(t, out, search.Short)
}
