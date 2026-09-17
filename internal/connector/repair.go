package connector

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
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
	ledger *Ledger
	polls  eventfeed.PollSource
	// maxPages caps the pages one pass walks; zero means maxRepairPagesPerPass.
	maxPages int
	// retryAfter is a server-directed wait the last pass was given. It is
	// honored exactly and is exempt from the repair cadence's own cap.
	retryAfter time.Duration
	// origin is the API origin every URL the walk follows must stay on.
	origin   string
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

// reconcileLoss walks a loss under its own recorded filter set.
func (w *repairWalker) reconcileLoss(ctx context.Context, loss Loss) error {
	walker := *w
	if loss.HasFilters {
		// Recorded with the loss, empty set included: the whole-account feed
		// is a filter set, and walking it narrowly would miss the very events
		// the loss is about.
		walker.filters = loss.Filters
	}
	return walker.reconcile(ctx, loss)
}

func (w *repairWalker) reconcile(ctx context.Context, loss Loss) error {
	for {
		if !w.now().Before(loss.DetectedAt.Add(maxLossLifetime)) {
			// The absolute deadline. A throttle postpones the window, and a
			// server that keeps asking for patience could postpone it forever;
			// a loss that can never close is its own kind of silence. At a day
			// old it closes, whatever the retry state, with its ids reported.
			unrecovered, err := w.ledger.CloseLoss(ctx, loss.ID, w.now())
			if err != nil {
				return err
			}
			w.log.Error("a buffer overflow reached its absolute deadline; what is still missing is unrecovered",
				"loss_id", loss.ID, "unrecovered", unrecovered, "age", w.now().Sub(loss.DetectedAt))
			return nil
		}
		if !w.now().Before(loss.DeadlineAt) {
			// Past the window already — perhaps across restarts whose walks
			// each ended in a failure no retry fixes. One last pass is still
			// worth trying; after it the loss closes, unless the server
			// refused that attempt too.
			return w.finalPass(ctx, &loss)
		}

		done, err := w.settled(ctx, loss)
		if err != nil || done {
			return err
		}

		cursor, err := w.walk(ctx, &loss)
		if errors.Is(err, errReconciliationEnded) {
			return w.closeIfExpired(ctx, loss)
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

		// A missing id the walk did not serve is NOT yet a gap: `next` can end
		// while an event is still inside the poll lane's safety delay. The
		// walk repeats on the repair cadence until the window closes — or
		// after a server-directed wait, whichever is longer.
		wait := w.interval
		if w.retryAfter > wait {
			wait = w.retryAfter
		}
		if w.retryAfter > 0 {
			if err := w.postpone(ctx, &loss); err != nil {
				return err
			}
			// Used: this wait was the server's answer to this attempt, and
			// says nothing about the next one.
			w.retryAfter = 0
		}
		if err := w.wait(ctx, wait); err != nil {
			return err
		}
	}
}

// postpone moves a loss's window out by the wait the server asked for.
//
// This is the whole rule for a throttle, in one place: it is not a failure and
// not a verdict, and the time it costs belongs to the server, not to the loss.
// The window therefore never runs out while a server is asking for patience,
// and a throttled attempt never condemns an id.
func (w *repairWalker) postpone(ctx context.Context, loss *Loss) error {
	extended := loss.DeadlineAt.Add(w.retryAfter)
	if err := w.ledger.ExtendLossDeadline(ctx, loss.ID, extended); err != nil {
		return err
	}
	loss.DeadlineAt = extended
	return nil
}

// finalPass runs one walk for a loss past its window and then closes it,
// whatever the walk managed: its missing ids become unrecovered.
func (w *repairWalker) finalPass(ctx context.Context, loss *Loss) error {
	if done, err := w.settled(ctx, *loss); err != nil || done {
		return err
	}
	if _, err := w.walk(ctx, loss); err != nil && !errors.Is(err, errReconciliationEnded) {
		return err
	}
	if done, err := w.settled(ctx, *loss); err != nil || done {
		return err
	}
	if w.retryAfter > 0 {
		// The server refused this attempt and named a wait, so there is
		// nothing to conclude about the ids. The window moves out by the wait
		// and the loss is walked again.
		wait := w.retryAfter
		if err := w.postpone(ctx, loss); err != nil {
			return err
		}
		w.retryAfter = 0
		w.log.Warn("the last repair attempt was throttled; waiting as the server asked and trying again",
			"loss_id", loss.ID, "retry_after", wait)
		if err := w.wait(ctx, wait); err != nil {
			return err
		}
		return w.reconcile(ctx, *loss)
	}
	if err := ctx.Err(); err != nil {
		// The final attempt did not run to its end; closing now would condemn
		// ids on a pass that never happened.
		return err
	}
	if done, err := w.settled(ctx, *loss); err != nil || done {
		return err
	}
	return w.closeExpired(ctx, *loss)
}

// closeIfExpired closes a loss whose walk ended early, if its window is over.
// A walk that ends in a failure no retry fixes leaves the loss open for the
// next start — but not forever.
func (w *repairWalker) closeIfExpired(ctx context.Context, loss Loss) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.now().Before(loss.DeadlineAt) {
		return nil
	}
	return w.closeExpired(ctx, loss)
}

