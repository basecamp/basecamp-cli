package admission

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// personableAgent is the personable_type of an Agent person.
const personableAgent = "Agent"

// Read retry defaults. A read that fails is retried with backoff and, after
// DefaultReadAttempts failures, the record is blocked(read_failed) — retained
// and recovered on the blocked schedule, never discarded.
const (
	DefaultReadAttempts = 5
	DefaultReadBackoff  = 2 * time.Second
	maxReadBackoff      = 30 * time.Second
	// maxRetryAfter is the most one decision waits in place, in total, on the
	// server's Retry-After; beyond it the record is held until the deadline.
	maxRetryAfter = 2 * time.Minute
)

// ReplyKind names where a reply to the record goes.
type ReplyKind string

const (
	// ReplyComment is a comment on ReplyDestination.RecordingID.
	ReplyComment ReplyKind = "comment"
	// ReplyChatLine is a line in the Campfire ReplyDestination.RecordingID.
	ReplyChatLine ReplyKind = "chat_line"
)

// ReplyDestination is where the worker's acknowledgement and reply, and the
// connector's lifecycle messages, are posted.
type ReplyDestination struct {
	Kind        ReplyKind `json:"kind"`
	RecordingID int64     `json:"recording_id"`
}

// Snapshot is the recording as admission read it: the instruction the worker
// is handed, versioned by UpdatedAt. The worker re-reads the live recording
// for anything newer.
type Snapshot struct {
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	AppURL    string    `json:"app_url"`
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at"`
	// ProjectName and RequesterName are what the session reading the
	// handoff calls them, so it need not ask Basecamp as the agent.
	// RequesterName is known only when the requester wrote the recording.
	ProjectName   string `json:"project_name,omitempty"`
	RequesterName string `json:"requester_name,omitempty"`
	// Role is whose request this is: an operator's or a participant's.
	Role Role `json:"role,omitempty"`
	// Owner says the operator connect.json names asked, in words they wrote:
	// they mentioned the agent, wrote to it in their direct Ping, or assigned
	// it a recording they wrote. A
	// comment on a followed thread, a completion and a boost are never the
	// owner's word; an operator's go on a declared ask is a separate
	// approval (AskRecordingID) and does not make one. Someone named with --allow is an operator but never the
	// owner, and neither is the owner bringing in someone else's words: an
	// allowlisted person's to-do the owner moves in is that person's.
	Owner bool `json:"owner,omitempty"`
	// Ask is the boosted post as this decision read it, on an operator's
	// boost that may be a declared ask's go: the exact words the fingerprint
	// was taken over. A go approves these words, not the post as it is
	// later, so the handoff gives them to the worker to act on.
	Ask *AskCopy `json:"ask,omitempty"`
}

// AskCopy is an ask's own words, as one read returned them. Title is empty
// for a comment or a chat line, whose title is not their own.
type AskCopy struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// Verdict is admission's decision about one event.
type Verdict struct {
	EventID     int64
	EventType   string
	BucketID    int64
	RecordingID int64
	// Revision is the record revision the decision started from.
	Revision int64
	// RetryAt is set on a blocked(throttled) verdict: the server's deadline,
	// before which the record is not decided again.
	RetryAt time.Time
	// RequesterID is who asked: the performer, or, when a participant wrote
	// the words the performer brought in, that participant, the person the
	// role was settled on.
	RequesterID int64
	// Role is whose request an admitted event is, settled on everyone whose
	// trust admitted it: the performer and, for the triggers whose
	// instruction is the recording's content, its author.
	Role Role

	State  State
	Reason Reason

	// The rest is set once the recording was read; a gate discard carries
	// none of it.
	Trigger Trigger
	// Acknowledge says the worker acknowledges and the guard arms. False for
	// completed, subscribed and boosted.
	Acknowledge bool
	// ConversationKey groups events into one task: the commented recording
	// for a comment, the Campfire for a chat line, the recording otherwise.
	ConversationKey string
	Reply           *ReplyDestination
	// Served is whether connect.json serves the record's project, and Class
	// is that entry's classification. No directory comes from connect.json:
	// the connector runs where it was started.
	Served bool
	Class  string
	// RecordingURL is the recording's app URL, once read. A URL, not content.
	RecordingURL string
	// Snapshot is set on an admitted verdict only.
	Snapshot *Snapshot

	// AskRecordingID is set on an admitted boost the operator gave, on the
	// agent's own recording: the ask that boost would be the go for, if the
	// agent declared that recording an ask. Admission cannot see asks; the
	// ledger decides in the verdict's own transaction, so one ask gets one go.
	// Only the operator's boost carries it, in every trust mode: as with an
	// assignment, an allowlist or a broader trust mode never extends it.
	AskRecordingID int64
	// AskContentHash is AskFingerprint of the boosted recording's title and
	// content as this decision read it. The ledger gives the go only when it equals the
	// hash the ask was declared with: an ask edited since it was declared is
	// void, and its boost is an ordinary reply.
	AskContentHash string
	// AskBoostID is the boost's own id. Basecamp's boost ids only grow, so the
	// ledger orders it against the boosts already on the ask when it was
	// declared, on Basecamp's clock and no other.
	AskBoostID int64

	// agentCaps is set on a verdict admitting an allowed agent's mention:
	// the committer counts it against the caps before writing it.
	agentCaps *agentCaps
}

