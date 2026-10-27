package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// recordingFiling is one Move… or Copy… of a recording, as the web's dialogs
// make it through Basecamp's recording moves and copies. Basecamp files it in
// the background, so runFiling waits. Each kind of recording fills in how its
// messages name it, where to look for it, and how to read back what was filed.
type recordingFiling struct {
	kind     basecamp.FilingType
	sourceID int64
	// noun names the recording in messages: "card" gives "card #456".
	noun string
	// destinationID is the container it goes into, from the destination
	// project's dock. toProjectID is set instead for a dock tool, which goes
	// to a project.
	destinationID   int64
	toProjectID     int64
	includeComments bool

	// destination describes where it goes for errors: "card table 900 (Ideas)".
	destination string
	// showCmd shows a recording of this kind given its id: "basecamp cards show".
	showCmd string
	// lookFor lists where a copy lands, so a copy that may have started is
	// checked for before copying again; landing says where in it, before lookFor.
	lookFor string
	landing string
	// read reads back the recording the filing made, by its new id.
	read func(ctx context.Context, id int64) (filedRecording, error)
}

// filedRecording is a recording that a filing made, read back for output:
// result is the recording with its source_id, and the rest names it and
// where it landed for the summary and follow-ups.
type filedRecording struct {
	result      any
	id          int64
	bucket      *basecamp.Bucket
	parent      *basecamp.Parent
	breadcrumbs []output.Breadcrumb
}

// runFiling starts the filing once, never retrying it (each copy request makes
// another copy), waits for Basecamp to finish it, and outputs what it made.
func runFiling(cmd *cobra.Command, app *appctx.App, f recordingFiling) error {
	filing, err := f.start(cmd.Context(), app)
	if err != nil {
		return copyStartError(f, convertSDKError(err))
	}

	finished := func() bool {
		return filing.Status == basecamp.FilingCompleted || filing.Status == basecamp.FilingFailed
	}
	_, waitErr := pollUntil(cmd.Context(), func(ctx context.Context) (bool, error) {
		if finished() {
			return true, nil
		}
		latest, err := app.Account().Recordings().GetFiling(ctx, filing.ID)
		if err != nil {
			return false, err
		}
		filing = latest
		return finished(), nil
	})

	verb, _, past := f.verbs()
	switch {
	case filing.Status == basecamp.FilingFailed:
		return &output.Error{
			Code:    output.CodeAPI,
			Message: fmt.Sprintf("Basecamp could not %s %s #%d to %s", verb, f.noun, f.sourceID, f.destination),
			Hint:    fmt.Sprintf("Check the %s with: %s %d", f.noun, f.showCmd, f.sourceID),
		}
	case filing.Status != basecamp.FilingCompleted || filing.DestinationRecording == nil:
		return stillFilingError(f, waitErr)
	}

	newID := filing.DestinationRecording.ID
	filed, err := f.read(cmd.Context(), newID)
	if err != nil {
		return &output.Error{
			Code:    output.CodeAPI,
			Message: fmt.Sprintf("%s %s #%d as %s #%d, but could not read the new %s", past, f.noun, f.sourceID, f.noun, newID, f.noun),
			Hint:    fmt.Sprintf("Show it with: %s %d", f.showCmd, newID),
			Cause:   textCause(err),
		}
	}
	return filedOK(app, f.noun, past, f.sourceID, filed)
}

func (f recordingFiling) start(ctx context.Context, app *appctx.App) (*basecamp.Filing, error) {
	recordings := app.Account().Recordings()
	if f.kind == basecamp.FilingCopy {
		return recordings.Copy(ctx, f.sourceID, &basecamp.CopyRecordingRequest{
			FilingDestinationID:  f.destinationID,
			DestinationProjectID: f.toProjectID,
			IncludeComments:      f.includeComments,
		})
	}
	return recordings.Move(ctx, f.sourceID, &basecamp.MoveRecordingRequest{
		FilingDestinationID:  f.destinationID,
		DestinationProjectID: f.toProjectID,
	})
}

// verbs returns the filing's verb as an infinitive, a progressive and a past.
func (f recordingFiling) verbs() (string, string, string) {
	if f.kind == basecamp.FilingCopy {
		return "copy", "copying", "Copied"
	}
	return "move", "moving", "Moved"
}

// copyStartError keeps a failed copy request from inviting a retry. Only a 4xx
// answer shows Basecamp refused the copy; a 5xx, a dropped connection or an
// unreadable answer can follow a copy it already started, so retrying would
// make a second one. A rate limit stays retryable: it is refused before
// anything is copied.
func copyStartError(f recordingFiling, err error) error {
	e := output.AsError(err)
	refused := e.HTTPStatus >= 400 && e.HTTPStatus < 500
	if f.kind != basecamp.FilingCopy || refused || e.Code == output.CodeRateLimit {
		return err
	}
	unsure := *e
	unsure.Retryable = false
	unsure.Hint = "Basecamp may have started the copy before this failed, and copying again makes another copy. Check first: " + f.lookFor
	return &unsure
}

