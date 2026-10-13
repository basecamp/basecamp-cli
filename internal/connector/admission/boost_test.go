package admission

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Boosts: someone boosts a recording the agent wrote. The boost is a reply,
// not a request: admitted as boosted, never acknowledged, never the owner's
// word, and only from someone who may mention the agent.

const boostID int64 = 31337

var boostedAt = time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)

// boostDetailsJSON is boost.created's details as the feed publishes them: a
// boost on the recording itself carries null event keys.
func boostDetailsJSON(id int64, onEvent *int64) json.RawMessage {
	event := "null"
	if onEvent != nil {
		event = strconv.FormatInt(*onEvent, 10)
	}
	return json.RawMessage(`{"boost_id":` + strconv.FormatInt(id, 10) +
		`,"boosted_event_id":` + event + `,"boosted_event_type":null}`)
}

func boostEvent(performer int64) Event {
	return Event{
		ID: eventID, EventType: "boost.created", BucketID: servedProj, RecordingID: recordingID,
		CreatorID: performer, Details: boostDetailsJSON(boostID, nil),
	}
}

// boostOn sets up a boost by booster on a recording of typ that author wrote.
func boostOn(f *fakeReads, typ string, author, booster int64) *basecamp.RecordingSummary {
	s := summaryWith(recordingID, servedProj, typ, author, "<div>Shall I ship the fix?</div>")
	switch {
	case typ == "Comment":
		s.Parent = &basecamp.Parent{ID: parentID}
	case strings.HasPrefix(typ, "Chat::Lines::"):
		s.CampfireID = campfireID
	}
	f.summaries[recordingID] = s
	f.boosts[boostID] = &basecamp.Boost{
		ID:        boostID,
		Content:   "yes, go",
		CreatedAt: boostedAt,
		Booster:   &basecamp.Person{ID: booster, Name: "Booster " + strconv.FormatInt(booster, 10)},
		Recording: &basecamp.Parent{ID: recordingID, Type: typ},
	}
	return s
}

func TestABoostOnTheAgentsCommentIsAdmittedAsAReply(t *testing.T) {
	f := newFakeReads()
	boostOn(f, "Comment", agentID, operatorID)

	v := decide(t, newAdmitter(t, basePolicy(), f), boostEvent(operatorID))
	require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
	assert.Equal(t, TriggerBoosted, v.Trigger)
	assert.False(t, v.Acknowledge, "a boost is a reply, not a request")
	assert.Equal(t, operatorID, v.RequesterID)
	assert.Equal(t, RoleOperator, v.Role)
	// Answered in the comment's thread, and part of its conversation.
	assert.Equal(t, &ReplyDestination{Kind: ReplyComment, RecordingID: parentID}, v.Reply)
	assert.Equal(t, "recording:9000", v.ConversationKey)

	require.NotNil(t, v.Snapshot)
	assert.Equal(t, "yes, go", v.Snapshot.Content, "the boost is the content")
	assert.Equal(t, boostedAt, v.Snapshot.UpdatedAt)
	assert.Equal(t, "Booster "+strconv.FormatInt(operatorID, 10), v.Snapshot.RequesterName)
	assert.Equal(t, RoleOperator, v.Snapshot.Role)
	assert.False(t, v.Snapshot.Owner, "a boost is never the owner's word")

	// The summary read is routed by the type the boost named, never by the
	// event type, which names no recording.
	require.Len(t, f.summaryRefs, 1)
	assert.Equal(t, "Comment", f.summaryRefs[0].RecordingType)
	assert.Equal(t, 1, f.boostCalls)
}

func TestABoostIsAnsweredWhereTheBoostedRecordingIs(t *testing.T) {
	t.Run("a chat line, in its Campfire", func(t *testing.T) {
		f := newFakeReads()
		boostOn(f, "Chat::Lines::RichText", agentID, operatorID)

		v := decide(t, newAdmitter(t, basePolicy(), f), boostEvent(operatorID))
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, &ReplyDestination{Kind: ReplyChatLine, RecordingID: campfireID}, v.Reply)
		assert.Equal(t, "campfire:8000", v.ConversationKey)
	})

	t.Run("a chat line found under no Campfire cannot be answered", func(t *testing.T) {
		f := newFakeReads()
		boostOn(f, "Chat::Lines::RichText", agentID, operatorID).CampfireID = 0

		v := decide(t, newAdmitter(t, basePolicy(), f), boostEvent(operatorID))
		assert.Equal(t, StateBlocked, v.State)
		assert.Equal(t, ReasonReadFailed, v.Reason)
	})

	t.Run("a comment without its recording cannot be answered", func(t *testing.T) {
		f := newFakeReads()
		boostOn(f, "Comment", agentID, operatorID).Parent = nil

		v := decide(t, newAdmitter(t, basePolicy(), f), boostEvent(operatorID))
		assert.Equal(t, StateBlocked, v.State)
		assert.Equal(t, ReasonReadFailed, v.Reason)
	})

	t.Run("a message, on itself", func(t *testing.T) {
		f := newFakeReads()
		boostOn(f, "Message", agentID, operatorID)

		v := decide(t, newAdmitter(t, basePolicy(), f), boostEvent(operatorID))
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, &ReplyDestination{Kind: ReplyComment, RecordingID: recordingID}, v.Reply)
		assert.Equal(t, "recording:9001", v.ConversationKey)
	})
}

