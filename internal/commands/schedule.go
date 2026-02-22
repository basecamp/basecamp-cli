package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewScheduleCmd creates the schedule command for managing schedules.
func NewScheduleCmd() *cobra.Command {
	var project string
	var scheduleID string

	cmd := &cobra.Command{
		Use:   "schedule [action]",
		Short: "Manage schedules and entries",
		Long: `Manage project schedules and schedule entries.

Use 'basecamp schedule' to view the project schedule.
Use 'basecamp schedule entries' to list schedule entries.
Use 'basecamp schedule create' to create new entries.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Handle numeric ID as first arg: basecamp schedule 123
			if len(args) > 0 && isNumeric(args[0]) {
				return runScheduleEntryShow(cmd, app, args[0], project, "")
			}

			// Default to schedule show
			return runScheduleShow(cmd, app, project, scheduleID)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVarP(&scheduleID, "schedule", "s", "", "Schedule ID (auto-detected)")

	cmd.AddCommand(
		newScheduleShowCmd(&project, &scheduleID),
		newScheduleEntriesCmd(&project, &scheduleID),
		newScheduleEntryShowCmd(&project),
		newScheduleCreateCmd(&project, &scheduleID),
		newScheduleUpdateCmd(&project),
		newScheduleSettingsCmd(&project, &scheduleID),
	)

	return cmd
}

func newScheduleShowCmd(project, scheduleID *string) *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show schedule info",
		Long:  "Display project schedule information.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			// Resolve account (enables interactive prompt if needed)
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			return runScheduleShow(cmd, app, *project, *scheduleID)
		},
	}
}

func runScheduleShow(cmd *cobra.Command, app *appctx.App, project, scheduleID string) error {
	// Resolve project from CLI flags and config, with interactive fallback
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}

	// If no project specified, try interactive resolution
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

	// Get schedule ID from dock if not specified
	if scheduleID == "" {
		scheduleID, err = getScheduleID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	bucketID, _ := strconv.ParseInt(resolvedProjectID, 10, 64)
	scheduleIDInt, _ := strconv.ParseInt(scheduleID, 10, 64)

	schedule, err := app.Account().Schedules().Get(cmd.Context(), bucketID, scheduleIDInt)
	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("%d entries (include due assignments: %t)", schedule.EntriesCount, schedule.IncludeDueAssignments)

	return app.OK(schedule,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "entries",
				Cmd:         fmt.Sprintf("basecamp schedule entries --project %s", resolvedProjectID),
				Description: "View schedule entries",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("basecamp schedule create \"Event\" --starts-at <datetime> --ends-at <datetime> --project %s", resolvedProjectID),
				Description: "Create entry",
			},
		),
	)
}

func newScheduleEntriesCmd(project, scheduleID *string) *cobra.Command {
	var status string
	var limit int
	var page int
	var all bool

	cmd := &cobra.Command{
		Use:   "entries",
		Short: "List schedule entries",
		Long:  "List all entries in a project schedule.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			// Resolve account (enables interactive prompt if needed)
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			return runScheduleEntries(cmd, app, *project, *scheduleID, status, limit, page, all)
		},
	}

	cmd.Flags().StringVar(&status, "status", "active", "Filter by status (active, archived, trashed)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of entries to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all entries (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Disable pagination and return first page only")

	return cmd
}

func runScheduleEntries(cmd *cobra.Command, app *appctx.App, project, scheduleID, status string, limit, page int, all bool) error {
	// Validate flag combinations
	if all && limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if page > 0 && (all || limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}
	if page > 1 {
		return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
	}

	// Resolve project from CLI flags and config, with interactive fallback
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}

	// If no project specified, try interactive resolution
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

	// Get schedule ID from dock if not specified
	if scheduleID == "" {
		scheduleID, err = getScheduleID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	bucketID, _ := strconv.ParseInt(resolvedProjectID, 10, 64)
	scheduleIDInt, _ := strconv.ParseInt(scheduleID, 10, 64)

	// Build pagination options
	opts := &basecamp.ScheduleEntryListOptions{}
	if all {
		opts.Limit = -1 // SDK treats -1 as "fetch all"
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}
	if status != "" {
		opts.Status = status
	}

	entriesResult, err := app.Account().Schedules().ListEntries(cmd.Context(), bucketID, scheduleIDInt, opts)
	if err != nil {
		return convertSDKError(err)
	}
	entries := entriesResult.Entries

	summary := fmt.Sprintf("%d schedule entries", len(entries))

	return app.OK(entries,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp schedule show <id> --project %s", resolvedProjectID),
				Description: "View entry details",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("basecamp schedule create \"Event\" --starts-at <datetime> --ends-at <datetime> --project %s", resolvedProjectID),
				Description: "Create entry",
			},
		),
	)
}

func newScheduleEntryShowCmd(project *string) *cobra.Command {
	var occurrenceDate string

	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show schedule entry",
		Long: `Display details of a schedule entry.

You can pass either an entry ID or a Basecamp URL:
  basecamp schedule show 789 --in my-project
  basecamp schedule show https://3.basecamp.com/123/buckets/456/schedule_entries/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			return runScheduleEntryShow(cmd, app, args[0], *project, occurrenceDate)
		},
	}

	cmd.Flags().StringVar(&occurrenceDate, "date", "", "Access specific occurrence of recurring entry (YYYYMMDD)")
	cmd.Flags().StringVar(&occurrenceDate, "occurrence", "", "Access specific occurrence (alias for --date)")

	return cmd
}

