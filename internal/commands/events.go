package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// NewEventsCmd creates the events command for viewing recording event history.
func NewEventsCmd() *cobra.Command {
	var project string
	var limit int
	var page int
	var all bool

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

			// Validate flag combinations
			if all && limit > 0 {
				return output.ErrUsage("--all and --limit are mutually exclusive")
			}
			if page > 0 && (all || limit > 0) {
				return output.ErrUsage("--page cannot be combined with --all or --limit")
			}
			if page > 1 {
				return output.ErrUsage("--page values >1 are not supported; use --all to fetch all results")
			}

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

			// Build pagination options
			opts := &basecamp.EventListOptions{}
			if all {
				opts.Limit = -1 // SDK treats -1 as "fetch all"
			} else if limit > 0 {
				opts.Limit = limit
			}
			if page > 0 {
				opts.Page = page
			}

			events, err := app.Account().Events().List(cmd.Context(), bucketID, recordingID, opts)
			if err != nil {
				return convertSDKError(err)
			}

			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("%d events for recording #%s", len(events), recordingIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "recording",
						Cmd:         fmt.Sprintf("bcq show %s --in %s", recordingIDStr, resolvedProjectID),
						Description: "View the recording",
					},
				),
			}

			// Add truncation notice if results may be limited
			if notice := output.TruncationNotice(len(events), basecamp.DefaultEventLimit, all, limit); notice != "" {
				respOpts = append(respOpts, output.WithNotice(notice))
			}

			return app.OK(events, respOpts...)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of events to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all events (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Disable pagination and return first page only")

	return cmd
}
