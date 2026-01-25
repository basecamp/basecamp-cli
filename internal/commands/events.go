package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// NewEventsCmd creates the events command for viewing recording event history.
func NewEventsCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "events <recording_id>",
		Short: "View recording event history",
		Long: `View the event history (audit trail) for any recording.

Events track all changes to a recording. Common event actions:
- created - Recording was created
- completed/uncompleted - Todo completion state changed
- assignment_changed - Assignees were added/removed
- content_changed - Content was edited
- archived/unarchived - Recording status changed
- commented_on - A comment was added`,
		Args: cobra.ExactArgs(1),
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

			path := fmt.Sprintf("/buckets/%s/recordings/%s/events.json", resolvedProjectID, recordingID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var events []json.RawMessage
			if err := json.Unmarshal(resp.Data, &events); err != nil {
				return fmt.Errorf("failed to parse events: %w", err)
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("%d events for recording #%s", len(events), recordingID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "recording",
						Cmd:         fmt.Sprintf("bcq show %s --in %s", recordingID, resolvedProjectID),
						Description: "View the recording",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	return cmd
}
