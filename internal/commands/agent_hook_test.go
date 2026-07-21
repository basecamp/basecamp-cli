package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
)

func TestAgentHookCommandIsHidden(t *testing.T) {
	cmd := NewAgentHookCmd()

	assert.True(t, cmd.Hidden)
	assert.Equal(t, "agent-hook", cmd.Name())
	assert.NotNil(t, findSubcommand(cmd, "session-start"))
	assert.NotNil(t, findSubcommand(cmd, "pre-commit-snapshot"))
	assert.NotNil(t, findSubcommand(cmd, "post-commit"))
}

func TestAgentHookSessionStartReportsAuthenticated(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "test-token")

	output := runAgentHook(t, "session-start", "", "")

	event, context := hookSpecificOutput(t, output)
	assert.Equal(t, "SessionStart", event)
	assert.Contains(t, context, "Basecamp is active")
	assert.Contains(t, context, "OAuth is ready")
	assert.NotContains(t, context, "test-token")
}

func TestAgentHookSessionStartReportsUnauthenticated(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")

	output := runAgentHook(t, "session-start", "", "")

	_, context := hookSpecificOutput(t, output)
	assert.Contains(t, context, "Basecamp is active")
	assert.Contains(t, context, "basecamp auth login")
}

func TestAgentHookNudgesWithCodexPayload(t *testing.T) {
	repo := newGitRepo(t, "main", "initial")
	t.Setenv("CLAUDE_PLUGIN_DATA", t.TempDir())

	pre := `{"session_id":"s1","tool_use_id":"t1","hook_event_name":"PreToolUse",` +
		`"tool_input":{"command":"git commit -m 'BC-123 ship'"},"turn_id":"turn-1","model":"gpt-5"}`
	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", pre, repo))

	commitFile(t, repo, "feature.txt", "BC-123 ship native hooks")

	post := `{"session_id":"s1","tool_use_id":"t1","hook_event_name":"PostToolUse",` +
		`"tool_input":{"command":"git commit -m 'BC-123 ship'"},` +
		`"tool_response":"[main abc1234] BC-123 ship","turn_id":"turn-1","model":"gpt-5"}`
	event, context := hookSpecificOutput(t, runAgentHook(t, "post-commit", post, repo))
	assert.Equal(t, "PostToolUse", event)
	assert.Contains(t, context, "BC-123")
	assert.Contains(t, context, "Nothing was posted")
}

func TestAgentHookNudgesWithClaudePayload(t *testing.T) {
	repo := newGitRepo(t, "main", "initial")
	t.Setenv("CLAUDE_PLUGIN_DATA", t.TempDir())

	pre := `{"session_id":"s2","tool_use_id":"t2","hook_event_name":"PreToolUse",` +
		`"tool_input":{"command":"git commit -m 'BC-77 ship'"}}`
	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", pre, repo))

	commitFile(t, repo, "feature.txt", "BC-77 ship native hooks")

	post := `{"session_id":"s2","tool_use_id":"t2","hook_event_name":"PostToolUse",` +
		`"tool_input":{"command":"git commit -m 'BC-77 ship'"},` +
		`"tool_response":{"stdout":"[main abc1234] BC-77 ship","stderr":""}}`
	event, context := hookSpecificOutput(t, runAgentHook(t, "post-commit", post, repo))
	assert.Equal(t, "PostToolUse", event)
	assert.Contains(t, context, "BC-77")
}

func TestAgentHookNudgesOnPostToolUseFailure(t *testing.T) {
	repo := newGitRepo(t, "main", "initial")
	t.Setenv("CLAUDE_PLUGIN_DATA", t.TempDir())

	pre := agentHookPayload("PreToolUse", "s3", "t3", "git commit -m ship && git push")
	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", pre, repo))

	// Simulate `git commit && git push` where the commit landed but the push
	// failed: HEAD advanced, then the tool reported failure.
	commitFile(t, repo, "feature.txt", "todo-456 ship")

	post := agentHookPayload("PostToolUseFailure", "s3", "t3", "git commit -m ship && git push")
	event, context := hookSpecificOutput(t, runAgentHook(t, "post-commit", post, repo))
	assert.Equal(t, "PostToolUseFailure", event)
	assert.Contains(t, context, "todo-456")
}

func TestAgentHookUsesBranchReference(t *testing.T) {
	repo := newGitRepo(t, "basecamp-789-native-hook", "initial")
	t.Setenv("CLAUDE_PLUGIN_DATA", t.TempDir())

	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", agentHookPayload("PreToolUse", "s", "t", "git commit -m ship"), repo))
	commitFile(t, repo, "feature.txt", "ship native hooks")

	_, context := hookSpecificOutput(t, runAgentHook(t, "post-commit", agentHookPayload("PostToolUse", "s", "t", "git commit -m ship"), repo))
	assert.Contains(t, context, "basecamp-789")
}

