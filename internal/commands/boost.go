package commands

import (
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewBoostsCmd creates the boost command for managing boosts.
func NewBoostsCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:     "boost [action]",
		Aliases: []string{"boosts"},
		Short:   "Manage boosts (reactions)",
		Long: `Manage boosts on items.

Boosts are tiny messages to show your support — a short note (16
characters max) or emoji.

Use 'basecamp boost list <id>' to see boosts on an item.
Use 'basecamp boost show <boost-id>' to view a specific boost.
Use 'basecamp boost create <id> "content"' to boost an item.
Use 'basecamp boost delete <boost-id>' to remove a boost.

Tip: In the TUI, press 'b' on any item to boost interactively.`,
		Annotations: map[string]string{"agent_notes": "Boosts are tiny messages of support (16 chars max), not just emoji\nIn TUI mode, press 'b' on any item to boost interactively"},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	cmd.AddCommand(
		newBoostListCmd(&project),
		newBoostShowCmd(&project),
		newBoostCreateCmd(&project),
		newBoostDeleteCmd(),
	)

	return cmd
}

func newBoostListCmd(project *string) *cobra.Command {
	var eventID string
	var allProjects bool

	cmd := &cobra.Command{
		Use:   "list [id|url]",
		Short: "List boosts on an item, or across every project",
		Long: `List boosts on an item.

You can pass either an ID or a Basecamp URL:
  basecamp boost list 789 --project my-project
  basecamp boost list https://3.basecamp.com/123/buckets/456/todos/789

Use --event to list boosts on a specific event within the item.

Without an ID, boosts from every accessible project are listed, newest
first — the first page of the feed:
  basecamp boost list
  basecamp boost list --all-projects`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			recording := ""
			if len(args) > 0 {
				recording = args[0]
			}
			return runBoostList(cmd, app, recording, *project, eventID, allProjects)
		},
	}

	cmd.Flags().StringVar(&eventID, "event", "", "Event ID (for event-specific boosts)")
	cmd.Flags().BoolVar(&allProjects, "all-projects", false, "List boosts across every project")

	return cmd
}

func runBoostList(cmd *cobra.Command, app *appctx.App, recording, project, eventID string, allProjects bool) error {
	// Boosts hang off a single recording, so a project cannot scope this
	// listing on its own — only an item ID can. Without one, the account-wide
	// feed answers instead, and a configured project is ignored rather than
	// turned into an error, since it could never have produced a listing here.
	explicitProject := project
	if explicitProject == "" {
		explicitProject = app.Flags.Project
	}

	if recording == "" {
		switch {
		case explicitProject != "" && allProjects:
			return output.ErrUsageHint("Cannot combine --all-projects with --project",
				"--all-projects lists boosts from every project; drop --project, or pass an item ID to list one item's boosts")
		case explicitProject != "":
			return output.ErrUsageHint("Boosts belong to an item, so --project alone cannot list them",
				fmt.Sprintf("Pass the item's ID or URL: basecamp boost list <id> --project %s", explicitProject))
		}
		return runBoostListAccountWide(cmd, app, eventID)
	}

	if allProjects {
		return output.ErrUsageHint("Cannot combine --all-projects with an item ID",
			"Drop the ID to list boosts across every project, or drop --all-projects to list that item's boosts")
	}

	recordingID, urlProjectID := extractWithProject(recording)

	projectID := project
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

	recordingIDInt, err := strconv.ParseInt(recordingID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid ID")
	}

	if eventID != "" {
		eventIDInt, err := strconv.ParseInt(eventID, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid event ID")
		}

		result, err := app.Account().Boosts().ListEvent(cmd.Context(), recordingIDInt, eventIDInt, nil)
		if err != nil {
			return convertSDKError(err)
		}

		summary := fmt.Sprintf("%d boosts on event", len(result.Boosts))

		return app.OK(result.Boosts,
			output.WithSummary(summary),
			output.WithBreadcrumbs(
				output.Breadcrumb{
					Action:      "create",
					Cmd:         fmt.Sprintf("basecamp boost create %s \"content\" --event %s --project %s", recordingID, eventID, resolvedProjectID),
					Description: "Boost this event",
				},
			),
		)
	}

	result, err := app.Account().Boosts().ListRecording(cmd.Context(), recordingIDInt, nil)
	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("%d boosts", len(result.Boosts))

	return app.OK(result.Boosts,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("basecamp boost create %s \"content\" --project %s", recordingID, resolvedProjectID),
				Description: "Boost this item",
			},
		),
	)
}

// runBoostListAccountWide lists boosts across every accessible project. The
// feed takes a page number and boost list has no paging flags to map onto it,
// so it stays on the first page rather than growing a parallel flag surface.
func runBoostListAccountWide(cmd *cobra.Command, app *appctx.App, eventID string) error {
	if err := rejectAccountWideTodolist(app, "boost"); err != nil {
		return err
	}
	if eventID != "" {
		return output.ErrUsageHint("--event names an event inside one item, which the account-wide feed has no equivalent for",
			"Pass the item's ID alongside --event, or drop --event to list boosts across every project")
	}

	result, err := app.Account().Everything().Boosts(cmd.Context(), 1)
	if err != nil {
		return convertSDKError(err)
	}

	// The boosted recording is nested, so every consumer but --json and
	// --agent reads the flat rows: the generic renderer skips nested maps,
	// which would drop the project and the item title that make a boost row
	// mean anything.
	respOpts := accountWideRespOpts(len(result.Boosts), "boost", "boosts", result.Meta, "", false)
	respOpts = append(respOpts, output.WithDisplayData(flattenAccountWideBoosts(result.Boosts)))
	respOpts = append(respOpts, output.WithBreadcrumbs(
		output.Breadcrumb{
			Action:      "show",
			Cmd:         "basecamp boost show <boost-id>",
			Description: "Show a boost",
		},
	))

	return app.OK(result.Boosts, respOpts...)
}

