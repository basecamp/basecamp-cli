package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// Message represents a Basecamp message.
type Message struct {
	ID        int64  `json:"id"`
	Subject   string `json:"subject"`
	Content   string `json:"content,omitempty"`
	CreatedAt string `json:"created_at"`
	Creator   struct {
		Name string `json:"name"`
	} `json:"creator"`
}

// NewMessagesCmd creates the messages command group.
func NewMessagesCmd() *cobra.Command {
	var project string
	var messageBoard string

	cmd := &cobra.Command{
		Use:     "messages",
		Aliases: []string{"msgs"},
		Short:   "Manage message board messages",
		Long:    "List, show, create, and manage messages in a project's message board.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default to list when called without subcommand
			return runMessagesList(cmd, project, messageBoard)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVar(&messageBoard, "message-board", "", "Message board ID (required if project has multiple)")

	cmd.AddCommand(
		newMessagesListCmd(&project, &messageBoard),
		newMessagesShowCmd(&project),
		newMessagesCreateCmd(&project, &messageBoard),
		newMessagesUpdateCmd(&project),
		newMessagesPinCmd(&project),
		newMessagesUnpinCmd(&project),
	)

	return cmd
}

func newMessagesListCmd(project *string, messageBoard *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List messages",
		Long:  "List all messages in a project's message board.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMessagesList(cmd, *project, *messageBoard)
		},
	}
}

func runMessagesList(cmd *cobra.Command, project string, messageBoard string) error {
	app := appctx.FromContext(cmd.Context())

	// Resolve project first (validate before account check)
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
	}

	if err := app.API.RequireAccount(); err != nil {
		return err
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	// Get message board ID from project dock
	messageBoardID, err := getMessageBoardID(cmd, app, resolvedProjectID, messageBoard)
	if err != nil {
		return err
	}

	// Get messages
	path := fmt.Sprintf("/buckets/%s/message_boards/%s/messages.json", resolvedProjectID, messageBoardID)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var messages []Message
	if err := resp.UnmarshalData(&messages); err != nil {
		return fmt.Errorf("failed to parse messages: %w", err)
	}

	return app.Output.OK(messages,
		output.WithSummary(fmt.Sprintf("%d messages", len(messages))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("bcq show message <id> --in %s", resolvedProjectID),
				Description: "Show message details",
			},
			output.Breadcrumb{
				Action:      "post",
				Cmd:         fmt.Sprintf("bcq message --subject <text> --in %s", resolvedProjectID),
				Description: "Post new message",
			},
		),
	)
}

func newMessagesShowCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show message details",
		Long:  "Display detailed information about a message.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			messageID := args[0]

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			path := fmt.Sprintf("/buckets/%s/messages/%s.json", resolvedProjectID, messageID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var message Message
			if err := json.Unmarshal(resp.Data, &message); err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Message: %s", message.Subject)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "comment",
						Cmd:         fmt.Sprintf("bcq comment --content <text> --on %s --in %s", messageID, resolvedProjectID),
						Description: "Add comment",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq messages --in %s", resolvedProjectID),
						Description: "Back to messages",
					},
				),
			)
		},
	}
	return cmd
}

func newMessagesCreateCmd(project *string, messageBoard *string) *cobra.Command {
	var subject string
	var content string
	var draft bool

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new message",
		Long:  "Post a new message to a project's message board.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			if subject == "" {
				return output.ErrUsage("--subject is required")
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
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Get message board ID from project dock
			messageBoardID, err := getMessageBoardID(cmd, app, resolvedProjectID, *messageBoard)
			if err != nil {
				return err
			}

			// Build request body
			body := map[string]string{
				"subject": subject,
			}
			if content != "" {
				body["content"] = content
			}

			// Default to active (published) status unless --draft is specified
			if draft {
				body["status"] = "drafted"
			} else {
				body["status"] = "active"
			}

			path := fmt.Sprintf("/buckets/%s/message_boards/%s/messages.json", resolvedProjectID, messageBoardID)
			resp, err := app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var message struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(resp.Data, &message); err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Posted message #%d", message.ID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "view",
						Cmd:         fmt.Sprintf("bcq show message %d --in %s", message.ID, resolvedProjectID),
						Description: "View message",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq messages --in %s", resolvedProjectID),
						Description: "List messages",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&subject, "subject", "s", "", "Message subject (required)")
	cmd.Flags().StringVarP(&content, "content", "b", "", "Message body content")
	cmd.Flags().StringVar(&content, "body", "", "Message body content (alias for --content)")
	cmd.Flags().BoolVar(&draft, "draft", false, "Create as draft (don't publish)")
	cmd.MarkFlagRequired("subject")

	return cmd
}

