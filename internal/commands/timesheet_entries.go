package commands

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/dateparse"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// newTimesheetEntryCmd creates the `timesheet entry` group: one timesheet
// entry at a time.
//
// bc3 routes timesheet entries by account alone — POST
// /recordings/:id/timesheet/entries.json to log time against a recording, and
// /timesheet_entries/:id for one entry — so no verb needs a project, and
// none reads the --project/--in the timesheet group carries for its reports.
func newTimesheetEntryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "entry",
		Short: "Log, view, change and delete timesheet entries",
		Long: `Log, view, change and delete individual timesheet entries.

An entry is time logged against one item: a to-do, message, card, document,
upload, schedule entry, or a project's own timesheet. Every verb but create
addresses the entry itself, by id or by its Basecamp URL.

  basecamp timesheet entry create 789 --hours 1.5
  basecamp timesheet entry show 1069480283
  basecamp timesheet entry update 1069480283 --hours 2:15
  basecamp timesheet entry delete 1069480283 --force`,
	}

	cmd.AddCommand(
		newTimesheetEntryShowCmd(),
		newTimesheetEntryCreateCmd(),
		newTimesheetEntryUpdateCmd(),
		newTimesheetEntryDeleteCmd(),
	)

	return cmd
}

func newTimesheetEntryShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show a timesheet entry",
		Long: `Show one timesheet entry: its date, hours, description, the person the
time is for, and the item it is logged against.

  basecamp timesheet entry show 1069480283
  basecamp timesheet entry show https://3.basecamp.com/123/projects/456/timesheet/entries/1069480283`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entryID, urlAccountID, err := timesheetEntryIDArg(args[0])
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			scope := newFollowUpScope(app)
			if err := timesheetURLAccount(app, args[0], urlAccountID); err != nil {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			entry, err := app.Account().Timesheet().Get(cmd.Context(), entryID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(entry,
				output.WithSummary(timesheetEntrySummary("Timesheet entry", entry)),
				output.WithBreadcrumbs(timesheetEntryBreadcrumbs(cmd, entry, scope.args(app.Config.AccountID))...),
			)
		},
	}
}

