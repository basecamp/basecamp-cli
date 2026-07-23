package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// setupAgentsEnvelope mirrors the `setup agents` typed result contract.
type setupAgentsEnvelope struct {
	Summary string `json:"summary"`
	Data    struct {
		SkillInstalled  bool     `json:"skill_installed"`
		Selector        string   `json:"selector"`
		Ambiguous       bool     `json:"ambiguous"`
		DetectedBefore  []string `json:"detected_before"`
		AttemptedAgents []string `json:"attempted_agents"`
		Errors          []string `json:"errors"`
		Warnings        []string `json:"warnings"`
		ManualCommands  []string `json:"manual_commands"`
		Agents          []struct {
			ID              string   `json:"id"`
			Name            string   `json:"name"`
			DetectedBefore  bool     `json:"detected_before"`
			DetectedAfter   bool     `json:"detected_after"`
			PluginInstalled bool     `json:"plugin_installed"`
			Errors          []string `json:"errors"`
			ManualCommands  []string `json:"manual_commands"`
		} `json:"agents"`
	} `json:"data"`
}

// runSetupAgentsJSON executes `setup agents` in machine mode and parses the envelope.
func runSetupAgentsJSON(t *testing.T) setupAgentsEnvelope {
	t.Helper()
	app, out := setupQuickstartTestApp(t, "", "")
	app.Flags.JSON = true
	app.Flags.Hints = true
	t.Cleanup(app.Close)

	cmd := NewSetupCmd()
	cmd.SetArgs([]string{"agents"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())

	var envelope setupAgentsEnvelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope), out.String())
	return envelope
}

// runSetupAgentsStyled executes `setup agents` rendering the styled (ANSI)
// output — the real installer path (curl | bash: piped stdin, TTY stdout).
func runSetupAgentsStyled(t *testing.T) string {
	t.Helper()
	app, out := setupQuickstartTestApp(t, "", "")
	app.Output = output.New(output.Options{Format: output.FormatStyled, Writer: out})
	t.Cleanup(app.Close)

	cmd := NewSetupCmd()
	cmd.SetArgs([]string{"agents"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())
	return out.String()
}

// emptyHome points HOME and PATH at empty temp dirs so no agent is detected.
func emptyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", filepath.Join(home, "empty-bin"))
	return home
}

func TestNewSetupCmdHasAgentsSubcommand(t *testing.T) {
	assert.NotNil(t, findSubcommand(NewSetupCmd(), "agents"))
}

// TestSetupAgentsRejectsPositionalArgs: selection is env-driven, so stray args
// (typos, or confusion with `setup <id>`) are rejected rather than ignored.
func TestSetupAgentsRejectsPositionalArgs(t *testing.T) {
	emptyHome(t)
	app, _ := setupQuickstartTestApp(t, "", "")
	app.Flags.JSON = true
	t.Cleanup(app.Close)

	cmd := NewSetupCmd()
	cmd.SetArgs([]string{"agents", "codex"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	assert.Error(t, cmd.Execute(), "unexpected positional arg should be rejected")
}

// TestSetupAgentsStyledSurfacesFailure is the styled-output regression guard:
// a per-agent failure must reach the human-facing output via a top-level flat
// field, since the styled renderer skips the nested `agents` array.
func TestSetupAgentsStyledSurfacesFailure(t *testing.T) {
	installCodexStub(t, codexStubOptions{marketplaceFailure: true})
	t.Setenv("BASECAMP_SETUP_AGENT", "codex")

	out := runSetupAgentsStyled(t)

	assert.Contains(t, out, "codex")
	assert.Contains(t, out, "marketplace add", "per-agent failure must survive styled rendering via top-level errors")
}

func TestSetupAgentsZeroDetected(t *testing.T) {
	emptyHome(t)
	t.Setenv("BASECAMP_SETUP_AGENT", "")

	env := runSetupAgentsJSON(t)

	assert.True(t, env.Data.SkillInstalled)
	assert.Equal(t, "auto", env.Data.Selector)
	assert.False(t, env.Data.Ambiguous)
	assert.Empty(t, env.Data.DetectedBefore)
	assert.Empty(t, env.Data.AttemptedAgents)
}

// TestSetupAgentsBaselineSkillFailure forces installSkillFiles to fail by making
// ~/.agents a regular file, so MkdirAll under it errors.
func TestSetupAgentsBaselineSkillFailure(t *testing.T) {
	home := emptyHome(t)
	t.Setenv("BASECAMP_SETUP_AGENT", "none")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".agents"), []byte("blocker"), 0o644))

	env := runSetupAgentsJSON(t)

	assert.False(t, env.Data.SkillInstalled)
	require.NotEmpty(t, env.Data.Errors)
	assert.Contains(t, env.Data.Errors[0], "skill:")
}

