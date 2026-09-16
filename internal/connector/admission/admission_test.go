package admission

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// catalog is every public type bc3's account event feed serves
// (Event::EventType::KINDS plus boost.created), as of bc3 master.
var catalog = []string{
	"comment.created",
	"comment.content_changed",
	"message.created",
	"message.content_changed",
	"message.subject_changed",
	"todo.created",
	"todo.completed",
	"todo.moved",
	"todo.content_changed",
	"todo.description_changed",
	"todo.scheduled",
	"todo.rescheduled",
	"todo.unscheduled",
	"todo.assignment_changed",
	"card.created",
	"card.completed",
	"card.moved",
	"card.content_changed",
	"card.title_changed",
	"card.due_on_changed",
	"card.assignment_changed",
	"chat.line.created",
	"question.created",
	"question.answer.created",
	"boost.created",
}

const (
	recordingID int64 = 9001
	parentID    int64 = 9000
	campfireID  int64 = 8000
	eventID     int64 = 424242
)

// TestEveryCatalogueTypeHasAVerdict runs one operator-performed event of every
// cataloged type, in a routed project, through admission. The v1 matrix types
// are admitted under their trigger; everything else is discarded at the gate
// for the price of its pointer.
func TestEveryCatalogueTypeHasAVerdict(t *testing.T) {
	admitted := map[string]Trigger{
		"comment.created":         TriggerMentioned,
		"message.created":         TriggerMentioned,
		"todo.created":            TriggerMentioned,
		"card.created":            TriggerMentioned,
		"chat.line.created":       TriggerMentioned,
		"todo.assignment_changed": TriggerAssigned,
		"card.assignment_changed": TriggerAssigned,
		"todo.completed":          TriggerCompleted,
		"card.completed":          TriggerCompleted,
	}
	require.Len(t, V1Matrix(), len(admitted), "the matrix and this table must name the same types")

	for _, eventType := range catalog {
		t.Run(eventType, func(t *testing.T) {
			f := newFakeReads()
			s := summaryWith(recordingID, routedProj, "Kanban::Card", operatorID, "<div>please look "+mentionOf(t, agentID)+"</div>")
			s.Parent = &basecamp.Parent{ID: parentID}
			s.CampfireID = campfireID
			s.Assignees = []basecamp.Person{{ID: agentID}}
			f.summaries[recordingID] = s
			f.assignments[eventID] = []int64{agentID}

			v := decide(t, newAdmitter(t, basePolicy(), f), Event{
				ID: eventID, EventType: eventType, BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID,
			})

			want, inMatrix := admitted[eventType]
			if !inMatrix {
				assert.Equal(t, StateDiscarded, v.State)
				assert.Equal(t, ReasonNotInMatrix, v.Reason)
				assert.Zero(t, f.totalReads(), "a type outside the matrix must cost no read")
				return
			}
			assert.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
			assert.Equal(t, want, v.Trigger)
			assert.Equal(t, operatorID, v.RequesterID)
			assert.Equal(t, "/work/connector", v.Route)
			assert.Equal(t, "internal", v.Class)
			require.NotNil(t, v.Snapshot)
			assert.Contains(t, v.Snapshot.Content, "please look")
		})
	}
}

func TestMentionIsMatchedByPersonIDNeverByName(t *testing.T) {
	f := newFakeReads()
	// The agent's name, typed and inside someone else's mention figure: no
	// attachment names the agent's Person id.
	content := `<div>@Marie Chef (Agent) please <bc-attachment sgid="` + basecampSGID(strangerID) +
		`" content-type="application/vnd.basecamp.mention"><figure><img alt="Marie Chef (Agent)">Marie Chef (Agent)</figure></bc-attachment></div>`
	s := summaryWith(recordingID, routedProj, "Message", operatorID, content)
	require.Equal(t, []int64{strangerID}, s.MentionedPersonIDs)
	f.summaries[recordingID] = s

	v := decide(t, newAdmitter(t, basePolicy(), f), Event{
		ID: eventID, EventType: "message.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID,
	})
	assert.Equal(t, StateDiscarded, v.State)
	assert.Equal(t, ReasonNotAddressed, v.Reason)
}

