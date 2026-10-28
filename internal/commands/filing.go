package commands

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

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
	destinationID int64
	// parentID is a folder inside the destination Docs & Files to put it in.
	parentID        int64
	toProjectID     int64
	includeComments bool

	// destination describes where it goes for errors: "card table 900 (Ideas)".
	destination string
	// showCmd shows a recording of this kind given its id: "basecamp cards show".
	showCmd string
	// lookFor lists where a copy lands, for a copy that may have started
	// without Basecamp naming its filing, so it is checked for before copying
	// again; match says what tells the copy apart there.
	lookFor string
	match   string
	// read reads back the recording the filing made, by its new id.
	read func(ctx context.Context, id int64) (filedRecording, error)

	// scope is the --profile and --account every command the filing prints
	// must carry to reach this account from a fresh shell. runFiling and
	// runFilings set it.
	scope string
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

// filingOutcome is how far a filing got, which decides whether running it
// again is safe.
type filingOutcome int

const (
	// filingDone filed the recording and read back what it made.
	filingDone filingOutcome = iota
	// filingRefused was refused by Basecamp before anything was filed, so
	// running it again is safe.
	filingRefused
	// filingUnread filed the recording under a known new id but could not
	// read it back. Running it again moves it twice or makes another copy.
	filingUnread
	// filingUnsettled may have started, failed inside Basecamp, or was still
	// running when the wait ended. It must be checked before running again.
	filingUnsettled
)

// runFiling files one recording and outputs what it made. scope is read
// before the command resolved its account, as newFollowUpScope requires.
func runFiling(cmd *cobra.Command, app *appctx.App, scope followUpScope, f recordingFiling) error {
	f.scope = scope.args(app.Config.AccountID)
	filed, _, err := fileRecording(cmd.Context(), app, f)
	if err != nil {
		return err
	}
	_, _, past := f.verbs()
	return filedOK(app, f.noun, past, f.sourceID, filed)
}

