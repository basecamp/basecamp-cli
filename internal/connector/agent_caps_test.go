package connector

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// The agent loop caps, held by the real ledger: an allowed agent's mentions
// are counted from the rows admission wrote, on the ledger's own clock.

const capsPeerAgent int64 = 53309518

type capsFixture struct {
	ledger    *Ledger
	admitter  *admission.Admitter
	committer *admission.Committer
	reads     *adapterReads
	clock     time.Time
}

func newCapsFixture(t *testing.T, threadCap, dailyCap int) *capsFixture {
	t.Helper()
	f := &capsFixture{
		ledger: newTestLedger(t),
		reads:  &adapterReads{summaries: map[int64]*basecamp.RecordingSummary{}},
		clock:  time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC),
	}
	f.ledger.now = func() time.Time { return f.clock }
	admitter, err := admission.NewAdmitter(admission.Policy{
		AgentID: adapterAgentID,
		Trust: admission.Trust{
			Mode: admission.TrustOperator, OperatorID: adapterOperatorID,
			AgentIDs: []int64{capsPeerAgent}, AgentThreadCap: threadCap, AgentDailyCap: dailyCap,
		},
		Projects: map[int64]admission.Project{adapterBucketID: {}},
	}, admission.Reads{Summaries: f.reads, Subscriptions: f.reads, Assignments: f.reads})
	require.NoError(t, err)
	f.admitter = admitter
	f.committer = admission.NewCommitter(f.ledger.Admission())
	return f
}

// mention runs one comment by the peer agent, mentioning this one, on
// thread, through intake's ledger, admission and the committer.
func (f *capsFixture) mention(t *testing.T, id, thread int64) admission.Verdict {
	t.Helper()
	ctx := context.Background()
	recording := 20000 + id
	f.reads.summaries[recording] = &basecamp.RecordingSummary{
		ID: recording, Status: "active", Type: "Comment", AppURL: "https://app.basecamp.com/2914079/buckets/48699913/recordings/1",
		Bucket:             &basecamp.Bucket{ID: adapterBucketID},
		Creator:            &basecamp.Person{ID: capsPeerAgent, PersonableType: "Agent"},
		Parent:             &basecamp.Parent{ID: thread},
		MentionedPersonIDs: []int64{adapterAgentID},
		Content:            "<div>" + mentionMarkup(adapterAgentID) + "</div>",
	}
	_, err := f.ledger.RecordSeen(ctx, eventfeed.Event{
		ID: id, Kind: "comment_created", EventType: "comment.created", Action: "created",
		CreatedAt: f.clock, BucketID: adapterBucketID, CreatorID: capsPeerAgent, RecordingID: recording, ActorType: "agent",
	}, LaneLive)
	require.NoError(t, err)
	ev, ok, err := f.ledger.Admission().LoadUndecided(ctx, id)
	require.NoError(t, err)
	require.True(t, ok)
	v, err := f.admitter.Decide(ctx, ev)
	require.NoError(t, err)
	require.Equal(t, admission.StateAdmitted, v.State, "admission admits it; the cap is the committer's: %q", v.Reason)
	v, err = f.committer.Commit(ctx, v)
	require.NoError(t, err)
	return v
}

