package admission

import (
	"context"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Agent-to-agent mentions: another agent wakes this one only when the
// operator named it in agent_ids, only by @mentioning this agent in content
// it wrote itself, and only as a participant.

// peerAgent is an agent the policy allows; otherAgent (fakes_test.go) is one
// it does not.
const peerAgent int64 = 53309518

func agentPolicy() Policy {
	p := basePolicy()
	p.Trust.AgentIDs = []int64{peerAgent}
	return p
}

// agentSummary is a comment the given agent wrote, as an Agent person.
func agentSummary(t *testing.T, author int64, content string) *basecamp.RecordingSummary {
	t.Helper()
	s := summaryWith(recordingID, servedProj, "Comment", author, content)
	s.Creator.PersonableType = personableAgent
	s.Parent = &basecamp.Parent{ID: parentID}
	return s
}

func agentComment(creator int64, actorType string) Event {
	return Event{ID: eventID, EventType: "comment.created", BucketID: servedProj, RecordingID: recordingID, CreatorID: creator, ActorType: actorType}
}

func TestWithNoAgentAllowedAnAgentsMentionIsDroppedAsToday(t *testing.T) {
	for _, lane := range []struct {
		name      string
		actorType string
		want      Reason
	}{
		// The push lane says it is an agent; the gate drops it unread.
		{"push lane", ActorTypeAgent, ReasonAgentAuthored},
		// The poll lane does not, and the agent is in no trust set.
		{"poll lane", "", ReasonUntrustedPerformer},
	} {
		t.Run(lane.name, func(t *testing.T) {
			f := newFakeReads()
			f.summaries[recordingID] = agentSummary(t, peerAgent, "<div>hi "+mentionOf(t, agentID)+"</div>")
			v := decide(t, newAdmitter(t, basePolicy(), f), agentComment(peerAgent, lane.actorType))
			assert.Equal(t, StateDiscarded, v.State)
			assert.Equal(t, lane.want, v.Reason)
			assert.Zero(t, f.totalReads())
			assert.Nil(t, v.agentCaps)
		})
	}
}

func TestAnAllowedAgentsMentionIsAdmittedAsAParticipant(t *testing.T) {
	for _, actorType := range []string{ActorTypeAgent, ""} {
		t.Run("actor type "+actorType, func(t *testing.T) {
			f := newFakeReads()
			f.summaries[recordingID] = agentSummary(t, peerAgent, "<div>hi "+mentionOf(t, agentID)+"</div>")
			v := decide(t, newAdmitter(t, agentPolicy(), f), agentComment(peerAgent, actorType))
			require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
			assert.Equal(t, TriggerMentioned, v.Trigger)
			assert.Equal(t, RoleParticipant, v.Role, "an agent's request never carries an operator's word")
			assert.Equal(t, peerAgent, v.RequesterID)
			assert.Equal(t, "recording:9000", v.ConversationKey)
			require.NotNil(t, v.agentCaps, "the committer must hold it to the caps")
			assert.Equal(t, DefaultAgentThreadCap, v.agentCaps.thread)
			assert.Equal(t, DefaultAgentDailyCap, v.agentCaps.daily)
			assert.Equal(t, peerAgent, v.agentCaps.agentID)
		})
	}
}

func TestAnAllowedAgentWithoutAMentionIsDropped(t *testing.T) {
	f := newFakeReads()
	f.summaries[recordingID] = agentSummary(t, peerAgent, "<div>thanks, all done</div>")
	// Subscribed to the thread: a person's comment here would be admitted
	// as subscribed. An agent's is not.
	f.subscriptions[parentID] = true
	v := decide(t, newAdmitter(t, agentPolicy(), f), agentComment(peerAgent, ActorTypeAgent))
	assert.Equal(t, StateDiscarded, v.State)
	assert.Equal(t, ReasonNotAddressed, v.Reason)
	assert.Empty(t, f.subCalls, "the subscribed rule is never open to an agent")
}

func TestAnAgentNotAllowedIsDropped(t *testing.T) {
	f := newFakeReads()
	f.summaries[recordingID] = agentSummary(t, otherAgent, "<div>hi "+mentionOf(t, agentID)+"</div>")
	v := decide(t, newAdmitter(t, agentPolicy(), f), agentComment(otherAgent, ActorTypeAgent))
	assert.Equal(t, StateDiscarded, v.State)
	assert.Equal(t, ReasonAgentNotAllowed, v.Reason)
	assert.Zero(t, f.totalReads())

	// On the poll lane it is no one the policy trusts.
	v = decide(t, newAdmitter(t, agentPolicy(), f), agentComment(otherAgent, ""))
	assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
}

func TestTheAgentNeverWakesItself(t *testing.T) {
	f := newFakeReads()
	f.summaries[recordingID] = agentSummary(t, agentID, "<div>note to self "+mentionOf(t, agentID)+"</div>")
	v := decide(t, newAdmitter(t, agentPolicy(), f), agentComment(agentID, ActorTypeAgent))
	assert.Equal(t, StateDiscarded, v.State)
	assert.Equal(t, ReasonAgentAuthored, v.Reason)

	p := agentPolicy()
	p.Trust.AgentIDs = []int64{peerAgent, agentID}
	require.ErrorContains(t, p.Validate(), "agent never wakes itself")
}

func TestAnAllowedAgentCannotAssignOrComplete(t *testing.T) {
	for _, c := range []struct {
		eventType string
		want      Reason
	}{
		{"card.assignment_changed", ReasonAssignmentNotOperator},
		{"todo.assignment_changed", ReasonAssignmentNotOperator},
		{"card.completed", ReasonNotAddressed},
		{"todo.completed", ReasonNotAddressed},
	} {
		t.Run(c.eventType, func(t *testing.T) {
			f := newFakeReads()
			s := agentSummary(t, peerAgent, "<div>"+mentionOf(t, agentID)+"</div>")
			s.Assignees = []basecamp.Person{{ID: agentID}}
			f.summaries[recordingID] = s
			f.assignments[eventID] = []int64{agentID}
			p := agentPolicy()
			p.Projects[servedProj] = Project{WatchCompletions: true}
			ev := Event{ID: eventID, EventType: c.eventType, BucketID: servedProj, RecordingID: recordingID, CreatorID: peerAgent, ActorType: ActorTypeAgent}
			v := decide(t, newAdmitter(t, p, f), ev)
			assert.Equal(t, StateDiscarded, v.State)
			assert.Equal(t, c.want, v.Reason)
			assert.Zero(t, f.totalReads(), "decided at the gate")
		})
	}
}

func TestAnAllowedAgentsMentionCountsOnlyInWordsItWrote(t *testing.T) {
	// The allowed agent performed the event, but the recording's author is
	// someone else: its words, not the agent's.
	f := newFakeReads()
	s := summaryWith(recordingID, servedProj, "Comment", operatorID, "<div>"+mentionOf(t, agentID)+"</div>")
	s.Parent = &basecamp.Parent{ID: parentID}
	f.summaries[recordingID] = s
	v := decide(t, newAdmitter(t, agentPolicy(), f), agentComment(peerAgent, ActorTypeAgent))
	assert.Equal(t, StateDiscarded, v.State)
	assert.Equal(t, ReasonUntrustedAuthor, v.Reason)

	// And a delegated action is never an allowed agent's own.
	ev := agentComment(operatorID, "")
	ev.PerformedByID = ptr(peerAgent)
	v = decide(t, newAdmitter(t, agentPolicy(), f), ev)
	assert.Equal(t, ReasonDelegated, v.Reason)
}

func TestAnAllowedAgentGetsNoHoldingReplyInAnUnservedProject(t *testing.T) {
	f := newFakeReads()
	s := agentSummary(t, peerAgent, "<div>hi "+mentionOf(t, agentID)+"</div>")
	s.Bucket = &basecamp.Bucket{ID: unservedProj}
	f.summaries[recordingID] = s
	ev := agentComment(peerAgent, ActorTypeAgent)
	ev.BucketID = unservedProj
	v := decide(t, newAdmitter(t, agentPolicy(), f), ev)
	assert.Equal(t, StateDiscarded, v.State, "blocked would post a holding reply the other agent could answer")
	assert.Equal(t, ReasonNoRoute, v.Reason)
}

func TestAgentPolicyValidation(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(*Policy)
		want string
	}{
		"not a person id":    {func(p *Policy) { p.Trust.AgentIDs = []int64{0} }, "not a Person id"},
		"the operator":       {func(p *Policy) { p.Trust.AgentIDs = []int64{operatorID} }, "both a trusted person and an allowed agent"},
		"allowlisted person": {func(p *Policy) { p.Trust.Mode, p.Trust.AllowlistIDs = TrustAllowlist, []int64{peerAgent} }, "both a trusted person"},
		"negative cap":       {func(p *Policy) { p.Trust.AgentThreadCap = -1 }, "agent_thread_cap"},
		"cap too high":       {func(p *Policy) { p.Trust.AgentDailyCap = MaxAgentCap + 1 }, "agent_daily_cap"},
	} {
		t.Run(name, func(t *testing.T) {
			p := agentPolicy()
			c.edit(&p)
			require.ErrorContains(t, p.Validate(), c.want)
		})
	}
	p := agentPolicy()
	p.Trust.AgentThreadCap, p.Trust.AgentDailyCap = 5, 30
	require.NoError(t, p.Validate())
	assert.Equal(t, 5, p.Trust.ThreadCap())
	assert.Equal(t, 30, p.Trust.DailyCap())
}