func TestAgentHookStaysSilentWhenCommitFails(t *testing.T) {
	repo := newGitRepo(t, "main", "BC-123 initial")
	t.Setenv("CLAUDE_PLUGIN_DATA", t.TempDir())

	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", agentHookPayload("PreToolUse", "s", "t", "git commit -m ship"), repo))

	// No commit happens: HEAD is unchanged, so no nudge even though the
	// branch history already references BC-123.
	assert.Empty(t, runAgentHook(t, "post-commit", agentHookPayload("PostToolUse", "s", "t", "git commit -m ship"), repo))
}

func TestAgentHookDoesNotRenudgeAfterFailedSecondCommit(t *testing.T) {
	repo := newGitRepo(t, "main", "initial")
	t.Setenv("CLAUDE_PLUGIN_DATA", t.TempDir())

	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", agentHookPayload("PreToolUse", "s", "t1", "git commit -m ship"), repo))
	commitFile(t, repo, "feature.txt", "BC-123 ship")
	_, context := hookSpecificOutput(t, runAgentHook(t, "post-commit", agentHookPayload("PostToolUse", "s", "t1", "git commit -m ship"), repo))
	assert.Contains(t, context, "BC-123")

	// Second tool call tries another commit that fails: HEAD stays at the
	// already-nudged commit, so no duplicate nudge.
	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", agentHookPayload("PreToolUse", "s", "t2", "git commit -m again"), repo))
	assert.Empty(t, runAgentHook(t, "post-commit", agentHookPayload("PostToolUse", "s", "t2", "git commit -m again"), repo))
}

func TestAgentHookRejectsHeadMovedByCheckout(t *testing.T) {
	repo := newGitRepo(t, "main", "initial")
	t.Setenv("CLAUDE_PLUGIN_DATA", t.TempDir())
	runGit(t, repo, "checkout", "-q", "-b", "bc-42-feature")
	commitFile(t, repo, "feature.txt", "feature work")
	runGit(t, repo, "checkout", "-q", "main")

	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", agentHookPayload("PreToolUse", "s", "t", "git checkout bc-42-feature # then commit"), repo))
	runGit(t, repo, "checkout", "-q", "bc-42-feature")

	// HEAD moved, but the last reflog action is a checkout, not a commit.
	assert.Empty(t, runAgentHook(t, "post-commit", agentHookPayload("PostToolUse", "s", "t", "git checkout bc-42-feature # then commit"), repo))
}

func TestAgentHookNudgesAfterAmend(t *testing.T) {
	repo := newGitRepo(t, "main", "initial")
	t.Setenv("CLAUDE_PLUGIN_DATA", t.TempDir())

	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", agentHookPayload("PreToolUse", "s", "t", "git commit --amend"), repo))
	runGit(t, repo, "commit", "--amend", "-m", "BC-321 amended")

	_, context := hookSpecificOutput(t, runAgentHook(t, "post-commit", agentHookPayload("PostToolUse", "s", "t", "git commit --amend"), repo))
	assert.Contains(t, context, "BC-321")
}

func TestAgentHookStaysSilentWithoutSnapshot(t *testing.T) {
	repo := newGitRepo(t, "main", "BC-123 initial")
	t.Setenv("CLAUDE_PLUGIN_DATA", t.TempDir())

	assert.Empty(t, runAgentHook(t, "post-commit", agentHookPayload("PostToolUse", "s", "t", "git commit -m ship"), repo))
}

