package commands

import (
	"fmt"
	"strconv"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewTimelineCmd creates the timeline command for viewing activity feeds.
func NewTimelineCmd() *cobra.Command {
	var project string
	var person string
	var limit int
	var page int
	var all bool

	cmd := &cobra.Command{
		Use:   "timeline [me]",
		Short: "View activity timeline",
		Long: `View activity timelines for the account, a project, or a person.

By default, shows the account-wide activity feed (recent activity across all projects).

Use --in to view a specific project's timeline.
Use "me" or --person to view a person's activity timeline.`,
		Annotations: map[string]string{"agent_notes": "Timeline shows activity feed — account-wide by default, or scoped with --in <project> or --person <id>"},
		Args:        cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTimeline(cmd, args, project, person, limit, page, all)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID or name (alias for --project)")
	cmd.Flags().StringVar(&person, "person", "", "Person ID or name")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of events to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all events (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	return cmd
}

func validateTimelinePagination(limit, page int, all bool) error {
	if all && limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if page > 0 && (all || limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}
	if page > 1 {
		return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
	}
	return nil
}

func timelineListOpts(limit, page int, all bool) *basecamp.TimelineListOptions {
	opts := &basecamp.TimelineListOptions{}
	if all {
		opts.Limit = -1
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}
	return opts
}

func runTimeline(cmd *cobra.Command, args []string, project, person string, limit, page int, all bool) error {
	if err := validateTimelinePagination(limit, page, all); err != nil {
		return err
	}

	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Validate positional argument - only "me" is supported
	if len(args) > 0 && args[0] != "me" {
		return output.ErrUsageHint(
			fmt.Sprintf("invalid argument %q", args[0]),
			"Only \"me\" is supported as a positional argument. Use --person <name> for other people.",
		)
	}

	// Check for mutually exclusive flags
	if person != "" && project != "" {
		return output.ErrUsage("--person and --project are mutually exclusive")
	}

	// Determine which timeline to show based on args and flags
	// Priority: positional "me" > --person flag > --project flag > default (account-wide)

	opts := timelineListOpts(limit, page, all)

	// Check for "me" positional argument
	if len(args) > 0 && args[0] == "me" {
		return runPersonTimeline(cmd, "me", opts)
	}

	// Check for --person flag
	if person != "" {
		return runPersonTimeline(cmd, person, opts)
	}

	// Check for --project flag
	if project != "" {
		return runProjectTimeline(cmd, project, opts)
	}

	// Default: account-wide activity feed
	result, err := app.Account().Timeline().Progress(cmd.Context(), opts)
	if err != nil {
		return convertSDKError(err)
	}

	respOpts := []output.ResponseOption{
		output.WithSummary(fmt.Sprintf("%d recent events", len(result.Events))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "project",
				Cmd:         "basecamp timeline --in <project>",
				Description: "View project timeline",
			},
			output.Breadcrumb{
				Action:      "person",
				Cmd:         "basecamp timeline me",
				Description: "View your activity",
			},
		),
	}

	if notice := output.TruncationNoticeWithTotal(len(result.Events), result.Meta.TotalCount); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}

	return app.OK(result.Events, respOpts...)
}

func runProjectTimeline(cmd *cobra.Command, project string, opts *basecamp.TimelineListOptions) error {
	app := appctx.FromContext(cmd.Context())

	// Resolve project name to ID
	resolvedProjectID, projectName, err := app.Names.ResolveProject(cmd.Context(), project)
	if err != nil {
		return err
	}

	projectIDInt, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	timelineResult, err := app.Account().Timeline().ProjectTimeline(cmd.Context(), projectIDInt, opts)
	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("%d events in %s", len(timelineResult.Events), projectName)
	if projectName == "" {
		summary = fmt.Sprintf("%d events in project #%s", len(timelineResult.Events), resolvedProjectID)
	}

	respOpts := []output.ResponseOption{
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "account",
				Cmd:         "basecamp timeline",
				Description: "View account-wide timeline",
			},
			output.Breadcrumb{
				Action:      "project",
				Cmd:         fmt.Sprintf("basecamp project show %s", resolvedProjectID),
				Description: "View project details",
			},
		),
	}

	if notice := output.TruncationNoticeWithTotal(len(timelineResult.Events), timelineResult.Meta.TotalCount); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}

	return app.OK(timelineResult.Events, respOpts...)
}

func runPersonTimeline(cmd *cobra.Command, personArg string, opts *basecamp.TimelineListOptions) error {
	app := appctx.FromContext(cmd.Context())

	// Resolve person name/ID
	resolvedPersonID, personName, err := app.Names.ResolvePerson(cmd.Context(), personArg)
	if err != nil {
		return err
	}

	personID, err := strconv.ParseInt(resolvedPersonID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid person ID")
	}

	result, err := app.Account().Timeline().PersonProgress(cmd.Context(), personID, opts)
	if err != nil {
		return convertSDKError(err)
	}

	// Use name from result if available, otherwise use resolved name
	displayName := personName
	if result.Person != nil && result.Person.Name != "" {
		displayName = result.Person.Name
	}

	summary := fmt.Sprintf("%d events for %s", len(result.Events), displayName)
	if displayName == "" {
		summary = fmt.Sprintf("%d events for person #%s", len(result.Events), resolvedPersonID)
	}

	respOpts := []output.ResponseOption{
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "account",
				Cmd:         "basecamp timeline",
				Description: "View account-wide timeline",
			},
			output.Breadcrumb{
				Action:      "person",
				Cmd:         fmt.Sprintf("basecamp people show %s", resolvedPersonID),
				Description: "View person details",
			},
		),
	}

	if notice := output.TruncationNoticeWithTotal(len(result.Events), result.Meta.TotalCount); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}

	return app.OK(result.Events, respOpts...)
}
