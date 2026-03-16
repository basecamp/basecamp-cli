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
		{"chat --project", "chat", commands.NewChatCmd, "--project"},
		{"chat --chat", "chat", commands.NewChatCmd, "--chat"},
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

func TestLeafCommandShowsParentScopedFlagsInFLAGS(t *testing.T) {
	// Parent-scoped persistent flags (--project, --chat, etc.) are promoted
	// into the FLAGS section on leaf commands, not buried in INHERITED FLAGS.
	// This is a renderer-wide policy: any leaf whose parent defines persistent
	// flags will show them in FLAGS.
	isolateHelpTest(t)

	tests := []struct {
		name      string
		args      []string
		addCmd    func() *cobra.Command
		wantFlags []string
	}{
		{
			"messages create shows --project in FLAGS",
			[]string{"messages", "create", "--help"},
			commands.NewMessagesCmd,
			[]string{"--project", "--in", "--message-board"},
		},
		{
			"chat post shows --project and --chat in FLAGS",
			[]string{"chat", "post", "--help"},
			commands.NewChatCmd,
			[]string{"--project", "--chat"},
		},
		{
			"timesheet report shows date and person flags in FLAGS",
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
			flagsSection := extractSection(out, "FLAGS")
			for _, flag := range tt.wantFlags {
				assert.Contains(t, flagsSection, flag,
					"expected %s in FLAGS section", flag)
			}
		})
	}
}

func TestCampfirePostHelpShowsChatFlag(t *testing.T) {
	// The campfire alias path must also show --chat in FLAGS.
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewChatCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"campfire", "post", "--help"})
	_ = cmd.Execute()

	out := buf.String()
	flagsSection := extractSection(out, "FLAGS")
	assert.Contains(t, flagsSection, "--chat")
	assert.Contains(t, flagsSection, "--project")
}

func TestLeafCommandKeepsRootGlobalsInInheritedFlags(t *testing.T) {
	// Root-level globals (--json, --quiet, etc.) must stay in INHERITED FLAGS,
	// not be promoted into FLAGS. This is the other half of the parent-scoped
	// promotion contract.
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewProjectsCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"projects", "list", "--help"})
	_ = cmd.Execute()

	out := buf.String()
	inheritedSection := extractSection(out, "INHERITED FLAGS")
	flagsSection := extractSection(out, "FLAGS")

	assert.Contains(t, inheritedSection, "--json",
		"--json should remain in INHERITED FLAGS")
	assert.Contains(t, inheritedSection, "--quiet",
		"--quiet should remain in INHERITED FLAGS")
	assert.NotContains(t, flagsSection, "--json",
		"--json should not be promoted to FLAGS")
	assert.NotContains(t, flagsSection, "--quiet",
		"--quiet should not be promoted to FLAGS")
}

// extractSection returns the text between a header (e.g. "FLAGS") and the
// next header or end of string. Headers are identified as lines that are
// all-caps words (the styled header text).
func extractSection(help, header string) string {
	lines := strings.Split(help, "\n")
	var collecting bool
	var section strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == header {
			collecting = true
			continue
		}
		if collecting && trimmed != "" && trimmed == strings.ToUpper(trimmed) && !strings.HasPrefix(trimmed, "-") {
			break // next header
		}
		if collecting {
			section.WriteString(line)
			section.WriteString("\n")
		}
	}
	return section.String()
}

func TestChatAliasShowsChatHelp(t *testing.T) {
	// Invoking via the old alias "campfire" should still render canonical
	// "basecamp chat" in the help output.
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewChatCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"campfire", "--help"})
	_ = cmd.Execute()

	out := buf.String()
	assert.Contains(t, out, "basecamp chat")
	assert.Contains(t, out, "ALIASES")
	assert.Contains(t, out, "campfire")
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

func TestJSONHelpProducesStructuredJSON(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help", "--json"})
	_ = cmd.Execute()

	out := buf.String()
	assert.True(t, strings.HasPrefix(out, "{"), "json help should produce JSON, got: %s", out)
	assert.Contains(t, out, `"command"`)
}

