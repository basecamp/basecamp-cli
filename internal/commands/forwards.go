package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
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
		Annotations: map[string]string{"agent_notes": "Forwards are emails sent into a Basecamp project's inbox\nEach project has one inbox (forward container)\nContent supports Markdown"},
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
	return getDockToolID(cmd.Context(), app, projectID, "inbox", inboxID, "inbox", "inbox")
}

func newForwardsListCmd(project, inboxID *string) *cobra.Command {
	var limit int
	var page int
	var all bool
	var allProjects bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List forwards in project inbox",
		Long: `List all email forwards in the project inbox.

With no project in scope — none passed, none configured, or --all-projects to
override a configured one — lists forwards across every accessible project.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runForwardsList(cmd, *project, *inboxID, limit, page, all, allProjects)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of forwards to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all forwards (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")
	cmd.Flags().BoolVar(&allProjects, "all-projects", false, "List forwards across every project")

	return cmd
}

func runForwardsList(cmd *cobra.Command, project, inboxID string, limit, page int, all, allProjects bool) error {
	app := appctx.FromContext(cmd.Context())

	// Select the scope before validating against it: the account-wide endpoint
	// serves any positive page, while the project-scoped one only serves the
	// first. --all-projects overrides a configured project and conflicts with
	// an explicit one, which arrives either after the group noun or at the
	// root (basecamp --project X forwards list).
	explicitProject := project != "" || app.Flags.Project != ""
	if allProjects && explicitProject {
		return output.ErrUsageHint(
			"--all-projects conflicts with --project/--in",
			"Drop one: --all-projects lists every project, --project lists one",
		)
	}
	if allProjects || !projectKnown(app, project) {
		return runForwardsListEverywhere(cmd, app, inboxID, limit, page, all)
	}

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

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Resolve project. No interactive fallback: reaching here means projectKnown
	// found one of these three sources set, and an empty scope listed
	// account-wide instead of prompting.
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
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

	inboxIDInt, err := strconv.ParseInt(resolvedInboxID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid inbox ID")
	}

	// Build pagination options
	opts := &basecamp.ForwardListOptions{}
	if all {
		opts.Limit = -1 // SDK treats -1 as "fetch all"
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}

	forwardsResult, err := app.Account().Forwards().List(cmd.Context(), inboxIDInt, opts)
	if err != nil {
		return convertSDKError(err)
	}
	forwards := forwardsResult.Forwards

	respOpts := []output.ResponseOption{
		output.WithSummary(fmt.Sprintf("%d forwards", len(forwards))),
	}

	// Add truncation notice if results may be limited
	if notice := output.TruncationNoticeWithTotal(len(forwards), forwardsResult.Meta.TotalCount); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}

	respOpts = append(respOpts,
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp forwards show <id> --in %s", resolvedProjectID),
				Description: "View a forward",
			},
			output.Breadcrumb{
				Action:      "inbox",
				Cmd:         fmt.Sprintf("basecamp forwards inbox --in %s", resolvedProjectID),
				Description: "View inbox details",
			},
		),
	)

	return app.OK(forwards, respOpts...)
}

// runForwardsListEverywhere lists forwards across every accessible project via
// the account-wide aggregate endpoint. No project is in scope here, so every
// flag that names something inside one project is rejected rather than ignored.
func runForwardsListEverywhere(cmd *cobra.Command, app *appctx.App, inboxID string, limit, page int, all bool) error {
	if err := rejectAccountWideTodolist(app, "forward"); err != nil {
		return err
	}
	if inboxID != "" {
		return output.ErrUsageHint(
			"--inbox names an inbox inside one project and has no account-wide meaning",
			"Pass --project/--in to list that project's inbox, or drop --inbox",
		)
	}
	if all && limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if page > 0 && (all || limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}
	if cmd.Flags().Changed("page") && page < 1 {
		return output.ErrUsageHint(
			"--page must be a positive page number",
			"Omit --page, or pass --all, to follow every page",
		)
	}
	if limit < 0 {
		return output.ErrUsage("--limit must be zero or positive")
	}

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// The endpoint spells "follow every page" as page 0, which is where both
	// --all and the default land: this command's project-scoped default is
	// already "0 = all", so listing account-wide does not silently start
	// capping. Only an explicit positive --page narrows to a single page.
	var sdkPage int32
	if page > 0 {
		sdkPage = accountWidePage(page, all)
	}

	result, err := app.Account().Everything().Forwards(cmd.Context(), sdkPage)
	if err != nil {
		return convertSDKError(err)
	}

	// --limit truncates client-side; accountWideRespOpts reports the shortfall
	// against the server's total so the trim is visible.
	forwards := result.Recordings
	if limit > 0 && len(forwards) > limit {
		forwards = forwards[:limit]
	}

	respOpts := accountWideRespOpts(len(forwards), "forward", "forwards", result.Meta, "--all")
	respOpts = append(respOpts, output.WithDisplayData(flattenAccountWideRecordings(forwards)))
	respOpts = append(respOpts,
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "basecamp forwards show <id> --in <project>",
				Description: "View a forward",
			},
			output.Breadcrumb{
				Action:      "list",
				Cmd:         "basecamp forwards list --in <project>",
				Description: "List one project's inbox",
			},
		),
	)

	return app.OK(forwards, respOpts...)
}

func newForwardsShowCmd(project *string) *cobra.Command {
	var cf *commentFlags

	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show a forward",
		Long: `Display detailed information about an email forward.

You can pass either a forward ID or a Basecamp URL:
  basecamp forwards show 789 --in my-project
  basecamp forwards show https://3.basecamp.com/123/buckets/456/inbox_forwards/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			forwardIDStr, urlProjectID := extractWithProject(args[0])

			forwardID, err := strconv.ParseInt(forwardIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid forward ID")
			}

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

			forward, err := app.Account().Forwards().Get(cmd.Context(), forwardID)
			if err != nil {
				return convertSDKError(err)
			}

			subject := forward.Subject
			if subject == "" {
				subject = "Forward"
			}

			enrichment := fetchCommentsForRecording(cmd.Context(), app, forwardIDStr, cf)
			data, commentOpts := enrichment.apply(forward, "")

			opts := make([]output.ResponseOption, 0, 2+len(commentOpts))
			opts = append(opts,
				output.WithSummary(subject),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "replies",
						Cmd:         fmt.Sprintf("basecamp forwards replies %s --in %s", forwardIDStr, resolvedProjectID),
						Description: "View replies",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("basecamp forwards --in %s", resolvedProjectID),
						Description: "List all forwards",
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

func newForwardsInboxCmd(project, inboxID *string) *cobra.Command {
	return &cobra.Command{
		Use:   "inbox",
		Short: "Show inbox details",
		Long:  "Display detailed information about the project inbox.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
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

			// Get inbox ID
			resolvedInboxID, err := getInboxID(cmd, app, resolvedProjectID, *inboxID)
			if err != nil {
				return err
			}

			inboxIDInt, err := strconv.ParseInt(resolvedInboxID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid inbox ID")
			}

			inbox, err := app.Account().Forwards().GetInbox(cmd.Context(), inboxIDInt)
			if err != nil {
				return convertSDKError(err)
			}

			title := inbox.Title
			if title == "" {
				title = "Inbox"
			}

			return app.OK(inbox,
				output.WithSummary(title),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "forwards",
						Cmd:         fmt.Sprintf("basecamp forwards --in %s", resolvedProjectID),
						Description: "List forwards",
					},
				),
			)
		},
	}
}

