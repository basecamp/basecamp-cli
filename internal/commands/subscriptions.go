package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewSubscriptionsCmd creates the subscriptions command for managing recording subscriptions.
func NewSubscriptionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subscriptions <recording_id|url>",
		Short: "Manage recording subscriptions",
		Long: `Manage recording subscriptions (who gets notified on changes).

Subscriptions control who receives notifications when a recording is updated,
commented on, or otherwise changed.`,
		Annotations: map[string]string{"agent_notes": "Subscriptions control email/push notifications for a recording"},
		Args:        cobra.ExactArgs(1),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			return ensureAccount(cmd, app)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubscriptionsShow(cmd, args[0])
		},
	}

	cmd.AddCommand(
		newSubscriptionsShowCmd(),
		newSubscriptionsSubscribeCmd(),
		newSubscriptionsUnsubscribeCmd(),
		newSubscriptionsAddCmd(),
		newSubscriptionsRemoveCmd(),
	)

	return cmd
}

func newSubscriptionsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <recording_id|url>",
		Short: "Show current subscribers",
		Long: `Display all current subscribers for a recording.

You can pass either a recording ID or a Basecamp URL:
  basecamp subscriptions show 789
  basecamp subscriptions show https://3.basecamp.com/123/buckets/456/recordings/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubscriptionsShow(cmd, args[0])
		},
	}
}

func runSubscriptionsShow(cmd *cobra.Command, recordingIDStr string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Extract ID from URL if provided
	recordingIDStr = extractID(recordingIDStr)

	recordingID, err := strconv.ParseInt(recordingIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid recording ID")
	}

	subscription, err := app.Account().Subscriptions().Get(cmd.Context(), recordingID)
	if err != nil {
		return convertSDKError(err)
	}

	subscribedStr := "no"
	if subscription.Subscribed {
		subscribedStr = "yes"
	}

	return app.OK(subscription,
		output.WithSummary(fmt.Sprintf("%d subscribers (you: %s)", subscription.Count, subscribedStr)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "subscribe",
				Cmd:         fmt.Sprintf("basecamp subscriptions subscribe %s", recordingIDStr),
				Description: "Subscribe yourself",
			},
			output.Breadcrumb{
				Action:      "unsubscribe",
				Cmd:         fmt.Sprintf("basecamp subscriptions unsubscribe %s", recordingIDStr),
				Description: "Unsubscribe yourself",
			},
		),
	)
}

func newSubscriptionsSubscribeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "subscribe <recording_id|url>",
		Short: "Subscribe yourself to a recording",
		Long: `Subscribe yourself to receive notifications for a recording.

You can pass either a recording ID or a Basecamp URL:
  basecamp subscriptions subscribe 789
  basecamp subscriptions subscribe https://3.basecamp.com/123/buckets/456/recordings/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			recordingIDStr := extractID(args[0])

			recordingID, err := strconv.ParseInt(recordingIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid recording ID")
			}

			subscription, err := app.Account().Subscriptions().Subscribe(cmd.Context(), recordingID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(subscription,
				output.WithSummary(fmt.Sprintf("Subscribed to recording #%s", recordingIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp subscriptions %s", recordingIDStr),
						Description: "View subscribers",
					},
					output.Breadcrumb{
						Action:      "unsubscribe",
						Cmd:         fmt.Sprintf("basecamp subscriptions unsubscribe %s", recordingIDStr),
						Description: "Unsubscribe",
					},
				),
			)
		},
	}
}

func newSubscriptionsUnsubscribeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unsubscribe <recording_id|url>",
		Short: "Unsubscribe yourself from a recording",
		Long: `Unsubscribe yourself from notifications for a recording.

You can pass either a recording ID or a Basecamp URL:
  basecamp subscriptions unsubscribe 789
  basecamp subscriptions unsubscribe https://3.basecamp.com/123/buckets/456/recordings/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			recordingIDStr := extractID(args[0])

			recordingID, err := strconv.ParseInt(recordingIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid recording ID")
			}

			// Unsubscribe - ignore errors for idempotency
			_ = app.Account().Subscriptions().Unsubscribe(cmd.Context(), recordingID)

			return app.OK(map[string]any{},
				output.WithSummary(fmt.Sprintf("Unsubscribed from recording #%s", recordingIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp subscriptions %s", recordingIDStr),
						Description: "View subscribers",
					},
					output.Breadcrumb{
						Action:      "subscribe",
						Cmd:         fmt.Sprintf("basecamp subscriptions subscribe %s", recordingIDStr),
						Description: "Re-subscribe",
					},
				),
			)
		},
	}
}

func newSubscriptionsAddCmd() *cobra.Command {
	var peopleIDs string

	cmd := &cobra.Command{
		Use:   "add <recording_id|url> [person_ids]",
		Short: "Add people to subscribers",
		Long: `Add people to the subscribers list for a recording.

You can pass either a recording ID or a Basecamp URL:
  basecamp subscriptions add 789 --people 1,2,3
  basecamp subscriptions add https://3.basecamp.com/123/buckets/456/recordings/789 --people 1,2,3`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubscriptionsUpdate(cmd, args, peopleIDs, "add")
		},
	}

	cmd.Flags().StringVar(&peopleIDs, "people", "", "Comma-separated person IDs")

	return cmd
}

func newSubscriptionsRemoveCmd() *cobra.Command {
	var peopleIDs string

	cmd := &cobra.Command{
		Use:   "remove <recording_id|url> [person_ids]",
		Short: "Remove people from subscribers",
		Long: `Remove people from the subscribers list for a recording.

You can pass either a recording ID or a Basecamp URL:
  basecamp subscriptions remove 789 --people 1,2,3
  basecamp subscriptions remove https://3.basecamp.com/123/buckets/456/recordings/789 --people 1,2,3`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubscriptionsUpdate(cmd, args, peopleIDs, "remove")
		},
	}

	cmd.Flags().StringVar(&peopleIDs, "people", "", "Comma-separated person IDs")

	return cmd
}

func runSubscriptionsUpdate(cmd *cobra.Command, args []string, peopleIDs, mode string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Extract ID from URL if provided
	recordingIDStr := extractID(args[0])

	recordingID, err := strconv.ParseInt(recordingIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid recording ID")
	}

	// Person IDs can come from second argument or --people flag
	if len(args) > 1 && peopleIDs == "" {
		peopleIDs = args[1]
	}

	if peopleIDs == "" {
		return output.ErrUsage("Person ID(s) required. Provide comma-separated person IDs")
	}

	// Parse comma-separated IDs into array
	var ids []int64
	for idStr := range strings.SplitSeq(peopleIDs, ",") {
		idStr = strings.TrimSpace(idStr)
		if idStr == "" {
			continue
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return output.ErrUsage(fmt.Sprintf("Invalid person ID: %s", idStr))
		}
		ids = append(ids, id)
	}

	// Build request
	req := &basecamp.UpdateSubscriptionRequest{}
	if mode == "add" {
		req.Subscriptions = ids
	} else {
		req.Unsubscriptions = ids
	}

	subscription, err := app.Account().Subscriptions().Update(cmd.Context(), recordingID, req)
	if err != nil {
		return convertSDKError(err)
	}

	actionWord := "Added"
	if mode == "remove" {
		actionWord = "Removed"
	}

	return app.OK(subscription,
		output.WithSummary(fmt.Sprintf("%s subscribers for recording #%s", actionWord, recordingIDStr)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp subscriptions %s", recordingIDStr),
				Description: "View subscribers",
			},
		),
	)
}
