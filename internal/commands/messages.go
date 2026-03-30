package commands

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/editor"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// NewMessagesCmd creates the messages command group.
func NewMessagesCmd() *cobra.Command {
	var project string
	var messageBoard string

	cmd := &cobra.Command{
		Use:     "messages",
		Aliases: []string{"msgs"},
		Short:   "Manage message board messages",
		Long: `List, show, create, and manage messages in a project's message board.

Most projects have a single message board. If a project has multiple,
use --message-board <id> to specify which one.`,
		Annotations: map[string]string{"agent_notes": "Rich text content accepts Markdown — the CLI converts to HTML\nCross-project messages: basecamp recordings messages --json\nPinned messages appear at the top of the message board\n@mentions: prefer [@Name](mention:SGID) for zero API calls, or [@Name](person:ID) for one lookup; @Name/@First.Last for fuzzy matching"},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVar(&messageBoard, "message-board", "", "Message board ID (required if project has multiple)")

	cmd.AddCommand(
		newMessagesListCmd(&project, &messageBoard),
		newMessagesShowCmd(),
		newMessagesCreateCmd(&project, &messageBoard),
		newMessagesUpdateCmd(),
		newMessagesPublishCmd(),
		newMessagesPinCmd(),
		newMessagesUnpinCmd(),
		newRecordableTrashCmd("message"),
		newRecordableArchiveCmd("message"),
		newRecordableRestoreCmd("message"),
	)

	return cmd
}

func newMessagesListCmd(project *string, messageBoard *string) *cobra.Command {
	var limit, page int
	var all bool
	var sortField string
	var reverse bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List messages",
		Long:  "List all messages in a project's message board.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMessagesList(cmd, *project, *messageBoard, limit, page, all, sortField, reverse)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of messages to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all messages (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")
	cmd.Flags().StringVar(&sortField, "sort", "", "Sort by field (title, created, updated)")
	cmd.Flags().BoolVar(&reverse, "reverse", false, "Reverse sort order")

	return cmd
}

func runMessagesList(cmd *cobra.Command, project string, messageBoard string, limit, page int, all bool, sortField string, reverse bool) error {
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
	if sortField != "" {
		if err := validateSortField(sortField, []string{"title", "created", "updated"}); err != nil {
			return err
		}
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

	if sortField != "" {
		sortMessages(messages, sortField, reverse)
	}

	// Build response options
	respOpts := []output.ResponseOption{
		output.WithSummary(fmt.Sprintf("%d messages", len(messages))),
		output.WithBreadcrumbs(messagesListBreadcrumbs(resolvedProjectID)...),
	}

	// Add truncation notice if results may be limited
	if notice := output.TruncationNoticeWithTotal(len(messages), messagesResult.Meta.TotalCount); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}

	respOpts = append(respOpts, output.WithEntity("message"))

	return app.OK(messages, respOpts...)
}

func messagesListBreadcrumbs(resolvedProjectID string) []output.Breadcrumb {
	return []output.Breadcrumb{
		{Action: "show", Cmd: "basecamp messages show <id>", Description: "Show message details"},
		{Action: "post", Cmd: fmt.Sprintf("basecamp message <title> --in %s", resolvedProjectID), Description: "Post new message"},
		{Action: "archived", Cmd: fmt.Sprintf("basecamp recordings messages --status archived --in %s", resolvedProjectID), Description: "Browse archived messages"},
	}
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
	}

	dlDir := addDownloadAttachmentsFlag(cmd)
	cf := addCommentFlags(cmd, false)

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		app := appctx.FromContext(cmd.Context())

		if err := ensureAccount(cmd, app); err != nil {
			return err
		}

		// Extract ID from URL if provided
		messageIDStr := extractID(args[0])

		messageID, err := strconv.ParseInt(messageIDStr, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid message ID")
		}

		message, err := app.Account().Messages().Get(cmd.Context(), messageID)
		if err != nil {
			return convertSDKError(err)
		}

		enrichment := fetchCommentsForRecording(cmd.Context(), app, messageIDStr, cf)

		opts := []output.ResponseOption{
			output.WithSummary(fmt.Sprintf("Message: %s", message.Subject)),
			output.WithEntity("message"),
			output.WithBreadcrumbs(
				output.Breadcrumb{
					Action:      "comment",
					Cmd:         fmt.Sprintf("basecamp comment %s <text>", messageIDStr),
					Description: "Add comment",
				},
			),
		}

		data := any(message)
		attachmentNotice := ""
		attachments := downloadableAttachments(richtext.ParseAttachments(message.Content))
		if len(attachments) > 0 {
			dl := runDownloadAttachments(cmd, app, attachments, dlDir)
			var dlResults []attachmentResult
			if dl != nil {
				dlResults = dl.Results
			}
			data = withAttachmentMeta(message, "content", attachments, dlResults)
			attachmentNotice = fmt.Sprintf("%d attachment(s) — download: basecamp attachments download %s",
				len(attachments), messageIDStr)
			if dl != nil && dl.Notice != "" {
				attachmentNotice += "; " + dl.Notice
			}
			opts = append(opts,
				output.WithBreadcrumbs(attachmentBreadcrumb(messageIDStr, len(attachments))),
			)
		}

		data, extraOpts := enrichment.apply(data, attachmentNotice)
		opts = append(opts, extraOpts...)

		return app.OK(data, opts...)
	}

	return cmd
}

