package commands

import (
	"fmt"
	"os"
	"path/filepath"
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
		Annotations: map[string]string{"agent_notes": "Projects may have multiple chats — use --chat to target a specific one\nContent is sent as plain text by default; use --content-type text/html for rich text\nChat is project-scoped, no cross-project chat queries\n@mentions supported: use @Name or @First.Last in content to create clickable mentions (auto-promotes to text/html)"},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVarP(&chatID, "chat", "c", "", "Chat room ID (for projects with multiple chat rooms)")
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
					Cmd:         "basecamp chat messages --chat <id> --in <project>",
					Description: "View messages",
				},
				output.Breadcrumb{
					Action:      "post",
					Cmd:         "basecamp chat post \"message\" --chat <id> --in <project>",
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

	// If a specific chat ID was given via --chat, fetch just that one
	if chatID != "" {
		chatIDInt, parseErr := strconv.ParseInt(chatID, 10, 64)
		if parseErr != nil {
			return output.ErrUsage("Invalid chat ID")
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
		return dockToolNotFoundError(allTools, "chat", resolvedProjectID, "chat")
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
			Cmd:         fmt.Sprintf("basecamp chat messages --chat %s --in %s", chatID, projectID),
			Description: "View messages",
		},
		{
			Action:      "post",
			Cmd:         fmt.Sprintf("basecamp chat post \"message\" --chat %s --in %s", chatID, projectID),
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
		return output.ErrUsage("Invalid chat ID")
	}

	// Get recent messages (lines) using SDK
	result, err := app.Account().Campfires().ListLines(cmd.Context(), chatIDInt, nil)
	if err != nil {
		return err
	}
	lines := result.Lines

	// Take last N messages (newest)
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}

	summary := fmt.Sprintf("%d messages", len(lines))

	return app.OK(lines,
		output.WithSummary(summary),
		output.WithEntity("chat_line"),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "post",
				Cmd:         fmt.Sprintf("basecamp chat post \"message\" --chat %s --in %s", chatID, resolvedProjectID),
				Description: "Post message",
			},
			output.Breadcrumb{
				Action:      "more",
				Cmd:         fmt.Sprintf("basecamp chat messages --limit 50 --chat %s --in %s", chatID, resolvedProjectID),
				Description: "Load more",
			},
		),
	)
}

func newChatPostCmd(project, chatID, contentType *string) *cobra.Command {
	var content string

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

			return runChatPost(cmd, app, *chatID, *project, messageContent, *contentType)
		},
	}

	cmd.Flags().StringVar(&content, "content", "", "Message content")
	cmd.Flags().StringVar(contentType, "content-type", "", "Content type (text/html for rich text)")

	return cmd
}

func runChatPost(cmd *cobra.Command, app *appctx.App, chatID, project, content, contentType string) error {
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
		return output.ErrUsage("Invalid chat ID")
	}

	// Resolve @mentions — if any are found, content becomes HTML
	resolved, resolveErr := resolveMentions(cmd.Context(), app.Names, content)
	if resolveErr != nil {
		return resolveErr
	}
	if resolved != content {
		content = resolved
		if contentType == "" {
			contentType = "text/html"
		}
	}

	// Post message using SDK
	var opts *basecamp.CreateLineOptions
	if contentType != "" {
		opts = &basecamp.CreateLineOptions{ContentType: contentType}
	}
	line, err := app.Account().Campfires().CreateLine(cmd.Context(), chatIDInt, content, opts)
	if err != nil {
		return err
	}

	summary := fmt.Sprintf("Posted message #%d", line.ID)

	// Build breadcrumbs — include project context if resolved
	var breadcrumbs []output.Breadcrumb
	if resolvedProjectID != "" {
		breadcrumbs = append(breadcrumbs,
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         fmt.Sprintf("basecamp chat messages --chat %s --in %s", chatID, resolvedProjectID),
				Description: "View messages",
			},
			output.Breadcrumb{
				Action:      "post",
				Cmd:         fmt.Sprintf("basecamp chat post \"reply\" --chat %s --in %s", chatID, resolvedProjectID),
				Description: "Post another",
			},
		)
	} else {
		breadcrumbs = append(breadcrumbs,
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         fmt.Sprintf("basecamp chat messages --chat %s", chatID),
				Description: "View messages",
			},
			output.Breadcrumb{
				Action:      "post",
				Cmd:         fmt.Sprintf("basecamp chat post \"reply\" --chat %s", chatID),
				Description: "Post another",
			},
		)
	}

	return app.OK(line,
		output.WithSummary(summary),
		output.WithEntity("chat_line"),
		output.WithBreadcrumbs(breadcrumbs...),
	)
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
		return output.ErrUsage("Invalid chat ID")
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
				Cmd:         fmt.Sprintf("basecamp chat messages --chat %s --in %s", chatID, resolvedProjectID),
				Description: "View messages",
			},
		)
	} else {
		breadcrumbs = append(breadcrumbs,
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         fmt.Sprintf("basecamp chat messages --chat %s", chatID),
				Description: "View messages",
			},
		)
	}

	return app.OK(line,
		output.WithSummary(fmt.Sprintf("Uploaded %s (#%d)", filename, line.ID)),
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
				return output.ErrUsage("Invalid chat ID")
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
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("basecamp chat delete %s --chat %s --in %s", lineID, effectiveChatID, resolvedProjectID),
						Description: "Delete line",
					},
					output.Breadcrumb{
						Action:      "messages",
						Cmd:         fmt.Sprintf("basecamp chat messages --chat %s --in %s", effectiveChatID, resolvedProjectID),
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
				return output.ErrUsage("Invalid chat ID")
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
						Cmd:         fmt.Sprintf("basecamp chat messages --chat %s --in %s", effectiveChatID, resolvedProjectID),
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
	return getDockToolID(cmd.Context(), app, projectID, "chat", "", "chat", "chat")
}