func TestABoostOnSomeoneElsesRecordingIsNotAdmitted(t *testing.T) {
	for _, author := range []int64{operatorID, strangerID, otherAgent} {
		t.Run(strconv.FormatInt(author, 10), func(t *testing.T) {
			f := newFakeReads()
			boostOn(f, "Comment", author, operatorID)

			v := decide(t, newAdmitter(t, basePolicy(), f), boostEvent(operatorID))
			assert.Equal(t, StateDiscarded, v.State)
			assert.Equal(t, ReasonNotAddressed, v.Reason)
			assert.Empty(t, v.Trigger)
			assert.Nil(t, v.Snapshot, "a discarded boost carries no content")
		})
	}
}

func TestABoostIsTrustedLikeAMention(t *testing.T) {
	t.Run("a booster outside the trust set is discarded at the gate", func(t *testing.T) {
		f := newFakeReads()
		boostOn(f, "Comment", agentID, strangerID)

		v := decide(t, newAdmitter(t, basePolicy(), f), boostEvent(strangerID))
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
		assert.Zero(t, f.totalReads(), "an untrusted boost must cost no read")
	})

	t.Run("an allowlisted booster is an operator, and not the owner", func(t *testing.T) {
		p := basePolicy()
		p.Trust = Trust{Mode: TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{allowedID}}
		f := newFakeReads()
		boostOn(f, "Comment", agentID, allowedID)

		v := decide(t, newAdmitter(t, p, f), boostEvent(allowedID))
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, allowedID, v.RequesterID)
		assert.Equal(t, RoleOperator, v.Role)
		assert.False(t, v.Snapshot.Owner)
	})

	t.Run("not operator-only: in project mode a member's boost is a participant's", func(t *testing.T) {
		p := basePolicy()
		p.Trust = Trust{Mode: TrustProject, OperatorID: operatorID}
		f := newFakeReads()
		f.members[servedProj] = map[int64]bool{memberID: true}
		boostOn(f, "Comment", agentID, memberID)

		v := decide(t, newAdmitter(t, p, f), boostEvent(memberID))
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, TriggerBoosted, v.Trigger)
		assert.Equal(t, memberID, v.RequesterID)
		assert.Equal(t, RoleParticipant, v.Role)
		assert.Equal(t, 1, f.memberCalls)
	})

	t.Run("in project mode someone membership does not confirm costs no boost read", func(t *testing.T) {
		p := basePolicy()
		p.Trust = Trust{Mode: TrustProject, OperatorID: operatorID}
		f := newFakeReads()
		f.members[servedProj] = map[int64]bool{memberID: true}
		boostOn(f, "Comment", agentID, clientID)

		v := decide(t, newAdmitter(t, p, f), boostEvent(clientID))
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
		assert.Zero(t, f.boostCalls)
		assert.Zero(t, f.summaryCalls)
	})

	t.Run("a boost whose booster is not the performer is not trusted on the performer's word", func(t *testing.T) {
		f := newFakeReads()
		boostOn(f, "Comment", agentID, strangerID)

		v := decide(t, newAdmitter(t, basePolicy(), f), boostEvent(operatorID))
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedAuthor, v.Reason)
		assert.Nil(t, v.Snapshot)
	})
}