func runScheduleEntryShow(cmd *cobra.Command, app *appctx.App, entryID, project, occurrenceDate string) error {
	// Extract ID and project from URL if provided
	extractedID, urlProjectID := extractWithProject(entryID)
	entryID = extractedID

	// Resolve project - use URL > flag > config, with interactive fallback
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
	entryIDInt, _ := strconv.ParseInt(entryID, 10, 64)

	// Use SDK method for specific occurrences of recurring entries
	if occurrenceDate != "" {
		entry, err := app.Account().Schedules().GetEntryOccurrence(cmd.Context(), bucketID, entryIDInt, occurrenceDate)
		if err != nil {
			return convertSDKError(err)
		}

		title := entry.Summary
		if title == "" {
			title = entry.Title
		}
		if title == "" {
			title = "Entry"
		}

		summary := fmt.Sprintf("%s: %s -> %s", title, entry.StartsAt.Format("2006-01-02 15:04"), entry.EndsAt.Format("2006-01-02 15:04"))

		return app.OK(entry,
			output.WithSummary(summary),
			output.WithBreadcrumbs(
				output.Breadcrumb{
					Action:      "update",
					Cmd:         fmt.Sprintf("basecamp schedule update %s --summary \"...\" --project %s", entryID, resolvedProjectID),
					Description: "Update entry",
				},
				output.Breadcrumb{
					Action:      "entries",
					Cmd:         fmt.Sprintf("basecamp schedule entries --project %s", resolvedProjectID),
					Description: "View all entries",
				},
			),
		)
	}

	entry, err := app.Account().Schedules().GetEntry(cmd.Context(), bucketID, entryIDInt)
	if err != nil {
		return convertSDKError(err)
	}

	title := entry.Summary
	if title == "" {
		title = entry.Title
	}
	if title == "" {
		title = "Entry"
	}

	summary := fmt.Sprintf("%s: %s -> %s", title, entry.StartsAt.Format("2006-01-02 15:04"), entry.EndsAt.Format("2006-01-02 15:04"))

	return app.OK(entry,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "update",
				Cmd:         fmt.Sprintf("basecamp schedule update %s --summary \"...\" --project %s", entryID, resolvedProjectID),
				Description: "Update entry",
			},
			output.Breadcrumb{
				Action:      "entries",
				Cmd:         fmt.Sprintf("basecamp schedule entries --project %s", resolvedProjectID),
				Description: "View all entries",
			},
		),
	)
}