// agentCaps is what the committer needs to hold an agent's mention to the
// loop caps.
type agentCaps struct {
	agentID int64
	agents  []int64
	thread  int
	daily   int
	window  time.Duration
}

// Admitter makes verdicts. It is safe for concurrent use: the reads carry
// their own caches, and nothing else is shared.
type Admitter struct {
	policy Policy
	matrix Matrix
	reads  Reads
	// served reads the projects connect.json serves now, and says so when it
	// cannot. Nil means the policy's own map, frozen at construction;
	// WithServed makes it live.
	served func() (map[int64]Project, error)

	attempts int
	backoff  time.Duration
	sleep    func(context.Context, time.Duration) error
	now      func() time.Time
}

// policyNow is the policy this decision runs against: the one the admitter
// was built with, with the served projects as they are at this moment when a
// source for them was given. Trust and the agent's own id never move.
//
// It is read once per decision and passed down, never read again part-way
// through: a verdict assembled from two snapshots could admit an event on a
// watch_completions flag that has just been turned on and stamp it with the
// class from the entry that flag replaced (Copilot on #765).
//
// A source that cannot answer marks the policy unknown rather than returning
// an empty map, which would read as "the operator serves nothing".
//
// The map is the caller's to copy; it is read, never written to.
func (a *Admitter) policyNow() Policy {
	if a.served == nil {
		return a.policy
	}
	p := a.policy
	projects, err := a.served()
	if err != nil {
		p.Projects, p.ProjectsUnknown = nil, true
		return p
	}
	p.Projects = projects
	return p
}

// decision is one Decide call: the admitter, plus what the call has spent
// waiting on the server's throttle and the earliest time it may ask again.
type decision struct {
	*Admitter
	throttleLeft time.Duration
	retryAt      time.Time
}

// ThrottledError wraps a read the server throttled for longer than one
// decision may wait. The record is held until RetryAt, never asked sooner.
type ThrottledError struct {
	RetryAt time.Time
	Err     error
}

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("admission: throttled until %s: %v", e.RetryAt.Format(time.RFC3339), e.Err)
}

func (e *ThrottledError) Unwrap() error { return e.Err }

// WithClock replaces the admitter's clock. Tests use it.
func WithClock(now func() time.Time) Option {
	return func(a *Admitter) { a.now = now }
}

// Option adjusts an Admitter.
type Option func(*Admitter)

// WithReadRetry sets how many times a read is attempted and the first
// backoff, which doubles up to thirty seconds.
func WithReadRetry(attempts int, backoff time.Duration) Option {
	return func(a *Admitter) {
		a.attempts = attempts
		a.backoff = backoff
	}
}

// WithSleep replaces the wait between read attempts. Tests use it.
func WithSleep(sleep func(context.Context, time.Duration) error) Option {
	return func(a *Admitter) { a.sleep = sleep }
}

// WithServed makes the served projects live: admission reads them at each
// decision instead of from the policy it was built with.
//
// The dispatcher already rereads connect.json, so without this, serving a
// project while the connector runs changed only half the answer. Unserving
// one left its events admitted and then never started — no work and no
// holding reply, so the person who mentioned the agent got nothing. Serving
// one left them blocked no_route until a restart, and the holding reply's own
// remedy could not work: a redispatch re-runs admission, against the same
// stale policy (Copilot on #765).
//
// Only the projects are live. Trust is not: who may drive the agent is a
// different kind of decision, and changing it under a running connector is
// not something this option quietly does.
func WithServed(served func() (map[int64]Project, error)) Option {
	return func(a *Admitter) { a.served = served }
}

// WithMatrix replaces the trigger matrix.
func WithMatrix(m Matrix) Option {
	return func(a *Admitter) { a.matrix = m }
}

