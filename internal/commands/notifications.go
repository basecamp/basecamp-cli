package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewNotificationsCmd creates the notifications command.
func NewNotificationsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notifications",
		Short: "View and manage notifications",
		Long: `View and manage your notifications.

Shows unread, read, and memory notifications. Use 'read' to mark
notifications as read.`,
		Annotations: map[string]string{
			"agent_notes": "Account-wide notifications — no --in <project> needed.\n" +
				"Returns unreads, reads, and memories sections.\n" +
				"Use 'read' with notification IDs to mark as read.",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNotificationsList(cmd, 0)
		},
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
		Long:  "List notifications (same as bare 'notifications').",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNotificationsList(cmd, page)
		},
	}

	cmd.Flags().Int32Var(&page, "page", 0, "Page number (default: first page)")

	return cmd
}

func runNotificationsList(cmd *cobra.Command, page int32) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	result, err := app.Account().MyNotifications().Get(cmd.Context(), page)
	if err != nil {
		return convertSDKError(err)
	}

	total := len(result.Unreads) + len(result.Reads) + len(result.Memories)
	summary := fmt.Sprintf("%d notification(s)", total)
	if len(result.Unreads) > 0 {
		summary += fmt.Sprintf(" (%d unread)", len(result.Unreads))
	}

	nextPage := page + 1
	if page == 0 {
		nextPage = 2
	}
	breadcrumbs := []output.Breadcrumb{
		{
			Action:      "read",
			Cmd:         "basecamp notifications read <id>",
			Description: "Mark as read",
		},
		{
			Action:      "next",
			Cmd:         fmt.Sprintf("basecamp notifications list --page %d", nextPage),
			Description: "Next page",
		},
	}

	return app.OK(result,
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}

func newNotificationsReadCmd() *cobra.Command {
	var page int32

	cmd := &cobra.Command{
		Use:   "read <id>...",
		Short: "Mark notifications as read",
		Long: `Mark one or more notifications as read.

Accepts notification IDs from the page you were viewing. Use --page to
match the page you listed (defaults to first page).

  basecamp notifications read 12345
  basecamp notifications read 12345 67890 --page 2`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Parse the requested notification IDs
			for _, arg := range args {
				if _, err := strconv.ParseInt(arg, 10, 64); err != nil {
					return output.ErrUsage(fmt.Sprintf("Invalid notification ID: %s", arg))
				}
			}

			// Fetch the same page the user was looking at
			result, err := app.Account().MyNotifications().Get(cmd.Context(), page)
			if err != nil {
				return convertSDKError(err)
			}

			// Build ID → SGID map from all notification sections
			sgidMap := make(map[int64]string)
			for _, n := range result.Unreads {
				if n.ReadableSGID != "" {
					sgidMap[n.ID] = n.ReadableSGID
				}
			}
			for _, n := range result.Reads {
				if n.ReadableSGID != "" {
					sgidMap[n.ID] = n.ReadableSGID
				}
			}
			for _, n := range result.Memories {
				if n.ReadableSGID != "" {
					sgidMap[n.ID] = n.ReadableSGID
				}
			}

			// Resolve each requested ID to its SGID
			var sgids []string
			var unresolved []string
			for _, arg := range args {
				id, _ := strconv.ParseInt(arg, 10, 64)
				if sgid, ok := sgidMap[id]; ok {
					sgids = append(sgids, sgid)
				} else {
					unresolved = append(unresolved, arg)
				}
			}

			if len(unresolved) > 0 {
				pageHint := ""
				if page > 0 {
					pageHint = fmt.Sprintf(" (page %d)", page)
				}
				return output.ErrUsageHint(
					fmt.Sprintf("Notification(s) not found%s: %s", pageHint, strings.Join(unresolved, ", ")),
					"Run 'basecamp notifications list' to see available notification IDs, then use --page to match",
				)
			}

			err = app.Account().MyNotifications().MarkAsRead(cmd.Context(), sgids)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"marked_read": len(sgids)},
				output.WithSummary(fmt.Sprintf("Marked %d notification(s) as read", len(sgids))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp notifications",
						Description: "View notifications",
					},
				),
			)
		},
	}

	cmd.Flags().Int32Var(&page, "page", 0, "Page to resolve IDs from (match the page you listed)")

	return cmd
}