func newTimesheetEntryCreateCmd() *cobra.Command {
	var hours, date, description, person string

	cmd := &cobra.Command{
		Use:   "create <recording-id|url>",
		Short: "Log time against an item",
		Long: `Log time against an item: a to-do, message, card, document, upload or
schedule entry, by its id or pasted Basecamp URL.

To log time to a project without an item, pass the id of the project's own
timesheet: the parent id of a project-level entry in
"basecamp timesheet project".

--hours is required, as a decimal (1.5) or hours and minutes (1:30).
--date defaults to today and takes ISO 8601 or a relative date (yesterday,
monday). --person is who the time is for, by id, name,
email or "me"; it defaults to you, and must be a non-client member of the
project.

  basecamp timesheet entry create 789 --hours 1.5
  basecamp timesheet entry create https://3.basecamp.com/123/buckets/456/todos/789 --hours 0:45 --date yesterday --description "Review"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			recordingID, urlAccountID, err := timesheetRecordingIDArg(args[0])
			if err != nil {
				return err
			}
			if strings.TrimSpace(hours) == "" {
				return output.ErrUsageHint("--hours is required", `Pass the time spent, as a decimal (1.5) or hours and minutes (1:30)`)
			}
			if err := validateTimesheetHours(hours); err != nil {
				return err
			}
			entryDate := time.Now().Format("2006-01-02")
			if cmd.Flags().Changed("date") {
				if entryDate, err = timesheetDate(date); err != nil {
					return err
				}
			}

			app := appctx.FromContext(cmd.Context())
			scope := newFollowUpScope(app)
			if err := timesheetURLAccount(app, args[0], urlAccountID); err != nil {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			req := &basecamp.CreateTimesheetEntryRequest{
				Date:        entryDate,
				Hours:       strings.TrimSpace(hours),
				Description: description,
			}
			if cmd.Flags().Changed("person") {
				if req.PersonID, err = timesheetPersonID(cmd, app, person); err != nil {
					return err
				}
			}

			entry, err := app.Account().Timesheet().Create(cmd.Context(), recordingID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(entry,
				output.WithSummary(timesheetEntrySummary("Logged timesheet entry", entry)),
				output.WithBreadcrumbs(timesheetEntryBreadcrumbs(cmd, entry, scope.args(app.Config.AccountID))...),
			)
		},
	}

	cmd.Flags().StringVar(&hours, "hours", "", `Hours spent: decimal ("1.5") or hours and minutes ("1:30")`)
	cmd.Flags().StringVarP(&date, "date", "d", "", "Date the time was spent (ISO 8601 or relative, e.g. yesterday; default today)")
	cmd.Flags().StringVar(&description, "description", "", "Note about the work")
	cmd.Flags().StringVar(&person, "person", "", `Person the time is for: ID, name, email or "me" (default: you)`)

	return cmd
}

func newTimesheetEntryUpdateCmd() *cobra.Command {
	var hours, date, description, person string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Change a timesheet entry",
		Long: `Change a timesheet entry's hours, date, description or person. Only the
flags you pass are sent; at least one is required. A description cannot be
cleared through the API, only replaced.

  basecamp timesheet entry update 1069480283 --hours 2.5
  basecamp timesheet entry update 1069480283 --date 2026-10-26 --description "Client call"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entryID, urlAccountID, err := timesheetEntryIDArg(args[0])
			if err != nil {
				return err
			}

			req := &basecamp.UpdateTimesheetEntryRequest{}
			changed := false
			if cmd.Flags().Changed("hours") {
				if err := validateTimesheetHours(hours); err != nil {
					return err
				}
				req.Hours = strings.TrimSpace(hours)
				changed = true
			}
			if cmd.Flags().Changed("date") {
				if req.Date, err = timesheetDate(date); err != nil {
					return err
				}
				changed = true
			}
			if cmd.Flags().Changed("description") {
				if description == "" {
					return output.ErrUsageHint("--description cannot be empty",
						"The API cannot clear a timesheet entry's description; pass the new text")
				}
				req.Description = description
				changed = true
			}
			if !changed && !cmd.Flags().Changed("person") {
				return output.ErrUsageHint("Nothing to update",
					"Pass at least one of --hours, --date, --description or --person")
			}

			app := appctx.FromContext(cmd.Context())
			scope := newFollowUpScope(app)
			if err := timesheetURLAccount(app, args[0], urlAccountID); err != nil {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if cmd.Flags().Changed("person") {
				if req.PersonID, err = timesheetPersonID(cmd, app, person); err != nil {
					return err
				}
			}

			entry, err := app.Account().Timesheet().Update(cmd.Context(), entryID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(entry,
				output.WithSummary(timesheetEntrySummary("Updated timesheet entry", entry)),
				output.WithBreadcrumbs(timesheetEntryBreadcrumbs(cmd, entry, scope.args(app.Config.AccountID))...),
			)
		},
	}

	cmd.Flags().StringVar(&hours, "hours", "", `Hours spent: decimal ("1.5") or hours and minutes ("1:30")`)
	cmd.Flags().StringVarP(&date, "date", "d", "", "Date the time was spent (ISO 8601 or relative, e.g. yesterday)")
	cmd.Flags().StringVar(&description, "description", "", "Note about the work")
	cmd.Flags().StringVar(&person, "person", "", `Person the time is for: ID, name, email or "me"`)

	return cmd
}

func newTimesheetEntryDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <id|url>",
		Short: "Delete a timesheet entry",
		Long: `Permanently delete a timesheet entry. It never goes to the trash and
cannot be restored, so this asks for confirmation; machine-output modes and
non-interactive sessions cannot answer one and need --force instead. It is the
only way to remove an entry: Basecamp refuses to trash or archive one. People
who may not remove the entry are refused.

  basecamp timesheet entry delete 1069480283
  basecamp timesheet entry delete 1069480283 --json --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entryID, urlAccountID, err := timesheetEntryIDArg(args[0])
			if err != nil {
				return err
			}

			// Permanent, so only with --force or a confirmation somebody can
			// answer — refused before any account lookup or request.
			if err := ensureDeleteConfirmable(cmd, force); err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			scope := newFollowUpScope(app)
			if err := timesheetURLAccount(app, args[0], urlAccountID); err != nil {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if !force {
				confirmed, err := tui.ConfirmDangerous("Permanently delete this timesheet entry?")
				switch {
				case errors.Is(err, tui.ErrCanceled):
					return nil
				case err != nil:
					return fmt.Errorf("confirming the delete: %w", err)
				case !confirmed:
					return nil
				}
			}

			if err := app.Account().Timesheet().Destroy(cmd.Context(), entryID); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"id": entryID, "deleted": true},
				output.WithSummary(fmt.Sprintf("Deleted timesheet entry #%d", entryID)),
				output.WithBreadcrumbs(output.Breadcrumb{
					Action:      "report",
					Cmd:         "basecamp timesheet report" + scope.args(app.Config.AccountID),
					Description: "View timesheet report",
				}),
			)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompt")

	return cmd
}

// timesheetEntryRoute matches the path of an entry's own URL: its app URL
// (projects/:p/timesheet/entries/:id) and its API URL in either form. The
// first group is the account, the second the entry.
var timesheetEntryRoute = regexp.MustCompile(`^/(\d+)/(?:(?:projects|buckets)/\d+/timesheet/entries|timesheet_entries)/(\d+)(?:\.json)?/?$`)

// timesheetEntryIDArg resolves the <id|url> positional the per-entry verbs
// take: a bare id or the entry's own URL, returning the URL's account too. Any
// other URL is refused rather than read as an entry id — a to-do's URL names
// the item the time is logged against, and `timesheet entry delete` must never
// aim at one of those.
func timesheetEntryIDArg(arg string) (id int64, urlAccountID string, err error) {
	raw := strings.TrimSpace(arg)
	if !isAllDigits(raw) {
		var m []string
		if u := pastedURL(raw); u != nil {
			m = timesheetEntryRoute.FindStringSubmatch(u.Path)
		}
		if m == nil {
			return 0, "", output.ErrUsageHint(
				fmt.Sprintf("%q does not name a timesheet entry", arg),
				"Pass the entry id, or its URL (…/timesheet/entries/<id>); "+
					"basecamp timesheet item <id> lists the entries on an item")
		}
		urlAccountID, raw = m[1], m[2]
	}
	id, perr := strconv.ParseInt(raw, 10, 64)
	if perr != nil || id <= 0 {
		return 0, "", output.ErrUsageHint(
			fmt.Sprintf("%q is not a timesheet entry id or URL", arg),
			"Pass the numeric entry id, or paste its Basecamp URL")
	}
	return id, urlAccountID, nil
}

// timesheetKinds are the URL types that name an item bc3 logs time against,
// as its API docs list them, or a generic recordings/<id> URL, which is as good
// as its bare id. bc3 still decides each case; the list keeps a URL for
// something else (a person, a project, a Campfire, a path the router only
// matched by shape) from sending its id out as a recording's.
var timesheetKinds = []string{
	"cards", "documents", "messages", "recordings", "schedule_entries", "todos", "uploads",
}

// timesheetRecordingIDArg resolves create's <recording-id|url>: a bare id, or
// the http(s) URL of one item of a timesheet kind, returning the URL's account
// too. Anything else is refused rather than read as a recording id.
func timesheetRecordingIDArg(arg string) (id int64, urlAccountID string, err error) {
	raw := strings.TrimSpace(arg)
	if !isAllDigits(raw) {
		// Route only the URL's own scheme, host and path, so nothing in a query
		// or fragment can be read as the item. No timesheet kind is named by a
		// fragment: a comment's #__recording_<id> cannot hold time.
		var parsed *urlarg.Parsed
		if u := pastedURL(raw); u != nil {
			parsed = urlarg.Parse(u.Scheme + "://" + u.Host + u.Path)
		}
		switch {
		case parsed == nil || parsed.IsCollection || parsed.RecordingID == "" ||
			!slices.Contains(timesheetKinds, parsed.Type):
			return 0, "", output.ErrUsageHint(
				fmt.Sprintf("%q does not name an item to log time against", arg),
				"Pass the id or URL of the to-do, message, card, document, upload or schedule entry. "+
					"For a project-level entry, pass the project timesheet's id: the parent id of a "+
					"project-level entry in basecamp timesheet project")
		case parsed.OccurrenceDate != "":
			return 0, "", output.ErrUsageHint("That URL names one occurrence of a recurring event",
				"Pass the occurrence's own id, which "+showOccurrence(raw)+" prints, or the event's id")
		}
		raw, urlAccountID = parsed.RecordingID, parsed.AccountID
	}
	id, perr := strconv.ParseInt(raw, 10, 64)
	if perr != nil || id <= 0 {
		return 0, "", output.ErrUsageHint(
			fmt.Sprintf("%q is not an item id or Basecamp URL", arg),
			"Pass the numeric id of the item to log time against, or paste its Basecamp URL")
	}
	return id, urlAccountID, nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// pastedURL parses an http(s) URL with a host, or returns nil. Callers match
// its Path alone: a regexp over the raw string would read text after a ? or #
// as path segments.
func pastedURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return nil
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u
	}
	return nil
}

// timesheetURLAccount holds a pasted URL to the account it names, before any
// request. Any argument that is not a bare id is a URL here, and the URL
// router is host-agnostic, so it must be on a trusted Basecamp host; its
// account is adopted when none is configured and must equal the configured one
// otherwise (numerically, as accountIDsEqual compares), rather than acting on
// a same-numbered entry or item in another account.
func timesheetURLAccount(app *appctx.App, arg, urlAccountID string) error {
	raw := strings.TrimSpace(arg)
	if isAllDigits(raw) {
		return nil
	}
	if !hostutil.IsTrustedBasecampHost(raw, app.Config.BaseURL) {
		return output.ErrUsage("refusing untrusted host in URL — expected a Basecamp URL")
	}
	switch {
	case urlAccountID == "":
	case app.Config.AccountID == "":
		app.Config.AccountID = urlAccountID
	case !accountIDsEqual(urlAccountID, app.Config.AccountID):
		return output.ErrUsage(fmt.Sprintf("URL account %s does not match the configured account %s", urlAccountID, app.Config.AccountID))
	}
	return nil
}

// timesheetHours is what bc3 accepts for hours: a decimal, or hours and
// minutes.
var timesheetHours = regexp.MustCompile(`^(?:\d+(?:\.\d+)?|\.\d+|\d+:[0-5]\d)$`)

func validateTimesheetHours(hours string) error {
	h := strings.TrimSpace(hours)
	if !timesheetHours.MatchString(h) {
		return output.ErrUsageHint(fmt.Sprintf("Invalid hours %q", hours),
			`Pass hours as a decimal ("1.5") or hours and minutes ("1:30")`)
	}
	return nil
}

// timesheetISODate is the only form bc3 takes for an entry's date.
var timesheetISODate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// timesheetDate turns --date into the ISO 8601 date bc3 takes. dateparse
// returns what it doesn't recognize unchanged, so anything that still isn't a
// real date is refused here rather than sent.
func timesheetDate(input string) (string, error) {
	parsed := dateparse.Parse(input)
	if timesheetISODate.MatchString(parsed) {
		if _, err := time.Parse("2006-01-02", parsed); err == nil {
			return parsed, nil
		}
	}
	return "", output.ErrUsageHint(fmt.Sprintf("Invalid date %q", input),
		"Pass an ISO 8601 date (2026-10-27) or a relative one (today, yesterday, monday)")
}

// timesheetPersonID resolves --person: a numeric id is sent as given, and a
// name, email or "me" is looked up.
func timesheetPersonID(cmd *cobra.Command, app *appctx.App, person string) (int64, error) {
	p := strings.TrimSpace(person)
	if p == "" {
		return 0, output.ErrUsageHint("--person cannot be empty", `Pass a person's ID, name, email or "me"`)
	}
	if id, err := strconv.ParseInt(p, 10, 64); err == nil {
		if id <= 0 {
			return 0, output.ErrUsage("Invalid person ID")
		}
		return id, nil
	}
	resolved, _, err := app.Names.ResolvePerson(cmd.Context(), p)
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(resolved, 10, 64)
	if err != nil {
		return 0, output.ErrUsage("Invalid person ID")
	}
	return id, nil
}

