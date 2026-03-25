package commands

import (
	"fmt"

	"github.com/spf13/cobra"

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
	)

	return cmd
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
