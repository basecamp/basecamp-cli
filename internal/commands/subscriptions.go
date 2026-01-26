package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// NewSubscriptionsCmd creates the subscriptions command for managing recording subscriptions.
func NewSubscriptionsCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "subscriptions <recording_id>",
		Short: "Manage recording subscriptions",
		Long: `Manage recording subscriptions (who gets notified on changes).

Subscriptions control who receives notifications when a recording is updated,
commented on, or otherwise changed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubscriptionsShow(cmd, project, args[0])
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	cmd.AddCommand(
		newSubscriptionsShowCmd(&project),
		newSubscriptionsSubscribeCmd(&project),
		newSubscriptionsUnsubscribeCmd(&project),
		newSubscriptionsAddCmd(&project),
		newSubscriptionsRemoveCmd(&project),
	)

	return cmd
}

func newSubscriptionsShowCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show <recording_id>",
		Short: "Show current subscribers",
		Long:  "Display all current subscribers for a recording.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubscriptionsShow(cmd, *project, args[0])
		},
	}
}

func runSubscriptionsShow(cmd *cobra.Command, project, recordingIDStr string) error {
	app := appctx.FromContext(cmd.Context())

	recordingID, err := strconv.ParseInt(recordingIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid recording ID")
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
		return output.ErrUsage("--project is required")
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	subscription, err := app.SDK.Subscriptions().Get(cmd.Context(), bucketID, recordingID)
	if err != nil {
		return convertSDKError(err)
	}

	subscribedStr := "no"
	if subscription.Subscribed {
		subscribedStr = "yes"
	}

	return app.Output.OK(subscription,
		output.WithSummary(fmt.Sprintf("%d subscribers (you: %s)", subscription.Count, subscribedStr)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "subscribe",
				Cmd:         fmt.Sprintf("bcq subscriptions subscribe %s --in %s", recordingIDStr, resolvedProjectID),
				Description: "Subscribe yourself",
			},
			output.Breadcrumb{
				Action:      "unsubscribe",
				Cmd:         fmt.Sprintf("bcq subscriptions unsubscribe %s --in %s", recordingIDStr, resolvedProjectID),
				Description: "Unsubscribe yourself",
			},
		),
	)
}

func newSubscriptionsSubscribeCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "subscribe <recording_id>",
		Short: "Subscribe yourself to a recording",
		Long:  "Subscribe yourself to receive notifications for a recording.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			recordingIDStr := args[0]
			recordingID, err := strconv.ParseInt(recordingIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid recording ID")
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			subscription, err := app.SDK.Subscriptions().Subscribe(cmd.Context(), bucketID, recordingID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.Output.OK(subscription,
				output.WithSummary(fmt.Sprintf("Subscribed to recording #%s", recordingIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq subscriptions %s --in %s", recordingIDStr, resolvedProjectID),
						Description: "View subscribers",
					},
					output.Breadcrumb{
						Action:      "unsubscribe",
						Cmd:         fmt.Sprintf("bcq subscriptions unsubscribe %s --in %s", recordingIDStr, resolvedProjectID),
						Description: "Unsubscribe",
					},
				),
			)
		},
	}
}

func newSubscriptionsUnsubscribeCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "unsubscribe <recording_id>",
		Short: "Unsubscribe yourself from a recording",
		Long:  "Unsubscribe yourself from notifications for a recording.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			recordingIDStr := args[0]
			recordingID, err := strconv.ParseInt(recordingIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid recording ID")
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			// Unsubscribe - ignore errors for idempotency
			_ = app.SDK.Subscriptions().Unsubscribe(cmd.Context(), bucketID, recordingID)

			return app.Output.OK(map[string]any{},
				output.WithSummary(fmt.Sprintf("Unsubscribed from recording #%s", recordingIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq subscriptions %s --in %s", recordingIDStr, resolvedProjectID),
						Description: "View subscribers",
					},
					output.Breadcrumb{
						Action:      "subscribe",
						Cmd:         fmt.Sprintf("bcq subscriptions subscribe %s --in %s", recordingIDStr, resolvedProjectID),
						Description: "Re-subscribe",
					},
				),
			)
		},
	}
}

func newSubscriptionsAddCmd(project *string) *cobra.Command {
	var peopleIDs string

	cmd := &cobra.Command{
		Use:   "add <recording_id> [person_ids]",
		Short: "Add people to subscribers",
		Long:  "Add people to the subscribers list for a recording.",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubscriptionsUpdate(cmd, *project, args, peopleIDs, "add")
		},
	}

	cmd.Flags().StringVar(&peopleIDs, "people", "", "Comma-separated person IDs")

	return cmd
}

func newSubscriptionsRemoveCmd(project *string) *cobra.Command {
	var peopleIDs string

	cmd := &cobra.Command{
		Use:   "remove <recording_id> [person_ids]",
		Short: "Remove people from subscribers",
		Long:  "Remove people from the subscribers list for a recording.",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubscriptionsUpdate(cmd, *project, args, peopleIDs, "remove")
		},
	}

	cmd.Flags().StringVar(&peopleIDs, "people", "", "Comma-separated person IDs")

	return cmd
}

func runSubscriptionsUpdate(cmd *cobra.Command, project string, args []string, peopleIDs, mode string) error {
	app := appctx.FromContext(cmd.Context())

	recordingIDStr := args[0]
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

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	// Parse comma-separated IDs into array
	var ids []int64
	for _, idStr := range strings.Split(peopleIDs, ",") {
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

	subscription, err := app.SDK.Subscriptions().Update(cmd.Context(), bucketID, recordingID, req)
	if err != nil {
		return convertSDKError(err)
	}

	actionWord := "Added"
	if mode == "remove" {
		actionWord = "Removed"
	}

	return app.Output.OK(subscription,
		output.WithSummary(fmt.Sprintf("%s subscribers for recording #%s", actionWord, recordingIDStr)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("bcq subscriptions %s --in %s", recordingIDStr, resolvedProjectID),
				Description: "View subscribers",
			},
		),
	)
}
