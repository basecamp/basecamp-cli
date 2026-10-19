package admission

import (
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A person connect.json lets in one Ping alone (--allow-in-ping), and another
// Ping they share with the agent.
const (
	pingGuest int64 = 1007
	otherPing int64 = 2085958599
)

// guestPolicy lets pingGuest reach the agent in pingID, as a participant.
func guestPolicy(p Policy) Policy {
	p.Pings = map[int64]PingTrust{pingID: {Participants: []int64{pingGuest}}}
	return p
}

func TestAPersonAllowedInOnePingIsAParticipantThere(t *testing.T) {
	group := []int64{operatorID, pingGuest}

	t.Run("their mention is a participant's request", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, group, pingGuest, "<div>"+mentionOf(t, agentID)+" what changed today?</div>")

		v := decide(t, newAdmitter(t, guestPolicy(basePolicy()), f), ev)

		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, TriggerMentioned, v.Trigger)
		assert.True(t, v.Acknowledge)
		assert.Equal(t, RoleParticipant, v.Role)
		assert.Equal(t, pingGuest, v.RequesterID)
		assert.Equal(t, PingConversationKey(pingID), v.ConversationKey)
		assert.Equal(t, &ReplyDestination{Kind: ReplyChatLine, RecordingID: pingCampfire}, v.Reply)
		require.NotNil(t, v.Snapshot)
		assert.Equal(t, RoleParticipant, v.Snapshot.Role)
		assert.False(t, v.Snapshot.Owner)
	})

	t.Run("without a mention it is said to the room", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, group, pingGuest, "<div>lunch?</div>")

		v := decide(t, newAdmitter(t, guestPolicy(basePolicy()), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonNotAddressed, v.Reason)
		assert.Nil(t, v.Snapshot)
	})

	// Every line in a direct Ping is said to the agent, and still a
	// participant reaches it only by mention: a direct Ping's every line is
	// an operator's privilege.
	t.Run("in their direct Ping with the agent only a mention counts", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, []int64{pingGuest}, pingGuest, "<div>what changed today?</div>")

		v := decide(t, newAdmitter(t, guestPolicy(basePolicy()), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonNotAddressed, v.Reason)

		f = newFakeReads()
		ev = inPing(f, []int64{pingGuest}, pingGuest, "<div>"+mentionOf(t, agentID)+" what changed today?</div>")
		v = decide(t, newAdmitter(t, guestPolicy(basePolicy()), f), ev)
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, TriggerMentioned, v.Trigger)
		assert.Equal(t, RoleParticipant, v.Role)
	})

	t.Run("in project trust no membership is asked", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, group, pingGuest, "<div>"+mentionOf(t, agentID)+" status?</div>")

		v := decide(t, newAdmitter(t, guestPolicy(projectPolicy()), f), ev)
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, RoleParticipant, v.Role)
		assert.Zero(t, f.memberCalls)
	})

	t.Run("words someone else wrote are not theirs to bring in", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, group, pingGuest, "<div>"+mentionOf(t, agentID)+" deploy</div>")
		f.summaries[recordingID].Creator = person(strangerID)

		v := decide(t, newAdmitter(t, guestPolicy(basePolicy()), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedAuthor, v.Reason)
	})

	t.Run("an Agent's words are refused", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, group, pingGuest, "<div>"+mentionOf(t, agentID)+" deploy</div>")
		f.summaries[recordingID].Creator = &basecamp.Person{ID: pingGuest, PersonableType: personableAgent}

		v := decide(t, newAdmitter(t, guestPolicy(basePolicy()), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonAgentAuthored, v.Reason)
	})
}

