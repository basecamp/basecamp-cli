package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skillLoadLine is the transcript line Claude Code writes when a skill loads.
func skillLoadLine(t *testing.T, dir string) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{"role": "user", "content": []map[string]string{
			{"type": "text", "text": "Base directory for this skill: " + dir + "\n\n# /basecamp-connect"},
		}},
	})
	require.NoError(t, err)
	return string(line)
}

func connectorSkillDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "skills", "basecamp-connect")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# skill\n"), 0o644))
	return dir
}

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	return path
}

func sessionStart(t *testing.T, source, transcript string) string {
	t.Helper()
	t.Setenv("CLAUDE_PLUGIN_ROOT", "/home/u/.claude/plugins/cache/37signals/basecamp-cli/0.13.0")
	t.Setenv("CLAUDE_PLUGIN_DATA", t.TempDir())
	payload, err := json.Marshal(map[string]string{
		"hook_event_name": "SessionStart", "session_id": "s", "source": source, "transcript_path": transcript,
	})
	require.NoError(t, err)
	return runAgentHookPreservingHome(t, string(payload))
}

func TestAgentHookConnectorReloadAfterCompaction(t *testing.T) {
	dir := connectorSkillDir(t)
	transcript := writeTranscript(t, `{"type":"user","message":{"content":"hi"}}`, skillLoadLine(t, dir))

	event, context := hookSpecificOutput(t, sessionStart(t, "compact", transcript))
	assert.Equal(t, "SessionStart", event)
	assert.Contains(t, context, "read "+filepath.Join(dir, "SKILL.md")+" in full")
	assert.Less(t, len(context), 10000, "Claude Code caps additionalContext at 10,000 characters")
}

func TestAgentHookConnectorReloadNamesTheLastSkillLoaded(t *testing.T) {
	first, last := connectorSkillDir(t), connectorSkillDir(t)
	transcript := writeTranscript(t, skillLoadLine(t, first), skillLoadLine(t, last))

	_, context := hookSpecificOutput(t, sessionStart(t, "compact", transcript))
	assert.Contains(t, context, filepath.Join(last, "SKILL.md"))
	assert.NotContains(t, context, filepath.Join(first, "SKILL.md"))
}

func TestAgentHookConnectorReloadOnlyAfterCompaction(t *testing.T) {
	transcript := writeTranscript(t, skillLoadLine(t, connectorSkillDir(t)))
	for _, source := range []string{"startup", "resume", "clear", ""} {
		assert.Empty(t, sessionStart(t, source, transcript), source)
	}
}

func TestAgentHookConnectorReloadSilentForOtherSessions(t *testing.T) {
	dir := connectorSkillDir(t)
	other := filepath.Join(filepath.Dir(dir), "basecamp")
	require.NoError(t, os.MkdirAll(other, 0o755))
	// A tool result that quotes a skill load escapes its quotes, so it isn't one.
	quoted, err := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": []map[string]any{
		{"type": "tool_result", "content": skillLoadLine(t, dir)},
	}}})
	require.NoError(t, err)

	for name, transcript := range map[string]string{
		"no skill":          writeTranscript(t, `{"type":"user","message":{"content":"hi"}}`),
		"another skill":     writeTranscript(t, skillLoadLine(t, other)),
		"quoted skill load": writeTranscript(t, string(quoted)),
		"missing":           filepath.Join(t.TempDir(), "absent.jsonl"),
		"no transcript":     "",
	} {
		assert.Empty(t, sessionStart(t, "compact", transcript), name)
	}
}

func TestAgentHookConnectorReloadSilentWhenSkillFileIsGone(t *testing.T) {
	dir := connectorSkillDir(t)
	transcript := writeTranscript(t, skillLoadLine(t, dir))
	require.NoError(t, os.Remove(filepath.Join(dir, "SKILL.md")))

	assert.Empty(t, sessionStart(t, "compact", transcript))
}

func TestAgentHookConnectorReloadReadsLinesLongerThanTheBuffer(t *testing.T) {
	dir := connectorSkillDir(t)
	long := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", 3<<20) + `"}}`
	transcript := writeTranscript(t, long, skillLoadLine(t, dir), long)

	_, context := hookSpecificOutput(t, sessionStart(t, "compact", transcript))
	assert.Contains(t, context, filepath.Join(dir, "SKILL.md"))
}
