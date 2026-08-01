package commands

import (
	"fmt"
	"math"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewAssignmentsCmd creates the assignments command.
func NewAssignmentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "assignments",
		Short: "View my assignments",
		Long: `View your current assignments across all projects.

Shows both priority and non-priority items. Use subcommands to filter
by completion status or due date.`,
		Args: cobra.NoArgs,
		Annotations: map[string]string{
			"agent_notes": "Account-wide — no --in <project> needed.\n" +
				"Shows priorities and non-priorities.\n" +
				"Use 'due overdue' for overdue items, 'completed' for done items.",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAssignmentsList(cmd)
		},
	}

	cmd.AddCommand(
		newAssignmentsListCmd(),
		newAssignmentsCompletedCmd(),
		newAssignmentsDueCmd(),
		newAssignmentsPrioritizeCmd(),
		newAssignmentsDeprioritizeCmd(),
		newAssignmentsReorderCmd(),
	)

	return cmd
}

// upNextIDGuidance explains which id the Up Next verbs take. There are three
// cases, not two, and the difference is invisible in the payload unless you
// know to look: the assignments listing normalizes a prioritized card-table
// step under its parent card, so the entry's top-level id is the *card's*.
const upNextIDGuidance = `Which id to pass:

  A to-do, or a card itself     the entry's own id
  A step not yet prioritized    the step's id, from the parent card's children
  A step already prioritized    the entry's priority_recording_id

That last case is the one that bites: once a step is prioritized the listing
shows it under its parent card, so the entry's id belongs to the card and only
priority_recording_id addresses the step. 'basecamp assignments list' is the
only place that value appears — it is in no URL you can paste.

If two steps on one card are prioritized, the listing shows the card once with
a single priority_recording_id, and the siblings are not separately
addressable.`

func newAssignmentsPrioritizeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prioritize <id|url>",
		Short: "Add an assignment to Up Next",
		Long: `Add an assignment to your Up Next list.

Idempotent: prioritizing something already in Up Next succeeds and changes
nothing.

` + upNextIDGuidance,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAssignmentPriorityVerb(cmd, args[0], "prioritize")
		},
	}
}

func newAssignmentsDeprioritizeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "deprioritize <id|url>",
		Short: "Remove an assignment from Up Next",
		Long: `Remove an assignment from your Up Next list.

This targets one exact recording, and the server answers 204 whether or not
anything matched — so an id that is not in Up Next reports success while
changing nothing. Read the id off 'basecamp assignments list' rather than
guessing it.

` + upNextIDGuidance,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAssignmentPriorityVerb(cmd, args[0], "deprioritize")
		},
	}
}

func newAssignmentsReorderCmd() *cobra.Command {
	var position int

	cmd := &cobra.Command{
		Use:   "reorder <id|url> --position <n>",
		Short: "Move an assignment within Up Next",
		Long: `Move an assignment to a new position in your Up Next list.

Positions are 1-based. This is never retried on a transient failure: replaying
a positional move could land the item somewhere else, so a failure here is a
real failure and safe to retry by hand.

` + upNextIDGuidance,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if !cmd.Flags().Changed("position") {
				return output.ErrUsageHint(
					"--position is required",
					"Pass the 1-based slot to move it to: basecamp assignments reorder <id> --position 1",
				)
			}
			// Bounded rather than clamped: serving a different position than the
			// one asked for would silently move the item somewhere else.
			if position < 1 || position > math.MaxInt32 {
				return output.ErrUsage("--position must be 1 or greater (positions are 1-based)")
			}

			recordingID, err := assignmentRecordingID(args[0])
			if err != nil {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if err := app.Account().MyAssignments().Reorder(cmd.Context(), recordingID, int32(position)); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"id": recordingID, "position": position},
				output.WithSummary(fmt.Sprintf("Moved %d to position %d in Up Next", recordingID, position)),
				output.WithBreadcrumbs(assignmentsListBreadcrumb()),
			)
		},
	}

	cmd.Flags().IntVar(&position, "position", 0, "1-based position in Up Next")

	return cmd
}

// runAssignmentPriorityVerb performs prioritize/deprioritize, which differ only
// in the call and the wording.
func runAssignmentPriorityVerb(cmd *cobra.Command, arg, verb string) error {
	app := appctx.FromContext(cmd.Context())

	recordingID, err := assignmentRecordingID(arg)
	if err != nil {
		return err
	}
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	summary := fmt.Sprintf("Added %d to Up Next", recordingID)
	if verb == "deprioritize" {
		err = app.Account().MyAssignments().Deprioritize(cmd.Context(), recordingID)
		summary = fmt.Sprintf("Removed %d from Up Next", recordingID)
	} else {
		err = app.Account().MyAssignments().Prioritize(cmd.Context(), recordingID)
	}
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(map[string]any{"id": recordingID},
		output.WithSummary(summary),
		output.WithBreadcrumbs(assignmentsListBreadcrumb()),
	)
}