// stillFilingError reports a move or copy that Basecamp accepted but had not
// finished when the wait ended.
func stillFilingError(f recordingFiling, waitErr error) error {
	_, progressive, _ := f.verbs()
	hint := fmt.Sprintf("Once it lands, %s %d returns the %s at its new id", f.showCmd, f.sourceID, f.noun)
	if f.kind == basecamp.FilingCopy {
		hint = fmt.Sprintf("Do not copy it again, which makes another copy. %s: %s", f.landing, f.lookFor)
	}
	return &output.Error{
		Code:    output.CodeInProgress,
		Message: fmt.Sprintf("Basecamp is still %s %s #%d to %s", progressive, f.noun, f.sourceID, f.destination),
		Hint:    hint,
		Cause:   textCause(waitErr),
	}
}

// textCause keeps a cause's message but not its type. output.AsError renders
// a rate-limit gate error found anywhere in the chain in place of the error
// wrapping it, which would turn an accepted filing's "do not retry" into a
// retryable rate limit.
func textCause(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(err.Error())
}

// filedOK outputs a recording that a move, copy or teleport made, with a
// summary naming the one it came from and where it landed.
func filedOK(app *appctx.App, noun, past string, sourceID int64, filed filedRecording) error {
	place := []string{}
	if filed.bucket != nil {
		place = append(place, filed.bucket.Name)
	}
	if filed.parent != nil {
		place = append(place, filed.parent.Title)
	}
	summary := fmt.Sprintf("%s %s #%d to %s as %s #%d", past, noun, sourceID, strings.Join(place, " › "), noun, filed.id)
	return app.OK(filed.result, output.WithSummary(summary), output.WithBreadcrumbs(filed.breadcrumbs...))
}

// filingWait bounds the wait for a recording that Basecamp moves or copies in
// the background: checks start first apart, back off to max, and stop after
// total. Tests shorten it.
var filingWait = struct{ first, max, total time.Duration }{
	first: 250 * time.Millisecond,
	max:   2 * time.Second,
	total: time.Minute,
}

// pollUntil runs check, backing off between runs, until it reports done or
// filingWait.total passes. It returns false with no error when the wait runs
// out, and ctx's error when ctx is canceled first.
func pollUntil(ctx context.Context, check func(context.Context) (bool, error)) (bool, error) {
	waitCtx, cancel := context.WithTimeout(ctx, filingWait.total)
	defer cancel()

	for delay := filingWait.first; ; delay = min(2*delay, filingWait.max) {
		done, err := check(waitCtx)
		if err == nil && done {
			return true, nil
		}
		if err != nil && waitCtx.Err() == nil {
			return false, err
		}
		select {
		case <-waitCtx.Done():
			return false, ctx.Err()
		case <-time.After(delay):
		}
	}
}

// parseFilingRef reads the recording or destination a move or copy names: a
// positive ID, or a URL to one recording of kind on a trusted Basecamp host in
// the session's account. extractID keeps only a URL's trailing number, which on
// a look-alike host, in another account or on another kind of recording names
// something else in this account. It runs after ensureAccount.
func parseFilingRef(app *appctx.App, arg, noun, kind, urlShape string) (filesMoveRef, error) {
	ref, err := parseFilesMoveRef(app, arg, []string{kind})
	if err != nil {
		return ref, output.ErrUsageHint(fmt.Sprintf("Expected a %s ID or URL: %v", noun, err),
			fmt.Sprintf("Pass a %s ID or a Basecamp %s URL (%s)", noun, noun, urlShape))
	}
	if ref.account != "" && !accountIDsEqual(ref.account, app.Config.AccountID) {
		return ref, output.ErrUsage(fmt.Sprintf("URL is for account %s, but this session uses account %s", ref.account, app.Config.AccountID))
	}
	return ref, nil
}

// rejectProjectConflict fails when an explicitly supplied project (the
// --in/--project flag or the recording URL's bucket) resolves to a different
// project than the recording's own bucket, before a move or copy acts on it.
// Config/interactive defaults are not checked — only explicit caller context
// can conflict.
func rejectProjectConflict(cmd *cobra.Command, app *appctx.App, noun, flagProject, urlProjectID string, bucketID int64) error {
	want := strconv.FormatInt(bucketID, 10)
	check := func(value, label string) error {
		if value == "" {
			return nil
		}
		resolved, _, err := app.Names.ResolveProject(cmd.Context(), value)
		if err != nil {
			return err
		}
		if resolved != want {
			return output.ErrUsageHint(
				fmt.Sprintf("The %s is in project %s, but %s points at project %s", noun, want, label, resolved),
				fmt.Sprintf("Omit the conflicting project — the %s's own project is always used", noun),
			)
		}
		return nil
	}
	if err := check(flagProject, "--in/--project"); err != nil {
		return err
	}
	// app.Flags.Project carries a root-level --project (e.g.
	// `basecamp --project 999 cards move …`), which lands here rather than in
	// the command's own flag — check it too so a root-level conflict can't slip
	// past into a move or copy.
	if err := check(app.Flags.Project, "--project"); err != nil {
		return err
	}
	return check(urlProjectID, "the "+noun+" URL")
}
