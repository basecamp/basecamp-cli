package connector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testRoute = "/work/connector"

// admitOn writes an admitted record on a conversation key.
func admitOn(t *testing.T, ledger *Ledger, id int64, key string) {
	t.Helper()
	seenRecord(t, ledger, id)
	_, err := ledger.Admission().Commit(context.Background(), admittedVerdict(id, 0, key))
	require.NoError(t, err)
}

func launch(t *testing.T, ledger *Ledger, id int64) Launch {
	t.Helper()
	l, err := ledger.LaunchTask(context.Background(), LaunchSpec{EventID: id, Route: testRoute, Driver: "fake", Deadline: time.Hour})
	require.NoError(t, err)
	return l
}

type attemptRow struct {
	State, StopReason string
	SpawnFailed       bool
}

func readAttempt(t *testing.T, ledger *Ledger, id string) attemptRow {
	t.Helper()
	var r attemptRow
	require.NoError(t, ledger.db.QueryRowContext(context.Background(), `SELECT state, stop_reason, spawn_failed FROM attempts WHERE id = ?`, id).Scan(&r.State, &r.StopReason, &r.SpawnFailed))
	return r
}

type taskEventState struct {
	Delivery, Outcome string
	ExposedBy         *string
	Withdrawn         *string
	Adopted           *int64
}

func readTaskEvent(t *testing.T, ledger *Ledger, taskID, eventID int64) taskEventState {
	t.Helper()
	var s taskEventState
	require.NoError(t, ledger.db.QueryRowContext(context.Background(), `SELECT delivery, outcome, exposed_attempt_id, withdrawn_at, adopted_reply_id FROM task_events WHERE task_id = ? AND event_id = ?`,
		taskID, eventID).Scan(&s.Delivery, &s.Outcome, &s.ExposedBy, &s.Withdrawn, &s.Adopted))
	return s
}

// Ledger invariant 1: launching, the originating exposure and the record's
// move are one transaction.
func TestLaunchWritesLaunchingAndExposureTogether(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	admitOn(t, ledger, 2, "recording:1")

	l := launch(t, ledger, 1)
	assert.Equal(t, []int64{1, 2}, l.EventIDs)
	assert.Equal(t, "launching", readAttempt(t, ledger, l.AttemptID).State)

	origin := readTaskEvent(t, ledger, l.TaskID, 1)
	assert.Equal(t, "exposed", origin.Delivery)
	require.NotNil(t, origin.ExposedBy)
	assert.Equal(t, l.AttemptID, *origin.ExposedBy)
	assert.Equal(t, StateDispatched, getRecord(t, ledger, 1).State)

	follow := readTaskEvent(t, ledger, l.TaskID, 2)
	assert.Equal(t, "admitted", follow.Delivery, "a joined follow-up is not exposed by the launch")
	assert.Equal(t, StateDispatched, getRecord(t, ledger, 2).State, "a record on a task has left the queue")
}

func TestALaunchHookFailureLeavesNothingWritten(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	ledger.SetHooks(Hooks{TaskLaunched: func(context.Context, Tx, Launch) error { return errors.New("outbox refused") }})

	_, err := ledger.LaunchTask(context.Background(), LaunchSpec{EventID: 1, Route: testRoute, Driver: "fake"})
	require.Error(t, err)
	assert.Equal(t, StateAdmitted, getRecord(t, ledger, 1).State)
	var tasks, attempts int
	require.NoError(t, ledger.db.QueryRowContext(context.Background(), `SELECT (SELECT COUNT(*) FROM tasks), (SELECT COUNT(*) FROM attempts)`).Scan(&tasks, &attempts))
	assert.Zero(t, tasks)
	assert.Zero(t, attempts)
}

func TestALaunchMustNameTheRecordsRoute(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	_, err := ledger.LaunchTask(context.Background(), LaunchSpec{EventID: 1, Route: "/somewhere/else", Driver: "fake"})
	assert.ErrorIs(t, err, ErrWorkDirMismatch)
	assert.Equal(t, StateAdmitted, getRecord(t, ledger, 1).State)
}

