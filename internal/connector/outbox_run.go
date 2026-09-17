package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/ndjson"
)

// Poster is how the outbox reaches Basecamp, as the agent.
type Poster interface {
	// Post creates one message and returns its id. It makes at most one
	// request: a retry is a second message.
	Post(ctx context.Context, dest Destination, body string) (int64, error)
	// List returns every message of dest.Kind the agent created at dest since
	// since, exhaustively: a listing that could not reach back that far is an
	// error, never a shorter answer.
	List(ctx context.Context, dest Destination, since time.Time) ([]PostedMessage, error)
}

// PostedMessage is one of the agent's messages at a destination.
type PostedMessage struct {
	ID        int64
	CreatedAt time.Time
	Content   string
}

// Outbox defaults.
const (
	DefaultOutboxTick = time.Second
	// DefaultReconcileAfter is how long a sending intent this process is not
	// sending is left before it is reconciled: long enough for a request that
	// failed on the wire to have landed, if it was going to.
	DefaultReconcileAfter = time.Minute
	// DefaultReconcileSlack widens a reconciliation listing back past the
	// sending time, for clock skew between this machine and Basecamp.
	DefaultReconcileSlack = 2 * time.Minute
	// DefaultPostTimeout bounds one request.
	DefaultPostTimeout = time.Minute
)

// OutboxOptions configures the outbox's sender.
type OutboxOptions struct {
	Ledger *Ledger
	Poster Poster
	// Paused, when set and true, holds sending (the hold marker). Reconciling
	// what was already sent goes on, since it only reads and adopts.
	Paused func(ctx context.Context) (bool, error)

	Lines  *ndjson.Writer
	Logger *slog.Logger

	Tick           time.Duration
	ReconcileAfter time.Duration
	ReconcileSlack time.Duration
	PostTimeout    time.Duration
}

// Outbox sends lifecycle intents and reconciles the ones a request left
// uncertain. One Outbox per ledger.
type Outbox struct {
	opts   OutboxOptions
	ledger *Ledger
	log    *slog.Logger

	// mu serializes sending and reconciling, so an intent this process is
	// sending is never reconciled under it.
	mu sync.Mutex
}

