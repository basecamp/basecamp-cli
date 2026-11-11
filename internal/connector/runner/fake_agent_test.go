package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// The tests run the real adapters against a stand-in for each coding agent's
// CLI: this test binary, started under the CLI's name. It reads the prompt
// the way the CLI would, writes down what it was started with, does what the
// prompt's markers say, and answers in that CLI's own stream format. What a
// model would decide is scripted; everything around it is the runner's.

// Environment the stand-in reads.
const (
	fakeLogEnv = "RUNNER_FAKE_LOG" // where it appends what it was started with
	fakeDirEnv = "RUNNER_FAKE_DIR" // the runner's directory, whose socket it calls
)

func TestMain(m *testing.M) {
	if name := filepath.Base(os.Args[0]); slices.Contains(AdapterNames(), name) {
		os.Exit(fakeAgent(name))
	}
	os.Exit(m.Run())
}

// fakeInvocation is one start of a stand-in.
type fakeInvocation struct {
	Agent  string   `json:"agent"`
	Args   []string `json:"args"`
	Dir    string   `json:"dir"`
	Prompt string   `json:"prompt"`
	Path   string   `json:"path"`
	Marker string   `json:"marker"`
	// Runner is the runner the stand-in was told started it.
	Runner string `json:"runner"`
}

var requestEvent = regexp.MustCompile(`"event_id":(\d+)`)