// Ledger invariant 2.
func TestOneLiveTaskPerConversationAndPerWorkingDirectory(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	launch(t, ledger, 1)

	admitOn(t, ledger, 3, "recording:3")
	_, err := ledger.LaunchTask(ctx, LaunchSpec{EventID: 3, Route: testRoute, Driver: "fake"})
	assert.ErrorIs(t, err, ErrNotStartable, "the working directory is busy")

	// The database holds it too, whatever the code checks first.
	_, err = ledger.db.ExecContext(context.Background(), `INSERT INTO tasks (token_sha256, created_at, conversation_key, work_dir) VALUES ('x', 'now', 'recording:9', ?)`, testRoute)
	require.Error(t, err)
	_, err = ledger.db.ExecContext(context.Background(), `INSERT INTO tasks (token_sha256, created_at, conversation_key, work_dir) VALUES ('y', 'now', 'recording:1', '/other')`)
	require.Error(t, err)
}

func TestAnEventIsOnAtMostOneLiveTask(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	l := launch(t, ledger, 1)
	_, err := ledger.db.ExecContext(context.Background(), `INSERT INTO tasks (token_sha256, created_at) VALUES ('z', 'now')`)
	require.NoError(t, err)
	_, err = ledger.db.ExecContext(context.Background(), `INSERT INTO task_events (task_id, event_id) VALUES (?, 1)`, l.TaskID+1)
	assert.ErrorContains(t, err, "UNIQUE constraint failed")
}

// Ledger invariant 3.
func TestAnEndedTaskHasNoValidToken(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	l := launch(t, ledger, 1)
	d, err := ledger.Dispatch(context.Background(), l.Token, adapterAgentID)
	require.NoError(t, err)
	_, ok, err := d.Get(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)

	_, err = ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopFinished})
	require.NoError(t, err)
	_, _, err = d.Get(ctx, 1)
	assert.ErrorIs(t, err, ErrTaskTokenRefused)

	admitOn(t, ledger, 2, "recording:2")
	l2 := launch(t, ledger, 2)
	_, err = ledger.db.ExecContext(context.Background(), `UPDATE tasks SET ended_at = 'now' WHERE id = ?`, l2.TaskID)
	assert.ErrorContains(t, err, "superseded")
}

// Ledger invariant 4: a proven spawn failure withdraws once.
func TestASpawnFailureIsRetriedOnceThenBlocked(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")

	first := launch(t, ledger, 1)
	s, err := ledger.EndAttempt(ctx, AttemptEnd{AttemptID: first.AttemptID, Stop: StopFailed, SpawnFailed: true})
	require.NoError(t, err)
	require.Len(t, s.Events, 1)
	assert.True(t, s.Events[0].Withdrawn)
	assert.False(t, s.Events[0].Blocked)
	assert.Equal(t, StateAdmitted, getRecord(t, ledger, 1).State)
	assert.NotNil(t, readTaskEvent(t, ledger, first.TaskID, 1).Withdrawn)

	second := launch(t, ledger, 1)
	s, err = ledger.EndAttempt(ctx, AttemptEnd{AttemptID: second.AttemptID, Stop: StopFailed, SpawnFailed: true})
	require.NoError(t, err)
	assert.True(t, s.Events[0].Blocked)
	record := getRecord(t, ledger, 1)
	assert.Equal(t, StateBlocked, record.State)
	assert.Equal(t, ReasonSpawnFailed, record.Reason)
}

func TestNoAutomaticRetryBlocksTheFirstSpawnFailure(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	l := launch(t, ledger, 1)
	_, err := ledger.EndAttempt(context.Background(), AttemptEnd{AttemptID: l.AttemptID, Stop: StopFailed, SpawnFailed: true, NoAutomaticRetry: true})
	require.NoError(t, err)
	assert.Equal(t, StateBlocked, getRecord(t, ledger, 1).State)
}

func TestAWorkerThatRanMakesItsExposedEventsUnknown(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	l := launch(t, ledger, 1)
	require.NoError(t, ledger.MarkRunning(ctx, l.AttemptID, AttemptProcess{PID: 4242, PGID: 4242, SessionID: "s"}))

	s, err := ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopLost})
	require.NoError(t, err)
	assert.Equal(t, OutcomeUnknown, s.Events[0].Outcome)
	assert.False(t, s.Events[0].Withdrawn)
	assert.Equal(t, StateCompleted, getRecord(t, ledger, 1).State)
}

