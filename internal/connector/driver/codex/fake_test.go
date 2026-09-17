//go:build unix

package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a fake `codex`: run with "exec" as its first
// argument, it plays the scenario in $CODEX_HOME/scenario.json instead of
// running tests. Everything it saw (argv, environment, prompt, the MCP
// server's environment) is written beside the scenario.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "exec" {
		os.Exit(fakeCodex())
	}
	os.Exit(m.Run())
}

type scenario struct {
	Thread string `json:"thread"`
	// TurnContext is written to the rollout as the turn_context payload;
	// nil writes none.
	TurnContext map[string]any `json:"turn_context"`
	// OldTurnContext is written before the prompt is read, as an earlier
	// turn of a resumed thread would be.
	OldTurnContext map[string]any `json:"old_turn_context"`
	// Events are written to stdout after thread.started.
	Events []string `json:"events"`
	// NoThread skips thread.started.
	NoThread bool `json:"no_thread"`
	// RunMCP starts each MCP server as Codex would and waits for it.
	RunMCP bool `json:"run_mcp"`
	// Child starts a child process in the fake's group and records its pid.
	Child bool `json:"child"`
	// Escape leaves a process of its own, outside the fake's process group,
	// holding the fake's stdout.
	Escape bool `json:"escape"`
	// Stderr is written, slowly, after the events.
	Stderr string `json:"stderr"`
	// CloseStdout closes stdout before the stderr is written: the reader is
	// done with the process well before the process is done.
	CloseStdout bool `json:"close_stdout"`
	// Deaf never reads its stdin: the prompt's write blocks once the pipe
	// fills.
	Deaf bool `json:"deaf"`
	// Hang waits to be killed after the events.
	Hang bool `json:"hang"`
	// Exit is the exit status.
	Exit int `json:"exit"`
}

type observed struct {
	Args       []string `json:"args"`
	Env        []string `json:"env"`
	Cwd        string   `json:"cwd"`
	Prompt     string   `json:"prompt"`
	MCPExit    int      `json:"mcp_exit"`
	ChildPID   int      `json:"child_pid"`
	EscapedPID int      `json:"escaped_pid"`
	Deaf       bool     `json:"deaf"`
}

func fakeCodex() int {
	home := os.Getenv("CODEX_HOME")
	data, err := os.ReadFile(filepath.Join(home, "scenario.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake codex: no scenario:", err)
		return 2
	}
	var sc scenario
	if err := json.Unmarshal(data, &sc); err != nil {
		fmt.Fprintln(os.Stderr, "fake codex: bad scenario:", err)
		return 2
	}
	obs := observed{Args: os.Args[1:], Env: os.Environ()}
	obs.Cwd, _ = os.Getwd()
	save := func() {
		out, _ := json.Marshal(obs)
		_ = os.WriteFile(filepath.Join(home, "observed.json"), out, 0o600)
	}
	defer save()

	rollout := filepath.Join(home, "sessions", "2026", "09", "17", "rollout-2026-09-17T08-00-00-"+sc.Thread+".jsonl")
	_ = os.MkdirAll(filepath.Dir(rollout), 0o700)
	if sc.OldTurnContext != nil {
		appendRecord(rollout, "turn_context", sc.OldTurnContext)
	}

	if sc.Deaf {
		obs.Deaf = true
		save()
	} else {
		prompt, _ := io.ReadAll(os.Stdin)
		obs.Prompt = string(prompt)
		save()
	}

	if sc.RunMCP {
		for _, server := range mcpServers(os.Args) {
			cmd := exec.CommandContext(context.Background(), server.command, server.args...) //nolint:gosec // the fake runs what the driver configured
			// As Codex does: a near-empty environment plus the declared env.
			cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}
			for k, v := range server.env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
			if err := cmd.Run(); err != nil {
				obs.MCPExit = 1
				fmt.Fprintln(os.Stderr, "required MCP servers failed to initialize")
				return 1
			}
		}
		save()
	}

	if sc.Escape {
		// setsid puts it in a group of its own, and it inherits stdout.
		escaped := exec.CommandContext(context.Background(), "setsid", "sleep", "120")
		escaped.Stdout = os.Stdout
		if err := escaped.Start(); err == nil {
			obs.EscapedPID = escaped.Process.Pid
			save()
		}
	}
	if sc.Child {
		child := exec.CommandContext(context.Background(), "sleep", "300")
		if err := child.Start(); err == nil {
			obs.ChildPID = child.Process.Pid
			save()
		}
	}

	appendRecord(rollout, "session_meta", map[string]any{"id": sc.Thread})
	if sc.TurnContext != nil {
		tc := map[string]any{}
		for k, v := range sc.TurnContext {
			tc[k] = v
		}
		if _, ok := tc["cwd"]; !ok {
			tc["cwd"] = obs.Cwd
		}
		raw, _ := json.Marshal(tc)
		_ = json.Unmarshal([]byte(strings.ReplaceAll(string(raw), "$CWD", obs.Cwd)), &tc)
		appendRecord(rollout, "turn_context", tc)
	}
	if !sc.NoThread {
		fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n", sc.Thread)
	}
	for _, e := range sc.Events {
		fmt.Println(e)
	}
	if sc.CloseStdout {
		_ = os.Stdout.Close()
	}
	if sc.Stderr != "" {
		// After the last stdout line, as a sandbox refusal Codex logs is.
		time.Sleep(50 * time.Millisecond)
		fmt.Fprintln(os.Stderr, sc.Stderr)
	}
	if sc.Hang {
		time.Sleep(5 * time.Minute)
	}
	return sc.Exit
}

func appendRecord(path, kind string, payload map[string]any) {
	line, _ := json.Marshal(map[string]any{"type": kind, "payload": payload})
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

type fakeServer struct {
	command string
	args    []string
	env     map[string]string
}

var tomlPair = regexp.MustCompile(`("(?:[^"\\]|\\.)*")=("(?:[^"\\]|\\.)*")`)

// mcpServers reads the mcp_servers overrides back from argv. The values are
// the JSON-compatible subset of TOML the driver writes; an env table is
// {"K"="v",...}.
func mcpServers(argv []string) []fakeServer {
	servers := map[string]*fakeServer{}
	get := func(name string) *fakeServer {
		if servers[name] == nil {
			servers[name] = &fakeServer{env: map[string]string{}}
		}
		return servers[name]
	}
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] != "-c" {
			continue
		}
		key, value, _ := strings.Cut(argv[i+1], "=")
		rest, ok := strings.CutPrefix(key, "mcp_servers.")
		if !ok {
			continue
		}
		name, field, _ := strings.Cut(rest, ".")
		switch field {
		case "command":
			_ = json.Unmarshal([]byte(value), &get(name).command)
		case "args":
			_ = json.Unmarshal([]byte(value), &get(name).args)
		case "env":
			for _, m := range tomlPair.FindAllStringSubmatch(value, -1) {
				var k, v string
				_ = json.Unmarshal([]byte(m[1]), &k)
				_ = json.Unmarshal([]byte(m[2]), &v)
				get(name).env[k] = v
			}
		}
	}
	out := make([]fakeServer, 0, len(servers))
	for _, s := range servers {
		out = append(out, *s)
	}
	return out
}