// OutboxLine is the stdout line for an intent's transitions: ids and states,
// never a body.
type OutboxLine struct {
	Type      string `json:"type"`
	IntentID  int64  `json:"intent_id"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	EventID   int64  `json:"event_id,omitempty"`
	AttemptID string `json:"attempt_id,omitempty"`
	ReceiptID int64  `json:"receipt_id,omitempty"`
}

// NewOutbox builds the sender.
func NewOutbox(opts OutboxOptions) (*Outbox, error) {
	switch {
	case opts.Ledger == nil:
		return nil, errors.New("connector: the outbox needs the ledger")
	case opts.Poster == nil:
		return nil, errors.New("connector: the outbox needs a poster")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Tick <= 0 {
		opts.Tick = DefaultOutboxTick
	}
	if opts.ReconcileAfter <= 0 {
		opts.ReconcileAfter = DefaultReconcileAfter
	}
	if opts.ReconcileSlack <= 0 {
		opts.ReconcileSlack = DefaultReconcileSlack
	}
	if opts.PostTimeout <= 0 {
		opts.PostTimeout = DefaultPostTimeout
	}
	return &Outbox{opts: opts, ledger: opts.Ledger, log: opts.Logger}, nil
}

// Run reconciles every intent a previous process left sending, then sends due
// intents and reconciles stale sending ones until ctx ends. It does not flush
// on the way out: call Flush once whatever settles attempts on shutdown is
// done, so their completion notices go out.
func (o *Outbox) Run(ctx context.Context) error {
	if err := o.Recover(ctx); err != nil && ctx.Err() == nil {
		o.log.Warn("connector: outbox recovery", "error", err)
	}
	ticker := time.NewTicker(o.opts.Tick)
	defer ticker.Stop()
	for {
		if err := o.Flush(ctx); err != nil && ctx.Err() == nil {
			o.log.Warn("connector: outbox", "error", err)
		}
		if _, err := o.reconcileStale(ctx, o.opts.ReconcileAfter); err != nil && ctx.Err() == nil {
			o.log.Warn("connector: outbox reconciliation", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Recover reconciles every sending intent, whatever its age. On start every
// one of them is a previous process's.
func (o *Outbox) Recover(ctx context.Context) error {
	_, err := o.reconcileStale(ctx, 0)
	return err
}

// Flush sends every intent that is due, one at a time, and returns when none
// is left or ctx ends. One flush claims an intent at most once: a claim that
// came back for an intent already claimed would be a second send, and stops
// the flush instead.
func (o *Outbox) Flush(ctx context.Context) error {
	claimed := map[int64]bool{}
	for ctx.Err() == nil {
		if o.opts.Paused != nil {
			paused, err := o.opts.Paused(ctx)
			if err != nil {
				return err
			}
			if paused {
				return nil
			}
		}
		id, err := o.sendNext(ctx, claimed)
		if err != nil {
			return err
		}
		if id == 0 {
			return nil
		}
	}
	return nil
}

// sendNext claims the oldest due intent and sends it. It returns the id it
// claimed, zero when none was due.
func (o *Outbox) sendNext(ctx context.Context, claimed map[int64]bool) (int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	intent, ok, err := o.ledger.claimIntent(ctx)
	if err != nil || !ok {
		return 0, err
	}
	if claimed[intent.ID] {
		return 0, fmt.Errorf("connector: outbox intent %d was claimed twice in one flush; not sending it again", intent.ID)
	}
	claimed[intent.ID] = true
	o.line(intent)
	if intent.State != IntentSending {
		// Claiming canceled it.
		return intent.ID, nil
	}

	// Invariant 3: the sending row is committed; only now is a request made.
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), o.opts.PostTimeout)
	receipt, postErr := o.opts.Poster.Post(postCtx, intent.Destination, intent.Body)
	cancel()
	if postErr != nil {
		// The request may have reached Basecamp. The intent stays sending and
		// is reconciled once it has had time to land; it is never posted
		// again (invariant 4).
		o.log.Warn("connector: a lifecycle message may not have been posted; it will be reconciled, not resent",
			"intent_id", intent.ID, "kind", string(intent.Kind), "error", postErr)
		return intent.ID, nil
	}
	if receipt <= 0 {
		o.log.Warn("connector: a lifecycle message was posted without an id; it will be reconciled", "intent_id", intent.ID)
		return intent.ID, nil
	}
	recorded, err := o.ledger.recordReceipt(context.WithoutCancel(ctx), intent.ID, receipt)
	if err != nil {
		// The message exists; reconciliation finds it by its body.
		o.log.Warn("connector: could not record a lifecycle message's receipt; it will be reconciled", "intent_id", intent.ID, "error", err)
		return intent.ID, nil
	}
	o.line(recorded)
	return intent.ID, nil
}

// claimIntent moves the oldest due pending intent to sending and commits, or,
// for a guard that no longer applies, to canceled. It is the only way to
// sending.
func (l *Ledger) claimIntent(ctx context.Context) (Intent, bool, error) {
	var (
		out Intent
		ok  bool
	)
	err := retryBusy(func() error {
		tx, err := l.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("connector: begin outbox claim: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		now := l.timestamp()
		rows, err := tx.QueryContext(ctx, selectIntents+` WHERE state = 'pending' AND not_before <= ? ORDER BY not_before, id LIMIT 1`, now)
		if err != nil {
			return fmt.Errorf("connector: outbox claim: %w", err)
		}
		intents, err := scanIntents(rows)
		if err != nil {
			return err
		}
		if len(intents) == 0 {
			ok = false
			return nil
		}
		in := intents[0]

		next, note := IntentSending, ""
		if in.Kind == IntentGuardAck {
			var stillCalledFor bool
			if err := tx.QueryRowContext(ctx, `
SELECT e.acknowledge = 1 AND e.state IN ('admitted', 'queued', 'dispatched')
   AND NOT EXISTS (SELECT 1 FROM task_events te
                   WHERE te.event_id = e.id AND (te.guard = 'canceled' OR te.delivery IN ('delivered', 'completed')))
FROM events e WHERE e.id = ?`, in.EventID).Scan(&stillCalledFor); err != nil {
				return fmt.Errorf("connector: outbox claim guard %d: %w", in.ID, err)
			}
			if !stillCalledFor {
				next, note = IntentCanceled, "no longer called for"
			} else if _, err := tx.ExecContext(ctx, `UPDATE task_events SET guard = 'fired' WHERE event_id = ? AND guard = 'armed'`, in.EventID); err != nil {
				return fmt.Errorf("connector: outbox claim guard %d: %w", in.ID, err)
			}
		}
		if next == IntentSending {
			_, err = tx.ExecContext(ctx, `UPDATE outbox SET state = 'sending', sending_at = ? WHERE id = ? AND state = 'pending'`, now, in.ID)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE outbox SET state = 'canceled', finished_at = ?, note = ? WHERE id = ? AND state = 'pending'`, now, note, in.ID)
		}
		if err != nil {
			return fmt.Errorf("connector: outbox claim %d: %w", in.ID, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("connector: commit outbox claim %d: %w", in.ID, err)
		}
		in.State, in.Note = next, note
		if next == IntentSending {
			t, _ := parseStamp(now)
			in.SendingAt = &t
		}
		out, ok = in, true
		return nil
	})
	return out, ok, err
}

