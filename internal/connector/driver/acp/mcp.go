package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// The MCP isolation boundary
//
// A session runs on the MCP servers it was given and on no others, and each
// of those runs on the environment it was given. Three places hold that
// line:
//
//  1. What is declared. wireServers turns SessionConfig.MCPServers into the
//     session/new mcpServers[], each with its whole environment written out:
//     some adapters pass their own environment down to a server and some
//     pass almost nothing, so nothing a server needs is left to inheritance.
//     What a server may inherit is bounded by what the adapter itself was
//     given, which is an allowlist (invariant 1, held in Driver.open). A
//     server without a name or an absolute command is ErrUnusable, and so is
//     an environment name that is not one.
//
//  2. What the adapter must not add. The adapter is configured so it can
//     load no MCP server of the host's: claude-agent-acp is given
//     settingSources: [] and strictMcpConfig, and codex-acp is refused
//     before it starts when its config declares mcp_servers
//     (ErrForeignMCPConfig, from codexPreflight) and is run with
//     DISABLE_MCP_CONFIG_FILTERING so the servers it was given reach the
//     session whole. Both live with the adapters, in adapters.go.
//
//  3. What actually connected. reportMCPServers is the one place that judges
//     the adapter's own account of its servers, however that account
//     arrives: Claude Code's init, forwarded as an SDK message
//     (onSDKMessage), or codex-acp's mcp_startup.<server> failures
//     (MCPStatus, in adapters.go). A server the session was given that did
//     not connect, a server it was never given that is there anyway, or — for
//     Claude — a first turn that ends with no init at all fails the turn with
//     ErrMCPServerNotConnected and ends the worker (invariant 9). An account
//     that names another session is not this session's account and is
//     dropped.
//
// Ending the session ends the servers: the adapter starts them, the worker's
// process group is ended as a group, and a server the adapter keeps outside
// that group loses the stdio it was started with.
// wireServer is ACP's stdio McpServer.
type wireServer struct {
	Name    string    `json:"name"`
	Command string    `json:"command"`
	Args    []string  `json:"args"`
	Env     []wireEnv `json:"env"`
}

type wireEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// wireServers declares every server's whole environment (invariant 1): some
// adapters pass their own environment down to MCP servers and some pass
// almost nothing, so nothing a server needs is left to inheritance.
func wireServers(servers []driver.MCPServer) ([]wireServer, error) {
	out := make([]wireServer, 0, len(servers))
	for _, srv := range servers {
		if srv.Name == "" || !filepath.IsAbs(srv.Command) {
			return nil, errors.New("acp: an MCP server needs a name and an absolute command")
		}
		env := make([]wireEnv, 0, len(srv.Env))
		for k, v := range srv.Env {
			if k == "" || strings.ContainsAny(k, "=\x00") {
				return nil, fmt.Errorf("acp: MCP server %q has an invalid environment name", srv.Name)
			}
			env = append(env, wireEnv{Name: k, Value: v})
		}
		slices.SortFunc(env, func(a, b wireEnv) int { return strings.Compare(a.Name, b.Name) })
		args := srv.Args
		if args == nil {
			args = []string{}
		}
		out = append(out, wireServer{Name: srv.Name, Command: srv.Command, Args: args, Env: env})
	}
	return out, nil
}

// reportMCPServers takes the agent's own account of its MCP servers
// (invariant 9): every server the session was given must be connected, and a
// server it was never given must not be there at all.
//
// complete says whether statuses is the agent's whole account of them (an
// init) or only what it said about one server (a startup failure).
func (s *session) reportMCPServers(statuses map[string]string, complete bool) {
	s.mu.Lock()
	names := slices.Clone(s.mcpNames)
	s.mu.Unlock()
	for name, status := range statuses {
		switch {
		case !slices.Contains(names, name):
			// strictMcpConfig and the Codex preflight are meant to leave the
			// agent nothing else; a server it names is evidence they did not,
			// whether this is its whole list or one startup report.
			s.fail(fmt.Errorf("%w: the agent has a server the session never gave it, %q", ErrMCPServerNotConnected, s.conn.agentText(name)))
			return
		case status != "connected":
			s.fail(fmt.Errorf("%w: %q is %q", ErrMCPServerNotConnected, name, s.conn.agentText(status)))
			return
		}
	}
	if !complete {
		return
	}
	for _, name := range names {
		if statuses[name] != "connected" {
			s.fail(fmt.Errorf("%w: the agent did not report %q at all", ErrMCPServerNotConnected, name))
			return
		}
	}
	s.mu.Lock()
	s.mcpConfirmed = true
	s.mu.Unlock()
}

// onSDKMessage reads the one Claude Code message the session asks
// claude-agent-acp to forward, its init, for each MCP server's name and
// status. Everything else in it, and every other message, is dropped unread.
func (s *session) onSDKMessage(params json.RawMessage) {
	if s.mcpStatus != MCPStatusInit {
		return
	}
	var n struct {
		SessionID string `json:"sessionId"`
		Message   struct {
			Type       string `json:"type"`
			Subtype    string `json:"subtype"`
			MCPServers []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"mcp_servers"`
		} `json:"message"`
	}
	if json.Unmarshal(params, &n) != nil || n.SessionID == "" || !s.ours(n.SessionID) ||
		n.Message.Type != "system" || n.Message.Subtype != "init" {
		return
	}
	statuses := map[string]string{}
	for _, srv := range n.Message.MCPServers {
		statuses[srv.Name] = srv.Status
	}
	s.mu.Lock()
	known := s.id != ""
	if !known {
		// The session's id is not known yet: this account of the servers is
		// held until it is, so an init naming another session cannot vouch
		// for this one.
		if s.earlyInit == nil {
			s.earlyInit = map[string]map[string]string{}
		}
		s.earlyInit[n.SessionID] = statuses
	}
	s.mu.Unlock()
	if known {
		s.reportMCPServers(statuses, true)
	}
}