func TestSetupAgentsSingleDetectedCodex(t *testing.T) {
	installCodexStub(t, codexStubOptions{})
	t.Setenv("BASECAMP_SETUP_AGENT", "")

	env := runSetupAgentsJSON(t)

	assert.Equal(t, "auto", env.Data.Selector)
	assert.False(t, env.Data.Ambiguous)
	assert.Equal(t, []string{"codex"}, env.Data.AttemptedAgents)
	require.Len(t, env.Data.Agents, 1)
	assert.Equal(t, "codex", env.Data.Agents[0].ID)
	assert.True(t, env.Data.Agents[0].DetectedBefore)
	assert.True(t, env.Data.Agents[0].DetectedAfter)
	assert.True(t, env.Data.Agents[0].PluginInstalled)
}

// TestSetupAgentsAmbiguous verifies that ≥2 detected agents with no selector
// never guesses — the flat data surfaces both choices.
func TestSetupAgentsAmbiguous(t *testing.T) {
	home := emptyHome(t)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex"), 0o755))
	t.Setenv("BASECAMP_SETUP_AGENT", "")

	env := runSetupAgentsJSON(t)

	assert.Equal(t, "auto", env.Data.Selector)
	assert.True(t, env.Data.Ambiguous)
	assert.Empty(t, env.Data.AttemptedAgents)
	assert.Equal(t, []string{"claude", "codex"}, env.Data.DetectedBefore)
	assert.Equal(t, []string{"basecamp setup claude", "basecamp setup codex"}, env.Data.ManualCommands)
	assert.NotEmpty(t, env.Data.Warnings)
}

// TestSetupAgentsAllForcesEveryHandler covers =all with zero, one, and two
// detected agents: every registered handler is attempted, and absent-binary
// agents produce synthesized remediation.
func TestSetupAgentsAllForcesEveryHandler(t *testing.T) {
	t.Run("zero detected", func(t *testing.T) {
		emptyHome(t)
		t.Setenv("BASECAMP_SETUP_AGENT", "all")

		env := runSetupAgentsJSON(t)

		assert.Equal(t, "all", env.Data.Selector)
		assert.Equal(t, []string{"claude", "codex"}, env.Data.AttemptedAgents)
		// Both binaries absent → symmetric synthesized remediation.
		assert.Contains(t, env.Data.ManualCommands, "basecamp setup claude")
		assert.Contains(t, env.Data.ManualCommands, "basecamp setup codex")
		require.GreaterOrEqual(t, len(env.Data.Warnings), 2)
	})

	t.Run("one detected", func(t *testing.T) {
		installCodexStub(t, codexStubOptions{}) // codex binary present, claude absent
		t.Setenv("BASECAMP_SETUP_AGENT", "all")

		env := runSetupAgentsJSON(t)

		assert.Equal(t, []string{"claude", "codex"}, env.Data.AttemptedAgents)
		// Claude binary absent → synthesized; codex present and healthy → not.
		assert.Contains(t, env.Data.ManualCommands, "basecamp setup claude")
		assert.NotContains(t, env.Data.ManualCommands, "basecamp setup codex")
	})

	t.Run("two detected", func(t *testing.T) {
		home := emptyHome(t)
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex"), 0o755))
		t.Setenv("BASECAMP_SETUP_AGENT", "all")

		env := runSetupAgentsJSON(t)

		assert.False(t, env.Data.Ambiguous, "explicit selector is never ambiguous")
		assert.Equal(t, []string{"claude", "codex"}, env.Data.AttemptedAgents)
		assert.Contains(t, env.Data.ManualCommands, "basecamp setup claude")
		assert.Contains(t, env.Data.ManualCommands, "basecamp setup codex")
	})
}

