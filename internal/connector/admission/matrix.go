package admission

// Trigger names why an admitted event is the agent's business.
type Trigger string

// The v1 triggers. boosted and queued are step 25; edited is step 28.
const (
	TriggerMentioned  Trigger = "mentioned"
	TriggerSubscribed Trigger = "subscribed"
	TriggerAssigned   Trigger = "assigned"
	TriggerCompleted  Trigger = "completed"
)

// Rule is one row of the trigger matrix: a trigger an event type can admit
// under, and the metadata the gate needs to judge it without a read.
type Rule struct {
	Trigger Trigger
	// OperatorOnly restricts the rule to events the operator performed,
	// whatever the trust mode. Assignments run the agent against a recording
	// on the assigner's say-so, so no broadened mode extends to them.
	OperatorOnly bool
	// RequiresRoute discards the rule at the gate when the project has no
	// route. Only mentioned and assigned are answered in an unmapped project
	// (blocked(no_route) and a holding reply); every other trigger is
	// discarded there, so it is cheaper to discard it before any read.
	RequiresRoute bool
	// Acknowledge says a person asked for something, so the worker's
	// acknowledgement and the thirty-second guard apply. A completion or a
	// subscription comment is not a request.
	Acknowledge bool
}

// Matrix maps an event type to the rules it can admit under, in the order
// they are tried. It is data: a later step adds rows without touching the
// gate.
type Matrix map[string][]Rule

var (
	ruleMentioned  = Rule{Trigger: TriggerMentioned, Acknowledge: true}
	ruleSubscribed = Rule{Trigger: TriggerSubscribed, RequiresRoute: true}
	ruleAssigned   = Rule{Trigger: TriggerAssigned, OperatorOnly: true, Acknowledge: true}
	ruleCompleted  = Rule{Trigger: TriggerCompleted, RequiresRoute: true}
)

// V1Matrix returns the version-1 trigger matrix. Every other cataloged type
// is discarded as not_in_matrix. A fresh copy each call, so no caller can
// widen another's.
func V1Matrix() Matrix {
	return Matrix{
		// A comment can both mention the agent and land on a recording it is
		// subscribed to. The mention wins, and not by this order: the
		// subscribed rule never applies to a comment that mentions the agent.
		"comment.created":   {ruleMentioned, ruleSubscribed},
		"message.created":   {ruleMentioned},
		"todo.created":      {ruleMentioned},
		"card.created":      {ruleMentioned},
		"chat.line.created": {ruleMentioned},

		"todo.assignment_changed": {ruleAssigned},
		"card.assignment_changed": {ruleAssigned},

		"todo.completed": {ruleCompleted},
		"card.completed": {ruleCompleted},
	}
}

// State is a verdict's ledger state. The names match intake's RecordState.
type State string

const (
	StateAdmitted  State = "admitted"
	StateQueued    State = "queued"
	StateBlocked   State = "blocked"
	StateDiscarded State = "discarded"
)

// Reason explains a blocked or discarded verdict.
type Reason string

// Discard reasons. Each is a verified verdict — the gate's reading of the
// pointer and policy, or a read that answered with what the verdict needs —
// never a failed or incomplete read.
const (
	// ReasonInvalidPointer: an id the pointer must carry is missing.
	ReasonInvalidPointer Reason = "invalid_pointer"
	// ReasonNotInMatrix: the event type admits under no trigger.
	ReasonNotInMatrix Reason = "not_in_matrix"
	// ReasonAgentAuthored: the agent created or performed the event, or an
	// agent actor did.
	ReasonAgentAuthored Reason = "agent_authored"
	// ReasonDelegated: an agent performed the event on someone's behalf.
	ReasonDelegated Reason = "delegated"
	// ReasonOutOfScope: the bucket is outside the --project scope.
	ReasonOutOfScope Reason = "out_of_scope"
	// ReasonUntrustedPerformer: the performer is not in the trust set.
	ReasonUntrustedPerformer Reason = "untrusted_performer"
	// ReasonAssignmentNotOperator: an assignment performed by anyone but the
	// operator.
	ReasonAssignmentNotOperator Reason = "assignment_not_operator"
	// ReasonUntrustedAuthor: the recording carrying the instruction was
	// written by someone outside the trust set.
	ReasonUntrustedAuthor Reason = "untrusted_author"
	// ReasonStale: the recording is still drafted or has been trashed.
	ReasonStale Reason = "stale"
	// ReasonNotAddressed: nothing about the recording targets the agent.
	ReasonNotAddressed Reason = "not_addressed"
)

// Blocked reasons. A blocked record is retained and recovered; it is never a
// tombstone.
const (
	// ReasonReadFailed: a read failed after its retries.
	ReasonReadFailed Reason = "read_failed"
	// ReasonReadUnresolved: a chat line found under no visible Campfire.
	ReasonReadUnresolved Reason = "read_unresolved"
	// ReasonDeltaUnverified: the assignment event was not found within the
	// events read's bound, or was found without its details, so who was added
	// is unknown.
	ReasonDeltaUnverified Reason = "delta_unverified"
	// ReasonBucketMismatch: the recording is not in the bucket the pointer
	// named — moved since the event, most likely.
	ReasonBucketMismatch Reason = "bucket_mismatch"
	// ReasonUnroutable: the SDK has no typed read for the pointer's type. Not
	// retried on a timer; only a redispatch re-runs it.
	ReasonUnroutable Reason = "unroutable"
	// ReasonNoRoute: shared by both states — a mentioned or assigned record in
	// a project with no route is blocked (and answered with a holding reply);
	// any other trigger there is discarded.
	ReasonNoRoute Reason = "no_route"
)