func timesheetEntrySummary(prefix string, entry *basecamp.TimesheetEntry) string {
	summary := fmt.Sprintf("%s #%d: %sh on %s", prefix, entry.ID, entry.Hours, entry.Date)
	// The generic renderers skip nested objects, so whose time it is would be
	// missing from styled and Markdown output without this.
	if entry.Person != nil && entry.Person.ID != 0 {
		summary += fmt.Sprintf(" by %s (#%d)", entry.Person.Name, entry.Person.ID)
	}
	if entry.Parent != nil && entry.Parent.Title != "" {
		summary += " for " + entry.Parent.Title
	}
	return summary
}

// timesheetEntryBreadcrumbs are the follow-ups for one entry: change or delete
// it, and see the rest of the time on its item.
//
// scopeArgs carries the profile and an account that would not come back on its
// own (adopted from a URL, given with --account), so a pasted follow-up acts
// where this one did.
func timesheetEntryBreadcrumbs(cmd *cobra.Command, entry *basecamp.TimesheetEntry, scopeArgs string) []output.Breadcrumb {
	crumbs := []output.Breadcrumb{
		{
			Action:      "update",
			Cmd:         fmt.Sprintf("basecamp timesheet entry update %d --hours <hours>%s", entry.ID, scopeArgs),
			Description: "Change entry",
		},
		{
			Action:      "delete",
			Cmd:         timesheetEntryDeleteCmd(cmd, entry.ID, scopeArgs),
			Description: "Delete entry",
		},
	}
	if entry.Parent != nil && entry.Parent.ID != 0 {
		crumbs = append(crumbs, output.Breadcrumb{
			Action:      "item",
			Cmd:         fmt.Sprintf("basecamp timesheet item %d%s", entry.Parent.ID, scopeArgs),
			Description: "View the item's timesheet",
		})
	}
	return crumbs
}

// timesheetEntryDeleteCmd builds the delete breadcrumb for whoever will read
// it, as deleteLineCmd does for chat lines: machine output cannot answer the
// confirmation, so it gets --force; a person gets the bare command and is
// asked, with any flag holding this invocation in a human format carried
// along so the pasted command renders the same way.
func timesheetEntryDeleteCmd(cmd *cobra.Command, entryID int64, scopeArgs string) string {
	c := fmt.Sprintf("basecamp timesheet entry delete %d%s", entryID, scopeArgs)
	if machineReadsThisOutput(cmd) {
		return c + " --force"
	}
	return c + humanFormatOverride(appctx.FromContext(cmd.Context()))
}