func TestPublishedDraft(t *testing.T) {
	// Publishing a draft is served as message.created (bc3 maps message_active
	// to it). What decides is the recording's status when admission reads it.
	for _, tc := range []struct {
		status string
		state  State
		reason Reason
	}{
		{"active", StateAdmitted, ""},
		{"drafted", StateDiscarded, ReasonStale},
		{"trashed", StateDiscarded, ReasonStale},
	} {
		t.Run(tc.status, func(t *testing.T) {
			f := newFakeReads()
			s := summaryWith(recordingID, routedProj, "Message", operatorID, mentionOf(t, agentID))
			s.Status = tc.status
			f.summaries[recordingID] = s

			v := decide(t, newAdmitter(t, basePolicy(), f), Event{
				ID: eventID, EventType: "message.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID,
			})
			assert.Equal(t, tc.state, v.State)
			assert.Equal(t, tc.reason, v.Reason)
			if tc.state == StateAdmitted {
				assert.Equal(t, TriggerMentioned, v.Trigger)
				assert.True(t, v.Acknowledge)
				assert.Equal(t, &ReplyDestination{Kind: ReplyComment, RecordingID: recordingID}, v.Reply)
				assert.Equal(t, "recording:9001", v.ConversationKey)
			} else {
				assert.Nil(t, v.Snapshot, "a stale record keeps no content")
			}
		})
	}
}

func TestChatLine(t *testing.T) {
	ev := Event{ID: eventID, EventType: "chat.line.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID}

	t.Run("mention in a line is answered in its Campfire", func(t *testing.T) {
		f := newFakeReads()
		s := summaryWith(recordingID, routedProj, "Chat::Lines::RichText", operatorID, mentionOf(t, agentID))
		s.CampfireID = campfireID
		f.summaries[recordingID] = s

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateAdmitted, v.State)
		assert.Equal(t, &ReplyDestination{Kind: ReplyChatLine, RecordingID: campfireID}, v.Reply)
		assert.Equal(t, "campfire:8000", v.ConversationKey)
	})

	t.Run("a line under no visible Campfire is blocked, not retried in place", func(t *testing.T) {
		f := newFakeReads()
		f.summaryErrs = []error{&basecamp.UnresolvedRecordingError{BucketID: routedProj, RecordingID: recordingID}}

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateBlocked, v.State)
		assert.Equal(t, ReasonReadUnresolved, v.Reason)
		assert.Equal(t, 1, f.summaryCalls)
		_, retried := NextBlockedRetry(v.Reason, testNow, testNow)
		assert.True(t, retried, "read_unresolved is recovered like read_failed")
	})

	t.Run("a resolved line without its Campfire cannot be answered", func(t *testing.T) {
		f := newFakeReads()
		f.summaries[recordingID] = summaryWith(recordingID, routedProj, "Chat::Lines::RichText", operatorID, mentionOf(t, agentID))

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateBlocked, v.State)
		assert.Equal(t, ReasonReadFailed, v.Reason)
	})
}

