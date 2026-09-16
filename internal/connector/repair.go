package connector

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// repairWalker reconciles one accepted buffer overflow.
//
// It repairs with EXPLICIT IDS and never with the feed's position. The
// position is an opaque signed token, so nothing can be compared to it; and
// the overflow's ids are live ids, far ahead of the poll lane's safety delay,
// so a checkpoint taken from one would skip everything behind it that the
// delay had not yet served. The walk therefore runs on its own cursor, kept on
// the loss record, and the feed's checkpoint is never written from it.
//
// It runs off the delivery path. Nothing about live intake waits for it.
type repairWalker struct {
	ledger   *Ledger
	polls    eventfeed.PollSource
	filters  eventfeed.Filters
	ingest   func(ctx context.Context, event eventfeed.Event, lane Lane) error
	now      func() time.Time
	interval time.Duration
	log      *slog.Logger

	// sleep is the wait between repair polls; nil means time.After. Injected
	// so a test can run ten minutes of repair polls without taking ten
	// minutes.
	sleep func(ctx context.Context, d time.Duration) error
}

func (w *repairWalker) reconcile(ctx context.Context, loss Loss) error {
	for {
		done, err := w.settled(ctx, loss)
		if err != nil || done {
			return err
		}

		cursor, err := w.walk(ctx, &loss)
		if errors.Is(err, errReconciliationEnded) {
			return nil
		}
		if err != nil {
			return err
		}
		loss.RepairCursor = cursor

		// Re-read before waiting: a walk that served everything has nothing
		// left to wait for.
		done, err = w.settled(ctx, loss)
		if err != nil || done {
			return err
		}

		if !w.now().Before(loss.DeadlineAt) {
			// The window is closed. What is still missing is a late
			// straggler, a recording deleted before it became poll-visible,
			// or history behind the epoch. It is recorded as unrecovered and
			// shown by status — the poll lane may still serve it later, and
			// intake resolves it then like any other event.
			unrecovered, err := w.ledger.CloseLoss(ctx, loss.ID, w.now())
			if err != nil {
				return err
			}
			if unrecovered > 0 {
				w.log.Error("a buffer overflow left events unrecovered", "loss_id", loss.ID, "unrecovered", unrecovered)
			}
			return nil
		}

		// A missing id the walk did not serve is NOT yet a gap: `next` can end
		// while an event is still inside the poll lane's safety delay. The
		// walk repeats on the repair cadence until the window closes.
		if err := w.wait(ctx, w.interval); err != nil {
			return err
		}
	}
}

// settled closes the loss and reports true when nothing is missing any more.
func (w *repairWalker) settled(ctx context.Context, loss Loss) (bool, error) {
	missing, err := w.ledger.MissingIDs(ctx, loss.ID, LossMissing)
	if err != nil {
		return false, err
	}
	if len(missing) > 0 {
		return false, nil
	}
	if _, err := w.ledger.CloseLoss(ctx, loss.ID, w.now()); err != nil {
		return false, err
	}
	// "Nothing missing" is not "everything recovered": an epoch can have
	// fenced some ids off as unrecovered already. Say which it was.
	unrecovered, err := w.ledger.MissingIDs(ctx, loss.ID, LossUnrecovered)
	if err != nil {
		return false, err
	}
	if len(unrecovered) > 0 {
		w.log.Warn("a buffer overflow's reconciliation ended with events unrecovered", "loss_id", loss.ID, "unrecovered", len(unrecovered))
	} else {
		w.log.Info("a buffer overflow was fully reconciled", "loss_id", loss.ID)
	}
	return true, nil
}

