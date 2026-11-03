package admission

import (
	"context"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const secondPingAgent int64 = 53309600

func pingAgentPolicy(p Policy) Policy {
	p.Pings = map[int64]PingTrust{pingID: {Agents: []int64{peerAgent}}}
	return p
}

func agentLineInPing(t *testing.T, f *fakeReads, ping int64, people []int64, author int64, content string, actorType string) Event {
	t.Helper()
	ev := inPingAt(f, ping, people, author, content)
	f.summaries[recordingID].Creator.PersonableType = personableAgent
	ev.ActorType = actorType
	return ev
}

func TestAnAgentLetIntoAPingIsHeardThereByMention(t *testing.T) {
	group := []int64{operatorID, memberID, peerAgent}
	for _, actorType := range []string{ActorTypeAgent, ""} {
		t.Run("actor type "+actorType, func(t *testing.T) {
			f := newFakeReads()
			ev := agentLineInPing(t, f, pingID, group, peerAgent, "<div>"+mentionOf(t, agentID)+" can you take the deploy notes?</div>", actorType)

			v := decide(t, newAdmitter(t, pingAgentPolicy(basePolicy()), f), ev)

			require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
			assert.Equal(t, TriggerMentioned, v.Trigger)
			assert.True(t, v.Acknowledge)
			assert.Equal(t, RoleParticipant, v.Role)
			assert.Equal(t, peerAgent, v.RequesterID)
			assert.Equal(t, PingConversationKey(pingID), v.ConversationKey)
			assert.Equal(t, &ReplyDestination{Kind: ReplyChatLine, RecordingID: pingCampfire}, v.Reply)
			require.NotNil(t, v.Snapshot)
			assert.Equal(t, RoleParticipant, v.Snapshot.Role)
			assert.False(t, v.Snapshot.Owner)
			require.NotNil(t, v.agentCaps)
			assert.Equal(t, peerAgent, v.agentCaps.agentID)
			assert.Equal(t, []int64{peerAgent}, v.agentCaps.agents)
			assert.Equal(t, DefaultAgentThreadCap, v.agentCaps.thread)
			assert.Equal(t, DefaultAgentDailyCap, v.agentCaps.daily)
		})
	}
}

func TestAnAgentLetIntoAPingIsHeardInEveryTrustMode(t *testing.T) {
	for name, base := range map[string]Policy{"operator": basePolicy(), "project": projectPolicy()} {
		t.Run(name, func(t *testing.T) {
			f := newFakeReads()
			ev := agentLineInPing(t, f, pingID, []int64{operatorID, peerAgent}, peerAgent, "<div>"+mentionOf(t, agentID)+" status?</div>", ActorTypeAgent)

			v := decide(t, newAdmitter(t, pingAgentPolicy(base), f), ev)
			require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
			assert.Equal(t, RoleParticipant, v.Role)
			assert.Zero(t, f.memberCalls)
		})
	}
}

func TestAnAgentLetIntoAPingNeedsAMentionEvenInADirectPing(t *testing.T) {
	for name, people := range map[string][]int64{
		"group":  {operatorID, peerAgent},
		"direct": {peerAgent},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeReads()
			ev := agentLineInPing(t, f, pingID, people, peerAgent, "<div>done, over to you</div>", ActorTypeAgent)

			v := decide(t, newAdmitter(t, pingAgentPolicy(basePolicy()), f), ev)
			assert.Equal(t, StateDiscarded, v.State)
			assert.Equal(t, ReasonNotAddressed, v.Reason)
			assert.Nil(t, v.Snapshot)
			assert.Nil(t, v.agentCaps)
		})
	}

	t.Run("a mention of someone else", func(t *testing.T) {
		f := newFakeReads()
		ev := agentLineInPing(t, f, pingID, []int64{operatorID, peerAgent}, peerAgent, "<div>"+mentionOf(t, operatorID)+" done</div>", ActorTypeAgent)

		v := decide(t, newAdmitter(t, pingAgentPolicy(basePolicy()), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonNotAddressed, v.Reason)
	})
}

