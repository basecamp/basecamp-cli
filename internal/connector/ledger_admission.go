package connector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/ndjson"
)

// Admission is the ledger as admission sees it: the Records it loads events
// from and the Ledger its verdicts are committed to.
//
// Admission is its own package and imports nothing from this one. This is the
// whole of the join: the pointer intake recorded goes out as an admission
// Event with the revision it was loaded at, and a verdict comes back through
// the ledger's one state-changing write, so the lifecycle that keeps a
// finished record finished is the same lifecycle a verdict meets.
type Admission struct {
	ledger *Ledger
}

var (
	_ admission.Records = Admission{}
	_ admission.Ledger  = Admission{}
)

// Admission returns the ledger's admission seam.
func (l *Ledger) Admission() Admission { return Admission{ledger: l} }

// undecided is where a record may be for admission to decide it: seen, which
// nothing has judged yet, and blocked, which recovery and redispatch decide
// again. Everything past these was decided by admission or moved on by
// dispatch, and is never decided a second time.
var undecided = []RecordState{StateSeen, StateBlocked}

// LoadUndecided loads a seen or blocked record as the event admission decides,
// with the revision it was loaded at. An unknown id, or a record past
// deciding, is not ok and is skipped.
func (a Admission) LoadUndecided(ctx context.Context, id int64) (admission.Event, bool, error) {
	record, ok, err := a.ledger.Get(ctx, id)
	if err != nil || !ok {
		return admission.Event{}, false, err
	}
	if record.State != StateSeen && record.State != StateBlocked {
		return admission.Event{}, false, nil
	}
	return admission.Event{
		ID:            record.ID,
		EventType:     record.EventType,
		BucketID:      record.BucketID,
		RecordingID:   record.RecordingID,
		CreatorID:     record.CreatorID,
		PerformedByID: record.PerformedByID,
		ActorType:     record.ActorType,
		Details:       record.Details,
		Revision:      record.Revision,
		SeenAt:        record.SeenAt,
	}, true, nil
}

// Commit writes a verdict onto its record in one transaction, and returns the
// state it wrote. It holds admission.Ledger's contract:
//
//   - the write applies only while the record is still at the verdict's
//     revision and still undecided, and it bumps the revision; otherwise it
//     is admission.ErrAlreadyDecided;
//   - an admitted verdict is written as queued when its conversation is
//     live, decided inside the transaction: another record on the key is
//     admitted and not yet dispatched (it becomes the task) or dispatched
//     (the task is running). A queued record alone is not a live
//     conversation: queued records join their task before it closes, and
//     counting one left behind would queue every later event on the key
//     with nothing to start a task;
//   - content is written only with an admitted verdict;
//   - a throttled verdict keeps its RetryAt.
func (a Admission) Commit(ctx context.Context, v admission.Verdict) (admission.State, error) {
	state, err := verdictState(v)
	if err != nil {
		return "", err
	}
	var snapshot []byte
	if v.Snapshot != nil {
		if snapshot, err = json.Marshal(v.Snapshot); err != nil {
			return "", fmt.Errorf("connector: commit verdict on %d: %w", v.EventID, err)
		}
	}

	var written RecordState
	err = retryBusy(func() error {
		var err error
		written, err = a.commit(ctx, v, state, snapshot)
		return err
	})
	if err != nil {
		return "", err
	}
	return admission.State(written), nil
}

