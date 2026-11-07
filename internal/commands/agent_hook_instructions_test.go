package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
)

// runAgentHookWithConfig runs the plugin's SessionStart hook on cfg.
func runAgentHookWithConfig(t *testing.T, cfg *config.Config, input string) string {
	t.Helper()
	t.Setenv("CLAUDE_PLUGIN_ROOT", "/home/u/.claude/plugins/cache/37signals/basecamp-cli/0.12.0")
	t.Setenv("CLAUDE_PLUGIN_DATA", t.TempDir())
	t.Setenv("PLUGIN_ROOT", "")
	app := appctx.NewApp(cfg)
	t.Cleanup(app.Close)
	cmd := NewAgentHookCmd()
	var stdout bytes.Buffer
	cmd.SetIn(strings.NewReader(input))
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"pre-commit-snapshot"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	require.NoError(t, cmd.Execute())
	return strings.TrimSpace(stdout.String())
}

func TestAgentHookPointsAtInstructionsWhenSet(t *testing.T) {
	cfg := config.Default()
	cfg.Instructions = "https://github.com/basecamp/coworker/blob/main/docs/basecamp-style.md"

	for _, source := range []string{"startup", "resume", "clear", "compact"} {
		output := runAgentHookWithConfig(t, cfg, `{"hook_event_name":"SessionStart","session_id":"s","source":"`+source+`"}`)
		require.NotEmpty(t, output, source)
		event, context := hookSpecificOutput(t, output)
		assert.Equal(t, "SessionStart", event)
		assert.Contains(t, context, "`basecamp instructions`", source)
		assert.Contains(t, context, "script or tool that posts it", source)
		var reply map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(output), &reply), source)
		assert.NotContains(t, reply, "systemMessage", "the pointer is for the model, not the user")
	}
}

func TestAgentHookPointsAtProfileInstructions(t *testing.T) {
	cfg := config.Default()
	cfg.ProfileInstructions = "/home/u/agent-voice.md"

	_, context := hookSpecificOutput(t, runAgentHookWithConfig(t, cfg, sessionStartPayload))
	assert.Contains(t, context, "`basecamp instructions`")
}

// A session that starts on a profile with no instructions can still post as
// one that has some, so instructions set on any configured profile count.
func TestAgentHookPointsAtAnotherProfilesInstructions(t *testing.T) {
	cfg := config.Default()
	cfg.Profiles = map[string]*config.ProfileConfig{
		"human": {BaseURL: "https://3.basecampapi.com"},
		"agent": {BaseURL: "https://3.basecampapi.com", Instructions: "/home/u/agent-voice.md"},
	}
	require.Empty(t, cfg.ProfileInstructions, "the active profile has none of its own")

	_, context := hookSpecificOutput(t, runAgentHookWithConfig(t, cfg, sessionStartPayload))
	assert.Contains(t, context, "`basecamp instructions`")
	assert.Contains(t, context, "-P <profile>")
}

func TestAgentHookSilentWhenNoProfileHasInstructions(t *testing.T) {
	cfg := config.Default()
	cfg.Profiles = map[string]*config.ProfileConfig{
		"human": {BaseURL: "https://3.basecampapi.com"},
		"agent": {BaseURL: "https://3.basecampapi.com"},
	}
	assert.Empty(t, runAgentHookWithConfig(t, cfg, sessionStartPayload))
}

func TestAgentHookSilentWithoutInstructions(t *testing.T) {
	assert.Empty(t, runAgentHookWithConfig(t, config.Default(), sessionStartPayload))
}

func TestAgentHookInstructionsPointerOnlyAtSessionStart(t *testing.T) {
	cfg := config.Default()
	cfg.Instructions = "/home/u/style.md"

	output := runAgentHookWithConfig(t, cfg,
		`{"hook_event_name":"PreToolUse","session_id":"s","tool_use_id":"t","tool_input":{"command":"basecamp comments create 1 hi"}}`)
	assert.Empty(t, output)
}