// walk runs one pass from the loss's own cursor to the frozen head, following
// `next` until it is absent. It returns the cursor to resume the next pass at.
func (w *repairWalker) walk(ctx context.Context, loss *Loss) (string, error) {
	cursor := eventfeed.Cursor{Since: strconv.FormatInt(loss.RepairSince, 10)}
	if loss.RepairCursor != "" {
		cursor = eventfeed.Cursor{Position: loss.RepairCursor}
	}

	last := loss.RepairCursor
	pass := &repairPass{followed: map[string]bool{}}
	for {
		page, err := w.polls.Poll(ctx, cursor, w.filters)
		if err != nil {
			next, err := w.pollFailure(ctx, loss, cursor, err, pass)
			if err != nil || next == nil {
				return last, err
			}
			cursor = *next
			if cursor.PageURL == "" && cursor.Position == "" {
				last = ""
			}
			continue
		}

		for _, event := range page.Events {
			// Through intake like any other event: the ledger's dedupe is
			// what marks the missing id recovered, and it is one code path
			// rather than two that must agree.
			if err := w.ingest(ctx, event, LaneRepair); err != nil {
				return last, err
			}
		}

		if page.Position != "" {
			last = page.Position
			if err := w.ledger.SaveRepairCursor(ctx, loss.ID, page.Position); err != nil {
				return last, err
			}
		}

		if page.Next == "" {
			// The walk reached its frozen head. That is NOT "caught up": a
			// page cut short by the safety horizon withholds the link on
			// purpose, so the caller polls again rather than concluding
			// anything.
			return last, nil
		}
		// An empty page with a `next` is ordinary — the walk crossed rows the
		// filters excluded — so the loop never stops on len(Events) == 0.
		cursor = eventfeed.Cursor{PageURL: page.Next}
	}
}

// errReconciliationEnded reports that the walk itself settled the loss's fate
// for this start — closed it, or left it open for the next — so the caller
// stops rather than looping back to find nothing missing and calling that a
// full recovery.
var errReconciliationEnded = errors.New("connector: reconciliation ended inside the repair walk")

// maxResumesPerPass bounds the 410 resumes one pass follows. Keyed by URL
// alone, a server that signs or nonces its resume URLs would make every answer
// look new; the bound does not depend on the server choosing stable URLs.
const maxResumesPerPass = 2

// repairPass is what one pass has already done, stated rather than inferred
// from the shape of the cursor.
type repairPass struct {
	// followed holds the resume URLs this pass has already followed. A 410 on
	// one of them is the same 410 again.
	followed map[string]bool
	// reentered is whether this pass has already dropped a refused cursor
	// for the explicit id.
	reentered bool
}