// fileRecording starts the filing once, never retrying it (each copy request
// makes another copy), waits for Basecamp to finish it, and reads back what it
// made.
func fileRecording(ctx context.Context, app *appctx.App, f recordingFiling) (filedRecording, filingOutcome, error) {
	filing, err := f.start(ctx, app)
	if err != nil {
		err = copyStartError(f, convertSDKError(err))
		if refusedBeforeFiling(output.AsError(err)) {
			return filedRecording{}, filingRefused, err
		}
		return filedRecording{}, filingUnsettled, err
	}

	finished := func() bool {
		return filing.Status == basecamp.FilingCompleted || filing.Status == basecamp.FilingFailed
	}
	_, waitErr := pollUntil(ctx, func(ctx context.Context) (bool, error) {
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
		return filedRecording{}, filingUnsettled, &output.Error{
			Code:    output.CodeAPI,
			Message: fmt.Sprintf("Basecamp could not %s %s #%d to %s", verb, f.noun, f.sourceID, f.destination),
			Hint:    fmt.Sprintf("Check the %s with: %s", f.noun, f.show(f.sourceID)),
		}
	case filing.Status != basecamp.FilingCompleted || filing.DestinationRecording == nil:
		return filedRecording{}, filingUnsettled, stillFilingError(f, filing.ID, waitErr)
	}

	newID := filing.DestinationRecording.ID
	filed, err := f.read(ctx, newID)
	if err != nil {
		return filedRecording{id: newID}, filingUnread, &output.Error{
			Code:    output.CodeAPI,
			Message: fmt.Sprintf("%s %s #%d as %s #%d, but could not read the new %s", past, f.noun, f.sourceID, f.noun, newID, f.noun),
			Hint:    "Show it with: " + f.show(newID),
			Cause:   textCause(err),
		}
	}
	for i := range filed.breadcrumbs {
		filed.breadcrumbs[i].Cmd += f.scope
	}
	return filed, filingDone, nil
}

// show is the command that shows a recording of this kind by its id.
func (f recordingFiling) show(id int64) string {
	return fmt.Sprintf("%s %d%s", f.showCmd, id, f.scope)
}

// check lists where a copy lands and says how to tell the copy apart there.
func (f recordingFiling) check() string {
	return f.lookFor + f.scope + ", then " + f.match
}

func (f recordingFiling) start(ctx context.Context, app *appctx.App) (*basecamp.Filing, error) {
	recordings := app.Account().Recordings()
	if f.kind == basecamp.FilingCopy {
		return recordings.Copy(ctx, f.sourceID, &basecamp.CopyRecordingRequest{
			FilingDestinationID:  f.destinationID,
			DestinationProjectID: f.toProjectID,
			ParentID:             f.parentID,
			IncludeComments:      f.includeComments,
		})
	}
	return recordings.Move(ctx, f.sourceID, &basecamp.MoveRecordingRequest{
		FilingDestinationID:  f.destinationID,
		DestinationProjectID: f.toProjectID,
		ParentID:             f.parentID,
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
	if f.kind != basecamp.FilingCopy || refusedBeforeFiling(e) {
		return err
	}
	unsure := *e
	unsure.Retryable = false
	unsure.Hint = "Basecamp may have started the copy before this failed, and copying again makes another copy. Check first: " + f.check()
	return &unsure
}

// refusedBeforeFiling reports whether a failed request to start a filing was
// refused before anything was filed: a 4xx answer, or a rate limit, which is
// refused before the request is handled.
func refusedBeforeFiling(e *output.Error) bool {
	return (e.HTTPStatus >= 400 && e.HTTPStatus < 500) || e.Code == output.CodeRateLimit
}

// stillFilingError reports a move or copy that Basecamp accepted but had not
// finished when the wait ended. Its filing says where it got to, whatever the
// recording's type and status, so the hint names the filing rather than a
// listing the result may not show in.
func stillFilingError(f recordingFiling, filingID int64, waitErr error) error {
	_, progressive, _ := f.verbs()
	hint := fmt.Sprintf("Do not move it again. Check it with: basecamp filings show %d%s, which names the %s's new id once it lands", filingID, f.scope, f.noun)
	if f.kind == basecamp.FilingCopy {
		hint = fmt.Sprintf("Do not copy it again, which makes another copy. Check it with: basecamp filings show %d%s, which names the copy once it lands", filingID, f.scope)
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
	summary := fmt.Sprintf("%s %s #%d to %s as %s #%d", past, noun, sourceID, filed.place(), noun, filed.id)
	return app.OK(filed.result, output.WithSummary(summary), output.WithBreadcrumbs(filed.breadcrumbs...))
}

// place names where a filed recording landed: "Dest › Triage".
func (filed filedRecording) place() string {
	place := []string{}
	if filed.bucket != nil {
		place = append(place, filed.bucket.Name)
	}
	if filed.parent != nil {
		place = append(place, filed.parent.Title)
	}
	return strings.Join(place, " › ")
}

// runFilings files several recordings one by one, as the web's bulk Move…
// and Copy… do, and outputs what each made. A failure does not stop the rest,
// and none is retried. If any is not cleanly filed the command fails, naming
// how far each got, and asks to run again only the ones Basecamp refused
// before filing: running again one that was filed, or may have been, moves it
// twice or makes another copy.
func runFilings(cmd *cobra.Command, app *appctx.App, scope followUpScope, filings []recordingFiling) error {
	if len(filings) == 1 {
		return runFiling(cmd, app, scope, filings[0])
	}
	verb, _, past := filings[0].verbs()
	nouns := output.PluralNoun(filings[0].noun)

	var (
		results     []any
		done        []string
		breadcrumbs []output.Breadcrumb
		report      []string
		refused     []int64
		causes      []string
		unread      []string
		unsettled   = map[string][]int64{}
		checks      []string
		worst       *output.Error
		worstOf     filingOutcome
	)
	for _, f := range filings {
		f.scope = scope.args(app.Config.AccountID)
		filed, outcome, err := fileRecording(cmd.Context(), app, f)
		var e *output.Error
		if err != nil {
			// The report keeps only each failure's text, so a 401's remedy
			// for the credential sent is settled here, while the SDK error
			// that App.Err would read is still attached.
			e = output.AsError(app.WithAuthRemedy(err))
			if worst == nil || outcome > worstOf {
				worst, worstOf = e, outcome
			}
		}
		switch outcome {
		case filingDone:
			results = append(results, filed.result)
			done = append(done, fmt.Sprintf("#%d as #%d in %s", f.sourceID, filed.id, filed.place()))
			for _, crumb := range filed.breadcrumbs {
				if !slices.ContainsFunc(breadcrumbs, func(b output.Breadcrumb) bool { return b.Cmd == crumb.Cmd }) {
					breadcrumbs = append(breadcrumbs, crumb)
				}
			}
		case filingRefused:
			refused = append(refused, f.sourceID)
			if e.Hint != "" && !slices.Contains(causes, e.Hint) {
				causes = append(causes, e.Hint)
			}
			report = append(report, fmt.Sprintf("#%d refused: %s", f.sourceID, e.Message))
		case filingUnread:
			unread = append(unread, fmt.Sprintf("#%d is #%d now (%s)", f.sourceID, filed.id, f.show(filed.id)))
			report = append(report, fmt.Sprintf("#%d %s as #%d, not read back", f.sourceID, strings.ToLower(past), filed.id))
		case filingUnsettled:
			check := e.Hint
			if check == "" {
				check = "Check it with: " + f.show(f.sourceID)
			}
			if _, seen := unsettled[check]; !seen {
				checks = append(checks, check)
			}
			unsettled[check] = append(unsettled[check], f.sourceID)
			report = append(report, fmt.Sprintf("#%d not settled: %s", f.sourceID, e.Message))
		}
	}

	if worst == nil {
		summary := fmt.Sprintf("%s %d %s: %s", past, len(done), nouns, strings.Join(done, ", "))
		return app.OK(results, output.WithSummary(summary), output.WithBreadcrumbs(breadcrumbs...))
	}

	message := fmt.Sprintf("%s %d of %d %s", past, len(done), len(filings), nouns)
	if len(done) > 0 {
		message += " (" + strings.Join(done, ", ") + ")"
	}
	var hints []string
	if len(unread) > 0 {
		hints = append(hints, fmt.Sprintf("Do not %s these again: %s.", verb, strings.Join(unread, ", ")))
	}
	for _, check := range checks {
		hints = append(hints, fmt.Sprintf("Do not %s %s again yet. %s", verb, sourceList(unsettled[check]), check))
	}
	if len(refused) > 0 {
		hints = append(hints, fmt.Sprintf("Basecamp refused %s before filing anything; once the cause is fixed, run the command again for %s alone.",
			sourceList(refused), sourceList(refused)))
		hints = append(hints, causes...)
	}
	return &output.Error{
		Code:       worst.Code,
		Message:    message + "; " + strings.Join(report, "; "),
		Hint:       strings.Join(hints, "\n"),
		HTTPStatus: worst.HTTPStatus,
		Cause:      textCause(worst),
	}
}

// filingNames holds the recordings a bulk move or copy names, each by the id
// it read as and the argument that named it. A moved recording's old id reads
// as its new one, so two arguments can name one recording, and filing it twice
// moves it twice or makes two copies. Comparing what each argument read as,
// before anything is filed, catches that where comparing the arguments cannot.
type filingNames map[int64]string

// add records that arg read as recording id, refusing a second name for it.
func (named filingNames) add(noun, arg string, id int64) error {
	if first, ok := named[id]; ok {
		return output.ErrUsageHint(
			fmt.Sprintf("%s and %s are both %s #%d", first, arg, noun, id),
			fmt.Sprintf("A moved %s's old id reads as its new one. Name %s #%d once", noun, noun, id),
		)
	}
	named[id] = arg
	return nil
}

// sourceList names recordings by id: "#456, #457".
func sourceList(ids []int64) string {
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = fmt.Sprintf("#%d", id)
	}
	return strings.Join(names, ", ")
}

// filingWait bounds the wait for a recording that Basecamp moves or copies in
// the background: checks start first apart, back off to max, and stop after
// total. Tests shorten it.
var filingWait = struct{ first, max, total time.Duration }{
	first: 250 * time.Millisecond,
	max:   2 * time.Second,
	total: time.Minute,
}

// recordingCopyLookFor lists the recordings of one type in a project in the
// source's status, every page. A copy keeps the source's status, so this shows
// a copy whatever state it was made in. Basecamp's recordings listing serves
// Todo, Todolist (groups included), Message, Document, Upload, Vault,
// Kanban::Card and Schedule::Entry, in the active, archived and trashed
// statuses only. A drafted copy is listed only among its author's drafts,
// which hold messages, documents and uploads.
func recordingCopyLookFor(recordableType string, projectID int64, status string) string {
	switch status {
	case "archived", "trashed":
	case "drafted":
		if slices.Contains([]string{"Message", "Document", "Upload"}, recordableType) {
			return "basecamp drafts list --all"
		}
		return fmt.Sprintf("open the destination in Basecamp, since Basecamp's API lists no drafted %s", recordableType)
	default:
		status = "active"
	}
	return fmt.Sprintf("basecamp recordings list %s --in %d --status %s --all", recordableType, projectID, status)
}

// copyMatch says what tells a copy of a recording apart in the container it
// lands in. A copy keeps its original's title, created_at and updated_at, so
// no sort puts it first; its title in that container finds it.
func copyMatch(sourceID int64, title, container string, containerID int64) string {
	name := fmt.Sprintf("the copy of #%d", sourceID)
	if title != "" {
		name = fmt.Sprintf("%q", title)
	}
	return fmt.Sprintf("look for %s in %s (#%d); a copy has its original's title and dates", name, cmp.Or(container, "the destination"), containerID)
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
	return parseFilingRefOf(app, arg, noun, urlShape, []string{kind})
}

// parseFilingRefOf is parseFilingRef for a recording a URL may name under
// several kinds, such as a Docs & Files item.
func parseFilingRefOf(app *appctx.App, arg, noun, urlShape string, kinds []string) (filesMoveRef, error) {
	ref, err := parseFilesMoveRef(app, arg, kinds)
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

// AnnotationFilingReads lists, space-separated, the container flags that a
// command moving or copying recordings inherits and reads. guardFilingFlags
// sets it.
const AnnotationFilingReads = "filing_reads"

// guardFilingFlags makes a command that moves or copies recordings refuse,
// before it reads or sends anything, a container flag it inherits but never
// reads. Such a flag would be ignored: todos copy --todolist 901 copied the
// to-do into its own list and reported success. reads names the inherited
// container flags the command does read, in some mode.
//
// A container flag is the root's --todolist or a flag that a command group
// adds for its commands (--card-table, --vault, --list, …). --project and
// --in are left to each command, which holds a filed recording to them; the
// root's account, profile and output flags are not containers.
func guardFilingFlags(cmd *cobra.Command, reads ...string) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[AnnotationFilingReads] = strings.Join(reads, " ")
	run := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := refuseUnreadContainerFlags(cmd, reads); err != nil {
			return err
		}
		return run(cmd, args)
	}
	return cmd
}

func refuseUnreadContainerFlags(cmd *cobra.Command, reads []string) error {
	var unread *pflag.Flag
	cmd.InheritedFlags().VisitAll(func(f *pflag.Flag) {
		if unread == nil && f.Changed && isContainerFlag(cmd, f) && !slices.Contains(reads, f.Name) {
			unread = f
		}
	})
	if unread == nil {
		return nil
	}
	hint := fmt.Sprintf("%s takes no destination; drop --%s", cmd.CommandPath(), unread.Name)
	if dests := destinationFlags(cmd); len(dests) > 0 {
		hint = fmt.Sprintf("Name where it goes with %s (see %s --help)", strings.Join(dests, " or "), cmd.CommandPath())
	}
	return output.ErrUsageHint(fmt.Sprintf("%s does not read --%s, so it would ignore it", cmd.CommandPath(), unread.Name), hint)
}

func isContainerFlag(cmd *cobra.Command, f *pflag.Flag) bool {
	switch f.Name {
	case "project", "in":
		return false
	case "todolist":
		return true
	}
	return cmd.Root().PersistentFlags().Lookup(f.Name) != f
}

// destinationFlags lists a command's own flags that name where it files a
// recording: --to (a folder or column) and the --to-<container> flags.
func destinationFlags(cmd *cobra.Command) []string {
	var dests []string
	cmd.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
		if strings.HasPrefix(f.Name, "to-") || (f.Name == "to" && f.Value.Type() == "string") {
			dests = append(dests, "--"+f.Name)
		}
	})
	return dests
}
