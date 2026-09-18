package connector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// admitOn writes an admitted record on a conversation key.
func admitOn(t *testing.T, ledger *Ledger, id int64, key string) {
	t.Helper()
	seenRecord(t, ledger, id)
	_, err := ledger.Admission().Commit(context.Background(), admittedVerdict(id, 0, key))
	require.NoError(t, err)
}

func launch(t *testing.T, ledger *Ledger, id int64) Launch {
	t.Helper()
	l, err := ledger.LaunchTask(context.Background(), LaunchSpec{EventID: id, Served: []int64{adapterBucketID}, Driver: "fake", Deadline: time.Hour})
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

	_, err := ledger.LaunchTask(context.Background(), LaunchSpec{EventID: 1, Served: []int64{adapterBucketID}, Driver: "fake"})
	require.Error(t, err)
	assert.Equal(t, StateAdmitted, getRecord(t, ledger, 1).State)
	var tasks, attempts int
	require.NoError(t, ledger.db.QueryRowContext(context.Background(), `SELECT (SELECT COUNT(*) FROM tasks), (SELECT COUNT(*) FROM attempts)`).Scan(&tasks, &attempts))
	assert.Zero(t, tasks)
	assert.Zero(t, attempts)
}

// Ledger invariant 2. Two conversations run side by side now: nothing holds
// a directory, because there is no per-task directory to hold.
func TestOneLiveTaskPerConversationAndNoMore(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	launch(t, ledger, 1)

	admitOn(t, ledger, 3, "recording:3")
	second, err := ledger.LaunchTask(ctx, LaunchSpec{EventID: 3, Served: []int64{adapterBucketID}, Driver: "fake"})
	require.NoError(t, err, "another conversation is another task, in the same directory")
	assert.NotZero(t, second.TaskID)

	// The database holds the conversation rule too, whatever the code checks
	// first.
	_, err = ledger.db.ExecContext(ctx, `INSERT INTO tasks (token_sha256, created_at, conversation_key) VALUES ('y', 'now', 'recording:1')`)
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
	// A worker completes what it pulled (#736).
	_, _, err = d.Get(ctx, 1)
	require.NoError(t, err)
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

	joined, err := ledger.JoinConversation(ctx, l.TaskID, []int64{adapterBucketID})
	require.NoError(t, err)
	assert.Equal(t, []int64{2}, joined)
	pending, err := ledger.UnexposedEvents(ctx, l.TaskID)
	require.NoError(t, err)
	assert.Equal(t, []int64{2}, pending)

	_, err = ledger.EndAttempt(ctx, AttemptEnd{AttemptID: l.AttemptID, Stop: StopFinished})
	require.NoError(t, err)
	admitOn(t, ledger, 3, "recording:1")
	joined, err = ledger.JoinConversation(ctx, l.TaskID, []int64{adapterBucketID})
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
	// A worker acknowledges what it pulled (#736): get_dispatch first.
	_, _, err = d.Get(ctx, 1)
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

// A follow-up on the task's conversation joins it. The conversation is the
// whole of the test now: there is no second thing for it to match.
func TestAFollowUpOnTheConversationJoinsTheTask(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	l := launch(t, ledger, 1)
	admitOn(t, ledger, 2, "recording:1")

	joined, err := ledger.JoinConversation(ctx, l.TaskID, []int64{adapterBucketID})
	require.NoError(t, err)
	assert.Equal(t, []int64{2}, joined)
}

// Copilot on #765: the originating record is held to the served set too, not
// only the records that join it.
//
// The dispatcher chooses a record from the set connect.json served when it
// ran the query, then reads the file again on its way into LaunchSpec. A
// project unserved in between would otherwise start a task anyway, because
// the record still carries the served bit admission wrote. The set the
// launch is given is the one that decides.
func TestALaunchIsRefusedForAProjectTheSpecDoesNotServe(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")

	_, err := ledger.LaunchTask(ctx, LaunchSpec{EventID: 1, Served: []int64{adapterBucketID + 1}, Driver: "fake"})
	assert.ErrorIs(t, err, ErrNotStartable, "another project's served set does not authorize this record")

	_, err = ledger.LaunchTask(ctx, LaunchSpec{EventID: 1, Driver: "fake"})
	assert.ErrorIs(t, err, ErrNotStartable, "and a launch that names no served project authorizes nothing")

	assert.Equal(t, StateAdmitted, getRecord(t, ledger, 1).State, "nothing was written either time")

	l, err := ledger.LaunchTask(ctx, LaunchSpec{EventID: 1, Served: []int64{adapterBucketID}, Driver: "fake"})
	require.NoError(t, err, "served, and it launches")
	assert.NotZero(t, l.TaskID)
}

// Copilot on #765: a task is authorized against one project, so nothing from
// another project joins it, however the conversation is shared.
//
// A conversation key is the recording's or the Campfire's, never the
// bucket's, so two records on one conversation can sit in two projects — a
// recording moved between them is the ordinary way. The record carries the
// `served` bit admission wrote, which says the project was served *then*;
// joining on that alone would expose an event through a task authorized
// against a different project, and would go on doing it after the operator
// stopped serving the second one.
func TestAFollowUpInAnotherProjectDoesNotJoinTheTask(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	l := launch(t, ledger, 1)

	// Event 2 is on the same conversation and in another project.
	seenRecord(t, ledger, 2)
	_, err := ledger.ledgerCommitWithBucket(admittedVerdict(2, 0, "recording:1"), adapterBucketID+1)
	require.NoError(t, err)

	joined, err := ledger.JoinConversation(ctx, l.TaskID, []int64{adapterBucketID, adapterBucketID + 1})
	require.NoError(t, err)
	assert.Empty(t, joined, "the task is the originating project's; another project's record is not handed to its worker")
	assert.Equal(t, StateQueued, getRecord(t, ledger, 2).State,
		"and it waits behind the live task on its conversation for a task of its own, rather than riding along in this one")
}

// And the served set is read now, not as it was when the record was
// admitted: a project the operator has stopped serving stops feeding the
// live task on its conversation.
func TestAFollowUpInAProjectNoLongerServedDoesNotJoinTheTask(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	l := launch(t, ledger, 1)
	admitOn(t, ledger, 2, "recording:1")

	joined, err := ledger.JoinConversation(ctx, l.TaskID, nil)
	require.NoError(t, err)
	assert.Empty(t, joined, "no project is served now")

	joined, err = ledger.JoinConversation(ctx, l.TaskID, []int64{adapterBucketID + 5})
	require.NoError(t, err)
	assert.Empty(t, joined, "and the task's own project is not among those served")

	joined, err = ledger.JoinConversation(ctx, l.TaskID, []int64{adapterBucketID})
	require.NoError(t, err)
	assert.Equal(t, []int64{2}, joined, "served again, and the follow-up joins")
}

// Review r2: work in a project no longer served is counted, not silently
// stuck.
func TestStrandedRecordsCountsWorkInUnservedProjects(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	seenRecord(t, ledger, 2)
	_, err := ledger.ledgerCommitWithBucket(admittedVerdict(2, 0, "recording:2"), adapterBucketID+1)
	require.NoError(t, err)

	stranded, err := ledger.StrandedRecords(ctx, []int64{adapterBucketID}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, stranded, "the record in a project connect.json no longer serves")

	stranded, err = ledger.StrandedRecords(ctx, []int64{adapterBucketID, adapterBucketID + 1}, nil)
	require.NoError(t, err)
	assert.Zero(t, stranded, "both projects served")

	stranded, err = ledger.StrandedRecords(ctx, []int64{adapterBucketID + 5}, []int64{adapterBucketID + 5})
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

// The refusal rule (driver's "Refusals"): a refusal is on the attempt's row
// the moment it is recorded, and settled with the attempt.
func TestARefusalIsRecordedOnTheLiveAttemptAndSettledWithIt(t *testing.T) {
	ledger := newTestLedger(t)
	admitOn(t, ledger, 1, "recording:1")
	l := launch(t, ledger, 1)
	refusals := func() int {
		var n int
		require.NoError(t, ledger.db.QueryRowContext(context.Background(), `SELECT refusals FROM attempts WHERE id = ?`, l.AttemptID).Scan(&n))
		return n
	}

	require.NoError(t, ledger.RecordRefusal(context.Background(), l.AttemptID))
	require.NoError(t, ledger.RecordRefusal(context.Background(), l.AttemptID))
	assert.Equal(t, 2, refusals(), "written as they happen, not at the end")

	_, err := ledger.EndAttempt(context.Background(), AttemptEnd{AttemptID: l.AttemptID, Stop: StopLost, UnrecordedRefusals: 1})
	require.NoError(t, err)
	assert.Equal(t, 3, refusals(), "what could not be written then is settled with the attempt")

	assert.ErrorIs(t, ledger.RecordRefusal(context.Background(), l.AttemptID), ErrNoLiveAttempt)
	assert.Equal(t, 3, refusals(), "an ended attempt's count is final")
}

// Copilot: settlement ends a task and adoption runs after it, so the next
// instruction on the conversation can already be on a task of its own. Its
// acknowledgement still bounds what the old event may adopt.
func TestTheAdoptionBoundaryIsTheConversationsNotTheTasks(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	admitOn(t, ledger, 1, "recording:1")
	first := launch(t, ledger, 1)
	d, err := ledger.Dispatch(ctx, first.Token, adapterAgentID)
	require.NoError(t, err)
	_, _, err = d.Get(ctx, 1)
	require.NoError(t, err)
	_, err = d.Ack(ctx, 1, nil)
	require.NoError(t, err)
	_, err = ledger.EndAttempt(ctx, AttemptEnd{AttemptID: first.AttemptID, Stop: StopLost})
	require.NoError(t, err)

	// The next instruction on the same conversation, on a task of its own.
	admitOn(t, ledger, 2, "recording:1")
	second := launch(t, ledger, 2)
	d2, err := ledger.Dispatch(ctx, second.Token, adapterAgentID)
	require.NoError(t, err)
	_, _, err = d2.Get(ctx, 2)
	require.NoError(t, err)
	_, err = d2.Ack(ctx, 2, nil)
	require.NoError(t, err)

	candidates, err := ledger.AdoptionCandidates(ctx, first.TaskID)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	assert.False(t, candidates[0].NextAckAt.IsZero(),
		"the later task's acknowledgement bounds what the lost event may adopt")
	assert.False(t, candidates[0].NextAckAt.Before(candidates[0].DeliveredAt))
}