// failureKind is the only rendering of a failed poll the walk logs. A poll's
// error text can carry its request URL — a position token or a
// server-supplied continuation — and the walk logs at the operator's terminal.
func failureKind(err error) string {
	var pollErr *eventfeed.PollError
	if errors.As(err, &pollErr) {
		return pollErr.Kind.String()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	return "unclassified"
}

// pollFailure decides what one failed repair poll means. It returns the cursor
// to continue this pass at, nil with no error to end the pass and wait for the
// next repair poll, or an error.
func (w *repairWalker) pollFailure(ctx context.Context, loss *Loss, cursor eventfeed.Cursor, err error, pass *repairPass) (*eventfeed.Cursor, error) {
	var retention *InboxRetentionGoneError
	if errors.As(err, &retention) {
		// The inbox lane's 410 on the account lane: the same foreign loss the
		// feed surfaces, recorded as its own class and never resumed as if it
		// were the epoch's. Nothing on this lane will serve the ids.
		if _, recordErr := w.ledger.RecordGap(ctx, Gap{
			DetectedAt: w.now(),
			Class:      GapRetention,
			EntryClass: EntryUnknown,
			Note:       "a retention 410 was served to the repair walk on the account lane; not resumed",
		}); recordErr != nil {
			return nil, recordErr
		}
		unrecovered, closeErr := w.ledger.CloseLoss(ctx, loss.ID, w.now())
		if closeErr != nil {
			return nil, closeErr
		}
		w.log.Error("the repair walk was answered with the inbox lane's 410; not resumed",
			"loss_id", loss.ID, "unrecovered", unrecovered)
		return nil, errReconciliationEnded
	}

	var pollErr *eventfeed.PollError
	if !errors.As(err, &pollErr) {
		w.log.Warn("a repair poll failed; retrying on the repair cadence", "loss_id", loss.ID, "failure", failureKind(err))
		return nil, nil
	}

	switch pollErr.Kind {
	case eventfeed.PollGone:
		epoch := pollErr.EpochAfterID
		if pass.followed[pollErr.ResumeURL] || len(pass.followed) >= maxResumesPerPass {
			// This pass already followed this resume, and it answered 410
			// again. Following it again would loop; the next pass tries.
			w.log.Warn("the repair walk's resume answered 410 again; retrying on the repair cadence", "loss_id", loss.ID)
			return nil, nil
		}
		if epoch != loss.RepairSince {
			// A fence this loss has not seen: history up to the epoch is
			// gone, and the ids there with it.
			if _, recordErr := w.ledger.RecordGap(ctx, Gap{
				DetectedAt:   w.now(),
				Class:        GapEpoch,
				EpochAfterID: &epoch,
				EntryClass:   entryClassOf(pollErr.ResumeURL),
				Note:         "the repair walk was seeded below the feed's epoch",
			}); recordErr != nil {
				return nil, recordErr
			}
			behind, markErr := w.ledger.MarkUnrecoveredThrough(ctx, loss.ID, epoch)
			if markErr != nil {
				return nil, markErr
			}
			w.log.Error("the repair walk fell below the feed's epoch; the dropped events behind it are gone",
				"loss_id", loss.ID, "epoch_after_id", epoch, "unrecovered", behind)
			// Later passes start at the fence.
			loss.RepairSince = epoch
			loss.RepairCursor = ""
			if setErr := w.ledger.SetRepairEntry(ctx, loss.ID, epoch); setErr != nil {
				return nil, setErr
			}
		}
		stillMissing, missErr := w.ledger.MissingIDs(ctx, loss.ID, LossMissing)
		if missErr != nil {
			return nil, missErr
		}
		if len(stillMissing) == 0 || pollErr.ResumeURL == "" {
			if _, closeErr := w.ledger.CloseLoss(ctx, loss.ID, w.now()); closeErr != nil {
				return nil, closeErr
			}
			return nil, errReconciliationEnded
		}
		// Ids above the epoch are still servable: the resume is followed as
		// served, whether or not this loss had met the fence before.
		pass.followed[pollErr.ResumeURL] = true
		return &eventfeed.Cursor{PageURL: pollErr.ResumeURL}, nil

	case eventfeed.PollFilterChanged, eventfeed.PollPositionInvalid:
		// The walk's own cursor was refused — minted under another filter
		// set, or no longer honored. Its explicit id is still good. Once per
		// pass: a refusal of the explicit id too is left to the cadence.
		if pass.reentered || (cursor.Position == "" && cursor.PageURL == "") {
			w.log.Warn("a repair poll was refused; retrying on the repair cadence", "loss_id", loss.ID, "failure", failureKind(err))
			return nil, nil
		}
		pass.reentered = true
		loss.RepairCursor = ""
		if saveErr := w.ledger.SaveRepairCursor(ctx, loss.ID, ""); saveErr != nil {
			return nil, saveErr
		}
		return &eventfeed.Cursor{Since: strconv.FormatInt(loss.RepairSince, 10)}, nil

	case eventfeed.PollTransient, eventfeed.PollThrottled, eventfeed.PollUnauthorized:
		// A reason to try again on the next repair poll, not a reason to call
		// the ids unrecovered. A slow or throttled walk delays nothing else.
		w.log.Warn("a repair poll failed; retrying on the repair cadence", "loss_id", loss.ID, "failure", failureKind(err))
		return nil, nil

	case eventfeed.PollFilterInvalid, eventfeed.PollRedirectRefused, eventfeed.PollUnrecoverable:
	}

	// Refused redirects, invalid filters, undifferentiated 400s, anything
	// unrecoverable: no repeat inside this window will change the answer.
	// The loss stays open, so the next start — perhaps after the cause is
	// fixed — tries again instead of finding the ids already condemned.
	w.log.Error("a repair poll failed in a way retrying will not fix; the loss stays open for the next start",
		"loss_id", loss.ID, "failure", failureKind(err))
	return nil, errReconciliationEnded
}

func (w *repairWalker) wait(ctx context.Context, d time.Duration) error {
	if w.sleep != nil {
		return w.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