func TestNoAgentWakesAnAgentByBoosting(t *testing.T) {
	for _, c := range []struct {
		name   string
		policy Policy
		ev     Event
		want   Reason
	}{
		{"the agent boosting its own post", basePolicy(), boostEvent(agentID), ReasonAgentAuthored},
		{"another agent, on the push lane", basePolicy(), func() Event {
			ev := boostEvent(otherAgent)
			ev.ActorType = ActorTypeAgent
			return ev
		}(), ReasonAgentAuthored},
		// An allowed agent reaches this one by mention only: two agents that
		// allow each other cannot boost each other's posts in a loop.
		{"an allowed agent", agentPolicy(), boostEvent(peerAgent), ReasonNotAddressed},
		{"a person's boost an agent performed for them", basePolicy(), func() Event {
			ev := boostEvent(operatorID)
			ev.PerformedByID = ptr(otherAgent)
			return ev
		}(), ReasonDelegated},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeReads()
			boostOn(f, "Comment", agentID, c.ev.Performer())

			v := decide(t, newAdmitter(t, c.policy, f), c.ev)
			assert.Equal(t, StateDiscarded, v.State)
			assert.Equal(t, c.want, v.Reason)
			assert.Zero(t, f.totalReads())
		})
	}
}

func TestABoostSettledFromItsPointerCostsNoRead(t *testing.T) {
	t.Run("in a project connect.json does not serve", func(t *testing.T) {
		f := newFakeReads()
		boostOn(f, "Comment", agentID, operatorID)
		ev := boostEvent(operatorID)
		ev.BucketID = unservedProj

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State, "never blocked: a holding reply to a boost answers nobody")
		assert.Equal(t, ReasonNoRoute, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("on one of the recording's events", func(t *testing.T) {
		f := newFakeReads()
		boostOn(f, "Todo", agentID, operatorID)
		ev := boostEvent(operatorID)
		ev.Details = boostDetailsJSON(boostID, ptr(5150))

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonNotAddressed, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	for name, details := range map[string]json.RawMessage{
		"without details":    nil,
		"without a boost id": json.RawMessage(`{"boosted_event_id":null}`),
		// Not the same as null: nothing says the boost is on the recording.
		"without boosted_event_id": json.RawMessage(`{"boost_id":31337}`),
		"unreadable details":       json.RawMessage(`{"boost_id":"x"`),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeReads()
			boostOn(f, "Comment", agentID, operatorID)
			ev := boostEvent(operatorID)
			ev.Details = details

			v := decide(t, newAdmitter(t, basePolicy(), f), ev)
			assert.Equal(t, StateDiscarded, v.State)
			assert.Equal(t, ReasonInvalidPointer, v.Reason)
			assert.Zero(t, f.totalReads())
		})
	}
}

func TestABoostReadThatDoesNotAnswerBlocks(t *testing.T) {
	t.Run("the boost is gone", func(t *testing.T) {
		f := newFakeReads()
		boostOn(f, "Comment", agentID, operatorID)
		delete(f.boosts, boostID)

		v := decide(t, newAdmitter(t, basePolicy(), f), boostEvent(operatorID))
		assert.Equal(t, StateBlocked, v.State)
		assert.Equal(t, ReasonReadFailed, v.Reason)
		assert.Zero(t, f.summaryCalls)
	})

	t.Run("a transport failure, after its retries", func(t *testing.T) {
		f := newFakeReads()
		boostOn(f, "Comment", agentID, operatorID)
		f.boostErr = errTransport

		v := decide(t, newAdmitter(t, basePolicy(), f), boostEvent(operatorID))
		assert.Equal(t, StateBlocked, v.State)
		assert.Equal(t, ReasonReadFailed, v.Reason)
		assert.Equal(t, DefaultReadAttempts, f.boostCalls)
	})

	for name, mutate := range map[string]func(*basecamp.Boost){
		"without its recording":      func(b *basecamp.Boost) { b.Recording = nil },
		"on another recording":       func(b *basecamp.Boost) { b.Recording.ID = parentID },
		"without the recording type": func(b *basecamp.Boost) { b.Recording.Type = "" },
		"without its booster":        func(b *basecamp.Boost) { b.Booster = nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeReads()
			boostOn(f, "Comment", agentID, operatorID)
			mutate(f.boosts[boostID])

			v := decide(t, newAdmitter(t, basePolicy(), f), boostEvent(operatorID))
			assert.Equal(t, StateBlocked, v.State)
			assert.Equal(t, ReasonReadFailed, v.Reason)
			assert.Zero(t, f.summaryCalls)
		})
	}
}

func TestABoostOnATrashedPostIsStale(t *testing.T) {
	f := newFakeReads()
	boostOn(f, "Comment", agentID, operatorID).Status = "trashed"

	v := decide(t, newAdmitter(t, basePolicy(), f), boostEvent(operatorID))
	assert.Equal(t, StateDiscarded, v.State)
	assert.Equal(t, ReasonStale, v.Reason)
}

// On the poll lane no actor type shows an agent; an Agent person the operator
// wrongly allowlisted passes the gate, and the boost read is where it shows.
func TestAnAllowlistedAgentsBoostIsRefusedByTheBoostRead(t *testing.T) {
	p := basePolicy()
	p.Trust = Trust{Mode: TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{allowedID}}
	f := newFakeReads()
	boostOn(f, "Comment", agentID, allowedID)
	f.boosts[boostID].Booster.PersonableType = personableAgent

	v := decide(t, newAdmitter(t, p, f), boostEvent(allowedID))
	assert.Equal(t, StateDiscarded, v.State)
	assert.Equal(t, ReasonAgentAuthored, v.Reason)
	assert.Nil(t, v.Snapshot)
	assert.Zero(t, f.summaryCalls, "refused before the boosted recording is read")
}

// Only the operator's own boost on the agent's post may be a go, in every
// trust mode: the verdict names the ask it would answer, and the ledger
// decides whether the agent declared one. Anyone else's boost names none.
func TestOnlyTheOperatorsBoostMayBeAGo(t *testing.T) {
	t.Run("the operator's boost names the post as the ask it would answer", func(t *testing.T) {
		f := newFakeReads()
		boostOn(f, "Comment", agentID, operatorID)
		v := decide(t, newAdmitter(t, basePolicy(), f), boostEvent(operatorID))
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, recordingID, v.AskRecordingID)
	})

	t.Run("an allowlisted operator's, even opted in to assigning, names none", func(t *testing.T) {
		p := basePolicy()
		p.Trust = Trust{Mode: TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{allowedID}, AllowAssignments: true}
		f := newFakeReads()
		boostOn(f, "Comment", agentID, allowedID)
		v := decide(t, newAdmitter(t, p, f), boostEvent(allowedID))
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, RoleOperator, v.Role)
		assert.Zero(t, v.AskRecordingID)
	})

	t.Run("a project member's names none", func(t *testing.T) {
		p := basePolicy()
		p.Trust = Trust{Mode: TrustProject, OperatorID: operatorID}
		f := newFakeReads()
		f.members[servedProj] = map[int64]bool{memberID: true}
		boostOn(f, "Comment", agentID, memberID)
		v := decide(t, newAdmitter(t, p, f), boostEvent(memberID))
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Zero(t, v.AskRecordingID)
	})

	t.Run("the operator's mention names none", func(t *testing.T) {
		f := newFakeReads()
		s := summaryWith(recordingID, servedProj, "Message", operatorID, mentionOf(t, agentID))
		f.summaries[recordingID] = s
		v := decide(t, newAdmitter(t, basePolicy(), f), Event{
			ID: eventID, EventType: "message.created", BucketID: servedProj, RecordingID: recordingID, CreatorID: operatorID,
		})
		require.Equal(t, StateAdmitted, v.State)
		assert.Zero(t, v.AskRecordingID)
	})

	t.Run("the operator's boost on someone else's post is not admitted at all", func(t *testing.T) {
		f := newFakeReads()
		boostOn(f, "Comment", strangerID, operatorID)
		v := decide(t, newAdmitter(t, basePolicy(), f), boostEvent(operatorID))
		assert.Equal(t, StateDiscarded, v.State)
		assert.Zero(t, v.AskRecordingID)
	})
}

