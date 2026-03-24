package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// NewChatCmd creates the chat command for real-time chat.
func NewChatCmd() *cobra.Command {
	var project string
	var chatID string
	var contentType string

	cmd := &cobra.Command{
		Use:     "chat [action]",
		Aliases: []string{"campfire"},
		Short:   "Interact with chat",
		Long: `Interact with chat (real-time messaging).

Use 'basecamp chat list' to see chats in a project.
Use 'basecamp chat messages' to view recent messages.
Use 'basecamp chat post "message"' to post a message.`,
		Annotations: map[string]string{"agent_notes": "Projects may have multiple chats — use --room to target a specific one\nContent is sent as plain text by default; use --content-type text/html for rich text\nChat is project-scoped, no cross-project chat queries\n@mentions: prefer [@Name](mention:SGID) for zero API calls, or [@Name](person:ID) for one lookup; @Name/@First.Last for fuzzy matching (auto-promotes to text/html)\nUse --content-type text/plain to bypass mention resolution"},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVarP(&chatID, "room", "r", "", "Campfire room ID (for projects with multiple rooms)")
	cmd.AddCommand(
		newChatListCmd(&project, &chatID),
		newChatMessagesCmd(&project, &chatID),
		newChatPostCmd(&project, &chatID, &contentType),
		newChatUploadCmd(&project, &chatID),
		newChatLineShowCmd(&project, &chatID),
		newChatLineDeleteCmd(&project, &chatID),
	)

	return cmd
}

func newChatListCmd(project, chatID *string) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List chats",
		Long:  "List chats in a project or account-wide with --all.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			return runChatList(cmd, app, *project, *chatID, all)
		},
	}

	cmd.Flags().BoolVarP(&all, "all", "A", false, "List all chats across account")

	return cmd
}

func runChatList(cmd *cobra.Command, app *appctx.App, project, chatID string, all bool) error {
	// Account-wide chat listing
	if all {
		result, err := app.Account().Campfires().List(cmd.Context(), nil)
		if err != nil {
			return err
		}
		chats := result.Campfires

		summary := fmt.Sprintf("%d chats", len(chats))

		return app.OK(chats,
			output.WithSummary(summary),
			output.WithBreadcrumbs(
				output.Breadcrumb{
					Action:      "messages",
					Cmd:         "basecamp chat messages --room <id> --in <project>",
					Description: "View messages",
				},
				output.Breadcrumb{
					Action:      "post",
					Cmd:         "basecamp chat post \"message\" --room <id> --in <project>",
					Description: "Post message",
				},
			),
		)
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
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	// If a specific room ID was given via --room, fetch just that one
	if chatID != "" {
		chatIDInt, parseErr := strconv.ParseInt(chatID, 10, 64)
		if parseErr != nil {
			return output.ErrUsage("Invalid chat room ID")
		}

		chat, getErr := app.Account().Campfires().Get(cmd.Context(), chatIDInt)
		if getErr != nil {
			return getErr
		}

		return app.OK([]*basecamp.Campfire{chat},
			output.WithSummary(fmt.Sprintf("Chat: %s", chatTitle(chat))),
			output.WithBreadcrumbs(chatListBreadcrumbs(chatID, resolvedProjectID)...),
		)
	}

	// Get all enabled chats from project dock
	enabled, allTools, err := getDockTools(cmd.Context(), app, resolvedProjectID, "chat")
	if err != nil {
		return err
	}
	if len(enabled) == 0 {
		return dockToolNotFoundError(allTools, "chat", resolvedProjectID, "chat room")
	}

	// Fetch full details for each enabled chat
	var chats []*basecamp.Campfire
	for _, match := range enabled {
		chat, getErr := app.Account().Campfires().Get(cmd.Context(), match.ID)
		if getErr != nil {
			return getErr
		}
		chats = append(chats, chat)
	}

	// Summary: title-based for single, count-based for multiple
	var summary string
	if len(chats) == 1 {
		summary = fmt.Sprintf("Chat: %s", chatTitle(chats[0]))
	} else {
		summary = fmt.Sprintf("%d chats in project", len(chats))
	}

	// Breadcrumbs: concrete ID for single, placeholder for multiple
	chatRef := "<id>"
	if len(chats) == 1 {
		chatRef = strconv.FormatInt(chats[0].ID, 10)
	}

	return app.OK(chats,
		output.WithSummary(summary),
		output.WithBreadcrumbs(chatListBreadcrumbs(chatRef, resolvedProjectID)...),
	)
}

func chatTitle(c *basecamp.Campfire) string {
	if c.Title != "" {
		return c.Title
	}
	return "Chat"
}

func chatListBreadcrumbs(chatID, projectID string) []output.Breadcrumb {
	return []output.Breadcrumb{
		{
			Action:      "messages",
			Cmd:         fmt.Sprintf("basecamp chat messages --room %s --in %s", chatID, projectID),
			Description: "View messages",
		},
		{
			Action:      "post",
			Cmd:         fmt.Sprintf("basecamp chat post \"message\" --room %s --in %s", chatID, projectID),
			Description: "Post message",
		},
	}
}

func newChatMessagesCmd(project, chatID *string) *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "messages",
		Short: "View recent messages",
		Long:  "View recent messages from a chat.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			return runChatMessages(cmd, app, *chatID, *project, limit)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 25, "Number of messages to show")

	return cmd
}