func newMessagesCreateCmd(project *string, messageBoard *string) *cobra.Command {
	var edit bool
	var draft bool
	var subscribe string
	var noSubscribe bool
	var attachFiles []string

	cmd := &cobra.Command{
		Use:   "create <title> [body]",
		Short: "Create a new message",
		Long:  "Post a new message to a project's message board.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no title
			if len(args) == 0 {
				return missingArg(cmd, "<title>")
			}
			title := args[0]

			// Body from second positional arg or --editor
			var body string
			if len(args) > 1 {
				body = args[1]
			}

			if strings.TrimSpace(title) == "" {
				return cmd.Help()
			}

			// Validate user input first, before checking account
			if edit && body != "" {
				return output.ErrUsage("cannot combine --edit and body argument")
			}
			if edit {
				fi, err := os.Stdin.Stat()
				if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
					return output.ErrUsage("cannot use --edit when stdin is not a terminal")
				}
				var editorErr error
				body, editorErr = editor.Open("")
				if editorErr != nil {
					return output.ErrUsage(editorErr.Error())
				}
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Resolve subscription flags before project (fail fast on bad input)
			subs, err := applySubscribeFlags(cmd.Context(), app.Names, subscribe, cmd.Flags().Changed("subscribe"), noSubscribe)
			if err != nil {
				return err
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
			html := richtext.MarkdownToHTML(body)

			// Resolve inline images (![alt](./path) → upload + <bc-attachment>)
			html, err = resolveLocalImages(cmd, app, html)
			if err != nil {
				return err
			}

			// Resolve @mentions
			mentionResult, err := resolveMentions(cmd.Context(), app.Names, html)
			if err != nil {
				return err
			}
			html = mentionResult.HTML
			mentionNotice := unresolvedMentionWarning(mentionResult.Unresolved)

			// Upload explicit --attach files and embed
			if len(attachFiles) > 0 {
				refs, attachErr := uploadAttachments(cmd, app, attachFiles)
				if attachErr != nil {
					return attachErr
				}
				html = richtext.EmbedAttachments(html, refs)
			}

			req := &basecamp.CreateMessageRequest{
				Subject:       title,
				Content:       html,
				Subscriptions: subs,
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

			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("Posted message #%d", message.ID)),
				output.WithEntity("message"),
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
			}
			if mentionNotice != "" {
				respOpts = append(respOpts, output.WithDiagnostic(mentionNotice))
			}
			return app.OK(message, respOpts...)
		},
	}

	cmd.Flags().BoolVar(&edit, "edit", false, "Open $EDITOR to compose message body")
	cmd.Flags().BoolVar(&draft, "draft", false, "Create as draft (don't publish)")
	cmd.Flags().StringVar(&subscribe, "subscribe", "", "Subscribe specific people (comma-separated names, emails, IDs, or \"me\")")
	cmd.Flags().BoolVar(&noSubscribe, "no-subscribe", false, "Don't subscribe anyone else (silent, no notifications)")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")

	return cmd
}

