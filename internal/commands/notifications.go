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

// NewNotificationsCmd creates the notifications command.
func NewNotificationsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notifications",
		Short: "View and manage notifications",
		Long: `View and manage your notifications.

Shows unread and read notifications, plus resurfaced items: Bubble Ups
(and Scheduled Bubble Ups) on Basecamp 5, Memories on Basecamp 4.
Use 'read' to mark notifications as read, 'bubbleups' to page through
all current and scheduled Bubble Ups.`,
		Args: cobra.NoArgs,
		Annotations: map[string]string{
			"agent_notes": "Account-wide notifications — no --in <project> needed.\n" +
				"Returns unreads and reads sections, plus bubble_ups/scheduled_bubble_ups (BC5) or memories (BC4).\n" +
				"Use 'read' with notification IDs to mark as read.\n" +
				"Use 'bubbleups' (BC5) for the full bubble-ups list; 'list --limit-bubble-ups' caps inline bubble-ups at 2.",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNotificationsList(cmd, 0, false)
		},
	}

	cmd.AddCommand(
		newNotificationsListCmd(),
		newNotificationsReadCmd(),
		newNotificationsBubbleupsCmd(),
	)

	return cmd
}

func newNotificationsListCmd() *cobra.Command {
	var page int32
	var limitBubbleUps bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List notifications",
		Long: `List notifications (same as bare 'notifications').

With --limit-bubble-ups, caps the inline bubble_ups list at 2 and omits
scheduled_bubble_ups (the totals are still reported). Use
'basecamp notifications bubbleups' to page through the full list.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNotificationsList(cmd, page, limitBubbleUps)
		},
	}

	cmd.Flags().Int32Var(&page, "page", 0, "Page number (default: first page)")
	cmd.Flags().BoolVar(&limitBubbleUps, "limit-bubble-ups", false, "Cap inline bubble-ups at 2 and omit scheduled bubble-ups (BC5)")

	return cmd
}

func runNotificationsList(cmd *cobra.Command, page int32, limitBubbleUps bool) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	var result *basecamp.NotificationsResult
	var err error
	if limitBubbleUps {
		result, err = app.Account().MyNotifications().GetWithOptions(cmd.Context(), page, basecamp.WithLimitBubbleUps())
	} else {
		result, err = app.Account().MyNotifications().Get(cmd.Context(), page)
	}
	if err != nil {
		return convertSDKError(err)
	}

	// BC5 carries resurfaced items in BubbleUps and may also populate
	// Memories as a compat alias of the same list; BC4 populates Memories
	// only. Count whichever list carries them, never both.
	resurfaced := len(result.BubbleUps)
	if resurfaced == 0 {
		resurfaced = len(result.Memories)
	}
	total := len(result.Unreads) + len(result.Reads) + resurfaced + len(result.ScheduledBubbleUps)
	summary := fmt.Sprintf("%d notification(s)", total)
	if len(result.Unreads) > 0 {
		summary += fmt.Sprintf(" (%d unread)", len(result.Unreads))
	}
	if limitBubbleUps {
		// The bubble_ups array is capped at 2 and scheduled_bubble_ups is
		// omitted; the counts report the uncapped totals.
		if result.BubbleUpsCount > 0 {
			summary += fmt.Sprintf(", %d of %d bubble-up(s)", len(result.BubbleUps), result.BubbleUpsCount)
		}
		if result.ScheduledBubbleUpsCount > 0 {
			summary += fmt.Sprintf(", %d scheduled bubble-up(s) not shown", result.ScheduledBubbleUpsCount)
		}
	} else {
		if len(result.BubbleUps) > 0 {
			summary += fmt.Sprintf(", %d bubble-up(s)", len(result.BubbleUps))
		}
		if len(result.ScheduledBubbleUps) > 0 {
			summary += fmt.Sprintf(", %d scheduled bubble-up(s)", len(result.ScheduledBubbleUps))
		}
	}

	nextPage := page + 1
	if page == 0 {
		nextPage = 2
	}
	readCmd := "basecamp notifications read <id>"
	if page > 0 {
		readCmd = fmt.Sprintf("basecamp notifications read <id> --page %d", page)
	}
	nextCmd := fmt.Sprintf("basecamp notifications list --page %d", nextPage)
	if limitBubbleUps {
		nextCmd += " --limit-bubble-ups"
	}
	breadcrumbs := []output.Breadcrumb{
		{
			Action:      "read",
			Cmd:         readCmd,
			Description: "Mark as read",
		},
		{
			Action:      "next",
			Cmd:         nextCmd,
			Description: "Next page",
		},
	}
	if limitBubbleUps {
		breadcrumbs = append(breadcrumbs, output.Breadcrumb{
			Action:      "bubbleups",
			Cmd:         "basecamp notifications bubbleups",
			Description: "All bubble-ups",
		})
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
			sections := [][]basecamp.Notification{
				result.Unreads, result.Reads, result.Memories,
				result.BubbleUps, result.ScheduledBubbleUps,
			}
			for _, section := range sections {
				for _, n := range section {
					if n.ReadableSGID != "" {
						sgidMap[n.ID] = n.ReadableSGID
					}
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

func newNotificationsBubbleupsCmd() *cobra.Command {
	var page int32

	cmd := &cobra.Command{
		Use:   "bubbleups",
		Short: "List all bubble-ups (Basecamp 5)",
		Long: `List all current and scheduled Bubble Ups (Basecamp 5).

Current bubble-ups come first (most recently bubbled up), then scheduled
bubble-ups (by scheduled time). Follows pagination by default; pass --page
to fetch a single page (50 per page).

  basecamp notifications bubbleups
  basecamp notifications bubbleups --page 2`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			result, err := app.Account().MyNotifications().BubbleUps(cmd.Context(), page)
			if err != nil {
				return convertSDKError(err)
			}
			bubbleUps := result.BubbleUps

			summary := fmt.Sprintf("%d bubble-up(s)", len(bubbleUps))
			if result.Meta.TotalCount > 0 && len(bubbleUps) < result.Meta.TotalCount {
				summary = fmt.Sprintf("%d of %d bubble-up(s)", len(bubbleUps), result.Meta.TotalCount)
			}

			// No "read" breadcrumb: notifications read resolves IDs from
			// the notification feed, not this dedicated endpoint, so it
			// cannot find bubble-ups that only appear here.
			return app.OK(bubbleUps,
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "notifications",
						Cmd:         "basecamp notifications",
						Description: "View notifications",
					},
				),
			)
		},
	}

	cmd.Flags().Int32Var(&page, "page", 0, "Fetch a single page (default: all pages)")

	return cmd
}