func (a Admission) commit(ctx context.Context, v admission.Verdict, state RecordState, snapshot []byte) (RecordState, error) {
	l := a.ledger
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("connector: begin verdict on %d: %w", v.EventID, err)
	}
	defer func() { _ = tx.Rollback() }()

	if state == StateAdmitted {
		// The record being decided is seen or blocked, so it never counts
		// itself as the conversation's task.
		var live bool
		if err := tx.QueryRowContext(ctx, liveConversation, v.ConversationKey, string(StateAdmitted), string(StateDispatched)).Scan(&live); err != nil {
			return "", fmt.Errorf("connector: read conversation of %d: %w", v.EventID, err)
		}
		if live {
			state = StateQueued
		}
	}

	var retryAt time.Time
	if state == StateBlocked {
		retryAt = v.RetryAt
	}
	reply := admission.ReplyDestination{}
	if v.Reply != nil {
		reply = *v.Reply
	}
	revision := v.Revision
	moved, err := l.move(ctx, tx, transition{
		id:       v.EventID,
		state:    state,
		reason:   string(v.Reason),
		from:     undecided,
		revision: &revision,
		retryAt:  retryAt,
		set: []assignment{
			{column: "decided_at", value: l.timestamp()},
			{column: "trigger_name", value: string(v.Trigger)},
			{column: "acknowledge", value: v.Acknowledge},
			{column: "conversation_key", value: v.ConversationKey},
			{column: "reply_kind", value: string(reply.Kind)},
			{column: "reply_recording_id", value: reply.RecordingID},
			{column: "routed", value: v.Routed},
			{column: "route", value: v.Route},
			{column: "class", value: v.Class},
			{column: "recording_url", value: v.RecordingURL},
			{column: "requester_id", value: v.RequesterID},
			{column: "snapshot", value: snapshot},
		},
	})
	if err != nil {
		return "", err
	}
	if !moved {
		return "", explainVerdictRefusal(ctx, tx, v)
	}
	if l.hooks.VerdictCommitted != nil {
		committed := CommittedVerdict{
			EventID:          v.EventID,
			State:            state,
			Reason:           string(v.Reason),
			Trigger:          string(v.Trigger),
			Acknowledge:      v.Acknowledge,
			ReplyKind:        string(reply.Kind),
			ReplyRecordingID: reply.RecordingID,
		}
		if err := l.hooks.VerdictCommitted(ctx, tx, committed); err != nil {
			return "", fmt.Errorf("connector: verdict hook for %d: %w", v.EventID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("connector: commit verdict on %d: %w", v.EventID, err)
	}
	return state, nil
}

// liveConversation asks whether a conversation has a task running or a record
// about to become one. It is read through events_conversation.
const liveConversation = `
SELECT EXISTS (
  SELECT 1 FROM events
  WHERE conversation_key = ? AND state IN (?, ?)
)`

// verdictState checks a verdict is one the ledger can write and maps it to
// the ledger's state. Queued is the ledger's decision, never a verdict's.
func verdictState(v admission.Verdict) (RecordState, error) {
	refuse := func(why string) (RecordState, error) {
		return "", fmt.Errorf("connector: verdict on %d refused: %s", v.EventID, why)
	}
	switch v.State {
	case admission.StateAdmitted:
		if v.Snapshot == nil {
			return refuse("an admitted verdict carries the recording it admitted")
		}
		if v.ConversationKey == "" {
			return refuse("an admitted verdict needs a conversation key, or it can never queue")
		}
		return StateAdmitted, nil
	case admission.StateBlocked, admission.StateDiscarded:
		if v.Snapshot != nil {
			return refuse("only an admitted verdict carries content")
		}
		return RecordState(v.State), nil
	default:
		return refuse(fmt.Sprintf("%q is not a state admission decides", v.State))
	}
}

// explainVerdictRefusal says why a verdict changed nothing, reading the row in
// the verdict's own transaction so the answer is the state that refused it.
func explainVerdictRefusal(ctx context.Context, tx dbtx, v admission.Verdict) error {
	var (
		state    string
		revision int64
	)
	switch err := tx.QueryRowContext(ctx, `SELECT state, revision FROM events WHERE id = ?`, v.EventID).Scan(&state, &revision); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("connector: verdict on %d: %w", v.EventID, ErrNoSuchRecord)
	case err != nil:
		return fmt.Errorf("connector: verdict on %d: %w", v.EventID, err)
	}
	// Moved on since the decision loaded it, by another decision or by
	// dispatch: the newer state stands.
	return fmt.Errorf("connector: verdict on %d loaded at revision %d, record is %s at revision %d: %w",
		v.EventID, v.Revision, state, revision, admission.ErrAlreadyDecided)
}

// AdmissionOptions wires admission onto intake's queue and ledger.
type AdmissionOptions struct {
	Ledger   *Ledger
	Queue    *Queue
	Admitter *admission.Admitter
	// Workers is the fetcher pool size; admission's default when zero.
	Workers int
	// Lines is the writer intake's pointer lines go through. Verdict lines
	// share it, so the two never tear each other on one stdout.
	Lines  *ndjson.Writer
	Logger *slog.Logger
}

// RunAdmission takes the ids intake hands over and decides each against the
// ledger, until ctx ends. It returns what admission.Run returns.
func RunAdmission(ctx context.Context, opts AdmissionOptions) error {
	if opts.Ledger == nil || opts.Queue == nil {
		return errors.New("connector: admission needs the ledger and the queue intake writes to")
	}
	records := opts.Ledger.Admission()
	return admission.Run(ctx, admission.RunOptions{
		Source:     opts.Queue,
		Records:    records,
		Admitter:   opts.Admitter,
		Committer:  admission.NewCommitter(records),
		Workers:    opts.Workers,
		LineWriter: opts.Lines,
		Logger:     opts.Logger,
	})
}
