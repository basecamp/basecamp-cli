package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
)

func TestCodexHookCommandIsHidden(t *testing.T) {
	cmd := NewCodexHookCmd()

	assert.True(t, cmd.Hidden)
	assert.Equal(t, "codex-hook", cmd.Name())
	assert.NotNil(t, findSubcommand(cmd, "session-start"))
	assert.NotNil(t, findSubcommand(cmd, "post-commit-check"))
}

func TestCodexSessionStartReportsAuthenticated(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "test-token")

	output := runCodexHook(t, "session-start", "", "")

	context := hookAdditionalContext(t, output)
	assert.Contains(t, context, "Basecamp is active")
	assert.Contains(t, context, "OAuth is ready")
	assert.NotContains(t, context, "test-token")
}

func TestCodexSessionStartReportsUnauthenticated(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")

	output := runCodexHook(t, "session-start", "", "")

	context := hookAdditionalContext(t, output)
	assert.Contains(t, context, "Basecamp is active")
	assert.Contains(t, context, "basecamp auth login")
}

func TestCodexPostCommitCheckIgnoresMalformedInput(t *testing.T) {
	assert.Empty(t, runCodexHook(t, "post-commit-check", "not json", ""))
}

func TestCodexPostCommitCheckIgnoresIrrelevantTool(t *testing.T) {
	input := `{"tool_name":"Read","tool_input":{"file_path":"README.md"},"tool_output":{"exit_code":0}}`

	assert.Empty(t, runCodexHook(t, "post-commit-check", input, ""))
}

func TestCodexPostCommitCheckIgnoresFailedCommit(t *testing.T) {
	repo := newGitRepo(t, "main", "BC-123 initial")
	input := `{"tool_name":"Bash","tool_input":{"command":"git commit -m failed"},"tool_output":{"exit_code":1}}`

	assert.Empty(t, runCodexHook(t, "post-commit-check", input, repo))
}

func TestCodexPostCommitCheckIgnoresUnrecognizedSuccessPayloads(t *testing.T) {
	repo := newGitRepo(t, "main", "BC-123 initial")
	inputs := []string{
		`{"tool_name":"Bash","tool_input":{"command":"git commit -m ship"},"tool_output":"command output"}`,
		`{"tool_name":"Bash","tool_input":{"command":"git commit -m ship"},"tool_output":{}}`,
		`{"tool_name":"Bash","tool_input":{"command":"git commit -m ship"},"tool_response":{"status":"unknown"}}`,
		`{"tool_name":"Bash","tool_input":{"command":"git commit -m ship"},"tool_response":{"exit_code":0,"success":false}}`,
	}

	for _, input := range inputs {
		assert.Empty(t, runCodexHook(t, "post-commit-check", input, repo))
	}
}

func TestCodexHookCommitDetection(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{name: "direct", command: "git commit -m ship", want: true},
		{name: "chained", command: "git add . && git commit -m ship", want: true},
		{name: "git options", command: "git -C . --no-pager commit -m ship", want: true},
		{name: "quoted git option", command: `git -C "repo with spaces" commit -m ship`, want: true},
		{name: "environment assignment", command: "GIT_AUTHOR_NAME=Bot git commit -m ship", want: true},
		{name: "quoted environment assignment", command: `GIT_AUTHOR_NAME="Build Bot" git commit -m ship`, want: true},
		{name: "env wrapper", command: "env GIT_AUTHOR_NAME=Bot git commit -m ship", want: true},
		{name: "env flag", command: "env -i git commit -m ship", want: true},
		{name: "env option value", command: "env -u TOKEN git commit -m ship", want: true},
		{name: "env chdir", command: `env -C "repo with spaces" git commit -m ship`, want: true},
		{name: "Windows executable", command: `"C:\Program Files\Git\bin\git.exe" commit -m ship`, want: true},
		{name: "commit after sequential command", command: "echo ready; git commit -m ship", want: true},
		{name: "stderr redirection", command: "git commit -m ship 2>&1", want: true},
		{name: "combined redirection", command: "git commit -m ship &>/dev/null", want: true},
		{name: "comment after and", command: "git commit -m ship && # note\necho done", want: true},
		{name: "mere mention", command: "echo git commit", want: false},
		{name: "assignment before mention", command: "MODE=test echo git commit", want: false},
		{name: "quoted mention", command: `echo "git commit"`, want: false},
		{name: "different subcommand", command: "git status # git commit", want: false},
		{name: "skipped or branch", command: "git diff --quiet || git commit -m ship", want: false},
		{name: "commit before sequential command", command: "git commit -m ship; echo done", want: false},
		{name: "ambiguous pipeline", command: "git commit -m ship | cat", want: false},
		{name: "background commit", command: "git commit -m ship &", want: false},
		{name: "escaped redirection before background", command: `git commit -m ship foo\>& true`, want: false},
		{name: "unknown env option", command: "env --unknown git commit -m ship", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]string{"command": tt.command})
			require.NoError(t, err)
			assert.Equal(t, tt.want, codexHookRanCommit(raw))
		})
	}
}

