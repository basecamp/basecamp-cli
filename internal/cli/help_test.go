package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestSubcommandGetsDefaultHelp(t *testing.T) {
	isolateHelpTest(t)

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
