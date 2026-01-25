package commands

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// NewTimesheetCmd creates the timesheet command for viewing timesheet reports.
func NewTimesheetCmd() *cobra.Command {
	var startDate string
	var endDate string
	var personID string
	var bucketID string

	cmd := &cobra.Command{
		Use:   "timesheet",
		Short: "View timesheet reports",
		Long: `View timesheet reports.

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
	if err := app.API.RequireAccount(); err != nil {
		return err
	}

	// Validate: if one date is provided, both are required
	if startDate != "" && endDate == "" {
		return output.ErrUsage("--end required when --start is provided")
	}
	if endDate != "" && startDate == "" {
		return output.ErrUsage("--start required when --end is provided")
	}

	// Build query params
	params := url.Values{}
	if startDate != "" {
		params.Set("start_date", startDate)
	}
	if endDate != "" {
		params.Set("end_date", endDate)
	}
	if personID != "" {
		params.Set("person_id", personID)
	}
	if bucketID != "" {
		params.Set("bucket_id", bucketID)
	}

	path := "/reports/timesheet.json"
	if len(params) > 0 {
		path = path + "?" + params.Encode()
	}

	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var entries []struct {
		Hours string `json:"hours"`
	}
	if err := json.Unmarshal(resp.Data, &entries); err != nil {
		return fmt.Errorf("failed to parse timesheet: %w", err)
	}

	// Calculate total hours
	var totalHours float64
	for _, e := range entries {
		var hours float64
		fmt.Sscanf(e.Hours, "%f", &hours)
		totalHours += hours
	}

	return app.Output.OK(json.RawMessage(resp.Data),
		output.WithSummary(fmt.Sprintf("%d timesheet entries (%.1fh total)", len(entries), totalHours)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "project",
				Cmd:         "bcq timesheet project <id>",
				Description: "View project timesheet",
			},
			output.Breadcrumb{
				Action:      "recording",
				Cmd:         "bcq timesheet recording <id> --project <project>",
				Description: "View recording timesheet",
			},
		),
	)
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			// Project from positional arg or flag
			if len(args) > 0 && project == "" {
				project = args[0]
			}

			// Resolve project
			projectID := project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("Project ID is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			path := fmt.Sprintf("/projects/%s/timesheet.json", resolvedProjectID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var entries []struct {
				Hours string `json:"hours"`
			}
			if err := json.Unmarshal(resp.Data, &entries); err != nil {
				return fmt.Errorf("failed to parse timesheet: %w", err)
			}

			// Calculate total hours
			var totalHours float64
			for _, e := range entries {
				var hours float64
				fmt.Sscanf(e.Hours, "%f", &hours)
				totalHours += hours
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("%d entries (%.1fh total)", len(entries), totalHours)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "report",
						Cmd:         "bcq timesheet report",
						Description: "View account-wide report",
					},
					output.Breadcrumb{
						Action:      "recording",
						Cmd:         fmt.Sprintf("bcq timesheet recording <id> --project %s", resolvedProjectID),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			recordingID := args[0]

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

			path := fmt.Sprintf("/projects/%s/recordings/%s/timesheet.json", resolvedProjectID, recordingID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var entries []struct {
				Hours string `json:"hours"`
			}
			if err := json.Unmarshal(resp.Data, &entries); err != nil {
				return fmt.Errorf("failed to parse timesheet: %w", err)
			}

			// Calculate total hours
			var totalHours float64
			for _, e := range entries {
				var hours float64
				fmt.Sscanf(e.Hours, "%f", &hours)
				totalHours += hours
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("%d entries (%.1fh total)", len(entries), totalHours)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "project",
						Cmd:         fmt.Sprintf("bcq timesheet project %s", resolvedProjectID),
						Description: "View project timesheet",
					},
					output.Breadcrumb{
						Action:      "report",
						Cmd:         "bcq timesheet report",
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