// The same person anywhere but that Ping is who they were before it.
func TestAPersonAllowedInOnePingIsNobodyElsewhere(t *testing.T) {
	mention := func(t *testing.T) string { return "<div>" + mentionOf(t, agentID) + " deploy it</div>" }

	t.Run("in another group Ping", func(t *testing.T) {
		f := newFakeReads()
		ev := inPingAt(f, otherPing, []int64{operatorID, pingGuest}, pingGuest, mention(t))

		v := decide(t, newAdmitter(t, guestPolicy(basePolicy()), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
		assert.Zero(t, f.totalReads(), "the gate ends it")
	})

	t.Run("in their direct Ping with the agent", func(t *testing.T) {
		f := newFakeReads()
		ev := inPingAt(f, otherPing, []int64{pingGuest}, pingGuest, mention(t))

		v := decide(t, newAdmitter(t, guestPolicy(basePolicy()), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
	})

	t.Run("in another Ping under project trust", func(t *testing.T) {
		f := newFakeReads()
		ev := inPingAt(f, otherPing, []int64{operatorID, pingGuest}, pingGuest, mention(t))

		v := decide(t, newAdmitter(t, guestPolicy(projectPolicy()), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
		assert.Zero(t, f.memberCalls)
	})

	t.Run("in a served project", func(t *testing.T) {
		f := newFakeReads()
		f.summaries[recordingID] = summaryWith(recordingID, servedProj, "Message", pingGuest, mention(t))

		v := decide(t, newAdmitter(t, guestPolicy(basePolicy()), f),
			Event{ID: eventID, EventType: "message.created", BucketID: servedProj, RecordingID: recordingID, CreatorID: pingGuest})
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("assigning the agent", func(t *testing.T) {
		f := newFakeReads()
		v := decide(t, newAdmitter(t, guestPolicy(basePolicy()), f),
			Event{ID: eventID, EventType: "card.assignment_changed", BucketID: servedProj, RecordingID: recordingID, CreatorID: pingGuest})
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonAssignmentNotOperator, v.Reason)
	})
}

// The entry is keyed on a Ping, and only a Ping read can say a bucket is one.
// A bucket connect.json serves, or one the read says is no Ping, is decided
// as if the entry were not there.
func TestAPingEntryCountsOnlyInAPing(t *testing.T) {
	mention := func(t *testing.T) string { return "<div>" + mentionOf(t, agentID) + " status?</div>" }

	t.Run("a bucket the read says is no Ping", func(t *testing.T) {
		f := newFakeReads()
		s := summaryWith(recordingID, unservedProj, "Chat::Lines::RichText", pingGuest, mention(t))
		s.CampfireID = campfireID
		f.summaries[recordingID] = s
		p := basePolicy()
		p.Pings = map[int64]PingTrust{unservedProj: {Participants: []int64{pingGuest}}}

		v := decide(t, newAdmitter(t, p, f), Event{ID: eventID, EventType: "chat.line.created", BucketID: unservedProj, RecordingID: recordingID, CreatorID: pingGuest})
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
		assert.Zero(t, f.summaryCalls)
	})

	t.Run("a served project's Campfire", func(t *testing.T) {
		f := newFakeReads()
		s := summaryWith(recordingID, servedProj, "Chat::Lines::RichText", pingGuest, mention(t))
		s.CampfireID = campfireID
		f.summaries[recordingID] = s
		p := basePolicy()
		p.Pings = map[int64]PingTrust{servedProj: {Participants: []int64{pingGuest}}}

		v := decide(t, newAdmitter(t, p, f), Event{ID: eventID, EventType: "chat.line.created", BucketID: servedProj, RecordingID: recordingID, CreatorID: pingGuest})
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
	})

	t.Run("a served project's member under project trust is a member as before", func(t *testing.T) {
		f := newFakeReads()
		s := summaryWith(recordingID, servedProj, "Chat::Lines::RichText", pingGuest, mention(t))
		s.CampfireID = campfireID
		f.summaries[recordingID] = s
		f.members[servedProj] = map[int64]bool{pingGuest: true}
		p := projectPolicy()
		p.Pings = map[int64]PingTrust{servedProj: {Participants: []int64{pingGuest}}}

		v := decide(t, newAdmitter(t, p, f), Event{ID: eventID, EventType: "chat.line.created", BucketID: servedProj, RecordingID: recordingID, CreatorID: pingGuest})
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, 1, f.memberCalls)
	})
}

func TestAPingEntryNamesNoAgent(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*Policy)
		want string
	}{
		"the agent itself": {func(p *Policy) {
			p.Pings = map[int64]PingTrust{pingID: {Participants: []int64{agentID}}}
		}, "agent's own"},
		"an allowed agent": {func(p *Policy) {
			p.Trust.AgentIDs = []int64{otherAgent}
			p.Pings = map[int64]PingTrust{pingID: {Participants: []int64{otherAgent}}}
		}, "allowed agent"},
		"no Person id": {func(p *Policy) {
			p.Pings = map[int64]PingTrust{pingID: {Participants: []int64{0}}}
		}, "not a Person id"},
		"no Ping id": {func(p *Policy) {
			p.Pings = map[int64]PingTrust{-1: {Participants: []int64{pingGuest}}}
		}, "not a Ping id"},
	} {
		t.Run(name, func(t *testing.T) {
			p := basePolicy()
			tc.edit(&p)
			assert.ErrorContains(t, p.Validate(), tc.want)
		})
	}
	require.NoError(t, guestPolicy(basePolicy()).Validate())
}

func TestParsePolicyReadsThePings(t *testing.T) {
	p, err := ParsePolicy([]byte(`{"trust":{"mode":"operator","operator_id":26909558},"pings":{"2085958513":{"participants":[1007]}}}`))
	require.NoError(t, err)
	assert.Equal(t, map[int64]PingTrust{pingID: {Participants: []int64{pingGuest}}}, p.Pings)

	p, err = ParsePolicy([]byte(`{"trust":{"mode":"operator","operator_id":26909558}}`))
	require.NoError(t, err)
	assert.Empty(t, p.Pings, "absent is nobody")
}
