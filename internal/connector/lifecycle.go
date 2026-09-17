package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Lifecycle messages are fixed forms. Every word comes from this file; every
// value comes from a ledger record — ids, states, stop reasons, times. None
// comes from content, from a worker or from a model, so a message can be
// rendered again from the records alone and matched against what Basecamp
// holds.

// GuardAckBody is the guard acknowledgement: a boost on the recording that
// asked. It carries no event id, because a boost is a few characters; two
// guards on one recording are therefore ambiguous to reconciliation, which
// leaves them indeterminate rather than guess.
const GuardAckBody = "👀 received"

// lifecycleSignature ends every comment and chat line the connector posts, so
// a person can tell a notice from the agent's own words.
const lifecycleSignature = "automatic notice from basecamp connect"

// renderHoldingReply is the reply to a mention or assignment in a project that
// has no route.
func renderHoldingReply(kind MessageKind, eventID int64) string {
	lines := []string{
		"I can't start on this here yet: this project has no working directory set up for me on the connector's machine, so nothing was run.",
		"Once the project is added to connect.json, a person can run it with: basecamp connect redispatch " + strconv.FormatInt(eventID, 10),
		"",
		"Event " + strconv.FormatInt(eventID, 10) + " · " + lifecycleSignature,
	}
	return renderLines(kind, lines)
}

// renderStillRunning is one still-running notice.
func renderStillRunning(kind MessageKind, taskID int64, attemptID string, occurrence int, launchedAt, progressAt time.Time) string {
	progress := "No progress has been reported yet."
	if !progressAt.IsZero() {
		progress = "Last progress at " + clock(progressAt) + "."
	}
	lines := []string{
		"Still working on this: task " + strconv.FormatInt(taskID, 10) + " started at " + clock(launchedAt) + ". " + progress,
		"",
		"Attempt " + attemptID + ", update " + strconv.Itoa(occurrence) + " · " + lifecycleSignature,
	}
	return renderLines(kind, lines)
}

// CompletionNeeded reports whether an attempt's settlement calls for a
// completion notice: an event failed or unknown, succeeded with no reply
// reported, or blocked from a further automatic start. Events that all
// succeeded with replies get none, and neither do events returned to wait for
// a task of their own or withdrawn for their one automatic retry.
func CompletionNeeded(s Settlement) bool {
	for _, e := range s.Events {
		if completionLine(e) != "" {
			return true
		}
	}
	return false
}

// completionLine is what the notice says about one event; empty when it says
// nothing.
func completionLine(e SettledEvent) string {
	id := strconv.FormatInt(e.EventID, 10)
	redispatch := " Needs a person: basecamp connect redispatch " + id
	switch {
	case e.Blocked:
		return "Event " + id + ": the worker could not be started, again." + redispatch
	case e.Withdrawn, e.Returned:
		return ""
	case e.Outcome == OutcomeFailed:
		return "Event " + id + ": failed." + redispatch
	case e.Outcome == OutcomeUnknown:
		return "Event " + id + ": unknown, the worker did not report on it." + redispatch
	case e.Outcome == OutcomeSucceeded && e.ReplyID == nil:
		return "Event " + id + ": succeeded, with no reply reported."
	}
	return ""
}

// stopSentence says how an attempt stopped.
func stopSentence(stop StopReason) string {
	switch stop {
	case StopFinished:
		return "the worker finished"
	case StopFailed:
		return "the worker failed"
	case StopDeadline:
		return "the worker was stopped at the task's deadline"
	case StopShutdown:
		return "the connector shut down and stopped the worker"
	case StopLost:
		return "the worker was lost"
	}
	return "the worker stopped"
}

// renderCompletion is an attempt's completion notice, or "" when the
// settlement calls for none.
func renderCompletion(kind MessageKind, s Settlement) string {
	if !CompletionNeeded(s) {
		return ""
	}
	lines := []string{"Task " + strconv.FormatInt(s.TaskID, 10) + " ended: " + stopSentence(s.Stop) + "."}
	for _, e := range s.Events {
		if line := completionLine(e); line != "" {
			lines = append(lines, line)
		}
	}
	lines = append(lines, "", "Attempt "+s.AttemptID+" · "+lifecycleSignature)
	return renderLines(kind, lines)
}

// renderLines lays lines out for the message kind: rich text for a comment,
// plain text for a chat line. Every line is escaped, though no line holds
// anything but this file's words and record values.
func renderLines(kind MessageKind, lines []string) string {
	if kind == MessageComment {
		escaped := make([]string, len(lines))
		for i, line := range lines {
			escaped[i] = html.EscapeString(line)
		}
		return "<div>" + strings.Join(escaped, "<br>") + "</div>"
	}
	return strings.Join(lines, "\n")
}

func clock(t time.Time) string { return t.UTC().Format("15:04 UTC") }