// recordReceipt moves a sending intent to sent with its receipt.
func (l *Ledger) recordReceipt(ctx context.Context, id, receipt int64) (Intent, error) {
	err := retryBusy(func() error {
		res, err := l.db.ExecContext(ctx, `UPDATE outbox SET state = 'sent', receipt_id = ?, finished_at = ? WHERE id = ? AND state = 'sending'`,
			receipt, l.timestamp(), id)
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("connector: receipt %d for intent %d: %w", receipt, id, ErrReceiptOwned)
			}
			return fmt.Errorf("connector: receipt for intent %d: %w", id, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("connector: receipt for intent %d: it is not sending", id)
		}
		return nil
	})
	if err != nil {
		return Intent{}, err
	}
	return l.Intent(ctx, id)
}

// reconcileStale reconciles every sending intent whose sending time is at
// least age ago. It returns how many it settled.
func (o *Outbox) reconcileStale(ctx context.Context, age time.Duration) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	intents, err := o.ledger.Intents(ctx, IntentFilter{States: []IntentState{IntentSending}})
	if err != nil {
		return 0, err
	}
	cutoff := o.ledger.now().Add(-age)
	settled := 0
	var firstErr error
	for i := len(intents) - 1; i >= 0; i-- {
		in := intents[i]
		if in.SendingAt != nil && in.SendingAt.After(cutoff) {
			continue
		}
		done, err := o.reconcile(ctx, in)
		if err != nil {
			o.log.Warn("connector: reconciling a lifecycle message", "intent_id", in.ID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if done {
			settled++
		}
	}
	return settled, firstErr
}

// reconcile settles one sending intent by listing its destination (invariant
// 5). A listing that fails leaves it sending, to try again; a listing that
// answers settles it as sent or indeterminate.
func (o *Outbox) reconcile(ctx context.Context, in Intent) (bool, error) {
	since := in.CreatedAt
	if in.SendingAt != nil {
		since = *in.SendingAt
	}
	since = since.Add(-o.opts.ReconcileSlack)
	listed, err := o.opts.Poster.List(ctx, in.Destination, since)
	if err != nil {
		return false, err
	}
	candidate, note, err := o.ledger.adoptable(ctx, in, listed)
	if err != nil {
		return false, err
	}
	updated, err := o.ledger.settleReconciled(ctx, in.ID, candidate, note)
	if err != nil {
		return false, err
	}
	o.line(updated)
	return true, nil
}

// adoptable picks the one message a sending intent may adopt, or says why
// there is none.
func (l *Ledger) adoptable(ctx context.Context, in Intent, listed []PostedMessage) (int64, string, error) {
	want := MessageText(in.Body)
	var matches []int64
	for _, m := range listed {
		if MessageText(m.Content) != want {
			continue
		}
		owned, err := l.receiptOwnedByOther(ctx, in.ID, in.Destination.Kind, m.ID)
		if err != nil {
			return 0, "", err
		}
		if !owned {
			matches = append(matches, m.ID)
		}
	}
	if len(matches) != 1 {
		return 0, strconv.Itoa(len(matches)) + " matching messages at the destination", nil
	}
	rivals, err := l.Intents(ctx, IntentFilter{States: []IntentState{IntentPending, IntentSending, IntentIndeterminate}})
	if err != nil {
		return 0, "", err
	}
	for _, r := range rivals {
		if r.ID != in.ID && r.Destination.Kind == in.Destination.Kind && r.Destination.RecordingID == in.Destination.RecordingID &&
			MessageText(r.Body) == want {
			return 0, "intent " + strconv.FormatInt(r.ID, 10) + " could claim the same message", nil
		}
	}
	return matches[0], "", nil
}

func (l *Ledger) receiptOwnedByOther(ctx context.Context, id int64, kind MessageKind, receipt int64) (bool, error) {
	var owned bool
	err := l.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM outbox WHERE message_kind = ? AND receipt_id = ? AND id <> ?)`,
		string(kind), receipt, id).Scan(&owned)
	return owned, err
}

// settleReconciled writes a reconciliation's answer onto a still-sending
// intent: sent with the adopted receipt, or indeterminate with why.
func (l *Ledger) settleReconciled(ctx context.Context, id, receipt int64, note string) (Intent, error) {
	err := retryBusy(func() error {
		var (
			res sql.Result
			err error
		)
		now := l.timestamp()
		if receipt > 0 {
			res, err = l.db.ExecContext(ctx, `UPDATE outbox SET state = 'sent', receipt_id = ?, finished_at = ?, note = 'adopted by reconciliation' WHERE id = ? AND state = 'sending'`,
				receipt, now, id)
		} else {
			res, err = l.db.ExecContext(ctx, `UPDATE outbox SET state = 'indeterminate', finished_at = ?, note = ? WHERE id = ? AND state = 'sending'`,
				now, note, id)
		}
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("connector: reconcile intent %d: %w", id, ErrReceiptOwned)
			}
			return fmt.Errorf("connector: reconcile intent %d: %w", id, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("connector: reconcile intent %d: it is no longer sending", id)
		}
		return nil
	})
	if err != nil {
		return Intent{}, err
	}
	return l.Intent(ctx, id)
}

// IsLifecycleMessage says whether a comment or chat line id is one of the
// connector's own lifecycle messages, for the adopted-reply rule. An error
// answers yes: a reply is not adopted on a guess.
func (o *Outbox) IsLifecycleMessage(id int64) bool {
	return IsLifecycleMessageIn(o.ledger)(id)
}

// IsLifecycleMessageIn is IsLifecycleMessage over a ledger, for a dispatcher
// built without a sender.
func IsLifecycleMessageIn(l *Ledger) func(id int64) bool {
	return func(id int64) bool {
		ctx := context.Background()
		for _, kind := range []MessageKind{MessageComment, MessageChatLine} {
			found, err := l.IsLifecycleReceipt(ctx, kind, id)
			if err != nil || found {
				return true
			}
		}
		return false
	}
}

func (o *Outbox) line(in Intent) {
	if o.opts.Lines == nil {
		return
	}
	line := OutboxLine{Type: "outbox", IntentID: in.ID, Kind: string(in.Kind), State: string(in.State), EventID: in.EventID, AttemptID: in.AttemptID}
	if in.ReceiptID != nil {
		line.ReceiptID = *in.ReceiptID
	}
	if err := o.opts.Lines.WriteLine(line); err != nil {
		o.log.Warn("connector: outbox line", "error", err)
	}
}
