package admission

import (
	"slices"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only one connector should answer an agent's Pings, so only one started to
// hear them does (Policy.HearsPings). Every other one hears its projects
// alone: a line in a Ping is discarded once the Ping read says the bucket is
// one, whoever wrote it, and nothing past that read is spent on it.
func TestAConnectorThatDoesNotHearPingsDiscardsEveryPingLine(t *testing.T) {
	cases := map[string]func(t *testing.T, f *fakeReads) (Policy, Event){
		"the operator's line in their direct Ping": func(_ *testing.T, f *fakeReads) (Policy, Event) {
			return basePolicy(), inPing(f, []int64{operatorID}, operatorID, "<div>what's on my plate?</div>")
		},
		"the operator's mention in a group Ping": func(t *testing.T, f *fakeReads) (Policy, Event) {
			return basePolicy(), inPing(f, []int64{operatorID, memberID}, operatorID, "<div>"+mentionOf(t, agentID)+" summarize this</div>")
		},
		"a person let into that one Ping": func(t *testing.T, f *fakeReads) (Policy, Event) {
			return guestPolicy(basePolicy()), inPing(f, []int64{operatorID, pingGuest}, pingGuest, "<div>"+mentionOf(t, agentID)+" what changed?</div>")
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeReads()
			p, ev := build(t, f)
			require.True(t, p.HearsPings, "the fixtures' connector hears Pings")
			heard := decide(t, newAdmitter(t, p, f), ev)
			require.Equal(t, StateAdmitted, heard.State, "reason %q: a connector that hears Pings takes this request", heard.Reason)

			f = newFakeReads()
			p, ev = build(t, f)
			p.HearsPings = false
			v := decide(t, newAdmitter(t, p, f), ev)
			assert.Equal(t, StateDiscarded, v.State)
			assert.Equal(t, ReasonPingsNotHeard, v.Reason)
			assert.Nil(t, v.Snapshot, "a discard carries no content")
			assert.Empty(t, v.ConversationKey)
			assert.Equal(t, 1, f.pingCalls, "the Ping read is what says the line is a Ping's")
			assert.Zero(t, f.summaryCalls, "and nothing past it is read")
		})
	}
}

// Not hearing Pings takes nothing away from a connector's projects: a line in
// a served project's Campfire, and a mention in a project it doesn't serve,
// are decided as they always were.
func TestAConnectorThatDoesNotHearPingsStillHearsItsProjects(t *testing.T) {
	noPings := basePolicy()
	noPings.HearsPings = false

	t.Run("a mention in a served project's Campfire", func(t *testing.T) {
		f := newFakeReads()
		s := summaryWith(recordingID, servedProj, "Chat::Lines::RichText", operatorID, "<div>"+mentionOf(t, agentID)+" ship it</div>")
		s.CampfireID = campfireID
		f.summaries[recordingID] = s

		v := decide(t, newAdmitter(t, noPings, f), Event{ID: eventID, EventType: "chat.line.created", BucketID: servedProj, RecordingID: recordingID, CreatorID: operatorID})
		assert.Equal(t, StateAdmitted, v.State, "reason %q", v.Reason)
		assert.True(t, v.Served)
		assert.Zero(t, f.pingCalls, "a served project's line is never asked about")
	})

	t.Run("a mention in an unserved project's Campfire", func(t *testing.T) {
		f := newFakeReads()
		s := summaryWith(recordingID, unservedProj, "Chat::Lines::RichText", operatorID, mentionOf(t, agentID))
		s.CampfireID = campfireID
		f.summaries[recordingID] = s

		v := decide(t, newAdmitter(t, noPings, f), Event{ID: eventID, EventType: "chat.line.created", BucketID: unservedProj, RecordingID: recordingID, CreatorID: operatorID})
		assert.Equal(t, StateBlocked, v.State, "a project's, not a Ping's: not this flag's to drop")
		assert.Equal(t, ReasonNoRoute, v.Reason)
	})
}

// Outside a served project, the Ping flag decides everything: a connector
// that hears Pings admits nothing there but a Ping's line, and one that does
// not admits nothing there at all. Every type in the matrix is tried, in a
// Ping and in a project connect.json does not serve, as the operator's own
// words mentioning the agent — the strongest request there is.
func TestNothingOutsideAServedProjectIsAdmittedWithoutPings(t *testing.T) {
	admittedPing := false
	for _, eventType := range slices.Sorted(func(yield func(string) bool) {
		for k := range V1Matrix() {
			if !yield(k) {
				return
			}
		}
	}) {
		for _, bucket := range []int64{pingID, unservedProj} {
			for _, hears := range []bool{true, false} {
				f := newFakeReads()
				ev := outsideAProject(t, f, eventType, bucket)
				p := basePolicy()
				p.HearsPings = hears
				v := decide(t, newAdmitter(t, p, f), ev)
				if v.State != StateAdmitted {
					continue
				}
				assert.True(t, hears, "%s in bucket %d was admitted by a connector that hears no Pings", eventType, bucket)
				assert.True(t, IsPingConversation(v.ConversationKey, pingID), "%s in bucket %d was admitted outside a Ping", eventType, bucket)
				admittedPing = true
			}
		}
	}
	assert.True(t, admittedPing, "the fixtures admit a Ping's line when Pings are heard, or this test sees nothing")
}

// outsideAProject is the operator's event of eventType in bucket, which
// connect.json does not serve, on a recording the operator wrote that
// mentions the agent. A bucket that is pingID is a direct Ping with the
// operator.
func outsideAProject(t *testing.T, f *fakeReads, eventType string, bucket int64) Event {
	t.Helper()
	if bucket == pingID {
		f.pings[pingID] = &basecamp.Ping{ID: pingID, People: []basecamp.Person{{ID: operatorID}}}
	}
	content := "<div>" + mentionOf(t, agentID) + " please look</div>"
	s := summaryWith(recordingID, bucket, "Comment", operatorID, content)
	s.Parent = &basecamp.Parent{ID: recordingID + 1}
	s.CampfireID = campfireID
	if eventType == "chat.line.created" {
		s.Type = "Chat::Lines::RichText"
	}
	f.summaries[recordingID] = s
	f.subscriptions[recordingID+1] = true
	f.assignments[eventID] = []int64{agentID}
	return Event{ID: eventID, EventType: eventType, BucketID: bucket, RecordingID: recordingID, CreatorID: operatorID}
}