// NewAdmitter validates the policy and the reads it needs.
func NewAdmitter(policy Policy, reads Reads, opts ...Option) (*Admitter, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	switch {
	case reads.Summaries == nil || reads.Subscriptions == nil || reads.Assignments == nil || reads.Boosts == nil || reads.Pings == nil:
		return nil, errors.New("admission: the summary, subscription, assignment, boost and ping reads are required")
	case policy.Trust.Mode == TrustProject && reads.Members == nil:
		return nil, errors.New("admission: project trust mode needs a membership read")
	}
	a := &Admitter{
		policy:   policy,
		matrix:   V1Matrix(),
		reads:    reads,
		attempts: DefaultReadAttempts,
		backoff:  DefaultReadBackoff,
		sleep:    sleepContext,
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(a)
	}
	if a.attempts < 1 {
		return nil, errors.New("admission: a read needs at least one attempt")
	}
	return a, nil
}

// Decide runs the gate and, for an event that passes it, the reads and the
// trigger rules. The only error it returns is the context's: every other
// failure is a verdict (blocked or discarded), because a read that failed is a
// fact about the record, not about the caller.
func (a *Admitter) Decide(ctx context.Context, ev Event) (out Verdict, err error) {
	d := &decision{Admitter: a, throttleLeft: maxRetryAfter}
	defer func() {
		if out.State == StateBlocked && out.Reason == ReasonThrottled {
			out.RetryAt = d.retryAt
		}
	}()
	v := Verdict{
		EventID:     ev.ID,
		EventType:   ev.EventType,
		BucketID:    ev.BucketID,
		RecordingID: ev.RecordingID,
		RequesterID: ev.Performer(),
		Revision:    ev.Revision,
	}

	if ev.SeenAt.IsZero() {
		ev.SeenAt = a.now()
	}

	policy := a.policyNow()
	gate := Gate(ev, policy, a.matrix)
	if gate.Discarded() {
		if policy.ProjectsUnknown && gate.Reason == ReasonNoRoute {
			// The gate dropped it for want of a served project, and nothing
			// could read which projects are served. Held rather than
			// discarded: a discard is the one outcome repairing the file
			// cannot reverse, and a blocked record can still be run. It
			// comes round on the blocked schedule until the file is back,
			// with no window (NextBlockedRetry), and a person may run
			// `basecamp connect redispatch <id>` sooner.
			return v.end(StateBlocked, ReasonConfigUnreadable), nil
		}
		// Every other gate discard turns on the trust set, the matrix or the
		// --project scope, none of which the unreadable file touches.
		return v.end(StateDiscarded, gate.Reason), nil
	}
	if policy.ProjectsUnknown {
		// Before any read, and before anything below can end the verdict:
		// nothing here can decide an event whose answer turns on a list that
		// could not be read, and a discard is the one outcome repairing the
		// file cannot reverse.
		//
		// It has to be here rather than after the trigger rules. A
		// comment.created admits under mentioned, which needs no served
		// project, or subscribed, which does — so with the list unknown the
		// gate keeps the first and drops the second, does not discard, and
		// match then returns not_addressed for a comment the agent is
		// subscribed to. Terminally, over a broken file (Copilot on #765).
		//
		// Costing no reads is the other half: an unreadable file stops the
		// connector spending the account's API budget on events it cannot
		// decide.
		return v.end(StateBlocked, ReasonConfigUnreadable), nil
	}

	// A boost names its boost in the pointer's details. What the details
	// alone can settle is settled before any read.
	var boostID int64
	if opens(gate, TriggerBoosted) {
		var reason Reason
		if boostID, reason = boostOnRecording(ev); reason != "" {
			return v.end(StateDiscarded, reason), nil
		}
	}

	// Where the line was said decides whose word counts, so it is settled
	// before anyone's trust is read: a Ping has no members to ask about.
	var ping *basecamp.Ping
	if _, served := policy.served(ev.BucketID); !served && ev.EventType == eventChatLineCreated {
		var reason Reason
		if ping, reason, err = d.pingOf(ctx, ev.BucketID); err != nil {
			return Verdict{}, err
		}
		if reason != "" {
			return v.end(StateBlocked, reason), nil
		}
		if ping != nil && !policy.HearsPings {
			// Another connector answers the agent's Pings. Every connector
			// sees every Ping, so one that admitted the line too would
			// answer it twice.
			return v.end(StateDiscarded, ReasonPingsNotHeard), nil
		}
		if ping != nil && gate.Role != RoleOperator && !gate.InPing {
			// Project trust's participants are a project's members, and a
			// Ping is no project. An allowed agent never gets here: the gate
			// discards its event in any bucket connect.json does not serve.
			return v.end(StateDiscarded, ReasonUntrustedPerformer), nil
		}
	}
	if gate.InPing && ping == nil {
		// Trusted only in a Ping, and the bucket is none the agent is in.
		return v.end(StateDiscarded, ReasonUntrustedPerformer), nil
	}

	if gate.ConfirmMembership {
		member, reason, err := d.memberOf(ctx, ev.BucketID, ev.Performer(), ev.SeenAt)
		if err != nil {
			return Verdict{}, err
		}
		if reason != "" {
			return v.end(StateBlocked, reason), nil
		}
		if !member {
			return v.end(StateDiscarded, ReasonUntrustedPerformer), nil
		}
	}

	var (
		summary *basecamp.RecordingSummary
		boost   *basecamp.Boost
	)
	ref := basecamp.RecordingRef{BucketID: ev.BucketID, RecordingID: ev.RecordingID, EventType: ev.EventType}
	if boostID > 0 {
		var (
			state  State
			reason Reason
		)
		boost, state, reason, err = d.readBoost(ctx, ev, boostID)
		if err != nil {
			return Verdict{}, err
		}
		if state != "" {
			return v.end(state, reason), nil
		}
		// The boost names the boosted recording's type, which the pointer
		// does not: the summary read routes by it.
		ref.RecordingType = boost.Recording.Type
	}
	err = d.retry(ctx, func() error {
		var err error
		summary, err = a.reads.Summaries.Summarize(ctx, ref)
		return err
	})
	if state, reason, ctxErr := classifySummaryError(ctx, err); ctxErr != nil {
		return Verdict{}, ctxErr
	} else if state != "" {
		return v.end(state, reason), nil
	}
	if summary == nil {
		return v.end(StateBlocked, ReasonReadFailed), nil
	}

	// Stale is decided before any trigger: a drafted recording has not been
	// published to anyone, and a trashed one was withdrawn.
	if summary.Status == "drafted" || summary.Status == "trashed" {
		return v.end(StateDiscarded, ReasonStale), nil
	}

	// A read that answered without what admission decides from is a read
	// that failed: an author to trust, the recording a comment hangs off, the
	// Campfire a line was found in. None of them is evidence of anything.
	shape := shapeOf(ev.EventType, summary)
	if summary.Creator == nil || summary.Creator.ID <= 0 ||
		(shape == shapeComment && (summary.Parent == nil || summary.Parent.ID <= 0)) ||
		(shape == shapeChatLine && summary.CampfireID <= 0) {
		return v.end(StateBlocked, ReasonReadFailed), nil
	}

	v.RecordingURL = summary.AppURL
	if project, ok := policy.served(ev.BucketID); ok {
		v.Served, v.Class = true, project.Class
	}

	var (
		rule   Rule
		author Role
		state  State
		reason Reason
	)
	if ping != nil {
		rule, author, state, reason = a.matchPing(summary, ping, gate.Role)
	} else if rule, author, state, reason, err = d.match(ctx, ev, policy, gate, summary); err != nil {
		return Verdict{}, err
	}
	if state != "" {
		return v.end(state, reason), nil
	}

	v.Trigger, v.Acknowledge = rule.Trigger, rule.Acknowledge
	// The performer's role, lowered to the author's when the instruction is
	// words someone else wrote: whoever wrote them settles whose request it
	// is, and bringing them in does not lend them more.
	v.Role = gate.Role
	if author != "" {
		v.Role = least(v.Role, author)
		if author == RoleParticipant {
			// The requester is who the role was settled on: a member's
			// words stay the member's request, so a reply that names the
			// requester names them, never the operator who moved them in.
			v.RequesterID = summary.Creator.ID
		}
	}
	v.address(summary, shape, ping != nil)
	if gate.Agent {
		v.agentCaps = &agentCaps{
			agentID: ev.Performer(),
			agents:  slices.Clone(policy.Trust.AgentIDs),
			thread:  policy.Trust.ThreadCap(),
			daily:   policy.Trust.DailyCap(),
			window:  AgentCapWindow,
		}
	}

	if !v.Served && ping == nil {
		// Mentioned and assigned are answered in an unserved project rather
		// than dropped: the record keeps its trigger and reply destination
		// for the holding reply, and is read again once the operator serves
		// the project. The gate already discarded every trigger that requires
		// a served project.
		return v.end(StateBlocked, ReasonNoRoute), nil
	}
	v.State = StateAdmitted
	if rule.Trigger == TriggerBoosted && ev.PerformedByID == nil && ev.Performer() == policy.Trust.OperatorID {
		v.AskRecordingID, v.AskContentHash = ev.RecordingID, AskFingerprint(summary.Type, summary.Title, summary.Content)
		v.AskBoostID = boost.ID
	}
	v.Snapshot = &Snapshot{
		Type:      summary.Type,
		Title:     summary.Title,
		AppURL:    summary.AppURL,
		Content:   summary.Content,
		UpdatedAt: summary.UpdatedAt,
		Role:      v.Role,
		// Only a request the owner made, in words the owner wrote: a mention,
		// or an assignment of a recording they wrote. A comment on a thread
		// the agent follows asks it nothing, a completion is context, and an
		// assigned recording someone else wrote is that person's words. A
		// boost is never Owner either, even the operator's go on a declared
		// ask (AskRecordingID): Owner is the owner's word on the connector
		// itself, and a go approves only what its ask names, never that.
		Owner: v.Role == RoleOperator && ev.Performer() == policy.Trust.OperatorID &&
			(rule.Trigger == TriggerMentioned || rule.Trigger == TriggerAssigned || rule.Trigger == TriggerPinged) &&
			summary.Creator.ID == policy.Trust.OperatorID,
	}
	if summary.Bucket != nil && ping == nil {
		v.Snapshot.ProjectName = summary.Bucket.Name
	}
	switch {
	case boost != nil:
		// The boost is what was said, and its booster said it: the boosted
		// recording is the agent's own, which the worker reads by its URL.
		v.Snapshot.Content, v.Snapshot.UpdatedAt = boost.Content, boost.CreatedAt
		if boost.Booster.ID == v.RequesterID {
			v.Snapshot.RequesterName = boost.Booster.Name
		}
	case summary.Creator != nil && summary.Creator.ID == v.RequesterID:
		v.Snapshot.RequesterName = summary.Creator.Name
	}
	if v.AskRecordingID > 0 {
		// The same read the fingerprint was taken over, so the words handed
		// off are exactly the ones the ledger matches against the declaration.
		v.Snapshot.Ask = &AskCopy{Title: AskTitle(summary.Type, summary.Title), Content: summary.Content}
	}
	return v, nil
}