var (
	breakTag  = regexp.MustCompile(`(?i)<br\s*/?>|</(div|p|li|h[1-6])>`)
	anyTag    = regexp.MustCompile(`<[^>]*>`)
	spaceRuns = regexp.MustCompile(`\s+`)
)

// MessageText is a message reduced to what reconciliation compares: tags
// dropped (a line break is a space), entities decoded, whitespace collapsed.
// Basecamp may wrap or re-attribute rich text it stores; the words stay.
func MessageText(content string) string {
	text := breakTag.ReplaceAllString(content, " ")
	text = anyTag.ReplaceAllString(text, "")
	text = html.UnescapeString(text)
	return strings.TrimSpace(spaceRuns.ReplaceAllString(text, " "))
}

// destinationKind maps a record's reply kind to the message a comment-shaped
// notice is posted as.
func destinationKind(replyKind string) (MessageKind, bool) {
	switch replyKind {
	case "comment":
		return MessageComment, true
	case "chat_line":
		return MessageChatLine, true
	}
	return "", false
}

// LifecycleOptions tunes the hooks.
type LifecycleOptions struct {
	// GuardDelay is how long a worker has to call get_dispatch before the
	// guard acknowledges; DefaultGuardDelay when zero.
	GuardDelay time.Duration
}

// DefaultGuardDelay is the guard's wait.
const DefaultGuardDelay = 30 * time.Second

// LifecycleHooks are the ledger hooks that write the outbox's intents, each in
// its transition's transaction (invariant 1). Install them with
// Ledger.SetHooks. A connector running --shadow installs none: it posts
// nothing, and a shadow ledger promoted later must not carry intents to send.
func LifecycleHooks(l *Ledger, opts LifecycleOptions) Hooks {
	if opts.GuardDelay <= 0 {
		opts.GuardDelay = DefaultGuardDelay
	}
	return Hooks{
		VerdictCommitted: func(ctx context.Context, tx Tx, v CommittedVerdict) error {
			return verdictIntents(ctx, tx, l.now(), opts.GuardDelay, v)
		},
		AttemptEnded: func(ctx context.Context, tx Tx, s Settlement) error {
			return completionIntent(ctx, tx, l.now(), s)
		},
		StillRunning: func(ctx context.Context, tx Tx, tick StillRunningTick) error {
			return stillRunningIntent(ctx, tx, l.now(), tick)
		},
	}
}

// verdictIntents writes the guard for an admitted request and the holding
// reply for an unrouted one.
func verdictIntents(ctx context.Context, tx Tx, now time.Time, guardDelay time.Duration, v CommittedVerdict) error {
	if !v.Acknowledge {
		// Subscribed and completed are not requests: no guard, no holding
		// reply.
		return nil
	}
	var bucketID, recordingID int64
	switch err := tx.QueryRowContext(ctx, `SELECT bucket_id, recording_id FROM events WHERE id = ?`, v.EventID).Scan(&bucketID, &recordingID); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("connector: lifecycle for event %d: %w", v.EventID, ErrNoSuchRecord)
	case err != nil:
		return fmt.Errorf("connector: lifecycle for event %d: %w", v.EventID, err)
	}
	switch {
	case v.State == StateAdmitted || v.State == StateQueued:
		err := writeIntent(ctx, tx, now, newIntent{
			key:         guardKey(v.EventID),
			kind:        IntentGuardAck,
			eventID:     v.EventID,
			destination: Destination{BucketID: bucketID, Kind: MessageBoost, RecordingID: recordingID},
			body:        GuardAckBody,
			notBefore:   now.Add(guardDelay),
		})
		return err
	case v.State == StateBlocked && v.Reason == "no_route":
		kind, ok := destinationKind(v.ReplyKind)
		if !ok || v.ReplyRecordingID <= 0 {
			return nil
		}
		err := writeIntent(ctx, tx, now, newIntent{
			key:         holdingKey(v.EventID),
			kind:        IntentHoldingReply,
			eventID:     v.EventID,
			destination: Destination{BucketID: bucketID, Kind: kind, RecordingID: v.ReplyRecordingID},
			body:        renderHoldingReply(kind, v.EventID),
		})
		return err
	}
	return nil
}

// originDestination is where a task's notices go: the reply destination of
// its originating event.
func originDestination(ctx context.Context, tx Tx, taskID int64) (Destination, bool, error) {
	var (
		bucketID, replyRecordingID int64
		replyKind                  string
	)
	err := tx.QueryRowContext(ctx, `
SELECT e.bucket_id, e.reply_kind, e.reply_recording_id
FROM tasks t JOIN events e ON e.id = t.originating_event_id WHERE t.id = ?`, taskID).Scan(&bucketID, &replyKind, &replyRecordingID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Destination{}, false, nil
	case err != nil:
		return Destination{}, false, fmt.Errorf("connector: destination of task %d: %w", taskID, err)
	}
	kind, ok := destinationKind(replyKind)
	if !ok || replyRecordingID <= 0 {
		return Destination{}, false, nil
	}
	return Destination{BucketID: bucketID, Kind: kind, RecordingID: replyRecordingID}, true, nil
}

