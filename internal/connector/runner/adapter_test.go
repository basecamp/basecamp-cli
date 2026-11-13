package runner

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The lines below are each CLI's own, as it wrote them for a small turn
// (Claude Code 2.1.322, Codex 0.167.0, Pi 1.5.0), cut down to the fields the
// adapters read and a few they must ignore.

func parseAll(a Adapter, stream string) []Event {
	var events []Event
	for line := range strings.SplitSeq(strings.TrimSpace(stream), "\n") {
		events = append(events, a.Parse([]byte(line))...)
	}
	return events
}

func TestClaudeCodeStream(t *testing.T) {
	events := parseAll(claudeAdapter{}, `
{"type":"system","subtype":"hook_started","hook_name":"SessionStart:startup","session_id":"04ffacba"}
{"type":"system","subtype":"init","cwd":"/work","session_id":"04ffacba","tools":["Task","Bash"],"model":"claude-haiku-5-5","permissionMode":"bypassPermissions"}
{"type":"system","subtype":"thinking_tokens","estimated_tokens":50,"session_id":"04ffacba"}
{"type":"assistant","message":{"model":"claude-haiku-5-5","role":"assistant","content":[{"type":"thinking","thinking":"","signature":"x"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"On it."}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"echo hi"}}]}}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":"hi","is_error":false}]},"session_id":"04ffacba"}
{"type":"user","message":{"role":"user","content":"a plain string"},"session_id":"04ffacba"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"The output is hi."}]}}
{"type":"result","subtype":"success","is_error":false,"result":"The output is hi.","session_id":"04ffacba","total_cost_usd":0.0056,"usage":{"input_tokens":4,"cache_creation_input_tokens":25285,"cache_read_input_tokens":48817,"output_tokens":200}}
not json at all
`)
	require.Len(t, events, 5)
	assert.Equal(t, Event{Kind: EventSession, SessionID: "04ffacba", Model: "claude-haiku-5-5"}, events[0])
	assert.Equal(t, Event{Kind: EventText, Text: "On it."}, events[1])
	assert.Equal(t, Event{Kind: EventTool, Tool: "Bash"}, events[2])
	assert.Equal(t, Event{Kind: EventText, Text: "The output is hi."}, events[3])
	assert.Equal(t, Event{Kind: EventResult, Text: "The output is hi.", SessionCost: true, Usage: Usage{InputTokens: 74106, OutputTokens: 200, CostUSD: 0.0056}}, events[4])

	// What a rule of its own settings refused is on the result, a call at a time.
	refused := parseAll(claudeAdapter{}, `{"type":"result","subtype":"success","is_error":false,"result":"Could not.","permission_denials":[{"tool_name":"Bash","tool_use_id":"t1","tool_input":{"command":"git push"}},{"tool_name":"Edit"}]}`)
	assert.Equal(t, []string{"Bash", "Edit"}, refused[0].Refused)

	// A session it was asked to continue and does not have: the id on the
	// line is the one asked for, not a session, so it is not passed on.
	failed := parseAll(claudeAdapter{}, `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"1111","errors":["No conversation found with session ID: 1111"]}`)
	assert.Equal(t, []Event{{Kind: EventResult, Failed: true, SessionCost: true, Text: "No conversation found with session ID: 1111"}}, failed)
}

func TestCodexStream(t *testing.T) {
	events := parseAll(codexAdapter{}, `
{"type":"thread.started","thread_id":"01a1cb89"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"On it."}}
{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"/usr/bin/bash -lc 'echo hi'","aggregated_output":"","exit_code":null,"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"/usr/bin/bash -lc 'echo hi'","aggregated_output":"hi\n","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"hi"}}
{"type":"turn.completed","usage":{"input_tokens":45605,"cached_input_tokens":24576,"output_tokens":49,"reasoning_output_tokens":0}}
`)
	assert.Equal(t, []Event{
		{Kind: EventSession, SessionID: "01a1cb89"},
		{Kind: EventText, Text: "On it."},
		{Kind: EventTool, Tool: "shell"},
		{Kind: EventText, Text: "hi"},
		{Kind: EventResult, Usage: Usage{InputTokens: 45605, OutputTokens: 49}},
	}, events)

	// A warning is an item and the turn goes on; a failure is a line.
	failed := parseAll(codexAdapter{}, `
{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Model metadata for x not found."}}
{"type":"error","message":"The 'x' model is not supported."}
{"type":"turn.failed","error":{"message":"The 'x' model is not supported."}}
`)
	assert.Equal(t, []Event{{Kind: EventError, Text: "The 'x' model is not supported."}, {Kind: EventError, Text: "The 'x' model is not supported."}}, failed)
}

