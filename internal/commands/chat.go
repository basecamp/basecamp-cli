package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
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
		Annotations: map[string]string{"agent_notes": "Projects may have multiple chats — use --room to target a specific one\nContent is sent as plain text by default; use --content-type text/html for rich text. HTML from end to end (opens and closes with a block tag such as <p>) is refused without a --content-type: pass text/html, or text/plain to send it as typed\nChat is project-scoped, no cross-project chat queries\n@mentions: prefer [@Name](mention:SGID) for zero API calls, or [@Name](person:ID) for one lookup; @Name/@First.Last for fuzzy matching (auto-promotes to text/html)\nUse --content-type text/plain to bypass mention resolution"},
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
		newChatLineUpdateCmd(&project, &chatID, &contentType),
		newChatLineDeleteCmd(&project, &chatID),
	)

	return cmd
}

func newChatListCmd(project, chatID *string) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List chats",
		Long: `List chats in a project or account-wide with --all.

These are project chats. Pings, private conversations with people, are not
chats of a project and are not listed here: use 'basecamp pings list'.`,
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
			output.WithNotice("These are project chats. Pings are not listed here; see: basecamp pings list"),
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

	return showChatMessages(cmd, app, chatIDInt, limit, chatRoomNav(chatID, resolvedProjectID))
}

// chatNav names the commands that continue from a Campfire, so the commands
// that read and write one can send the reader on in their own vocabulary: a
// project chat continues through `chat`, a Ping through `pings`.
type chatNav struct {
	messages func(limit int) string
	post     func(text string) string
	// unresolved words the notice for @mentions left as text; nil uses the
	// project chat's.
	unresolved func(names []string) string
}

func (n chatNav) unresolvedMentions(names []string) string {
	if n.unresolved != nil {
		return n.unresolved(names)
	}
	return unresolvedMentionWarning(names)
}

// chatRoomNav continues a project chat with `chat --room`, adding --in when a
// project is known.
func chatRoomNav(roomID, projectID string) chatNav {
	in := ""
	if projectID != "" {
		in = " --in " + projectID
	}
	return chatNav{
		messages: func(limit int) string {
			if limit > 0 {
				return fmt.Sprintf("basecamp chat messages --limit %d --room %s%s", limit, roomID, in)
			}
			return fmt.Sprintf("basecamp chat messages --room %s%s", roomID, in)
		},
		post: func(text string) string {
			return fmt.Sprintf("basecamp chat post %q --room %s%s", text, roomID, in)
		},
	}
}

