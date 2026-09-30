package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
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
	var allProjects bool
	var sortField string
	var reverse bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List messages",
		Long: `List all messages in a project's message board.

With no project in scope, lists every message across all accessible projects,
newest first. --all-projects asks for that listing explicitly, overriding a
configured project.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMessagesList(cmd, *project, *messageBoard, limit, page, all, allProjects, sortField, reverse)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of messages to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all messages (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")
	cmd.Flags().BoolVar(&allProjects, "all-projects", false, "List messages across every accessible project")
	cmd.Flags().StringVar(&sortField, "sort", "", "Sort by field (title, created, updated)")
	cmd.Flags().BoolVar(&reverse, "reverse", false, "Reverse sort order")

	return cmd
}

func runMessagesList(cmd *cobra.Command, project string, messageBoard string, limit, page int, all, allProjects bool, sortField string, reverse bool) error {
	app := appctx.FromContext(cmd.Context())

	// Flag combinations that mean the same thing in either scope. The page
	// rules are scope-specific and live on the branches below: a project's
	// message board only serves page 1, while the account-wide feed serves any
	// positive page.
	if all && limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if page > 0 && (all || limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
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

	// Select the scope before validating against it. An explicit project wins,
	// then a configured one; with neither, the account-wide feed answers the
	// same question across every project rather than prompting for one.
	// --all-projects pins that intent, so it overrides a configured project and
	// conflicts with an explicit one.
	explicitProject := project
	if explicitProject == "" {
		explicitProject = app.Flags.Project
	}
	switch {
	case allProjects && explicitProject != "":
		return output.ErrUsageHint("--all-projects conflicts with --project/--in",
			"drop one: --all-projects lists every project, --project lists one")
	case allProjects, !projectKnown(app, project):
		return runMessagesListAccountWide(cmd, app, messageBoard, limit, page, all, sortField, reverse)
	}

	projectID := explicitProject
	if projectID == "" {
		projectID = app.Config.ProjectID
	}

	if page > 1 {
		return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
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
		{Action: "post", Cmd: fmt.Sprintf("basecamp messages create <title> --in %s", resolvedProjectID), Description: "Post new message"},
		{Action: "archived", Cmd: fmt.Sprintf("basecamp recordings messages --status archived --in %s", resolvedProjectID), Description: "Browse archived messages"},
	}
}

// runMessagesListAccountWide lists every message across all accessible
// projects. The feed is flat []Recording, and each item carries its own
// bucket — which the generic renderers skip by name, so human-facing output
// reads flattened rows that keep the project while machine formats get the
// raw payload.
func runMessagesListAccountWide(cmd *cobra.Command, app *appctx.App, messageBoard string, limit, page int, all bool, sortField string, reverse bool) error {
	if err := rejectAccountWideTodolist(app, "message"); err != nil {
		return err
	}
	// --message-board names one project's board, so it cannot narrow a listing
	// that spans every project. It reaches here from the group's persistent
	// flag, whichever side of the subcommand it was written on.
	if messageBoard != "" {
		return output.ErrUsageHint("--message-board applies to a single project's message board",
			"drop --message-board, or pass --project/--in to list that board")
	}
	if reverse && sortField == "" {
		return output.ErrUsage("--reverse requires --sort")
	}
	if limit < 0 {
		return output.ErrUsage("--limit must be a positive number of messages")
	}
	if page < 0 || (page == 0 && cmd.Flags().Changed("page")) {
		return output.ErrUsageHint("--page must be a positive page number",
			"use --all to follow every page")
	}

	// bounded reports that this invocation applies a cap at all; capped
	// reports that the walk actually stopped short of the listing. They are
	// different questions — --all is unbounded, and a bounded walk that
	// reaches the end of a short listing is not truncated.
	bounded := !all && page == 0
	wanted := limit
	if wanted == 0 {
		wanted = accountWideDefaultLimit
	}

	recordings, capped, meta, err := messagesAccountWideFetch(cmd.Context(), app, wanted, page, all, sortField != "")
	if err != nil {
		return err
	}

	// Sort before truncating: capping first would sort only the window that
	// happened to survive.
	if sortField != "" {
		sortMessagesAccountWide(recordings, sortField, reverse)
	}
	if bounded && len(recordings) > wanted {
		// A sorted listing arrives whole and is capped here, so the trim is
		// itself the truncation the notice has to report.
		capped = true
		recordings = recordings[:wanted]
	}

	respOpts := accountWideRespOpts(len(recordings), "message", "messages", meta, limit > 0)
	if notice := accountWideCapNotice(capped, meta, len(recordings), "messages"); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}
	respOpts = append(respOpts, output.WithDisplayData(flattenAccountWideRecordings(recordings)))
	respOpts = append(respOpts, output.WithBreadcrumbs(messagesAccountWideBreadcrumbs()...))

	return app.OK(recordings, respOpts...)
}

// messagesAccountWideFetch collects the account-wide feed, which takes a single
// page number: N returns exactly page N, and 0 follows the Link header across
// every page.
//
// A capped listing collects one page at a time and stops once it has enough,
// rather than crawling the whole account and discarding most of it. A sorted
// listing cannot do that — the cap applies after the sort, so every page has to
// be in hand first.
func messagesAccountWideFetch(ctx context.Context, app *appctx.App, wanted, page int, all, sorted bool) ([]basecamp.Recording, bool, basecamp.ListMeta, error) {
	everything := app.Account().Everything()

	fetch := func(p int32) ([]basecamp.Recording, basecamp.ListMeta, error) {
		result, err := everything.Messages(ctx, p)
		if err != nil {
			return nil, basecamp.ListMeta{}, convertSDKError(err)
		}
		return result.Recordings, result.Meta, nil
	}

	// An explicit page or --all is exactly what was asked for, and a sorted
	// listing needs every page before the cap can apply (I4). Neither is a
	// bounded walk, so neither can report one stopping short.
	if page > 0 || all {
		sdkPage, err := accountWidePage(page, all)
		if err != nil {
			return nil, false, basecamp.ListMeta{}, err
		}
		recordings, meta, err := fetch(sdkPage)
		return recordings, false, meta, err
	}
	if sorted {
		recordings, meta, err := fetch(0)
		return recordings, false, meta, err
	}

	recordings, capped, meta, err := accountWideCollect(fetch, accountWideFlatCount[basecamp.Recording], wanted)
	if err != nil {
		return nil, false, basecamp.ListMeta{}, err
	}
	return recordings, capped, meta, nil
}

func messagesAccountWideBreadcrumbs() []output.Breadcrumb {
	return []output.Breadcrumb{
		{Action: "show", Cmd: "basecamp messages show <id>", Description: "Show message details"},
		{Action: "project", Cmd: "basecamp messages list --in <project>", Description: "List one project's message board"},
		{Action: "search", Cmd: "basecamp search <query> --type message", Description: "Search messages"},
	}
}

// messagesAccountWideTitle returns the display title for a message recording.
// The /messages.json feed renders the message partial on top of the base
// recording projection, so Subject is the message's own title; Title is the
// generic fallback.
func messagesAccountWideTitle(r basecamp.Recording) string {
	if r.Subject != nil && *r.Subject != "" {
		return *r.Subject
	}
	return r.Title
}

// sortMessagesAccountWide sorts account-wide message recordings, matching the
// fields and default directions sortMessages applies to project-scoped
// messages.
func sortMessagesAccountWide(recordings []basecamp.Recording, field string, reverse bool) {
	sort.SliceStable(recordings, func(i, j int) bool {
		switch field {
		case "title":
			return strings.ToLower(messagesAccountWideTitle(recordings[i])) < strings.ToLower(messagesAccountWideTitle(recordings[j]))
		case "created":
			return recordings[i].CreatedAt.After(recordings[j].CreatedAt)
		case "updated":
			return recordings[i].UpdatedAt.After(recordings[j].UpdatedAt)
		}
		return false
	})
	if reverse {
		slices.Reverse(recordings)
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
					Cmd:         fmt.Sprintf("basecamp comments create %s <text>", messageIDStr),
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
	var visibleToClients bool
	var category string

	cmd := &cobra.Command{
		Use:   "create <title> [body]",
		Short: "Create a new message",
		Long: `Post a new message to a project's message board.