func TestPiStream(t *testing.T) {
	events := parseAll(piAdapter{}, `
{"type":"session","version":3,"id":"76de5806","cwd":"/work"}
{"type":"agent_start"}
{"type":"message_end","message":{"role":"system","content":""}}
{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"run echo hi"}]}}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"On"}}
{"type":"message_end","message":{"role":"assistant","provider":"openai-codex","model":"gpt-6.1-sol","usage":{"input":1997,"output":61,"cacheRead":3,"cacheWrite":0,"cost":{"total":0.0046}},"stopReason":"toolUse","content":[{"type":"thinking","thinking":""},{"type":"text","text":"On it.\n"},{"type":"toolCall","id":"call_1","name":"bash","arguments":{"command":"echo hi"}}]}}
{"type":"message_end","message":{"role":"toolResult","toolName":"bash","isError":false,"content":[{"type":"text","text":"hi\n"}]}}
{"type":"turn_end","message":{"role":"assistant","stopReason":"toolUse","content":[{"type":"text","text":"On it.\n"}]}}
{"type":"message_end","message":{"role":"assistant","provider":"openai-codex","model":"gpt-6.1-sol","usage":{"input":2071,"output":5,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.0042}},"stopReason":"stop","content":[{"type":"text","text":"hi"}]}}
{"type":"agent_end","messages":[]}
`)
	assert.Equal(t, []Event{
		{Kind: EventSession, SessionID: "76de5806"},
		{Kind: EventText, Text: "On it.\n"},
		{Kind: EventTool, Tool: "bash"},
		{Kind: EventUsage, Model: "openai-codex/gpt-6.1-sol", Usage: Usage{InputTokens: 2000, OutputTokens: 61, CostUSD: 0.0046}},
		{Kind: EventText, Text: "hi"},
		{Kind: EventResult, Model: "openai-codex/gpt-6.1-sol", Usage: Usage{InputTokens: 2071, OutputTokens: 5, CostUSD: 0.0042}},
	}, events)

	failed := parseAll(piAdapter{}, `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"429 rate limited","content":[]}}`)
	assert.Equal(t, []Event{{Kind: EventError, Text: "429 rate limited"}}, failed)
}

func TestATurnsArgumentsNeverCarryThePromptAndAlwaysTurnPromptsOff(t *testing.T) {
	spec := Spec{Model: "m", Effort: "high"}
	assert.Equal(t, []string{"-p", "--output-format", "stream-json", "--verbose", "--permission-mode", "bypassPermissions", "--session-id", "new", "--model", "m", "--effort", "high"},
		claudeAdapter{}.Args(Turn{Role: RoleWorker, Spec: spec, NewID: "new"}))
	// The orchestrator's own monitor and subagents are the connector's here.
	assert.Equal(t, []string{"-p", "--output-format", "stream-json", "--verbose", "--permission-mode", "bypassPermissions", "--resume", "old", "--disallowedTools", "Task,Agent,Monitor"},
		claudeAdapter{}.Args(Turn{Role: RoleOrchestrator, Resume: "old", NewID: "ignored"}))
	assert.Equal(t, []string{"exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-m", "m", "-c", `model_reasoning_effort="high"`, "-"},
		codexAdapter{}.Args(Turn{Spec: spec}))
	assert.Equal(t, []string{"exec", "resume", "old", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-"},
		codexAdapter{}.Args(Turn{Resume: "old"}))
	assert.Equal(t, []string{"-p", "--mode", "json", "--session-id", "new", "--model", "m", "--thinking", "high"}, piAdapter{}.Args(Turn{Spec: spec, NewID: "new"}))
	// Pi continues with the flag that fails for a session it does not have.
	assert.Equal(t, []string{"-p", "--mode", "json", "--session", "old"}, piAdapter{}.Args(Turn{Resume: "old", NewID: "ignored"}))

	// Guarded starts each CLI under its own guardrails, not with prompts off:
	// Claude Code in its auto mode, Codex in its sandbox with the network on
	// and the directories it is given. Pi has none to start under.
	guarded := Turn{Role: RoleWorker, NewID: "new", Permissions: PermissionsGuarded}
	assert.Equal(t, []string{"-p", "--output-format", "stream-json", "--verbose", "--permission-mode", "auto", "--session-id", "new"}, claudeAdapter{}.Args(guarded))
	sandboxed := []string{"-c", `sandbox_mode="workspace-write"`, "-c", "sandbox_workspace_write.network_access=true", "-c", `approval_policy="on-request"`, "-c", `approvals_reviewer="auto_review"`}
	assert.Equal(t, slices.Concat([]string{"exec", "--json", "--skip-git-repo-check"}, sandboxed, []string{"-"}), codexAdapter{}.Args(guarded))
	guarded.Resume = "old"
	assert.Equal(t, slices.Concat([]string{"exec", "resume", "old", "--json", "--skip-git-repo-check"}, sandboxed, []string{"-"}), codexAdapter{}.Args(guarded),
		"a continued session is started under the same guardrails")

	// Claude Code and Codex have guardrails to run under. Pi has none, and
	// is refused under guarded rather than run wide open under that name.
	require.NoError(t, CheckPermissions(PermissionsFull, Spec{Adapter: "pi"}))
	require.NoError(t, CheckPermissions(PermissionsGuarded, Spec{Adapter: "claude"}, Spec{Adapter: "codex"}))
	require.ErrorContains(t, CheckPermissions(PermissionsGuarded, Spec{Adapter: "claude"}, Spec{Adapter: "pi"}), "pi has no guardrails of its own")

	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, claudeAdapter{}.NewSessionID())
	assert.Empty(t, codexAdapter{}.NewSessionID(), "Codex names its own sessions")
	assert.NotEqual(t, piAdapter{}.NewSessionID(), piAdapter{}.NewSessionID())
}

