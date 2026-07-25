package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/editor"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// NewCommentsCmd creates the comments command group (list/show/update).
func NewCommentsCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:         "comments",
		Short:       "List and manage comments",
		Long:        "List, show, and update comments on items.",
		Annotations: map[string]string{"agent_notes": "Comments are flat — reply to parent item, not to other comments\nURL fragments (#__recording_456) are comment IDs — comment on the parent recording_id, not the comment_id\nComments are on items (todos, messages, cards, etc.) — not on other comments\n@mentions: prefer [@Name](mention:SGID) for zero API calls, or [@Name](person:ID) for one lookup; @Name/@First.Last for fuzzy matching"},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	cmd.AddCommand(
		newCommentsListCmd(),
		newCommentsShowCmd(),
		newCommentsThreadCmd(),
		newCommentsCreateCmd(),
		newCommentsUpdateCmd(),
		newRecordableTrashCmd("comment"),
		newRecordableArchiveCmd("comment"),
		newRecordableRestoreCmd("comment"),
	)

	return cmd
}

func newCommentsListCmd() *cobra.Command {
	var limit, page int
	var all bool

	cmd := &cobra.Command{
		Use:   "list <id|url>",
		Short: "List comments on an item",
		Long:  "List all comments on an item.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>")
			}
			return runCommentsList(cmd, args[0], limit, page, all)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of comments to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all comments (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	return cmd
}

func runCommentsList(cmd *cobra.Command, recordingID string, limit, page int, all bool) error {
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

	// Extract recording ID from URL if provided
	recordingID = extractID(recordingID)

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	recID, err := strconv.ParseInt(recordingID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid ID")
	}

	// Build pagination options
	opts := &basecamp.CommentListOptions{}
	if all {
		opts.Limit = -1 // SDK treats -1 as unlimited
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}

	commentsResult, err := app.Account().Comments().List(cmd.Context(), recID, opts)
	if err != nil {
		return convertSDKError(err)
	}
	comments := commentsResult.Comments

	// Build response options
	respOpts := []output.ResponseOption{
		output.WithEntity("comment"),
		output.WithSummary(fmt.Sprintf("%d comments on #%s", len(comments), recordingID)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "add",
				Cmd:         fmt.Sprintf("basecamp comments create %s <text>", recordingID),
				Description: "Add comment",
			},
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "basecamp comments show <id>",
				Description: "Show comment",
			},
		),
	}

	// Add truncation notice if results may be limited
	if notice := output.TruncationNoticeWithTotal(len(comments), commentsResult.Meta.TotalCount); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}

	return app.OK(comments, respOpts...)
}

func newCommentsShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show comment details",
		Long: `Display detailed information about a comment.

You can pass either a comment ID or a Basecamp URL:
  basecamp comments show 789
  basecamp comments show https://3.basecamp.com/123/buckets/456/todos/111#__recording_789`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>")
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract comment ID from URL if provided
			// Uses extractCommentWithProject to prefer CommentID from URL fragments
			commentIDStr, _ := extractCommentWithProject(args[0])

			commentID, err := strconv.ParseInt(commentIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid comment ID")
			}

			comment, err := app.Account().Comments().Get(cmd.Context(), commentID)
			if err != nil {
				return convertSDKError(err)
			}

			creatorName := ""
			if comment.Creator != nil {
				creatorName = comment.Creator.Name
			}

			return app.OK(comment,
				output.WithEntity("comment"),
				output.WithSummary(fmt.Sprintf("Comment #%s by %s", commentIDStr, creatorName)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("basecamp comments update %s <text>", commentIDStr),
						Description: "Update comment",
					},
				),
			)
		},
	}
	return cmd
}

// defaultCommentThreadWindow is the default maximum number of comments returned
// by `comments thread` when neither --all nor --window is given. Focus-centered:
// (N-1)/2 older, the focus, and the remainder newer.
const defaultCommentThreadWindow = 41

