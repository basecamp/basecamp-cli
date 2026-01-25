package commands

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// ScheduleEntry represents a schedule entry from Basecamp.
type ScheduleEntry struct {
	ID        int64  `json:"id"`
	Summary   string `json:"summary"`
	StartsAt  string `json:"starts_at"`
	EndsAt    string `json:"ends_at"`
	AllDay    bool   `json:"all_day"`
	CreatedAt string `json:"created_at"`
}

// NewScheduleCmd creates the schedule command for managing schedules.
func NewScheduleCmd() *cobra.Command {
	var project string
	var scheduleID string

	cmd := &cobra.Command{
		Use:   "schedule [action]",
		Short: "Manage schedules and entries",
		Long: `Manage project schedules and schedule entries.

Use 'bcq schedule' to view the project schedule.
Use 'bcq schedule entries' to list schedule entries.
Use 'bcq schedule create' to create new entries.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			// Handle numeric ID as first arg: bcq schedule 123
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}
			return runScheduleShow(cmd, app, *project, *scheduleID)
		},
	}
}

func runScheduleShow(cmd *cobra.Command, app *appctx.App, project, scheduleID string) error {
	// Resolve project
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return output.ErrUsage("--project is required")
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

	path := fmt.Sprintf("/buckets/%s/schedules/%s.json", resolvedProjectID, scheduleID)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var schedule map[string]any
	if err := json.Unmarshal(resp.Data, &schedule); err != nil {
		return err
	}

	entriesCount := 0
	if ec, ok := schedule["entries_count"].(float64); ok {
		entriesCount = int(ec)
	}
	includeDue := false
	if id, ok := schedule["include_due_assignments"].(bool); ok {
		includeDue = id
	}

	summary := fmt.Sprintf("%d entries (include due assignments: %t)", entriesCount, includeDue)

	return app.Output.OK(json.RawMessage(resp.Data),
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "entries",
				Cmd:         fmt.Sprintf("bcq schedule entries --project %s", resolvedProjectID),
				Description: "View schedule entries",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("bcq schedule create \"Event\" --starts-at <datetime> --ends-at <datetime> --project %s", resolvedProjectID),
				Description: "Create entry",
			},
		),
	)
}

func newScheduleEntriesCmd(project, scheduleID *string) *cobra.Command {
	var status string

	cmd := &cobra.Command{
		Use:   "entries",
		Short: "List schedule entries",
		Long:  "List all entries in a project schedule.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}
			return runScheduleEntries(cmd, app, *project, *scheduleID, status)
		},
	}

	cmd.Flags().StringVar(&status, "status", "active", "Filter by status (active, archived, trashed)")

	return cmd
}

func runScheduleEntries(cmd *cobra.Command, app *appctx.App, project, scheduleID, status string) error {
	// Resolve project
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return output.ErrUsage("--project is required")
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

	path := fmt.Sprintf("/buckets/%s/schedules/%s/entries.json?status=%s", resolvedProjectID, scheduleID, status)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var entries []json.RawMessage
	if err := resp.UnmarshalData(&entries); err != nil {
		return fmt.Errorf("failed to parse schedule entries: %w", err)
	}

	summary := fmt.Sprintf("%d schedule entries", len(entries))

	return app.Output.OK(entries,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("bcq schedule show <id> --project %s", resolvedProjectID),
				Description: "View entry details",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("bcq schedule create \"Event\" --starts-at <datetime> --ends-at <datetime> --project %s", resolvedProjectID),
				Description: "Create entry",
			},
		),
	)
}

func newScheduleEntryShowCmd(project *string) *cobra.Command {
	var occurrenceDate string

	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show schedule entry",
		Long:  "Display details of a schedule entry.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
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
	// Resolve project
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return output.ErrUsage("--project is required")
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	var path string
	if occurrenceDate != "" {
		path = fmt.Sprintf("/buckets/%s/schedule_entries/%s/occurrences/%s.json", resolvedProjectID, entryID, occurrenceDate)
	} else {
		path = fmt.Sprintf("/buckets/%s/schedule_entries/%s.json", resolvedProjectID, entryID)
	}

	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var entry struct {
		Summary  string `json:"summary"`
		Title    string `json:"title"`
		StartsAt string `json:"starts_at"`
		EndsAt   string `json:"ends_at"`
	}
	if err := json.Unmarshal(resp.Data, &entry); err != nil {
		return err
	}

	title := entry.Summary
	if title == "" {
		title = entry.Title
	}
	if title == "" {
		title = "Entry"
	}

	summary := fmt.Sprintf("%s: %s → %s", title, entry.StartsAt, entry.EndsAt)

	return app.Output.OK(json.RawMessage(resp.Data),
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "update",
				Cmd:         fmt.Sprintf("bcq schedule update %s --summary \"...\" --project %s", entryID, resolvedProjectID),
				Description: "Update entry",
			},
			output.Breadcrumb{
				Action:      "entries",
				Cmd:         fmt.Sprintf("bcq schedule entries --project %s", resolvedProjectID),
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
			if err := app.API.RequireAccount(); err != nil {
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
	// Resolve project
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return output.ErrUsage("--project is required")
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

	// Build request body
	body := map[string]any{
		"summary":   summary,
		"starts_at": startsAt,
		"ends_at":   endsAt,
	}

	if description != "" {
		body["description"] = description
	}
	if allDay {
		body["all_day"] = true
	}
	if notify {
		body["notify"] = true
	}
	if participants != "" {
		var ids []int64
		for _, idStr := range strings.Split(participants, ",") {
			idStr = strings.TrimSpace(idStr)
			if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			body["participant_ids"] = ids
		}
	}

	path := fmt.Sprintf("/buckets/%s/schedules/%s/entries.json", resolvedProjectID, scheduleID)
	resp, err := app.API.Post(cmd.Context(), path, body)
	if err != nil {
		return err
	}

	var entry struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(resp.Data, &entry); err != nil {
		return err
	}

	resultSummary := fmt.Sprintf("Created schedule entry #%d: %s", entry.ID, summary)

	return app.Output.OK(json.RawMessage(resp.Data),
		output.WithSummary(resultSummary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("bcq schedule show %d --project %s", entry.ID, resolvedProjectID),
				Description: "View entry",
			},
			output.Breadcrumb{
				Action:      "entries",
				Cmd:         fmt.Sprintf("bcq schedule entries --project %s", resolvedProjectID),
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
		Use:   "update <id>",
		Short: "Update a schedule entry",
		Long:  "Update an existing schedule entry.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			entryID := args[0]

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Build request body with provided fields only
			body := make(map[string]any)

			if summary != "" {
				body["summary"] = summary
			}
			if startsAt != "" {
				body["starts_at"] = startsAt
			}
			if endsAt != "" {
				body["ends_at"] = endsAt
			}
			if description != "" {
				body["description"] = description
			}
			if cmd.Flags().Changed("all-day") {
				body["all_day"] = allDay
			}
			if cmd.Flags().Changed("notify") {
				body["notify"] = notify
			}
			if participants != "" {
				var ids []int64
				for _, idStr := range strings.Split(participants, ",") {
					idStr = strings.TrimSpace(idStr)
					if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
						ids = append(ids, id)
					}
				}
				if len(ids) > 0 {
					body["participant_ids"] = ids
				}
			}

			if len(body) == 0 {
				return output.ErrUsage("No update fields provided")
			}

			path := fmt.Sprintf("/buckets/%s/schedule_entries/%s.json", resolvedProjectID, entryID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			resultSummary := fmt.Sprintf("Updated schedule entry #%s", entryID)

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(resultSummary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq schedule show %s --project %s", entryID, resolvedProjectID),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			if !cmd.Flags().Changed("include-due") {
				return output.ErrUsage("--include-due required (true or false)")
			}

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
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

			body := map[string]bool{
				"include_due_assignments": includeDue,
			}

			path := fmt.Sprintf("/buckets/%s/schedules/%s.json", resolvedProjectID, effectiveScheduleID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			resultSummary := "Updated schedule settings"

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(resultSummary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq schedule --project %s", resolvedProjectID),
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
