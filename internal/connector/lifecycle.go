package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"regexp"
	"slices"
	"sort"
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

// redispatchCommand is the only thing a lifecycle notice ever asks a person to
// do. Every notice that carries it is retractable, because a person's decision
// on the record answers it; every notice that does not — the guard
// acknowledgement, the still-running notice, a completion notice reporting an
// outcome nobody has to act on — is a record of a moment, and a record stands.
const redispatchCommand = "basecamp connect redispatch "

func redispatchAsk(eventID int64) string {
	return redispatchCommand + strconv.FormatInt(eventID, 10)
}

// redispatchAsksIn is every event a posted message asks a person to
// redispatch, in the order it names them and without repeats. One completion
// notice speaks for a whole attempt, so it can ask about several.
//
// It reads the body that went out, not the records: the records have moved on
// — that is why the message is being retracted — and what a reader is looking
// at is the words.
func redispatchAsksIn(body string) []int64 {
	text := MessageText(body)
	var (
		out  []int64
		seen map[int64]bool
	)
	for from := 0; from < len(text); {
		i := strings.Index(text[from:], redispatchCommand)
		if i < 0 {
			break
		}
		digits := from + i + len(redispatchCommand)
		end := digits
		for end < len(text) && text[end] >= '0' && text[end] <= '9' {
			end++
		}
		// The whole run of digits, so "redispatch 12" is an ask for event 12
		// and for no other.
		if id, err := strconv.ParseInt(text[digits:end], 10, 64); err == nil && !seen[id] {
			if seen == nil {
				seen = map[int64]bool{}
			}
			seen[id] = true
			out = append(out, id)
		}
		from = digits
	}
	return out
}

// asksRedispatch reports whether a posted message asks a person to redispatch
// eventID.
func asksRedispatch(body string, eventID int64) bool {
	return slices.Contains(redispatchAsksIn(body), eventID)
}

// eventList names events the way a sentence does: "event 42", "events 42 and
// 43", "events 42, 43 and 44".
func eventList(ids []int64) string {
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = strconv.FormatInt(id, 10)
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return "event " + names[0]
	}
	return "events " + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// renderHoldingReply is the reply to a mention or assignment in a project that
// has no route.
func renderHoldingReply(kind MessageKind, eventID int64) string {
	lines := []string{
		"I can't start on this here yet: this project has no working directory set up for me on the connector's machine, so nothing was run.",
		"Once the project is added to connect.json, a person can run it with: " + redispatchAsk(eventID),
		"",
		"Event " + strconv.FormatInt(eventID, 10) + " · " + lifecycleSignature,
	}
	return renderLines(kind, lines)
}

// renderRetraction answers an ask an earlier notice made. Both of the things
// it claims are checked before it is sent: that a person decided the record at
// that time, which the decisions table holds, and that the record is not
// waiting for the ask any more, which its state says at the moment the message
// goes out (askStillOpen). It does not say the work is done, which the
// connector does not know, and it does not touch the notice it answers. Nobody
// is named: the connector knows who decided only as the handle the command
// recorded, which is not a person on this card.
//
// One ask, one answer. Where two unanswered asks for the same event stand at
// one destination — a ledger upgraded from a schema that had no retractions
// carries them — each gets its own, and each names when the notice it answers
// went out (posted): two lines saying the same words under two different
// notices tell a reader nothing about which is answered, and are the same
// message to reconciliation, which would rather leave both for a person than
// guess which is which.
func renderRetraction(kind MessageKind, eventID int64, others []int64, postedAt time.Time, action DecisionAction, at time.Time) string {
	id := strconv.FormatInt(eventID, 10)
	answer := "A person ran it at " + clock(at) + ", and event " + id + " is not waiting for it now."
	if action == DecisionDiscard {
		answer = "A person closed event " + id + " instead, at " + clock(at) + ", and it is not waiting for anything now."
	}
	notice := "an earlier notice here"
	if !postedAt.IsZero() {
		notice = "an earlier notice here, posted at " + clock(postedAt) + ","
	}
	// A completion notice speaks for a whole attempt and can ask about several
	// events. This answers one of them, and says so: a closing line that spoke
	// for the notice would dismiss asks nobody has acted on, which is the
	// mistake this whole change is about.
	stands := "The earlier notice stands as a record of when it was written; nothing in it needs doing."
	if len(others) > 0 {
		stands = "The earlier notice stands as a record of when it was written. It also asks about " +
			eventList(others) + "; this answers only event " + id + "."
	}
	lines := []string{
		"Event " + id + ": " + notice + " asked a person to run " + redispatchAsk(eventID) + ". " + answer,
		stands,
		"",
		"Event " + id + " · " + lifecycleSignature,
	}
	return renderLines(kind, lines)
}

