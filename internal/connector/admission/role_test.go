package admission

import (
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Opening a project to its members is how a colleague's question reaches the
// agent at all, but a project's membership is not a list of people whose word
// should authorize a merge or a deploy. Every admitted request says which it
// is: role operator for the operator and anyone named with --allow, role
// participant for someone admitted only as a member of the project.
func TestOperatorsAndParticipants(t *testing.T) {
	projectWithOperators := func() Policy {
		p := basePolicy()
		p.Trust = Trust{Mode: TrustProject, OperatorID: operatorID, AllowlistIDs: []int64{allowedID}}
		return p
	}
	mention := func(t *testing.T, f *fakeReads, recordingType string, author int64) {
		t.Helper()
		s := summaryWith(recordingID, servedProj, recordingType, author, mentionOf(t, agentID))
		s.Parent = &basecamp.Parent{ID: parentID}
		f.summaries[recordingID] = s
	}
	event := func(eventType string, performer int64) Event {
		return Event{ID: eventID, EventType: eventType, BucketID: servedProj, RecordingID: recordingID, CreatorID: performer}
	}

	t.Run("named operators combine with project trust", func(t *testing.T) {
		require.NoError(t, projectWithOperators().Validate())

		p := basePolicy()
		p.Trust.AllowlistIDs = []int64{allowedID}
		assert.Error(t, p.Validate(), "operator mode names nobody but the operator")
	})

	for name, tc := range map[string]struct {
		policy    func() Policy
		performer int64
	}{
		"the operator": {basePolicy, operatorID},
		"someone named, in allowlist mode": {func() Policy {
			p := basePolicy()
			p.Trust = Trust{Mode: TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{allowedID}}
			return p
		}, allowedID},
		"someone named, alongside the project": {projectWithOperators, allowedID},
	} {
		t.Run(name+" is an operator", func(t *testing.T) {
			f := newFakeReads()
			mention(t, f, "Comment", tc.performer)
			v := decide(t, newAdmitter(t, tc.policy(), f), event("comment.created", tc.performer))
			require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
			assert.Equal(t, RoleOperator, v.Role)
			assert.Equal(t, RoleOperator, v.Snapshot.Role)
			assert.Zero(t, f.memberCalls, "an operator's standing needs no membership read")
		})
	}

	t.Run("a member who mentions the agent is a participant", func(t *testing.T) {
		f := newFakeReads()
		mention(t, f, "Comment", memberID)
		f.members[servedProj] = map[int64]bool{memberID: true}
		v := decide(t, newAdmitter(t, projectWithOperators(), f), event("comment.created", memberID))
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, TriggerMentioned, v.Trigger)
		assert.Equal(t, RoleParticipant, v.Role)
		assert.Equal(t, RoleParticipant, v.Snapshot.Role)
	})

	t.Run("a member who comments on a thread the agent follows is a participant", func(t *testing.T) {
		f := newFakeReads()
		s := summaryWith(recordingID, servedProj, "Comment", memberID, "<div>any news?</div>")
		s.Parent = &basecamp.Parent{ID: parentID}
		f.summaries[recordingID] = s
		f.subscriptions[parentID] = true
		f.members[servedProj] = map[int64]bool{memberID: true}
		v := decide(t, newAdmitter(t, projectWithOperators(), f), event("comment.created", memberID))
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, TriggerSubscribed, v.Trigger)
		assert.Equal(t, RoleParticipant, v.Role)
	})

	// The words are what the agent acts on, so whoever wrote them settles
	// the role: the operator moving in a member's to-do does not lend it the
	// operator's standing.
	t.Run("the operator bringing in a member's words is a participant's request", func(t *testing.T) {
		f := newFakeReads()
		mention(t, f, "Todo", memberID)
		f.members[servedProj] = map[int64]bool{memberID: true}
		v := decide(t, newAdmitter(t, projectWithOperators(), f), event("todo.created", operatorID))
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, RoleParticipant, v.Role)
	})

	// And the other way round: a member re-filing the operator's words is
	// still a member's act.
	t.Run("a member bringing in the operator's words is a participant's request", func(t *testing.T) {
		f := newFakeReads()
		mention(t, f, "Todo", operatorID)
		f.members[servedProj] = map[int64]bool{memberID: true}
		v := decide(t, newAdmitter(t, projectWithOperators(), f), event("todo.created", memberID))
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, RoleParticipant, v.Role)
	})

	// A member's completion is context, as a followed-thread comment is: it
	// reaches the agent marked as a participant's, and asks for no
	// acknowledgement. What it may lead to is the session's policy.
	t.Run("a member's completion is a participant's context", func(t *testing.T) {
		f := newFakeReads()
		todo := summaryWith(recordingID, watchedProj, "Todo", strangerID, "<div>ship it</div>")
		todo.Assignees = []basecamp.Person{{ID: agentID}}
		f.summaries[recordingID] = todo
		f.members[watchedProj] = map[int64]bool{memberID: true}
		v := decide(t, newAdmitter(t, projectWithOperators(), f),
			Event{ID: eventID, EventType: "todo.completed", BucketID: watchedProj, RecordingID: recordingID, CreatorID: memberID})
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, TriggerCompleted, v.Trigger)
		assert.Equal(t, RoleParticipant, v.Role)
		assert.False(t, v.Acknowledge, "context, not a request")
		assert.Equal(t, 1, f.memberCalls, "a member only once membership confirms them")
	})

	t.Run("a named operator's completion is admitted", func(t *testing.T) {
		f := newFakeReads()
		f.summaries[recordingID] = summaryWith(recordingID, watchedProj, "Todo", strangerID, "<div>ship it</div>")
		v := decide(t, newAdmitter(t, projectWithOperators(), f),
			Event{ID: eventID, EventType: "todo.completed", BucketID: watchedProj, RecordingID: recordingID, CreatorID: allowedID})
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, RoleOperator, v.Role)
	})

	t.Run("a member's assignment stays the operator's alone", func(t *testing.T) {
		f := newFakeReads()
		v := decide(t, newAdmitter(t, projectWithOperators(), f), event("card.assignment_changed", memberID))
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonAssignmentNotOperator, v.Reason)
		assert.Zero(t, f.totalReads())
	})

	t.Run("and so does a named operator's", func(t *testing.T) {
		f := newFakeReads()
		v := decide(t, newAdmitter(t, projectWithOperators(), f), event("card.assignment_changed", allowedID))
		assert.Equal(t, StateDiscarded, v.State)
		assert.Equal(t, ReasonAssignmentNotOperator, v.Reason)
	})
}
