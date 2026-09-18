//go:build unix

package connector

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/acp"
)

// The ACP driver's row: one adapter process per session, spoken to in
// newline-delimited JSON-RPC 2.0 on its stdio.
//
// The adapter it runs is the pinned claude-agent-acp, and the fake reports
// that adapter's own package and version (acp.ClaudeAgentACP), so a version
// bump cannot leave the row claiming to be an adapter the driver would
// refuse. The row does not locate an installed adapter: the fake agent is
// the executable, which is what Locate would otherwise decide, and that is
// the same seam the spawn rows use.
func init() {
	registerHarnessDriver(harnessDriver{
		Name:      acp.Name,
		FollowUps: true,
		New: func(agent string) driver.Driver {
			d, err := acp.New(acp.Options{
				Adapter: acp.ClaudeAgentACP,
				Binary:  agent,
				Lookup:  func(string) (string, bool) { return "", false },
			})
			if err != nil {
				panic("recovery harness: the acp row: " + err.Error())
			}
			return d
		},
		Agent: fakeACPAdapter,
	})
}

// fakeACPAdapter is claude-agent-acp's side of the wire: initialize, a
// session, its asking mode, the adapter's own answer to the MCP read-back,
// and a turn per prompt.
//
// What it answers is what the driver's own rules demand of a real adapter —
// the pinned package and version, an asking mode it offers and then confirms,
// Claude Code's init forwarded with every MCP server connected, and a /mcp
// answer that counts the servers the session declared — so a session reaching
// the fake worker has passed the same handshake a real one does.
func fakeACPAdapter(w *fakeWorker) int {
	adapter := acp.ClaudeAgentACP
	askMode := adapter.Modes[driver.ModeEditsInWorkDir]
	const sessionID = "recovery-harness"

	out := bufio.NewWriter(os.Stdout)
	var mu sync.Mutex
	send := func(v any) {
		data, err := json.Marshal(v)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		_, _ = out.Write(append(data, '\n'))
		_ = out.Flush()
	}
	reply := func(id json.RawMessage, result any) {
		send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	fail := func(id json.RawMessage, message string) {
		send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32603, "message": message}})
	}
	notify := func(method string, params any) {
		send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	}

	var (
		servers  []string
		declared *driver.MCPServer
		once     sync.Once
		bindErr  error
	)
	bound := make(chan struct{})
	badMode := w.BadMode()

	// When the session's MCP servers come up, and so when the connector's
	// bridge dials the task token's socket: as the handshake ends, and apart
	// from the wire, so the adapter goes on answering while its server
	// starts.
	//
	// It matters which side of the handshake that is: a connection that
	// arrives before the socket is armed waits in the listener's backlog,
	// which is what that backlog is for, so arriving early is fine, while
	// waiting for the token before answering deadlocks. This fake binds on
	// the right side of it. The rule, why the connector does not arm the
	// socket sooner, and what holds the pinned adapters to it are on the
	// Adapter type in driver/acp.
	starting := false
	startBind := func() {
		once.Do(func() {
			starting = true
			go func() {
				defer close(bound)
				if declared != nil {
					bindErr = w.Bind(context.Background(), *declared)
				}
			}()
		})
	}
	// A server that was starting when the connector died still says what
	// became of its token: the parent checks that every worker either took
	// one or said why it could not, and a process that exited with the bind
	// still in flight would answer neither.
	defer func() {
		if starting {
			<-bound
		}
	}()
	awaitBind := func() error {
		startBind()
		<-bound
		return bindErr
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64<<10), 16<<20)
	for in.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &m) != nil {
			continue
		}
		switch m.Method {
		case "initialize":
			reply(m.ID, map[string]any{
				"protocolVersion":   acp.ProtocolVersion,
				"agentCapabilities": map[string]any{"loadSession": adapter.LoadSession},
				"agentInfo":         map[string]any{"name": adapter.Package, "version": adapter.Version},
			})

		case "session/new":
			var p struct {
				MCPServers []struct {
					Name    string   `json:"name"`
					Command string   `json:"command"`
					Args    []string `json:"args"`
					Env     []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"env"`
				} `json:"mcpServers"`
			}
			if json.Unmarshal(m.Params, &p) != nil {
				fail(m.ID, "unreadable session/new")
				return 11
			}
			for _, s := range p.MCPServers {
				servers = append(servers, s.Name)
				if s.Name != MCPServerName {
					continue
				}
				env := make(map[string]string, len(s.Env))
				for _, e := range s.Env {
					env[e.Name] = e.Value
				}
				// Kept, not started: the declaration carries the server's
				// whole environment, and the server itself comes up on the
				// first turn the session is given. See declared, below.
				declared = &driver.MCPServer{Name: s.Name, Command: s.Command, Args: s.Args, Env: env}
			}
			reply(m.ID, map[string]any{
				"sessionId": sessionID,
				"modes": map[string]any{
					// Opened in another of the adapter's modes, so the mode
					// the driver confirms is one it set.
					"currentModeId":  "acceptEdits",
					"availableModes": []map[string]string{{"id": askMode}, {"id": "acceptEdits"}},
				},
			})

		case "session/set_mode":
			reply(m.ID, nil)
			reported := askMode
			if badMode {
				// It reports a mode other than the one asked for, and the
				// driver ends the session before it is ever prompted. The
				// handshake ends here, before the read-back and so before
				// the session's MCP servers would start: this worker never
				// asks for a task token, and says so rather than leaving the
				// parent's credential check a worker it cannot account for.
				reported = "bypassPermissions"
				w.log(0, 0, "bad-mode")
				w.log(0, 0, "bind-failed: the session ended in its handshake, before its MCP servers started")
			}
			notify("session/update", map[string]any{"sessionId": sessionID,
				"update": map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": reported}})

		case "session/prompt":
			var p struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			if json.Unmarshal(m.Params, &p) != nil {
				fail(m.ID, "unreadable session/prompt")
				continue
			}
			var text strings.Builder
			for _, block := range p.Prompt {
				text.WriteString(block.Text)
			}
			if text.String() == adapter.Readback.Command {
				// The adapter answers its own read-back, with no model: its
				// account of the session's MCP servers, and Claude Code's
				// init forwarded, which is how this adapter says they
				// connected (acp.MCPStatusInit).
				statuses := make([]map[string]string, 0, len(servers))
				for _, name := range servers {
					statuses = append(statuses, map[string]string{"name": name, "status": "connected"})
				}
				notify("_claude/sdkMessage", map[string]any{"sessionId": sessionID,
					"message": map[string]any{"type": "system", "subtype": "init", "mcp_servers": statuses}})
				notify("session/update", map[string]any{"sessionId": sessionID,
					"update": map[string]any{"sessionUpdate": "agent_message_chunk",
						"content": map[string]any{"type": "text",
							"text": fmt.Sprintf("%d MCP server(s): %d connected, 0 not connected, 0 disabled.", len(servers), len(servers))}}})
				reply(m.ID, map[string]any{"stopReason": "end_turn"})
				// The handshake is over with this answer: the session's MCP
				// servers start now, and the connector is about to arm their
				// socket.
				startBind()
				continue
			}
			if err := awaitBind(); err != nil {
				fail(m.ID, "the session's MCP server did not start")
				continue
			}
			if err := w.Turn(context.Background(), text.String()); err != nil {
				fail(m.ID, "the turn failed")
				continue
			}
			reply(m.ID, map[string]any{"stopReason": "end_turn",
				"usage": map[string]any{"inputTokens": 1, "outputTokens": 1}})

		case "session/cancel":
			// A notification: the turn in flight is the connector's to end,
			// and this process ends with the group it leads.
		}
	}
	return 0
}
