package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewEventsCmd creates the events command for viewing recording event history.
func NewEventsCmd() *cobra.Command {
	var limit int
	var page int
	var all bool

	cmd := &cobra.Command{
		Use:   "events <recording_id|url>",
		Short: "View recording event history",
		Long: `View the event history (audit trail) for any recording.

You can pass either a recording ID or a Basecamp URL:
  basecamp events 789
  basecamp events https://3.basecamp.com/123/buckets/456/todos/789

Events track all changes to a recording. Common event actions:
- created - Recording was created
- completed/uncompleted - Todo completion state changed
- assignment_changed - Assignees were added/removed
- content_changed - Content was edited
- archived/unarchived - Recording status changed
- commented_on - A comment was added`,
		Annotations: map[string]string{"agent_notes": "Events show change history for a specific recording"},
		Args:        cobra.ExactArgs(1),
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
				return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			recordingIDStr := extractID(args[0])

			recordingID, err := strconv.ParseInt(recordingIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid recording ID")
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

			eventsResult, err := app.Account().Events().List(cmd.Context(), recordingID, opts)
			if err != nil {
				return convertSDKError(err)
			}
			events := eventsResult.Events

			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("%d events for recording #%s", len(events), recordingIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "recording",
						Cmd:         fmt.Sprintf("basecamp show %s", recordingIDStr),
						Description: "View the recording",
					},
				),
			}

			// Add truncation notice if results may be limited
			if notice := output.TruncationNoticeWithTotal(len(events), eventsResult.Meta.TotalCount); notice != "" {
				respOpts = append(respOpts, output.WithNotice(notice))
			}

			return app.OK(events, respOpts...)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of events to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all events (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	return cmd
}
