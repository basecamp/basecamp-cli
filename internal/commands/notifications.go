package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

func NewNotificationsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notifications",
		Short: "Manage your notifications",
		Long:  "View and mark read the current user's Basecamp notifications.",
	}

	cmd.AddCommand(
		newNotificationsListCmd(),
		newNotificationsReadCmd(),
	)

	return cmd
}

func newNotificationsListCmd() *cobra.Command {
	var page int32

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List notifications",
		Long:  "List the current user's notifications grouped into unreads, reads, and memories.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			result, err := app.Account().MyNotifications().Get(cmd.Context(), page)
			if err != nil {
				return convertSDKError(err)
			}

			summaryParts := []string{
				fmt.Sprintf("%d unread", len(result.Unreads)),
				fmt.Sprintf("%d read", len(result.Reads)),
			}
			if len(result.Memories) > 0 {
				summaryParts = append(summaryParts, fmt.Sprintf("%d memories", len(result.Memories)))
			}

			return app.OK(result,
				output.WithSummary("Notifications: "+strings.Join(summaryParts, ", ")),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "read",
						Cmd:         "basecamp notifications read <readable-sgid>",
						Description: "Mark notifications as read",
					},
				),
			)
		},
	}

	cmd.Flags().Int32Var(&page, "page", 0, "Page of read notifications to fetch")

	return cmd
}

func newNotificationsReadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "read <readable-sgid>...",
		Short: "Mark notifications as read",
		Long:  "Mark one or more notifications as read by readable SGID.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if err := app.Account().MyNotifications().MarkAsRead(cmd.Context(), args); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{
				"readables": args,
				"updated":   true,
			},
				output.WithSummary(fmt.Sprintf("Marked %d notification(s) as read", len(args))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp notifications list",
						Description: "List notifications",
					},
				),
			)
		},
	}
}