func runChatMessages(cmd *cobra.Command, app *appctx.App, chatID, project string, limit int) error {
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

	// Get chat ID from project if not specified
	if chatID == "" {
		chatID, err = getChatID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	chatIDInt, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid chat room ID")
	}

	if limit < 0 {
		return output.ErrUsage("--limit must not be negative")
	}

	// Get recent messages (lines) using SDK, newest first
	result, err := app.Account().Campfires().ListLines(cmd.Context(), chatIDInt, &basecamp.CampfireLineListOptions{
		Sort:      "created_at",
		Direction: "desc",
		Limit:     limit,
	})
	if err != nil {
		return err
	}
	lines := result.Lines

	// Reverse to chronological order for display (API returns newest-first)
	slices.Reverse(lines)

	summary := fmt.Sprintf("%d messages", len(lines))

	return app.OK(lines,
		output.WithSummary(summary),
		output.WithEntity("chat_line"),
		output.WithDisplayData(chatLinesDisplayData(lines)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "post",
				Cmd:         fmt.Sprintf("basecamp chat post \"message\" --room %s --in %s", chatID, resolvedProjectID),
				Description: "Post message",
			},
			output.Breadcrumb{
				Action:      "more",
				Cmd:         fmt.Sprintf("basecamp chat messages --limit 50 --room %s --in %s", chatID, resolvedProjectID),
				Description: "Load more",
			},
		),
	)
}