func newCommentsThreadCmd() *cobra.Command {
	var all bool
	var window int

	cmd := &cobra.Command{
		Use:   "thread <comment-id|comment-url>",
		Short: "Show a comment with its discussion, ready to reply",
		Long: `Show a comment together with its parent recording and the surrounding
discussion, plus a ready-to-use reply target and author @mention.

The argument must identify a specific comment — a bare comment ID, a comment
fragment URL (…#__recording_789), or a direct comment URL. A plain recording
URL is rejected; use ` + "`basecamp show`" + ` for the recording itself.

By default the discussion is a window of up to 41 comments centered on the
focus. Use --window N to change the size or --all to return every fetched
comment. Selection bounds the output, never the fetch.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCommentsThread(cmd, args[0], all, window)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Return every fetched comment instead of a window")
	cmd.Flags().IntVar(&window, "window", 0, "Maximum comments to return, centered on the focus (default 41)")

	return cmd
}

func runCommentsThread(cmd *cobra.Command, arg string, all bool, window int) error {
	app := appctx.FromContext(cmd.Context())

	windowSet := cmd.Flags().Changed("window")
	if all && windowSet {
		return output.ErrUsage("--all and --window are mutually exclusive")
	}
	if windowSet && window <= 0 {
		return output.ErrUsage("--window must be a positive number")
	}
	effectiveWindow := defaultCommentThreadWindow
	if windowSet {
		effectiveWindow = window
	}

	// Require an exact comment trigger before any API call — a plain recording
	// URL or collection is rejected with a pointer to `basecamp show`.
	triggerIDStr, urlAccountID, err := commentTriggerID(arg)
	if err != nil {
		return err
	}

	// Guard against resolving a same-numbered comment in the wrong account: a
	// URL names its account, and if it disagrees with the configured one the
	// safe move is to stop rather than silently target a different account (and
	// build a reply target that points elsewhere). Rejects before any API call.
	if urlAccountID != "" && app.Config.AccountID != "" && urlAccountID != app.Config.AccountID {
		return output.ErrUsage(fmt.Sprintf("URL account %s does not match the configured account %s", urlAccountID, app.Config.AccountID))
	}

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	triggerID, err := strconv.ParseInt(triggerIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid comment ID")
	}

	// Step 1 — resolve the focus comment.
	trigger, err := app.Account().Comments().Get(cmd.Context(), triggerID)
	if err != nil {
		return convertSDKError(err)
	}

	// Step 2 — reply target + parent safety. A reply routes to the parent
	// recording, never to the comment; without a parent it cannot be built.
	if trigger.Parent == nil || trigger.Parent.ID == 0 {
		return &output.Error{
			Code:    output.CodeAPI,
			Message: fmt.Sprintf("Comment #%d has no parent recording; cannot build a reply target", triggerID),
			Hint:    "This usually means the comment was returned without its parent. Try `basecamp comments show`.",
		}
	}
	parentID := trigger.Parent.ID
	parentIDStr := strconv.FormatInt(parentID, 10)

	// Step 3 — full parent recording, via a server-derived endpoint (never the
	// parent's URL field, which could point off-origin).
	recording, recordingFull, err := fetchThreadParent(cmd, app, trigger.Parent)
	if err != nil {
		return err
	}

	// Step 4 — truthful discussion window over the active comments.
	listResult, err := app.Account().Comments().List(cmd.Context(), parentID, &basecamp.CommentListOptions{Limit: -1})
	if err != nil {
		return convertSDKError(err)
	}
	fetched := sortCommentsChronologically(listResult.Comments)
	focusIdx := commentIndexByID(fetched, trigger.ID)
	selected, selection := selectCommentWindow(fetched, focusIdx, all, effectiveWindow)

	meta := buildCommentsMeta(len(fetched), len(selected), selection, focusIdx >= 0, listResult.Meta)

	// Step 5 — reply-ready, escaped author handle.
	author := buildFocusAuthor(trigger.Creator)

	focus := map[string]any{
		"comment_id": trigger.ID,
		"created_at": trigger.CreatedAt.Format(time.RFC3339),
		"content":    trigger.Content,
		"author":     author,
	}

	data := map[string]any{
		"recording_full": recordingFull,
		"focus":          focus,
		"comments":       selected,
		"comments_meta":  meta,
		"reply_target":   map[string]any{"recording_id": parentID},
	}
	// A fully-fetched parent is `recording`; a sparse ref (unmapped type) is
	// `recording_ref` so consumers can tell the two apart.
	if recordingFull {
		data["recording"] = recording
	} else {
		data["recording_ref"] = recording
	}

	display := threadDisplayProjection(recording, recordingFull, trigger)

	respOpts := []output.ResponseOption{
		output.WithEntity("comment_thread"),
		output.WithDisplayData(display),
		output.WithSummary(threadSummary(trigger, author, len(selected))),
		output.WithBreadcrumbs(output.Breadcrumb{
			Action:      "reply",
			Cmd:         fmt.Sprintf("basecamp comments create %s <text>", parentIDStr),
			Description: "Reply on the parent recording",
		}),
	}
	if listResult.Meta.Truncated {
		respOpts = append(respOpts, output.WithNotice(
			"Discussion truncated: more comments exist on the server than were fetched. "+
				"\"all\"/\"most recent\" cover the fetched subset only."))
	}

	return app.OK(data, respOpts...)
}

// commentTriggerID classifies the argument and returns an exact comment ID plus
// the account named by the URL (empty for a bare ID). It accepts a bare comment
// ID, a comment fragment URL (…#__recording_789), or a direct comment URL
// (/comments/789). A plain recording URL or collection URL is rejected — the
// plain-URL extractor would silently collapse it to a recording ID, which is
// exactly the mistake this command must not make.
func commentTriggerID(arg string) (id, accountID string, err error) {
	parsed := urlarg.Parse(arg)
	if parsed == nil {
		// Not a URL — treat as a bare comment ID (validated by ParseInt later).
		return arg, "", nil
	}
	if parsed.CommentID != "" {
		return parsed.CommentID, parsed.AccountID, nil
	}
	if parsed.Type == "comments" && parsed.RecordingID != "" && !parsed.IsCollection {
		return parsed.RecordingID, parsed.AccountID, nil
	}
	return "", "", output.ErrUsageHint(
		"That URL points to a recording, not a comment",
		"Pass a comment ID or a comment URL (with a #__recording_… fragment). "+
			"For the recording itself, use: basecamp show <url>",
	)
}

// fetchThreadParent loads the full parent recording using a type-derived
// endpoint. A mapped type that then fails to fetch, returns 204, or fails to
// decode is a hard error — never a silent degrade. Only an unmapped or
// contextual-without-parent type (endpoint "") returns a sparse ref built from
// the parent fields, with recordingFull=false.
func fetchThreadParent(cmd *cobra.Command, app *appctx.App, parent *basecamp.Parent) (map[string]any, bool, error) {
	parentIDStr := strconv.FormatInt(parent.ID, 10)
	parentData := map[string]any{
		"type": parent.Type,
		"id":   parent.ID,
		"url":  parent.URL,
	}
	endpoint := recordingTypeEndpoint(parentData, parentIDStr)
	if endpoint == "" {
		ref := map[string]any{
			"id":   parent.ID,
			"type": parent.Type,
			"url":  parent.URL,
		}
		if parent.Title != "" {
			ref["title"] = parent.Title
		}
		return ref, false, nil
	}

	resp, err := app.Account().Get(cmd.Context(), endpoint)
	if err != nil {
		return nil, false, convertSDKError(err)
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil, false, &output.Error{
			Code:       output.CodeAPI,
			Message:    fmt.Sprintf("Parent recording %s returned no content", parentIDStr),
			HTTPStatus: resp.StatusCode,
		}
	}

	// UseNumber preserves int64 IDs (> 2^53) through the map round-trip.
	var recording map[string]any
	dec := json.NewDecoder(bytes.NewReader(resp.Data))
	dec.UseNumber()
	if err := dec.Decode(&recording); err != nil {
		return nil, false, &output.Error{
			Code:    output.CodeAPI,
			Message: fmt.Sprintf("Could not decode parent recording %s: %v", parentIDStr, err),
		}
	}
	return recording, true, nil
}

// sortCommentsChronologically returns the comments sorted by created_at
// ascending, then id ascending. The API guarantees no order, so we never trust
// the received sequence.
func sortCommentsChronologically(comments []basecamp.Comment) []basecamp.Comment {
	sorted := make([]basecamp.Comment, len(comments))
	copy(sorted, comments)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
	})
	return sorted
}

// commentIndexByID returns the index of the comment with the given ID, or -1.
func commentIndexByID(comments []basecamp.Comment, id int64) int {
	for i := range comments {
		if comments[i].ID == id {
			return i
		}
	}
	return -1
}

// selectCommentWindow chooses which comments to return. --all returns every
// fetched comment. Otherwise it returns a window of at most `window` comments:
// centered on the focus when present ((N-1)/2 older, the focus, the rest newer,
// with unused capacity shifted toward the other side at either boundary), or the
// most-recent N when the focus is absent from the fetched set.
func selectCommentWindow(comments []basecamp.Comment, focusIdx int, all bool, window int) ([]basecamp.Comment, string) {
	if all {
		return comments, "all_fetched"
	}

	n := window
	if n > len(comments) {
		n = len(comments)
	}
	if n <= 0 {
		return []basecamp.Comment{}, "focus_window"
	}

	// Focus absent → most-recent N (the tail of the chronological list).
	if focusIdx < 0 {
		return comments[len(comments)-n:], "focus_window"
	}

	before := (n - 1) / 2
	after := n - 1 - before
	start := focusIdx - before
	end := focusIdx + after // inclusive

	// Shift unused capacity toward the other side at each boundary.
	if start < 0 {
		end += -start
		start = 0
	}
	if last := len(comments) - 1; end > last {
		start -= end - last
		end = last
	}
	if start < 0 {
		start = 0
	}
	return comments[start : end+1], "focus_window"
}

// buildCommentsMeta assembles the facts-only comments_meta object.
func buildCommentsMeta(fetched, returned int, selection string, focusPresent bool, meta basecamp.ListMeta) map[string]any {
	m := map[string]any{
		"scope":                    "active",
		"fetched":                  fetched,
		"returned":                 returned,
		"selection":                selection,
		"order":                    "created_at_asc_id_asc",
		"focus_in_active_comments": focusPresent,
		"fetch_complete":           !meta.Truncated,
		"api_truncated":            meta.Truncated,
	}
	// TotalCount is 0 when the X-Total-Count header was absent — never treat 0
	// as an authoritative total.
	if meta.TotalCount > 0 {
		m["total"] = meta.TotalCount
		m["total_known"] = true
	} else {
		m["total"] = nil
		m["total_known"] = false
	}
	return m
}

// buildFocusAuthor builds the reply-ready author handle. The @mention label is
// SanitizeSingleLine'd first (drops newlines/terminal controls) then
// Markdown-escaped, so a hostile name can neither break the terminal nor the
// [@name](scheme:value) link syntax.
func buildFocusAuthor(creator *basecamp.Person) map[string]any {
	if creator == nil {
		return map[string]any{
			"id":      nil,
			"name":    "",
			"mention": unavailableMention(),
		}
	}

	author := map[string]any{
		"id":   creator.ID,
		"name": creator.Name,
	}

	label := markdownEscape(richtext.SanitizeSingleLine(creator.Name))
	switch {
	case label == "":
		author["mention"] = unavailableMention()
	case creator.AttachableSGID != "":
		author["mention"] = map[string]any{
			"syntax":          fmt.Sprintf("[@%s](mention:%s)", label, creator.AttachableSGID),
			"resolution":      "embedded_sgid",
			"requires_lookup": false,
		}
	case creator.ID > 0:
		// A positive person ID with no embedded SGID resolves via one lookup at
		// reply time — not labeled non-pingable, which we don't verify here.
		author["mention"] = map[string]any{
			"syntax":          fmt.Sprintf("[@%s](person:%d)", label, creator.ID),
			"resolution":      "person_lookup",
			"requires_lookup": true,
		}
	default:
		author["mention"] = unavailableMention()
	}
	return author
}

func unavailableMention() map[string]any {
	return map[string]any{
		"syntax":          nil,
		"resolution":      "unavailable",
		"requires_lookup": false,
	}
}

// markdownEscape backslash-escapes Markdown metacharacters so an author name is
// safe inside a [@name](scheme:value) mention link and cannot inject emphasis,
// links, or raw HTML. Newlines/terminal controls are handled separately by
// SanitizeSingleLine, which must run first.
func markdownEscape(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", "\\*",
		"_", "\\_",
		"{", "\\{",
		"}", "\\}",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"<", "\\<",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"!", "\\!",
		"|", "\\|",
		"~", "\\~",
	)
	return replacer.Replace(s)
}

// threadDisplayProjection builds the flat projection consumed by the
// comment_thread schema. Presenter schemas read direct keys, not nested
// recording.*/focus.*, so this is distinct from the structured .data.
func threadDisplayProjection(recording map[string]any, recordingFull bool, trigger *basecamp.Comment) map[string]any {
	authorName := ""
	if trigger.Creator != nil {
		authorName = trigger.Creator.Name
	}
	// The projection feeds the human renderer, so single-line detail fields
	// (author, type) are collapsed to one line here — the presenter's text
	// format strips terminal escapes but preserves newlines, which would break
	// the row layout. The machine contract keeps these values raw.
	return map[string]any{
		"recording_id":          stringField(recording["id"]),
		"recording_type":        richtext.SanitizeSingleLine(stringField(recording["type"])),
		"recording_title":       recordingTitleField(recording),
		"recording_content":     stringField(recording["content"]),
		"recording_description": stringField(recording["description"]),
		"recording_full":        recordingFull,
		"focus_comment_id":      trigger.ID,
		"focus_author":          richtext.SanitizeSingleLine(authorName),
		"focus_created_at":      trigger.CreatedAt.Format(time.RFC3339),
		"focus_content":         trigger.Content,
	}
}

// recordingTitleField extracts a human title from a recording, trying the usual
// title-bearing fields in order.
func recordingTitleField(recording map[string]any) string {
	for _, key := range []string{"title", "name", "subject"} {
		if v := stringField(recording[key]); v != "" {
			return v
		}
	}
	return ""
}

// stringField renders a decoded JSON value as a string. IDs decoded with
// UseNumber arrive as json.Number; everything else is best-effort.
func stringField(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	case json.Number:
		return val.String()
	default:
		return fmt.Sprintf("%v", val)
	}
}

// threadSummary is the one-line human summary for the thread response.
func threadSummary(trigger *basecamp.Comment, author map[string]any, returned int) string {
	name, _ := author["name"].(string)
	if name == "" {
		name = "unknown"
	}
	return fmt.Sprintf("Comment #%d by %s — %d in discussion", trigger.ID, name, returned)
}

func newCommentsUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update <id|url> <content>",
		Short: "Update a comment",
		Long: `Update an existing comment's content.

You can pass either a comment ID or a Basecamp URL:
  basecamp comments update 789 "new text"
  basecamp comments update https://3.basecamp.com/123/buckets/456/todos/111#__recording_789 "new text"

Use - as the content argument to read the updated content from stdin:
  basecamp comments update 789 - < body.md

For multiline or non-ASCII content, prefer stdin over bash ANSI-C quoting
($'...') — under a POSIX /bin/sh it posts a literal leading $ and keeps \n
as backslash-n.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>")
			}
			if len(args) < 2 {
				return missingArg(cmd, "<content>")
			}

			content, err := contentArgOrStdin(cmd, args[1:])
			if err != nil {
				return err
			}
			if strings.TrimSpace(content) == "" {
				return missingArg(cmd, "<content>")
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract comment ID from URL if provided
			// Uses extractCommentWithProject to prefer CommentID from URL fragments
			commentIDStr, _ := extractCommentWithProject(args[0])

			commentID, err := strconv.ParseInt(commentIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid comment ID")
			}

			// Convert Markdown content to HTML for Basecamp's rich text fields
			html := richtext.MarkdownToHTML(content)

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

			req := &basecamp.UpdateCommentRequest{
				Content: html,
			}

			comment, err := app.Account().Comments().Update(cmd.Context(), commentID, req)
			if err != nil {
				return convertSDKError(err)
			}

			respOpts := []output.ResponseOption{
				output.WithEntity("comment"),
				output.WithSummary(fmt.Sprintf("Updated comment #%s", commentIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp comments show %s", commentIDStr),
						Description: "View comment",
					},
				),
			}
			if notice := unresolvedMentionWarning(mentionResult.Unresolved); notice != "" {
				respOpts = append(respOpts, output.WithDiagnostic(notice))
			}
			return app.OK(comment, respOpts...)
		},
	}

	return cmd
}