// flattenAccountWideBoosts turns the account-wide feed into flat rows for the
// styled renderer: each boost nests the recording it sits on, which renders as
// an unreadable cell. Machine formats keep the nested payload. Booster and
// Recording are both optional, so every row carries the same keys whether or
// not the feed populated them.
func flattenAccountWideBoosts(boosts []basecamp.EverythingBoost) []map[string]any {
	rows := make([]map[string]any, 0, len(boosts))
	for _, boost := range boosts {
		booster := ""
		if boost.Booster != nil {
			booster = boost.Booster.Name
		}
		project, title, recordingType := "", "", ""
		if boost.Recording != nil {
			title = boost.Recording.Title
			recordingType = simplifyType(boost.Recording.Type)
			if boost.Recording.Bucket != nil {
				project = boost.Recording.Bucket.Name
			}
		}
		rows = append(rows, map[string]any{
			"id":      boost.ID,
			"project": project,
			"booster": booster,
			"content": boost.Content,
			"title":   title,
			"type":    recordingType,
		})
	}
	return rows
}

func newBoostShowCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <boost-id|url>",
		Short: "Show a specific boost",
		Long: `Show details of a specific boost.

You can pass either a boost ID or a Basecamp URL:
  basecamp boost show 789 --project my-project
  basecamp boost show https://3.basecamp.com/123/buckets/456/boosts/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			boostID, urlProjectID := extractWithProject(args[0])

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

			boostIDInt, err := strconv.ParseInt(boostID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid boost ID")
			}

			boost, err := app.Account().Boosts().Get(cmd.Context(), boostIDInt)
			if err != nil {
				return convertSDKError(err)
			}

			boosterName := ""
			if boost.Booster != nil {
				boosterName = boost.Booster.Name
			}
			summary := fmt.Sprintf("Boost #%s %s", boostID, boost.Content)
			if boosterName != "" {
				summary = fmt.Sprintf("%s by %s", summary, boosterName)
			}

			return app.OK(boost,
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("basecamp boost delete %s --project %s", boostID, resolvedProjectID),
						Description: "Delete boost",
					},
				),
			)
		},
	}
	return cmd
}

func newBoostCreateCmd(project *string) *cobra.Command {
	var eventID string

	cmd := &cobra.Command{
		Use:   "create <id|url> <content>",
		Short: "Boost an item",
		Long: `Boost an item with a short note or emoji.

You can pass either an ID or a Basecamp URL:
  basecamp boost create 789 "🎉" --project my-project
  basecamp boost create https://3.basecamp.com/123/buckets/456/todos/789 "👍"

Use --event to boost a specific event within the item.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			return runBoostCreate(cmd, app, args[0], *project, args[1], eventID)
		},
	}

	cmd.Flags().StringVar(&eventID, "event", "", "Event ID (for event-specific boosts)")

	return cmd
}

func runBoostCreate(cmd *cobra.Command, app *appctx.App, recording, project, content, eventID string) error {
	if n := utf8.RuneCountInString(content); n > 16 {
		return output.ErrUsage(fmt.Sprintf("Boost content too long (%d characters, max 16)", n))
	}

	recordingID, urlProjectID := extractWithProject(recording)

	projectID := project
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

	recordingIDInt, err := strconv.ParseInt(recordingID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid ID")
	}

	if eventID != "" {
		eventIDInt, err := strconv.ParseInt(eventID, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid event ID")
		}

		boost, err := app.Account().Boosts().CreateEvent(cmd.Context(), recordingIDInt, eventIDInt, content)
		if err != nil {
			return convertSDKError(err)
		}

		summary := fmt.Sprintf("Boosted event with %s", boost.Content)

		return app.OK(boost,
			output.WithSummary(summary),
			output.WithBreadcrumbs(
				output.Breadcrumb{
					Action:      "list",
					Cmd:         fmt.Sprintf("basecamp boost list %s --event %s --project %s", recordingID, eventID, resolvedProjectID),
					Description: "View boosts",
				},
			),
		)
	}

	boost, err := app.Account().Boosts().CreateRecording(cmd.Context(), recordingIDInt, content)
	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("Boosted with %s", boost.Content)

	return app.OK(boost,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp boost show %d --project %s", boost.ID, resolvedProjectID),
				Description: "View boost",
			},
			output.Breadcrumb{
				Action:      "list",
				Cmd:         fmt.Sprintf("basecamp boost list %s --project %s", recordingID, resolvedProjectID),
				Description: "View all boosts",
			},
		),
	)
}

func newBoostDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <boost-id|url>",
		Short: "Delete a boost",
		Long: `Delete a boost.

You can pass either a boost ID or a Basecamp URL:
  basecamp boost delete 789
  basecamp boost delete https://3.basecamp.com/123/buckets/456/boosts/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			boostID := extractID(args[0])

			boostIDInt, err := strconv.ParseInt(boostID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid boost ID")
			}

			err = app.Account().Boosts().Delete(cmd.Context(), boostIDInt)
			if err != nil {
				return convertSDKError(err)
			}

			result := map[string]any{
				"trashed": true,
				"id":      boostID,
			}

			return app.OK(result,
				output.WithSummary(fmt.Sprintf("Deleted boost #%s", boostID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp boost list <id> --project <project>",
						Description: "View boosts",
					},
				),
			)
		},
	}
	return cmd
}