func (w *repairWalker) closeExpired(ctx context.Context, loss Loss) error {
	unrecovered, err := w.ledger.CloseLoss(ctx, loss.ID, w.now())
	if err != nil {
		return err
	}
	if unrecovered > 0 {
		w.log.Error("a buffer overflow's window closed with events unrecovered", "loss_id", loss.ID, "unrecovered", unrecovered)
	}
	return nil
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
	walked := map[string]bool{}
	maxPages := w.maxPages
	if maxPages <= 0 {
		maxPages = maxRepairPagesPerPass
	}
	for pages := 0; ; {
		if err := ctx.Err(); err != nil {
			// Cancellation is a delay, never a verdict: the loss stays open
			// on disk for the next start.
			return last, err
		}
		if pages >= maxPages {
			// Distinct positions forever evade cycle detection. The pass ends
			// at the cap, and the next one resumes from the cursor saved on
			// the last page.
			w.log.Warn("a repair pass reached its page cap; resuming on the repair cadence", "loss_id", loss.ID, "pages", pages)
			return last, nil
		}
		pages++
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
		if walked[page.Next] || page.Next == cursor.PageURL {
			// A next already walked this pass — itself, or a cycle — would
			// spin. The pass ends and the repair cadence is the backoff.
			w.log.Warn("a repair page's next repeats a URL this pass already walked; ending the pass", "loss_id", loss.ID)
			return last, nil
		}
		walked[page.Next] = true
		if err := sameOrigin(w.origin, page.Next); err != nil {
			w.log.Error("a repair page's next URL leaves the API origin; the loss stays open for the next start", "loss_id", loss.ID)
			return last, errReconciliationEnded
		}
		cursor = eventfeed.Cursor{PageURL: page.Next}
	}
}

// errReconciliationEnded reports that the walk itself settled the loss's fate
// for this start — closed it, or left it open for the next — so the caller
// stops rather than looping back to find nothing missing and calling that a
// full recovery.
var errReconciliationEnded = errors.New("connector: reconciliation ended inside the repair walk")

// maxLossLifetime is how long a loss can stay open, however often a server
// asks for patience. A day is far past any horizon the feed itself has.
const maxLossLifetime = 24 * time.Hour

// maxRepairPagesPerPass bounds the pages one repair pass walks. A thousand
// pages crosses up to a million ledger rows; past that the pass yields to the
// repair cadence and resumes from its saved cursor.
const maxRepairPagesPerPass = 1000

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
	if ctxErr := ctx.Err(); ctxErr != nil {
		// The caller's cancellation, not the walk failing. It propagates, so
		// nothing downstream mistakes a shutdown for a pass that ran.
		return nil, ctxErr
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
		if err := sameOrigin(w.origin, pollErr.ResumeURL); err != nil {
			w.log.Error("a repair 410's resume URL leaves the API origin; the loss stays open for the next start", "loss_id", loss.ID)
			return nil, errReconciliationEnded
		}
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

	case eventfeed.PollThrottled:
		// The server named a wait. It is a directive, not a hint: polling
		// again on the repair cadence would answer a fifteen-minute
		// Retry-After every minute.
		if pollErr.RetryAfter > w.retryAfter {
			w.retryAfter = pollErr.RetryAfter
		}
		w.log.Warn("a repair poll was throttled; waiting as the server asked",
			"loss_id", loss.ID, "retry_after", pollErr.RetryAfter)
		return nil, nil

	case eventfeed.PollTransient, eventfeed.PollUnauthorized:
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

// sameOrigin holds a URL the repair walk is about to follow to the API origin,
// with no scheme downgrade.
//
// This is the one check intake keeps from its temporary adapter. The SDK's
// live poll source re-issues a followed URL's cursor against its own client
// and never requests the URL itself, but it leaves origin validation to the
// caller — its connector runs it before every followed URL, unexported. The
// repair walk follows next and resume URLs outside that connector, so it
// runs the check itself rather than following a foreign URL's cursor.
func sameOrigin(origin, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.User != nil {
		return errForeignContinuation
	}
	canonical, err := eventfeed.CanonicalOrigin(parsed.Scheme + "://" + parsed.Host)
	if err != nil || canonical != origin {
		return errForeignContinuation
	}
	return nil
}

var errForeignContinuation = errors.New("connector: a followed URL leaves the API origin")