func newMessagesUpdateCmd() *cobra.Command {
	var title string
	var body string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a message",
		Long: `Update an existing message's title or body.

You can pass either a message ID or a Basecamp URL:
  basecamp messages update 789 --title "new title"
  basecamp messages update 789 --body "new body"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(title) == "" && strings.TrimSpace(body) == "" {
				return noChanges(cmd)
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			messageIDStr := extractID(args[0])

			messageID, err := strconv.ParseInt(messageIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid message ID")
			}

			// Build SDK request
			// Convert Markdown content to HTML for Basecamp's rich text fields
			html := richtext.MarkdownToHTML(body)

			// Resolve inline images (![alt](./path) → upload + <bc-attachment>)
			html, err = resolveLocalImages(cmd, app, html)
			if err != nil {
				return err
			}

			// Resolve @mentions
			mentionResult, err := resolveMentions(cmd.Context(), app.Names, html)
			if err != nil {
				return err
			}
			html = mentionResult.HTML

			req := &basecamp.UpdateMessageRequest{
				Subject: title,
				Content: html,
			}

			message, err := app.Account().Messages().Update(cmd.Context(), messageID, req)
			if err != nil {
				return convertSDKError(err)
			}

			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("Updated message #%s", messageIDStr)),
				output.WithEntity("message"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp messages show %s", messageIDStr),
						Description: "View message",
					},
				),
			}
			if notice := unresolvedMentionWarning(mentionResult.Unresolved); notice != "" {
				respOpts = append(respOpts, output.WithDiagnostic(notice))
			}
			return app.OK(message, respOpts...)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "New title")
	cmd.Flags().StringVarP(&body, "body", "b", "", "New body content")

	return cmd
}

func newMessagesPublishCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "publish <id|url>",
		Short: "Publish a draft message",
		Long: `Publish a draft message, making it visible on the message board.

You can pass either a message ID or a Basecamp URL:
  basecamp messages publish 789
  basecamp messages publish https://3.basecamp.com/123/buckets/456/messages/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			messageIDStr := extractID(args[0])

			messageID, err := strconv.ParseInt(messageIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid message ID")
			}

			req := &basecamp.UpdateMessageRequest{
				Status: "active",
			}

			message, err := app.Account().Messages().Update(cmd.Context(), messageID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(message,
				output.WithSummary(fmt.Sprintf("Published message #%s", messageIDStr)),
				output.WithEntity("message"),
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
			messageIDStr := extractID(args[0])

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
			messageIDStr := extractID(args[0])

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
	var edit bool
	var project string
	var messageBoard string
	var draft bool
	var subscribe string
	var noSubscribe bool
	var attachFiles []string

	cmd := &cobra.Command{
		Use:   "message <title> [body]",
		Short: "Post a new message",
		Long: `Post a message to a project's message board. Shortcut for 'basecamp messages create'.

Most projects have a single message board. If a project has multiple,
use --message-board <id> to specify which one.

Content supports Markdown and @mentions (@Name or @First.Last):
  basecamp message "Title" "Hey @Jane.Smith, **check this out**"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Show help when invoked with no title
			if len(args) == 0 {
				return missingArg(cmd, "<title>")
			}
			title := args[0]
			if strings.TrimSpace(title) == "" {
				return cmd.Help()
			}

			// Body from second positional arg or --editor
			var body string
			if len(args) > 1 {
				body = args[1]
			}

			// Validate user input first, before checking account
			if edit && body != "" {
				return output.ErrUsage("cannot combine --edit and body argument")
			}
			if edit {
				fi, err := os.Stdin.Stat()
				if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
					return output.ErrUsage("cannot use --edit when stdin is not a terminal")
				}
				var editorErr error
				body, editorErr = editor.Open("")
				if editorErr != nil {
					return output.ErrUsage(editorErr.Error())
				}
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Resolve subscription flags before project (fail fast on bad input)
			subs, err := applySubscribeFlags(cmd.Context(), app.Names, subscribe, cmd.Flags().Changed("subscribe"), noSubscribe)
			if err != nil {
				return err
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
			html := richtext.MarkdownToHTML(body)

			// Resolve inline images (![alt](./path) → upload + <bc-attachment>)
			html, err = resolveLocalImages(cmd, app, html)
			if err != nil {
				return err
			}

			// Resolve @mentions
			mentionResult, err := resolveMentions(cmd.Context(), app.Names, html)
			if err != nil {
				return err
			}
			html = mentionResult.HTML
			mentionNotice := unresolvedMentionWarning(mentionResult.Unresolved)

			// Upload explicit --attach files and embed
			if len(attachFiles) > 0 {
				refs, attachErr := uploadAttachments(cmd, app, attachFiles)
				if attachErr != nil {
					return attachErr
				}
				html = richtext.EmbedAttachments(html, refs)
			}

			req := &basecamp.CreateMessageRequest{
				Subject:       title,
				Content:       html,
				Subscriptions: subs,
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

			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("Posted message #%d", message.ID)),
				output.WithEntity("message"),
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
			}
			if mentionNotice != "" {
				respOpts = append(respOpts, output.WithDiagnostic(mentionNotice))
			}
			return app.OK(message, respOpts...)
		},
	}

	cmd.Flags().BoolVar(&edit, "edit", false, "Open $EDITOR to compose message body")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().StringVar(&messageBoard, "message-board", "", "Message board ID (required if project has multiple)")
	cmd.Flags().BoolVar(&draft, "draft", false, "Create as draft (don't publish)")
	cmd.Flags().StringVar(&subscribe, "subscribe", "", "Subscribe specific people (comma-separated names, emails, IDs, or \"me\")")
	cmd.Flags().BoolVar(&noSubscribe, "no-subscribe", false, "Don't subscribe anyone else (silent, no notifications)")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")

	return cmd
}

// getMessageBoardID retrieves the message board ID from a project's dock, handling multi-dock projects.
func getMessageBoardID(cmd *cobra.Command, app *appctx.App, projectID string, explicitID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "message_board", explicitID, "message board", "message-board")
}