func TestBareGroupJSONHelpProducesJSON(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewTodosCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"todos", "--json"})
	_ = cmd.Execute()

	out := buf.String()
	assert.True(t, strings.HasPrefix(out, "{"), "bare group --json should produce JSON, got: %s", out)
	assert.Contains(t, out, `"subcommands"`)
}

func TestBareGroupTextHelpUnchanged(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewTodosCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"todos"})
	_ = cmd.Execute()

	out := buf.String()
	assert.Contains(t, out, "COMMANDS")
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

func TestAgentHelpIncludesArgs(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewTodoCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"todo", "--help", "--agent"})
	_ = cmd.Execute()

	out := buf.String()
	assert.Contains(t, out, `"args"`)
	assert.Contains(t, out, `"name":"content"`)
	assert.Contains(t, out, `"required":true`)
	assert.Contains(t, out, `"kind":"text"`)
}

func TestAgentHelpOmitsArgsWhenEmpty(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewProjectsCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"projects", "list", "--help", "--agent"})
	_ = cmd.Execute()

	out := buf.String()
	assert.NotContains(t, out, `"args"`)
}

func TestLeafCommandHelpShowsArguments(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewShowCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"show", "--help"})
	_ = cmd.Execute()

	out := buf.String()
	assert.Contains(t, out, "ARGUMENTS")
	assert.Contains(t, out, "[type]")
	assert.Contains(t, out, "<id|url>")
}

func TestRootHelpContainsJQFlag(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	_ = cmd.Execute()

	out := buf.String()
	assert.Contains(t, out, "--jq")
	assert.Contains(t, out, "Filter JSON with jq expression")
}

func TestBareGroupWithFlagsSuppressesHelp(t *testing.T) {
	// Bare group invocation with explicit local flags suppresses help output
	// so cli.Execute() can emit a usage error. Cobra still returns nil —
	// the error conversion happens in Execute() which calls os.Exit.
	isolateHelpTest(t)

	tests := []struct {
		name   string
		args   []string
		addCmd func() *cobra.Command
	}{
		{"cards --in", []string{"cards", "--in", "myproject"}, commands.NewCardsCmd},
		{"cards --project", []string{"cards", "--project", "myproject"}, commands.NewCardsCmd},
		{"cards --in --json", []string{"cards", "--in", "myproject", "--json"}, commands.NewCardsCmd},
		{"cards --in --agent", []string{"cards", "--in", "myproject", "--agent"}, commands.NewCardsCmd},
		{"messages --in", []string{"messages", "--in", "myproject"}, commands.NewMessagesCmd},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			cmd := NewRootCmd()
			cmd.AddCommand(tt.addCmd())
			cmd.SetOut(&buf)
			cmd.SetArgs(tt.args)

			executedCmd, err := cmd.ExecuteC()
			require.NoError(t, err, "Cobra returns nil for non-runnable commands")
			assert.Empty(t, buf.String(), "help text should be suppressed")
			assert.True(t, isBareGroupWithFlags(executedCmd),
				"Execute() should detect this and convert to a usage error")
		})
	}
}

func TestBareGroupWithoutFlagsShowsHelp(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewCardsCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"cards"})
	err := cmd.Execute()

	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "COMMANDS")
	assert.Contains(t, out, "USAGE")
}

func TestBareGroupWithFlagsAndHelpShowsHelp(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewCardsCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"cards", "--in", "myproject", "--help"})
	err := cmd.Execute()

	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "COMMANDS")
	assert.Contains(t, out, "USAGE")
}

func TestGroupCommandHelpOmitsArguments(t *testing.T) {
	isolateHelpTest(t)

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewPeopleCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"people", "--help"})
	_ = cmd.Execute()

	out := buf.String()
	assert.NotContains(t, out, "ARGUMENTS")
}
