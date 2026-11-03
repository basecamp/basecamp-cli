package admission

// Trigger names why an admitted event is the agent's business.
type Trigger string

// The triggers. queued is step 25, still to come; edited is step 28.
const (
	TriggerMentioned  Trigger = "mentioned"
	TriggerSubscribed Trigger = "subscribed"
	TriggerAssigned   Trigger = "assigned"
	TriggerCompleted  Trigger = "completed"
	// TriggerBoosted: someone boosted a recording the agent wrote. The boost
	// is the content; the boosted recording is what it answers.
	TriggerBoosted Trigger = "boosted"
	// TriggerPinged: an operator's line in a direct Ping with the agent, the
	// agent and one other person. Every line there is said to the agent, so
	// none needs a mention; in a group Ping a line is the agent's business
	// only as a mention.
	TriggerPinged Trigger = "pinged"
)

// Rule is one row of the trigger matrix: a trigger an event type can admit
// under, and the metadata the gate needs to judge it without a read.
type Rule struct {
	Trigger Trigger
	// OperatorOnly restricts the rule to events the operator performed,
	// whatever the trust mode, and to the people the allowlist names when
	// Trust.AllowAssignments opts them in. Assignments run the agent against
	// a recording on the assigner's say-so, so no broadened mode extends to
	// them by itself.
	OperatorOnly bool
	// RequiresServed discards the rule at the gate when connect.json does not
	// serve the project. Only mentioned and assigned are answered in an
	// unserved project (blocked(no_route) and a holding reply); every other
	// trigger is discarded there, so it is cheaper to discard it before any
	// read.
	RequiresServed bool
	// Acknowledge says a person asked for something, so the worker's
	// acknowledgement and the thirty-second guard apply. A completion, a
	// subscription comment or a boost is not a request.
	Acknowledge bool
}

// Matrix maps an event type to the rules it can admit under, in the order
// they are tried. It is data: a later step adds rows without touching the
// gate.
type Matrix map[string][]Rule

var (
	ruleMentioned  = Rule{Trigger: TriggerMentioned, Acknowledge: true}
	ruleSubscribed = Rule{Trigger: TriggerSubscribed, RequiresServed: true}
	ruleAssigned   = Rule{Trigger: TriggerAssigned, OperatorOnly: true, Acknowledge: true}
	ruleCompleted  = Rule{Trigger: TriggerCompleted, RequiresServed: true}
	// A boost answers something the agent wrote; it asks for nothing, so it
	// is not acknowledged. It requires a served project because nothing in an
	// unserved one waits on it: blocked(no_route) would post a holding reply
	// to a thumbs-up. Who may boost is whoever may mention the agent, in
	// every trust mode, so it is not OperatorOnly.
	ruleBoosted = Rule{Trigger: TriggerBoosted, RequiresServed: true}
	// Only a line in a Ping admits under pinged, and a Ping is never a served
	// project, so the rule is not in the matrix: matchPing returns it.
	rulePinged = Rule{Trigger: TriggerPinged, Acknowledge: true}
)

// eventChatLineCreated is the one type a Ping's lines arrive as.
const eventChatLineCreated = "chat.line.created"

// V1Matrix returns the trigger matrix: version 1, plus boosted. Every other cataloged type
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

		// Admitted only for a boost on a recording the agent wrote, never on
		// one of a recording's events (boostedRecording).
		"boost.created": {ruleBoosted},
	}
}

// State is a verdict's ledger state. The names match intake's RecordState.
type State string

const (
	StateAdmitted  State = "admitted"
	StateQueued    State = "queued"
	StateBlocked   State = "blocked"
	StateDiscarded State = "discarded"
	// StateHeld is never a verdict. It is what the ledger writes instead of
	// admitted or queued for a record a hold tagged for review, which waits
	// for a person.
	StateHeld State = "held"
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
	// ReasonPingsNotHeard: a line in a Ping, read by a connector that does
	// not hear Pings (Policy.HearsPings). The connector that hears them
	// answers it, from its own ledger.
	ReasonPingsNotHeard Reason = "pings_not_heard"
	// ReasonUntrustedPerformer: the performer is not in the trust set.
	ReasonUntrustedPerformer Reason = "untrusted_performer"
	// ReasonAssignmentNotOperator: an assignment performed by anyone other
	// than the operator or a person the allowlist names whom
	// allow_assignments opts in.
	ReasonAssignmentNotOperator Reason = "assignment_not_operator"
	// ReasonUntrustedAuthor: the recording carrying the instruction was
	// written by someone outside the trust set.
	ReasonUntrustedAuthor Reason = "untrusted_author"
	// ReasonStale: the recording is still drafted or has been trashed.
	ReasonStale Reason = "stale"
	// ReasonNotAddressed: nothing about the recording targets the agent.
	ReasonNotAddressed Reason = "not_addressed"
	// ReasonAgentNotAllowed: another agent's event, when the policy allows
	// agents to mention this one and does not name that agent.
	ReasonAgentNotAllowed Reason = "agent_not_allowed"
	// ReasonAgentThreadCap: an allowed agent's mention past the per-thread
	// cap (Trust.AgentThreadCap within AgentCapWindow).
	ReasonAgentThreadCap Reason = "agent_thread_cap"
	// ReasonAgentDailyCap: an allowed agent's mention past that agent's cap
	// across every thread (Trust.AgentDailyCap within AgentCapWindow).
	ReasonAgentDailyCap Reason = "agent_daily_cap"
)

// Blocked reasons. A blocked record is retained and recovered; it is never a
// tombstone.
const (
	// ReasonReadFailed: a read failed after its retries.
	ReasonReadFailed Reason = "read_failed"
	// ReasonReadUnresolved: a chat line found under no visible Campfire.
	ReasonReadUnresolved Reason = "read_unresolved"
	// ReasonThrottled: the server throttled a read for longer than a decision
	// waits in place. Retried at Verdict.RetryAt, never before.
	ReasonThrottled Reason = "throttled"
	// ReasonTrustUnverified: in project trust mode, the only project listing
	// at hand predates the event and a fresh one was read too recently to read
	// again; the person may have been added since.
	ReasonTrustUnverified Reason = "trust_unverified"
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
	// a project connect.json does not serve is blocked (and answered with a
	// holding reply); any other trigger there is discarded.
	//
	// The stored value stays "no_route", the word a served project was called
	// by when it also named a directory. It is not renamed with the concept:
	// it is written onto the record, and both the holding reply and the
	// retraction that answers it read the record's reason back (askStillOpen,
	// holdingReplyReason). A ledger in use carries rows and pending outbox
	// intents written with this value, and a row is read as it was written.
	ReasonNoRoute Reason = "no_route"
	// ReasonConfigUnreadable: the record's answer turns on which projects
	// connect.json serves, and connect.json could not be read. Not
	// no_route: that says the operator has not served this project, which
	// would be a false thing to say — and to post a holding reply about —
	// when the truth is that nothing could read the file.
	//
	// It is the one blocked reason with no window: the intake sweep re-offers
	// it every BlockedRetryInterval for as long as it stands (NextBlockedRetry,
	// internal/connector Intake.sweepBlockedRetries). A day is the right bound
	// for a failing server, which is not coming back on its own after one; a
	// broken connect.json is a local file, and an operator away for a week is
	// ordinary.
	ReasonConfigUnreadable Reason = "config_unreadable"
)