func TestAssignments(t *testing.T) {
	ev := func(performer int64) Event {
		return Event{ID: eventID, EventType: "card.assignment_changed", BucketID: routedProj, RecordingID: recordingID, CreatorID: performer}
	}
	card := func(f *fakeReads) {
		f.summaries[recordingID] = summaryWith(recordingID, routedProj, "Kanban::Card", strangerID, "<div>a card</div>")
	}

	t.Run("an assignment that adds someone else is discarded", func(t *testing.T) {
		f := newFakeReads()
		card(f)
		f.assignments[eventID] = []int64{strangerID}
		// The agent is a current assignee from an earlier assignment; that is
		// not evidence this event added it.
		f.summaries[recordingID].Assignees = []basecamp.Person{{ID: agentID}, {ID: strangerID}}

		v := decide(t, newAdmitter(t, basePolicy(), f), ev(operatorID))
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonNotAddressed, v.Reason)
	})

	t.Run("the operator assigning the agent admits it", func(t *testing.T) {
		f := newFakeReads()
		card(f)
		f.assignments[eventID] = []int64{strangerID, agentID}

		v := decide(t, newAdmitter(t, basePolicy(), f), ev(operatorID))
		assert.Equal(t, StateAdmitted, v.State)
		assert.Equal(t, TriggerAssigned, v.Trigger)
		assert.True(t, v.Acknowledge)
		assert.Equal(t, &ReplyDestination{Kind: ReplyComment, RecordingID: recordingID}, v.Reply)
	})

	t.Run("an event not found within the bound is blocked, not judged from current assignees", func(t *testing.T) {
		f := newFakeReads()
		card(f)
		f.summaries[recordingID].Assignees = []basecamp.Person{{ID: agentID}}

		v := decide(t, newAdmitter(t, basePolicy(), f), ev(operatorID))
		assert.Equal(t, StateBlocked, v.State)
		assert.Equal(t, ReasonDeltaUnverified, v.Reason)
	})

	t.Run("assignments are operator-only in every trust mode", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			policy    func(*Policy)
			performer int64
		}{
			{"allowlisted person", func(p *Policy) {
				p.Trust = Trust{Mode: TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{allowedID}}
			}, allowedID},
			{"project member", func(p *Policy) { p.Trust = Trust{Mode: TrustProject, OperatorID: operatorID} }, memberID},
			{"stranger", func(*Policy) {}, strangerID},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newFakeReads()
				card(f)
				f.members[routedProj] = map[int64]bool{memberID: true}
				f.assignments[eventID] = []int64{agentID}
				p := basePolicy()
				tc.policy(&p)

				v := decide(t, newAdmitter(t, p, f), ev(tc.performer))
				assert.Equal(t, StateDiscarded, v.State)
				assert.Equal(t, ReasonAssignmentNotOperator, v.Reason)
				assert.Zero(t, f.totalReads(), "the gate refuses a non-operator assignment without a read")
			})
		}
	})
}

