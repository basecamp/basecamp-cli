package connector

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

const otherPersonID int64 = 1001

// dispatchFixture is a task holding an originating mention (event 1) and a
// follow-up queued on the same conversation (event 2), both still admitted on
// the task.
type dispatchFixture struct {
	ledger *Ledger
	grant  TaskGrant
	d      *TaskDispatch
}

func newDispatchFixture(t *testing.T) dispatchFixture {
	t.Helper()
	ledger := newTestLedger(t)
	ctx := context.Background()
	for _, id := range []int64{1, 2} {
		seenRecord(t, ledger, id)
		v := admittedVerdict(id, 0, "recording:10304028989")
		v.Snapshot.Content = "<div>" + mentionMarkup(adapterAgentID) + " please ask " + mentionMarkup(otherPersonID) + " about it</div>"
		_, err := ledger.Admission().Commit(ctx, v)
		require.NoError(t, err)
	}
	require.Equal(t, StateQueued, getRecord(t, ledger, 2).State)
	grant, err := ledger.CreateTask(ctx, []int64{1, 2})
	require.NoError(t, err)
	d, err := ledger.Dispatch(grant.Token, adapterAgentID)
	require.NoError(t, err)
	return dispatchFixture{ledger: ledger, grant: grant, d: d}
}

type taskEventRow struct {
	Delivery    string
	Guard       string
	ExposedAt   *string
	DeliveredAt *string
	CompletedAt *string
}

func (f dispatchFixture) row(t *testing.T, eventID int64) taskEventRow {
	t.Helper()
	return f.rowContext(context.Background(), t, eventID)
}

func (f dispatchFixture) rowContext(ctx context.Context, t *testing.T, eventID int64) taskEventRow {
	t.Helper()
	var r taskEventRow
	require.NoError(t, f.ledger.db.QueryRowContext(ctx, `SELECT delivery, guard, exposed_at, delivered_at, completed_at FROM task_events WHERE task_id = ? AND event_id = ?`,
		f.grant.ID, eventID).Scan(&r.Delivery, &r.Guard, &r.ExposedAt, &r.DeliveredAt, &r.CompletedAt))
	return r
}

// Done when: get_dispatch writes exposed on first call and nothing on repeats.
func TestGetDispatchExposesOnceAndRepeatsWriteNothing(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	f.ledger.now = func() time.Time { return t0 }

	first, ok, err := f.d.Get(ctx, 2)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, DeliveryExposed, first.Delivery)
	row := f.row(t, 2)
	assert.Equal(t, "exposed", row.Delivery)
	require.NotNil(t, row.ExposedAt)
	record := getRecord(t, f.ledger, 2)
	assert.Equal(t, StateDispatched, record.State, "a follow-up handed to a worker is dispatched")

	f.ledger.now = func() time.Time { return t0.Add(time.Minute) }
	again, ok, err := f.d.Get(ctx, 2)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, first, again, "a repeat returns the same instruction")
	assert.Equal(t, row, f.row(t, 2), "and marks nothing further")
	assert.Equal(t, record.Revision, getRecord(t, f.ledger, 2).Revision)
	assert.Equal(t, StateAdmitted, getRecord(t, f.ledger, 1).State, "the other event is untouched")
}

func TestGetDispatchWithoutAnIDIsTheEarliestNotAcknowledged(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()

	got, ok, err := f.d.Get(ctx, 0)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, int64(1), got.EventID)

	// Exposed but not acknowledged is still the earliest.
	got, _, err = f.d.Get(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.EventID)

	_, err = f.d.Ack(ctx, 1, nil)
	require.NoError(t, err)
	got, ok, err = f.d.Get(ctx, 0)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, int64(2), got.EventID)

	_, err = f.d.Complete(ctx, 2, Completion{Outcome: OutcomeSucceeded})
	require.NoError(t, err)
	_, ok, err = f.d.Get(ctx, 0)
	require.NoError(t, err)
	assert.False(t, ok, "nothing is left to acknowledge")
}