// match tries the gate's open rules in matrix order and returns the first
// that admits, or the state and reason that end the event. For a rule whose
// instruction is the recording's content it also returns the author's role;
// for any other it returns none.
func (a *decision) match(ctx context.Context, ev Event, policy Policy, gate GateResult, summary *basecamp.RecordingSummary) (Rule, Role, State, Reason, error) {
	rules := gate.Rules
	agent := a.policy.AgentID
	mentioned := slices.Contains(summary.MentionedPersonIDs, agent)
	endState, endReason := StateDiscarded, ReasonNotAddressed

	for _, rule := range rules {
		switch rule.Trigger {
		case TriggerMentioned:
			if !mentioned {
				continue
			}
			author, state, reason, err := a.authorTrusted(ctx, ev, summary, gate.Agent)
			if err != nil || state != "" {
				return Rule{}, "", state, reason, err
			}
			return rule, author, "", "", nil

		case TriggerSubscribed:
			// A mention is its own trigger; a comment that mentions the agent
			// never falls through to subscription, even when the mention was
			// refused.
			if mentioned {
				continue
			}
			if summary.Parent == nil || summary.Parent.ID <= 0 {
				// The subscription asked is the parent's; without one there is
				// nothing to ask, and nothing verified.
				return Rule{}, "", StateBlocked, ReasonReadFailed, nil
			}
			subscribed, reason, err := a.subscribed(ctx, summary.Parent.ID)
			if err != nil {
				return Rule{}, "", "", "", err
			}
			if reason != "" {
				return Rule{}, "", StateBlocked, reason, nil
			}
			if !subscribed {
				continue
			}
			author, state, reason, err := a.authorTrusted(ctx, ev, summary, false)
			if err != nil || state != "" {
				return Rule{}, "", state, reason, err
			}
			return rule, author, "", "", nil

		case TriggerAssigned:
			var (
				added []int64
				found bool
			)
			err := a.retry(ctx, func() error {
				var err error
				added, found, err = a.reads.Assignments.AddedPersonIDs(ctx, ev.RecordingID, ev.ID)
				return err
			})
			if err != nil {
				reason, ctxErr := failure(ctx, err)
				if ctxErr != nil {
					return Rule{}, "", "", "", ctxErr
				}
				return Rule{}, "", StateBlocked, reason, nil
			}
			if !found {
				// Current assignees are not evidence of who this event added.
				endState, endReason = StateBlocked, ReasonDeltaUnverified
				continue
			}
			// Added by this event, and still assigned now. The first is the
			// instruction; the second refuses one withdrawn before admission
			// got to it. Neither alone admits.
			if slices.Contains(added, agent) && assigned(summary, agent) {
				return rule, "", "", "", nil
			}

		case TriggerBoosted:
			// Basecamp addresses a boost on a recording to whoever published
			// it (bc3 Boostable#boostee: published_by, else edited_by), and
			// the recording's creator stands for them here: they differ only
			// for a draft someone else published. The booster was trusted at
			// the gate and wrote the boost (readBoost); the boosted words are
			// the agent's own, so there is no author to trust.
			if summary.Creator.ID == agent {
				return rule, "", "", "", nil
			}

		case TriggerCompleted:
			project, served := policy.served(ev.BucketID)
			if served && project.WatchCompletions {
				return rule, "", "", "", nil
			}
			if assigned(summary, agent) {
				return rule, "", "", "", nil
			}
			subscribed, reason, err := a.subscribed(ctx, ev.RecordingID)
			if err != nil {
				return Rule{}, "", "", "", err
			}
			if reason != "" {
				return Rule{}, "", StateBlocked, reason, nil
			}
			if subscribed {
				return rule, "", "", "", nil
			}

		case TriggerPinged:
			// Not in the matrix: only matchPing returns it.
		}
	}
	return Rule{}, "", endState, endReason, nil
}