// TestSetupAgentsCodexMissingBinary asserts the real missing-binary contract:
// the agentSetupError is unioned into top-level errors + a warning, and the
// deduped remediation is the single `basecamp setup codex` (not the 3-command seq).
func TestSetupAgentsCodexMissingBinary(t *testing.T) {
	emptyHome(t)
	t.Setenv("BASECAMP_SETUP_AGENT", "codex")

	env := runSetupAgentsJSON(t)

	assert.Equal(t, "codex", env.Data.Selector)
	require.NotEmpty(t, env.Data.Errors)
	assert.Contains(t, env.Data.Errors[0], "codex: ")
	assert.Contains(t, env.Data.Errors[0], "Codex executable not found")
	assert.NotEmpty(t, env.Data.Warnings)
	assert.Equal(t, []string{"basecamp setup codex"}, env.Data.ManualCommands)
}

// TestSetupAgentsCodexPreservesManualOrder guards against string-sorting: when
// marketplace add fails, the aggregate manual_commands must preserve Codex's own
// ordered remediation sequence (marketplace add → upgrade → plugin add).
func TestSetupAgentsCodexPreservesManualOrder(t *testing.T) {
	installCodexStub(t, codexStubOptions{marketplaceFailure: true})
	t.Setenv("BASECAMP_SETUP_AGENT", "codex")

	env := runSetupAgentsJSON(t)

	assert.Equal(t, []string{
		"codex plugin marketplace add basecamp/claude-plugins",
		"codex plugin marketplace upgrade 37signals",
		"codex plugin add basecamp@37signals",
	}, env.Data.ManualCommands)
}

// TestSetupAgentsClaudeNoBinary asserts the real Claude contract: no binary means
// plugin_installed:false, but linkSkillToClaude creates ~/.claude/skills/basecamp
// so detection flips to true, and missing-binary remediation is synthesized.
func TestSetupAgentsClaudeNoBinary(t *testing.T) {
	home := emptyHome(t)
	t.Setenv("BASECAMP_SETUP_AGENT", "claude")

	env := runSetupAgentsJSON(t)

	require.Len(t, env.Data.Agents, 1)
	claude := env.Data.Agents[0]
	assert.Equal(t, "claude", claude.ID)
	assert.False(t, claude.DetectedBefore)
	assert.True(t, claude.DetectedAfter, "linkSkillToClaude creates ~/.claude, flipping detection")
	assert.False(t, claude.PluginInstalled)

	// linkSkillToClaude created the skill link.
	_, statErr := os.Stat(filepath.Join(home, ".claude", "skills", "basecamp"))
	assert.NoError(t, statErr)

	assert.NotEmpty(t, env.Data.Warnings)
	assert.Contains(t, env.Data.ManualCommands, "basecamp setup claude")
}

func TestSetupAgentsNoneSelector(t *testing.T) {
	home := emptyHome(t)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex"), 0o755))
	t.Setenv("BASECAMP_SETUP_AGENT", "none")

	env := runSetupAgentsJSON(t)

	assert.Equal(t, "none", env.Data.Selector)
	assert.True(t, env.Data.SkillInstalled)
	assert.Empty(t, env.Data.AttemptedAgents)
	assert.Equal(t, []string{"codex"}, env.Data.DetectedBefore)
}

func TestSetupAgentsInvalidSelector(t *testing.T) {
	emptyHome(t)
	t.Setenv("BASECAMP_SETUP_AGENT", "frobnicate")

	env := runSetupAgentsJSON(t)

	assert.Equal(t, "invalid", env.Data.Selector)
	assert.True(t, env.Data.SkillInstalled)
	assert.Empty(t, env.Data.AttemptedAgents)
	require.NotEmpty(t, env.Data.Warnings)
	assert.Contains(t, env.Data.Warnings[0], "frobnicate")
}
