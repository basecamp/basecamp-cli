package admission

import (
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Ping's bucket and its one Campfire. A Ping is never in connect.json.
const (
	pingID       int64 = 2085958513
	pingCampfire int64 = 1069480181
)

// inPing puts a line in the Ping whose others, as the agent sees them, are
// people, written by author, and returns its event, performed by author.
func inPing(f *fakeReads, people []int64, author int64, content string) Event {
	return inPingAt(f, pingID, people, author, content)
}

// inPingAt is inPing in the Ping whose bucket is ping.
func inPingAt(f *fakeReads, ping int64, people []int64, author int64, content string) Event {
	read := &basecamp.Ping{ID: ping}
	for _, id := range people {
		read.People = append(read.People, basecamp.Person{ID: id})
	}
	f.pings[ping] = read
	s := summaryWith(recordingID, ping, "Chat::Lines::RichText", author, content)
	s.Bucket = &basecamp.Bucket{ID: ping, Name: "Rob Zolkos", Type: "Circle"}
	s.CampfireID = pingCampfire
	f.summaries[recordingID] = s
	return Event{ID: eventID, EventType: "chat.line.created", BucketID: ping, RecordingID: recordingID, CreatorID: author}
}

func projectPolicy() Policy {
	p := basePolicy()
	p.Trust.Mode = TrustProject
	return p
}

func TestAnOperatorsLineInADirectPingIsARequest(t *testing.T) {
	f := newFakeReads()
	ev := inPing(f, []int64{operatorID}, operatorID, "<div>what's on my plate?</div>")

	v := decide(t, newAdmitter(t, basePolicy(), f), ev)

	require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
	assert.Equal(t, TriggerPinged, v.Trigger, "no mention is needed in a direct Ping")
	assert.True(t, v.Acknowledge)
	assert.Equal(t, RoleOperator, v.Role)
	assert.Equal(t, operatorID, v.RequesterID)
	assert.Equal(t, &ReplyDestination{Kind: ReplyChatLine, RecordingID: pingCampfire}, v.Reply)
	assert.Equal(t, PingConversationKey(pingID), v.ConversationKey)
	assert.True(t, IsPingConversation(v.ConversationKey, pingID))
	assert.False(t, v.Served, "connect.json serves no Ping")
	assert.Empty(t, v.Class)
	require.NotNil(t, v.Snapshot)
	assert.Equal(t, "<div>what's on my plate?</div>", v.Snapshot.Content)
	assert.True(t, v.Snapshot.Owner, "the owner's own line in their direct Ping is their word")
	assert.Empty(t, v.Snapshot.ProjectName, "a Ping is no project")
}

func TestAPersonTheAllowlistNamesIsHeardInADirectPing(t *testing.T) {
	f := newFakeReads()
	p := basePolicy()
	p.Trust.Mode, p.Trust.AllowlistIDs = TrustAllowlist, []int64{allowedID}
	ev := inPing(f, []int64{allowedID}, allowedID, "<div>summarize the launch</div>")

	v := decide(t, newAdmitter(t, p, f), ev)

	require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
	assert.Equal(t, TriggerPinged, v.Trigger)
	assert.Equal(t, RoleOperator, v.Role)
	assert.False(t, v.Snapshot.Owner, "an operator, not the owner")
}

// Who may give the agent work by Ping: the operator and the people the
// allowlist names. Project trust admits a project's members, and a Ping is no
// project, so it admits nobody there.
func TestOnlyOperatorsGiveTheAgentWorkByPing(t *testing.T) {
	t.Run("someone the policy does not name is discarded unread", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, []int64{strangerID}, strangerID, "<div>hi</div>")

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("in project trust a member is no operator in a Ping", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, []int64{memberID}, memberID, "<div>deploy it</div>")
		// Were a membership read asked about the Ping's bucket and said yes,
		// the line would still not be a member's to give.
		f.members[pingID] = map[int64]bool{memberID: true}

		v := decide(t, newAdmitter(t, projectPolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
		assert.Zero(t, f.memberCalls, "a Ping has no members to ask about")
		assert.Zero(t, f.summaryCalls, "nor any line to read")
	})

	t.Run("in project trust a stranger is discarded, not held on a membership read a Ping refuses", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, []int64{memberID}, memberID, "<div>deploy it</div>")
		// Basecamp's project people listing answers 404 for a Ping's bucket.
		f.memberErr = &basecamp.Error{Code: basecamp.CodeNotFound, Message: "not found"}

		v := decide(t, newAdmitter(t, projectPolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
	})

	t.Run("an operator bringing in someone else's words brings in nothing", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, []int64{operatorID}, strangerID, "<div>deploy it</div>")
		ev.CreatorID = operatorID

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedAuthor, v.Reason)
	})

	t.Run("in project trust the operator is still heard", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, []int64{operatorID}, operatorID, "<div>status?</div>")

		v := decide(t, newAdmitter(t, projectPolicy(), f), ev)
		assert.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, RoleOperator, v.Role)
		assert.Zero(t, f.memberCalls)
	})
}