// The instruction is an allowlist: the fields a worker needs, the agent's own
// mention stripped, and nothing that is a route, a position or a token.
func TestGetDispatchHandsOutOnlyTheAllowlist(t *testing.T) {
	f := newDispatchFixture(t)

	got, ok, err := f.d.Get(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, ok)

	assert.Equal(t, Instruction{
		EventID: 1, EventType: "comment.created", Trigger: "mentioned", Class: "internal",
		Recording: InstructionRecording{
			BucketID: adapterBucketID, RecordingID: 10304028972, Type: "Comment", Title: "A comment",
			URL: "https://app.basecamp.com/2914079/buckets/48699913/recordings/10304028972",
		},
		ReplyTo:          InstructionReply{Kind: "comment", RecordingID: 10304028989},
		RequesterID:      adapterOperatorID,
		Acknowledge:      true,
		Delivery:         DeliveryExposed,
		Content:          "<div> please ask " + mentionMarkup(otherPersonID) + " about it</div>",
		ContentUpdatedAt: time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC),
	}, got)

	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(encoded, &fields))
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{"acknowledge", "class", "content", "content_updated_at", "delivery", "event_id", "event_type",
		"guard_acknowledged", "recording", "reply_to", "requester_id", "trigger"}, keys)
	assert.NotContains(t, string(encoded), "/work/connector", "no route")
	assert.NotContains(t, string(encoded), f.grant.Token, "no token")
}

func TestGetDispatchCancelsTheGuardAndReportsAFiredOne(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	require.Equal(t, "armed", f.row(t, 1).Guard, "an acknowledged trigger arms the guard")

	got, _, err := f.d.Get(ctx, 1)
	require.NoError(t, err)
	assert.False(t, got.GuardAcknowledged)
	assert.Equal(t, "canceled", f.row(t, 1).Guard)

	// The connector fired the guard on event 2 before the worker asked.
	_, err = f.ledger.db.ExecContext(context.Background(), `UPDATE task_events SET guard = 'fired' WHERE task_id = ? AND event_id = 2`, f.grant.ID)
	require.NoError(t, err)
	got, _, err = f.d.Get(ctx, 2)
	require.NoError(t, err)
	assert.True(t, got.GuardAcknowledged)
	assert.Equal(t, "fired", f.row(t, 2).Guard, "a fired guard stays fired")
}

func TestGetDispatchCancelsAnArmedGuardOnAnAlreadyExposedEvent(t *testing.T) {
	f := newDispatchFixture(t)
	// Exposed at launch by the dispatcher, guard still armed.
	require.NoError(t, f.ledger.SetState(context.Background(), 1, StateDispatched, ""))
	_, err := f.ledger.db.ExecContext(context.Background(), `UPDATE task_events SET delivery = 'exposed' WHERE event_id = 1`)
	require.NoError(t, err)

	_, _, err = f.d.Get(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, "canceled", f.row(t, 1).Guard)
}

// Done when: ack_dispatch and complete_dispatch move the delivery state.
func TestAckAndCompleteMoveTheDelivery(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	_, _, err := f.d.Get(ctx, 1)
	require.NoError(t, err)

	ackID := int64(9001)
	receipt, err := f.d.Ack(ctx, 1, &ackID)
	require.NoError(t, err)
	assert.Equal(t, Receipt{EventID: 1, Delivery: DeliveryDelivered, AckID: &ackID, Links: []string{}}, receipt)
	delivered := f.row(t, 1)
	assert.Equal(t, "delivered", delivered.Delivery)
	require.NotNil(t, delivered.DeliveredAt)

	// A lost tool response, retried.
	again, err := f.d.Ack(ctx, 1, &ackID)
	require.NoError(t, err)
	assert.Equal(t, receipt, again)
	assert.Equal(t, delivered, f.row(t, 1))

	replyID := int64(9002)
	done, err := f.d.Complete(ctx, 1, Completion{Outcome: OutcomeSucceeded, Links: []string{"https://github.com/basecamp/basecamp-cli/pull/1"}, ReplyID: &replyID})
	require.NoError(t, err)
	assert.Equal(t, DeliveryCompleted, done.Delivery)
	assert.Equal(t, OutcomeSucceeded, done.Outcome)
	assert.Equal(t, &replyID, done.ReplyID)
	assert.Equal(t, &ackID, done.AckID)
	assert.Equal(t, StateCompleted, getRecord(t, f.ledger, 1).State)
	completed := f.row(t, 1)
	assert.Equal(t, delivered.DeliveredAt, completed.DeliveredAt)

	repeat, err := f.d.Complete(ctx, 1, Completion{Outcome: OutcomeSucceeded, Links: []string{"https://github.com/basecamp/basecamp-cli/pull/1"}, ReplyID: &replyID})
	require.NoError(t, err)
	assert.Equal(t, done, repeat)
	assert.Equal(t, completed, f.row(t, 1))
}

func TestCompleteAlsoAcknowledges(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	_, _, err := f.d.Get(ctx, 2)
	require.NoError(t, err)

	receipt, err := f.d.Complete(ctx, 2, Completion{Outcome: OutcomeFailed})
	require.NoError(t, err)
	assert.Equal(t, DeliveryCompleted, receipt.Delivery)
	row := f.row(t, 2)
	assert.NotNil(t, row.DeliveredAt)
	assert.NotNil(t, row.CompletedAt)
}