// matchPing decides what a line in a Ping is to the agent, and returns its
// author's role. Its performer was an operator at the gate, or a participant
// connect.json lets into this Ping; its words must be one of those too, and no
// agent's. A mention is a request from either. Every line in a direct Ping is
// said to the agent, but only an operator's needs no mention.
func (a *Admitter) matchPing(summary *basecamp.RecordingSummary, ping *basecamp.Ping, performer Role) (Rule, Role, State, Reason) {
	author := summary.Creator
	role := a.policy.Trust.roleOf(author.ID)
	switch {
	case author.ID == a.policy.AgentID, author.PersonableType == personableAgent:
		return Rule{}, "", StateDiscarded, ReasonAgentAuthored
	case role != RoleOperator && !a.policy.pingParticipant(ping.ID, author.ID):
		return Rule{}, "", StateDiscarded, ReasonUntrustedAuthor
	case slices.Contains(summary.MentionedPersonIDs, a.policy.AgentID):
		return ruleMentioned, role, "", ""
	case least(role, performer) == RoleOperator && direct(ping, author.ID):
		return rulePinged, role, "", ""
	}
	return Rule{}, "", StateDiscarded, ReasonNotAddressed
}

// direct reports whether the Ping is the agent and the writer alone. People
// are the others in it as the agent sees it. Basecamp carries no group flag,
// so a group that has shrunk to the two of them reads as direct, which it now
// is: nobody else reads the line. A direct Ping never gains a third person.
func direct(ping *basecamp.Ping, writer int64) bool {
	return len(ping.People) == 1 && ping.People[0].ID == writer
}