// capLedger counts admitted agent mentions the way the real ledger does,
// over a window measured on its own clock.
type capLedger struct {
	*fakeLedger
	now      int // hours since the start
	admitted []struct {
		key   string
		agent int64
		at    int
	}
}

func (l *capLedger) Commit(ctx context.Context, v Verdict) (State, error) {
	state, err := l.fakeLedger.Commit(ctx, v)
	if err == nil && v.State == StateAdmitted {
		l.admitted = append(l.admitted, struct {
			key   string
			agent int64
			at    int
		}{v.ConversationKey, v.RequesterID, l.now})
	}
	return state, err
}

func (l *capLedger) CountAgentRequests(_ context.Context, key string, agents []int64, window time.Duration) (int, error) {
	n := 0
	for _, a := range l.admitted {
		if (key == "" || a.key == key) && containsID(agents, a.agent) && l.now-a.at < int(window.Hours()) {
			n++
		}
	}
	return n, nil
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func TestTheThreadCapTripsAtNPlusOneAndResetsAfterTheWindow(t *testing.T) {
	ledger := &capLedger{fakeLedger: newFakeLedger()}
	c := NewCommitter(ledger)
	f := newFakeReads()
	f.summaries[recordingID] = agentSummary(t, peerAgent, "<div>"+mentionOf(t, agentID)+"</div>")
	p := agentPolicy()
	p.Trust.AgentThreadCap = 2
	a := newAdmitter(t, p, f)

	commit := func(id int64) Verdict {
		t.Helper()
		ev := agentComment(peerAgent, ActorTypeAgent)
		ev.ID = id
		v := decide(t, a, ev)
		require.Equal(t, StateAdmitted, v.State)
		v, err := c.Commit(context.Background(), v)
		require.NoError(t, err)
		return v
	}
	for id := int64(1); id <= 2; id++ {
		v := commit(id)
		assert.NotEqual(t, StateDiscarded, v.State, "mention %d is within the cap", id)
	}
	require.Len(t, ledger.admitted, 2, "the cap is reached, not merely asserted")
	v := commit(3)
	assert.Equal(t, StateDiscarded, v.State)
	assert.Equal(t, ReasonAgentThreadCap, v.Reason)
	assert.Nil(t, v.Snapshot, "a capped mention carries no content")
	assert.Empty(t, v.Trigger, "nor a trigger, so it is not counted as admitted")
	assert.Nil(t, v.Reply, "and nobody is answered")

	ledger.now = 23
	assert.Equal(t, ReasonAgentThreadCap, commit(4).Reason, "still inside 24 hours")

	ledger.now = 24
	v = commit(5)
	assert.NotEqual(t, StateDiscarded, v.State, "the window has passed")
}

func TestTheDailyCapSpansThreads(t *testing.T) {
	ledger := &capLedger{fakeLedger: newFakeLedger()}
	c := NewCommitter(ledger)
	f := newFakeReads()
	p := agentPolicy()
	p.Trust.AgentDailyCap = 2
	a := newAdmitter(t, p, f)

	for i := int64(1); i <= 3; i++ {
		// A new thread each time: the thread cap never trips.
		rec := recordingID + i
		s := agentSummary(t, peerAgent, "<div>"+mentionOf(t, agentID)+"</div>")
		s.ID, s.Parent = rec, &basecamp.Parent{ID: parentID + 100*i}
		f.summaries[rec] = s
		ev := agentComment(peerAgent, ActorTypeAgent)
		ev.ID, ev.RecordingID = i, rec
		v := decide(t, a, ev)
		require.Equal(t, StateAdmitted, v.State)
		v, err := c.Commit(context.Background(), v)
		require.NoError(t, err)
		if i <= 2 {
			assert.Equal(t, StateAdmitted, v.State)
			continue
		}
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonAgentDailyCap, v.Reason)
	}
}

func TestAnAgentsMentionIsRefusedByALedgerThatCannotCount(t *testing.T) {
	c := NewCommitter(newFakeLedger())
	f := newFakeReads()
	f.summaries[recordingID] = agentSummary(t, peerAgent, "<div>"+mentionOf(t, agentID)+"</div>")
	v := decide(t, newAdmitter(t, agentPolicy(), f), agentComment(peerAgent, ActorTypeAgent))
	_, err := c.Commit(context.Background(), v)
	require.ErrorContains(t, err, "cannot count agent mentions")
}
