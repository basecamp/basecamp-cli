package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/dateparse"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewReportsCmd creates the reports command for viewing various reports.
func NewReportsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reports",
		Short: "View reports",
		Long: `View various reports including assignable people, assigned todos, overdue todos, and upcoming schedule.

Reports provide cross-project views of assignments and schedules.`,
	}

	cmd.AddCommand(
		newReportsAssignableCmd(),
		newReportsAssignedCmd(),
		newReportsOverdueCmd(),
		newReportsScheduleCmd(),
	)

	return cmd
}

func newReportsAssignableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "assignable",
		Short: "List people who can be assigned todos",
		Long:  "List all people in the account who can be assigned to todos.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			people, err := app.Account().Reports().AssignablePeople(cmd.Context())
			if err != nil {
				return convertSDKError(err)
			}

			summary := fmt.Sprintf("%d assignable people", len(people))

			return app.OK(people,
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "assigned",
						Cmd:         "bcq reports assigned <person>",
						Description: "View todos assigned to a person",
					},
					output.Breadcrumb{
						Action:      "people",
						Cmd:         "bcq people list",
						Description: "List all people",
					},
				),
			)
		},
	}
}

func newReportsAssignedCmd() *cobra.Command {
	var groupBy string

	cmd := &cobra.Command{
		Use:   "assigned [person]",
		Short: "View todos assigned to a person",
		Long: `View todos assigned to a specific person.

If no person is specified, defaults to "me" (the current user).
Results can be grouped by bucket (project) or date.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Default to "me" if no person specified
			person := "me"
			if len(args) > 0 {
				person = args[0]
			}

			// Resolve person name/ID
			personIDStr, personName, err := app.Names.ResolvePerson(cmd.Context(), person)
			if err != nil {
				return err
			}

			personID, err := strconv.ParseInt(personIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid person ID")
			}

			// Build options
			var opts *basecamp.AssignedTodosOptions
			if groupBy != "" {
				if groupBy != "bucket" && groupBy != "date" {
					return output.ErrUsage("--group-by must be 'bucket' or 'date'")
				}
				opts = &basecamp.AssignedTodosOptions{GroupBy: groupBy}
			}

			result, err := app.Account().Reports().AssignedTodos(cmd.Context(), personID, opts)
			if err != nil {
				return convertSDKError(err)
			}

			// Build summary
			todoCount := len(result.Todos)
			displayName := personName
			if displayName == "" && result.Person != nil {
				displayName = result.Person.Name
			}
			if displayName == "" {
				displayName = personIDStr
			}

			summary := fmt.Sprintf("%d todos assigned to %s", todoCount, displayName)
			if result.GroupedBy != "" {
				summary += fmt.Sprintf(" (grouped by %s)", result.GroupedBy)
			}

			return app.OK(result,
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "overdue",
						Cmd:         "bcq reports overdue",
						Description: "View overdue todos",
					},
					output.Breadcrumb{
						Action:      "todos",
						Cmd:         "bcq todos --in <project> --assignee " + personIDStr,
						Description: "List todos in a specific project",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&groupBy, "group-by", "", "Group results by 'bucket' or 'date'")

	return cmd
}

func newReportsOverdueCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "overdue",
		Short: "View overdue todos grouped by lateness",
		Long: `View all overdue todos grouped by how late they are.

Todos are grouped into categories:
  - Under a week late
  - Over a week late
  - Over a month late
  - Over three months late`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			result, err := app.Account().Reports().OverdueTodos(cmd.Context())
			if err != nil {
				return convertSDKError(err)
			}

			// Count total overdue todos
			total := len(result.UnderAWeekLate) +
				len(result.OverAWeekLate) +
				len(result.OverAMonthLate) +
				len(result.OverThreeMonthsLate)

			var summary strings.Builder
			summary.WriteString(fmt.Sprintf("%d overdue todos", total))
			if total > 0 {
				var parts []string
				if n := len(result.UnderAWeekLate); n > 0 {
					parts = append(parts, fmt.Sprintf("%d <1 week", n))
				}
				if n := len(result.OverAWeekLate); n > 0 {
					parts = append(parts, fmt.Sprintf("%d >1 week", n))
				}
				if n := len(result.OverAMonthLate); n > 0 {
					parts = append(parts, fmt.Sprintf("%d >1 month", n))
				}
				if n := len(result.OverThreeMonthsLate); n > 0 {
					parts = append(parts, fmt.Sprintf("%d >3 months", n))
				}
				if len(parts) > 0 {
					summary.WriteString(" (")
					for i, p := range parts {
						if i > 0 {
							summary.WriteString(", ")
						}
						summary.WriteString(p)
					}
					summary.WriteString(")")
				}
			}

			return app.OK(result,
				output.WithSummary(summary.String()),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "assigned",
						Cmd:         "bcq reports assigned",
						Description: "View your assigned todos",
					},
					output.Breadcrumb{
						Action:      "schedule",
						Cmd:         "bcq reports schedule",
						Description: "View upcoming schedule",
					},
				),
			)
		},
	}
}

func newReportsScheduleCmd() *cobra.Command {
	var startDate string
	var endDate string

	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "View upcoming schedule entries",
		Long: `View upcoming schedule entries and assignables within a date window.

By default shows the upcoming schedule. Use --start and --end to specify a date range.
Dates can be natural language (e.g., "today", "next week", "+7") or YYYY-MM-DD format.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Parse dates if provided (dateparse handles natural language like "today", "+7")
			// Unrecognized formats are normalized (trimmed/lowercased) and passed through for the API to validate
			parsedStart := dateparse.Parse(startDate)
			parsedEnd := dateparse.Parse(endDate)

			result, err := app.Account().Reports().UpcomingSchedule(cmd.Context(), parsedStart, parsedEnd)
			if err != nil {
				return convertSDKError(err)
			}

			// Count items
			entryCount := len(result.ScheduleEntries)
			recurringCount := len(result.RecurringOccurrences)
			assignableCount := len(result.Assignables)
			total := entryCount + recurringCount + assignableCount

			// Build summary
			var parts []string
			if entryCount > 0 {
				parts = append(parts, fmt.Sprintf("%d entries", entryCount))
			}
			if recurringCount > 0 {
				parts = append(parts, fmt.Sprintf("%d recurring", recurringCount))
			}
			if assignableCount > 0 {
				parts = append(parts, fmt.Sprintf("%d assignables", assignableCount))
			}

			var summary strings.Builder
			summary.WriteString(fmt.Sprintf("%d upcoming items", total))
			if len(parts) > 0 {
				summary.WriteString(" (")
				for i, p := range parts {
					if i > 0 {
						summary.WriteString(", ")
					}
					summary.WriteString(p)
				}
				summary.WriteString(")")
			}

			return app.OK(result,
				output.WithSummary(summary.String()),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "overdue",
						Cmd:         "bcq reports overdue",
						Description: "View overdue todos",
					},
					output.Breadcrumb{
						Action:      "schedule",
						Cmd:         "bcq schedule entries --project <project>",
						Description: "View project schedule",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&startDate, "start", "", "Start date (e.g., today, next week, 2024-01-15)")
	cmd.Flags().StringVar(&endDate, "end", "", "End date (e.g., +30, eom, 2024-02-15)")

	return cmd
}