func TestAnAgentNotLetIntoThePingIsNotHeardThere(t *testing.T) {
	mention := func(t *testing.T) string { return "<div>" + mentionOf(t, agentID) + " your turn</div>" }

	t.Run("an agent no entry names, on the push lane", func(t *testing.T) {
		f := newFakeReads()
		ev := agentLineInPing(t, f, pingID, []int64{operatorID, peerAgent, otherAgent}, otherAgent, mention(t), ActorTypeAgent)

		v := decide(t, newAdmitter(t, pingAgentPolicy(basePolicy()), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonAgentNotAllowed, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("an agent no entry names, on the poll lane", func(t *testing.T) {
		f := newFakeReads()
		ev := agentLineInPing(t, f, pingID, []int64{operatorID, peerAgent, otherAgent}, otherAgent, mention(t), "")

		v := decide(t, newAdmitter(t, pingAgentPolicy(basePolicy()), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("an agent let into another Ping", func(t *testing.T) {
		f := newFakeReads()
		ev := agentLineInPing(t, f, otherPing, []int64{operatorID, peerAgent}, peerAgent, mention(t), ActorTypeAgent)

		v := decide(t, newAdmitter(t, pingAgentPolicy(basePolicy()), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonAgentNotAllowed, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("an agent allowed everywhere else, with no Ping entry", func(t *testing.T) {
		f := newFakeReads()
		ev := agentLineInPing(t, f, pingID, []int64{operatorID, peerAgent}, peerAgent, mention(t), ActorTypeAgent)

		v := decide(t, newAdmitter(t, agentPolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonNoRoute, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("a person let into the Ping, whose words an agent wrote", func(t *testing.T) {
		f := newFakeReads()
		p := pingAgentPolicy(basePolicy())
		p.Pings[pingID] = PingTrust{Participants: []int64{pingGuest}, Agents: []int64{peerAgent}}
		ev := inPing(f, []int64{operatorID, pingGuest, peerAgent}, pingGuest, mention(t))
		f.summaries[recordingID].Creator = &basecamp.Person{ID: peerAgent, PersonableType: personableAgent}

		v := decide(t, newAdmitter(t, p, f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonAgentAuthored, v.Reason)
	})
}

func TestAnAgentLetIntoAPingIsNobodyElsewhere(t *testing.T) {
	mention := func(t *testing.T) string { return "<div>hi " + mentionOf(t, agentID) + "</div>" }

	t.Run("in a served project", func(t *testing.T) {
		f := newFakeReads()
		f.summaries[recordingID] = agentSummary(t, peerAgent, mention(t))

		v := decide(t, newAdmitter(t, pingAgentPolicy(basePolicy()), f), agentComment(peerAgent, ActorTypeAgent))
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonAgentNotAllowed, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("in an unserved project", func(t *testing.T) {
		f := newFakeReads()
		ev := agentComment(peerAgent, ActorTypeAgent)
		ev.BucketID = unservedProj

		v := decide(t, newAdmitter(t, pingAgentPolicy(basePolicy()), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonAgentNotAllowed, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("in a served project's Campfire keyed as its Ping", func(t *testing.T) {
		f := newFakeReads()
		s := summaryWith(recordingID, servedProj, "Chat::Lines::RichText", peerAgent, mention(t))
		s.Creator.PersonableType = personableAgent
		s.CampfireID = campfireID
		f.summaries[recordingID] = s
		p := basePolicy()
		p.Pings = map[int64]PingTrust{servedProj: {Agents: []int64{peerAgent}}}

		v := decide(t, newAdmitter(t, p, f), Event{ID: eventID, EventType: "chat.line.created", BucketID: servedProj, RecordingID: recordingID, CreatorID: peerAgent, ActorType: ActorTypeAgent})
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonAgentNotAllowed, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("in a bucket the read says is no Ping", func(t *testing.T) {
		f := newFakeReads()
		s := summaryWith(recordingID, unservedProj, "Chat::Lines::RichText", peerAgent, mention(t))
		s.Creator.PersonableType = personableAgent
		s.CampfireID = campfireID
		f.summaries[recordingID] = s
		p := basePolicy()
		p.Pings = map[int64]PingTrust{unservedProj: {Agents: []int64{peerAgent}}}

		v := decide(t, newAdmitter(t, p, f), Event{ID: eventID, EventType: "chat.line.created", BucketID: unservedProj, RecordingID: recordingID, CreatorID: peerAgent, ActorType: ActorTypeAgent})
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
		assert.Zero(t, f.summaryCalls)
	})

	t.Run("boosting a line in the Ping", func(t *testing.T) {
		f := newFakeReads()
		ev := Event{ID: eventID, EventType: "boost.created", BucketID: pingID, RecordingID: recordingID, CreatorID: peerAgent, ActorType: ActorTypeAgent}

		v := decide(t, newAdmitter(t, pingAgentPolicy(basePolicy()), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonNotAddressed, v.Reason)
		assert.Zero(t, f.totalReads())
	})
}

func TestAnAgentLetIntoAPingIsHeardOnlyInWordsItWrote(t *testing.T) {
	mention := func(t *testing.T) string { return "<div>" + mentionOf(t, agentID) + " deploy</div>" }
	for name, c := range map[string]struct {
		creator *basecamp.Person
		want    Reason
	}{
		"the operator's words":           {&basecamp.Person{ID: operatorID}, ReasonUntrustedAuthor},
		"another agent let in":           {&basecamp.Person{ID: secondPingAgent, PersonableType: personableAgent}, ReasonUntrustedAuthor},
		"its own id, read as no Agent":   {&basecamp.Person{ID: peerAgent}, ReasonUntrustedAuthor},
		"the agent's own words":          {&basecamp.Person{ID: agentID, PersonableType: personableAgent}, ReasonAgentAuthored},
		"an agent no entry names at all": {&basecamp.Person{ID: otherAgent, PersonableType: personableAgent}, ReasonUntrustedAuthor},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeReads()
			p := basePolicy()
			p.Pings = map[int64]PingTrust{pingID: {Agents: []int64{peerAgent, secondPingAgent}}}
			ev := agentLineInPing(t, f, pingID, []int64{operatorID, peerAgent, secondPingAgent}, peerAgent, mention(t), ActorTypeAgent)
			f.summaries[recordingID].Creator = c.creator

			v := decide(t, newAdmitter(t, p, f), ev)
			assert.Equal(t, StateDiscarded, v.State)
			assert.Equal(t, c.want, v.Reason)
			assert.Nil(t, v.Snapshot)
		})
	}

	t.Run("a line it posted for the operator", func(t *testing.T) {
		f := newFakeReads()
		ev := inPing(f, []int64{operatorID, peerAgent}, operatorID, mention(t))
		ev.PerformedByID = ptr(peerAgent)

		v := decide(t, newAdmitter(t, pingAgentPolicy(basePolicy()), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonDelegated, v.Reason)
		assert.Zero(t, f.totalReads())
	})
}

func TestTwoAgentsInAPingAreHeldToTheCaps(t *testing.T) {
	ledger := &capLedger{fakeLedger: newFakeLedger()}
	c := NewCommitter(ledger)
	p := basePolicy()
	p.Pings = map[int64]PingTrust{pingID: {Agents: []int64{peerAgent, secondPingAgent}}}
	p.Trust.AgentThreadCap = 3
	group := []int64{operatorID, peerAgent, secondPingAgent}

	commit := func(id, author int64) Verdict {
		t.Helper()
		f := newFakeReads()
		ev := agentLineInPing(t, f, pingID, group, author, "<div>"+mentionOf(t, agentID)+" your turn</div>", ActorTypeAgent)
		ev.ID = id
		v := decide(t, newAdmitter(t, p, f), ev)
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		v, err := c.Commit(context.Background(), v)
		require.NoError(t, err)
		return v
	}

	assert.NotEqual(t, StateDiscarded, commit(1, peerAgent).State)
	assert.NotEqual(t, StateDiscarded, commit(2, secondPingAgent).State)
	assert.NotEqual(t, StateDiscarded, commit(3, peerAgent).State)
	require.Len(t, ledger.admitted, 3)
	v := commit(4, secondPingAgent)
	assert.Equal(t, StateDiscarded, v.State, "the thread cap counts both agents in the Ping")
	assert.Equal(t, ReasonAgentThreadCap, v.Reason)
	assert.Nil(t, v.Snapshot)
	assert.Empty(t, v.Trigger)

	ledger.now = 25
	assert.NotEqual(t, StateDiscarded, commit(5, secondPingAgent).State, "the window has passed")
}

func TestOneAgentsDailyCapSpansItsPingAndItsProjects(t *testing.T) {
	ledger := &capLedger{fakeLedger: newFakeLedger()}
	c := NewCommitter(ledger)
	p := pingAgentPolicy(agentPolicy())
	p.Trust.AgentDailyCap = 2

	f := newFakeReads()
	f.summaries[recordingID] = agentSummary(t, peerAgent, "<div>"+mentionOf(t, agentID)+"</div>")
	ev := agentComment(peerAgent, ActorTypeAgent)
	ev.ID = 1
	v := decide(t, newAdmitter(t, p, f), ev)
	require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
	v, err := c.Commit(context.Background(), v)
	require.NoError(t, err)
	require.NotEqual(t, StateDiscarded, v.State)

	for _, step := range []struct {
		id   int64
		want State
	}{{2, StateAdmitted}, {3, StateDiscarded}} {
		f := newFakeReads()
		ev := agentLineInPing(t, f, pingID, []int64{operatorID, peerAgent}, peerAgent, "<div>"+mentionOf(t, agentID)+" and this</div>", ActorTypeAgent)
		ev.ID = step.id
		v := decide(t, newAdmitter(t, p, f), ev)
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		v, err := c.Commit(context.Background(), v)
		require.NoError(t, err)
		if step.want == StateDiscarded {
			assert.Equal(t, StateDiscarded, v.State, "mention %d", step.id)
			assert.Equal(t, ReasonAgentDailyCap, v.Reason)
			continue
		}
		assert.NotEqual(t, StateDiscarded, v.State, "mention %d", step.id)
	}
}

func TestAPingsAgentsAreValidated(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(*Policy)
		want string
	}{
		"the agent itself": {func(p *Policy) {
			p.Pings = map[int64]PingTrust{pingID: {Agents: []int64{agentID}}}
		}, "agent never wakes itself"},
		"no Person id": {func(p *Policy) {
			p.Pings = map[int64]PingTrust{pingID: {Agents: []int64{0}}}
		}, "not a Person id"},
		"the operator": {func(p *Policy) {
			p.Pings = map[int64]PingTrust{pingID: {Agents: []int64{operatorID}}}
		}, "both a trusted person and an allowed agent"},
		"a person the allowlist names": {func(p *Policy) {
			p.Trust.Mode, p.Trust.AllowlistIDs = TrustAllowlist, []int64{allowedID}
			p.Pings = map[int64]PingTrust{pingID: {Agents: []int64{allowedID}}}
		}, "both a trusted person and an allowed agent"},
		"a participant of the same Ping": {func(p *Policy) {
			p.Pings = map[int64]PingTrust{pingID: {Participants: []int64{peerAgent}, Agents: []int64{peerAgent}}}
		}, "is an allowed agent"},
		"a participant of another Ping": {func(p *Policy) {
			p.Pings = map[int64]PingTrust{pingID: {Agents: []int64{peerAgent}}, otherPing: {Participants: []int64{peerAgent}}}
		}, "is an allowed agent"},
	} {
		t.Run(name, func(t *testing.T) {
			p := basePolicy()
			c.edit(&p)
			assert.ErrorContains(t, p.Validate(), c.want)
		})
	}
	require.NoError(t, pingAgentPolicy(agentPolicy()).Validate(), "an agent may be allowed in projects and in a Ping both")
}

func TestAllowedAgentsIsEveryAgentAnywhere(t *testing.T) {
	assert.Empty(t, basePolicy().AllowedAgents())
	assert.Equal(t, []int64{peerAgent}, pingAgentPolicy(basePolicy()).AllowedAgents())
	p := agentPolicy()
	p.Pings = map[int64]PingTrust{pingID: {Agents: []int64{secondPingAgent, peerAgent}}, otherPing: {Participants: []int64{pingGuest}}}
	assert.Equal(t, []int64{peerAgent, secondPingAgent}, p.AllowedAgents())
}

func TestParsePolicyReadsAPingsAgents(t *testing.T) {
	p, err := ParsePolicy([]byte(`{"trust":{"mode":"operator","operator_id":26909558},"pings":{"2085958513":{"agents":[53309518]}}}`))
	require.NoError(t, err)
	assert.Equal(t, map[int64]PingTrust{pingID: {Agents: []int64{peerAgent}}}, p.Pings)
}
