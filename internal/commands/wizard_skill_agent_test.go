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
	"github.com/basecamp/basecamp-cli/internal/harness"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// runSetupSkillAgentJSON executes `setup <id>` in machine mode for a
// shared-skill agent and parses the envelope `setup codex` also answers.
func runSetupSkillAgentJSON(t *testing.T, agent harness.SkillAgent) setupCodexEnvelope {
	t.Helper()
	app, output := setupQuickstartTestApp(t, "", "")
	app.Flags.JSON = true
	app.Flags.Hints = true
	t.Cleanup(app.Close)

	cmd := NewSetupCmd()
	cmd.SetArgs([]string{agent.ID})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())

	var envelope setupCodexEnvelope
	require.NoError(t, json.Unmarshal(output.Bytes(), &envelope), output.String())
	return envelope
}

func TestNewSetupCmdHasSkillAgentSubcommands(t *testing.T) {
	forEachSkillAgent(t, func(t *testing.T, agent harness.SkillAgent) {
		sub := findSubcommand(NewSetupCmd(), agent.ID)
		require.NotNil(t, sub)
		assert.Equal(t, "Connect "+agent.Name+" to Basecamp", sub.Short)
	})
}

func TestSetupSkillAgentHandlerIsInTheTable(t *testing.T) {
	forEachSkillAgent(t, func(t *testing.T, agent harness.SkillAgent) {
		handler, ok := agentSetupHandlers[agent.ID]
		require.True(t, ok)
		assert.Equal(t, []string{"Install the shared Basecamp skill for " + agent.Name}, handler.Labels)
		assert.NotNil(t, handler.Run)
		assert.NotNil(t, handler.RunNonInteractive)
	})
	// The plugin agents keep their own handlers alongside.
	assert.Contains(t, agentSetupHandlers, "claude")
	assert.Contains(t, agentSetupHandlers, "codex")
}

// `setup <id>` on a machine without the agent installs the shared skill,
// reports the agent missing with its own remediation, and leaves no trace
// of the agent behind: fabricating its home would make every later
// detection — and this command's own verdict — report it installed.
func TestSetupSkillAgentNotDetectedDoesNotFabricateHome(t *testing.T) {
	forEachSkillAgent(t, func(t *testing.T, agent harness.SkillAgent) {
		home := emptyHome(t)
		t.Setenv(agent.HomeEnv, "")

		envelope := runSetupSkillAgentJSON(t, agent)

		assert.False(t, envelope.Data.AgentDetected)
		assert.False(t, envelope.Data.PluginInstalled)
		assert.Equal(t, agent.Name+" not detected", envelope.Summary)
		require.Len(t, envelope.Data.Errors, 1)
		assert.Contains(t, envelope.Data.Errors[0], agent.Name+" not detected")
		assert.Equal(t, []string{"basecamp setup " + agent.ID}, envelope.Data.ManualCommands)
		assert.FileExists(t, filepath.Join(home, ".agents", "skills", "basecamp", "SKILL.md"), "the shared skill is installed regardless")
		assert.NoFileExists(t, filepath.Join(home, agent.HomeDir))
	})
}

func TestSetupSkillAgentDetectedByHomeConnects(t *testing.T) {
	forEachSkillAgent(t, func(t *testing.T, agent harness.SkillAgent) {
		home := emptyHome(t)
		t.Setenv(agent.HomeEnv, "")
		require.NoError(t, os.MkdirAll(filepath.Join(home, agent.HomeDir), 0o755))

		envelope := runSetupSkillAgentJSON(t, agent)

		assert.True(t, envelope.Data.AgentDetected)
		assert.True(t, envelope.Data.PluginInstalled)
		assert.Empty(t, envelope.Data.Errors)
		assert.Empty(t, envelope.Data.ManualCommands)
		assert.Equal(t, agent.Name+" connected", envelope.Summary)
		require.NotEmpty(t, envelope.Breadcrumbs)
		assert.Equal(t, "basecamp doctor", envelope.Breadcrumbs[0].Cmd)
		assert.NoFileExists(t, filepath.Join(home, agent.HomeDir, "skills"), "the skill is not copied into the agent's home; it reads ~/.agents directly")
	})
}

// A relocated home ($GROK_HOME) detects the agent the same way, and a binary
// alone — no home directory yet — is enough too.
func TestSetupSkillAgentDetectedByOverrideOrBinary(t *testing.T) {
	forEachSkillAgent(t, func(t *testing.T, agent harness.SkillAgent) {
		t.Run("home override", func(t *testing.T) {
			emptyHome(t)
			t.Setenv(agent.HomeEnv, t.TempDir())

			envelope := runSetupSkillAgentJSON(t, agent)

			assert.True(t, envelope.Data.AgentDetected)
			assert.True(t, envelope.Data.PluginInstalled)
		})
		t.Run("binary on PATH", func(t *testing.T) {
			emptyHome(t)
			t.Setenv(agent.HomeEnv, "")
			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, agent.Binary), []byte("#!/bin/sh\n"), 0o755)) //nolint:gosec // G306: test stub must be executable
			t.Setenv("PATH", bin)

			envelope := runSetupSkillAgentJSON(t, agent)

			assert.True(t, envelope.Data.AgentDetected)
			assert.True(t, envelope.Data.PluginInstalled)
		})
	})
}

// The interactive handler warns and continues: `basecamp setup` must never
// abort on one agent, and the post-setup snapshot is what reports the miss.
func TestSkillAgentSetupHandlerWarnsAndContinues(t *testing.T) {
	forEachSkillAgent(t, func(t *testing.T, agent harness.SkillAgent) {
		home := emptyHome(t)
		t.Setenv(agent.HomeEnv, "")
		styles := tui.NewStylesWithTheme(tui.ResolveTheme(false))
		handler := agentSetupHandlers[agent.ID]

		run := func() string {
			var out bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			require.NoError(t, handler.Run(cmd, styles))
			return out.String()
		}

		out := run()
		assert.Contains(t, out, agent.Name+" skill setup failed")
		assert.Contains(t, out, agent.Name+" not detected")
		assert.Contains(t, out, "basecamp doctor")
		assert.NoFileExists(t, filepath.Join(home, agent.HomeDir))

		// Detected but the shared skill missing is its own failure; the caller
		// installs the skill first, and this step only confirms it.
		require.NoError(t, os.MkdirAll(filepath.Join(home, agent.HomeDir), 0o755))
		out = run()
		assert.Contains(t, out, "shared Basecamp skill is not installed")

		_, err := installSkillFiles()
		require.NoError(t, err)
		out = run()
		assert.Contains(t, out, agent.Name+" skill installed ("+harness.AgentSkillPath()+")")
		assert.Contains(t, out, "Start a new "+agent.Name+" session")
	})
}