Use - as the body argument to read the body from stdin:
  printf 'Long **Markdown** body' | basecamp messages create "Title" -

Use --category to file the message under a message type, by ID or name:
  basecamp messages create "Launch" "We shipped" --category Announcement
List a project's message types with: basecamp messagetypes list --in <project>`,
		// Bounded so a stray third token is a usage error rather than being
		// silently dropped after "-" has already drained stdin.
		Args: cobra.MaximumNArgs(2),
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

			// Validate user input first, before checking account. The --edit
			// exclusion runs before "-" resolution so --edit … - errors
			// without consuming stdin.
			if edit && body != "" {
				return output.ErrUsage("cannot combine --edit and body argument")
			}
			if err := rejectSubscribeConflict(cmd.Flags().Changed("subscribe"), noSubscribe, subscribe); err != nil {
				return err
			}
			if err := requireNumericID(*messageBoard, "message board ID"); err != nil {
				return err
			}
			if cmd.Flags().Changed("category") {
				if err := validateMessageCategoryFlag(category); err != nil {
					return err
				}
			}
			// Attachment paths are readable or not regardless of the body, so
			// check them before the pipe is drained.
			if err := validateAttachPaths(attachFiles); err != nil {
				return err
			}

			var err error
			body, err = resolveContentValue(cmd, body, 1, "[body]")
			if err != nil {
				return err
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

			var categoryID int64
			if cmd.Flags().Changed("category") {
				categoryID, err = resolveMessageCategory(cmd.Context(), app, resolvedProjectID, category)
				if err != nil {
					return err
				}
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
				CategoryID:    categoryID,
				Subscriptions: subs,
			}

			// Default to active (published) status unless --draft is specified
			if draft {
				req.Status = "drafted"
			} else {
				req.Status = "active"
			}

			// Set client visibility only when the flag was provided. Omitting it
			// uses the server's default: team-only when posting as a team member,
			// but a client-authenticated caller always creates client-visible
			// records (an explicit false is overridden server-side).
			if cmd.Flags().Changed("visible-to-clients") {
				req.VisibleToClients = &visibleToClients
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
	cmd.Flags().BoolVar(&visibleToClients, "visible-to-clients", false, "Make the message visible to clients on the project (omit for the server default; client-authenticated callers always post client-visible)")
	cmd.Flags().StringVar(&category, "category", "", "Message type (category) ID or name; see 'basecamp messagetypes list'")

	allowDash(cmd, "arg:1")

	return cmd
}

func newMessagesUpdateCmd() *cobra.Command {
	var title string
	var body string
	var category string
	var noCategory bool

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a message",
		Long: `Update an existing message's title, body, or category.

You can pass either a message ID or a Basecamp URL:
  basecamp messages update 789 --title "new title"
  basecamp messages update 789 --body "new body"
  basecamp messages update 789 --category Announcement
  basecamp messages update 789 --no-category

--category takes a message type ID or name, matched against the message's
project. List a project's message types with: basecamp messagetypes list --in <project>`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			categoryChanged := cmd.Flags().Changed("category")
			if categoryChanged && noCategory {
				return output.ErrUsage("--category and --no-category cannot be used together")
			}
			if categoryChanged {
				if err := validateMessageCategoryFlag(category); err != nil {
					return err
				}
			}
			if strings.TrimSpace(title) == "" && strings.TrimSpace(body) == "" && !categoryChanged && !noCategory {
				return noChanges(cmd)
			}

			// Extract ID from URL if provided
			messageIDStr := extractID(args[0])

			messageID, err := strconv.ParseInt(messageIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid message ID")
			}

			// Syntactic checks first, then "-", then account and network: a
			// malformed ID is answered without waiting on the producer, and a
			// blank pipe cannot mask it.
			body, err = resolveContentValue(cmd, body, -1, "--body")
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			var categoryID int64
			if categoryChanged {
				categoryID, err = resolveUpdatedMessageCategory(cmd.Context(), app, messageID, category)
				if err != nil {
					return err
				}
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
				Subject:       title,
				Content:       html,
				CategoryID:    categoryID,
				ClearCategory: noCategory,
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
	cmd.Flags().StringVarP(&body, "body", "b", "", "New body content; use - to read from stdin")
	cmd.Flags().StringVar(&category, "category", "", "Message type (category) ID or name; see 'basecamp messagetypes list'")
	cmd.Flags().BoolVar(&noCategory, "no-category", false, "Remove the message's category")

	allowDash(cmd, "flag:body")

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

// getMessageBoardID retrieves the message board ID from a project's dock, handling multi-dock projects.
func getMessageBoardID(cmd *cobra.Command, app *appctx.App, projectID string, explicitID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "message_board", explicitID, "message board", "message-board")
}

// validateMessageCategoryFlag rejects a --category value that can never name a
// message type, before anything is read from stdin or the network.
func validateMessageCategoryFlag(category string) error {
	value := strings.TrimSpace(category)
	if value == "" {
		return output.ErrUsageHint("--category needs a message type ID or name",
			"List message types with: basecamp messagetypes list --in <project>")
	}
	if id, err := strconv.ParseInt(value, 10, 64); (err == nil && id <= 0) || errors.Is(err, strconv.ErrRange) {
		return output.ErrUsage("--category must be a positive message type ID or a name")
	}
	return nil
}

// resolveUpdatedMessageCategory resolves --category for messages update. A
// name is matched against the message's own project, which is read from the
// message, so an ID alone never costs a lookup.
func resolveUpdatedMessageCategory(ctx context.Context, app *appctx.App, messageID int64, category string) (int64, error) {
	if id, ok := numericMessageCategory(category); ok {
		return id, nil
	}
	message, err := app.Account().Messages().Get(ctx, messageID)
	if err != nil {
		return 0, convertSDKError(err)
	}
	if message.Bucket == nil || message.Bucket.ID == 0 {
		return 0, output.ErrUsageHint("Cannot tell which project this message is in to look up its category by name",
			"Pass the message type ID instead: basecamp messagetypes list --in <project>")
	}
	return resolveMessageCategory(ctx, app, strconv.FormatInt(message.Bucket.ID, 10), category)
}

func numericMessageCategory(category string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(category), 10, 64)
	return id, err == nil && id > 0
}

// resolveMessageCategory turns a --category value into a message type ID. A
// positive integer is taken as the ID as given. Anything else is a name,
// matched against the project's message types: an exact match first, then a
// case-insensitive one. There is no partial matching — a guessed category is a
// silent mislabel on a post everyone on the project can see.
func resolveMessageCategory(ctx context.Context, app *appctx.App, projectID, category string) (int64, error) {
	if id, ok := numericMessageCategory(category); ok {
		return id, nil
	}
	bucketID, err := strconv.ParseInt(projectID, 10, 64)
	if err != nil {
		return 0, output.ErrUsage("Invalid project ID")
	}
	result, err := app.Account().MessageTypes().List(ctx, bucketID, nil)
	if err != nil {
		return 0, convertSDKError(err)
	}

	name := strings.TrimSpace(category)
	var folded []basecamp.MessageType
	for _, t := range result.MessageTypes {
		if t.Name == name {
			return t.ID, nil
		}
		if strings.EqualFold(t.Name, name) {
			folded = append(folded, t)
		}
	}
	switch len(folded) {
	case 1:
		return folded[0].ID, nil
	case 0:
		available := make([]string, 0, len(result.MessageTypes))
		for _, t := range result.MessageTypes {
			available = append(available, fmt.Sprintf("%s (%d)", t.Name, t.ID))
		}
		hint := "This project has no message types."
		if len(available) > 0 {
			hint = "Available: " + strings.Join(available, ", ") + "."
		}
		hint += fmt.Sprintf(" See: basecamp messagetypes list --in %s", projectID)
		return 0, output.ErrNotFoundHint("message type", name, hint)
	default:
		matches := make([]string, len(folded))
		for i, t := range folded {
			matches[i] = fmt.Sprintf("%s (%d)", t.Name, t.ID)
		}
		ambiguous := output.ErrAmbiguous("message type", matches)
		ambiguous.Hint = "Pass the message type ID instead: " + strings.Join(matches, ", ")
		return 0, ambiguous
	}
}