func TestAgentHookRemovesExpiredSnapshots(t *testing.T) {
	repo := newGitRepo(t, "main", "initial")
	data := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", data)
	stateDir := filepath.Join(data, "commit-snapshots")
	require.NoError(t, os.MkdirAll(stateDir, 0o700))
	expired := filepath.Join(stateDir, strings.Repeat("0", 64))
	require.NoError(t, os.WriteFile(expired, []byte("stale\n"), 0o600))
	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(expired, old, old))

	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", agentHookPayload("PreToolUse", "s", "t", "git commit -m ship"), repo))

	_, err := os.Stat(expired)
	assert.ErrorIs(t, err, os.ErrNotExist)
	entries, err := os.ReadDir(stateDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestAgentHookSnapshotFileIsPrivateAndHashNamed(t *testing.T) {
	repo := newGitRepo(t, "main", "initial")
	data := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", data)

	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", agentHookPayload("PreToolUse", "secret-session", "secret-tool-use", "git commit -m ship"), repo))

	entries, err := os.ReadDir(filepath.Join(data, "commit-snapshots"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	name := entries[0].Name()
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), name)
	assert.NotContains(t, name, "secret")
	if runtime.GOOS != "windows" {
		info, err := entries[0].Info()
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestAgentHookPrefilterSkipsNonCommitCommands(t *testing.T) {
	repo := newGitRepo(t, "main", "initial")
	data := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", data)

	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", agentHookPayload("PreToolUse", "s", "t", "git status"), repo))

	_, err := os.Stat(filepath.Join(data, "commit-snapshots"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestAgentHookIgnoresNonRepository(t *testing.T) {
	data := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", data)

	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", agentHookPayload("PreToolUse", "s", "t", "git commit -m BC-123"), t.TempDir()))

	_, err := os.Stat(filepath.Join(data, "commit-snapshots"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestAgentHookStaysSilentWithoutReference(t *testing.T) {
	repo := newGitRepo(t, "main", "initial")
	t.Setenv("CLAUDE_PLUGIN_DATA", t.TempDir())

	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", agentHookPayload("PreToolUse", "s", "t", "git commit -m ship"), repo))
	commitFile(t, repo, "feature.txt", "ship without reference")

	assert.Empty(t, runAgentHook(t, "post-commit", agentHookPayload("PostToolUse", "s", "t", "git commit -m ship"), repo))
}

func TestAgentHookNudgesOnFirstCommitFromUnbornHead(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "agent-hook@example.com")
	runGit(t, repo, "config", "user.name", "Agent Hook Test")
	data := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", data)

	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", agentHookPayload("PreToolUse", "s", "t", "git commit -m 'BC-9 first'"), repo))

	entries, err := os.ReadDir(filepath.Join(data, "commit-snapshots"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	snapshot, err := os.ReadFile(filepath.Join(data, "commit-snapshots", entries[0].Name()))
	require.NoError(t, err)
	assert.Equal(t, "EMPTY", strings.TrimSpace(string(snapshot)))

	commitFile(t, repo, "README.md", "BC-9 first commit")

	_, context := hookSpecificOutput(t, runAgentHook(t, "post-commit", agentHookPayload("PostToolUse", "s", "t", "git commit -m 'BC-9 first'"), repo))
	assert.Contains(t, context, "BC-9")
}

func TestAgentHookIgnoresMalformedInput(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_DATA", t.TempDir())

	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", "not json", ""))
	assert.Empty(t, runAgentHook(t, "post-commit", "not json", ""))
}

func TestAgentHookIgnoresOversizedInput(t *testing.T) {
	data := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", data)
	oversized := `{"session_id":"s","tool_use_id":"t","cwd":"` + strings.Repeat("a", maxAgentHookInput) + `"}`

	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", oversized, ""))
	assert.Empty(t, runAgentHook(t, "post-commit", oversized, ""))

	_, err := os.Stat(filepath.Join(data, "commit-snapshots"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestAgentHookFallsBackToCacheDirState(t *testing.T) {
	repo := newGitRepo(t, "main", "initial")
	t.Setenv("CLAUDE_PLUGIN_DATA", "")
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)

	assert.Empty(t, runAgentHook(t, "pre-commit-snapshot", agentHookPayload("PreToolUse", "s", "t", "git commit -m ship"), repo))

	entries, err := os.ReadDir(filepath.Join(cache, "basecamp", "agent-hook"))
	require.NoError(t, err)
	assert.Len(t, entries, 1)

	commitFile(t, repo, "feature.txt", "BC-55 ship")

	_, context := hookSpecificOutput(t, runAgentHook(t, "post-commit", agentHookPayload("PostToolUse", "s", "t", "git commit -m ship"), repo))
	assert.Contains(t, context, "BC-55")
}

func agentHookPayload(event, sessionID, toolUseID, command string) string {
	payload, err := json.Marshal(map[string]any{
		"session_id":      sessionID,
		"tool_use_id":     toolUseID,
		"hook_event_name": event,
		"tool_input":      map[string]string{"command": command},
	})
	if err != nil {
		panic(err)
	}
	return string(payload)
}

func runAgentHook(t *testing.T, subcommand, input, cwd string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	app := appctx.NewApp(config.Default())
	t.Cleanup(app.Close)

	cmd := NewAgentHookCmd()
	var stdout bytes.Buffer
	cmd.SetIn(strings.NewReader(input))
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{subcommand})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	if cwd != "" {
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(input), &payload))
		payload["cwd"] = cwd
		encoded, err := json.Marshal(payload)
		require.NoError(t, err)
		cmd.SetIn(bytes.NewReader(encoded))
	}
	require.NoError(t, cmd.Execute())
	return strings.TrimSpace(stdout.String())
}

func hookSpecificOutput(t *testing.T, output string) (string, string) {
	t.Helper()
	var payload struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal([]byte(output), &payload))
	return payload.HookSpecificOutput.HookEventName, payload.HookSpecificOutput.AdditionalContext
}

func commitFile(t *testing.T, repo, name, subject string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte("fixture\n"), 0o644))
	runGit(t, repo, "add", name)
	runGit(t, repo, "commit", "-m", subject)
}

func newGitRepo(t *testing.T, branch, subject string) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", branch)
	runGit(t, repo, "config", "user.email", "agent-hook@example.com")
	runGit(t, repo, "config", "user.name", "Agent Hook Test")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644))
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", subject)
	return repo
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	// Git wrappers (e.g. git-ai) spawn background work that writes into .git
	// after a commit returns, racing t.TempDir cleanup.
	cmd.Env = append(os.Environ(), "GIT_AI_SKIP_ALL_HOOKS=1")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}