// The fingerprint covers a titled post's title and content, length-prefixed so
// the fields cannot run together, and a comment's or line's content alone.
func TestAskFingerprint(t *testing.T) {
	const content = "<div>push abc123</div>"
	assert.Equal(t, AskFingerprint("Comment", "Re: a card", content), AskFingerprint("Comment", "Re: renamed card", content),
		"a comment's title is its parent's, and is not the ask's")
	assert.Equal(t, AskFingerprint("Chat::Lines::RichText", "", content), AskFingerprint("Comment", "", content))
	assert.Equal(t, AskFingerprint("Question::Answer", "What did you work on?", content), AskFingerprint("Question::Answer", "Renamed question", content),
		"a check-in answer's title is its question's, and is not the ask's")
	assert.Empty(t, AskTitle("Question::Answer", "What did you work on?"), "nor is it handed off as the ask's words")
	assert.NotEqual(t, AskFingerprint("Message", "Push abc123?", content), AskFingerprint("Message", "Deploy production?", content),
		"a message's subject is part of what it asks")
	assert.NotEqual(t, AskFingerprint("Message", "ab", "c"), AskFingerprint("Message", "a", "bc"),
		"the fields cannot run together")
	assert.Equal(t, AskFingerprint("Kanban::Card", "Push abc123?", content), AskFingerprint("Kanban::Card", "Push abc123?", content),
		"an unedited post read twice fingerprints the same")
}
