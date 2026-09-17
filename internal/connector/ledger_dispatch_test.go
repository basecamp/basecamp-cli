package connector

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	d, err := ledger.Dispatch(ctx, grant.Token, adapterAgentID)
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
	record := getRecord(t, f.ledger, 2)
	require.Equal(t, StateDispatched, record.State)

	first, ok, err := f.d.Get(ctx, 2)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, DeliveryExposed, first.Delivery)
	row := f.row(t, 2)
	assert.Equal(t, "exposed", row.Delivery)
	require.NotNil(t, row.ExposedAt)
	assert.Equal(t, "admitted", f.row(t, 1).Delivery, "the other event is not exposed")

	f.ledger.now = func() time.Time { return t0.Add(time.Minute) }
	again, ok, err := f.d.Get(ctx, 2)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, first, again, "a repeat returns the same instruction")
	assert.Equal(t, row, f.row(t, 2), "and marks nothing further")
	assert.Equal(t, record.Revision, getRecord(t, f.ledger, 2).Revision, "exposure is the task's, not the record's")
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

	_, err = f.ledger.Dispatch(ctx, "not-a-token", adapterAgentID)
	assert.ErrorIs(t, err, ErrTaskTokenRefused, "refused when bound, not only on use")
	_, err = f.ledger.Dispatch(ctx, f.grant.Token, adapterAgentID)
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
}

func TestAnEventThatLeftThePathIsNotHandedOut(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	require.NoError(t, f.ledger.SetState(ctx, 2, StateBlocked, "no_route"))

	_, _, err := f.d.Get(ctx, 2)
	assert.ErrorIs(t, err, ErrNotDispatchable)
	assert.Equal(t, "admitted", f.row(t, 2).Delivery)

	// Withdrawn back to admitted, content and all: not a worker's any more.
	require.NoError(t, f.ledger.SetState(ctx, 1, StateAdmitted, ""))
	require.NotEmpty(t, getRecord(t, f.ledger, 1).Decision.Snapshot)
	_, _, err = f.d.Get(ctx, 1)
	assert.ErrorIs(t, err, ErrNotDispatchable)
	assert.Equal(t, "admitted", f.row(t, 1).Delivery)
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

// A record is dispatched exactly while a live task carries it: joining a task
// moves it there, in the task's own transaction.
func TestCreateTaskDispatchesItsRecords(t *testing.T) {
	f := newDispatchFixture(t)
	assert.Equal(t, StateDispatched, getRecord(t, f.ledger, 1).State)
	assert.Equal(t, StateDispatched, getRecord(t, f.ledger, 2).State, "the queued follow-up too")
}

// An event is on at most one live task. A retried launch is refused and
// writes nothing; after a redispatch supersedes the task, it joins a new one.
func TestAnEventIsOnOneLiveTask(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	countTasks := func() int {
		var n int
		require.NoError(t, f.ledger.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&n))
		return n
	}

	_, err := f.ledger.CreateTask(ctx, []int64{1})
	assert.ErrorIs(t, err, ErrEventOnLiveTask)
	_, err = f.ledger.CreateTask(ctx, []int64{2, 1})
	assert.ErrorIs(t, err, ErrEventOnLiveTask)
	assert.Equal(t, 1, countTasks(), "a refused task leaves no task behind")

	// The database refuses it too, whoever writes.
	_, err = f.ledger.db.ExecContext(ctx, `INSERT INTO tasks (id, token_sha256, created_at) VALUES (99, 'x', 'now')`)
	require.NoError(t, err)
	_, err = f.ledger.db.ExecContext(ctx, `INSERT INTO task_events (task_id, event_id) VALUES (99, 1)`)
	require.Error(t, err)

	require.NoError(t, f.ledger.SupersedeTask(ctx, f.grant.ID))
	grant, err := f.ledger.CreateTask(ctx, []int64{1, 2})
	require.NoError(t, err)
	d, err := f.ledger.Dispatch(ctx, grant.Token, adapterAgentID)
	require.NoError(t, err)
	got, ok, err := d.Get(ctx, 0)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, int64(1), got.EventID)
	_, _, err = f.d.Get(ctx, 1)
	assert.ErrorIs(t, err, ErrTaskTokenRefused)
}

// The dispatcher writes a task with its attempt and exposure in one commit;
// a task created in a transaction that rolls back leaves nothing, and does not
// hold the event.
func TestCreateTaskInsideACallersTransaction(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	_, err := ledger.Admission().Commit(ctx, admittedVerdict(1, 0, "recording:9"))
	require.NoError(t, err)

	tx, err := ledger.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = ledger.createTask(ctx, tx, []int64{1})
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	assert.Equal(t, StateAdmitted, getRecord(t, ledger, 1).State)

	tx, err = ledger.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = ledger.createTask(ctx, tx, []int64{1})
	require.NoError(t, err)
	_, err = ledger.createTask(ctx, tx, []int64{1})
	require.ErrorIs(t, err, ErrEventOnLiveTask, "one live task per event within a transaction too")
	require.NoError(t, tx.Rollback())

	tx, err = ledger.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	grant, err := ledger.createTask(ctx, tx, []int64{1})
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	_, err = ledger.Dispatch(ctx, grant.Token, adapterAgentID)
	require.NoError(t, err)
	assert.Equal(t, StateDispatched, getRecord(t, ledger, 1).State)
}