func TestALineLongerThanTheBoundIsSkippedAndTheNextIsRead(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 300)+"\nshort\nlast"), 16)
	line, tooLong, err := readLine(r, 100)
	require.NoError(t, err)
	assert.True(t, tooLong)
	assert.Empty(t, line)
	line, tooLong, err = readLine(r, 100)
	require.NoError(t, err)
	assert.False(t, tooLong)
	assert.Equal(t, "short\n", string(line))
	line, _, err = readLine(r, 100)
	require.Error(t, err)
	assert.Equal(t, "last", string(line))
}

func TestCheckInstalledNamesTheCommandThatIsMissing(t *testing.T) {
	look := func(name string) (string, error) {
		if name == "codex" {
			return "", assert.AnError
		}
		return "/bin/" + name, nil
	}
	require.NoError(t, CheckInstalled(look, Spec{Adapter: "claude"}, Spec{Adapter: "pi"}))
	require.ErrorContains(t, CheckInstalled(look, Spec{Adapter: "claude"}, Spec{Adapter: "codex"}), "the codex adapter needs the `codex` command")
	require.ErrorContains(t, CheckInstalled(look, Spec{Adapter: "aider"}), "unknown adapter")
}

// The console asks each coding agent's CLI its version: the first line it
// answers --version with, and for one that is missing or does not answer,
// that it is so.
// What a CLI prints past the start of its answer is taken and not kept, so
// one that prints without end fills no memory.
func TestOnlyTheStartOfAVersionAnswerIsKept(t *testing.T) {
	out := head{limit: 8}
	for range 1000 {
		n, err := out.Write([]byte("0123456789"))
		require.NoError(t, err)
		require.Equal(t, 10, n, "the rest is taken, so the command is not stopped mid-write")
	}
	assert.Equal(t, "01234567", string(out.kept))
}

func TestInstalledAgentsAreAskedTheirVersions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in CLIs are shell scripts")
	}
	dir := t.TempDir()
	script := func(name, body string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700)) //nolint:gosec // G306: a stand-in CLI has to be executable
		return path
	}
	found := map[string]string{
		"claude": script("claude", `[ "$1" = "--version" ] && printf '2.1.0 (Claude Code)\nmore\n'`),
		"codex":  script("codex", `exit 3`),
	}
	look := func(name string) (string, error) {
		if path, ok := found[name]; ok {
			return path, nil
		}
		return "", exec.ErrNotFound
	}

	agents := InstalledAgents(t.Context(), look)
	require.Len(t, agents, 3)
	assert.Equal(t, Installed{Name: "claude", Path: found["claude"], Version: "2.1.0 (Claude Code)", Guarded: true}, agents[0])
	assert.Equal(t, "codex", agents[1].Name)
	assert.Empty(t, agents[1].Version)
	assert.Contains(t, agents[1].Error, "could not be asked its version")
	assert.Equal(t, Installed{Name: "pi"}, agents[2])
}
