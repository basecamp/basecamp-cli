package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/api"
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

	// Get inbox ID
	resolvedInboxID, err := getInboxID(cmd, app, resolvedProjectID, inboxID)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/buckets/%s/inboxes/%s/forwards.json", resolvedProjectID, resolvedInboxID)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var forwards []json.RawMessage
	if err := json.Unmarshal(resp.Data, &forwards); err != nil {
		return fmt.Errorf("failed to parse forwards: %w", err)
	}

	return app.Output.OK(json.RawMessage(resp.Data),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			forwardID := args[0]

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

			path := fmt.Sprintf("/buckets/%s/inbox_forwards/%s.json", resolvedProjectID, forwardID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var forward struct {
				Subject string `json:"subject"`
			}
			if err := json.Unmarshal(resp.Data, &forward); err != nil {
				return fmt.Errorf("failed to parse forward: %w", err)
			}

			subject := forward.Subject
			if subject == "" {
				subject = "Forward"
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(subject),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "replies",
						Cmd:         fmt.Sprintf("bcq forwards replies %s --in %s", forwardID, resolvedProjectID),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
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

			// Get inbox ID
			resolvedInboxID, err := getInboxID(cmd, app, resolvedProjectID, *inboxID)
			if err != nil {
				return err
			}

			path := fmt.Sprintf("/buckets/%s/inboxes/%s.json", resolvedProjectID, resolvedInboxID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var inbox struct {
				Title         string `json:"title"`
				ForwardsCount int    `json:"forwards_count"`
			}
			if err := json.Unmarshal(resp.Data, &inbox); err != nil {
				return fmt.Errorf("failed to parse inbox: %w", err)
			}

			title := inbox.Title
			if title == "" {
				title = "Inbox"
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("%s (%d forwards)", title, inbox.ForwardsCount)),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			forwardID := args[0]

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

			path := fmt.Sprintf("/buckets/%s/inbox_forwards/%s/replies.json", resolvedProjectID, forwardID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var replies []json.RawMessage
			if err := json.Unmarshal(resp.Data, &replies); err != nil {
				return fmt.Errorf("failed to parse replies: %w", err)
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("%d replies to forward #%s", len(replies), forwardID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "forward",
						Cmd:         fmt.Sprintf("bcq forwards show %s --in %s", forwardID, resolvedProjectID),
						Description: "View the forward",
					},
					output.Breadcrumb{
						Action:      "reply",
						Cmd:         fmt.Sprintf("bcq forwards reply %s <reply_id> --in %s", forwardID, resolvedProjectID),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			forwardID := args[0]
			replyID := args[1]

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

			path := fmt.Sprintf("/buckets/%s/inbox_forwards/%s/replies/%s.json", resolvedProjectID, forwardID, replyID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var reply struct {
				Title string `json:"title"`
			}
			if err := json.Unmarshal(resp.Data, &reply); err != nil {
				return fmt.Errorf("failed to parse reply: %w", err)
			}

			title := reply.Title
			if title == "" {
				title = "Reply"
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(title),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "forward",
						Cmd:         fmt.Sprintf("bcq forwards show %s --in %s", forwardID, resolvedProjectID),
						Description: "View the forward",
					},
					output.Breadcrumb{
						Action:      "replies",
						Cmd:         fmt.Sprintf("bcq forwards replies %s --in %s", forwardID, resolvedProjectID),
						Description: "List all replies",
					},
				),
			)
		},
	}
}

// Ensure api package is imported for type reference
var _ *api.Response