func assigned(summary *basecamp.RecordingSummary, personID int64) bool {
	return slices.ContainsFunc(summary.Assignees, func(p basecamp.Person) bool { return p.ID == personID })
}

// authorTrusted checks the person who wrote the recording, for the triggers
// whose instruction is that recording's content, and returns their role. The
// performer was trusted at the gate; the author is usually the same person,
// but not always.
//
// agent says the gate admitted the performer as an allowed agent. Its words
// count only when that agent wrote them itself, and only as a participant's.
func (a *decision) authorTrusted(ctx context.Context, ev Event, summary *basecamp.RecordingSummary, agent bool) (Role, State, Reason, error) {
	author := summary.Creator.ID
	switch {
	case author == a.policy.AgentID:
		return "", StateDiscarded, ReasonAgentAuthored, nil
	case agent:
		if author == ev.Performer() && summary.Creator.PersonableType == personableAgent && a.policy.Trust.allowsAgent(author) {
			return RoleParticipant, "", "", nil
		}
		return "", StateDiscarded, ReasonUntrustedAuthor, nil
	case summary.Creator.PersonableType == personableAgent:
		// The agent itself, or any Agent principal. The poll lane carries no
		// actor type, so this read is where another agent's content shows.
		return "", StateDiscarded, ReasonAgentAuthored, nil
	case a.policy.Trust.roleOf(author) == RoleOperator:
		// The operator, or someone the allowlist names; Validate keeps an
		// allowlist out of operator mode.
		return RoleOperator, "", "", nil
	case author == ev.Performer():
		// Trusted at the gate, and named by nobody: a member.
		return RoleParticipant, "", "", nil
	case a.policy.Trust.Mode == TrustProject:
		member, reason, err := a.memberOf(ctx, ev.BucketID, author, ev.SeenAt)
		switch {
		case err != nil:
			return "", "", "", err
		case reason != "":
			return "", StateBlocked, reason, nil
		case member:
			return RoleParticipant, "", "", nil
		}
	}
	return "", StateDiscarded, ReasonUntrustedAuthor, nil
}