func TestCodexPostCommitCheckIgnoresSuccessfulCommitWithoutReference(t *testing.T) {
	repo := newGitRepo(t, "main", "ship native plugin")
	input := `{"tool_name":"Bash","tool_input":{"command":"git commit -m ship"},"tool_output":{"exit_code":0}}`

	assert.Empty(t, runCodexHook(t, "post-commit-check", input, repo))
}

func TestCodexPostCommitCheckUsesSubjectReferenceFromToolOutput(t *testing.T) {
	repo := newGitRepo(t, "main", "BC-123 ship native plugin")
	input := `{"tool_name":"Bash","tool_input":{"command":"git commit -m ship"},"tool_output":{"exit_code":0}}`

	message := hookSystemMessage(t, runCodexHook(t, "post-commit-check", input, repo))
	assert.Contains(t, message, "BC-123")
	assert.Contains(t, message, "basecamp comments create 123")
	assert.Contains(t, message, "basecamp todos complete 123")
}

func TestCodexPostCommitCheckUsesBranchReferenceFromToolResponse(t *testing.T) {
	repo := newGitRepo(t, "todo-456-native-hook", "ship native hook")
	input := `{"tool_name":"Bash","tool_input":{"cmd":"git commit -m ship"},"tool_response":{"status":"completed","exit_code":0}}`

	message := hookSystemMessage(t, runCodexHook(t, "post-commit-check", input, repo))
	assert.Contains(t, message, "todo-456")
	assert.Contains(t, message, "basecamp comments create 456")
	assert.Contains(t, message, "basecamp todos complete 456")
}

func TestCodexPostCommitCheckIgnoresNonRepository(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"git commit -m BC-123"},"tool_response":{"status":"completed"}}`

	assert.Empty(t, runCodexHook(t, "post-commit-check", input, t.TempDir()))
}

func TestCodexPostCommitCheckDoesNotRunBasecamp(t *testing.T) {
	repo := newGitRepo(t, "basecamp-789-native-hook", "ship native hook")
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "basecamp-calls")
	fakeBasecamp := filepath.Join(binDir, "basecamp")
	require.NoError(t, os.WriteFile(fakeBasecamp, []byte("#!/bin/sh\necho called >> \""+logPath+"\"\n"), 0o755)) //nolint:gosec // test executable
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	input := `{"tool_name":"Bash","tool_input":{"command":"git commit -m ship"},"tool_response":{"status":"completed"}}`

	message := hookSystemMessage(t, runCodexHook(t, "post-commit-check", input, repo))

	assert.Contains(t, message, "basecamp-789")
	_, err := os.Stat(logPath)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func runCodexHook(t *testing.T, subcommand, input, cwd string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	app := appctx.NewApp(config.Default())
	t.Cleanup(app.Close)

	cmd := NewCodexHookCmd()
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

func hookAdditionalContext(t *testing.T, output string) string {
	t.Helper()
	var payload struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal([]byte(output), &payload))
	return payload.HookSpecificOutput.AdditionalContext
}

func hookSystemMessage(t *testing.T, output string) string {
	t.Helper()
	var payload struct {
		SystemMessage string `json:"systemMessage"`
	}
	require.NoError(t, json.Unmarshal([]byte(output), &payload))
	return payload.SystemMessage
}

func findSubcommand(cmd interface{ Commands() []*cobra.Command }, name string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}

func newGitRepo(t *testing.T, branch, subject string) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", branch)
	runGit(t, repo, "config", "user.email", "codex-hook@example.com")
	runGit(t, repo, "config", "user.name", "Codex Hook Test")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644))
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", subject)
	return repo
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}