func assignmentsListBreadcrumb() output.Breadcrumb {
	return output.Breadcrumb{
		Action:      "list",
		Cmd:         "basecamp assignments list",
		Description: "See Up Next and its priority_recording_id values",
	}
}

// assignmentRecordingID resolves the <id|url> positional the Up Next verbs take.
func assignmentRecordingID(arg string) (int64, error) {
	id, err := strconv.ParseInt(extractID(arg), 10, 64)
	if err != nil {
		return 0, output.ErrUsageHint(
			fmt.Sprintf("%q is not a recording id or Basecamp URL", arg),
			"Pass a numeric recording id, or paste the recording's Basecamp URL",
		)
	}
	return id, nil
}

func newAssignmentsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List current assignments",
		Long:  "List all current assignments (same as bare 'assignments').",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAssignmentsList(cmd)
		},
	}
}

func runAssignmentsList(cmd *cobra.Command) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	result, err := app.Account().MyAssignments().Get(cmd.Context())
	if err != nil {
		return convertSDKError(err)
	}

	total := len(result.Priorities) + len(result.NonPriorities)
	summary := fmt.Sprintf("%d assignment(s)", total)
	if len(result.Priorities) > 0 {
		summary += fmt.Sprintf(" (%d priority)", len(result.Priorities))
	}

	return app.OK(result,
		output.WithDisplayData(flattenAssignments(result)),
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "completed",
				Cmd:         "basecamp assignments completed",
				Description: "View completed assignments",
			},
			output.Breadcrumb{
				Action:      "due",
				Cmd:         "basecamp assignments due overdue",
				Description: "View overdue assignments",
			},
		),
	)
}

// flattenAssignments builds the display rows for the assignments listing.
//
// The rows exist mainly to carry priority_recording_id, which is what
// 'assignments reorder' and 'assignments deprioritize' need to address a
// prioritized card-table step. That value appears in no URL and in no other
// command's output, so a listing that omits it leaves those two verbs with no
// way to name their target — they would report a successful 204 while changing
// nothing. The project comes along for the same reason it does on every other
// account-wide listing: without it a cross-project row is unattributable.
func flattenAssignments(result *basecamp.MyAssignmentsResult) []map[string]any {
	if result == nil {
		return nil
	}

	rows := make([]map[string]any, 0, len(result.Priorities)+len(result.NonPriorities))
	rows = appendAssignmentRows(rows, result.Priorities, true)
	rows = appendAssignmentRows(rows, result.NonPriorities, false)
	return rows
}

func appendAssignmentRows(rows []map[string]any, items []basecamp.MyAssignment, priority bool) []map[string]any {
	for _, item := range items {
		row := map[string]any{
			"id":      item.ID,
			"content": item.Content,
			"type":    item.Type,
			"project": item.Bucket.Name,
			"due_on":  item.DueOn,
			"up_next": priority,
		}
		// Present only once the step or card has been prioritized, and the one
		// id that addresses it thereafter.
		if item.PriorityRecordingID != nil {
			row["priority_recording_id"] = *item.PriorityRecordingID
		}
		rows = append(rows, row)
	}
	return rows
}

func newAssignmentsCompletedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "completed",
		Short: "View completed assignments",
		Long:  "View your recently completed assignments.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			items, err := app.Account().MyAssignments().Completed(cmd.Context())
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(items,
				output.WithSummary(fmt.Sprintf("%d completed assignment(s)", len(items))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "current",
						Cmd:         "basecamp assignments",
						Description: "View current assignments",
					},
				),
			)
		},
	}
}

func newAssignmentsDueCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "due [scope]",
		Short: "View assignments by due date",
		Long: `View assignments filtered by due date scope.

Scopes: overdue, due_today, due_tomorrow, due_later_this_week, due_next_week, due_later.

  basecamp assignments due overdue
  basecamp assignments due due_today`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			scope := ""
			if len(args) > 0 {
				scope = args[0]
			}

			items, err := app.Account().MyAssignments().Due(cmd.Context(), scope)
			if err != nil {
				return convertSDKError(err)
			}

			label := "due assignment(s)"
			if scope != "" {
				label = scope + " assignment(s)"
			}

			return app.OK(items,
				output.WithSummary(fmt.Sprintf("%d %s", len(items), label)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "all",
						Cmd:         "basecamp assignments",
						Description: "View all assignments",
					},
				),
			)
		},
	}
}