// shape is how a recording is answered: a comment on the recording it hangs
// off, a chat line in its Campfire, anything else on itself.
type shape int

const (
	shapeRecording shape = iota
	shapeComment
	shapeChatLine
)

// shapeOf reads the shape from the event type, except for a boost, which
// takes the shape of the recording it is on: a boost on the agent's comment
// is answered in that comment's thread, and joins its conversation.
func shapeOf(eventType string, summary *basecamp.RecordingSummary) shape {
	boost := eventType == eventBoostCreated
	switch {
	case eventType == "comment.created", boost && summary.Type == "Comment":
		return shapeComment
	case eventType == eventChatLineCreated, boost && strings.HasPrefix(summary.Type, "Chat::Lines::"):
		return shapeChatLine
	}
	return shapeRecording
}

// address sets the conversation key and reply destination. Decide has
// already refused a comment without its recording and a line without its
// Campfire. A line in a Ping is answered in its Campfire like any other, and
// its conversation is the Ping.
func (v *Verdict) address(summary *basecamp.RecordingSummary, shape shape, ping bool) {
	switch {
	case ping:
		v.ConversationKey = PingConversationKey(v.BucketID)
		v.Reply = &ReplyDestination{Kind: ReplyChatLine, RecordingID: summary.CampfireID}
	case shape == shapeChatLine:
		v.ConversationKey = "campfire:" + strconv.FormatInt(summary.CampfireID, 10)
		v.Reply = &ReplyDestination{Kind: ReplyChatLine, RecordingID: summary.CampfireID}
	case shape == shapeComment:
		v.ConversationKey = "recording:" + strconv.FormatInt(summary.Parent.ID, 10)
		v.Reply = &ReplyDestination{Kind: ReplyComment, RecordingID: summary.Parent.ID}
	default:
		v.ConversationKey = "recording:" + strconv.FormatInt(v.RecordingID, 10)
		v.Reply = &ReplyDestination{Kind: ReplyComment, RecordingID: v.RecordingID}
	}
}

// PingConversationKey is the conversation of a line in a Ping: the Ping
// itself, whose one Campfire holds all of it. It also marks the record as a
// Ping's for whoever reads the ledger, since a Ping is answered with no served
// project; admission writes it only after the Ping read said the bucket is one.
func PingConversationKey(pingID int64) string {
	return PingConversationPrefix + strconv.FormatInt(pingID, 10)
}

// IsPingConversation reports whether key is the conversation of the Ping whose
// bucket is bucketID.
func IsPingConversation(key string, bucketID int64) bool {
	return key == PingConversationKey(bucketID)
}

// PingConversationPrefix is PingConversationKey's prefix, for a query that
// builds the key from a bucket id.
const PingConversationPrefix = "ping:"

func (a *decision) subscribed(ctx context.Context, recordingID int64) (bool, Reason, error) {
	var subscribed bool
	err := a.retry(ctx, func() error {
		var err error
		subscribed, err = a.reads.Subscriptions.Subscribed(ctx, recordingID)
		return err
	})
	if err != nil {
		reason, ctxErr := failure(ctx, err)
		return false, reason, ctxErr
	}
	return subscribed, "", nil
}

// pingOf reads the Ping the bucket is, or nil when it is none.
func (a *decision) pingOf(ctx context.Context, bucketID int64) (*basecamp.Ping, Reason, error) {
	var ping *basecamp.Ping
	err := a.retry(ctx, func() error {
		var err error
		ping, err = a.reads.Pings.Ping(ctx, bucketID)
		return err
	})
	if err != nil {
		reason, ctxErr := failure(ctx, err)
		return nil, reason, ctxErr
	}
	return ping, "", nil
}

func (a *decision) memberOf(ctx context.Context, bucketID, personID int64, asOf time.Time) (bool, Reason, error) {
	var member bool
	err := a.retry(ctx, func() error {
		var err error
		member, err = a.reads.Members.NonClientMember(ctx, bucketID, personID, asOf)
		return err
	})
	if err != nil {
		reason, ctxErr := failure(ctx, err)
		return false, reason, ctxErr
	}
	return member, "", nil
}