// fakeAgent is the stand-in's whole behavior. Markers in the prompt:
//
//	[basecamp-request]  dispatch a worker for the event, as the skill would
//	REPO=<dir>          ... in that repo (default: no repo)
//	SLOW-WORKER         ... and have the worker never answer
//	REFUSE-ME           report two tool calls refused by the CLI's own rules
//	FAIL-ALWAYS         end the turn in an error
//	FAIL-ONCE           end the first turn that sees it in an error
//	HANG                never answer
//
// A session named in the runner directory's lost-sessions file cannot be
// continued: the stand-in fails as a CLI does that no longer has it.
func fakeAgent(agent string) int {
	prompt, _ := io.ReadAll(os.Stdin)
	text := string(prompt)
	dir, _ := os.Getwd()
	record, _ := json.Marshal(fakeInvocation{Agent: agent, Args: os.Args[1:], Dir: dir, Prompt: text, Path: os.Getenv("PATH"), Marker: os.Getenv("CLAUDECODE"),
		Runner: strings.TrimSpace(os.Getenv(SocketEnv) + " " + os.Getenv(ProfileEnv))})
	if log, err := os.OpenFile(os.Getenv(fakeLogEnv), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		_, _ = log.Write(append(record, '\n'))
		_ = log.Close()
	}

	session := argAfter("--resume", argAfter("--session", argAfter("--session-id", "")))
	if lost, _ := os.ReadFile(filepath.Join(os.Getenv(fakeDirEnv), "lost-sessions")); session != "" && strings.Contains(string(lost), session) {
		// A session the CLI no longer has: it says so on stderr and writes
		// nothing to its stream, as the real ones do.
		fmt.Fprintln(os.Stderr, "no session found for", session)
		return 1
	}
	if agent == "codex" {
		session = fmt.Sprintf("codex-thread-%d", os.Getpid())
		if i := slices.Index(os.Args, "resume"); i >= 0 && i+1 < len(os.Args) {
			session = os.Args[i+1]
		}
		if lost, _ := os.ReadFile(filepath.Join(os.Getenv(fakeDirEnv), "lost-sessions")); strings.Contains(string(lost), session) {
			fmt.Fprintln(os.Stderr, "no rollout found for thread id", session)
			return 1
		}
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	emit := func(v any) {
		line, _ := json.Marshal(v)
		_, _ = out.Write(append(line, '\n'))
		_ = out.Flush()
	}
	switch agent {
	case "claude":
		emit(map[string]any{"type": "system", "subtype": "init", "session_id": session, "model": "fake-claude"})
	case "codex":
		emit(map[string]any{"type": "thread.started", "thread_id": session})
	case "pi":
		emit(map[string]any{"type": "session", "id": session})
	}

	failed := ""
	switch {
	case strings.Contains(text, "HANG"):
		time.Sleep(time.Hour)
	case strings.Contains(text, "FAIL-ALWAYS"), strings.HasPrefix(text, "[connector] Your last turn ended in an error"):
		// The second is a worker's retry of the first, in the runner's words.
		failed = "scripted failure"
	case strings.Contains(text, "FAIL-ONCE"):
		once := filepath.Join(os.Getenv(fakeDirEnv), "failed-once")
		if _, err := os.Stat(once); err != nil {
			_ = os.WriteFile(once, nil, 0o600)
			failed = "scripted failure"
		}
	}
	answer := "done"
	if failed == "" && strings.Contains(text, "[basecamp-request]") {
		for _, match := range requestEvent.FindAllStringSubmatch(text, -1) {
			var id int64
			_, _ = fmt.Sscan(match[1], &id)
			req := DispatchRequest{EventID: id, NoRepo: true, Handoff: "handoff for event " + match[1]}
			if repo := regexp.MustCompile(`REPO=([^\s"\\]+)`).FindStringSubmatch(text); repo != nil {
				req.Repo, req.NoRepo = repo[1], false
			}
			if strings.Contains(text, "SLOW-WORKER") {
				req.Handoff += " HANG"
			}
			// By the socket the runner named for it, as a real agent's
			// commands reach it.
			resp, err := Call(context.Background(), os.Getenv(SocketEnv), Request{Op: OpDispatch, Dispatch: req}, 10*time.Second)
			if err != nil {
				answer = "dispatch failed: " + err.Error()
			} else {
				answer = "dispatched " + resp.Worker.ID
			}
		}
	}

	switch agent {
	case "claude":
		if failed != "" {
			emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "errors": []string{failed}})
			return 1
		}
		emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": "Bash"}}}})
		emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": answer}}}})
		result := map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": answer, "total_cost_usd": 0.01}
		if strings.Contains(text, "REFUSE-ME") {
			result["permission_denials"] = []any{map[string]any{"tool_name": "Bash"}, map[string]any{"tool_name": "Bash"}}
		}
		emit(result)
	case "codex":
		if failed != "" {
			emit(map[string]any{"type": "turn.failed", "error": map[string]any{"message": failed}})
			return 1
		}
		emit(map[string]any{"type": "item.started", "item": map[string]any{"type": "command_execution"}})
		emit(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": answer}})
		emit(map[string]any{"type": "turn.completed", "usage": map[string]any{"input_tokens": 10, "output_tokens": 2}})
	case "pi":
		if failed != "" {
			emit(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "error", "errorMessage": failed}})
			return 1
		}
		emit(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "provider": "fake", "model": "pi-1", "stopReason": "stop",
			"content": []any{map[string]any{"type": "text", "text": answer}}}})
	}
	return 0
}

func argAfter(flag, fallback string) string {
	if i := slices.Index(os.Args, flag); i >= 0 && i+1 < len(os.Args) {
		return os.Args[i+1]
	}
	return fallback
}

// fakeAgents makes this test binary each coding agent's CLI, and returns the
// LookPath that finds them and the log they write.
func fakeAgents(t *testing.T) (look func(string) (string, error), logPath string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	for _, name := range AdapterNames() {
		if err := os.Symlink(self, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	return func(name string) (string, error) { return filepath.Join(bin, name), nil }, filepath.Join(bin, "invocations.ndjson")
}

// invocations reads what the stand-ins were started with, oldest first.
func invocations(t *testing.T, logPath string) []fakeInvocation {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		return nil
	}
	var out []fakeInvocation
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var inv fakeInvocation
		if err := json.Unmarshal([]byte(line), &inv); err != nil {
			t.Fatalf("invocation log: %v", err)
		}
		out = append(out, inv)
	}
	return out
}