func newChatPostCmd(project, chatID, contentType *string) *cobra.Command {
	var content string
	var attachFiles []string

	cmd := &cobra.Command{
		Use:   "post <message>",
		Short: "Post a message",
		Long: `Post a message to a chat.

By default, messages are sent as plain text. Use --content-type text/html
for rich text (HTML) messages.

@mentions (@Name or @First.Last) are resolved automatically and the
content type is promoted to text/html when mentions are present.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Validate user input first, before checking account
			messageContent := content
			if len(args) > 0 {
				messageContent = args[0]
			}

			// Show help when invoked with no message content
			if strings.TrimSpace(messageContent) == "" {
				return missingArg(cmd, "<message>")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			return runChatPost(cmd, app, *chatID, *project, messageContent, *contentType, attachFiles)
		},
	}

	cmd.Flags().StringVar(&content, "content", "", "Message content")
	cmd.Flags().StringVar(contentType, "content-type", "", "Content type (text/html for rich text)")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")

	return cmd
}

func runChatPost(cmd *cobra.Command, app *appctx.App, chatID, project, content, contentType string, attachFiles []string) error {
	// Resolve project only when needed (chat ID not provided, or for breadcrumbs)
	var resolvedProjectID string
	if chatID == "" {
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

		var err error
		resolvedProjectID, _, err = app.Names.ResolveProject(cmd.Context(), projectID)
		if err != nil {
			return err
		}

		chatID, err = getChatID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	chatIDInt, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid chat room ID")
	}

	// Resolve @mentions — skip if user explicitly set a non-HTML content type.
	// When contentType is unset, convert Markdown to HTML first so the mention
	// resolver operates on HTML input.
	var mentionNotice string
	if contentType == "" || contentType == "text/html" {
		mentionInput := content
		if contentType == "" {
			mentionInput = richtext.MarkdownToHTML(content)
		}
		result, resolveErr := resolveMentions(cmd.Context(), app.Names, mentionInput)
		if resolveErr != nil {
			return resolveErr
		}
		if result.HTML != mentionInput || len(result.Unresolved) > 0 {
			content = result.HTML
			if contentType == "" {
				contentType = "text/html"
			}
		}
		mentionNotice = unresolvedMentionWarning(result.Unresolved)
	}

	// Post message using SDK
	var line *basecamp.CampfireLine
	var uploadIDs []int64

	// Post text message if there's content
	if content != "" {
		var opts *basecamp.CreateLineOptions
		if contentType != "" {
			opts = &basecamp.CreateLineOptions{ContentType: contentType}
		}
		var err error
		line, err = app.Account().Campfires().CreateLine(cmd.Context(), chatIDInt, content, opts)
		if err != nil {
			return convertSDKError(err)
		}
	}

	// Upload attachments using CreateUpload
	for _, filePath := range attachFiles {
		normalized := richtext.NormalizeDragPath(filePath)
		if err := richtext.ValidateFile(normalized); err != nil {
			return fmt.Errorf("%s: %w", filePath, err)
		}

		mimeType := richtext.DetectMIME(normalized)
		filename := filepath.Base(normalized)

		f, err := os.Open(normalized)
		if err != nil {
			return fmt.Errorf("%s: %w", filePath, err)
		}

		uploadLine, err := app.Account().Campfires().CreateUpload(cmd.Context(), chatIDInt, filename, mimeType, f)
		f.Close()
		if err != nil {
			sdkErr := convertSDKError(err)
			if outErr, ok := sdkErr.(*output.Error); ok {
				outErr.Message = fmt.Sprintf("%s: %s", filePath, outErr.Message)
				return outErr
			}
			return fmt.Errorf("%s: %w", filePath, sdkErr)
		}
		uploadIDs = append(uploadIDs, uploadLine.ID)
	}

	// Build summary
	var summary string
	if line != nil && len(uploadIDs) > 0 {
		summary = fmt.Sprintf("Posted message #%d with %d attachment(s)", line.ID, len(uploadIDs))
	} else if line != nil {
		summary = fmt.Sprintf("Posted message #%d", line.ID)
	} else if len(uploadIDs) > 0 {
		summary = fmt.Sprintf("Posted %d attachment(s)", len(uploadIDs))
	}

	// Build breadcrumbs — include project context if resolved
	var breadcrumbs []output.Breadcrumb
	if resolvedProjectID != "" {
		breadcrumbs = append(breadcrumbs,
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         fmt.Sprintf("basecamp chat messages --room %s --in %s", chatID, resolvedProjectID),
				Description: "View messages",
			},
			output.Breadcrumb{
				Action:      "post",
				Cmd:         fmt.Sprintf("basecamp chat post \"reply\" --room %s --in %s", chatID, resolvedProjectID),
				Description: "Post another",
			},
		)
	} else {
		breadcrumbs = append(breadcrumbs,
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         fmt.Sprintf("basecamp chat messages --room %s", chatID),
				Description: "View messages",
			},
			output.Breadcrumb{
				Action:      "post",
				Cmd:         fmt.Sprintf("basecamp chat post \"reply\" --room %s", chatID),
				Description: "Post another",
			},
		)
	}

	respOpts := []output.ResponseOption{
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	}
	if mentionNotice != "" {
		respOpts = append(respOpts, output.WithDiagnostic(mentionNotice))
	}

	// Text-only: return the Line object directly (preserves JSON contract)
	if line != nil && len(uploadIDs) == 0 {
		respOpts = append(respOpts,
			output.WithEntity("chat_line"),
			output.WithDisplayData(chatLineDisplayData(line)),
		)
		return app.OK(line, respOpts...)
	}

	// Uploads involved: return composite result
	result := map[string]any{}
	if line != nil {
		result["message_id"] = line.ID
	}
	if len(uploadIDs) > 0 {
		result["upload_ids"] = uploadIDs
	}
	return app.OK(result, respOpts...)
}

func newChatUploadCmd(project, chatID *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upload <file>",
		Short: "Upload a file to chat",
		Long: `Upload a file directly to a chat room.

The file is uploaded as a chat line (message with an attachment).`,
		Example: `  basecamp chat upload ./screenshot.png --in my-project`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			return runChatUpload(cmd, app, *chatID, *project, args[0])
		},
	}
	return cmd
}

func runChatUpload(cmd *cobra.Command, app *appctx.App, chatID, project, filePath string) error {
	// Normalize drag/paste paths and validate
	filePath = richtext.NormalizeDragPath(filePath)
	if err := richtext.ValidateFile(filePath); err != nil {
		return fmt.Errorf("%s: %w", filePath, err)
	}

	// Resolve project — required when chat ID not provided, optional for breadcrumbs
	var resolvedProjectID string
	if chatID == "" {
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

		var err error
		resolvedProjectID, _, err = app.Names.ResolveProject(cmd.Context(), projectID)
		if err != nil {
			return err
		}

		chatID, err = getChatID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	} else if project != "" {
		// Chat ID provided directly — still resolve project for breadcrumbs
		var err error
		resolvedProjectID, _, err = app.Names.ResolveProject(cmd.Context(), project)
		if err != nil {
			return err
		}
	}

	chatIDInt, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid chat room ID")
	}

	contentType := richtext.DetectMIME(filePath)
	filename := filepath.Base(filePath)

	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("%s: %w", filePath, err)
	}
	defer f.Close()

	line, err := app.Account().Campfires().CreateUpload(cmd.Context(), chatIDInt, filename, contentType, f)
	if err != nil {
		return convertSDKError(err)
	}

	// Build breadcrumbs
	var breadcrumbs []output.Breadcrumb
	if resolvedProjectID != "" {
		breadcrumbs = append(breadcrumbs,
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         fmt.Sprintf("basecamp chat messages --room %s --in %s", chatID, resolvedProjectID),
				Description: "View messages",
			},
		)
	} else {
		breadcrumbs = append(breadcrumbs,
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         fmt.Sprintf("basecamp chat messages --room %s", chatID),
				Description: "View messages",
			},
		)
	}

	// Build summary — prefer attachment filename from API response over local basename
	uploadName := filename
	if len(line.Attachments) > 0 && line.Attachments[0].Filename != "" {
		uploadName = line.Attachments[0].Filename
	}
	summary := fmt.Sprintf("Uploaded %s (#%d)", uploadName, line.ID)
	if len(line.Attachments) > 0 && line.Attachments[0].ByteSize > 0 {
		summary = fmt.Sprintf("Uploaded %s (%s) (#%d)", uploadName, humanSize(line.Attachments[0].ByteSize), line.ID)
	}

	return app.OK(line,
		output.WithSummary(summary),
		output.WithEntity("chat_line"),
		output.WithDisplayData(chatLineDisplayData(line)),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}

func newChatLineShowCmd(project, chatID *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "line <id|url>",
		Aliases: []string{"show"},
		Short:   "Show a specific message",
		Long: `Show details of a specific message line.

You can pass either a line ID or a Basecamp line URL:
  basecamp chat line 789 --in my-project
  basecamp chat line https://3.basecamp.com/123/buckets/456/chats/789/lines/111`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			lineID, urlProjectID := extractWithProject(args[0])

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

			// Get chat ID from project if not specified
			effectiveChatID := *chatID
			if effectiveChatID == "" {
				effectiveChatID, err = getChatID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}

			chatIDInt, err := strconv.ParseInt(effectiveChatID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid chat room ID")
			}
			lineIDInt, err := strconv.ParseInt(lineID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid line ID")
			}

			// Get line using SDK
			line, err := app.Account().Campfires().GetLine(cmd.Context(), chatIDInt, lineIDInt)
			if err != nil {
				return err
			}

			creatorName := ""
			if line.Creator != nil {
				creatorName = line.Creator.Name
			}
			summary := fmt.Sprintf("Line #%s by %s", lineID, creatorName)

			return app.OK(line,
				output.WithSummary(summary),
				output.WithEntity("chat_line"),
				output.WithDisplayData(chatLineDisplayData(line)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("basecamp chat delete %s --room %s --in %s", lineID, effectiveChatID, resolvedProjectID),
						Description: "Delete line",
					},
					output.Breadcrumb{
						Action:      "messages",
						Cmd:         fmt.Sprintf("basecamp chat messages --room %s --in %s", effectiveChatID, resolvedProjectID),
						Description: "Back to messages",
					},
				),
			)
		},
	}
	return cmd
}

