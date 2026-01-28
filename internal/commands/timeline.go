package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// NewTimelineCmd creates the timeline command for viewing activity feeds.
func NewTimelineCmd() *cobra.Command {
	var project string
	var person string

	cmd := &cobra.Command{
		Use:   "timeline [me]",
		Short: "View activity timeline",
		Long: `View activity timelines for the account, a project, or a person.

By default, shows the account-wide activity feed (recent activity across all projects).

Use --in to view a specific project's timeline.
Use "me" or --person to view a person's activity timeline.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTimeline(cmd, args, project, person)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID or name (alias for --project)")
	cmd.Flags().StringVar(&person, "person", "", "Person ID or name")

	return cmd
}

func runTimeline(cmd *cobra.Command, args []string, project, person string) error {
	app := appctx.FromContext(cmd.Context())

	if err := app.RequireAccount(); err != nil {
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

	// Check for "me" positional argument
	if len(args) > 0 && args[0] == "me" {
		return runPersonTimeline(cmd, "me")
	}

	// Check for --person flag
	if person != "" {
		return runPersonTimeline(cmd, person)
	}

	// Check for --project flag
	if project != "" {
		return runProjectTimeline(cmd, project)
	}

	// Default: account-wide activity feed
	events, err := app.Account().Timeline().Progress(cmd.Context())
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(events,
		output.WithSummary(fmt.Sprintf("%d recent events", len(events))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "project",
				Cmd:         "bcq timeline --in <project>",
				Description: "View project timeline",
			},
			output.Breadcrumb{
				Action:      "person",
				Cmd:         "bcq timeline me",
				Description: "View your activity",
			},
		),
	)
}

func runProjectTimeline(cmd *cobra.Command, project string) error {
	app := appctx.FromContext(cmd.Context())

	// Resolve project name to ID
	resolvedProjectID, projectName, err := app.Names.ResolveProject(cmd.Context(), project)
	if err != nil {
		return err
	}

	projectID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	events, err := app.Account().Timeline().ProjectTimeline(cmd.Context(), projectID)
	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("%d events in %s", len(events), projectName)
	if projectName == "" {
		summary = fmt.Sprintf("%d events in project #%s", len(events), resolvedProjectID)
	}

	return app.OK(events,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "account",
				Cmd:         "bcq timeline",
				Description: "View account-wide timeline",
			},
			output.Breadcrumb{
				Action:      "project",
				Cmd:         fmt.Sprintf("bcq project show %s", resolvedProjectID),
				Description: "View project details",
			},
		),
	)
}

func runPersonTimeline(cmd *cobra.Command, personArg string) error {
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

	result, err := app.Account().Timeline().PersonProgress(cmd.Context(), personID)
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

	return app.OK(result.Events,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "account",
				Cmd:         "bcq timeline",
				Description: "View account-wide timeline",
			},
			output.Breadcrumb{
				Action:      "person",
				Cmd:         fmt.Sprintf("bcq people show %s", resolvedPersonID),
				Description: "View person details",
			},
		),
	)
}
