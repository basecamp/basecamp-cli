package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// NewForwardsCmd creates the forwards command for managing email forwards.
func NewForwardsCmd() *cobra.Command {
	var project string
	var inboxID string

	cmd := &cobra.Command{
		Use:   "forwards",
		Short: "Manage email forwards (inbox)",
		Long: `Manage email forwards in project inbox.

Forwards are emails forwarded into Basecamp. Each project has an inbox
that can receive forwarded emails.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runForwardsList(cmd, project, inboxID)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVar(&inboxID, "inbox", "", "Inbox ID (auto-detected from project)")

	cmd.AddCommand(
		newForwardsListCmd(&project, &inboxID),
		newForwardsShowCmd(&project),
		newForwardsInboxCmd(&project, &inboxID),
		newForwardsRepliesCmd(&project),
		newForwardsReplyCmd(&project),
	)

	return cmd
}

// getInboxID gets the inbox ID from the project dock, handling multi-dock projects.
func getInboxID(cmd *cobra.Command, app *appctx.App, projectID, inboxID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "inbox", inboxID, "inbox")
}

func newForwardsListCmd(project, inboxID *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List forwards in project inbox",
		Long:  "List all email forwards in the project inbox.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runForwardsList(cmd, *project, *inboxID)
		},
	}
}

func runForwardsList(cmd *cobra.Command, project, inboxID string) error {
	app := appctx.FromContext(cmd.Context())

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

	// Get inbox ID
	resolvedInboxID, err := getInboxID(cmd, app, resolvedProjectID, inboxID)
	if err != nil {
		return err
	}

	inboxIDInt, err := strconv.ParseInt(resolvedInboxID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid inbox ID")
	}

	forwards, err := app.SDK.Forwards().List(cmd.Context(), bucketID, inboxIDInt)
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(forwards,
		output.WithSummary(fmt.Sprintf("%d forwards", len(forwards))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("bcq forwards show <id> --in %s", resolvedProjectID),
				Description: "View a forward",
			},
			output.Breadcrumb{
				Action:      "inbox",
				Cmd:         fmt.Sprintf("bcq forwards inbox --in %s", resolvedProjectID),
				Description: "View inbox details",
			},
		),
	)
}

func newForwardsShowCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show a forward",
		Long:  "Display detailed information about an email forward.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			forwardIDStr := args[0]
			forwardID, err := strconv.ParseInt(forwardIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid forward ID")
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

			forward, err := app.SDK.Forwards().Get(cmd.Context(), bucketID, forwardID)
			if err != nil {
				return convertSDKError(err)
			}

			subject := forward.Subject
			if subject == "" {
				subject = "Forward"
			}

			return app.OK(forward,
				output.WithSummary(subject),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "replies",
						Cmd:         fmt.Sprintf("bcq forwards replies %s --in %s", forwardIDStr, resolvedProjectID),
						Description: "View replies",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq forwards --in %s", resolvedProjectID),
						Description: "List all forwards",
					},
				),
			)
		},
	}
}

func newForwardsInboxCmd(project, inboxID *string) *cobra.Command {
	return &cobra.Command{
		Use:   "inbox",
		Short: "Show inbox details",
		Long:  "Display detailed information about the project inbox.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

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

			// Get inbox ID
			resolvedInboxID, err := getInboxID(cmd, app, resolvedProjectID, *inboxID)
			if err != nil {
				return err
			}

			inboxIDInt, err := strconv.ParseInt(resolvedInboxID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid inbox ID")
			}

			inbox, err := app.SDK.Forwards().GetInbox(cmd.Context(), bucketID, inboxIDInt)
			if err != nil {
				return convertSDKError(err)
			}

			title := inbox.Title
			if title == "" {
				title = "Inbox"
			}

			return app.OK(inbox,
				output.WithSummary(title),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "forwards",
						Cmd:         fmt.Sprintf("bcq forwards --in %s", resolvedProjectID),
						Description: "List forwards",
					},
				),
			)
		},
	}
}

func newForwardsRepliesCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "replies <forward_id>",
		Short: "List replies to a forward",
		Long:  "List all replies to an email forward.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			forwardIDStr := args[0]
			forwardID, err := strconv.ParseInt(forwardIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid forward ID")
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

			replies, err := app.SDK.Forwards().ListReplies(cmd.Context(), bucketID, forwardID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(replies,
				output.WithSummary(fmt.Sprintf("%d replies to forward #%s", len(replies), forwardIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "forward",
						Cmd:         fmt.Sprintf("bcq forwards show %s --in %s", forwardIDStr, resolvedProjectID),
						Description: "View the forward",
					},
					output.Breadcrumb{
						Action:      "reply",
						Cmd:         fmt.Sprintf("bcq forwards reply %s <reply_id> --in %s", forwardIDStr, resolvedProjectID),
						Description: "View a reply",
					},
				),
			)
		},
	}
}

func newForwardsReplyCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "reply <forward_id> <reply_id>",
		Short: "Show a specific reply",
		Long:  "Display detailed information about a reply to an email forward.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			forwardIDStr := args[0]
			forwardID, err := strconv.ParseInt(forwardIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid forward ID")
			}

			replyIDStr := args[1]
			replyID, err := strconv.ParseInt(replyIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid reply ID")
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

			reply, err := app.SDK.Forwards().GetReply(cmd.Context(), bucketID, forwardID, replyID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(reply,
				output.WithSummary("Reply"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "forward",
						Cmd:         fmt.Sprintf("bcq forwards show %s --in %s", forwardIDStr, resolvedProjectID),
						Description: "View the forward",
					},
					output.Breadcrumb{
						Action:      "replies",
						Cmd:         fmt.Sprintf("bcq forwards replies %s --in %s", forwardIDStr, resolvedProjectID),
						Description: "List all replies",
					},
				),
			)
		},
	}
}