// A task is only ever made of instructions a worker can pull. A record that
// lost its snapshot on the way through blocked is refused, not dispatched as
// an empty task.
func TestCreateTaskRefusesARecordWithoutItsInstruction(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	require.NoError(t, f.ledger.SetState(ctx, 1, StateBlocked, "read_failed"))
	require.NoError(t, f.ledger.SupersedeTask(ctx, f.grant.ID))
	require.NoError(t, f.ledger.SetState(ctx, 1, StateAdmitted, ""))

	_, err := f.ledger.CreateTask(ctx, []int64{1})
	require.ErrorIs(t, err, ErrNotDispatchable)
	assert.Equal(t, StateAdmitted, getRecord(t, f.ledger, 1).State)

	_, err = f.ledger.CreateTask(ctx, []int64{2, 2})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrEventOnLiveTask, "a duplicate id is not another task's event")
}

func TestConcurrentLaunchesOfOneEventMakeOneTask(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	_, err := ledger.Admission().Commit(ctx, admittedVerdict(1, 0, "recording:9"))
	require.NoError(t, err)

	const racers = 8
	errs := make([]error, racers)
	done := make(chan struct{})
	for i := range racers {
		go func() {
			defer func() { done <- struct{}{} }()
			_, errs[i] = ledger.CreateTask(ctx, []int64{1})
		}()
	}
	for range racers {
		<-done
	}
	created := 0
	for _, err := range errs {
		if err == nil {
			created++
			continue
		}
		assert.ErrorIs(t, err, ErrEventOnLiveTask)
	}
	assert.Equal(t, 1, created)
	var live int
	require.NoError(t, ledger.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_events WHERE event_id = 1 AND retired_at IS NULL`).Scan(&live))
	assert.Equal(t, 1, live)
}

// One event that left the path never hides the rest of the task.
func TestTheEarliestSkipsAnEventThatLeftThePath(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	require.NoError(t, f.ledger.SetState(ctx, 1, StateBlocked, "read_failed"))

	got, ok, err := f.d.Get(ctx, 0)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, int64(2), got.EventID)
}

// A worker's report is what it did: it is recorded even when the record has
// moved since, and only a dispatched record is completed by it.
func TestAReportIsRecordedWhateverHappenedToTheRecord(t *testing.T) {
	f := newDispatchFixture(t)
	ctx := context.Background()
	_, _, err := f.d.Get(ctx, 2)
	require.NoError(t, err)
	require.NoError(t, f.ledger.SetState(ctx, 2, StateAdmitted, ""))

	_, err = f.d.Ack(ctx, 2, nil)
	require.NoError(t, err)
	receipt, err := f.d.Complete(ctx, 2, Completion{Outcome: OutcomeFailed})
	require.NoError(t, err)
	assert.Equal(t, DeliveryCompleted, receipt.Delivery)
	assert.Equal(t, OutcomeFailed, receipt.Outcome)
	assert.Equal(t, StateAdmitted, getRecord(t, f.ledger, 2).State)
}

// A worker's open never leaves a ledger behind where there was none: not
// through the privacy check, and not through SQLite, whichever the file
// disappears before.
func TestOpenExistingLedgerCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.Mkdir(dir, 0o700))
	path := filepath.Join(dir, LedgerFile)

	_, err := OpenExistingLedger(context.Background(), path)
	require.ErrorIs(t, err, os.ErrNotExist)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "the privacy check created nothing")

	// The file gone after the check: SQLite itself must refuse.
	db, err := sql.Open("sqlite", ledgerDSN(path, false))
	require.NoError(t, err)
	defer db.Close()
	require.Error(t, db.PingContext(context.Background()))
	entries, err = os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "SQLite created nothing")

	owner, err := sql.Open("sqlite", ledgerDSN(path, true))
	require.NoError(t, err)
	defer owner.Close()
	require.NoError(t, owner.PingContext(context.Background()), "the connector's own open still creates")
}

func TestOpenExistingLedgerNeverCreatesOrMigrates(t *testing.T) {
	dir := t.TempDir() + "/state"
	_, err := OpenExistingLedger(context.Background(), dir+"/missing.db")
	require.Error(t, err)

	path := dir + "/connector.db"
	all := migrations
	migrations = all[:4]
	old, err := OpenLedger(path)
	migrations = all
	require.NoError(t, err)
	require.NoError(t, old.Close())

	_, err = OpenExistingLedger(context.Background(), path)
	require.ErrorIs(t, err, ErrLedgerSchema)
	again, err := OpenLedger(path)
	require.NoError(t, err)
	version, err := again.SchemaVersion(context.Background())
	require.NoError(t, err)
	require.NoError(t, again.Close())
	assert.Equal(t, len(migrations), version, "migrated only by the owner's open")

	current, err := OpenExistingLedger(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, current.Close())
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
			require.ErrorIs(t, err, ErrInvalidReport)
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
		"single-quoted sgid":             {"a" + strings.ReplaceAll(agent, `"`, "'") + "b", "ab"},
		"a > inside another attribute":   {"a" + strings.Replace(agent, "<bc-attachment ", `<bc-attachment caption="x > y" `, 1) + "b", "ab"},
		"an entity in the sgid":          {"a" + entityEncodedSGID(agent) + "b", "ab"},
		"inside a comment it is text":    {"<!-- " + agent + " -->" + other, "<!-- " + agent + " -->" + other},
		"uppercase":                      {"a" + strings.ToUpper(agent[:14]) + agent[14:] + "b", "ab"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, StripMentionsOf(tc.in, adapterAgentID))
		})
	}
}

// entityEncodedSGID writes the mention's sgid with its first character as a
// hex entity, the way a serializer may.
func entityEncodedSGID(mention string) string {
	i := strings.Index(mention, `sgid="`) + len(`sgid="`)
	return mention[:i] + fmt.Sprintf("&#x%x;", mention[i]) + mention[i+1:]
}