func newMessagesUpdateCmd(project *string) *cobra.Command {
	var subject string
	var content string

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a message",
		Long:  "Update an existing message's subject or content.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			messageID := args[0]

			if subject == "" && content == "" {
				return output.ErrUsage("at least one of --subject or --content is required")
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
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Build request body
			body := make(map[string]string)
			if subject != "" {
				body["subject"] = subject
			}
			if content != "" {
				body["content"] = content
			}

			path := fmt.Sprintf("/buckets/%s/messages/%s.json", resolvedProjectID, messageID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Updated message #%s", messageID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq messages show %s --in %s", messageID, resolvedProjectID),
						Description: "View message",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&subject, "subject", "s", "", "New message subject")
	cmd.Flags().StringVarP(&content, "content", "b", "", "New message content")
	cmd.Flags().StringVar(&content, "body", "", "New message content (alias for --content)")

	return cmd
}

func newMessagesPinCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pin <id>",
		Short: "Pin a message",
		Long:  "Pin a message to the top of the message board.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			messageID := args[0]

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			path := fmt.Sprintf("/buckets/%s/recordings/%s/pin.json", resolvedProjectID, messageID)
			_, err = app.API.Post(cmd.Context(), path, map[string]string{})
			if err != nil {
				return err
			}

			return app.Output.OK(map[string]string{
				"id":     messageID,
				"status": "pinned",
			},
				output.WithSummary(fmt.Sprintf("Pinned message #%s", messageID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "unpin",
						Cmd:         fmt.Sprintf("bcq messages unpin %s --in %s", messageID, resolvedProjectID),
						Description: "Unpin message",
					},
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq messages show %s --in %s", messageID, resolvedProjectID),
						Description: "View message",
					},
				),
			)
		},
	}
	return cmd
}

func newMessagesUnpinCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unpin <id>",
		Short: "Unpin a message",
		Long:  "Remove a message from the pinned position.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			messageID := args[0]

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			path := fmt.Sprintf("/buckets/%s/recordings/%s/pin.json", resolvedProjectID, messageID)
			_, err = app.API.Delete(cmd.Context(), path)
			if err != nil {
				return err
			}

			return app.Output.OK(map[string]string{
				"id":     messageID,
				"status": "unpinned",
			},
				output.WithSummary(fmt.Sprintf("Unpinned message #%s", messageID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "pin",
						Cmd:         fmt.Sprintf("bcq messages pin %s --in %s", messageID, resolvedProjectID),
						Description: "Pin message",
					},
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq messages show %s --in %s", messageID, resolvedProjectID),
						Description: "View message",
					},
				),
			)
		},
	}
	return cmd
}

// NewMessageCmd creates the message command (shortcut for creating messages).
func NewMessageCmd() *cobra.Command {
	var subject string
	var content string
	var project string
	var messageBoard string
	var draft bool

	cmd := &cobra.Command{
		Use:   "message",
		Short: "Post a new message",
		Long:  "Post a message to a project's message board. Shortcut for 'bcq messages create'.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			if subject == "" {
				return output.ErrUsage("--subject is required")
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
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Get message board ID from project dock
			messageBoardID, err := getMessageBoardID(cmd, app, resolvedProjectID, messageBoard)
			if err != nil {
				return err
			}

			// Build request body
			body := map[string]string{
				"subject": subject,
			}
			if content != "" {
				body["content"] = content
			}
			if draft {
				body["status"] = "drafted"
			} else {
				body["status"] = "active"
			}

			path := fmt.Sprintf("/buckets/%s/message_boards/%s/messages.json", resolvedProjectID, messageBoardID)
			resp, err := app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var message struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(resp.Data, &message); err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Posted message #%d", message.ID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "view",
						Cmd:         fmt.Sprintf("bcq show message %d --in %s", message.ID, resolvedProjectID),
						Description: "View message",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq messages --in %s", resolvedProjectID),
						Description: "List messages",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&subject, "subject", "s", "", "Message subject (required)")
	cmd.Flags().StringVarP(&content, "content", "b", "", "Message body content")
	cmd.Flags().StringVar(&content, "body", "", "Message body content (alias for --content)")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().StringVar(&messageBoard, "message-board", "", "Message board ID (required if project has multiple)")
	cmd.Flags().BoolVar(&draft, "draft", false, "Create as draft (don't publish)")
	cmd.MarkFlagRequired("subject")

	return cmd
}

// getMessageBoardID retrieves the message board ID from a project's dock, handling multi-dock projects.
func getMessageBoardID(cmd *cobra.Command, app *appctx.App, projectID string, explicitID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "message_board", explicitID, "message board")
}
