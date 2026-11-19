package admission

import (
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A boost in a Ping: someone boosts a line in a Ping the agent is in. It is
// heard as a boost on the agent's comment is in a project — a reply, from
// whoever may mention the agent there — and only by the connector that hears
// the agent's Pings.

// boostInPing puts a line author wrote in the Ping whose others are people,
// boosted by booster, and returns the boost's event.
func boostInPing(t *testing.T, f *fakeReads, people []int64, author, booster int64) Event {
	t.Helper()
	inPing(f, people, author, "<div>Shall I ship the fix?</div>")
	f.boosts[boostID] = &basecamp.Boost{
		ID:        boostID,
		Content:   "yes, go",
		CreatedAt: boostedAt,
		Booster:   &basecamp.Person{ID: booster, Name: "Booster"},
		Recording: &basecamp.Parent{ID: recordingID, Type: "Chat::Lines::RichText"},
	}
	return Event{
		ID: eventID, EventType: "boost.created", BucketID: pingID, RecordingID: recordingID,
		CreatorID: booster, Details: boostDetailsJSON(boostID, nil),
	}
}

func TestTheOperatorsBoostOnTheAgentsPingLineIsAdmittedAsAReply(t *testing.T) {
	for name, people := range map[string][]int64{
		"a direct Ping": {operatorID},
		"a group Ping":  {operatorID, memberID},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeReads()
			ev := boostInPing(t, f, people, agentID, operatorID)

			v := decide(t, newAdmitter(t, basePolicy(), f), ev)

			require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
			assert.Equal(t, TriggerBoosted, v.Trigger)
			assert.False(t, v.Acknowledge, "a boost is a reply, not a request")
			assert.Equal(t, RoleOperator, v.Role)
			assert.Equal(t, operatorID, v.RequesterID)
			assert.Equal(t, &ReplyDestination{Kind: ReplyChatLine, RecordingID: pingCampfire}, v.Reply)
			assert.Equal(t, PingConversationKey(pingID), v.ConversationKey)
			assert.True(t, IsPingConversation(v.ConversationKey, pingID), "handed off as a Ping's")
			assert.False(t, v.Served)
			require.NotNil(t, v.Snapshot)
			assert.Equal(t, "yes, go", v.Snapshot.Content, "the boost is the content")
			assert.False(t, v.Snapshot.Owner, "a boost is never the owner's word on the connector")
			assert.Empty(t, v.Snapshot.ProjectName)
			// As in a project: the operator's boost names the line as the ask
			// it would answer, and the ledger decides whether one was declared.
			assert.Equal(t, recordingID, v.AskRecordingID)
			assert.Equal(t, "Chat::Lines::RichText", f.summaryRefs[0].RecordingType)
		})
	}
}

func TestAPersonAllowedInOnePingMayBoostTheAgentsLineThere(t *testing.T) {
	f := newFakeReads()
	ev := boostInPing(t, f, []int64{operatorID, pingGuest}, agentID, pingGuest)

	v := decide(t, newAdmitter(t, guestPolicy(basePolicy()), f), ev)

	require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
	assert.Equal(t, TriggerBoosted, v.Trigger)
	assert.Equal(t, RoleParticipant, v.Role)
	assert.Equal(t, pingGuest, v.RequesterID)
	assert.Zero(t, v.AskRecordingID, "only the operator's boost may be a go")
	assert.False(t, v.Snapshot.Owner)
}