func newCommentsCreateCmd() *cobra.Command {
	var edit bool
	var attachFiles []string

	cmd := &cobra.Command{
		Use:   "create <id|url> <content>",
		Short: "Add a comment",
		Long: `Add a comment to a Basecamp item (todo, message, card, etc.)

The first argument is the item ID or URL to comment on.
Comma-separated IDs add the same comment to multiple items:
  basecamp comments create 789 "Looks good!"
  basecamp comments create 789,012,345 "Looks good!"
  basecamp comments create https://3.basecamp.com/123/buckets/456/todos/789 "Looks good!"

Content can also be piped from stdin:
  printf 'Looks good!' | basecamp comments create 789

Content supports Markdown and @mentions (@Name or @First.Last):
  basecamp comments create 789 "Hey @Jane.Smith, **please review**"

Use - as the content argument to read content from stdin:
  basecamp comments create 789 - < body.md

For multiline or non-ASCII content, prefer stdin over bash ANSI-C quoting
($'...'). $'...' is a bash/zsh extension; under a POSIX /bin/sh (dash,
busybox-ash) it posts a literal leading $ and keeps \n as backslash-n:
  printf '%s\n' 'First line' '' '<bc-attachment ...>' | basecamp comments create 789 -`,
		Annotations: map[string]string{"agent_notes": "Comments are flat — reply to parent item, not to other comments\nURL fragments (#__recording_456) are comment IDs — comment on the parent recording_id, not the comment_id\nComments are on items (todos, messages, cards, etc.) — not on other comments"},
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Show help when invoked with no args
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>")
			}

			// First arg is always the recording ID(s)
			recordingArg := args[0]

			if edit && len(args) > 1 {
				return output.ErrUsage("cannot combine --edit and positional content")
			}

			var content string
			if len(args) > 1 {
				var err error
				content, err = contentArgOrStdin(cmd, args[1:])
				if err != nil {
					return err
				}
			}
			if edit {
				fi, err := os.Stdin.Stat()
				if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
					return output.ErrUsage("cannot use --edit when stdin is not a terminal")
				}
				content, err = editor.Open("")
				if err != nil {
					return output.ErrUsage(err.Error())
				}
			}

			if !edit && strings.TrimSpace(content) == "" {
				stdinContent, hasPipedStdin, err := readPipedStdin(cmd)
				if err != nil {
					return err
				}
				if hasPipedStdin {
					content = stdinContent
				}
			}

			// Show help when invoked with no content; keep error if editor was opened
			if strings.TrimSpace(content) == "" {
				if edit {
					return output.ErrUsage("Comment content required")
				}
				return missingArg(cmd, "<content>")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Expand comma-separated IDs and extract from URLs
			expandedIDs := extractIDs([]string{recordingArg})

			// Create comments on all recordings
			// Convert Markdown content to HTML for Basecamp's rich text fields
			html := richtext.MarkdownToHTML(content)

			// Resolve inline images (![alt](./path) → upload + <bc-attachment>)
			html, err := resolveLocalImages(cmd, app, html)
			if err != nil {
				return err
			}

			// Resolve @mentions (e.g., @John, @John.Doe → clickable mention tags)
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

			req := &basecamp.CreateCommentRequest{
				Content: html,
			}

			var commented []string
			var commentIDs []string
			var failed []string
			var lastComment *basecamp.Comment
			var firstAPIErr error // Capture first API error for better error reporting

			for _, recordingIDStr := range expandedIDs {
				recordingID, parseErr := strconv.ParseInt(recordingIDStr, 10, 64)
				if parseErr != nil {
					failed = append(failed, recordingIDStr)
					continue
				}

				comment, createErr := app.Account().Comments().Create(cmd.Context(), recordingID, req)
				if createErr != nil {
					failed = append(failed, recordingIDStr)
					if firstAPIErr == nil {
						firstAPIErr = createErr
					}
					continue
				}

				lastComment = comment
				commentIDs = append(commentIDs, fmt.Sprintf("%d", comment.ID))
				commented = append(commented, recordingIDStr)
			}

			// If all operations failed, return an error for automation
			if len(commented) == 0 && len(failed) > 0 {
				if firstAPIErr != nil {
					// Convert SDK error to preserve rate-limit hints and exit codes
					converted := convertSDKError(firstAPIErr)
					// If it's an output.Error, preserve its fields but add IDs to message
					var outErr *output.Error
					if errors.As(converted, &outErr) {
						return &output.Error{
							Code:       outErr.Code,
							Message:    fmt.Sprintf("Failed to comment on items %s: %s", strings.Join(failed, ", "), outErr.Message),
							Hint:       outErr.Hint,
							HTTPStatus: outErr.HTTPStatus,
							Retryable:  outErr.Retryable,
							Cause:      outErr,
						}
					}
					return fmt.Errorf("failed to comment on items %s: %w", strings.Join(failed, ", "), converted)
				}
				return output.ErrUsage(fmt.Sprintf("Failed to comment on all items: %s", strings.Join(failed, ", ")))
			}

			// Single comment: return the comment object directly
			if len(commented) == 1 && len(failed) == 0 && lastComment != nil {
				respOpts := []output.ResponseOption{
					output.WithEntity("comment"),
					output.WithSummary(fmt.Sprintf("Commented on #%s", commented[0])),
					output.WithBreadcrumbs(
						output.Breadcrumb{
							Action:      "show",
							Cmd:         fmt.Sprintf("basecamp comments show %d", lastComment.ID),
							Description: "View comment",
						},
						output.Breadcrumb{
							Action:      "update",
							Cmd:         fmt.Sprintf("basecamp comments update %d <text>", lastComment.ID),
							Description: "Update comment",
						},
					),
				}
				if mentionNotice != "" {
					respOpts = append(respOpts, output.WithDiagnostic(mentionNotice))
				}
				return app.OK(lastComment, respOpts...)
			}

			// Batch: build result map
			result := map[string]any{
				"commented_recordings": commented,
				"comment_ids":          commentIDs,
				"failed":               failed,
			}

			var summary string
			if len(failed) > 0 {
				summary = fmt.Sprintf("Added %d comment(s), %d failed: %s", len(commented), len(failed), strings.Join(failed, ", "))
			} else {
				summary = fmt.Sprintf("Added %d comment(s) to: %s", len(commented), strings.Join(commented, ", "))
			}

			batchOpts := []output.ResponseOption{
				output.WithSummary(summary),
			}
			if mentionNotice != "" {
				batchOpts = append(batchOpts, output.WithDiagnostic(mentionNotice))
			}
			return app.OK(result, batchOpts...)
		},
	}

	cmd.Flags().BoolVar(&edit, "edit", false, "Open $EDITOR to compose content")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")

	return cmd
}

func contentArgOrStdin(cmd *cobra.Command, args []string) (string, error) {
	if len(args) == 1 && args[0] == "-" {
		b, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", output.ErrUsage(fmt.Sprintf("failed to read content from stdin: %v", err))
		}
		return string(b), nil
	}
	return strings.Join(args, " "), nil
}