// showChatMessages prints the most recent lines of a Campfire, oldest first.
func showChatMessages(cmd *cobra.Command, app *appctx.App, roomID int64, limit int, nav chatNav) error {
	if limit < 0 {
		return output.ErrUsage("--limit must not be negative")
	}

	// Get recent messages (lines) using SDK, newest first
	result, err := app.Account().Campfires().ListLines(cmd.Context(), roomID, &basecamp.CampfireLineListOptions{
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
				Cmd:         nav.post("message"),
				Description: "Post message",
			},
			output.Breadcrumb{
				Action:      "more",
				Cmd:         nav.messages(50),
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
for rich text (HTML) messages. A message that is HTML from end to end, opening
and closing with a block tag such as <p>, <ul> or <div>, is refused without a
--content-type rather than posted with its tags showing: pass text/html to send
it as rich text, or text/plain to send it exactly as typed.

@mentions (@Name or @First.Last) are resolved automatically and the
content type is promoted to text/html when mentions are present.

Use - as the message argument to read the message from stdin:
  printf 'Build is green' | basecamp chat post - --in my-project`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// An explicitly supplied --room must be numeric; when it is absent
			// the room is resolved from the project, which needs the network.
			// Check the supplied case before the pipe is drained.
			checkRoom := func() error {
				if *chatID != "" {
					if _, err := strconv.ParseInt(*chatID, 10, 64); err != nil {
						return output.ErrUsage("Invalid chat room ID")
					}
				}
				return nil
			}

			messageContent, err := readPostInput(cmd, args, 0, content, *contentType, attachFiles, checkRoom)
			if err != nil || (messageContent == "" && len(attachFiles) == 0) {
				return err
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			return runChatPost(cmd, app, *chatID, *project, messageContent, *contentType, attachFiles)
		},
	}

	cmd.Flags().StringVar(&content, "content", "", "Message content; use - to read from stdin")
	cmd.Flags().StringVar(contentType, "content-type", "", "Content type (text/html for rich text)")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")

	allowDash(cmd, "arg:0", "flag:content")

	return cmd
}

// readPostInput validates and reads the message for a chat post. msgArgs is
// the command's positional message, at argIndex among its arguments. Everything
// decidable from the arguments is checked before stdin is read, so a bad
// invocation does not cost the caller a drained pipe; checkRoom is the
// command's own check of an explicit room, run at the same point.
//
// A positional and --content are the same input, so supplying both is rejected
// rather than one silently winning: with "-" in play, the losing source would
// discard piped content unread.
//
// A missing message shows help in a terminal, which is not an error: the result
// is then empty with a nil error, and the caller is done unless it has
// attachments, which post without text.
func readPostInput(cmd *cobra.Command, msgArgs []string, argIndex int, content, contentType string, attachFiles []string, checkRoom func() error) (string, error) {
	messageContent := content
	index, what := -1, "--content"
	if len(msgArgs) > 0 {
		if cmd.Flags().Changed("content") {
			return "", output.ErrUsage("cannot combine a <message> argument with --content")
		}
		messageContent = msgArgs[0]
		index, what = argIndex, "<message>"
	}

	if err := checkRoom(); err != nil {
		return "", err
	}
	switch contentType {
	case "", "text/html", "text/plain":
	default:
		return "", output.ErrUsage(fmt.Sprintf("unsupported --content-type %q (expected text/html or text/plain)", contentType))
	}

	// Attachment paths are readable or not regardless of the body, so
	// check them before the pipe is drained.
	if err := validateAttachPaths(attachFiles); err != nil {
		return "", err
	}

	messageContent, err := resolveContentValue(cmd, messageContent, index, what)
	if err != nil {
		return "", err
	}

	// Show help when invoked with neither a message nor attachments
	if strings.TrimSpace(messageContent) == "" {
		if len(attachFiles) > 0 {
			return "", nil
		}
		return "", missingArg(cmd, "<message>")
	}
	return messageContent, nil
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

	mentions := func(ctx context.Context, html string) (richtext.MentionResult, error) {
		return resolveMentions(ctx, app.Names, mentionScope(app, resolvedProjectID, project, app.Flags.Project), html)
	}
	return postToChat(cmd, app, chatIDInt, content, contentType, attachFiles, mentions, chatRoomNav(chatID, resolvedProjectID))
}

// mentionResolver turns the mention syntax in HTML into mention attachments.
type mentionResolver func(ctx context.Context, html string) (richtext.MentionResult, error)

// postToChat posts text and attachments to a Campfire.
func postToChat(cmd *cobra.Command, app *appctx.App, chatIDInt int64, content, contentType string, attachFiles []string, resolve mentionResolver, nav chatNav) error {
	// Resolve @mentions — skip if user explicitly set a non-HTML content type.
	// When contentType is unset, convert Markdown to HTML first so the mention
	// resolver operates on HTML input.
	var mentionNotice string
	if contentType == "" || contentType == "text/html" {
		mentionInput := content
		if contentType == "" {
			mentionInput = richtext.MarkdownToHTML(content)
		}
		result, resolveErr := resolve(cmd.Context(), mentionInput)
		if resolveErr != nil {
			return resolveErr
		}
		if result.HTML != mentionInput || len(result.Unresolved) > 0 {
			content = result.HTML
			if contentType == "" {
				contentType = "text/html"
			}
		}
		mentionNotice = nav.unresolvedMentions(result.Unresolved)
	}

	// HTML sent as plain text shows its tags, and whether markup was meant
	// can't be read from the text ("<p> pairs with </p>"), so ask, before
	// anything is posted, rather than guess.
	if contentType == "" && richtext.IsHTMLDocument(content) {
		return output.ErrUsageHint("this message is HTML, which plain text would show as tags",
			"Send it as rich text with --content-type text/html, or exactly as typed with --content-type text/plain")
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

	breadcrumbs := []output.Breadcrumb{
		{Action: "messages", Cmd: nav.messages(0), Description: "View messages"},
		{Action: "post", Cmd: nav.post("reply"), Description: "Post another"},
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

	return uploadToChat(cmd, app, chatIDInt, filePath, chatRoomNav(chatID, resolvedProjectID))
}

// uploadToChat uploads a validated local file to a Campfire as a chat line.
func uploadToChat(cmd *cobra.Command, app *appctx.App, chatIDInt int64, filePath string, nav chatNav) error {
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
		output.WithBreadcrumbs(output.Breadcrumb{
			Action:      "messages",
			Cmd:         nav.messages(0),
			Description: "View messages",
		}),
	)
}

func newChatLineShowCmd(project, chatID *string) *cobra.Command {
	var cf *commentFlags

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

			// Extract ID, project and room from URL if provided
			lineID, urlProjectID, urlChatID, urlIsPing, err := chatLineRef(cmd.Context(), app, args[0])
			if err != nil {
				return err
			}

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if urlProjectID != "" && (projectID == "" || urlIsPing) {
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

			// The URL's room wins over --room, which may be stale from a
			// previous command; the project's default room is the last resort,
			// and fails when the project has more than one.
			effectiveChatID := urlChatID
			if effectiveChatID == "" {
				effectiveChatID = *chatID
			}
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

			enrichment := fetchCommentsForRecording(cmd.Context(), app, lineID, cf)
			data, commentOpts := enrichment.apply(line, "")
			summary := fmt.Sprintf("Line #%s by %s", lineID, creatorName)

			opts := make([]output.ResponseOption, 0, 4+len(commentOpts))
			opts = append(opts,
				output.WithSummary(summary),
				output.WithEntity("chat_line"),
				output.WithDisplayData(chatLineDisplayData(line)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         deleteLineCmd(cmd, lineID, effectiveChatID, resolvedProjectID),
						Description: "Delete line (permanent)",
					},
					output.Breadcrumb{
						Action:      "messages",
						Cmd:         fmt.Sprintf("basecamp chat messages --room %s --in %s", effectiveChatID, resolvedProjectID),
						Description: "Back to messages",
					},
				),
			)
			opts = append(opts, commentOpts...)

			return app.OK(data, opts...)
		},
	}

	cf = addCommentFlags(cmd, false)

	return cmd
}

// deleteLineCmd builds the delete breadcrumb for whoever will read it.
// Breadcrumbs are not machine-only — they render in styled and Markdown output
// too — so the flag depends on the audience:
//
//   - Machine output: no confirmation is shown in that mode, so the bare
//     command could only fail. Emit --force.
//   - Human-facing output: they will be asked to confirm. Emit the bare command,
//     because handing over a pre-forced one quietly spends the affirmation this
//     change exists to require.
//
// The audience is the *rendered format* of this envelope — the only property of
// the current invocation that survives into the one the reader will type. Three
// tempting predicates are all wrong here, each for the same reason:
//
//   - stdin: `chat line 111 </dev/null` on a terminal is a human reading styled
//     output, and the redirection is over by the time they paste.
//   - BASECAMP_NONINTERACTIVE: a one-shot assignment ends with the command and
//     does not change what the output looks like.
//   - isMachineOutput: true whenever stdout is not a TTY, so
//     `chat line 111 --md > guide.md` — a document written for a person — is
//     classified as machine output.
//
// EffectiveFormat resolves the auto case the same way the renderer does, so a
// plain redirect (auto → JSON) is machine, while an explicitly human format
// stays human wherever it is written.
//
// When the guess is wrong the reader gets a usage error naming --force, which is
// recoverable. The reverse mistake is a permanent delete with no confirmation,
// which is not — so this errs toward omitting the flag.
func deleteLineCmd(cmd *cobra.Command, lineID, chatID, projectID string) string {
	c := fmt.Sprintf("basecamp chat delete %s --room %s --in %s", lineID, chatID, projectID)
	if machineReadsThisOutput(cmd) {
		return c + " --force"
	}
	// Human-facing, so no --force. But if this invocation is only human-facing
	// because a flag overrode a machine-output config, that flag has to travel
	// with the suggestion: pasting it starts a fresh process which re-reads the
	// config, renders json/quiet again, and refuses the very command we just
	// handed over for lacking --force. Carrying the flag keeps it both runnable
	// and confirmable, which adding --force would not.
	return c + humanFormatOverride(appctx.FromContext(cmd.Context()))
}

// humanFormatOverride returns the flag that is holding this invocation in a
// human format against a configured machine one, or "" when the configuration
// is not fighting it.
func humanFormatOverride(app *appctx.App) string {
	if app == nil || app.Config == nil {
		return ""
	}
	switch app.Config.Format {
	case "json", "quiet": // a fresh process would render machine output
	default:
		return ""
	}
	switch {
	case app.Flags.Styled:
		return " --styled"
	case app.Flags.MD:
		return " --md"
	default:
		return ""
	}
}

func newChatLineUpdateCmd(project, chatID, contentType *string) *cobra.Command {
	var content string

	cmd := &cobra.Command{
		Use:   "update <id|url> [content]",
		Short: "Update an existing message",
		Long: `Update the content of an existing chat message.

You can pass either a line ID or a Basecamp line URL:
  basecamp chat update 789 "edited message" --in my-project
  basecamp chat update https://3.basecamp.com/123/buckets/456/chats/789/lines/111 --content "edited"

Chat edits are always stored as rich text. By default, content is treated as
Markdown and converted to HTML, and @mentions resolve like 'chat post'. Use
--content-type text/html to supply HTML directly, or --content-type text/plain
to send the text verbatim (escaped, line breaks preserved, no mention
resolution). --content-type is applied locally — the server always coerces the
edit to rich text.`,
		// Accept 0–2 args so a missing <id|url> yields a purpose-specific usage
		// error from RunE, rather than Cobra's generic "requires at least" (which
		// root.go rewrites into a misleading "Todo ID(s) required"). Mirrors
		// chat post's MaximumNArgs handling.
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if len(args) == 0 {
				return missingArg(cmd, "<id|url>")
			}

			// A positional and --content are the same input, so supplying
			// both is rejected rather than one silently winning — with "-"
			// in play, the losing source would discard piped content unread.
			messageContent := content
			argIndex, what := -1, "--content"
			if len(args) > 1 {
				if cmd.Flags().Changed("content") {
					return output.ErrUsage("cannot combine a [content] argument with --content")
				}
				messageContent = args[1]
				argIndex, what = 1, "[content]"
			}

			// Validate the content mode before reading stdin, not just before
			// the request: an unknown --content-type dooms the invocation, and
			// draining the pipe first makes the caller wait on a producer whose
			// output is already discarded — or lets a blank pipe answer "stdin
			// is empty" instead of naming the bad flag.
			ct := *contentType
			switch ct {
			case "", "text/html", "text/plain":
			default:
				return output.ErrUsage(fmt.Sprintf("unsupported --content-type %q (expected text/html or text/plain)", ct))
			}

			// Resolve the line reference before the read: the URL host and
			// shape, an explicitly supplied --room, and a bare numeric line ID
			// are all decidable from the arguments and the configured base URL,
			// so a bad reference must not cost the caller a drained pipe.
			lineID := args[0]
			urlChatID := ""
			urlProjectID := ""
			var parsedURL *urlarg.Parsed
			if urlarg.IsURL(args[0]) {
				if !hostutil.IsTrustedBasecampHost(args[0], app.Config.BaseURL) {
					return output.ErrUsage("refusing untrusted host in URL — expected a Basecamp URL")
				}
				parsed := urlarg.Parse(args[0])
				// Require an individual chat-line URL. A room's line-collection URL
				// (/chats/{c}/lines) also parses as Type "lines" but with
				// IsCollection set and RecordingID holding the campfire ID, not a
				// line ID — accepting it would edit PUT /chats/{c}/lines/{c}.
				if parsed == nil || parsed.Type != "lines" || parsed.IsCollection {
					return output.ErrUsage("expected a chat-line ID or URL of the form /chats/{c}/lines/{l} or /chats/{c}@{l}")
				}
				lineID = parsed.RecordingID
				urlChatID = parsed.CampfireID
				urlProjectID = parsed.ProjectID
				parsedURL = parsed
			}
			if _, err := strconv.ParseInt(lineID, 10, 64); err != nil {
				return output.ErrUsage("Invalid chat line ID")
			}
			if *chatID != "" {
				if _, err := strconv.ParseInt(*chatID, 10, 64); err != nil {
					return output.ErrUsage("Invalid chat room ID")
				}
			}

			var contentErr error
			messageContent, contentErr = resolveContentValue(cmd, messageContent, argIndex, what)
			if contentErr != nil {
				return contentErr
			}

			if strings.TrimSpace(messageContent) == "" {
				return missingArg(cmd, "<content>")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// The URL's host and shape were settled before the read; only the
			// account comparison had to wait for a resolved account. Editing in
			// the wrong account is worth stopping for rather than silently
			// targeting a different one.
			if parsedURL != nil && parsedURL.AccountID != "" && app.Config.AccountID != "" &&
				parsedURL.AccountID != app.Config.AccountID {
				return output.ErrUsage(fmt.Sprintf("URL account %s does not match the configured account %s", parsedURL.AccountID, app.Config.AccountID))
			}

			var ping *basecamp.Ping
			if parsedURL != nil && parsedURL.PingID != "" {
				var err error
				if ping, urlChatID, err = pingURLCampfire(cmd.Context(), app, parsedURL.PingID); err != nil {
					return err
				}
			}

			// Resolve the chat (campfire) ID, and a project only when needed. The
			// PUT needs just the chat + line IDs; a project is required only to
			// discover the default room (no URL chat and no --room) and is
			// otherwise resolved opportunistically for breadcrumbs — never by
			// prompting. URL-derived values win over --room/--in/--project, which
			// may be stale from a previous command in the same shell.
			effectiveChatID := urlChatID
			if effectiveChatID == "" {
				effectiveChatID = *chatID
			}

			projectHint := urlProjectID
			if projectHint == "" {
				projectHint = *project
			}
			if projectHint == "" {
				projectHint = app.Flags.Project
			}
			if projectHint == "" {
				projectHint = app.Config.ProjectID
			}

			var resolvedProjectID string
			var err error
			if effectiveChatID == "" {
				// No room in hand — resolve a project (prompting as a last resort)
				// so we can discover the project's default chat.
				if projectHint == "" {
					if err = ensureProject(cmd, app); err != nil {
						return err
					}
					projectHint = app.Config.ProjectID
				}
				resolvedProjectID, _, err = app.Names.ResolveProject(cmd.Context(), projectHint)
				if err != nil {
					return err
				}
				effectiveChatID, err = getChatID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}
			// When the room is already known (--room or a URL), the project is
			// needed only for breadcrumbs — resolved best-effort *after* the edit
			// (below) so a stale/unreachable default project can't fail the PUT.

			chatIDInt, err := strconv.ParseInt(effectiveChatID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid chat room ID")
			}
			lineIDInt, err := strconv.ParseInt(lineID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid line ID")
			}

			// Resolve the content into the exact rich-text body we PUT. Chat edits
			// always render as rich text server-side, so:
			//   - unset (default): Markdown → HTML unconditionally, then resolve
			//     @mentions (mirrors messages.go, which converts unconditionally);
			//   - text/html: use the supplied HTML as-is, then resolve @mentions;
			//   - text/plain: serialize literally (escape + preserve breaks) and
			//     skip mention resolution.
			var mentionNotice string
			switch ct {
			case "text/plain":
				messageContent = richtext.PlainToHTML(messageContent)
			default:
				if ct == "" {
					messageContent = richtext.MarkdownToHTML(messageContent)
				}
				resolve := func(ctx context.Context, html string) (richtext.MentionResult, error) {
					return resolveMentions(ctx, app.Names, mentionScope(app, urlProjectID, resolvedProjectID, *project, app.Flags.Project), html)
				}
				notice := unresolvedMentionWarning
				if ping != nil {
					// A Ping is no project, and only its people can read the line.
					resolve, notice = pingMentionResolver(ping), pingNav(ping.ID).unresolvedMentions
				}
				result, resolveErr := resolve(cmd.Context(), messageContent)
				if resolveErr != nil {
					return resolveErr
				}
				messageContent = result.HTML
				mentionNotice = notice(result.Unresolved)
			}

			if err := app.Account().Campfires().UpdateLine(cmd.Context(), chatIDInt, lineIDInt, messageContent); err != nil {
				return convertSDKError(err)
			}

			// SDK PUT returns 204; re-fetch so the response carries the canonical
			// post-update line. A failure here doesn't roll back the update — we
			// surface it as a diagnostic rather than the command's exit code.
			line, fetchErr := app.Account().Campfires().GetLine(cmd.Context(), chatIDInt, lineIDInt)

			// Resolve the project for breadcrumbs only, best-effort: the edit has
			// already been sent using just the chat + line IDs, so a stale or
			// unreachable default project must not fail the command — it only
			// drops --in from the breadcrumbs.
			if resolvedProjectID == "" && projectHint != "" {
				if rp, _, perr := app.Names.ResolveProject(cmd.Context(), projectHint); perr == nil {
					resolvedProjectID = rp
				}
			}

			// Include --in only when a project was resolved (mirrors post/upload,
			// which omit it when editing via --room without a project).
			showCmd := fmt.Sprintf("basecamp chat line %s --room %s", lineID, effectiveChatID)
			messagesCmd := fmt.Sprintf("basecamp chat messages --room %s", effectiveChatID)
			if resolvedProjectID != "" {
				showCmd = fmt.Sprintf("basecamp chat line %s --room %s --in %s", lineID, effectiveChatID, resolvedProjectID)
				messagesCmd = fmt.Sprintf("basecamp chat messages --room %s --in %s", effectiveChatID, resolvedProjectID)
			}

			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("Updated line #%s", lineID)),
				output.WithEntity("chat_line"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         showCmd,
						Description: "View line",
					},
					output.Breadcrumb{
						Action:      "messages",
						Cmd:         messagesCmd,
						Description: "Back to messages",
					},
				),
			}
			if line != nil {
				respOpts = append(respOpts, output.WithDisplayData(chatLineDisplayData(line)))
			}
			if mentionNotice != "" {
				respOpts = append(respOpts, output.WithDiagnostic(mentionNotice))
			}
			if fetchErr != nil {
				respOpts = append(respOpts, output.WithDiagnostic(fmt.Sprintf("update succeeded; refetch failed: %v", fetchErr)))
			}

			if line == nil {
				return app.OK(map[string]any{"updated": true, "id": lineID}, respOpts...)
			}
			return app.OK(line, respOpts...)
		},
	}

	cmd.Flags().StringVar(&content, "content", "", "New message content; use - to read from stdin")
	cmd.Flags().StringVar(contentType, "content-type", "", "Input handling: text/html (supply HTML) or text/plain (verbatim); applied locally, edits always render as rich text")

	allowDash(cmd, "arg:1", "flag:content")

	return cmd
}

func newChatLineDeleteCmd(project, chatID *string) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <id|url>",
		Short: "Delete a message",
		Long: `Delete a message line from a chat.

This permanently deletes the message — it is not moved to trash.

Deleting asks for confirmation. Where nothing can answer that, --force is
required instead, so a permanent delete always carries an explicit statement
of intent. That covers any machine-output mode (--json, --agent, --quiet),
BASECAMP_NONINTERACTIVE, and a confirmation that could not be shown or
answered — which needs a terminal on both stdin and stderr, since the prompt
is drawn to stderr.

You can pass either a line ID or a Basecamp line URL:
  basecamp chat delete 789 --in my-project
  basecamp chat delete 789 --in my-project --json --force
  basecamp chat delete https://3.basecamp.com/123/buckets/456/chats/789/lines/111`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// A chat line delete is permanent — the API does not trash it — so
			// it happens only with --force or a confirmation somebody can
			// answer. Refuse before any account, project or chat lookup: an
			// invocation that cannot proceed should not spend two round trips
			// discovering that.
			if err := ensureDeleteConfirmable(cmd, force); err != nil {
				return err
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID, project and room from URL if provided
			lineID, urlProjectID, urlChatID, urlIsPing, err := chatLineRef(cmd.Context(), app, args[0])
			if err != nil {
				return err
			}

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if urlProjectID != "" && (projectID == "" || urlIsPing) {
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

			// The URL's room wins over --room, which may be stale from a
			// previous command; the project's default room is the last resort,
			// and fails when the project has more than one.
			effectiveChatID := urlChatID
			if effectiveChatID == "" {
				effectiveChatID = *chatID
			}
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

			// Confirm unless forced. ensureDeleteConfirmable established that
			// without --force a prompt will be shown and can be answered — one
			// predicate for both, so the guard cannot accept an invocation that
			// this block then skips, which would delete unconfirmed.
			if !force {
				confirmed, err := tui.ConfirmDangerous("Permanently delete this chat line?")
				switch {
				case errors.Is(err, tui.ErrCanceled):
					return nil // the user answered, and the answer was no
				case err != nil:
					// Not a cancellation — a timeout or a bubbletea failure.
					// Nothing is deleted either way, but say what happened
					// rather than reporting a decision nobody made.
					return fmt.Errorf("confirming the delete: %w", err)
				case !confirmed:
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

// chatLineRef extracts the line, project and room IDs from a chat line
// argument. A chat-line URL names the room that owns the line; a bare ID names
// none, leaving the room to --room or the project's default.
//
// A URL is held to what chat update holds it to: a trusted Basecamp host, an
// individual line (a room's /chats/{c}/lines parses with the room's ID where
// the line's belongs, and would act on line {c}), and the configured account.
//
// A Ping line URL names the Ping, not a room: its Campfire is looked up here
// and returned as the room, with isPing set so the caller keeps the Ping as the
// bucket whatever --in says.
func chatLineRef(ctx context.Context, app *appctx.App, arg string) (lineID, projectID, chatID string, isPing bool, err error) {
	if !urlarg.IsURL(arg) {
		return arg, "", "", false, nil
	}
	if !hostutil.IsTrustedBasecampHost(arg, app.Config.BaseURL) {
		return "", "", "", false, output.ErrUsage("refusing untrusted host in URL — expected a Basecamp URL")
	}
	parsed := urlarg.Parse(arg)
	if parsed != nil && parsed.PingID != "" && parsed.Type != "lines" {
		return "", "", "", false, output.ErrUsageHint("a ping URL names a ping, not a message",
			fmt.Sprintf("Show the ping with: basecamp pings show %s; a message URL ends in @<line>", parsed.PingID))
	}
	if parsed == nil || parsed.Type != "lines" || parsed.IsCollection {
		return "", "", "", false, output.ErrUsage("expected a chat-line ID or URL of the form /chats/{c}/lines/{l} or /chats/{c}@{l}")
	}
	if parsed.AccountID != "" && app.Config.AccountID != "" && parsed.AccountID != app.Config.AccountID {
		return "", "", "", false, output.ErrUsage(fmt.Sprintf("URL account %s does not match the configured account %s", parsed.AccountID, app.Config.AccountID))
	}
	if parsed.PingID != "" {
		_, chatID, err = pingURLCampfire(ctx, app, parsed.PingID)
		if err != nil {
			return "", "", "", false, err
		}
		return parsed.RecordingID, parsed.ProjectID, chatID, true, nil
	}
	return parsed.RecordingID, parsed.ProjectID, parsed.CampfireID, false, nil
}

// pingURLCampfire finds the Ping a chat-line URL names, and its Campfire. The
// URL carries the Ping and the line but not the Campfire, so the room the URL
// names has to be asked of the account, as a /chats/{c}@{l} URL names its own.
func pingURLCampfire(ctx context.Context, app *appctx.App, pingID string) (*basecamp.Ping, string, error) {
	id, err := strconv.ParseInt(pingID, 10, 64)
	if err != nil {
		return nil, "", output.ErrUsage("Invalid ping ID")
	}
	ping, campfireID, err := resolvePingCampfire(ctx, app, pingTarget{id: id}, pingLookupOnly)
	if err != nil {
		return nil, "", err
	}
	return ping, strconv.FormatInt(campfireID, 10), nil
}

// getChatID retrieves the chat ID from a project's dock, handling multi-dock projects.
func getChatID(cmd *cobra.Command, app *appctx.App, projectID string) (string, error) {
	id, err := getDockToolID(cmd.Context(), app, projectID, "chat", "", "chat room", "room")
	if err != nil {
		return pingCampfireID(cmd.Context(), app, projectID, err)
	}
	return id, nil
}

// pingCampfireID finds the Campfire of a Ping named where a project was
// expected. A Ping is a bucket without a dock, so a project lookup that failed
// as not found is retried as a Ping. The project's error stands when the Ping
// is not found either; a Ping lookup that failed any other way (Pings turned
// off, a rate limit, the network) is reported instead, since it left the answer
// unknown.
func pingCampfireID(ctx context.Context, app *appctx.App, bucketID string, lookupErr error) (string, error) {
	var e *output.Error
	if !errors.As(lookupErr, &e) || e.Code != output.CodeNotFound {
		return "", lookupErr
	}
	id, err := strconv.ParseInt(bucketID, 10, 64)
	if err != nil {
		return "", lookupErr
	}
	_, campfireID, err := resolvePingCampfire(ctx, app, pingTarget{id: id}, pingLookupOnly)
	if err != nil {
		if errors.As(err, &e) && e.Code == output.CodeNotFound {
			return "", lookupErr
		}
		return "", err
	}
	return strconv.FormatInt(campfireID, 10), nil
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