// askStillOpen reports whether a record is still in the state the notice
// described — still waiting for the very thing that notice asked a person to
// do — and whether the record is there to ask about at all.
//
// It is the difference between a decision and an answer. A person's redispatch
// of a blocked record authorizes it and hands its prerequisite back to the
// caller to run again; if that run does not settle the blocking condition the
// record is still blocked, the operator is still being told to redispatch, and
// a message saying the ask is answered would stop the next reader acting on a
// notice that is still live. Silence is better than that, so the retraction
// waits for the record to move.
//
// The question is asked of the notice that made the ask, not of the record in
// general, because "waiting" is not one state. A holding reply answers one
// blocked reason, as its own claim does (holdingReplyReason): a record whose
// rerun replaced no_route with read_failed or throttled is not waiting on a
// person at all — those reasons come round again on their own — so that ask
// is answered and the record moving between them is the answer. A completion
// notice asks about an outcome nobody has decided, and only the latest live
// outcome is that record's, as loadEventTask reads it: a retired row from a
// task that has since been superseded and rerun says nothing about now.
func askStillOpen(ctx context.Context, q Tx, source Intent, eventID int64) (open, found bool, err error) {
	var query string
	args := []any{eventID}
	switch source.Kind {
	case IntentHoldingReply:
		query = `SELECT state = 'blocked' AND reason = ?2 FROM events WHERE id = ?1`
		args = append(args, holdingReplyReason(source))
	case IntentCompletion:
		query = `
SELECT e.state = 'completed' AND e.redispatch_decision IS NULL
   AND (SELECT te.outcome FROM task_events te
        WHERE te.event_id = e.id AND te.withdrawn_at IS NULL
        ORDER BY te.task_id DESC LIMIT 1) IN ('unknown', 'failed')
FROM events e WHERE e.id = ?1`
	default:
		// No other kind carries an ask, so no retraction is written against
		// one. Left waiting rather than sent, which is the direction that
		// cannot put a wrong answer on a card.
		return true, true, nil
	}
	switch err := q.QueryRowContext(ctx, query, args...).Scan(&open); {
	case errors.Is(err, sql.ErrNoRows):
		return false, false, nil
	case err != nil:
		return false, false, fmt.Errorf("connector: read what event %d is waiting for: %w", eventID, err)
	}
	return open, true, nil
}

