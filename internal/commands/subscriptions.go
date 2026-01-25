package commands

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

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

func runSubscriptionsShow(cmd *cobra.Command, project, recordingID string) error {
	app := appctx.FromContext(cmd.Context())
	if err := app.API.RequireAccount(); err != nil {
		return err
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

	path := fmt.Sprintf("/buckets/%s/recordings/%s/subscription.json", resolvedProjectID, recordingID)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var subscription struct {
		Subscribed  bool `json:"subscribed"`
		Count       int  `json:"count"`
		Subscribers []struct {
			Name string `json:"name"`
		} `json:"subscribers"`
	}
	if err := json.Unmarshal(resp.Data, &subscription); err != nil {
		return fmt.Errorf("failed to parse subscription: %w", err)
	}

	subscribedStr := "no"
	if subscription.Subscribed {
		subscribedStr = "yes"
	}

	return app.Output.OK(json.RawMessage(resp.Data),
		output.WithSummary(fmt.Sprintf("%d subscribers (you: %s)", subscription.Count, subscribedStr)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "subscribe",
				Cmd:         fmt.Sprintf("bcq subscriptions subscribe %s --in %s", recordingID, resolvedProjectID),
				Description: "Subscribe yourself",
			},
			output.Breadcrumb{
				Action:      "unsubscribe",
				Cmd:         fmt.Sprintf("bcq subscriptions unsubscribe %s --in %s", recordingID, resolvedProjectID),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			recordingID := args[0]

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

			path := fmt.Sprintf("/buckets/%s/recordings/%s/subscription.json", resolvedProjectID, recordingID)
			resp, err := app.API.Post(cmd.Context(), path, map[string]any{})
			if err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Subscribed to recording #%s", recordingID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq subscriptions %s --in %s", recordingID, resolvedProjectID),
						Description: "View subscribers",
					},
					output.Breadcrumb{
						Action:      "unsubscribe",
						Cmd:         fmt.Sprintf("bcq subscriptions unsubscribe %s --in %s", recordingID, resolvedProjectID),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			recordingID := args[0]

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

			path := fmt.Sprintf("/buckets/%s/recordings/%s/subscription.json", resolvedProjectID, recordingID)
			// DELETE request - ignore errors for idempotency
			_, _ = app.API.Delete(cmd.Context(), path)

			return app.Output.OK(map[string]any{},
				output.WithSummary(fmt.Sprintf("Unsubscribed from recording #%s", recordingID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq subscriptions %s --in %s", recordingID, resolvedProjectID),
						Description: "View subscribers",
					},
					output.Breadcrumb{
						Action:      "subscribe",
						Cmd:         fmt.Sprintf("bcq subscriptions subscribe %s --in %s", recordingID, resolvedProjectID),
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
	if err := app.API.RequireAccount(); err != nil {
		return err
	}

	recordingID := args[0]

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

	// Build payload
	var body map[string]any
	if mode == "add" {
		body = map[string]any{"subscriptions": ids}
	} else {
		body = map[string]any{"unsubscriptions": ids}
	}

	path := fmt.Sprintf("/buckets/%s/recordings/%s/subscription.json", resolvedProjectID, recordingID)
	resp, err := app.API.Put(cmd.Context(), path, body)
	if err != nil {
		return err
	}

	actionWord := "Added"
	if mode == "remove" {
		actionWord = "Removed"
	}

	return app.Output.OK(json.RawMessage(resp.Data),
		output.WithSummary(fmt.Sprintf("%s subscribers for recording #%s", actionWord, recordingID)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("bcq subscriptions %s --in %s", recordingID, resolvedProjectID),
				Description: "View subscribers",
			},
		),
	)
}
