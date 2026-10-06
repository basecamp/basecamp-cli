package admission

import (
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allow_assignments lets the people the allowlist names assign the agent work,
// as the operator does. The assigner is the feed's performer, which Basecamp
// writes and no payload carries, so naming someone here trusts them, not text
// that claims to be them. Nobody it doesn't name gains anything.
func TestNamedOperatorsMayAssignWhenOptedIn(t *testing.T) {
	optedIn := func() Policy {
		p := basePolicy()
		p.Trust = Trust{Mode: TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{allowedID}, AllowAssignments: true}
		return p
	}
	assignment := func(performer int64) Event {
		return Event{ID: eventID, EventType: "card.assignment_changed", BucketID: servedProj, RecordingID: recordingID, CreatorID: performer}
	}
	card := func(f *fakeReads) {
		f.summaries[recordingID] = summaryWith(recordingID, servedProj, "Kanban::Card", strangerID, "<div>a card</div>")
		f.summaries[recordingID].Assignees = []basecamp.Person{{ID: agentID}}
		f.assignments[eventID] = []int64{agentID}
	}

	t.Run("a named operator assigning the agent is admitted", func(t *testing.T) {
		f := newFakeReads()
		card(f)
		v := decide(t, newAdmitter(t, optedIn(), f), assignment(allowedID))
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, TriggerAssigned, v.Trigger)
		assert.Equal(t, allowedID, v.RequesterID)
		assert.Equal(t, 1, f.assignCalls, "and only once the events show this assignment added the agent")
	})

	t.Run("so is one named beside project trust, and the members still are not", func(t *testing.T) {
		p := optedIn()
		p.Trust.Mode = TrustProject
		f := newFakeReads()
		card(f)
		f.members[servedProj] = map[int64]bool{memberID: true}
		v := decide(t, newAdmitter(t, p, f), assignment(allowedID))
		require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.Equal(t, RoleOperator, v.Role)

		f = newFakeReads()
		card(f)
		f.members[servedProj] = map[int64]bool{memberID: true}
		v = decide(t, newAdmitter(t, p, f), assignment(memberID))
		assert.Equal(t, ReasonAssignmentNotOperator, v.Reason)
	})

	t.Run("anyone else is still refused at the gate", func(t *testing.T) {
		for _, performer := range []int64{strangerID, memberID} {
			f := newFakeReads()
			card(f)
			v := decide(t, newAdmitter(t, optedIn(), f), assignment(performer))
			assert.Equal(t, StateDiscarded, v.State)
			assert.Equal(t, ReasonAssignmentNotOperator, v.Reason)
			assert.Zero(t, f.totalReads())
		}
	})

	t.Run("without the opt-in, a named operator's assignment is refused", func(t *testing.T) {
		f := newFakeReads()
		card(f)
		p := optedIn()
		p.Trust.AllowAssignments = false
		v := decide(t, newAdmitter(t, p, f), assignment(allowedID))
		assert.Equal(t, ReasonAssignmentNotOperator, v.Reason)
	})

	t.Run("an opt-in that names nobody is refused", func(t *testing.T) {
		p := basePolicy()
		p.Trust.AllowAssignments = true
		assert.Error(t, p.Validate())
	})
}