func newScheduleCreateCmd(project, scheduleID *string) *cobra.Command {
	var summary string
	var startsAt string
	var endsAt string
	var description string
	var allDay bool
	var notify bool
	var participants string

	cmd := &cobra.Command{
		Use:   "create [summary]",
		Short: "Create a schedule entry",
		Long:  "Create a new entry in the project schedule.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			entrySummary := summary
			if len(args) > 0 {
				entrySummary = args[0]
			}

			if entrySummary == "" {
				return output.ErrUsage("Summary required")
			}
			if startsAt == "" {
				return output.ErrUsage("--starts-at required (ISO 8601 datetime)")
			}
			if endsAt == "" {
				return output.ErrUsage("--ends-at required (ISO 8601 datetime)")
			}

			return runScheduleCreate(cmd, app, *project, *scheduleID, entrySummary, startsAt, endsAt, description, allDay, notify, participants)
		},
	}

	cmd.Flags().StringVar(&summary, "summary", "", "Event title/summary")
	cmd.Flags().StringVar(&summary, "title", "", "Event title (alias for --summary)")
	cmd.Flags().StringVar(&startsAt, "starts-at", "", "Start time (ISO 8601)")
	cmd.Flags().StringVar(&startsAt, "start", "", "Start time (alias)")
	cmd.Flags().StringVar(&endsAt, "ends-at", "", "End time (ISO 8601)")
	cmd.Flags().StringVar(&endsAt, "end", "", "End time (alias)")
	cmd.Flags().StringVar(&description, "description", "", "Detailed description")
	cmd.Flags().StringVar(&description, "desc", "", "Description (alias)")
	cmd.Flags().BoolVar(&allDay, "all-day", false, "Mark as all-day event")
	cmd.Flags().BoolVar(&notify, "notify", false, "Notify participants")
	cmd.Flags().StringVar(&participants, "participants", "", "Comma-separated person IDs")
	cmd.Flags().StringVar(&participants, "people", "", "Person IDs (alias)")

	return cmd
}

func runScheduleCreate(cmd *cobra.Command, app *appctx.App, project, scheduleID, summary, startsAt, endsAt, description string, allDay, notify bool, participants string) error {
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

	// Get schedule ID from dock if not specified
	if scheduleID == "" {
		scheduleID, err = getScheduleID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	bucketID, _ := strconv.ParseInt(resolvedProjectID, 10, 64)
	scheduleIDInt, _ := strconv.ParseInt(scheduleID, 10, 64)

	// Build request
	req := &basecamp.CreateScheduleEntryRequest{
		Summary:     summary,
		StartsAt:    startsAt,
		EndsAt:      endsAt,
		Description: description,
		AllDay:      allDay,
		Notify:      notify,
	}

	if participants != "" {
		var ids []int64
		for idStr := range strings.SplitSeq(participants, ",") {
			idStr = strings.TrimSpace(idStr)
			if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			req.ParticipantIDs = ids
		}
	}

	entry, err := app.Account().Schedules().CreateEntry(cmd.Context(), bucketID, scheduleIDInt, req)
	if err != nil {
		return convertSDKError(err)
	}

	resultSummary := fmt.Sprintf("Created schedule entry #%d: %s", entry.ID, summary)

	return app.OK(entry,
		output.WithSummary(resultSummary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp schedule show %d --project %s", entry.ID, resolvedProjectID),
				Description: "View entry",
			},
			output.Breadcrumb{
				Action:      "entries",
				Cmd:         fmt.Sprintf("basecamp schedule entries --project %s", resolvedProjectID),
				Description: "View all entries",
			},
		),
	)
}

