package admission

// GateResult is the gate's reading of a pointer.
type GateResult struct {
	// Rules are the matrix rules still open after the gate, in matrix order.
	// Empty means the event is discarded for Reason.
	Rules []Rule
	// Reason is why the event was discarded; empty when Rules is not.
	Reason Reason
	// ConfirmMembership says the performer is trusted only if a read confirms
	// they are a non-client member of the project (project trust mode, a
	// performer the policy does not name). The gate cannot see membership.
	ConfirmMembership bool
	// Role is the performer's: operator when the policy names them,
	// participant when only membership can trust them.
	Role Role
	// Agent says the performer is an agent the policy allows to mention this
	// one (Trust.AgentIDs). Only the mentioned rule is open to it, its role
	// is participant, and the committer holds it to the agent caps.
	Agent bool
}

// Discarded reports whether the gate ended the event.
func (g GateResult) Discarded() bool { return len(g.Rules) == 0 }

// Gate decides from the pointer and the policy alone. It makes no read and
// calls nothing, so it is safe to run on every event the feed serves.
//
// The checks run in a fixed order and the first that ends the event names the
// reason: the pointer's own ids, the matrix, the agent's own hand, scope, then
// per rule the trust set and whether the project is served.
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
	case ev.CreatorID == p.AgentID, ev.Performer() == p.AgentID:
		return GateResult{Reason: ReasonAgentAuthored}
	case ev.PerformedByID == nil && p.Trust.allowsAgent(ev.CreatorID):
		// An agent the operator named with --allow-agent, acting as itself.
		// Checked by id rather than by actor type, which only the push lane
		// carries: setup verified the id reads back as an Agent person.
		return gateAgent(ev, p, rules)
	case ev.ActorType == ActorTypeAgent:
		if len(p.Trust.AgentIDs) > 0 {
			// Agents are allowed, and this one was not named.
			return GateResult{Reason: ReasonAgentNotAllowed}
		}
		return GateResult{Reason: ReasonAgentAuthored}
	case ev.PerformedByID != nil:
		// Performed by an agent on someone's behalf. An agent's delegated
		// action never wakes an agent, whoever it acted for: only an allowed
		// agent's own mention does.
		return GateResult{Reason: ReasonDelegated}
	}

	if !p.inScope(ev.BucketID) {
		return GateResult{Reason: ReasonOutOfScope}
	}

	performer := ev.Performer()
	isOperator := performer == p.Trust.OperatorID
	role := p.Trust.roleOf(performer)
	_, served := p.served(ev.BucketID)

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
		case rule.OperatorOnly && p.Trust.AllowAssignments && role == RoleOperator:
			// A named operator the operator opted in to assigning. The
			// performer is the feed's, written by Basecamp, never a payload's.
		case rule.OperatorOnly:
			drop(ReasonAssignmentNotOperator)
			continue
		case role == RoleOperator:
			// Named in the allowlist; Validate keeps one out of operator mode.
		case p.Trust.Mode == TrustProject:
			needsMembership = true
		default:
			drop(ReasonUntrustedPerformer)
			continue
		}
		if rule.RequiresServed && !served {
			drop(ReasonNoRoute)
			continue
		}
		membership = membership || needsMembership
		open = append(open, rule)
	}
	if len(open) == 0 {
		return GateResult{Reason: reason}
	}
	return GateResult{Rules: open, ConfirmMembership: membership, Role: role}
}

// gateAgent opens an allowed agent's event to the mentioned rule alone. An
// agent reaches this one only by @mentioning it: never by assignment, by a
// comment on a thread it follows, or by a completion. Whether the content
// really mentions this agent, and that the agent wrote it, is for the read.
func gateAgent(ev Event, p Policy, rules []Rule) GateResult {
	if !p.inScope(ev.BucketID) {
		return GateResult{Reason: ReasonOutOfScope}
	}
	var open []Rule
	reason := ReasonNotAddressed
	for _, rule := range rules {
		switch {
		case rule.Trigger == TriggerMentioned:
			open = append(open, rule)
		case rule.OperatorOnly:
			reason = ReasonAssignmentNotOperator
		}
	}
	if len(open) == 0 {
		return GateResult{Reason: reason}
	}
	return GateResult{Rules: open, Role: RoleParticipant, Agent: true}
}