// renderStillRunning is one still-running notice. It is dated rather than
// written in the present tense, because it can be the last thing the
// connector says on a task: an attempt whose events all succeeded with a
// reply calls for no completion notice, so nothing follows to correct a
// sentence that claims the work is under way now. "Working on this as of
// 12:10 UTC" is a record of a moment, like every other lifecycle message,
// and stays true after the task ends.
//
// The stamp is the connector's own — when it saw the attempt live — and not
// the worker's last progress, which is a different fact the notice reports
// separately: a task can be alive and quiet for an hour.
func renderStillRunning(kind MessageKind, taskID int64, attemptID string, occurrence int, at, launchedAt, progressAt time.Time) string {
	progress := "No progress has been reported yet."
	if !progressAt.IsZero() {
		progress = "Last progress at " + clock(progressAt) + "."
	}
	lines := []string{
		"Working on this as of " + clock(at) + ": task " + strconv.FormatInt(taskID, 10) + " started at " + clock(launchedAt) + ". " + progress,
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
	redispatch := " Needs a person: " + redispatchAsk(e.EventID)
	if e.Decided {
		// A person already redispatched or discarded it: the notice says what
		// happened, and asks for nothing.
		redispatch = ""
	}
	switch {
	case e.Blocked:
		return "Event " + id + ": the worker could not be started." + redispatch
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
		RecordDecided: func(ctx context.Context, tx Tx, d RecordDecision) error {
			return retractionIntents(ctx, tx, l.now(), d)
		},
	}
}

// retractionIntents writes a retraction for every posted notice that asked for
// the decision a person has just made.
//
// Only a message that went out, or is on its way out, is retracted: a pending
// one stands down at its claim, which is the pre-send half of this and is
// cheaper, and one canceled, indeterminate or abandoned may never have
// reached the destination at all. A message still sending gets a retraction
// written now and held at its claim until the message it answers is known to
// exist.
//
// Asks for one event can stand at two destinations — a holding reply goes to
// the record's own reply, an attempt's completion notice to the originating
// record's — and, on a ledger upgraded from a schema that had no retractions,
// several can stand at one: nothing answered the older ones as they were
// overtaken. Every ask nothing has answered yet is retracted, which is what
// the key says (invariant 2: one retraction per posted message and the event
// whose ask it answers), and an ask that already has its answer is left alone,
// so deciding an event again writes nothing new.
func retractionIntents(ctx context.Context, tx Tx, now time.Time, d RecordDecision) error {
	asked, err := postedAsks(ctx, tx, d.EventID)
	if err != nil {
		return err
	}
	for _, p := range asked {
		if err := writeIntent(ctx, tx, now, newIntent{
			key:         retractionKey(p.id, d.EventID),
			kind:        IntentRetraction,
			eventID:     d.EventID,
			retracts:    p.id,
			destination: p.dest,
			body:        renderRetraction(p.dest.Kind, d.EventID, p.others, p.postedAt, d.Action, d.At),
		}); err != nil {
			return err
		}
	}
	return nil
}

// postedAsk is one message the connector posted that asks for an event's
// redispatch, with the other events it asks about — none, for a holding
// reply; the rest of the attempt, for a completion notice.
//
// postedAt is when that message went out, and is set only when another
// unanswered ask for the same event stands at the same destination: the
// retraction names it then, so each of the two answers one notice and says
// which.
type postedAsk struct {
	id       int64
	dest     Destination
	others   []int64
	postedAt time.Time
}

// postedAsks reads every ask for an event that nothing has answered yet, and
// closes the cursor before its caller writes: one statement at a time on a
// transaction.
func postedAsks(ctx context.Context, tx Tx, eventID int64) ([]postedAsk, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT o.id, o.bucket_id, o.message_kind, o.recording_id, o.body, COALESCE(o.sending_at, o.created_at)
FROM outbox o
WHERE o.state IN ('sent', 'sending')
  AND NOT EXISTS (SELECT 1 FROM outbox r WHERE r.retracts = o.id AND r.event_id = ?1)
  AND (
     (o.kind = 'holding_reply' AND o.event_id = ?1)
  OR (o.kind = 'completion' AND o.attempt_id IN (
        SELECT a.id FROM attempts a JOIN task_events te ON te.task_id = a.task_id WHERE te.event_id = ?1)))
ORDER BY o.id`, eventID)
	if err != nil {
		return nil, fmt.Errorf("connector: read the posted asks for event %d: %w", eventID, err)
	}
	defer func() { _ = rows.Close() }()
	standing := map[Destination][]postedAsk{}
	for rows.Next() {
		var (
			p                  postedAsk
			kind, body, posted string
		)
		if err := rows.Scan(&p.id, &p.dest.BucketID, &kind, &p.dest.RecordingID, &body, &posted); err != nil {
			return nil, fmt.Errorf("connector: read the posted asks for event %d: %w", eventID, err)
		}
		p.dest.Kind = MessageKind(kind)
		asked := redispatchAsksIn(body)
		if !slices.Contains(asked, eventID) {
			continue
		}
		for _, id := range asked {
			if id != eventID {
				p.others = append(p.others, id)
			}
		}
		// An unreadable stamp is no reason not to answer the ask: the
		// retraction goes out without naming the hour.
		if at, err := parseStamp(posted); err == nil {
			p.postedAt = at
		}
		standing[p.dest] = append(standing[p.dest], p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("connector: read the posted asks for event %d: %w", eventID, err)
	}
	out := make([]postedAsk, 0, len(standing))
	for _, at := range standing {
		for _, p := range at {
			if len(at) == 1 {
				p.postedAt = time.Time{}
			}
			out = append(out, p)
		}
	}
	// In id order, so two retractions written at once are written in the order
	// the messages they answer went out.
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out, nil
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

	// Decided is a person's decision this settlement's notice would otherwise
	// ask for: the record left the state the notice describes, a redispatch
	// waits on it, or an authorization was made strictly after this attempt
	// ended. An authorization from before — a redispatch that led to this
	// attempt — answered for an earlier outcome, not this one, and a stamp
	// equal to the attempt's end is not evidence that it came after: the
	// notice asks again, which is the safe direction.
	rows, err := q.QueryContext(ctx, `
SELECT te.event_id, te.delivery, te.outcome, te.reply_id, te.withdrawn_at IS NOT NULL, e.state, e.reason,
       e.state NOT IN ('completed', 'blocked') OR e.redispatch_decision IS NOT NULL
       OR COALESCE(e.authorized_at > (SELECT ended_at FROM attempts WHERE id = ?2), 0)
FROM task_events te JOIN events e ON e.id = te.event_id
WHERE te.task_id = ?1 AND (te.withdrawn_at IS NULL OR te.exposed_attempt_id = ?2)
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
		if err := rows.Scan(&e.EventID, &delivery, &outcome, &reply, &e.Withdrawn, &state, &reason, &e.Decided); err != nil {
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
		body:        renderStillRunning(dest.Kind, tick.TaskID, tick.AttemptID, tick.Occurrence, now, launchedAt, tick.ProgressAt),
	})
	return err
}