func newChatLineDeleteCmd(project, chatID *string) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <id|url>",
		Short: "Delete a message",
		Long: `Delete a message line from a chat.

This permanently deletes the message — it is not moved to trash.

You can pass either a line ID or a Basecamp line URL:
  basecamp chat delete 789 --in my-project
  basecamp chat delete https://3.basecamp.com/123/buckets/456/chats/789/lines/111`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			lineID, urlProjectID := extractWithProject(args[0])

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

			// Get chat ID from project if not specified
			effectiveChatID := *chatID
			if effectiveChatID == "" {
				effectiveChatID, err = getChatID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}

			chatIDInt, err := strconv.ParseInt(effectiveChatID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid chat room ID")
			}
			lineIDInt, err := strconv.ParseInt(lineID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid line ID")
			}

			// Confirm destructive action in interactive mode
			if !force && !isMachineOutput(cmd) {
				confirmed, err := tui.ConfirmDangerous("Permanently delete this chat line?")
				if err != nil {
					return nil //nolint:nilerr // user canceled prompt
				}
				if !confirmed {
					return nil
				}
			}

			// Delete line using SDK
			err = app.Account().Campfires().DeleteLine(cmd.Context(), chatIDInt, lineIDInt)
			if err != nil {
				return err
			}

			summary := fmt.Sprintf("Deleted line #%s", lineID)

			return app.OK(map[string]any{"deleted": true, "id": lineID},
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "messages",
						Cmd:         fmt.Sprintf("basecamp chat messages --room %s --in %s", effectiveChatID, resolvedProjectID),
						Description: "Back to messages",
					},
				),
			)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompt")

	return cmd
}

