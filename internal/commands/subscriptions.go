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
	var project string

	cmd := &cobra.Command{
		Use:   "subscriptions <recording_id|url>",
		Short: "Manage recording subscriptions",
		Long: `Manage recording subscriptions (who gets notified on changes).

Subscriptions control who receives notifications when a recording is updated,
commented on, or otherwise changed.`,
		Args: cobra.ExactArgs(1),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			return ensureAccount(cmd, app)
		},
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
		Use:   "show <recording_id|url>",
		Short: "Show current subscribers",
		Long: `Display all current subscribers for a recording.

You can pass either a recording ID or a Basecamp URL:
  basecamp subscriptions show 789 --in my-project
  basecamp subscriptions show https://3.basecamp.com/123/buckets/456/recordings/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubscriptionsShow(cmd, *project, args[0])
		},
	}
}

func runSubscriptionsShow(cmd *cobra.Command, project, recordingIDStr string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Extract ID and project from URL if provided
	recordingIDStr, urlProjectID := extractWithProject(recordingIDStr)

	recordingID, err := strconv.ParseInt(recordingIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid recording ID")
	}

	// Resolve project - use URL > flag > config, with interactive fallback
	projectID := project
	if projectID == "" && urlProjectID != "" {
		projectID = urlProjectID
	}
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

	subscription, err := app.Account().Subscriptions().Get(cmd.Context(), bucketID, recordingID)
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
				Cmd:         fmt.Sprintf("basecamp subscriptions subscribe %s --in %s", recordingIDStr, resolvedProjectID),
				Description: "Subscribe yourself",
			},
			output.Breadcrumb{
				Action:      "unsubscribe",
				Cmd:         fmt.Sprintf("basecamp subscriptions unsubscribe %s --in %s", recordingIDStr, resolvedProjectID),
				Description: "Unsubscribe yourself",
			},
		),
	)
}

func newSubscriptionsSubscribeCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "subscribe <recording_id|url>",
		Short: "Subscribe yourself to a recording",
		Long: `Subscribe yourself to receive notifications for a recording.

You can pass either a recording ID or a Basecamp URL:
  basecamp subscriptions subscribe 789 --in my-project
  basecamp subscriptions subscribe https://3.basecamp.com/123/buckets/456/recordings/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			recordingIDStr, urlProjectID := extractWithProject(args[0])

			recordingID, err := strconv.ParseInt(recordingIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid recording ID")
			}

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
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

			subscription, err := app.Account().Subscriptions().Subscribe(cmd.Context(), bucketID, recordingID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(subscription,
				output.WithSummary(fmt.Sprintf("Subscribed to recording #%s", recordingIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp subscriptions %s --in %s", recordingIDStr, resolvedProjectID),
						Description: "View subscribers",
					},
					output.Breadcrumb{
						Action:      "unsubscribe",
						Cmd:         fmt.Sprintf("basecamp subscriptions unsubscribe %s --in %s", recordingIDStr, resolvedProjectID),
						Description: "Unsubscribe",
					},
				),
			)
		},
	}
}

func newSubscriptionsUnsubscribeCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "unsubscribe <recording_id|url>",
		Short: "Unsubscribe yourself from a recording",
		Long: `Unsubscribe yourself from notifications for a recording.

You can pass either a recording ID or a Basecamp URL:
  basecamp subscriptions unsubscribe 789 --in my-project
  basecamp subscriptions unsubscribe https://3.basecamp.com/123/buckets/456/recordings/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			recordingIDStr, urlProjectID := extractWithProject(args[0])

			recordingID, err := strconv.ParseInt(recordingIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid recording ID")
			}

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
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

			// Unsubscribe - ignore errors for idempotency
			_ = app.Account().Subscriptions().Unsubscribe(cmd.Context(), bucketID, recordingID)

			return app.OK(map[string]any{},
				output.WithSummary(fmt.Sprintf("Unsubscribed from recording #%s", recordingIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp subscriptions %s --in %s", recordingIDStr, resolvedProjectID),
						Description: "View subscribers",
					},
					output.Breadcrumb{
						Action:      "subscribe",
						Cmd:         fmt.Sprintf("basecamp subscriptions subscribe %s --in %s", recordingIDStr, resolvedProjectID),
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
		Use:   "add <recording_id|url> [person_ids]",
		Short: "Add people to subscribers",
		Long: `Add people to the subscribers list for a recording.

You can pass either a recording ID or a Basecamp URL:
  basecamp subscriptions add 789 --people 1,2,3 --in my-project
  basecamp subscriptions add https://3.basecamp.com/123/buckets/456/recordings/789 --people 1,2,3`,
		Args: cobra.RangeArgs(1, 2),
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
		Use:   "remove <recording_id|url> [person_ids]",
		Short: "Remove people from subscribers",
		Long: `Remove people from the subscribers list for a recording.

You can pass either a recording ID or a Basecamp URL:
  basecamp subscriptions remove 789 --people 1,2,3 --in my-project
  basecamp subscriptions remove https://3.basecamp.com/123/buckets/456/recordings/789 --people 1,2,3`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubscriptionsUpdate(cmd, *project, args, peopleIDs, "remove")
		},
	}

	cmd.Flags().StringVar(&peopleIDs, "people", "", "Comma-separated person IDs")

	return cmd
}

func runSubscriptionsUpdate(cmd *cobra.Command, project string, args []string, peopleIDs, mode string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Extract ID and project from URL if provided
	recordingIDStr, urlProjectID := extractWithProject(args[0])

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

	// Resolve project - use URL > flag > config, with interactive fallback
	projectID := project
	if projectID == "" && urlProjectID != "" {
		projectID = urlProjectID
	}
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

	subscription, err := app.Account().Subscriptions().Update(cmd.Context(), bucketID, recordingID, req)
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
				Cmd:         fmt.Sprintf("basecamp subscriptions %s --in %s", recordingIDStr, resolvedProjectID),
				Description: "View subscribers",
			},
		),
	)
}