// retry runs read up to the configured attempts, backing off between them.
// An answer that another attempt seconds later cannot change ends it at once:
// a chat line found under no Campfire, a pointer with no typed read, a
// recording in another bucket, and a 401, 403 or 404. The record is still
// blocked, and recovered on the blocked schedule rather than in place.
//
// A throttled read is never asked again before the server's Retry-After. The
// wait is spent in place while the decision's throttle budget (maxRetryAfter,
// across every read the decision makes) covers it; past that, or on the last
// attempt, the read returns a ThrottledError carrying the deadline, and the
// record is held until then. A fetcher is never parked on a long throttle,
// and nothing retries early.
func (a *decision) retry(ctx context.Context, read func() error) error {
	wait := a.backoff
	for attempt := 1; ; attempt++ {
		err := read()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || final(err) {
			return err
		}
		pause := wait
		if asked := retryAfter(err); asked > 0 {
			// The latest deadline any read of this decision was given.
			if until := a.now().Add(asked); until.After(a.retryAt) {
				a.retryAt = until
			}
			if attempt >= a.attempts || asked > a.throttleLeft {
				return &ThrottledError{RetryAt: a.retryAt, Err: err}
			}
			a.throttleLeft -= asked
			pause = max(pause, asked)
		} else if attempt >= a.attempts {
			return err
		}
		if sleepErr := a.sleep(ctx, pause); sleepErr != nil {
			return sleepErr
		}
		wait = min(wait*2, maxReadBackoff)
	}
}

func retryAfter(err error) time.Duration {
	if apiErr, ok := errors.AsType[*basecamp.Error](err); ok && apiErr.RetryAfter > 0 {
		return time.Duration(apiErr.RetryAfter) * time.Second
	}
	return 0
}

// failure maps a read that failed to the reason its record blocks on. A
// context error is returned as such: the caller stopped, the record did not
// change.
func failure(ctx context.Context, err error) (Reason, error) {
	switch {
	case ctx.Err() != nil:
		return "", ctx.Err()
	case errors.As(err, new(*ThrottledError)):
		return ReasonThrottled, nil
	case errors.Is(err, ErrMembershipUnverified):
		return ReasonTrustUnverified, nil
	default:
		return ReasonReadFailed, nil
	}
}

// final reports errors that are answers rather than failures.
func final(err error) bool {
	if apiErr, ok := errors.AsType[*basecamp.Error](err); ok {
		switch apiErr.Code {
		case basecamp.CodeNotFound, basecamp.CodeForbidden, basecamp.CodeAuth:
			return true
		}
	}
	return errors.Is(err, ErrMembershipUnverified) ||
		errors.Is(err, basecamp.ErrRecordingUnresolved) ||
		errors.Is(err, basecamp.ErrNoRecordingType) ||
		errors.Is(err, basecamp.ErrUnknownRecordingType) ||
		errors.Is(err, basecamp.ErrBucketMismatch)
}

// classifySummaryError maps a summary read's outcome to a verdict. A context
// error is returned as such: the caller stopped, the record did not change.
func classifySummaryError(ctx context.Context, err error) (State, Reason, error) {
	switch {
	case err == nil:
		return "", "", nil
	case ctx.Err() != nil:
		return "", "", ctx.Err()
	case errors.As(err, new(*ThrottledError)):
		return StateBlocked, ReasonThrottled, nil
	case errors.Is(err, basecamp.ErrRecordingUnresolved):
		return StateBlocked, ReasonReadUnresolved, nil
	case errors.Is(err, basecamp.ErrBucketMismatch):
		// The recording moved since the event; it is not evidence the event
		// was never the agent's business.
		return StateBlocked, ReasonBucketMismatch, nil
	case errors.Is(err, basecamp.ErrNoRecordingType), errors.Is(err, basecamp.ErrUnknownRecordingType):
		return StateBlocked, ReasonUnroutable, nil
	default:
		return StateBlocked, ReasonReadFailed, nil
	}
}

// end sets a state that is not admitted. The snapshot is only ever built on
// the admitted path, so neither state carries content: a blocked record reads
// the recording again when it is re-run, and a discarded one never needs it.
func (v Verdict) end(state State, reason Reason) Verdict {
	v.State, v.Reason = state, reason
	v.agentCaps = nil
	return v
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("admission: read backoff: %w", ctx.Err())
	}
}
