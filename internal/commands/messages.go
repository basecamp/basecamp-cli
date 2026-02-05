package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// NewMessagesCmd creates the messages command group.
func NewMessagesCmd() *cobra.Command {
	var project string
	var messageBoard string
	var limit, page int
	var all bool

	cmd := &cobra.Command{
		Use:     "messages",
		Aliases: []string{"msgs"},
		Short:   "Manage message board messages",
		Long:    "List, show, create, and manage messages in a project's message board.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default to list when called without subcommand
			return runMessagesList(cmd, project, messageBoard, limit, page, all)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVar(&messageBoard, "message-board", "", "Message board ID (required if project has multiple)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of messages to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all messages (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	cmd.AddCommand(
		newMessagesListCmd(&project, &messageBoard),
		newMessagesShowCmd(),
		newMessagesCreateCmd(&project, &messageBoard),
		newMessagesUpdateCmd(),
		newMessagesPinCmd(),
		newMessagesUnpinCmd(),
	)

	return cmd
}

func newMessagesListCmd(project *string, messageBoard *string) *cobra.Command {
	var limit, page int
	var all bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List messages",
		Long:  "List all messages in a project's message board.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMessagesList(cmd, *project, *messageBoard, limit, page, all)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of messages to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all messages (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	return cmd
}

func runMessagesList(cmd *cobra.Command, project string, messageBoard string, limit, page int, all bool) error {
	app := appctx.FromContext(cmd.Context())

	// Validate flag combinations
	if all && limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if page > 0 && (all || limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}
	if page > 1 {
		return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
	}

	// Resolve account (enables interactive prompt if needed)
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Resolve project from CLI flags and config, with interactive fallback
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}

	// If no project specified, try interactive resolution
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

	// Get message board ID from project dock
	messageBoardIDStr, err := getMessageBoardID(cmd, app, resolvedProjectID, messageBoard)
	if err != nil {
		return err
	}

	boardID, err := strconv.ParseInt(messageBoardIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid message board ID")
	}

	// Build pagination options
	opts := &basecamp.MessageListOptions{}
	if all {
		opts.Limit = -1 // SDK treats -1 as unlimited
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}

	// Get messages using SDK
	messagesResult, err := app.Account().Messages().List(cmd.Context(), boardID, opts)
	if err != nil {
		return convertSDKError(err)
	}
	messages := messagesResult.Messages

	// Build response options
	respOpts := []output.ResponseOption{
		output.WithSummary(fmt.Sprintf("%d messages", len(messages))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp show message <id> --in %s", resolvedProjectID),
				Description: "Show message details",
			},
			output.Breadcrumb{
				Action:      "post",
				Cmd:         fmt.Sprintf("basecamp message --subject <text> --in %s", resolvedProjectID),
				Description: "Post new message",
			},
		),
	}

	// Add truncation notice if results may be limited
	if notice := output.TruncationNoticeWithTotal(len(messages), messagesResult.Meta.TotalCount); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}

	return app.OK(messages, respOpts...)
}

func newMessagesShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show message details",
		Long: `Display detailed information about a message.

You can pass either a message ID or a Basecamp URL:
  basecamp messages show 789
  basecamp messages show https://3.basecamp.com/123/buckets/456/messages/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			messageIDStr, _ := extractWithProject(args[0])

			messageID, err := strconv.ParseInt(messageIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid message ID")
			}

			message, err := app.Account().Messages().Get(cmd.Context(), messageID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(message,
				output.WithSummary(fmt.Sprintf("Message: %s", message.Subject)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "comment",
						Cmd:         fmt.Sprintf("basecamp comment --content <text> --on %s", messageIDStr),
						Description: "Add comment",
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

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if subject == "" {
				return output.ErrUsage("--subject is required")
			}

			// Resolve project, with interactive fallback
			projectID := *project
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

			// Get message board ID from project dock
			messageBoardIDStr, err := getMessageBoardID(cmd, app, resolvedProjectID, *messageBoard)
			if err != nil {
				return err
			}

			boardID, err := strconv.ParseInt(messageBoardIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid message board ID")
			}

			// Build SDK request
			// Convert Markdown content to HTML for Basecamp's rich text fields
			req := &basecamp.CreateMessageRequest{
				Subject: subject,
				Content: richtext.MarkdownToHTML(content),
			}

			// Default to active (published) status unless --draft is specified
			if draft {
				req.Status = "drafted"
			} else {
				req.Status = "active"
			}

			message, err := app.Account().Messages().Create(cmd.Context(), boardID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(message,
				output.WithSummary(fmt.Sprintf("Posted message #%d", message.ID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "view",
						Cmd:         fmt.Sprintf("basecamp show message %d --in %s", message.ID, resolvedProjectID),
						Description: "View message",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("basecamp messages --in %s", resolvedProjectID),
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
	_ = cmd.MarkFlagRequired("subject")

	return cmd
}

func newMessagesUpdateCmd() *cobra.Command {
	var subject string
	var content string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a message",
		Long: `Update an existing message's subject or content.

