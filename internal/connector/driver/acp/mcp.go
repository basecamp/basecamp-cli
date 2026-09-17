package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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
//  3. What actually connected. Every account of the servers is read and
//     judged in this file, whichever adapter sends it and whatever shape it
//     arrives in: Claude Code's init, forwarded as an SDK message
//     (onSDKMessage), or codex-acp's failed mcp_startup.<server> tool calls
//     (noteStartupFailure). reportMCPServers judges an account —  a server
//     the session was given that did not connect, or a server it was never
//     given that is there anyway, fails the turn with
//     ErrMCPServerNotConnected and ends the worker (invariant 9) — and
//     mcpUnconfirmedLocked judges the absence of one, which only the end of a
//     turn can see. An account naming another session is not this session's
//     and is held or dropped, never applied.
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
	seen := make(map[string]bool, len(servers))
	for _, srv := range servers {
		if srv.Name == "" || !filepath.IsAbs(srv.Command) {
			return nil, errors.New("acp: an MCP server needs a name and an absolute command")
		}
		if seen[srv.Name] {
			// Two servers of one name are one name in the agent's account of
			// them, so one could stand for the other: there is no session
			// this driver can judge.
			return nil, fmt.Errorf("acp: two MCP servers are named %q", srv.Name)
		}
		seen[srv.Name] = true
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
	// Reduced before it is held: what is held is the agent's to send, and as
	// much of it as it likes, until the session's own id settles which one
	// account matters.
	held := s.reduce(statuses)
	s.mu.Lock()
	known := s.id != ""
	if !known && validSessionID(n.SessionID) {
		// The session's id is not known yet: this account of the servers is
		// held until it is, so an init naming another session cannot vouch
		// for this one. An id this session could never be given is not held
		// at all, and neither is an account past the bound.
		if s.earlyInit == nil {
			s.earlyInit = map[string]earlyAccount{}
		}
		if _, ok := s.earlyInit[n.SessionID]; ok || len(s.earlyInit) < maxEarlyInit {
			s.earlyInit[n.SessionID] = held
		}
	}
	s.mu.Unlock()
	if known {
		s.reportMCPServers(statuses, true)
	}
}

// earlyAccount is an account of the MCP servers that arrived before the
// session's id did, reduced to what judging it needs: what the agent said of
// each server this session was given, keyed by the session's own name for it,
// and whether it named a server the session did not give. Neither the agent's
// own names nor how many it sends are kept, so what is held is bounded by
// what the session gave — and a name the session never gave is never kept as
// a name at all, only as the reason it fails, because a name put through a
// sanitizer can come out as one the session did give.
type earlyAccount struct {
	statuses map[string]string
	foreign  bool
	// reason is the foreign name and status, sanitized, for the error only.
	reason string
}

// reduce is that reduction.
func (s *session) reduce(statuses map[string]string) earlyAccount {
	s.mu.Lock()
	names := slices.Clone(s.mcpNames)
	s.mu.Unlock()
	held := earlyAccount{statuses: make(map[string]string, len(names))}
	for name, status := range statuses {
		if i := slices.Index(names, name); i >= 0 {
			// Keyed by the session's own name, which is the one thing here
			// that is not the agent's text.
			held.statuses[names[i]] = s.conn.agentText(status)
			continue
		}
		if !held.foreign {
			held.foreign = true
			held.reason = fmt.Sprintf("%q is %q", s.conn.agentText(name), s.conn.agentText(status))
		}
	}
	return held
}

// reportAccount applies a held account: a server the session never gave fails
// it here, because that name was not kept, and the rest is judged by
// reportMCPServers like any other account.
func (s *session) reportAccount(a earlyAccount) {
	if a.foreign {
		s.fail(fmt.Errorf("%w: the agent has a server the session never gave it, %s", ErrMCPServerNotConnected, a.reason))
		return
	}
	s.reportMCPServers(a.statuses, true)
}

// mcpUnconfirmedLocked reports the third case of the rule: a Claude session
// whose turn ended with no account of its MCP servers at all. The other two
// (a server that did not connect, a server the session never gave) are
// reportMCPServers'; this one can only be seen when a turn ends, so
// finishTurn asks it here rather than judging for itself.
func (s *session) mcpUnconfirmedLocked() bool {
	return s.mcpStatus == MCPStatusInit && len(s.mcpNames) > 0 && !s.mcpConfirmed
}

// noteStartupFailure reads codex-acp's account, which arrives as failed tool
// calls named for the server that did not start, one at a time.
//
// A load's replayed history can carry one of these from the session's earlier
// life, and nothing on the wire tells it apart from the failure of the server
// this process has just started — both are session/update for the same
// session, both during the load. So a replayed failure fails the load, which
// is the safe way round: a session that cannot be loaded is started fresh,
// and a startup failure taken for history would be a worker running without
// the tools it was given.
func (s *session) noteStartupFailure(u sessionUpdate) {
	if s.mcpStatus != MCPStatusStartupFailures || !strings.HasPrefix(u.ToolCallID, "mcp_startup.") ||
		(u.Status != string(driver.ToolFailed) && u.Status != outcomeCanceled) {
		return
	}
	name := strings.TrimPrefix(u.ToolCallID, "mcp_startup.")
	if unescaped, err := url.PathUnescape(name); err == nil {
		name = unescaped
	}
	s.reportMCPServers(map[string]string{name: "failed"}, false)
}