// getChatID retrieves the chat ID from a project's dock, handling multi-dock projects.
func getChatID(cmd *cobra.Command, app *appctx.App, projectID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "chat", "", "chat room", "room")
}

// chatLineDisplayContent produces human-readable content for a campfire line,
// injecting attachment file sizes where present.
func chatLineDisplayContent(line *basecamp.CampfireLine) string {
	if line.Content != "" {
		if richtext.IsHTML(line.Content) {
			text := richtext.HTMLToMarkdown(line.Content)
			return injectAttachmentSizes(text, line.Attachments)
		}
		if len(line.Attachments) > 0 {
			return line.Content + "\n" + formatChatAttachments(line.Attachments)
		}
		return line.Content
	}
	if len(line.Attachments) > 0 {
		return formatChatAttachments(line.Attachments)
	}
	if line.Title != "" {
		return line.Title
	}
	return ""
}

// injectAttachmentSizes rewrites deterministic attachment marker lines
// (📎 filename) produced by richtext.HTMLToMarkdown, appending (size).
// Only exact marker lines are modified; user-authored text is untouched.
func injectAttachmentSizes(text string, attachments []basecamp.CampfireLineAttachment) string {
	if len(attachments) == 0 {
		return text
	}

	// Build filename → []ByteSize lookup (handles duplicate filenames).
	type sizeEntry struct {
		sizes []int64
		idx   int
	}
	lookup := make(map[string]*sizeEntry, len(attachments))
	for _, att := range attachments {
		name := att.Filename
		if name == "" {
			name = att.Title
		}
		if name == "" {
			continue
		}
		entry, ok := lookup[name]
		if !ok {
			entry = &sizeEntry{}
			lookup[name] = entry
		}
		entry.sizes = append(entry.sizes, att.ByteSize)
	}

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "📎 ") {
			continue
		}
		filename := strings.TrimPrefix(trimmed, "📎 ")
		entry, ok := lookup[filename]
		if !ok || entry.idx >= len(entry.sizes) {
			continue
		}
		size := entry.sizes[entry.idx]
		entry.idx++
		if size > 0 {
			lines[i] = fmt.Sprintf("📎 %s (%s)", filename, humanSize(size))
		}
	}
	return strings.Join(lines, "\n")
}

// formatChatAttachments builds 📎 lines from an attachment array.
func formatChatAttachments(attachments []basecamp.CampfireLineAttachment) string {
	var b strings.Builder
	for i, att := range attachments {
		name := att.Filename
		if name == "" {
			name = att.Title
		}
		if name == "" {
			name = "attachment"
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		if att.ByteSize > 0 {
			fmt.Fprintf(&b, "📎 %s (%s)", name, humanSize(att.ByteSize))
		} else {
			fmt.Fprintf(&b, "📎 %s", name)
		}
	}
	return b.String()
}

// chatLinesDisplayData builds display-data for a slice of campfire lines.
// The original structs are preserved for JSON; only the "content" field
// is replaced with the display-ready version for styled/markdown output.
func chatLinesDisplayData(lines []basecamp.CampfireLine) any {
	normalized := output.NormalizeData(lines)

	// NormalizeData returns []map[string]any when all elements are objects.
	if items, ok := normalized.([]map[string]any); ok {
		n := len(lines)
		if len(items) < n {
			n = len(items)
		}
		for i := range n {
			if display := chatLineDisplayContent(&lines[i]); display != "" {
				items[i]["content"] = display
			}
		}
		return items
	}
	return normalized
}

// chatLineDisplayData builds display-data for a single campfire line.
func chatLineDisplayData(line *basecamp.CampfireLine) any {
	normalized := output.NormalizeData(line)
	m, ok := normalized.(map[string]any)
	if !ok {
		return normalized
	}
	display := chatLineDisplayContent(line)
	if display != "" {
		m["content"] = display
	}
	return m
}
