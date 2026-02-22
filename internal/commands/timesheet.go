package commands

import (
	"fmt"
	"strconv"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/dateparse"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewTimesheetCmd creates the timesheet command for managing time tracking.
func NewTimesheetCmd() *cobra.Command {
	var startDate string
	var endDate string
	var personID string
	var bucketID string

	cmd := &cobra.Command{
		Use:   "timesheet",
		Short: "Manage time tracking",
		Long: `Manage time tracking.

Timesheet entries track time logged against any recording (todo, message,
document, etc.). The account-wide report defaults to the last month if no
date range is specified.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTimesheetReport(cmd, startDate, endDate, personID, bucketID)
		},
	}

	cmd.PersistentFlags().StringVar(&startDate, "start", "", "Start date (ISO 8601, e.g., 2024-01-01)")
	cmd.PersistentFlags().StringVar(&startDate, "from", "", "Start date (alias for --start)")
	cmd.PersistentFlags().StringVar(&endDate, "end", "", "End date (ISO 8601)")
	cmd.PersistentFlags().StringVar(&endDate, "to", "", "End date (alias for --end)")
	cmd.PersistentFlags().StringVar(&personID, "person", "", "Filter by person ID")
	cmd.PersistentFlags().StringVar(&bucketID, "bucket", "", "Filter by project/bucket ID")

	cmd.AddCommand(
		newTimesheetReportCmd(&startDate, &endDate, &personID, &bucketID),
		newTimesheetProjectCmd(),
		newTimesheetRecordingCmd(),
		newTimesheetEntryCmd(),
	)

	return cmd
}

func newTimesheetReportCmd(startDate, endDate, personID, bucketID *string) *cobra.Command {
	return &cobra.Command{
		Use:   "report",
		Short: "View account-wide timesheet report",
		Long:  "View account-wide timesheet report with optional filters.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTimesheetReport(cmd, *startDate, *endDate, *personID, *bucketID)
		},
	}
}

func runTimesheetReport(cmd *cobra.Command, startDate, endDate, personID, bucketID string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Validate: if one date is provided, both are required
	if startDate != "" && endDate == "" {
		return output.ErrUsage("--end required when --start is provided")
	}
	if endDate != "" && startDate == "" {
		return output.ErrUsage("--start required when --end is provided")
	}

	// Build options
	opts := &basecamp.TimesheetReportOptions{
		From: startDate,
		To:   endDate,
	}
	if personID != "" {
		pid, err := strconv.ParseInt(personID, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid person ID")
		}
		opts.PersonID = pid
	}

	// If bucketID is provided, use ProjectReport instead of Report
	if bucketID != "" {
		bid, err := strconv.ParseInt(bucketID, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid bucket ID")
		}
		entries, err := app.Account().Timesheet().ProjectReport(cmd.Context(), bid, opts)
		if err != nil {
			return convertSDKError(err)
		}
		totalHours := sumTimesheetHours(entries)
		return app.OK(entries,
			output.WithSummary(fmt.Sprintf("%d timesheet entries (%.1fh total)", len(entries), totalHours)),
			output.WithBreadcrumbs(
				output.Breadcrumb{
					Action:      "create",
					Cmd:         "basecamp timesheet entry create --recording <id> --hours <hours> --date <date> --project <project>",
					Description: "Log time",
				},
				output.Breadcrumb{
					Action:      "project",
					Cmd:         "basecamp timesheet project <id>",
					Description: "View project timesheet",
				},
				output.Breadcrumb{
					Action:      "recording",
					Cmd:         "basecamp timesheet recording <id> --project <project>",
					Description: "View recording timesheet",
				},
			),
		)
	}

	entries, err := app.Account().Timesheet().Report(cmd.Context(), opts)
	if err != nil {
		return convertSDKError(err)
	}

	totalHours := sumTimesheetHours(entries)

	return app.OK(entries,
		output.WithSummary(fmt.Sprintf("%d timesheet entries (%.1fh total)", len(entries), totalHours)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "create",
				Cmd:         "basecamp timesheet entry create --recording <id> --hours <hours> --date <date>",
				Description: "Log time",
			},
			output.Breadcrumb{
				Action:      "project",
				Cmd:         "basecamp timesheet project <id>",
				Description: "View project timesheet",
			},
			output.Breadcrumb{
				Action:      "recording",
				Cmd:         "basecamp timesheet recording <id> --project <project>",
				Description: "View recording timesheet",
			},
		),
	)
}

func sumTimesheetHours(entries []basecamp.TimesheetEntry) float64 {
	var total float64
	for _, e := range entries {
		var hours float64
		_, _ = fmt.Sscanf(e.Hours, "%f", &hours) //nolint:gosec // G104: Hours string from API
		total += hours
	}
	return total
}

func newTimesheetProjectCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "project [id]",
		Short: "View project timesheet",
		Long:  "View timesheet entries for a specific project.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Project from positional arg or flag
			if len(args) > 0 && project == "" {
				project = args[0]
			}

			// Resolve project, with interactive fallback
			projectID := project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			entries, err := app.Account().Timesheet().ProjectReport(cmd.Context(), bucketID, nil)
			if err != nil {
				return convertSDKError(err)
			}

			totalHours := sumTimesheetHours(entries)

			return app.OK(entries,
				output.WithSummary(fmt.Sprintf("%d entries (%.1fh total)", len(entries), totalHours)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "create",
						Cmd:         fmt.Sprintf("basecamp timesheet entry create --recording <id> --hours <hours> --date <date> --project %s", resolvedProjectID),
						Description: "Log time",
					},
					output.Breadcrumb{
						Action:      "report",
						Cmd:         "basecamp timesheet report",
						Description: "View account-wide report",
					},
					output.Breadcrumb{
						Action:      "recording",
						Cmd:         fmt.Sprintf("basecamp timesheet recording <id> --project %s", resolvedProjectID),
						Description: "View recording timesheet",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	return cmd
}

func newTimesheetRecordingCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "recording <id>",
		Short: "View recording timesheet",
		Long:  "View timesheet entries for a specific recording.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			recordingIDStr := args[0]
			recordingID, err := strconv.ParseInt(recordingIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid recording ID")
			}

			// Resolve project, with interactive fallback
			projectID := project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			entries, err := app.Account().Timesheet().RecordingReport(cmd.Context(), bucketID, recordingID, nil)
			if err != nil {
				return convertSDKError(err)
			}

			totalHours := sumTimesheetHours(entries)

			return app.OK(entries,
				output.WithSummary(fmt.Sprintf("%d entries (%.1fh total)", len(entries), totalHours)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "create",
						Cmd:         fmt.Sprintf("basecamp timesheet entry create --recording %s --hours <hours> --date <date> --project %s", recordingIDStr, resolvedProjectID),
						Description: "Log time",
					},
					output.Breadcrumb{
						Action:      "project",
						Cmd:         fmt.Sprintf("basecamp timesheet project %s", resolvedProjectID),
						Description: "View project timesheet",
					},
					output.Breadcrumb{
						Action:      "report",
						Cmd:         "basecamp timesheet report",
						Description: "View account-wide report",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	return cmd
}

// --- Entry subcommands ---

func newTimesheetEntryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "entry",
		Short: "Manage timesheet entries",
		Long:  "Create, view, update, and trash individual timesheet entries.",
	}

	cmd.AddCommand(
		newTimesheetEntryShowCmd(),
		newTimesheetEntryCreateCmd(),
		newTimesheetEntryUpdateCmd(),
		newTimesheetEntryTrashCmd(),
	)

	return cmd
}

func newTimesheetEntryShowCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show a timesheet entry",
		Long: `Show details for a timesheet entry.

You can pass either an entry ID or a Basecamp URL:
  basecamp timesheet entry show 789 --project my-project
  basecamp timesheet entry show https://3.basecamp.com/123/buckets/456/timesheet_entries/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			entryID, urlProjectID := extractWithProject(args[0])

			projectID := project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			bucketID, _ := strconv.ParseInt(resolvedProjectID, 10, 64)
			entryIDInt, err := strconv.ParseInt(entryID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid entry ID")
			}

			entry, err := app.Account().Timesheet().Get(cmd.Context(), bucketID, entryIDInt)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(entry,
				output.WithSummary(fmt.Sprintf("Timesheet entry #%s: %sh on %s", entryID, entry.Hours, entry.Date)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("basecamp timesheet entry update %s --project %s", entryID, resolvedProjectID),
						Description: "Update entry",
					},
					output.Breadcrumb{
						Action:      "trash",
						Cmd:         fmt.Sprintf("basecamp timesheet entry trash %s --project %s", entryID, resolvedProjectID),
						Description: "Trash entry",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	return cmd
}

func newTimesheetEntryCreateCmd() *cobra.Command {
	var project string
	var recording string
	var hours string
	var date string
	var description string
	var personID string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a timesheet entry",
		Long: `Create a new timesheet entry on a recording.

Log time against any recording (todo, message, document, etc.):
  basecamp timesheet entry create --recording 789 --hours 1.5 --date today --project my-project
  basecamp timesheet entry create -r https://3.basecamp.com/123/buckets/456/todos/789 --hours 2:30 --date 2024-01-15

Hours can be decimal (1.5) or time format (1:30).
Dates support natural language: today, tomorrow, yesterday, monday, etc.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if hours == "" {
				return output.ErrUsage("--hours required")
			}
			if date == "" {
				return output.ErrUsage("--date required")
			}
			if recording == "" {
				return output.ErrUsage("--recording required")
			}

			return runTimesheetCreate(cmd, project, recording, hours, date, description, personID,
				func(entry *basecamp.TimesheetEntry) string {
					return fmt.Sprintf("Created timesheet entry #%d: %sh on %s", entry.ID, entry.Hours, entry.Date)
				},
			)
		},
	}

	cmd.Flags().StringVarP(&recording, "recording", "r", "", "Recording ID or URL to log time against")
	cmd.Flags().StringVar(&recording, "on", "", "Recording ID or URL (alias for --recording)")
	cmd.Flags().StringVar(&hours, "hours", "", "Hours (decimal \"1.5\" or time \"1:30\")")
	cmd.Flags().StringVarP(&date, "date", "d", "", "Date (ISO 8601 or natural language)")
	cmd.Flags().StringVar(&description, "description", "", "Description")
	cmd.Flags().StringVar(&description, "desc", "", "Description (alias)")
	cmd.Flags().StringVar(&personID, "person", "", "Person ID (defaults to authenticated user)")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	return cmd
}

func runTimesheetCreate(cmd *cobra.Command, project, recording, hours, date, description, personID string, summaryFn func(*basecamp.TimesheetEntry) string) error {
	app := appctx.FromContext(cmd.Context())
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	recordingID, urlProjectID := extractWithProject(recording)

	projectID := project
	if projectID == "" && urlProjectID != "" {
		projectID = urlProjectID
	}
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	bucketID, _ := strconv.ParseInt(resolvedProjectID, 10, 64)
	recordingIDInt, err := strconv.ParseInt(recordingID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid recording ID")
	}

	parsedDate := dateparse.Parse(date)

	req := &basecamp.CreateTimesheetEntryRequest{
		Date:        parsedDate,
		Hours:       hours,
		Description: description,
	}
	if personID != "" {
		pid, err := strconv.ParseInt(personID, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid person ID")
		}
		req.PersonID = pid
	}

	entry, err := app.Account().Timesheet().Create(cmd.Context(), bucketID, recordingIDInt, req)
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(entry,
		output.WithSummary(summaryFn(entry)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp timesheet entry show %d --project %s", entry.ID, resolvedProjectID),
				Description: "View entry",
			},
			output.Breadcrumb{
				Action:      "update",
				Cmd:         fmt.Sprintf("basecamp timesheet entry update %d --project %s", entry.ID, resolvedProjectID),
				Description: "Update entry",
			},
			output.Breadcrumb{
				Action:      "trash",
				Cmd:         fmt.Sprintf("basecamp timesheet entry trash %d --project %s", entry.ID, resolvedProjectID),
				Description: "Trash entry",
			},
		),
	)
}

func newTimesheetEntryUpdateCmd() *cobra.Command {
	var project string
	var hours string
	var date string
	var description string
	var personID string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a timesheet entry",
		Long: `Update an existing timesheet entry.

You can pass either an entry ID or a Basecamp URL:
  basecamp timesheet entry update 789 --hours 2.0 --project my-project
  basecamp timesheet entry update https://3.basecamp.com/123/buckets/456/timesheet_entries/789 --date tomorrow`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			entryID, urlProjectID := extractWithProject(args[0])

			projectID := project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			bucketID, _ := strconv.ParseInt(resolvedProjectID, 10, 64)
			entryIDInt, err := strconv.ParseInt(entryID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid entry ID")
			}

			req := &basecamp.UpdateTimesheetEntryRequest{}
			hasChanges := false

			if hours != "" {
				req.Hours = hours
				hasChanges = true
			}
			if date != "" {
				req.Date = dateparse.Parse(date)
				hasChanges = true
			}
			if description != "" {
				req.Description = description
				hasChanges = true
			}
			if personID != "" {
				pid, err := strconv.ParseInt(personID, 10, 64)
				if err != nil {
					return output.ErrUsage("Invalid person ID")
				}
				req.PersonID = pid
				hasChanges = true
			}

			if !hasChanges {
				return output.ErrUsage("No update fields provided")
			}

			entry, err := app.Account().Timesheet().Update(cmd.Context(), bucketID, entryIDInt, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(entry,
				output.WithSummary(fmt.Sprintf("Updated timesheet entry #%s", entryID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp timesheet entry show %s --project %s", entryID, resolvedProjectID),
						Description: "View entry",
					},
					output.Breadcrumb{
						Action:      "trash",
						Cmd:         fmt.Sprintf("basecamp timesheet entry trash %s --project %s", entryID, resolvedProjectID),
						Description: "Trash entry",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&hours, "hours", "", "Hours (decimal \"1.5\" or time \"1:30\")")
	cmd.Flags().StringVarP(&date, "date", "d", "", "Date (ISO 8601 or natural language)")
	cmd.Flags().StringVar(&description, "description", "", "Description")
	cmd.Flags().StringVar(&description, "desc", "", "Description (alias)")
	cmd.Flags().StringVar(&personID, "person", "", "Person ID")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	return cmd
}

func newTimesheetEntryTrashCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "trash <id|url>",
		Short: "Trash a timesheet entry",
		Long: `Move a timesheet entry to the trash.

You can pass either an entry ID or a Basecamp URL:
  basecamp timesheet entry trash 789 --project my-project
  basecamp timesheet entry trash https://3.basecamp.com/123/buckets/456/timesheet_entries/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			entryID, urlProjectID := extractWithProject(args[0])

			projectID := project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			bucketID, _ := strconv.ParseInt(resolvedProjectID, 10, 64)
			entryIDInt, err := strconv.ParseInt(entryID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid entry ID")
			}

			err = app.Account().Timesheet().Trash(cmd.Context(), bucketID, entryIDInt)
			if err != nil {
				return convertSDKError(err)
			}

			result := map[string]any{
				"trashed": true,
				"id":      entryID,
			}

			return app.OK(result,
				output.WithSummary(fmt.Sprintf("Trashed timesheet entry #%s", entryID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "report",
						Cmd:         "basecamp timesheet report",
						Description: "View timesheet report",
					},
					output.Breadcrumb{
						Action:      "create",
						Cmd:         "basecamp timesheet entry create --recording <id> --hours <hours> --date <date>",
						Description: "Log time",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	return cmd
}

// --- Clock shortcut ---

// NewClockCmd creates the clock shortcut command for quickly logging time.
func NewClockCmd() *cobra.Command {
	var project string
	var recording string
	var date string
	var description string
	var personID string

	cmd := &cobra.Command{
		Use:   "clock <hours>",
		Short: "Log time",
		Long: `Log time against a recording (shortcut for timesheet entry create).

Hours as positional argument, --date defaults to today:
  basecamp clock 1.5 --on 789 --project my-project
  basecamp clock 2:30 --on https://3.basecamp.com/123/buckets/456/todos/789 --date yesterday

Hours can be decimal (1.5) or time format (1:30).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hours := args[0]

			if recording == "" {
				return output.ErrUsage("--on required")
			}

			// Default date to today
			if date == "" {
				date = time.Now().Format("2006-01-02")
			}

			return runTimesheetCreate(cmd, project, recording, hours, date, description, personID,
				func(entry *basecamp.TimesheetEntry) string {
					return fmt.Sprintf("Logged %sh on %s", entry.Hours, entry.Date)
				},
			)
		},
	}

	cmd.Flags().StringVarP(&recording, "on", "r", "", "Recording ID or URL to log time against")
	cmd.Flags().StringVar(&recording, "recording", "", "Recording ID or URL (alias for --on)")
	cmd.Flags().StringVarP(&date, "date", "d", "", "Date (default: today)")
	cmd.Flags().StringVar(&description, "description", "", "Description")
	cmd.Flags().StringVar(&description, "desc", "", "Description (alias)")
	cmd.Flags().StringVar(&personID, "person", "", "Person ID (defaults to authenticated user)")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	return cmd
}