func TestAReportedOutcomeStands(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	_, _, err := f.d.Get(ctx, 1)
	require.NoError(t, err)
	first := int64(1)
	_, err = f.d.Ack(ctx, 1, &first)
	require.NoError(t, err)
	second := int64(2)
	_, err = f.d.Ack(ctx, 1, &second)
	assert.ErrorIs(t, err, ErrReportConflict)

	_, err = f.d.Complete(ctx, 1, Completion{Outcome: OutcomeSucceeded})
	require.NoError(t, err)
	for _, c := range []Completion{
		{Outcome: OutcomeFailed},
		{Outcome: OutcomeSucceeded, Links: []string{"https://example.com/a"}},
		{Outcome: OutcomeSucceeded, ReplyID: &second},
	} {
		_, err = f.d.Complete(ctx, 1, c)
		assert.ErrorIs(t, err, ErrReportConflict)
	}
}

func TestAReportNeedsTheEventHandedOut(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()

	_, err := f.d.Ack(ctx, 2, nil)
	assert.ErrorIs(t, err, ErrNotExposed)
	_, err = f.d.Complete(ctx, 2, Completion{Outcome: OutcomeSucceeded})
	assert.ErrorIs(t, err, ErrNotExposed)
	assert.Equal(t, "admitted", f.row(t, 2).Delivery)
	assert.Equal(t, StateQueued, getRecord(t, f.ledger, 2).State)
}

// Done when: a superseded token is refused.
func TestASupersededTokenIsRefused(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	_, _, err := f.d.Get(ctx, 1)
	require.NoError(t, err)

	require.NoError(t, f.ledger.SupersedeTask(ctx, f.grant.ID))

	_, _, err = f.d.Get(ctx, 2)
	assert.ErrorIs(t, err, ErrTaskTokenRefused)
	_, err = f.d.Ack(ctx, 1, nil)
	assert.ErrorIs(t, err, ErrTaskTokenRefused)
	_, err = f.d.Complete(ctx, 1, Completion{Outcome: OutcomeSucceeded})
	assert.ErrorIs(t, err, ErrTaskTokenRefused)
	assert.Equal(t, "admitted", f.row(t, 2).Delivery, "a refused get exposes nothing")
	assert.Equal(t, "exposed", f.row(t, 1).Delivery)

	stranger, err := f.ledger.Dispatch("not-a-token", adapterAgentID)
	require.NoError(t, err)
	_, _, err = stranger.Get(ctx, 1)
	assert.ErrorIs(t, err, ErrTaskTokenRefused)
}

func TestAWorkerSeesOnlyItsOwnTask(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	seenRecord(t, f.ledger, 3)
	_, err := f.ledger.Admission().Commit(ctx, admittedVerdict(3, 0, "recording:other"))
	require.NoError(t, err)
	_, err = f.ledger.CreateTask(ctx, []int64{3})
	require.NoError(t, err)

	_, _, err = f.d.Get(ctx, 3)
	assert.ErrorIs(t, err, ErrNotOnTask)
	_, err = f.d.Ack(ctx, 3, nil)
	assert.ErrorIs(t, err, ErrNotOnTask)
	_, _, err = f.d.Get(ctx, 404)
	assert.ErrorIs(t, err, ErrNotOnTask, "an unknown event reads the same as another task's")
	assert.Equal(t, StateAdmitted, getRecord(t, f.ledger, 3).State)
}

func TestAnEventThatLeftThePathIsNotHandedOut(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	require.NoError(t, f.ledger.SetState(ctx, 2, StateDiscarded, "by_operator"))

	_, _, err := f.d.Get(ctx, 2)
	assert.ErrorIs(t, err, ErrNotDispatchable)
	assert.Equal(t, "admitted", f.row(t, 2).Delivery)
}

// Retention took the instruction: a completed event asked for again answers
// that it can no longer be dispatched, never an empty instruction.
func TestAnEventWhoseContentWasDroppedIsNotHandedOut(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	f.ledger.now = func() time.Time { return at }
	_, _, err := f.d.Get(ctx, 1)
	require.NoError(t, err)
	_, err = f.d.Complete(ctx, 1, Completion{Outcome: OutcomeSucceeded})
	require.NoError(t, err)
	dropped, err := f.ledger.DropContent(ctx, at.Add(time.Hour), at.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, dropped)

	_, _, err = f.d.Get(ctx, 1)
	assert.ErrorIs(t, err, ErrNotDispatchable)
}

