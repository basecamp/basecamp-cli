package connector

import (
	"context"
	"slices"
	"strings"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// Policy is the connector's v1 permission policy: reading, searching,
// planning, editing and the agent's Basecamp MCP tools are allowed, and the
// rest is refused without asking anyone.
//
// It bounds no directory, and that is a deliberate gap rather than an
// oversight. Until a project was routed to one, the policy refused any edit
// resolving outside the record's working directory; with the routes gone the
// only directory left is wherever the operator happened to start the
// connector, and a boundary that moves with that looks like a guarantee and
// behaves like an accident — worse to reason about than none. So the bound
// went with the route rather than being repointed.
//
// It was policy, not containment, even when it was there: the worker runs
// with the operator's ambient authority, so anything escaping the tool layer
// — a shell, a path the check could not see, a prompt injection that finds
// one — was already unstopped. What contains a worker is the agent's own
// sandbox (Codex's) and the sandbox launcher being built separately. Until
// that lands, a worker edits wherever the account can.
type Policy struct{}

var _ driver.PermissionPolicy = Policy{}

// DefaultPolicy is the v1 policy.
func DefaultPolicy() Policy { return Policy{} }

// policyAllowedKinds are what a worker does without asking, besides edits.
var policyAllowedKinds = []driver.ToolKind{driver.ToolRead, driver.ToolSearch, driver.ToolThink}

// Rules implements driver.PermissionPolicy.
func (p Policy) Rules() driver.PermissionRules {
	return driver.PermissionRules{
		Mode:            driver.ModeEdits,
		AllowKinds:      slices.Clone(policyAllowedKinds),
		AllowMCPServers: []string{MCPServerName},
	}
}

// Decide implements driver.PermissionPolicy.
func (p Policy) Decide(_ context.Context, req driver.PermissionRequest) driver.PermissionDecision {
	if strings.HasPrefix(req.Tool, "mcp__"+MCPServerName+"__") {
		return driver.PermissionDecision{Allow: true}
	}
	if req.Kind == driver.ToolEdit || slices.Contains(policyAllowedKinds, req.Kind) {
		return driver.PermissionDecision{Allow: true}
	}
	return driver.PermissionDecision{Allow: false}
}