func TestASpawnFailureNeverWithdrawsAnExposureTheWorkerMade(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	admitOn(t, ledger, 2, "recording:1")
	l := launch(t, ledger, 1)
	d, err := ledger.Dispatch(context.Background(), l.Token, adapterAgentID)
	require.NoError(t, err)
	_, _, err = d.Get(ctx, 2)
	require.NoError(t, err)

	s, err := ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopFailed, SpawnFailed: true})
	require.NoError(t, err)
	byID := map[int64]SettledEvent{}
	for _, e := range s.Events {
		byID[e.EventID] = e
	}
	assert.True(t, byID[1].Withdrawn)
	assert.Equal(t, OutcomeUnknown, byID[2].Outcome, "get_dispatch's exposure is not the launch's to withdraw")
}

// Ledger invariant 5 and the sibling rule.
func TestSettlementKeepsReportsAndReturnsWhatWasNeverExposed(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	for _, id := range []int64{1, 2, 3} {
		admitOn(t, ledger, id, "recording:1")
	}
	l := launch(t, ledger, 1)
	d, err := ledger.Dispatch(context.Background(), l.Token, adapterAgentID)
	require.NoError(t, err)
	reply := int64(99)
	_, err = d.Complete(ctx, 1, Completion{Outcome: OutcomeFailed, ReplyID: &reply})
	require.NoError(t, err)
	exposed, err := ledger.ExposeEvent(ctx, l.AttemptID, 2)
	require.NoError(t, err)
	require.True(t, exposed)

	s, err := ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopFinished})
	require.NoError(t, err)
	byID := map[int64]SettledEvent{}
	for _, e := range s.Events {
		byID[e.EventID] = e
	}
	assert.Equal(t, OutcomeFailed, byID[1].Outcome, "a reported outcome stands, whatever the stop reason")
	assert.True(t, byID[1].Reported)
	assert.Equal(t, OutcomeUnknown, byID[2].Outcome)
	assert.True(t, byID[3].Returned)
	assert.Equal(t, StateAdmitted, getRecord(t, ledger, 3).State)
	assert.Equal(t, "finished", readAttempt(t, ledger, l.AttemptID).StopReason)

	// A returned follow-up starts a task of its own.
	startable, err := ledger.StartableRecords(ctx, 10)
	require.NoError(t, err)
	require.Len(t, startable, 1)
	assert.Equal(t, int64(3), startable[0].ID)
}

func TestExposeEventIsWrittenOnceAndOnlyForALiveAttempt(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	admitOn(t, ledger, 2, "recording:1")
	l := launch(t, ledger, 1)

	exposed, err := ledger.ExposeEvent(ctx, l.AttemptID, 2)
	require.NoError(t, err)
	assert.True(t, exposed)
	exposed, err = ledger.ExposeEvent(ctx, l.AttemptID, 2)
	require.NoError(t, err)
	assert.False(t, exposed)

	_, err = ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopShutdown})
	require.NoError(t, err)
	_, err = ledger.ExposeEvent(ctx, l.AttemptID, 2)
	assert.ErrorIs(t, err, ErrNoLiveAttempt)
	_, err = ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopShutdown})
	assert.ErrorIs(t, err, ErrNoLiveAttempt)
}

func TestJoinConversationTakesLaterFollowUpsOnlyWhileTheTaskIsLive(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	l := launch(t, ledger, 1)
	admitOn(t, ledger, 2, "recording:1")
	assert.Equal(t, StateQueued, getRecord(t, ledger, 2).State)

	joined, err := ledger.JoinConversation(ctx, l.TaskID)
	require.NoError(t, err)
	assert.Equal(t, []int64{2}, joined)
	pending, err := ledger.UnexposedEvents(ctx, l.TaskID)
	require.NoError(t, err)
	assert.Equal(t, []int64{2}, pending)

	_, err = ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopFinished})
	require.NoError(t, err)
	admitOn(t, ledger, 3, "recording:1")
	joined, err = ledger.JoinConversation(ctx, l.TaskID)
	require.NoError(t, err)
	assert.Empty(t, joined)
}

