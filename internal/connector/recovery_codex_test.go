//go:build unix

package connector

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/codex"
)

// The Codex spawn driver's row: `codex exec --json`, one process per turn.
//
// Codex takes no follow-up prompt (driver.Capabilities), so the harness's
// follow-up cases run against this row in the shape the dispatcher gives them
// here: an event that arrives mid-turn is admitted and waits for a task of
// its own, rather than being exposed to the worker in hand. forEachDriver
// hands each case the row, and harnessDriver.FollowUps says which shape to
// expect.
//
// CODEX_HOME is the harness's own: the driver reads the policy Codex applied
// out of the rollout Codex writes under it, so a row that left it unset would
// have the fake writing into the operator's ~/.codex.
func init() {
	registerHarnessDriver(harnessDriver{
		Name:      codex.Name,
		FollowUps: false,
		New: func(agent string) driver.Driver {
			home := filepath.Join(filepath.Dir(agent), "codex")
			return codex.New(codex.Options{
				Binary:     agent,
				CloseGrace: 5 * time.Second,
				Lookup: func(name string) (string, bool) {
					if name == "CODEX_HOME" {
						return home, true
					}
					return "", false
				},
			})
		},
		Agent: fakeCodex,
	})
}

// fakeCodex is `codex exec --json … -`: one process per turn, the prompt read
// from stdin to the end, the thread announced on stdout, and the policy Codex
// applied written to the thread's rollout — which is where the driver reads it
// back (codex's invariant 3), so the fake writes it before it says anything.
func fakeCodex(w *fakeWorker) int {
	args := os.Args[1:]
	for _, server := range codexMCPServers(args) {
		if server.Name != MCPServerName {
			continue
		}
		if err := w.Bind(context.Background(), server); err != nil {
			return 12
		}
	}

	home := os.Getenv("CODEX_HOME")
	if home == "" {
		return 10
	}
	cwd, err := os.Getwd()
	if err != nil {
		return 10
	}
	thread, err := fakeThreadID()
	if err != nil {
		return 10
	}
	// Where findRollout looks: sessions/YYYY/MM/DD/rollout-*-<thread>.jsonl.
	rollout := filepath.Join(home, "sessions", "2026", "09", "18", "rollout-2026-09-18T00-00-00-"+thread+".jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		return 10
	}
	badMode := w.BadMode()
	appendRollout(rollout, "session_meta", map[string]any{"id": thread})
	appendRollout(rollout, "turn_context", codexTurnContext(cwd, badMode))

	// The prompt is read from stdin, never argv, and the driver closes stdin
	// behind it.
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 13
	}

	out := bufio.NewWriter(os.Stdout)
	emit := func(v any) {
		data, _ := json.Marshal(v)
		_, _ = out.Write(append(data, '\n'))
		_ = out.Flush()
	}
	emit(map[string]any{"type": "thread.started", "thread_id": thread})
	if badMode {
		// The rollout says a policy other than the one the flags asked for.
		// The driver ends the session; this process waits to be ended, as the
		// Claude row's does, rather than racing that kill with work of its
		// own.
		w.log(0, 0, "bad-mode")
		time.Sleep(2 * time.Minute)
		return 9
	}
	if err := w.Turn(context.Background(), string(prompt)); err != nil {
		emit(map[string]any{"type": "turn.failed"})
		return 0
	}
	emit(map[string]any{"type": "turn.completed", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	return 0
}

// codexTurnContext is the policy record the driver checks. bad reports a
// sandbox other than the one the flags asked for, which is this driver's
// equivalent of an agent that reports the wrong permission mode.
func codexTurnContext(cwd string, bad bool) map[string]any {
	approval := "never"
	if bad {
		approval = "on-request"
	}
	return map[string]any{
		"cwd":             cwd,
		"approval_policy": approval,
		"sandbox_policy": map[string]any{
			"type": "workspace-write", "network_access": false,
			"exclude_slash_tmp": true, "exclude_tmpdir_env_var": true,
			"writable_roots": []string{},
		},
		"file_system_sandbox_policy": map[string]any{
			"kind": "restricted",
			"entries": []map[string]any{
				{"path": map[string]any{"type": "path", "path": cwd}, "access": "read-write"},
			},
		},
	}
}

func appendRollout(path, kind string, payload map[string]any) {
	line, err := json.Marshal(map[string]any{"type": kind, "payload": payload})
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

func fakeThreadID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32], nil
}

// codexTOMLPair is one "key"="value" of an inline TOML table.
var codexTOMLPair = regexp.MustCompile(`("(?:[^"\\]|\\.)*")=("(?:[^"\\]|\\.)*")`)

// codexMCPServers reads the MCP server declarations back out of argv, where
// the driver puts them as `-c mcp_servers.<name>.<field>=<TOML>`. The values
// are the JSON-compatible subset of TOML the driver writes.
func codexMCPServers(argv []string) []driver.MCPServer {
	servers := map[string]*driver.MCPServer{}
	order := []string{}
	get := func(name string) *driver.MCPServer {
		if servers[name] == nil {
			servers[name] = &driver.MCPServer{Name: name, Env: map[string]string{}}
			order = append(order, name)
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
			_ = json.Unmarshal([]byte(value), &get(name).Command)
		case "args":
			_ = json.Unmarshal([]byte(value), &get(name).Args)
		case "env":
			for _, m := range codexTOMLPair.FindAllStringSubmatch(value, -1) {
				var k, v string
				if json.Unmarshal([]byte(m[1]), &k) == nil && json.Unmarshal([]byte(m[2]), &v) == nil {
					get(name).Env[k] = v
				}
			}
		}
	}
	out := make([]driver.MCPServer, 0, len(order))
	for _, name := range order {
		out = append(out, *servers[name])
	}
	return out
}
