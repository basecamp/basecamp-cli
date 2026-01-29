package commands

import (
	"fmt"
	"strconv"

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

			events, err := app.Account().Events().List(cmd.Context(), bucketID, recordingID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(events,
				output.WithSummary(fmt.Sprintf("%d events for recording #%s", len(events), recordingIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "recording",
						Cmd:         fmt.Sprintf("bcq show %s --in %s", recordingIDStr, resolvedProjectID),
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
