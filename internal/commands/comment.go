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
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/editor"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
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
		newCommentsListCmd(&project),
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

func newCommentsListCmd(project *string) *cobra.Command {
	var limit, page int
	var all, allProjects bool

	cmd := &cobra.Command{
		Use:   "list [id|url]",
		Short: "List comments on an item, or across every project",
		Long: `List all comments on an item.

Without an item, lists every comment across all accessible projects,
newest first. Comments hang off a single item rather than off a project,
so a project cannot narrow that listing: a configured project is ignored
and an explicit --project asks for an item instead. Pass --all-projects to
state the account-wide intent outright.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			recordingArg := ""
			if len(args) > 0 {
				recordingArg = args[0]
			}
			return runCommentsList(cmd, recordingArg, *project, limit, page, all, allProjects)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of comments to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all comments (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")
	cmd.Flags().BoolVar(&allProjects, "all-projects", false, "List comments across every project instead of one item")

	return cmd
}

// runCommentsList picks the scope before validating against it. Comments are
// per-item, so a project alone cannot produce a listing and the usual
// flag > config > prompt precedence does not apply: only an item ID scopes the
// item feed, and everything else lands on the account-wide one.
func runCommentsList(cmd *cobra.Command, recordingArg, project string, limit, page int, all, allProjects bool) error {
	app := appctx.FromContext(cmd.Context())

	// Combinations that are wrong under either scope.
	if all && limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if page > 0 && (all || limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}

	// --project/-p/--in is explicit whether it is given after the group noun
	// (bound to project here) or at the root, where it lands on app.Flags
	// instead — so cmd.Flags().Changed would miss half the forms.
	explicitProject := project != "" || app.Flags.Project != ""

	switch {
	case allProjects && recordingArg != "":
		return output.ErrUsageHint(
			"--all-projects cannot be combined with an item ID",
			"Drop the ID to list every comment in the account, or drop --all-projects to list one item's comments.")
	case allProjects && explicitProject:
		return output.ErrUsageHint(
			"--all-projects cannot be combined with --project",
			"Account-wide comments span every project. Drop --project, or drop --all-projects and pass an item ID.")
	case allProjects:
		return runCommentsListAccountWide(cmd, app, limit, page, all)
	case recordingArg != "":
		return runCommentsListForItem(cmd, app, recordingArg, limit, page, all)
	case explicitProject:
		return output.ErrUsageHint(
			"--project cannot scope a comment listing; an item ID can",
			"Comments belong to one item: basecamp comments list <id|url>. "+
				"To list every comment in the account, drop --project and pass --all-projects.")
	default:
		// No item and no explicit project. A configured project is not a scope
		// this listing could honor even if we wanted to, so it is ignored rather
		// than turned into an error, and the account-wide feed answers instead.
		return runCommentsListAccountWide(cmd, app, limit, page, all)
	}
}

func runCommentsListForItem(cmd *cobra.Command, app *appctx.App, recordingArg string, limit, page int, all bool) error {
	if page > 1 {
		return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
	}

	// Extract recording ID from URL if provided
	recordingID := extractID(recordingArg)

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

// defaultAccountWideCommentLimit caps the account-wide feed at the same 100
// comments `comments list <id>` defaults to. Dropping the item must not
// silently promote a bounded command into a full-account crawl.
const defaultAccountWideCommentLimit = 100

// runCommentsListAccountWide lists every comment in the account, newest first.
// The payload is []Recording, which the styled renderer already handles as-is
// (`recordings list` hands it the same type), so there is nothing to flatten
// and no format branch to make.
func runCommentsListAccountWide(cmd *cobra.Command, app *appctx.App, limit, page int, all bool) error {
	// A todolist names a container inside one project, so it cannot narrow an
	// account-wide feed any more than a project can. Reject the explicit flag
	// rather than accept and drop it; an ambient configured todolist is ignored
	// on the same grounds as an ambient configured project.
	if app.Flags.Todolist != "" {
		return output.ErrUsageHint(
			"--todolist cannot scope an account-wide comment listing",
			"Drop --todolist, or pass an item ID to list that item's comments.")
	}
	if limit < 0 {
		return output.ErrUsage("--limit must be a positive number")
	}
	// --page 0 is Cobra's "unset" but the SDK's "follow every page", so an
	// explicit 0 would hand back a full-account crawl nobody asked for. --all is
	// the spelling for that.
	if cmd.Flags().Changed("page") && page < 1 {
		return output.ErrUsage("--page must be a positive page number; use --all to fetch every page")
	}

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	var (
		comments []basecamp.Recording
		meta     basecamp.ListMeta
	)
	switch {
	case all || page > 0:
		// accountWidePage maps --all onto page 0 ("follow the Link header") and
		// otherwise passes the requested page straight through: the aggregate
		// accepts any positive page, unlike the item feed.
		result, err := app.Account().Everything().Comments(cmd.Context(), accountWidePage(page, all))
		if err != nil {
			return convertSDKError(err)
		}
		comments, meta = result.Recordings, result.Meta
	default:
		effectiveLimit := defaultAccountWideCommentLimit
		if limit > 0 {
			effectiveLimit = limit
		}
		var err error
		comments, meta, err = fetchAccountWideComments(cmd, app, effectiveLimit)
		if err != nil {
			return err
		}
	}

	respOpts := append(accountWideRespOpts(len(comments), "comment", "comments", meta, "--all", limit > 0),
		output.WithDisplayData(flattenAccountWideRecordings(comments)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "basecamp comments show <id>",
				Description: "Show comment",
			},
			output.Breadcrumb{
				Action:      "thread",
				Cmd:         "basecamp comments thread <id>",
				Description: "Show a comment with its discussion",
			},
		))

	return app.OK(comments, respOpts...)
}

// fetchAccountWideComments collects up to limit comments by walking positive
// pages, stopping as soon as the limit is met or a page comes back empty.
// Asking for page 0 would follow the Link header to the end of the account
// before truncating — correct, but it downloads every comment to keep 100.
// Each iteration adds at least one comment, so the walk always terminates.
func fetchAccountWideComments(cmd *cobra.Command, app *appctx.App, limit int) ([]basecamp.Recording, basecamp.ListMeta, error) {
	var (
		comments []basecamp.Recording
		meta     basecamp.ListMeta
	)
	for page := int32(1); len(comments) < limit; page++ {
		result, err := app.Account().Everything().Comments(cmd.Context(), page)
		if err != nil {
			return nil, meta, convertSDKError(err)
		}
		if page == 1 {
			// X-Total-Count is the account-wide total on every page; take it
			// from the first so the truncation notice is honest about how much
			// this walk left behind.
			meta = result.Meta
		}
		if len(result.Recordings) == 0 {
			break
		}
		comments = append(comments, result.Recordings...)
	}
	if len(comments) > limit {
		comments = comments[:limit]
	}
	return comments, meta, nil
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

			// Whether the account is persistently available (on-disk config or env)
			// before resolution. When it isn't — adopted from the URL, resolved
			// interactively, or passed via the process-local --account flag — the
			// advertised follow-ups must spell out --account so they remain runnable
			// in a fresh process.
			persistentAccount := hasPersistentAccount(app.Config)

			// Same safe front door as `thread`: classify, guard the account, and
			// validate the ID — so a plain recording URL or wrong-account URL is
			// rejected here instead of 404ing or silently targeting elsewhere.
			commentID, err := resolveCommentTrigger(cmd, app, args[0])
			if err != nil {
				return err
			}
			accountArg := replyAccountArg(persistentAccount, app.Config.AccountID)

			comment, err := app.Account().Comments().Get(cmd.Context(), commentID)
			if err != nil {
				return convertSDKError(err)
			}

			// Guard an empty/zero comment before enrichment. app.OK's checkZeroData
			// rejects an all-zero recording, but the non-empty mention map we add
			// below would mask it — reporting a bogus success (comment #0, empty
			// content) instead of empty_response. A real comment always has a
			// positive ID.
			if comment == nil || comment.ID == 0 {
				return &output.Error{
					Code:    "empty_response",
					Message: "API returned empty data",
					Hint:    "The response contained no comment. This may indicate a deserialization issue.",
				}
			}

			creatorName := ""
			if comment.Creator != nil {
				creatorName = comment.Creator.Name
			}

			// Enrich with the two cheap reply atoms — both pure functions of this
			// single Get, no extra API call. reply_target routes to the parent
			// recording (comments are flat); mention is a paste-ready author
			// handle with the same escaped/round-tripping contract as `thread`.
			// These ride in machine .data only (comment.yaml is untouched); the
			// reply breadcrumb is the human-output affordance.
			m, ok := output.NormalizeData(comment).(map[string]any)
			if !ok || m == nil {
				// Fail closed — never emit an empty "successful" comment if the
				// normalized shape is unexpectedly not a map.
				return &output.Error{
					Code:    output.CodeAPI,
					Message: fmt.Sprintf("Could not render comment #%d", commentID),
				}
			}
			m["mention"] = buildFocusAuthor(comment.Creator)["mention"]

			breadcrumbs := make([]output.Breadcrumb, 0, 2)
			breadcrumbs = append(breadcrumbs, output.Breadcrumb{
				Action:      "update",
				Cmd:         fmt.Sprintf("basecamp comments update %d <text>%s", commentID, accountArg),
				Description: "Update comment",
			})
			if comment.Parent != nil && comment.Parent.ID != 0 {
				m["reply_target"] = replyTarget(comment.Parent.ID, app.Config.AccountID)
				breadcrumbs = append(breadcrumbs, output.Breadcrumb{
					Action:      "reply",
					Cmd:         fmt.Sprintf("basecamp comments create %d <text>%s", comment.Parent.ID, accountArg),
					Description: "Reply on the parent recording",
				})
			}

			return app.OK(m,
				output.WithEntity("comment"),
				output.WithSummary(fmt.Sprintf("Comment #%d by %s", commentID, creatorName)),
				output.WithBreadcrumbs(breadcrumbs...),
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

	// Classify + guard + validate through the one shared front door, so `thread`
	// and `comments show` are equally safe: a plain recording URL / collection is
	// rejected toward `basecamp show`, a wrong-account URL and a non-positive ID
	// are rejected before any API call.
	persistentAccount := hasPersistentAccount(app.Config)
	triggerID, err := resolveCommentTrigger(cmd, app, arg)
	if err != nil {
		return err
	}
	// When the account isn't persistently available (adopted from the URL,
	// resolved interactively, or passed via the process-local --account flag), the
	// advertised follow-ups must carry --account so they stay runnable in a fresh
	// process.
	accountArg := replyAccountArg(persistentAccount, app.Config.AccountID)

	// Step 1 — resolve the focus comment.
	trigger, err := app.Account().Comments().Get(cmd.Context(), triggerID)
	if err != nil {
		return convertSDKError(err)
	}

	// Guard an empty/zero focus before dereferencing it — a nil or all-zero
	// comment (empty API response or a deserialization regression) would panic on
	// trigger.Parent or misreport the zero value as a real comment missing its
	// parent. A real comment always has a positive ID. Mirrors `comments show`.
	if trigger == nil || trigger.ID == 0 {
		return &output.Error{
			Code:    "empty_response",
			Message: "API returned empty data",
			Hint:    "The response contained no comment. This may indicate a deserialization issue.",
		}
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
		"comment_id":          trigger.ID,
		"created_at":          trigger.CreatedAt.Format(time.RFC3339),
		"content":             trigger.Content,
		"author":              author,
		"content_attachments": trigger.ContentAttachments,
	}

	data := map[string]any{
		"recording_full": recordingFull,
		"focus":          focus,
		"comments":       selected,
		"comments_meta":  meta,
		"reply_target":   replyTarget(parentID, app.Config.AccountID),
	}
	// A fully-fetched parent is `recording`; a sparse ref (unmapped type) is
	// `recording_ref` so consumers can tell the two apart.
	if recordingFull {
		data["recording"] = recording
	} else {
		data["recording_ref"] = recording
	}

	focusPresent := focusIdx >= 0
	fetchComplete := !listResult.Meta.Truncated

	focusMention := ""
	if m, ok := author["mention"].(map[string]any); ok {
		if s, ok := m["syntax"].(string); ok {
			focusMention = s
		}
	}
	display := threadDisplayProjection(recording, recordingFull, trigger, focusMention)

	// Breadcrumbs: a type-safe attachment download (when the focus carries
	// files) comes first; the reply target is always last. The typed
	// `--type comment` form is built inline — the generic attachmentBreadcrumb
	// omits it, and a bare recording lookup 204/404s for comments.
	breadcrumbs := make([]output.Breadcrumb, 0, 2)
	if len(trigger.ContentAttachments) > 0 {
		breadcrumbs = append(breadcrumbs, output.Breadcrumb{
			Action:      "download",
			Cmd:         fmt.Sprintf("basecamp attachments download %d --type comment%s", trigger.ID, accountArg),
			Description: "Download comment attachments",
		})
	}
	breadcrumbs = append(breadcrumbs, output.Breadcrumb{
		Action:      "reply",
		Cmd:         fmt.Sprintf("basecamp comments create %s <text>%s", parentIDStr, accountArg),
		Description: "Reply on the parent recording",
	})

	respOpts := []output.ResponseOption{
		output.WithEntity("comment_thread"),
		output.WithDisplayData(display),
		output.WithSummary(threadSummary(trigger, author, len(selected), len(fetched), selection, fetchComplete, focusPresent)),
		output.WithBreadcrumbs(breadcrumbs...),
	}

	// One combined notice: WithNotice overwrites, so collect fragments and emit
	// them together. States facts only — focus absence and truncation — never
	// speculation about archived/trashed/deleted comments.
	var notices []string
	if !focusPresent {
		if all {
			notices = append(notices, fmt.Sprintf(
				"Focus comment is not present in the fetched active discussion; showing all %d fetched comments.", len(selected)))
		} else {
			notices = append(notices, fmt.Sprintf(
				"Focus comment is not present in the fetched active discussion; showing the most recent %d fetched comments.", len(selected)))
		}
	}
	if listResult.Meta.Truncated {
		notices = append(notices, "Discussion truncated: more comments exist on the server than were fetched. "+
			"\"all\"/\"most recent\" cover the fetched subset only.")
	}
	if len(notices) > 0 {
		respOpts = append(respOpts, output.WithNotice(strings.Join(notices, " ")))
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

// resolveCommentTrigger is the single safe front door both `comments thread` and
// `comments show` use to turn an argument into a validated comment ID. It
// enforces every trust-boundary check before any API call — trusted host,
// exact-comment classification, a positive ID, and URL/account identity — so the
// cheaper `show` is no less trustworthy than the deep `thread`.
func resolveCommentTrigger(cmd *cobra.Command, app *appctx.App, arg string) (int64, error) {
	// A URL-shaped trigger must live on a trusted Basecamp host. The URL router
	// is host-agnostic, so without this a look-alike on an attacker-controlled
	// host (evil.example/…/comments/456) would be parsed into a fetch of an
	// internal comment — the confused-deputy case hostutil exists to prevent.
	// Bare IDs are not URLs and skip the check.
	if urlarg.IsURL(arg) && !hostutil.IsTrustedBasecampHost(arg, app.Config.BaseURL) {
		return 0, output.ErrUsage("refusing untrusted host in URL — expected a Basecamp URL")
	}

	triggerIDStr, urlAccountID, err := commentTriggerID(arg)
	if err != nil {
		return 0, err
	}

	// Validate the ID before any account resolution, so a bad ID fails as
	// "Invalid comment ID" with zero requests even when no account is configured
	// (ensureAccount would otherwise demand --account first).
	id, err := strconv.ParseInt(triggerIDStr, 10, 64)
	if err != nil || id <= 0 {
		return 0, output.ErrUsage("Invalid comment ID")
	}

	// Reconcile the URL's account with configuration before ensureAccount, so the
	// URL's identity governs the fetch rather than an interactively-selected
	// account: adopt it when nothing is configured, reject a configured mismatch
	// rather than silently fetch a same-numbered comment elsewhere.
	if urlAccountID != "" {
		switch {
		case app.Config.AccountID == "":
			app.Config.AccountID = urlAccountID
		case urlAccountID != app.Config.AccountID:
			return 0, output.ErrUsage(fmt.Sprintf("URL account %s does not match the configured account %s", urlAccountID, app.Config.AccountID))
		}
	}

	if err := ensureAccount(cmd, app); err != nil {
		return 0, err
	}
	return id, nil
}

// hasPersistentAccount reports whether the account is available to a fresh
// process without re-specifying it — i.e. backed by an on-disk config layer or
// an exported env var. A flag- or prompt-sourced account is process-local: it
// lives only for this invocation, so an advertised follow-up copied into a new
// process would fail to resolve it. Call this BEFORE resolveCommentTrigger, so a
// URL-adopted / interactively-resolved account (AccountID empty at entry) also
// reads as non-persistent.
func hasPersistentAccount(cfg *config.Config) bool {
	if cfg.AccountID == "" {
		return false
	}
	switch cfg.Sources["account_id"] {
	case string(config.SourceFlag), string(config.SourcePrompt):
		return false
	default:
		return true
	}
}

// replyAccountArg returns " --account <id>" to append to an advertised
// follow-up command when the account is not persistently available (adopted from
// the URL, resolved interactively, or passed via the process-local --account
// flag this run), so the command stays runnable in a fresh process; otherwise
// "". A persistent account needs no echo — the follow-up inherits it.
func replyAccountArg(persistentAccount bool, accountID string) string {
	if persistentAccount || accountID == "" {
		return ""
	}
	return " --account " + accountID
}

// replyTarget builds the machine reply-target contract: the parent recording to
// reply on, plus the account it belongs to so a consumer can construct a
// fully-qualified reply regardless of its own configuration.
func replyTarget(recordingID int64, accountID string) map[string]any {
	rt := map[string]any{"recording_id": recordingID}
	if accountID != "" {
		rt["account_id"] = accountID
	}
	return rt
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
	// A JSON null or {} decodes to an empty map — never claim recording_full over
	// nothing. Hard-error instead of emitting an empty recording.
	if len(recording) == 0 {
		return nil, false, &output.Error{
			Code:    output.CodeAPI,
			Message: fmt.Sprintf("Parent recording %s returned an empty body", parentIDStr),
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
func threadDisplayProjection(recording map[string]any, recordingFull bool, trigger *basecamp.Comment, focusMention string) map[string]any {
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
		"focus_mention":         focusMention,
		"focus_created_at":      trigger.CreatedAt.Format(time.RFC3339),
		"focus_content":         trigger.Content,
	}
}

// recordingTitleField extracts a human title from a recording, trying the usual
// title-bearing fields in order.
func recordingTitleField(recording map[string]any) string {
	// `content` is last: title/name/subject-bearing types (message, card,
	// document) keep those, while types whose title lives in `content` — notably
	// Todo/Todolist::Todo — fall back to it instead of rendering an empty
	// headline. Rich-body types are unaffected because they carry a real title.
	for _, key := range []string{"title", "name", "subject", "content"} {
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

// threadSummary is the one-line human summary for the thread response. It is a
// pure function of the discussion facts (selection, returned, fetched,
// fetch_complete, focus_present) over the full fetch_complete × focus_present ×
// selection cube, so it never asserts a server-side total the fetch did not
// confirm and never claims to center "around the focus" when the focus is absent.
func threadSummary(trigger *basecamp.Comment, author map[string]any, returned, fetched int, selection string, fetchComplete, focusPresent bool) string {
	name, _ := author["name"].(string)
	if name == "" {
		name = "unknown"
	}
	prefix := fmt.Sprintf("Comment #%d by %s — ", trigger.ID, name)

	var body string
	switch {
	case selection == "all_fetched" && fetchComplete:
		body = fmt.Sprintf("all %d active comments", fetched)
	case selection == "all_fetched":
		body = fmt.Sprintf("all %d fetched active comments; fetch incomplete", fetched)
	case focusPresent && fetchComplete:
		body = fmt.Sprintf("showing %d of %d active comments around the focus", returned, fetched)
	case focusPresent:
		body = fmt.Sprintf("showing %d around the focus from %d fetched active comments; more exist on server", returned, fetched)
	case fetchComplete:
		body = fmt.Sprintf("showing the %d most recent of %d active comments", returned, fetched)
	default:
		body = fmt.Sprintf("showing the %d most recent within the fetched subset (%d fetched)", returned, fetched)
	}
	return prefix + body
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
