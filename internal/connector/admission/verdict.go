package admission

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// Read retry defaults. A read that fails is retried with backoff and, after
// DefaultReadAttempts failures, the record is blocked(read_failed) — retained
// and recovered on the blocked schedule, never discarded.
const (
	DefaultReadAttempts = 5
	DefaultReadBackoff  = 2 * time.Second
	maxReadBackoff      = 30 * time.Second
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
}

// Verdict is admission's decision about one event.
type Verdict struct {
	EventID     int64
	EventType   string
	BucketID    int64
	RecordingID int64
	// RequesterID is the performer whose trust admitted the event.
	RequesterID int64

	State  State
	Reason Reason

	// The rest is set once the recording was read; a gate discard carries
	// none of it.
	Trigger Trigger
	// Acknowledge says the worker acknowledges and the guard arms. False for
	// completed and subscribed.
	Acknowledge bool
	// ConversationKey groups events into one task: the commented recording
	// for a comment, the Campfire for a chat line, the recording otherwise.
	ConversationKey string
	Reply           *ReplyDestination
	// Route and Class come from connect.json; Routed is false when the
	// project has none.
	Routed   bool
	Route    string
	Class    string
	Snapshot *Snapshot
}

// Admitter makes verdicts. It is safe for concurrent use: the reads carry
// their own caches, and nothing else is shared.
type Admitter struct {
	policy Policy
	matrix Matrix
	reads  Reads

	attempts int
	backoff  time.Duration
	sleep    func(context.Context, time.Duration) error
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
	case reads.Summaries == nil || reads.Subscriptions == nil || reads.Assignments == nil:
		return nil, errors.New("admission: the summary, subscription and assignment reads are required")
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
func (a *Admitter) Decide(ctx context.Context, ev Event) (Verdict, error) {
	v := Verdict{
		EventID:     ev.ID,
		EventType:   ev.EventType,
		BucketID:    ev.BucketID,
		RecordingID: ev.RecordingID,
		RequesterID: ev.Performer(),
	}

	gate := Gate(ev, a.policy, a.matrix)
	if gate.Discarded() {
		return v.end(StateDiscarded, gate.Reason), nil
	}

	if gate.ConfirmMembership {
		member, reason, err := a.memberOf(ctx, ev.BucketID, ev.Performer())
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

	var summary *basecamp.RecordingSummary
	ref := basecamp.RecordingRef{BucketID: ev.BucketID, RecordingID: ev.RecordingID, EventType: ev.EventType}
	err := a.retry(ctx, func() error {
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

	v.Snapshot = &Snapshot{
		Type:      summary.Type,
		Title:     summary.Title,
		AppURL:    summary.AppURL,
		Content:   summary.Content,
		UpdatedAt: summary.UpdatedAt,
	}
	if route, ok := a.policy.route(ev.BucketID); ok {
		v.Routed, v.Route, v.Class = true, route.Path, route.Class
	}

	rule, state, reason, err := a.match(ctx, ev, gate.Rules, summary)
	if err != nil {
		return Verdict{}, err
	}
	if state != "" {
		if state == StateBlocked {
			// A blocked record re-runs its reads; it keeps no content meanwhile.
			v.Snapshot = nil
		}
		return v.end(state, reason), nil
	}

	v.Trigger, v.Acknowledge = rule.Trigger, rule.Acknowledge
	if state, reason := v.address(summary); state != "" {
		return v.end(state, reason), nil
	}

	if !v.Routed {
		// Mentioned and assigned are answered in an unmapped project rather
		// than dropped: the record keeps its snapshot and waits for a route.
		// Any other trigger was already discarded at the gate.
		if rule.RequiresRoute {
			return v.end(StateDiscarded, ReasonNoRoute), nil
		}
		return v.end(StateBlocked, ReasonNoRoute), nil
	}
	v.State = StateAdmitted
	return v, nil
}

// match tries the gate's open rules in matrix order and returns the first
// that admits, or the state and reason that end the event.
func (a *Admitter) match(ctx context.Context, ev Event, rules []Rule, summary *basecamp.RecordingSummary) (Rule, State, Reason, error) {
	agent := a.policy.AgentID
	mentioned := slices.Contains(summary.MentionedPersonIDs, agent)
	endState, endReason := StateDiscarded, ReasonNotAddressed

	for _, rule := range rules {
		switch rule.Trigger {
		case TriggerMentioned:
			if !mentioned {
				continue
			}
			state, reason, err := a.authorTrusted(ctx, ev, summary)
			if err != nil || state != "" {
				return Rule{}, state, reason, err
			}
			return rule, "", "", nil

		case TriggerSubscribed:
			// A mention is its own trigger; a comment that mentions the agent
			// never falls through to subscription, even when the mention was
			// refused.
			if mentioned || summary.Parent == nil || summary.Parent.ID <= 0 {
				continue
			}
			subscribed, reason, err := a.subscribed(ctx, summary.Parent.ID)
			if err != nil {
				return Rule{}, "", "", err
			}
			if reason != "" {
				return Rule{}, StateBlocked, reason, nil
			}
			if !subscribed {
				continue
			}
			state, reason, err := a.authorTrusted(ctx, ev, summary)
			if err != nil || state != "" {
				return Rule{}, state, reason, err
			}
			return rule, "", "", nil

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
				if ctxErr := ctx.Err(); ctxErr != nil {
					return Rule{}, "", "", ctxErr
				}
				return Rule{}, StateBlocked, ReasonReadFailed, nil
			}
			if !found {
				// Current assignees are not evidence of who this event added.
				endState, endReason = StateBlocked, ReasonDeltaUnverified
				continue
			}
			if slices.Contains(added, agent) {
				return rule, "", "", nil
			}

		case TriggerCompleted:
			route, routed := a.policy.route(ev.BucketID)
			if routed && route.WatchCompletions {
				return rule, "", "", nil
			}
			if slices.ContainsFunc(summary.Assignees, func(p basecamp.Person) bool { return p.ID == agent }) {
				return rule, "", "", nil
			}
			subscribed, reason, err := a.subscribed(ctx, ev.RecordingID)
			if err != nil {
				return Rule{}, "", "", err
			}
			if reason != "" {
				return Rule{}, StateBlocked, reason, nil
			}
			if subscribed {
				return rule, "", "", nil
			}
		}
	}
	return Rule{}, endState, endReason, nil
}

// authorTrusted checks the person who wrote the recording, for the triggers
// whose instruction is that recording's content. The performer was trusted
// at the gate; the author is usually the same person, but not always.
func (a *Admitter) authorTrusted(ctx context.Context, ev Event, summary *basecamp.RecordingSummary) (State, Reason, error) {
	if summary.Creator == nil || summary.Creator.ID <= 0 {
		return StateDiscarded, ReasonUntrustedAuthor, nil
	}
	author := summary.Creator.ID
	switch {
	case author == a.policy.AgentID:
		return StateDiscarded, ReasonAgentAuthored, nil
	case author == ev.Performer():
		return "", "", nil
	case author == a.policy.Trust.OperatorID:
		return "", "", nil
	case a.policy.Trust.Mode == TrustAllowlist && slices.Contains(a.policy.Trust.AllowlistIDs, author):
		return "", "", nil
	case a.policy.Trust.Mode == TrustProject:
		member, reason, err := a.memberOf(ctx, ev.BucketID, author)
		switch {
		case err != nil:
			return "", "", err
		case reason != "":
			return StateBlocked, reason, nil
		case member:
			return "", "", nil
		}
	}
	return StateDiscarded, ReasonUntrustedAuthor, nil
}

// address sets the conversation key and reply destination. A comment without
// the recording it hangs off, or a chat line without its Campfire, cannot be
// answered; that is a read that did not give admission what it needs.
func (v *Verdict) address(summary *basecamp.RecordingSummary) (State, Reason) {
	switch v.EventType {
	case "chat.line.created":
		if summary.CampfireID <= 0 {
			return StateBlocked, ReasonReadFailed
		}
		v.ConversationKey = "campfire:" + strconv.FormatInt(summary.CampfireID, 10)
		v.Reply = &ReplyDestination{Kind: ReplyChatLine, RecordingID: summary.CampfireID}
	case "comment.created":
		if summary.Parent == nil || summary.Parent.ID <= 0 {
			return StateBlocked, ReasonReadFailed
		}
		v.ConversationKey = "recording:" + strconv.FormatInt(summary.Parent.ID, 10)
		v.Reply = &ReplyDestination{Kind: ReplyComment, RecordingID: summary.Parent.ID}
	default:
		v.ConversationKey = "recording:" + strconv.FormatInt(v.RecordingID, 10)
		v.Reply = &ReplyDestination{Kind: ReplyComment, RecordingID: v.RecordingID}
	}
	return "", ""
}

func (a *Admitter) subscribed(ctx context.Context, recordingID int64) (bool, Reason, error) {
	var subscribed bool
	err := a.retry(ctx, func() error {
		var err error
		subscribed, err = a.reads.Subscriptions.Subscribed(ctx, recordingID)
		return err
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, "", ctxErr
		}
		return false, ReasonReadFailed, nil
	}
	return subscribed, "", nil
}

func (a *Admitter) memberOf(ctx context.Context, bucketID, personID int64) (bool, Reason, error) {
	var member bool
	err := a.retry(ctx, func() error {
		var err error
		member, err = a.reads.Members.NonClientMember(ctx, bucketID, personID)
		return err
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, "", ctxErr
		}
		return false, ReasonReadFailed, nil
	}
	return member, "", nil
}

// retry runs read up to the configured attempts, backing off between them.
// Verdict-bearing errors from the summary read end it at once: retrying a
// chat line found under no Campfire, or a pointer with no typed read, cannot
// change the answer within one admission.
func (a *Admitter) retry(ctx context.Context, read func() error) error {
	wait := a.backoff
	var err error
	for attempt := 1; ; attempt++ {
		if err = read(); err == nil {
			return nil
		}
		if ctx.Err() != nil || final(err) || attempt >= a.attempts {
			return err
		}
		if sleepErr := a.sleep(ctx, wait); sleepErr != nil {
			return sleepErr
		}
		wait = min(wait*2, maxReadBackoff)
	}
}

// final reports errors that are answers rather than failures.
func final(err error) bool {
	return errors.Is(err, basecamp.ErrRecordingUnresolved) ||
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
	case errors.Is(err, basecamp.ErrRecordingUnresolved):
		return StateBlocked, ReasonReadUnresolved, nil
	case errors.Is(err, basecamp.ErrBucketMismatch):
		return StateDiscarded, ReasonBucketMismatch, nil
	case errors.Is(err, basecamp.ErrNoRecordingType), errors.Is(err, basecamp.ErrUnknownRecordingType):
		return StateDiscarded, ReasonUnroutable, nil
	default:
		return StateBlocked, ReasonReadFailed, nil
	}
}

// end sets the final state. A discarded record keeps no content, trigger or
// destination: it is a tombstone in the making.
func (v Verdict) end(state State, reason Reason) Verdict {
	v.State, v.Reason = state, reason
	if state == StateDiscarded {
		v.Snapshot, v.Trigger, v.Acknowledge, v.Reply, v.ConversationKey = nil, "", false, nil, ""
	}
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