func TestTheLedgerCapsAnAgentsMentionsPerThread(t *testing.T) {
	f := newCapsFixture(t, 3, 0)
	const thread int64 = 7000
	for id := int64(1); id <= 3; id++ {
		v := f.mention(t, id, thread)
		assert.Contains(t, []admission.State{admission.StateAdmitted, admission.StateQueued}, v.State, "mention %d is within the cap", id)
	}
	n, err := f.ledger.Admission().CountAgentRequests(context.Background(), "recording:7000", []int64{capsPeerAgent}, admission.AgentCapWindow)
	require.NoError(t, err)
	require.Equal(t, 3, n, "three admitted mentions are on the ledger")

	v := f.mention(t, 4, thread)
	assert.Equal(t, admission.StateDiscarded, v.State)
	assert.Equal(t, admission.ReasonAgentThreadCap, v.Reason)
	record := getRecord(t, f.ledger, 4)
	assert.Equal(t, StateDiscarded, record.State)
	assert.Equal(t, "agent_thread_cap", record.Reason, "greppable in the ledger")

	// Another thread is not this one's business.
	assert.NotEqual(t, admission.StateDiscarded, f.mention(t, 5, thread+1).State)

	// A handed-off row still counts: moving on after admission is not
	// leaving the window.
	require.NoError(t, f.ledger.SetState(context.Background(), 1, StateDiscarded, "handed_off"))
	f.clock = f.clock.Add(23 * time.Hour)
	assert.Equal(t, admission.ReasonAgentThreadCap, f.mention(t, 6, thread).Reason, "still inside 24 hours")

	f.clock = f.clock.Add(time.Hour + time.Second)
	assert.NotEqual(t, admission.StateDiscarded, f.mention(t, 7, thread).State, "the window has passed")
}

func TestTheLedgerCapsOneAgentAcrossThreads(t *testing.T) {
	f := newCapsFixture(t, 0, 2)
	assert.NotEqual(t, admission.StateDiscarded, f.mention(t, 1, 7001).State)
	assert.NotEqual(t, admission.StateDiscarded, f.mention(t, 2, 7002).State)
	v := f.mention(t, 3, 7003)
	assert.Equal(t, admission.StateDiscarded, v.State)
	assert.Equal(t, admission.ReasonAgentDailyCap, v.Reason)
}

func TestCountAgentRequestsCountsOnlyAdmittedAgentMentions(t *testing.T) {
	ledger := newTestLedger(t)
	store := ledger.Admission()
	ctx := context.Background()

	// A person's admitted mention on the same thread is not an agent's.
	seenRecord(t, ledger, 1)
	_, err := store.Commit(ctx, admittedVerdict(1, 0, "recording:1"))
	require.NoError(t, err)
	// A blocked record was never admitted.
	seenRecord(t, ledger, 2)
	blocked := blockedVerdict(2, 0, admission.ReasonReadFailed)
	blocked.RequesterID = capsPeerAgent
	_, err = store.Commit(ctx, blocked)
	require.NoError(t, err)

	n, err := store.CountAgentRequests(ctx, "recording:1", []int64{capsPeerAgent}, admission.AgentCapWindow)
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = store.CountAgentRequests(ctx, "", nil, admission.AgentCapWindow)
	require.NoError(t, err)
	assert.Zero(t, n, "no agents, nothing to count")

	seenRecord(t, ledger, 3)
	agent := admittedVerdict(3, 0, "recording:1")
	agent.RequesterID = capsPeerAgent
	_, err = store.Commit(ctx, agent)
	require.NoError(t, err)
	n, err = store.CountAgentRequests(ctx, "recording:1", []int64{capsPeerAgent}, admission.AgentCapWindow)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

// Mentions a restart caught up on and closed unread were never answered, so
// they are no round of a loop and leave the caps alone.
func TestMentionsClosedUnreadDoNotCount(t *testing.T) {
	f := newCapsFixture(t, 2, 0)
	const thread int64 = 7100
	for id := int64(1); id <= 2; id++ {
		require.NotEqual(t, admission.StateDiscarded, f.mention(t, id, thread).State)
	}
	ctx := context.Background()
	require.NoError(t, f.ledger.SetState(ctx, 1, StateDiscarded, ReasonBeforeThisRun))
	require.NoError(t, f.ledger.SetState(ctx, 2, StateDiscarded, ReasonUnreadable))
	v := f.mention(t, 3, thread)
	assert.NotEqual(t, admission.StateDiscarded, v.State, "neither closed-unread mention counts")
	require.NoError(t, f.ledger.SetState(ctx, 3, StateDiscarded, ReasonHandedOff))
	assert.NotEqual(t, admission.StateDiscarded, f.mention(t, 4, thread).State)
	assert.Equal(t, admission.ReasonAgentThreadCap, f.mention(t, 5, thread).Reason, "handed off ones still do")
}
