//go:build runner_live

package runner

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These run the real coding agents installed on this computer, signed in as
// whoever is, and spend a few model requests. They are opt-in:
//
//	go test -tags runner_live ./internal/connector/runner -run TestInstalled -v
//
// They hold each adapter to what the runner needs of it: a turn takes its
// prompt on standard input and answers, the CLI confirms a session id, and a
// second turn continues that session.
func TestInstalledAgentsAnswerAndContinueTheirSession(t *testing.T) {
	specs := map[string]Spec{
		"claude": {Adapter: "claude", Model: os.Getenv("RUNNER_LIVE_CLAUDE_MODEL"), Effort: "low"},
		"codex":  {Adapter: "codex", Effort: "low"},
		"pi":     {Adapter: "pi", Effort: "low"},
	}
	for _, adapter := range adapters {
		t.Run(adapter.Name(), func(t *testing.T) {
			if _, err := exec.LookPath(adapter.Binary()); err != nil {
				t.Skipf("%s is not installed", adapter.Binary())
			}
			t.Parallel()
			dir := t.TempDir()
			r := &Runner{opts: Options{Dir: dir, Environ: os.Environ, Now: time.Now}}
			require.NoError(t, os.MkdirAll(dir+"/logs", 0o700))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			tools := 0
			count := func(event Event) {
				if event.Kind == EventTool {
					tools++
				}
			}
			turn := Turn{Role: RoleWorker, Spec: specs[adapter.Name()], NewID: adapter.NewSessionID(), Dir: dir}
			first, err := r.turn(ctx, "probe", adapter, turn, "Run the shell command `echo runner-probe` and then reply with exactly its output and nothing else.", nil, count)
			require.NoError(t, err)
			assert.Contains(t, first.Text, "runner-probe")
			assert.Positive(t, tools, "the shell command is seen as a tool call, with permission prompts off")
			require.NotEmpty(t, first.SessionID)
			if turn.NewID != "" {
				assert.Equal(t, turn.NewID, first.SessionID)
			}

			turn.Resume, turn.NewID = first.SessionID, ""
			second, err := r.turn(ctx, "probe", adapter, turn, "What command did I ask you to run? Reply with the command alone.", nil, nil)
			require.NoError(t, err)
			assert.Contains(t, second.Text, "echo runner-probe")
			assert.Equal(t, first.SessionID, second.SessionID)
			t.Logf("%s: model %q, %d input and %d output tokens, $%.4f", adapter.Name(), cmpText(second.Model, first.Model), first.Usage.InputTokens+second.Usage.InputTokens, first.Usage.OutputTokens+second.Usage.OutputTokens, max(second.SessionCostUSD, first.Usage.CostUSD+second.Usage.CostUSD))

			// A session that does not exist fails, and says so.
			turn.Resume = "00000000-0000-4000-8000-000000000000"
			lost, err := r.turn(ctx, "probe", adapter, turn, "hello", nil, nil)
			require.ErrorIs(t, err, ErrTurnFailed)
			assert.Empty(t, lost.SessionID, "a session that could not be opened is not confirmed, which is what starts the request over in a new one")
		})
	}
}

// Under guarded permissions, the coding agents that have guardrails still do
// what a request needs with no one asked: run the basecamp command, write in
// the directory they work in and commit in its repo, on a new session and a
// continued one. The commit is the telling one for Codex: its sandbox keeps a
// repo's git data read-only, so it lands only if Codex's own reviewer let the
// command run outside.
func TestInstalledAgentsWorkUnattendedUnderTheirOwnGuardrails(t *testing.T) {
	for _, adapter := range adapters {
		if !adapter.Guarded() {
			continue
		}
		t.Run(adapter.Name(), func(t *testing.T) {
			if _, err := exec.LookPath(adapter.Binary()); err != nil {
				t.Skipf("%s is not installed", adapter.Binary())
			}
			t.Parallel()
			dir := t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, string(out))
				return string(out)
			}
			git("init", "-q", "-b", "main")
			require.NoError(t, os.WriteFile(dir+"/README.md", []byte("probe\n"), 0o600))
			git("add", "-A")
			git("-c", "user.name=Probe", "-c", "user.email=probe@example.com", "commit", "-q", "-m", "start")
			git("config", "user.name", "Probe")
			git("config", "user.email", "probe@example.com")

			r := &Runner{opts: Options{Dir: dir, Environ: os.Environ, Now: time.Now}}
			require.NoError(t, os.MkdirAll(dir+"/logs", 0o700))
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
			defer cancel()

			spec := Spec{Adapter: adapter.Name(), Effort: "low"}
			if adapter.Name() == "claude" {
				spec.Model = os.Getenv("RUNNER_LIVE_CLAUDE_MODEL")
			}
			note := guardedNote(PermissionsGuarded, adapter.Name())
			turn := Turn{Role: RoleWorker, Spec: spec, NewID: adapter.NewSessionID(), Dir: dir, Permissions: PermissionsGuarded}
			first, err := r.turn(ctx, "guarded", adapter, turn, note+"\n\nRun `basecamp --version`. Then create first.txt containing one line, and commit it with git (message: first). Reply with the version number the first command printed, and nothing else.", nil, nil)
			require.NoError(t, err)
			assert.Regexp(t, `\d+\.\d+\.\d+`, first.Text)
			assert.Contains(t, git("log", "--format=%s"), "first")

			turn.Resume, turn.NewID = first.SessionID, ""
			_, err = r.turn(ctx, "guarded", adapter, turn, "Now create second.txt containing one line and commit it too (message: second). Reply DONE.", nil, nil)
			require.NoError(t, err)
			assert.Contains(t, git("log", "--format=%s"), "second", "a continued session is under the same guardrails")
		})
	}
}
