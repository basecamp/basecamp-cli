package admission

import "slices"

// GateResult is the gate's reading of a pointer.
type GateResult struct {
	// Rules are the matrix rules still open after the gate, in matrix order.
	// Empty means the event is discarded for Reason.
	Rules []Rule
	// Reason is why the event was discarded; empty when Rules is not.
	Reason Reason
	// ConfirmMembership says the performer is trusted only if a read confirms
	// they are a non-client member of the project (project trust mode, a
	// performer other than the operator). The gate cannot see membership.
	ConfirmMembership bool
}

// Discarded reports whether the gate ended the event.
func (g GateResult) Discarded() bool { return len(g.Rules) == 0 }

// Gate decides from the pointer and the policy alone. It makes no read and
// calls nothing, so it is safe to run on every event the feed serves.
//
// The checks run in a fixed order and the first that ends the event names the
// reason: the pointer's own ids, the matrix, the agent's own hand, scope, then
// per rule the trust set and the route.
func Gate(ev Event, p Policy, m Matrix) GateResult {
	if ev.ID <= 0 || ev.BucketID <= 0 || ev.RecordingID <= 0 || ev.CreatorID <= 0 {
		return GateResult{Reason: ReasonInvalidPointer}
	}
	rules, ok := m[ev.EventType]
	if !ok || len(rules) == 0 {
		return GateResult{Reason: ReasonNotInMatrix}
	}

	// The agent's own id never authorizes, in any role. This runs before
	// trust so that no trust mode, however broad, and no allowlist, however
	// written, can reach an event the agent had a hand in.
	switch {
	case ev.CreatorID == p.AgentID, ev.Performer() == p.AgentID, ev.ActorType == ActorTypeAgent:
		return GateResult{Reason: ReasonAgentAuthored}
	case ev.PerformedByID != nil:
		// Performed by an agent on someone's behalf. Until agent-to-agent is
		// designed, an agent's action never wakes an agent, whoever it acted
		// for.
		return GateResult{Reason: ReasonDelegated}
	}

	if !p.inScope(ev.BucketID) {
		return GateResult{Reason: ReasonOutOfScope}
	}

	performer := ev.Performer()
	isOperator := performer == p.Trust.OperatorID
	_, routed := p.route(ev.BucketID)

	var (
		open       []Rule
		reason     Reason
		membership bool
	)
	drop := func(r Reason) {
		if reason == "" {
			reason = r
		}
	}
	for _, rule := range rules {
		needsMembership := false
		switch {
		case isOperator:
		case rule.OperatorOnly:
			drop(ReasonAssignmentNotOperator)
			continue
		case p.Trust.Mode == TrustAllowlist && slices.Contains(p.Trust.AllowlistIDs, performer):
		case p.Trust.Mode == TrustProject:
			needsMembership = true
		default:
			drop(ReasonUntrustedPerformer)
			continue
		}
		if rule.RequiresRoute && !routed {
			drop(ReasonNoRoute)
			continue
		}
		membership = membership || needsMembership
		open = append(open, rule)
	}
	if len(open) == 0 {
		return GateResult{Reason: reason}
	}
	return GateResult{Rules: open, ConfirmMembership: membership}
}