// Ledger invariant 7.
func TestAttemptStatesMoveForwardOnly(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	l := launch(t, ledger, 1)
	require.NoError(t, ledger.MarkRunning(ctx, l.AttemptID, AttemptProcess{PID: 1234, PGID: 1234, SessionID: "s"}))
	_, err := ledger.db.ExecContext(context.Background(), `UPDATE attempts SET state = 'launching' WHERE id = ?`, l.AttemptID)
	assert.ErrorContains(t, err, "never goes back")
	assert.ErrorIs(t, ledger.MarkRunning(ctx, l.AttemptID, AttemptProcess{}), ErrNoLiveAttempt)
	_, err = ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopDeadline})
	require.NoError(t, err)
	_, err = ledger.db.ExecContext(context.Background(), `UPDATE attempts SET stop_reason = 'finished', state = 'ended' WHERE id = ?`, l.AttemptID)
	assert.Error(t, err, "an ended attempt's stop reason is not rewritten")
}

func TestLiveAttemptsIncludesLaunching(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	l := launch(t, ledger, 1)
	live, err := ledger.LiveAttempts(ctx)
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, AttemptLaunching, live[0].State)
	assert.Equal(t, l.AttemptID, live[0].AttemptID)
	assert.Equal(t, testRoute, live[0].WorkDir)
}

func TestAHookFailureRollsTheTransitionBack(t *testing.T) {
	t.Run("attempt ended", func(t *testing.T) {
		ctx := context.Background()
		ledger := newTestLedger(t)
		admitOn(t, ledger, 1, "recording:1")
		l := launch(t, ledger, 1)
		ledger.SetHooks(Hooks{AttemptEnded: func(context.Context, Tx, Settlement) error { return errors.New("no") }})
		_, err := ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopFinished})
		require.Error(t, err)
		assert.Equal(t, "launching", readAttempt(t, ledger, l.AttemptID).State)
		assert.Equal(t, StateDispatched, getRecord(t, ledger, 1).State)
	})
	t.Run("verdict", func(t *testing.T) {
		ctx := context.Background()
		ledger := newTestLedger(t)
		seenRecord(t, ledger, 1)
		ledger.SetHooks(Hooks{VerdictCommitted: func(context.Context, Tx, CommittedVerdict) error { return errors.New("no") }})
		_, err := ledger.Admission().Commit(ctx, admittedVerdict(1, 0, "recording:1"))
		require.Error(t, err)
		assert.Equal(t, StateSeen, getRecord(t, ledger, 1).State)
	})
	t.Run("still running", func(t *testing.T) {
		ctx := context.Background()
		ledger := newTestLedger(t)
		admitOn(t, ledger, 1, "recording:1")
		l := launch(t, ledger, 1)
		ledger.SetHooks(Hooks{StillRunning: func(context.Context, Tx, StillRunningTick) error { return errors.New("no") }})
		_, err := ledger.StillRunning(ctx, l.AttemptID)
		require.Error(t, err)
		ledger.SetHooks(Hooks{})
		tick, err := ledger.StillRunning(ctx, l.AttemptID)
		require.NoError(t, err)
		assert.Equal(t, 1, tick.Occurrence, "the refused occurrence was not counted")
	})
}

// Ledger invariant 6.
func TestAnAdoptedReplyNeverMakesAnOutcome(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	l := launch(t, ledger, 1)
	d, err := ledger.Dispatch(context.Background(), l.Token, adapterAgentID)
	require.NoError(t, err)
	_, err = d.Ack(ctx, 1, nil)
	require.NoError(t, err)
	_, err = ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopLost})
	require.NoError(t, err)

	candidates, err := ledger.AdoptionCandidates(ctx, l.TaskID)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.NoError(t, ledger.AdoptReply(ctx, l.TaskID, 1, 555))
	row := readTaskEvent(t, ledger, l.TaskID, 1)
	assert.Equal(t, "unknown", row.Outcome)
	require.NotNil(t, row.Adopted)
	assert.Equal(t, int64(555), *row.Adopted)
	assert.Error(t, ledger.AdoptReply(ctx, l.TaskID, 1, 556), "one adoption")
}

