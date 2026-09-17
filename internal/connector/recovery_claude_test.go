//go:build unix

package connector

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"slices"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/claude"
)

// The Claude Code spawn driver's row: `claude -p` speaking stream-json.
func init() {
	registerHarnessDriver(harnessDriver{
		Name: claude.Name,
		New: func(agent string) driver.Driver {
			return claude.New(claude.Options{Binary: agent, CloseGrace: 5 * time.Second, Lookup: func(string) (string, bool) { return "", false }})
		},
		Agent: fakeClaude,
	})
	if os.Getenv(harnessRealEnv) != "" {
		// The real Claude Code on PATH, for TestRecoveryAgainstRealAgents.
		registerHarnessDriver(harnessDriver{
			Name: claude.Name + "-real",
			Real: true,
			New:  func(string) driver.Driver { return claude.New(claude.Options{}) },
		})
	}
}

// fakeClaude is `claude -p --input-format stream-json --output-format
// stream-json`: one process per session, a user message per prompt, the init
// message before the first result, and a result per turn.
func fakeClaude(w *fakeWorker) int {
	args := os.Args[1:]
	flag := func(name string) string {
		i := slices.Index(args, name)
		if i < 0 || i+1 >= len(args) {
			return ""
		}
		return args[i+1]
	}
	// The server declaration is read before the init message: the driver
	// removes the file once the agent reports its servers started.
	var config struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	data, err := os.ReadFile(flag("--mcp-config"))
	if err != nil {
		return 10
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return 11
	}
	names := make([]string, 0, len(config.MCPServers))
	for name, s := range config.MCPServers {
		names = append(names, name)
		if name == MCPServerName {
			if err := w.Bind(context.Background(), driver.MCPServer{Name: name, Command: s.Command, Args: s.Args, Env: s.Env}); err != nil {
				return 12
			}
		}
	}

	sessionID := flag("--session-id")
	if sessionID == "" {
		sessionID = flag("--resume")
	}
	mode := flag("--permission-mode")
	badMode := w.BadMode()
	if badMode {
		mode = "bypassPermissions"
	}

	out := bufio.NewWriter(os.Stdout)
	emit := func(v any) {
		data, _ := json.Marshal(v)
		_, _ = out.Write(append(data, '\n'))
		_ = out.Flush()
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64<<10), 16<<20)
	inited := false
	for in.Scan() {
		var msg struct {
			Type    string `json:"type"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(in.Bytes(), &msg) != nil {
			continue
		}
		switch msg.Type {
		case "control_request":
			emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "session_id": sessionID})
			continue
		case "user":
		default:
			continue
		}
		if !inited {
			inited = true
			servers := make([]map[string]string, 0, len(names))
			for _, name := range names {
				servers = append(servers, map[string]string{"name": name, "status": "connected"})
			}
			emit(map[string]any{"type": "system", "subtype": "init", "session_id": sessionID, "permissionMode": mode, "mcp_servers": servers})
			if badMode {
				// It reported the wrong mode and waits to be ended. Whether a
				// real agent would already have acted is exactly what the
				// connector cannot know; this one acting would only race the
				// driver's kill.
				w.log(0, 0, "bad-mode")
				time.Sleep(2 * time.Minute)
				return 9
			}
		}
		if err := w.Turn(context.Background(), msg.Message.Content); err != nil {
			emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "session_id": sessionID})
			continue
		}
		emit(map[string]any{"type": "result", "subtype": "success", "stop_reason": "end_turn", "is_error": false, "session_id": sessionID,
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	}
	return 0
}