// The dispatcher writes the originating event's record as dispatched when it
// launches the worker. get_dispatch exposes it all the same.
func TestGetDispatchExposesAnEventAlreadyDispatchedAtLaunch(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	require.NoError(t, f.ledger.SetState(ctx, 1, StateDispatched, ""))
	revision := getRecord(t, f.ledger, 1).Revision

	got, ok, err := f.d.Get(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, DeliveryExposed, got.Delivery)
	assert.Equal(t, "exposed", f.row(t, 1).Delivery)
	assert.Equal(t, revision, getRecord(t, f.ledger, 1).Revision, "the record was already where exposure puts it")
}

func TestDeliveryNeverGoesBack(t *testing.T) {
	f := newDispatchFixture(t)
	_, _, err := f.d.Get(context.Background(), 1)
	require.NoError(t, err)

	_, err = f.ledger.db.ExecContext(context.Background(), `UPDATE task_events SET delivery = 'admitted' WHERE event_id = 1`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never goes back")
}

func TestCreateTaskTakesOnlyWorkWaitingForAWorker(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)

	_, err := ledger.CreateTask(ctx, []int64{1})
	require.Error(t, err)
	_, err = ledger.CreateTask(ctx, []int64{404})
	assert.ErrorIs(t, err, ErrNoSuchRecord)
	_, err = ledger.CreateTask(ctx, nil)
	require.Error(t, err)
	var tasks int
	require.NoError(t, ledger.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM tasks`).Scan(&tasks))
	assert.Zero(t, tasks, "a refused task leaves nothing behind")

	blocked := blockedVerdict(1, 0, admission.ReasonNoRoute)
	_, err = ledger.Admission().Commit(ctx, blocked)
	require.NoError(t, err)
	_, err = ledger.CreateTask(ctx, []int64{1})
	require.Error(t, err)
}

func TestTheTokenIsStoredOnlyAsAHash(t *testing.T) {
	f := newDispatchFixture(t)
	var stored string
	require.NoError(t, f.ledger.db.QueryRowContext(context.Background(), `SELECT token_sha256 FROM tasks WHERE id = ?`, f.grant.ID).Scan(&stored))
	assert.NotContains(t, stored, f.grant.Token)
	assert.Len(t, stored, 64)
	assert.GreaterOrEqual(t, len(f.grant.Token), 43, "32 random bytes")
}

func TestCompleteRefusesMalformedReports(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	_, _, err := f.d.Get(ctx, 1)
	require.NoError(t, err)

	tooMany := make([]string, maxCompletionLinks+1)
	for i := range tooMany {
		tooMany[i] = "https://example.com/"
	}
	for name, c := range map[string]Completion{
		"no outcome":      {},
		"unknown outcome": {Outcome: "unknown"},
		"not a URL":       {Outcome: OutcomeSucceeded, Links: []string{"javascript:alert(1)"}},
		"too many links":  {Outcome: OutcomeSucceeded, Links: tooMany},
		"a link too long": {Outcome: OutcomeSucceeded, Links: []string{"https://example.com/" + strings.Repeat("a", maxLinkLength)}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.d.Complete(ctx, 1, c)
			require.Error(t, err)
			assert.Equal(t, "exposed", f.rowContext(ctx, t, 1).Delivery)
		})
	}
}

func TestStripMentionsOf(t *testing.T) {
	agent := mentionMarkup(adapterAgentID)
	other := mentionMarkup(otherPersonID)
	withFigure := strings.Replace(agent, "></bc-attachment>", `><figure><img src="a.png"><figcaption>Agent</figcaption></figure></bc-attachment>`, 1)
	file := `<bc-attachment sgid="BAh7notaperson" content-type="application/pdf" filename="a.pdf"></bc-attachment>`
	selfClosing := strings.Replace(agent, "></bc-attachment>", " />", 1)
	unclosed := strings.Replace(agent, "</bc-attachment>", "", 1)

	for name, tc := range map[string]struct{ in, want string }{
		"the agent's mention":            {"<div>" + agent + " do it</div>", "<div> do it</div>"},
		"with its figure":                {"<div>" + withFigure + " do it</div>", "<div> do it</div>"},
		"another person's mention stays": {"<div>" + other + " and " + agent + "</div>", "<div>" + other + " and </div>"},
		"a file stays":                   {file + agent, file},
		"self-closing":                   {"a" + selfClosing + "b", "ab"},
		"self-closing, before another":   {selfClosing + other, other},
		"unclosed, before another":       {unclosed + " x " + other, " x " + other},
		"every occurrence":               {agent + " and " + agent, " and "},
		"no attachments":                 {"<div>plain</div>", "<div>plain</div>"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, StripMentionsOf(tc.in, adapterAgentID))
		})
	}
}