func completionIntent(ctx context.Context, tx Tx, now time.Time, s Settlement) error {
	// The notice is rendered from the rows the settlement wrote, not from the
	// Settlement handed to the hook: what is posted is what the ledger says.
	settled, err := settlementFromRecords(ctx, tx, s.AttemptID)
	if err != nil {
		return err
	}
	dest, ok, err := originDestination(ctx, tx, settled.TaskID)
	if err != nil || !ok {
		return err
	}
	// A settlement that calls for no notice renders nothing, and nothing is
	// written.
	err = writeIntent(ctx, tx, now, newIntent{
		key:         completionKey(settled.AttemptID),
		kind:        IntentCompletion,
		taskID:      settled.TaskID,
		attemptID:   settled.AttemptID,
		destination: dest,
		body:        renderCompletion(dest.Kind, settled),
	})
	return err
}

// settlementFromRecords reads an ended attempt's settlement back from the
// ledger: the attempt's stop reason, and each event's delivery, outcome,
// reply and withdrawal on its task.
func settlementFromRecords(ctx context.Context, q Tx, attemptID string) (Settlement, error) {
	s := Settlement{AttemptID: attemptID}
	var (
		stop        string
		spawnFailed bool
		originating sql.NullInt64
	)
	err := q.QueryRowContext(ctx, `
SELECT a.task_id, a.stop_reason, a.spawn_failed, t.originating_event_id
FROM attempts a JOIN tasks t ON t.id = a.task_id WHERE a.id = ? AND a.state = 'ended'`, attemptID).Scan(&s.TaskID, &stop, &spawnFailed, &originating)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Settlement{}, fmt.Errorf("connector: settlement of %s: %w", attemptID, ErrNoLiveAttempt)
	case err != nil:
		return Settlement{}, fmt.Errorf("connector: settlement of %s: %w", attemptID, err)
	}
	s.Stop, s.SpawnFailed, s.OriginatingEventID = StopReason(stop), spawnFailed, originating.Int64

	rows, err := q.QueryContext(ctx, `
SELECT te.event_id, te.delivery, te.outcome, te.reply_id, te.withdrawn_at IS NOT NULL, e.state, e.reason
FROM task_events te JOIN events e ON e.id = te.event_id
WHERE te.task_id = ? AND (te.withdrawn_at IS NULL OR te.exposed_attempt_id = ?)
ORDER BY te.event_id`, s.TaskID, attemptID)
	if err != nil {
		return Settlement{}, fmt.Errorf("connector: settlement of %s: %w", attemptID, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			e                        SettledEvent
			delivery, outcome, state string
			reason                   string
			reply                    sql.NullInt64
		)
		if err := rows.Scan(&e.EventID, &delivery, &outcome, &reply, &e.Withdrawn, &state, &reason); err != nil {
			return Settlement{}, fmt.Errorf("connector: settlement of %s: %w", attemptID, err)
		}
		switch {
		case e.Withdrawn:
			e.Blocked = RecordState(state) == StateBlocked && reason == ReasonSpawnFailed
		case Delivery(delivery) == DeliveryCompleted:
			e.Outcome = Outcome(outcome)
			e.Reported = e.Outcome != OutcomeUnknown
			if reply.Valid {
				id := reply.Int64
				e.ReplyID = &id
			}
		default:
			e.Returned = true
		}
		s.Events = append(s.Events, e)
	}
	return s, rows.Err()
}

func stillRunningIntent(ctx context.Context, tx Tx, now time.Time, tick StillRunningTick) error {
	dest, ok, err := originDestination(ctx, tx, tick.TaskID)
	if err != nil || !ok {
		return err
	}
	var launched string
	if err := tx.QueryRowContext(ctx, `SELECT launched_at FROM attempts WHERE id = ?`, tick.AttemptID).Scan(&launched); err != nil {
		return fmt.Errorf("connector: still-running for %s: %w", tick.AttemptID, err)
	}
	launchedAt, err := parseStamp(launched)
	if err != nil {
		return err
	}
	err = writeIntent(ctx, tx, now, newIntent{
		key:         stillRunningKey(tick.AttemptID, tick.Occurrence),
		kind:        IntentStillRunning,
		taskID:      tick.TaskID,
		attemptID:   tick.AttemptID,
		occurrence:  tick.Occurrence,
		destination: dest,
		body:        renderStillRunning(dest.Kind, tick.TaskID, tick.AttemptID, tick.Occurrence, launchedAt, tick.ProgressAt),
	})
	return err
}