func TestABoostInAPingIsTrustedLikeAMentionThere(t *testing.T) {
	t.Run("someone the policy does not name is discarded unread", func(t *testing.T) {
		f := newFakeReads()
		ev := boostInPing(t, f, []int64{operatorID, strangerID}, agentID, strangerID)

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("a person let into another Ping is nobody in this one", func(t *testing.T) {
		f := newFakeReads()
		p := basePolicy()
		p.Pings = map[int64]PingTrust{otherPing: {Participants: []int64{pingGuest}}}
		ev := boostInPing(t, f, []int64{operatorID, pingGuest}, agentID, pingGuest)

		v := decide(t, newAdmitter(t, p, f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
		assert.Zero(t, f.boostCalls)
		assert.Zero(t, f.summaryCalls)
	})

	t.Run("in project trust a member is nobody in a Ping", func(t *testing.T) {
		f := newFakeReads()
		f.members[pingID] = map[int64]bool{memberID: true}
		ev := boostInPing(t, f, []int64{operatorID, memberID}, agentID, memberID)

		v := decide(t, newAdmitter(t, projectPolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
		assert.Zero(t, f.memberCalls, "a Ping has no members to ask about")
		assert.Zero(t, f.boostCalls)
	})

	t.Run("a boost whose booster is not the performer", func(t *testing.T) {
		f := newFakeReads()
		ev := boostInPing(t, f, []int64{operatorID, strangerID}, agentID, strangerID)
		ev.CreatorID = operatorID

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedAuthor, v.Reason)
	})

	t.Run("no agent wakes the agent by boosting in a Ping", func(t *testing.T) {
		for name, c := range map[string]struct {
			policy Policy
			ev     func(Event) Event
			want   Reason
		}{
			"the agent itself": {basePolicy(), func(ev Event) Event { ev.CreatorID = agentID; return ev }, ReasonAgentAuthored},
			"an allowed agent": {agentPolicy(), func(ev Event) Event { ev.CreatorID = peerAgent; return ev }, ReasonNoRoute},
			"a person's boost an agent performed": {basePolicy(), func(ev Event) Event {
				ev.PerformedByID = ptr(otherAgent)
				return ev
			}, ReasonDelegated},
		} {
			t.Run(name, func(t *testing.T) {
				f := newFakeReads()
				ev := c.ev(boostInPing(t, f, []int64{operatorID}, agentID, operatorID))

				v := decide(t, newAdmitter(t, c.policy, f), ev)
				assert.Equal(t, StateDiscarded, v.State)
				assert.Equal(t, c.want, v.Reason)
				assert.Zero(t, f.totalReads())
			})
		}
	})
}

func TestABoostOnSomeoneElsesPingLineIsNotTheAgents(t *testing.T) {
	for name, author := range map[string]int64{
		"the operator's own line": operatorID,
		"someone else's line":     memberID,
		"another agent's line":    otherAgent,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeReads()
			ev := boostInPing(t, f, []int64{operatorID, memberID}, author, operatorID)

			v := decide(t, newAdmitter(t, basePolicy(), f), ev)
			assert.Equal(t, StateDiscarded, v.State)
			assert.Equal(t, ReasonNotAddressed, v.Reason)
			assert.Nil(t, v.Snapshot)
			assert.Zero(t, v.AskRecordingID)
		})
	}
}

// Only the connector that hears the agent's Pings hears a boost in one, so
// one boost is answered once. Any other discards it at the gate, unread.
func TestAConnectorThatDoesNotHearPingsDiscardsABoostInOne(t *testing.T) {
	f := newFakeReads()
	ev := boostInPing(t, f, []int64{operatorID}, agentID, operatorID)
	p := basePolicy()
	p.HearsPings = false

	v := decide(t, newAdmitter(t, p, f), ev)
	assert.Equal(t, StateDiscarded, v.State)
	assert.Equal(t, ReasonNoRoute, v.Reason)
	assert.Zero(t, f.totalReads())
}

func TestABoostInAPingWithConnectJSONUnreadableIsHeld(t *testing.T) {
	f := newFakeReads()
	ev := boostInPing(t, f, []int64{operatorID}, agentID, operatorID)
	p := basePolicy()
	p.ProjectsUnknown = true

	v := decide(t, newAdmitter(t, p, f), ev)
	assert.Equal(t, StateBlocked, v.State)
	assert.Equal(t, ReasonConfigUnreadable, v.Reason)
	assert.Zero(t, f.totalReads())
}
