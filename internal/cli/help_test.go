package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/commands"
)

func TestRootHelpContainsCategoryHeaders(t *testing.T) {
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
	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	_ = cmd.Execute()

	out := buf.String()
	assert.Contains(t, out, "basecamp commands")
	assert.Contains(t, out, "basecamp <command> -h")
}

func TestSubcommandGetsDefaultHelp(t *testing.T) {
	var buf bytes.Buffer
	cmd := NewRootCmd()
	todos := commands.NewTodosCmd()
	cmd.AddCommand(todos)
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"todos", "--help"})
	_ = cmd.Execute()

	out := buf.String()
	// Subcommand help should NOT have our curated categories
	assert.NotContains(t, out, "CORE COMMANDS")
	// Should contain the subcommand's own description
	assert.Contains(t, out, "todos")
}

func TestAgentHelpProducesJSON(t *testing.T) {
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
	t.Setenv("BASECAMP_PROFILE", "nonexistent")
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

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
	t.Setenv("BASECAMP_NO_KEYRING", "1")

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
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	var buf bytes.Buffer
	cmd := NewRootCmd()
	cmd.AddCommand(commands.NewQuickStartCmd())
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--agent"})
	err := cmd.Execute()
	require.NoError(t, err)

	assert.NotContains(t, buf.String(), "CORE COMMANDS")
}

func TestRootHelpUsesLiveCommandDescriptions(t *testing.T) {
	// Help descriptions should come from the registered commands' Short field,
	// not from the catalog. This catches drift between catalog copy and actual
	// command metadata.
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