func newScheduleUpdateCmd(project *string) *cobra.Command {
	var summary string
	var startsAt string
	var endsAt string
	var description string
	var allDay bool
	var notify bool
	var participants string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a schedule entry",
		Long: `Update an existing schedule entry.

You can pass either an entry ID or a Basecamp URL:
  basecamp schedule update 789 --summary "new title" --in my-project
  basecamp schedule update https://3.basecamp.com/123/buckets/456/schedule_entries/789 --summary "new title"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			entryID, urlProjectID := extractWithProject(args[0])

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
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
			entryIDInt, _ := strconv.ParseInt(entryID, 10, 64)

			// Build request with provided fields only
			req := &basecamp.UpdateScheduleEntryRequest{}
			hasChanges := false

			if summary != "" {
				req.Summary = summary
				hasChanges = true
			}
			if startsAt != "" {
				req.StartsAt = startsAt
				hasChanges = true
			}
			if endsAt != "" {
				req.EndsAt = endsAt
				hasChanges = true
			}
			if description != "" {
				req.Description = description
				hasChanges = true
			}
			if cmd.Flags().Changed("all-day") {
				req.AllDay = allDay
				hasChanges = true
			}
			if cmd.Flags().Changed("notify") {
				req.Notify = notify
				hasChanges = true
			}
			if participants != "" {
				var ids []int64
				for idStr := range strings.SplitSeq(participants, ",") {
					idStr = strings.TrimSpace(idStr)
					if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
						ids = append(ids, id)
					}
				}
				if len(ids) > 0 {
					req.ParticipantIDs = ids
					hasChanges = true
				}
			}

			if !hasChanges {
				return output.ErrUsage("No update fields provided")
			}

			entry, err := app.Account().Schedules().UpdateEntry(cmd.Context(), bucketID, entryIDInt, req)
			if err != nil {
				return convertSDKError(err)
			}

			resultSummary := fmt.Sprintf("Updated schedule entry #%s", entryID)

			return app.OK(entry,
				output.WithSummary(resultSummary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp schedule show %s --project %s", entryID, resolvedProjectID),
						Description: "View entry",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&summary, "summary", "", "Event title/summary")
	cmd.Flags().StringVar(&summary, "title", "", "Event title (alias)")
	cmd.Flags().StringVar(&startsAt, "starts-at", "", "Start time (ISO 8601)")
	cmd.Flags().StringVar(&startsAt, "start", "", "Start time (alias)")
	cmd.Flags().StringVar(&endsAt, "ends-at", "", "End time (ISO 8601)")
	cmd.Flags().StringVar(&endsAt, "end", "", "End time (alias)")
	cmd.Flags().StringVar(&description, "description", "", "Detailed description")
	cmd.Flags().StringVar(&description, "desc", "", "Description (alias)")
	cmd.Flags().BoolVar(&allDay, "all-day", false, "Mark as all-day event")
	cmd.Flags().BoolVar(&notify, "notify", false, "Notify participants")
	cmd.Flags().StringVar(&participants, "participants", "", "Comma-separated person IDs")
	cmd.Flags().StringVar(&participants, "people", "", "Person IDs (alias)")

	return cmd
}

func newScheduleSettingsCmd(project, scheduleID *string) *cobra.Command {
	var includeDue bool

	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Update schedule settings",
		Long:  "Update schedule settings like including due assignments.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if !cmd.Flags().Changed("include-due") {
				return output.ErrUsage("--include-due required (true or false)")
			}

			// Resolve project, with interactive fallback
			projectID := *project
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

			// Get schedule ID from dock if not specified
			effectiveScheduleID := *scheduleID
			if effectiveScheduleID == "" {
				effectiveScheduleID, err = getScheduleID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}

			bucketID, _ := strconv.ParseInt(resolvedProjectID, 10, 64)
			scheduleIDInt, _ := strconv.ParseInt(effectiveScheduleID, 10, 64)

			req := &basecamp.UpdateScheduleSettingsRequest{
				IncludeDueAssignments: includeDue,
			}

			schedule, err := app.Account().Schedules().UpdateSettings(cmd.Context(), bucketID, scheduleIDInt, req)
			if err != nil {
				return convertSDKError(err)
			}

			resultSummary := "Updated schedule settings"

			return app.OK(schedule,
				output.WithSummary(resultSummary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp schedule --project %s", resolvedProjectID),
						Description: "View schedule",
					},
				),
			)
		},
	}

	cmd.Flags().BoolVar(&includeDue, "include-due", false, "Include due dates from todos/cards")
	cmd.Flags().BoolVar(&includeDue, "include-due-assignments", false, "Include due assignments (alias)")

	return cmd
}

// getScheduleID retrieves the schedule ID from a project's dock, handling multi-dock projects.
func getScheduleID(cmd *cobra.Command, app *appctx.App, projectID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "schedule", "", "schedule")
}
