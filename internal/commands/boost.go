package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewBoostsCmd creates the boost command for managing emoji reactions.
func NewBoostsCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:     "boost [action]",
		Aliases: []string{"boosts"},
		Short:   "Manage boosts (reactions)",
		Long: `Manage boosts (emoji reactions) on recordings.

Use 'basecamp boost list <recording-id>' to see boosts on a recording.
Use 'basecamp boost show <boost-id>' to view a specific boost.
Use 'basecamp boost create <recording-id> "emoji"' to boost a recording.
Use 'basecamp boost delete <boost-id>' to remove a boost.`,
		Args: cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	cmd.AddCommand(
		newBoostListCmd(&project),
		newBoostShowCmd(&project),
		newBoostCreateCmd(&project),
		newBoostDeleteCmd(&project),
	)

	return cmd
}

func newBoostListCmd(project *string) *cobra.Command {
	var eventID string

	cmd := &cobra.Command{
		Use:   "list <recording-id|url>",
		Short: "List boosts on a recording",
		Long: `List boosts on a recording.

You can pass either a recording ID or a Basecamp URL:
  basecamp boost list 789 --project my-project
  basecamp boost list https://3.basecamp.com/123/buckets/456/todos/789

Use --event to list boosts on a specific event within the recording.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			return runBoostList(cmd, app, args[0], *project, eventID)
		},
	}

	cmd.Flags().StringVar(&eventID, "event", "", "Event ID (for event-specific boosts)")

	return cmd
}

func runBoostList(cmd *cobra.Command, app *appctx.App, recording, project, eventID string) error {
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

	bucketID, _ := strconv.ParseInt(resolvedProjectID, 10, 64)
	recordingIDInt, err := strconv.ParseInt(recordingID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid recording ID")
	}

	if eventID != "" {
		eventIDInt, err := strconv.ParseInt(eventID, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid event ID")
		}

		result, err := app.Account().Boosts().ListEvent(cmd.Context(), bucketID, recordingIDInt, eventIDInt)
		if err != nil {
			return convertSDKError(err)
		}

		summary := fmt.Sprintf("%d boosts on event", len(result.Boosts))

		return app.OK(result.Boosts,
			output.WithSummary(summary),
			output.WithBreadcrumbs(
				output.Breadcrumb{
					Action:      "create",
					Cmd:         fmt.Sprintf("basecamp boost create %s \"emoji\" --event %s --project %s", recordingID, eventID, resolvedProjectID),
					Description: "Boost this event",
				},
			),
		)
	}

	result, err := app.Account().Boosts().ListRecording(cmd.Context(), bucketID, recordingIDInt)
	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("%d boosts", len(result.Boosts))

	return app.OK(result.Boosts,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("basecamp boost create %s \"emoji\" --project %s", recordingID, resolvedProjectID),
				Description: "Boost this recording",
			},
		),
	)
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

			bucketID, _ := strconv.ParseInt(resolvedProjectID, 10, 64)
			boostIDInt, err := strconv.ParseInt(boostID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid boost ID")
			}

			boost, err := app.Account().Boosts().Get(cmd.Context(), bucketID, boostIDInt)
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
		Use:   "create <recording-id|url> <content>",
		Short: "Boost a recording",
		Long: `Boost a recording with an emoji reaction.

You can pass either a recording ID or a Basecamp URL:
  basecamp boost create 789 "🎉" --project my-project
  basecamp boost create https://3.basecamp.com/123/buckets/456/todos/789 "👍"

Use --event to boost a specific event within the recording.`,
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

	bucketID, _ := strconv.ParseInt(resolvedProjectID, 10, 64)
	recordingIDInt, err := strconv.ParseInt(recordingID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid recording ID")
	}

	if eventID != "" {
		eventIDInt, err := strconv.ParseInt(eventID, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid event ID")
		}

		boost, err := app.Account().Boosts().CreateEvent(cmd.Context(), bucketID, recordingIDInt, eventIDInt, content)
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

	boost, err := app.Account().Boosts().CreateRecording(cmd.Context(), bucketID, recordingIDInt, content)
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

func newBoostDeleteCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <boost-id|url>",
		Short: "Delete a boost",
		Long: `Delete a boost from a recording.

You can pass either a boost ID or a Basecamp URL:
  basecamp boost delete 789 --project my-project
  basecamp boost delete https://3.basecamp.com/123/buckets/456/boosts/789`,
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

			bucketID, _ := strconv.ParseInt(resolvedProjectID, 10, 64)
			boostIDInt, err := strconv.ParseInt(boostID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid boost ID")
			}

			err = app.Account().Boosts().Delete(cmd.Context(), bucketID, boostIDInt)
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
						Cmd:         "basecamp boost list <recording-id> --project <project>",
						Description: "View boosts",
					},
				),
			)
		},
	}
	return cmd
}

// NewBoostShortcutCmd creates the boost shortcut command for quickly boosting a recording.
func NewBoostShortcutCmd() *cobra.Command {
	var project string
	var recording string
	var eventID string

	cmd := &cobra.Command{
		Use:   "react <content>",
		Short: "Boost a recording",
		Long: `Boost a recording with an emoji reaction (shortcut for boost create).

Content as positional argument, --on for the recording:
  basecamp react "🎉" --on 789 --project my-project
  basecamp react "👍" --on https://3.basecamp.com/123/buckets/456/todos/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			content := args[0]

			if recording == "" {
				return output.ErrUsage("--on or --recording required")
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			return runBoostCreate(cmd, app, recording, project, content, eventID)
		},
	}

	cmd.Flags().StringVarP(&recording, "on", "r", "", "Recording ID or URL to boost")
	cmd.Flags().StringVar(&recording, "recording", "", "Recording ID or URL (alias for --on)")
	cmd.Flags().StringVar(&eventID, "event", "", "Event ID (for event-specific boosts)")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	return cmd
}