func TestAReadThatFailsFiveTimesBlocks(t *testing.T) {
	ev := Event{ID: eventID, EventType: "card.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID}

	t.Run("five failures", func(t *testing.T) {
		f := newFakeReads()
		f.summaryErrs = []error{errTransport, errTransport, errTransport, errTransport, errTransport, nil}
		f.summaries[recordingID] = summaryWith(recordingID, routedProj, "Kanban::Card", operatorID, mentionOf(t, agentID))

		var waits int
		a, err := NewAdmitter(basePolicy(), f.reads(), WithSleep(func(context.Context, time.Duration) error { waits++; return nil }))
		require.NoError(t, err)
		v := decide(t, a, ev)
		assert.Equal(t, StateBlocked, v.State)
		assert.Equal(t, ReasonReadFailed, v.Reason)
		assert.Equal(t, DefaultReadAttempts, f.summaryCalls)
		assert.Equal(t, DefaultReadAttempts-1, waits, "a backoff between attempts, none after the last")
	})

	t.Run("four failures then an answer", func(t *testing.T) {
		f := newFakeReads()
		f.summaryErrs = []error{errTransport, errTransport, errTransport, errTransport}
		f.summaries[recordingID] = summaryWith(recordingID, routedProj, "Kanban::Card", operatorID, mentionOf(t, agentID))

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateAdmitted, v.State)
		assert.Equal(t, 5, f.summaryCalls)
	})

	t.Run("a failed subscription read blocks too", func(t *testing.T) {
		f := newFakeReads()
		f.summaries[recordingID] = summaryWith(recordingID, routedProj, "Kanban::Card", operatorID, "<div>done</div>")
		f.subErr = errTransport

		v := decide(t, newAdmitter(t, basePolicy(), f), Event{ID: eventID, EventType: "card.completed", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID})
		assert.Equal(t, StateBlocked, v.State)
		assert.Equal(t, ReasonReadFailed, v.Reason)
		assert.Len(t, f.subCalls, DefaultReadAttempts)
	})

	t.Run("a failed assignment read blocks too", func(t *testing.T) {
		f := newFakeReads()
		f.summaries[recordingID] = summaryWith(recordingID, routedProj, "Kanban::Card", operatorID, "<div>card</div>")
		f.assignErr = errTransport

		v := decide(t, newAdmitter(t, basePolicy(), f), Event{ID: eventID, EventType: "card.assignment_changed", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID})
		assert.Equal(t, StateBlocked, v.State)
		assert.Equal(t, ReasonReadFailed, v.Reason)
		assert.Equal(t, DefaultReadAttempts, f.assignCalls)
	})

	t.Run("a recording in another bucket is discarded without retrying", func(t *testing.T) {
		f := newFakeReads()
		f.summaryErrs = []error{&basecamp.BucketMismatchError{BucketID: 1}}

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonBucketMismatch, v.Reason)
		assert.Equal(t, 1, f.summaryCalls)
	})

	t.Run("a canceled context is not a verdict", func(t *testing.T) {
		f := newFakeReads()
		f.summaryErrs = []error{errTransport, errTransport}
		ctx, cancel := context.WithCancel(context.Background())
		a, err := NewAdmitter(basePolicy(), f.reads(), WithSleep(func(context.Context, time.Duration) error {
			cancel()
			return ctx.Err()
		}))
		require.NoError(t, err)
		_, err = a.Decide(ctx, ev)
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestCompletions(t *testing.T) {
	completion := func(bucket, performer int64) Event {
		return Event{ID: eventID, EventType: "todo.completed", BucketID: bucket, RecordingID: recordingID, CreatorID: performer}
	}
	todo := func(f *fakeReads, bucket int64) *basecamp.RecordingSummary {
		s := summaryWith(recordingID, bucket, "Todo", strangerID, "<div>ship it</div>")
		f.summaries[recordingID] = s
		return s
	}

	t.Run("by someone outside the trust set: discarded at the gate", func(t *testing.T) {
		f := newFakeReads()
		todo(f, watchedProj).Assignees = []basecamp.Person{{ID: agentID}}

		v := decide(t, newAdmitter(t, basePolicy(), f), completion(watchedProj, strangerID))
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("the agent has no stake: discarded", func(t *testing.T) {
		f := newFakeReads()
		todo(f, routedProj)

		v := decide(t, newAdmitter(t, basePolicy(), f), completion(routedProj, operatorID))
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonNotAddressed, v.Reason)
		assert.Equal(t, []int64{recordingID}, f.subCalls, "the subscription asked is the completed recording's own")
	})

	t.Run("assigned to the agent: admitted with no subscription read", func(t *testing.T) {
		f := newFakeReads()
		todo(f, routedProj).Assignees = []basecamp.Person{{ID: strangerID}, {ID: agentID}}

		v := decide(t, newAdmitter(t, basePolicy(), f), completion(routedProj, operatorID))
		assert.Equal(t, StateAdmitted, v.State)
		assert.Equal(t, TriggerCompleted, v.Trigger)
		assert.Empty(t, f.subCalls)
	})

	t.Run("subscribed: admitted", func(t *testing.T) {
		f := newFakeReads()
		todo(f, routedProj)
		f.subscriptions[recordingID] = true

		v := decide(t, newAdmitter(t, basePolicy(), f), completion(routedProj, operatorID))
		assert.Equal(t, StateAdmitted, v.State)
		assert.Equal(t, TriggerCompleted, v.Trigger)
	})

	t.Run("in a watch_completions project the agent is not otherwise on: admitted, no stake read", func(t *testing.T) {
		f := newFakeReads()
		todo(f, watchedProj)

		v := decide(t, newAdmitter(t, basePolicy(), f), Event{ID: eventID, EventType: "card.completed", BucketID: watchedProj, RecordingID: recordingID, CreatorID: operatorID})
		assert.Equal(t, StateAdmitted, v.State)
		assert.Equal(t, TriggerCompleted, v.Trigger)
		assert.False(t, v.Acknowledge, "a completion is not a request: no acknowledgement, no guard")
		assert.Equal(t, &ReplyDestination{Kind: ReplyComment, RecordingID: recordingID}, v.Reply)
		assert.Equal(t, "/work/board", v.Route)
		assert.Empty(t, f.subCalls)
		assert.Equal(t, 1, f.summaryCalls)
	})

	t.Run("in a project with no route: discarded before any read", func(t *testing.T) {
		f := newFakeReads()
		todo(f, unmapped).Assignees = []basecamp.Person{{ID: agentID}}

		v := decide(t, newAdmitter(t, basePolicy(), f), completion(unmapped, operatorID))
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonNoRoute, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("by an allowlisted person: trusted like any other trigger, not operator-only", func(t *testing.T) {
		f := newFakeReads()
		todo(f, watchedProj)
		p := basePolicy()
		p.Trust = Trust{Mode: TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{allowedID}}

		v := decide(t, newAdmitter(t, p, f), completion(watchedProj, allowedID))
		assert.Equal(t, StateAdmitted, v.State)
	})
}

func TestTheAgentNeverAuthorizesItself(t *testing.T) {
	p := basePolicy()
	p.Trust = Trust{Mode: TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{allowedID}}

	for _, tc := range []struct {
		name   string
		ev     Event
		reason Reason
	}{
		{"created by the agent", Event{CreatorID: agentID}, ReasonAgentAuthored},
		{"created by the agent, performed by another agent", Event{CreatorID: agentID, PerformedByID: ptr(otherAgent)}, ReasonAgentAuthored},
		{"performed by the agent for the operator", Event{CreatorID: operatorID, PerformedByID: ptr(agentID)}, ReasonAgentAuthored},
		{"an agent actor on the push lane", Event{CreatorID: operatorID, ActorType: ActorTypeAgent}, ReasonAgentAuthored},
		{"another agent acting for the operator", Event{CreatorID: operatorID, PerformedByID: ptr(otherAgent)}, ReasonDelegated},
	} {
		for _, eventType := range []string{"comment.created", "card.assignment_changed", "card.completed", "chat.line.created"} {
			t.Run(tc.name+"/"+eventType, func(t *testing.T) {
				f := newFakeReads()
				s := summaryWith(recordingID, watchedProj, "Kanban::Card", operatorID, mentionOf(t, agentID))
				s.Parent = &basecamp.Parent{ID: parentID}
				s.CampfireID = campfireID
				s.Assignees = []basecamp.Person{{ID: agentID}}
				f.summaries[recordingID] = s
				f.subscriptions[parentID] = true
				f.assignments[eventID] = []int64{agentID}

				ev := tc.ev
				ev.ID, ev.EventType, ev.BucketID, ev.RecordingID = eventID, eventType, watchedProj, recordingID
				v := decide(t, newAdmitter(t, p, f), ev)
				assert.Equal(t, StateDiscarded, v.State)
				assert.Equal(t, tc.reason, v.Reason)
				assert.Zero(t, f.totalReads())
			})
		}
	}

	t.Run("a policy allowlisting the agent does not validate", func(t *testing.T) {
		bad := basePolicy()
		bad.Trust = Trust{Mode: TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{allowedID, agentID}}
		_, err := NewAdmitter(bad, newFakeReads().reads())
		require.ErrorContains(t, err, "agent's own Person id is in the allowlist")
	})

	t.Run("a policy naming the agent as operator does not validate", func(t *testing.T) {
		bad := basePolicy()
		bad.Trust.OperatorID = agentID
		_, err := NewAdmitter(bad, newFakeReads().reads())
		require.Error(t, err)
	})

	t.Run("content the agent wrote, surfaced by a trusted person, is not an instruction", func(t *testing.T) {
		f := newFakeReads()
		f.summaries[recordingID] = summaryWith(recordingID, routedProj, "Todo", agentID, mentionOf(t, agentID))

		v := decide(t, newAdmitter(t, basePolicy(), f), Event{ID: eventID, EventType: "todo.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID})
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonAgentAuthored, v.Reason)
	})
}

func TestContentAuthorMustBeTrusted(t *testing.T) {
	// A to-do moved in from another project is served as todo.created with the
	// mover as creator. The instruction in it is the original author's.
	ev := Event{ID: eventID, EventType: "todo.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID}

	t.Run("an untrusted author", func(t *testing.T) {
		f := newFakeReads()
		f.summaries[recordingID] = summaryWith(recordingID, routedProj, "Todo", strangerID, mentionOf(t, agentID))

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedAuthor, v.Reason)
	})

	t.Run("an unknown author", func(t *testing.T) {
		f := newFakeReads()
		s := summaryWith(recordingID, routedProj, "Todo", strangerID, mentionOf(t, agentID))
		s.Creator = nil
		f.summaries[recordingID] = s

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedAuthor, v.Reason)
	})

	t.Run("an allowlisted author moved in by the operator", func(t *testing.T) {
		f := newFakeReads()
		f.summaries[recordingID] = summaryWith(recordingID, routedProj, "Todo", allowedID, mentionOf(t, agentID))
		p := basePolicy()
		p.Trust = Trust{Mode: TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{allowedID}}

		v := decide(t, newAdmitter(t, p, f), ev)
		assert.Equal(t, StateAdmitted, v.State)
	})

	t.Run("a subscription comment by an untrusted author", func(t *testing.T) {
		f := newFakeReads()
		s := summaryWith(recordingID, routedProj, "Comment", strangerID, "<div>go</div>")
		s.Parent = &basecamp.Parent{ID: parentID}
		f.summaries[recordingID] = s
		f.subscriptions[parentID] = true

		v := decide(t, newAdmitter(t, basePolicy(), f), Event{ID: eventID, EventType: "comment.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID})
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedAuthor, v.Reason)
	})
}

func TestProjectTrustMode(t *testing.T) {
	p := basePolicy()
	p.Trust = Trust{Mode: TrustProject, OperatorID: operatorID}
	comment := func(performer int64) Event {
		return Event{ID: eventID, EventType: "comment.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: performer}
	}
	setup := func(t *testing.T, author int64) *fakeReads {
		f := newFakeReads()
		s := summaryWith(recordingID, routedProj, "Comment", author, mentionOf(t, agentID))
		s.Parent = &basecamp.Parent{ID: parentID}
		f.summaries[recordingID] = s
		f.members[routedProj] = map[int64]bool{memberID: true}
		return f
	}

	t.Run("a non-client member is trusted after the membership read", func(t *testing.T) {
		f := setup(t, memberID)
		v := decide(t, newAdmitter(t, p, f), comment(memberID))
		assert.Equal(t, StateAdmitted, v.State)
		assert.Equal(t, 1, f.memberCalls)
	})

	t.Run("a client is not, and costs no recording read", func(t *testing.T) {
		f := setup(t, clientID)
		v := decide(t, newAdmitter(t, p, f), comment(clientID))
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
		assert.Zero(t, f.summaryCalls)
	})

	t.Run("the operator needs no membership read", func(t *testing.T) {
		f := setup(t, operatorID)
		v := decide(t, newAdmitter(t, p, f), comment(operatorID))
		assert.Equal(t, StateAdmitted, v.State)
		assert.Zero(t, f.memberCalls)
	})

	t.Run("project mode requires the membership read", func(t *testing.T) {
		reads := newFakeReads().reads()
		reads.Members = nil
		_, err := NewAdmitter(p, reads)
		require.Error(t, err)
	})
}

func TestSubscribedComments(t *testing.T) {
	comment := Event{ID: eventID, EventType: "comment.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID}
	setup := func(content string) *fakeReads {
		f := newFakeReads()
		s := summaryWith(recordingID, routedProj, "Comment", operatorID, content)
		s.Parent = &basecamp.Parent{ID: parentID}
		f.summaries[recordingID] = s
		return f
	}

	t.Run("a comment on a recording the agent follows", func(t *testing.T) {
		f := setup("<div>next step?</div>")
		f.subscriptions[parentID] = true

		v := decide(t, newAdmitter(t, basePolicy(), f), comment)
		assert.Equal(t, StateAdmitted, v.State)
		assert.Equal(t, TriggerSubscribed, v.Trigger)
		assert.False(t, v.Acknowledge)
		assert.Equal(t, []int64{parentID}, f.subCalls, "the subscription asked is the commented recording's")
		assert.Equal(t, &ReplyDestination{Kind: ReplyComment, RecordingID: parentID}, v.Reply)
		assert.Equal(t, "recording:9000", v.ConversationKey)
	})

	t.Run("a mention wins and needs no subscription read", func(t *testing.T) {
		f := setup(mentionOf(t, agentID))
		f.subscriptions[parentID] = true

		v := decide(t, newAdmitter(t, basePolicy(), f), comment)
		assert.Equal(t, TriggerMentioned, v.Trigger)
		assert.Empty(t, f.subCalls)
	})

	t.Run("not followed", func(t *testing.T) {
		f := setup("<div>chatter</div>")
		v := decide(t, newAdmitter(t, basePolicy(), f), comment)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonNotAddressed, v.Reason)
		assert.Nil(t, v.Snapshot, "a discarded record keeps no content")
		assert.Empty(t, v.Trigger)
	})

	t.Run("in an unmapped project only the mention is considered", func(t *testing.T) {
		f := setup("<div>chatter</div>")
		f.summaries[recordingID].Bucket = &basecamp.Bucket{ID: unmapped}
		f.subscriptions[parentID] = true
		ev := comment
		ev.BucketID = unmapped

		v := decide(t, newAdmitter(t, basePolicy(), f), ev)
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonNotAddressed, v.Reason)
		assert.Empty(t, f.subCalls)
	})
}

func TestNoRoute(t *testing.T) {
	t.Run("a mention in an unmapped project is blocked and keeps its snapshot", func(t *testing.T) {
		f := newFakeReads()
		f.summaries[recordingID] = summaryWith(recordingID, unmapped, "Kanban::Card", operatorID, mentionOf(t, agentID))

		v := decide(t, newAdmitter(t, basePolicy(), f), Event{ID: eventID, EventType: "card.created", BucketID: unmapped, RecordingID: recordingID, CreatorID: operatorID})
		assert.Equal(t, StateBlocked, v.State)
		assert.Equal(t, ReasonNoRoute, v.Reason)
		assert.False(t, v.Routed)
		assert.NotNil(t, v.Snapshot)
		assert.NotNil(t, v.Reply, "the holding reply needs a destination")
	})

	t.Run("an assignment in an unmapped project is blocked", func(t *testing.T) {
		f := newFakeReads()
		f.summaries[recordingID] = summaryWith(recordingID, unmapped, "Todo", strangerID, "<div>todo</div>")
		f.assignments[eventID] = []int64{agentID}

		v := decide(t, newAdmitter(t, basePolicy(), f), Event{ID: eventID, EventType: "todo.assignment_changed", BucketID: unmapped, RecordingID: recordingID, CreatorID: operatorID})
		assert.Equal(t, StateBlocked, v.State)
		assert.Equal(t, ReasonNoRoute, v.Reason)
	})
}

func TestScope(t *testing.T) {
	f := newFakeReads()
	p := basePolicy()
	p.Buckets = []int64{watchedProj}

	v := decide(t, newAdmitter(t, p, f), Event{ID: eventID, EventType: "card.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID})
	assert.Equal(t, StateDiscarded, v.State)
	assert.Equal(t, ReasonOutOfScope, v.Reason)
	assert.Zero(t, f.totalReads())
}

func TestInvalidPointer(t *testing.T) {
	f := newFakeReads()
	v := decide(t, newAdmitter(t, basePolicy(), f), Event{ID: eventID, EventType: "card.created", BucketID: routedProj, CreatorID: operatorID})
	assert.Equal(t, StateDiscarded, v.State)
	assert.Equal(t, ReasonInvalidPointer, v.Reason)
	assert.Zero(t, f.totalReads())
}

func TestMatrixIsACopy(t *testing.T) {
	m := V1Matrix()
	m["card.moved"] = []Rule{{Trigger: "queued"}}
	_, widened := V1Matrix()["card.moved"]
	assert.False(t, widened)
}

func TestAllowlistMode(t *testing.T) {
	p := basePolicy()
	p.Trust = Trust{Mode: TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{allowedID}}
	card := func(performer int64) Event {
		return Event{ID: eventID, EventType: "card.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: performer}
	}

	t.Run("an allowlisted person is trusted", func(t *testing.T) {
		f := newFakeReads()
		f.summaries[recordingID] = summaryWith(recordingID, routedProj, "Kanban::Card", allowedID, mentionOf(t, agentID))
		v := decide(t, newAdmitter(t, p, f), card(allowedID))
		assert.Equal(t, StateAdmitted, v.State)
		assert.Zero(t, f.memberCalls)
	})

	t.Run("anyone else is not, without a read", func(t *testing.T) {
		f := newFakeReads()
		f.summaries[recordingID] = summaryWith(recordingID, routedProj, "Kanban::Card", memberID, mentionOf(t, agentID))
		f.members[routedProj] = map[int64]bool{memberID: true}
		v := decide(t, newAdmitter(t, p, f), card(memberID))
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonUntrustedPerformer, v.Reason)
		assert.Zero(t, f.totalReads())
	})
}

func TestProjectModeContentAuthor(t *testing.T) {
	p := basePolicy()
	p.Trust = Trust{Mode: TrustProject, OperatorID: operatorID}
	movedIn := Event{ID: eventID, EventType: "todo.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID}

	for _, tc := range []struct {
		author int64
		state  State
		reason Reason
	}{
		{memberID, StateAdmitted, ""},
		{clientID, StateDiscarded, ReasonUntrustedAuthor},
	} {
		t.Run(strconv.FormatInt(tc.author, 10), func(t *testing.T) {
			f := newFakeReads()
			f.summaries[recordingID] = summaryWith(recordingID, routedProj, "Todo", tc.author, mentionOf(t, agentID))
			f.members[routedProj] = map[int64]bool{memberID: true}

			v := decide(t, newAdmitter(t, p, f), movedIn)
			assert.Equal(t, tc.state, v.State)
			assert.Equal(t, tc.reason, v.Reason)
			assert.Equal(t, 1, f.memberCalls, "the author's membership is read; the operator's is not")
		})
	}
}

func TestSubscribedNeverStandsInForARefusedMention(t *testing.T) {
	// With a matrix where subscription is the only rule, a comment that
	// mentions the agent is still not a subscription trigger.
	f := newFakeReads()
	s := summaryWith(recordingID, routedProj, "Comment", operatorID, mentionOf(t, agentID))
	s.Parent = &basecamp.Parent{ID: parentID}
	f.summaries[recordingID] = s
	f.subscriptions[parentID] = true

	a, err := NewAdmitter(basePolicy(), f.reads(), WithMatrix(Matrix{"comment.created": {ruleSubscribed}}))
	require.NoError(t, err)
	v := decide(t, a, Event{ID: eventID, EventType: "comment.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID})
	assert.Equal(t, StateDiscarded, v.State)
	assert.Equal(t, ReasonNotAddressed, v.Reason)
	assert.Empty(t, f.subCalls)
}

func TestACommentWithoutItsRecordingCannotBeAnswered(t *testing.T) {
	f := newFakeReads()
	f.summaries[recordingID] = summaryWith(recordingID, routedProj, "Comment", operatorID, mentionOf(t, agentID))

	v := decide(t, newAdmitter(t, basePolicy(), f), Event{ID: eventID, EventType: "comment.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID})
	assert.Equal(t, StateBlocked, v.State)
	assert.Equal(t, ReasonReadFailed, v.Reason)
}

type nilSummaries struct{ *fakeReads }

func (*nilSummaries) Summarize(context.Context, basecamp.RecordingRef) (*basecamp.RecordingSummary, error) {
	return nil, nil //nolint:nilnil // the shape under test
}

func TestAnEmptySummaryIsAFailedRead(t *testing.T) {
	f := &nilSummaries{fakeReads: newFakeReads()}
	reads := f.reads()
	reads.Summaries = f
	a, err := NewAdmitter(basePolicy(), reads, WithSleep(func(context.Context, time.Duration) error { return nil }))
	require.NoError(t, err)

	v := decide(t, a, Event{ID: eventID, EventType: "card.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID})
	assert.Equal(t, StateBlocked, v.State)
	assert.Equal(t, ReasonReadFailed, v.Reason)
}
