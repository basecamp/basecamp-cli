package admission

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCampfireWithoutMentions(t *testing.T) {
	for _, who := range []int64{operatorID, allowedID, memberID} {
		t.Run(strconv.FormatInt(who, 10), func(t *testing.T) {
			p := basePolicy()
			p.HearsPings = false
			p.Trust.Mode = TrustProject
			p.Trust.AllowlistIDs = []int64{allowedID}
			p.Projects[servedProj] = Project{ListenCampfires: []int64{campfireID}}
			f := newFakeReads()
			f.members[servedProj] = map[int64]bool{memberID: true}
			s := summaryWith(recordingID, servedProj, "Chat::Lines::RichText", who, "<div>Any news?</div>")
			s.CampfireID = campfireID
			f.summaries[recordingID] = s
			v := decide(t, newAdmitter(t, p, f), Event{ID: eventID, EventType: eventChatLineCreated, BucketID: servedProj, RecordingID: recordingID, CreatorID: who})
			require.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
			assert.Equal(t, TriggerCampfire, v.Trigger)
			assert.True(t, v.Acknowledge)
			assert.Equal(t, "campfire:"+strconv.FormatInt(campfireID, 10), v.ConversationKey)
			assert.Equal(t, campfireID, v.Reply.RecordingID)
			assert.Equal(t, "chat_line", string(v.Reply.Kind))
			assert.Equal(t, who == operatorID, v.Snapshot.Owner)
			assert.Equal(t, s.Content, v.Snapshot.Content)
			if who == memberID {
				assert.Equal(t, RoleParticipant, v.Role)
			} else {
				assert.Equal(t, RoleOperator, v.Role)
			}
		})
	}
}

func TestCampfireOptInBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*Policy, *fakeReads, *Event)
		state   State
		trigger Trigger
	}{
		{"default requires mentions", func(p *Policy, _ *fakeReads, _ *Event) { p.Projects[servedProj] = Project{} }, StateDiscarded, ""},
		{"another Campfire", func(_ *Policy, f *fakeReads, _ *Event) { f.summaries[recordingID].CampfireID++ }, StateDiscarded, ""},
		{"no Campfire", func(_ *Policy, f *fakeReads, _ *Event) { f.summaries[recordingID].CampfireID = 0 }, StateBlocked, ""},
		{"another project", func(_ *Policy, f *fakeReads, e *Event) {
			e.BucketID = watchedProj
			f.summaries[recordingID].Bucket.ID = watchedProj
		}, StateDiscarded, ""},
		{"untrusted person", func(_ *Policy, f *fakeReads, e *Event) {
			e.CreatorID = strangerID
			f.summaries[recordingID].Creator.ID = strangerID
		}, StateDiscarded, ""},
		{"untrusted author", func(_ *Policy, f *fakeReads, _ *Event) { f.summaries[recordingID].Creator.ID = strangerID }, StateDiscarded, ""},
		{"self authored", func(_ *Policy, _ *fakeReads, e *Event) { e.CreatorID = agentID }, StateDiscarded, ""},
		{"allowed agent needs mention", func(p *Policy, f *fakeReads, e *Event) {
			p.Trust.AgentIDs = []int64{otherAgent}
			e.CreatorID = otherAgent
			f.summaries[recordingID].Creator.ID = otherAgent
		}, StateDiscarded, ""},
		{"mention keeps its trigger", func(_ *Policy, f *fakeReads, _ *Event) {
			f.summaries[recordingID].MentionedPersonIDs = []int64{agentID}
		}, StateAdmitted, TriggerMentioned},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := basePolicy()
			p.Projects[servedProj] = Project{ListenCampfires: []int64{campfireID}}
			f := newFakeReads()
			s := summaryWith(recordingID, servedProj, "Chat::Lines::RichText", operatorID, "hello")
			s.CampfireID = campfireID
			f.summaries[recordingID] = s
			e := Event{ID: eventID, EventType: eventChatLineCreated, BucketID: servedProj, RecordingID: recordingID, CreatorID: operatorID}
			tc.change(&p, f, &e)
			v := decide(t, newAdmitter(t, p, f), e)
			assert.Equal(t, tc.state, v.State, "reason %q", v.Reason)
			assert.Equal(t, tc.trigger, v.Trigger)
		})
	}
}