You can pass either a message ID or a Basecamp URL:
  basecamp messages update 789 --subject "new title"
  basecamp messages update https://3.basecamp.com/123/buckets/456/messages/789 --subject "new title"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			messageIDStr, _ := extractWithProject(args[0])

			messageID, err := strconv.ParseInt(messageIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid message ID")
			}

			if subject == "" && content == "" {
				return output.ErrUsage("at least one of --subject or --content is required")
			}

			// Build SDK request
			// Convert Markdown content to HTML for Basecamp's rich text fields
			req := &basecamp.UpdateMessageRequest{
				Subject: subject,
				Content: richtext.MarkdownToHTML(content),
			}

			message, err := app.Account().Messages().Update(cmd.Context(), messageID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(message,
				output.WithSummary(fmt.Sprintf("Updated message #%s", messageIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp messages show %s", messageIDStr),
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

func newMessagesPinCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pin <id|url>",
		Short: "Pin a message",
		Long: `Pin a message to the top of the message board.

You can pass either a message ID or a Basecamp URL:
  basecamp messages pin 789
  basecamp messages pin https://3.basecamp.com/123/buckets/456/messages/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			messageIDStr, _ := extractWithProject(args[0])

			messageID, err := strconv.ParseInt(messageIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid message ID")
			}

			err = app.Account().Messages().Pin(cmd.Context(), messageID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]string{
				"id":     messageIDStr,
				"status": "pinned",
			},
				output.WithSummary(fmt.Sprintf("Pinned message #%s", messageIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "unpin",
						Cmd:         fmt.Sprintf("basecamp messages unpin %s", messageIDStr),
						Description: "Unpin message",
					},
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp messages show %s", messageIDStr),
						Description: "View message",
					},
				),
			)
		},
	}
	return cmd
}

func newMessagesUnpinCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unpin <id|url>",
		Short: "Unpin a message",
		Long: `Remove a message from the pinned position.

You can pass either a message ID or a Basecamp URL:
  basecamp messages unpin 789
  basecamp messages unpin https://3.basecamp.com/123/buckets/456/messages/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			messageIDStr, _ := extractWithProject(args[0])

			messageID, err := strconv.ParseInt(messageIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid message ID")
			}

			err = app.Account().Messages().Unpin(cmd.Context(), messageID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]string{
				"id":     messageIDStr,
				"status": "unpinned",
			},
				output.WithSummary(fmt.Sprintf("Unpinned message #%s", messageIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "pin",
						Cmd:         fmt.Sprintf("basecamp messages pin %s", messageIDStr),
						Description: "Pin message",
					},
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp messages show %s", messageIDStr),
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
		Long:  "Post a message to a project's message board. Shortcut for 'basecamp messages create'.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if subject == "" {
				return output.ErrUsage("--subject is required")
			}

			// Resolve project, with interactive fallback
			projectID := project
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

			// Get message board ID from project dock
			messageBoardIDStr, err := getMessageBoardID(cmd, app, resolvedProjectID, messageBoard)
			if err != nil {
				return err
			}

			boardID, err := strconv.ParseInt(messageBoardIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid message board ID")
			}

			// Build SDK request
			// Convert Markdown content to HTML for Basecamp's rich text fields
			req := &basecamp.CreateMessageRequest{
				Subject: subject,
				Content: richtext.MarkdownToHTML(content),
			}
			if draft {
				req.Status = "drafted"
			} else {
				req.Status = "active"
			}

			message, err := app.Account().Messages().Create(cmd.Context(), boardID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(message,
				output.WithSummary(fmt.Sprintf("Posted message #%d", message.ID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "view",
						Cmd:         fmt.Sprintf("basecamp show message %d --in %s", message.ID, resolvedProjectID),
						Description: "View message",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("basecamp messages --in %s", resolvedProjectID),
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
	_ = cmd.MarkFlagRequired("subject")

	return cmd
}

// getMessageBoardID retrieves the message board ID from a project's dock, handling multi-dock projects.
func getMessageBoardID(cmd *cobra.Command, app *appctx.App, projectID string, explicitID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "message_board", explicitID, "message board")
}