func TestAdoptableReplyRule(t *testing.T) {
	acked := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	c := AdoptionCandidate{DeliveredAt: acked, NextAckAt: acked.Add(10 * time.Minute)}
	at := func(m int) time.Time { return acked.Add(time.Duration(m) * time.Minute) }

	id, ok := AdoptableReply(c, []AgentReply{{ID: 1, CreatedAt: at(-1)}, {ID: 2, CreatedAt: at(1)}, {ID: 3, CreatedAt: at(11)}}, nil)
	assert.True(t, ok)
	assert.Equal(t, int64(2), id, "only a reply after the ack and before a later instruction's ack")

	_, ok = AdoptableReply(c, []AgentReply{{ID: 2, CreatedAt: at(1)}, {ID: 4, CreatedAt: at(2)}}, nil)
	assert.False(t, ok, "two candidates adopt nothing")

	_, ok = AdoptableReply(c, []AgentReply{{ID: 2, CreatedAt: at(1)}}, func(id int64) bool { return id == 2 })
	assert.False(t, ok, "a lifecycle message is never adopted")
}

// Copilot: a follow-up admitted under another route waits for its own task.
func TestAFollowUpOnAnotherRouteDoesNotJoinTheTask(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	l := launch(t, ledger, 1)
	seenRecord(t, ledger, 2)
	v := admittedVerdict(2, 0, "recording:1")
	v.Route = "/work/moved"
	_, err := ledger.Admission().Commit(ctx, v)
	require.NoError(t, err)

	joined, err := ledger.JoinConversation(ctx, l.TaskID)
	require.NoError(t, err)
	assert.Empty(t, joined)
}

// Review r2: work no approved route covers is counted, not silently stuck.
func TestStrandedRecordsCountsWorkNoRouteCovers(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	seenRecord(t, ledger, 2)
	moved := admittedVerdict(2, 0, "recording:2")
	moved.Route = "/work/moved"
	_, err := ledger.Admission().Commit(ctx, moved)
	require.NoError(t, err)

	stranded, err := ledger.StrandedRecords(ctx, map[int64]string{adapterBucketID: testRoute}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, stranded, "the record admitted under a route connect.json no longer has")

	stranded, err = ledger.StrandedRecords(ctx, map[int64]string{adapterBucketID: testRoute, adapterBucketID + 1: "/work/moved"}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, stranded, "the route must be approved for the record's own project")

	stranded, err = ledger.StrandedRecords(ctx, map[int64]string{adapterBucketID + 5: testRoute}, []int64{adapterBucketID + 5})
	require.NoError(t, err)
	assert.Zero(t, stranded, "work in a project this run does not hear is another run's, not stranded")
}

// Review r2: the worker's acknowledgement is never adopted as its reply.
func TestAnAcknowledgementIsNeverAdoptedAsTheReply(t *testing.T) {
	acked := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	c := AdoptionCandidate{DeliveredAt: acked, AckID: 7}
	// The ack comment's server timestamp is after this machine's
	// delivered_at, so time alone would adopt it.
	_, ok := AdoptableReply(c, []AgentReply{{ID: 7, CreatedAt: acked.Add(time.Second)}}, nil)
	assert.False(t, ok)
}

// Review r4: a record something else moved is settled where it was put; the
// whole settlement must not fail, or the attempt is stranded for good.
func TestSettlementWorksAroundARecordSomethingElseMoved(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	l := launch(t, ledger, 1)
	var moved []int64
	ledger.SetHooks(Hooks{RecordMoved: func(eventID int64, _ RecordState) { moved = append(moved, eventID) }})
	// A person discards the record while its worker is running.
	require.NoError(t, ledger.SetState(ctx, 1, StateBlocked, "by_operator"))

	settlement, err := ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopLost})
	require.NoError(t, err, "the attempt is settled, not stranded")
	assert.Equal(t, []int64{1}, moved)
	assert.Equal(t, "ended", readAttempt(t, ledger, l.AttemptID).State)
	require.Len(t, settlement.Events, 1)
	assert.False(t, settlement.Events[0].Reported)
	assert.Equal(t, StateBlocked, getRecord(t, ledger, 1).State, "left where it was put")
}