func newForwardsRepliesCmd(project *string) *cobra.Command {
	var limit int
	var page int
	var all bool

	cmd := &cobra.Command{
		Use:   "replies <forward_id|url>",
		Short: "List replies to a forward",
		Long: `List all replies to an email forward.

You can pass either a forward ID or a Basecamp URL:
  basecamp forwards replies 789 --in my-project
  basecamp forwards replies https://3.basecamp.com/123/buckets/456/inbox_forwards/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			forwardIDStr, urlProjectID := extractWithProject(args[0])

			forwardID, err := strconv.ParseInt(forwardIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid forward ID")
			}

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

			// Build pagination options
			opts := &basecamp.ForwardReplyListOptions{}
			if all {
				opts.Limit = -1 // SDK treats -1 as "fetch all"
			} else if limit > 0 {
				opts.Limit = limit
			}
			if page > 0 {
				opts.Page = page
			}

			repliesResult, err := app.Account().Forwards().ListReplies(cmd.Context(), forwardID, opts)
			if err != nil {
				return convertSDKError(err)
			}
			replies := repliesResult.Replies

			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("%d replies to forward #%s", len(replies), forwardIDStr)),
			}

			// Add truncation notice if results may be limited
			if notice := output.TruncationNoticeWithTotal(len(replies), repliesResult.Meta.TotalCount); notice != "" {
				respOpts = append(respOpts, output.WithNotice(notice))
			}

			respOpts = append(respOpts,
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "forward",
						Cmd:         fmt.Sprintf("basecamp forwards show %s --in %s", forwardIDStr, resolvedProjectID),
						Description: "View the forward",
					},
					output.Breadcrumb{
						Action:      "reply",
						Cmd:         fmt.Sprintf("basecamp forwards reply %s <reply_id> --in %s", forwardIDStr, resolvedProjectID),
						Description: "View a reply",
					},
				),
			)

			return app.OK(replies, respOpts...)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of replies to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all replies (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	return cmd
}

func newForwardsReplyCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "reply <forward_id|url> <reply_id|url>",
		Short: "Show a specific reply",
		Long: `Display detailed information about a reply to an email forward.

You can pass either IDs or Basecamp URLs:
  basecamp forwards reply 789 456 --in my-project
  basecamp forwards reply https://3.basecamp.com/123/buckets/456/inbox_forwards/789 456`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract IDs and project from URLs if provided
			forwardIDStr, urlProjectID := extractWithProject(args[0])
			replyIDStr := extractID(args[1])

			forwardID, err := strconv.ParseInt(forwardIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid forward ID")
			}

			replyID, err := strconv.ParseInt(replyIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid reply ID")
			}

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

			reply, err := app.Account().Forwards().GetReply(cmd.Context(), forwardID, replyID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(reply,
				output.WithSummary("Reply"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "forward",
						Cmd:         fmt.Sprintf("basecamp forwards show %s --in %s", forwardIDStr, resolvedProjectID),
						Description: "View the forward",
					},
					output.Breadcrumb{
						Action:      "replies",
						Cmd:         fmt.Sprintf("basecamp forwards replies %s --in %s", forwardIDStr, resolvedProjectID),
						Description: "List all replies",
					},
				),
			)
		},
	}
}