// In a group Ping a line is said to the room, so it is the agent's business
// only when it mentions the agent, by Person id.
func TestAGroupPingLineIsARequestOnlyAsAMention(t *testing.T) {
	group := []int64{operatorID, memberID}

	t.Run("without a mention", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, group, operatorID, "<div>lunch?</div>")

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonNotAddressed, v.Reason)
		assert.Nil(t, v.Snapshot)
	})

	t.Run("with a mention", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, group, operatorID, "<div>"+mentionOf(t, agentID)+" summarize this thread</div>")

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, TriggerMentioned, v.Trigger)
		assert.True(t, v.Snapshot.Owner)
		assert.Equal(t, PingConversationKey(pingID), v.ConversationKey)
		assert.Equal(t, &ReplyDestination{Kind: ReplyChatLine, RecordingID: pingCampfire}, v.Reply)
	})

	t.Run("a mention of someone else is not one of the agent", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, group, operatorID, "<div>"+mentionOf(t, memberID)+" can you look?</div>")

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonNotAddressed, v.Reason)
	})
}

// Direct is read from the Ping's people as the agent sees them, the others in
// it: exactly one, who wrote the line. Basecamp has no group flag on the
// wire, so a group that has shrunk to the agent and one person reads as
// direct, which it now is: nobody else reads the line.
func TestADirectPingIsTheAgentAndTheOneWhoWrote(t *testing.T) {
	for name, tc := range map[string]struct {
		people []int64
		want   State
	}{
		"the agent and the writer":               {people: []int64{operatorID}, want: StateAdmitted},
		"the agent, the writer and someone":      {people: []int64{operatorID, memberID}, want: StateDiscarded},
		"the agent alone, everyone else gone":    {people: []int64{agentID}, want: StateDiscarded},
		"the agent and someone, the writer gone": {people: []int64{memberID}, want: StateDiscarded},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeReads()
			ev := inPing(f, tc.people, operatorID, "<div>status?</div>")

			v := decide(t, newAdmitter(t, basePolicy(), f), ev)
			assert.Equal(t, tc.want, v.State, "reason %q", v.Reason)
		})
	}
}

// No agent authorizes in a Ping either: the agent's own line, another agent's
// and a delegated one never wake it. An agent the operator allows to mention
// this one is an agent's request, a participant's, and a Ping hears none.
func TestNoAgentAuthorizesInAPing(t *testing.T) {
	t.Run("the agent's own line", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, []int64{operatorID}, agentID, "<div>done</div>")

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonAgentAuthored, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("an allowed agent's mention", func(t *testing.T) {
		f := newFakeReads()
		p := basePolicy()
		p.Trust.AgentIDs = []int64{otherAgent}
		ev := inPing(f, []int64{operatorID, otherAgent}, otherAgent, "<div>"+mentionOf(t, agentID)+" your turn</div>")
		ev.ActorType = ActorTypeAgent
		f.summaries[recordingID].Creator.PersonableType = personableAgent

		v := decide(t, newAdmitter(t, p, f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonNoRoute, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("another agent on the push lane", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, []int64{otherAgent}, otherAgent, "<div>"+mentionOf(t, agentID)+"</div>")
		ev.ActorType = ActorTypeAgent

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonAgentAuthored, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("a line an agent posted on the operator's behalf", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, []int64{operatorID}, operatorID, "<div>deploy</div>")
		ev.PerformedByID = ptr(otherAgent)

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonDelegated, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("an Agent's words, seen only in the read", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, []int64{operatorID}, operatorID, "<div>deploy</div>")
		f.summaries[recordingID].Creator = &basecamp.Person{ID: operatorID, PersonableType: personableAgent}

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonAgentAuthored, v.Reason)
	})
}

// Discarded means verified: a Ping read that failed holds the line.
func TestAPingReadThatFailsBlocks(t *testing.T) {
	f := newFakeReads()
	ev := inPing(f, []int64{operatorID}, operatorID, "<div>status?</div>")
	f.pingErr = errTransport

	v := decide(t, newAdmitter(t, basePolicy(), f), ev)
	assert.Equal(t, StateBlocked, v.State)
	assert.Equal(t, ReasonReadFailed, v.Reason)
	assert.Equal(t, DefaultReadAttempts, f.pingCalls)
}

// A project's Campfire is no Ping: a line there is decided as it always was,
// and a served project's lines never ask.
func TestAProjectsCampfireIsNotAPing(t *testing.T) {
	t.Run("unserved", func(t *testing.T) {
		f := newFakeReads()
		s := summaryWith(recordingID, unservedProj, "Chat::Lines::RichText", operatorID, mentionOf(t, agentID))
		s.CampfireID = campfireID
		f.summaries[recordingID] = s

		v := decide(t, newAdmitter(t, basePolicy(), f), Event{ID: eventID, EventType: "chat.line.created", BucketID: unservedProj, RecordingID: recordingID, CreatorID: operatorID})
		assert.Equal(t, StateBlocked, v.State)
		assert.Equal(t, ReasonNoRoute, v.Reason)
		assert.Equal(t, "campfire:8000", v.ConversationKey)
	})

	t.Run("served", func(t *testing.T) {
		f := newFakeReads()
		s := summaryWith(recordingID, servedProj, "Chat::Lines::RichText", operatorID, "<div>lunch?</div>")
		s.CampfireID = campfireID
		f.summaries[recordingID] = s

		v := decide(t, newAdmitter(t, basePolicy(), f), Event{ID: eventID, EventType: "chat.line.created", BucketID: servedProj, RecordingID: recordingID, CreatorID: operatorID})
		assert.Equal(t, ReasonNotAddressed, v.Reason)
		assert.Zero(t, f.pingCalls)
	})
}
