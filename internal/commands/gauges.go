package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/completion"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

func NewGaugesCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "gauges",
		Short: "Manage project gauges",
		Long:  "List gauges across projects and manage gauge needles for a project.",
		Annotations: map[string]string{
			"agent_notes": "Gauges are account-wide to list, but needle operations are project-scoped. Use --in when creating or toggling gauges.",
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID or name (alias for --project)")

	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("project", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("in", completer.ProjectNameCompletion())

	cmd.AddCommand(
		newGaugesListCmd(),
		newGaugesNeedlesCmd(&project),
		newGaugesShowCmd(),
		newGaugesCreateCmd(&project),
		newGaugesUpdateCmd(),
		newGaugesDeleteCmd(),
		newGaugesEnableCmd(&project),
		newGaugesDisableCmd(&project),
	)

	return cmd
}

func newGaugesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List gauges",
		Long:  "List gauges across all projects visible to the current user.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			gauges, err := app.Account().Gauges().List(cmd.Context())
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(gauges,
				output.WithSummary(fmt.Sprintf("%d gauges", len(gauges))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "needles",
						Cmd:         "basecamp gauges needles --in <project>",
						Description: "List gauge needles for a project",
					},
				),
			)
		},
	}
}

func newGaugesNeedlesCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "needles",
		Short: "List gauge needles",
		Long:  "List gauge needles for a project, ordered newest first.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			projectID, err := resolveGaugeProjectID(cmd, app, *project)
			if err != nil {
				return err
			}

			needles, err := app.Account().Gauges().ListNeedles(cmd.Context(), projectID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(needles,
				output.WithSummary(fmt.Sprintf("%d gauge needles", len(needles))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp gauges show <needle-id>",
						Description: "Show a gauge needle",
					},
					output.Breadcrumb{
						Action:      "create",
						Cmd:         "basecamp gauges create --position <0-100> --in <project>",
						Description: "Create a gauge needle",
					},
				),
			)
		},
	}
}

func newGaugesShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <needle-id|url>",
		Short: "Show a gauge needle",
		Long:  "Show details for a single gauge needle.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			needleID, err := strconv.ParseInt(extractID(args[0]), 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid gauge needle ID")
			}

			needle, err := app.Account().Gauges().GetNeedle(cmd.Context(), needleID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(needle,
				output.WithSummary(fmt.Sprintf("Gauge needle #%d", needleID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("basecamp gauges update %d --description <text>", needleID),
						Description: "Update this gauge needle",
					},
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("basecamp gauges delete %d", needleID),
						Description: "Delete this gauge needle",
					},
				),
			)
		},
	}
}

func newGaugesCreateCmd(project *string) *cobra.Command {
	var position int
	var color string
	var description string
	var notify string
	var subscriptions []string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a gauge needle",
		Long:  "Create a gauge needle (progress update) for a project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			if position < 0 || position > 100 {
				return output.ErrUsage("--position must be between 0 and 100")
			}
			if err := validateGaugeColor(color); err != nil {
				return err
			}
			if err := validateGaugeNotify(notify); err != nil {
				return err
			}

			projectID, err := resolveGaugeProjectID(cmd, app, *project)
			if err != nil {
				return err
			}

			html, mentionNotice, err := gaugeDescriptionHTML(cmd, app, description)
			if err != nil {
				return err
			}

			peopleIDs, err := resolveGaugeSubscriptions(cmd, app, subscriptions, notify)
			if err != nil {
				return err
			}
			if len(peopleIDs) > 0 && notify == "" {
				notify = "custom"
			}

			needle, err := app.Account().Gauges().CreateNeedle(cmd.Context(), projectID, &basecamp.CreateGaugeNeedleRequest{
				Position:      int32(position),
				Color:         color,
				Description:   html,
				Notify:        notify,
				Subscriptions: peopleIDs,
			})
			if err != nil {
				return convertSDKError(err)
			}

			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("Created gauge needle at %d", position)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp gauges show %d", needle.ID),
						Description: "Show this gauge needle",
					},
				),
			}
			if mentionNotice != "" {
				respOpts = append(respOpts, output.WithDiagnostic(mentionNotice))
			}

			return app.OK(needle, respOpts...)
		},
	}

	cmd.Flags().IntVar(&position, "position", -1, "Needle position from 0 to 100")
	cmd.Flags().StringVar(&color, "color", "", "Status color (green, yellow, or red)")
	cmd.Flags().StringVar(&description, "description", "", "Gauge description in Markdown")
	cmd.Flags().StringVar(&notify, "notify", "", "Who to notify (everyone, working_on, or custom)")
	cmd.Flags().StringSliceVar(&subscriptions, "subscribe", nil, "People to notify when --notify custom")

	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("subscribe", completer.PeopleNameCompletion())

	return cmd
}

func newGaugesUpdateCmd() *cobra.Command {
	var description string

	cmd := &cobra.Command{
		Use:   "update <needle-id|url>",
		Short: "Update a gauge needle",
		Long:  "Update the description of a gauge needle.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if description == "" {
				return missingArg(cmd, "--description")
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			needleID, err := strconv.ParseInt(extractID(args[0]), 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid gauge needle ID")
			}

			html, mentionNotice, err := gaugeDescriptionHTML(cmd, app, description)
			if err != nil {
				return err
			}

			needle, err := app.Account().Gauges().UpdateNeedle(cmd.Context(), needleID, &basecamp.UpdateGaugeNeedleRequest{
				Description: html,
			})
			if err != nil {
				return convertSDKError(err)
			}

			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("Updated gauge needle #%d", needleID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp gauges show %d", needleID),
						Description: "Show this gauge needle",
					},
				),
			}
			if mentionNotice != "" {
				respOpts = append(respOpts, output.WithDiagnostic(mentionNotice))
			}

			return app.OK(needle, respOpts...)
		},
	}

	cmd.Flags().StringVar(&description, "description", "", "Gauge description in Markdown")

	return cmd
}

func newGaugesDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <needle-id|url>",
		Short: "Delete a gauge needle",
		Long:  "Delete a gauge needle.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			needleID, err := strconv.ParseInt(extractID(args[0]), 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid gauge needle ID")
			}

			if err := app.Account().Gauges().DestroyNeedle(cmd.Context(), needleID); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"deleted": true},
				output.WithSummary(fmt.Sprintf("Deleted gauge needle #%d", needleID)),
			)
		},
	}
}

func newGaugesEnableCmd(project *string) *cobra.Command {
	return newGaugesToggleCmd("enable", true, project)
}

func newGaugesDisableCmd(project *string) *cobra.Command {
	return newGaugesToggleCmd("disable", false, project)
}

func newGaugesToggleCmd(use string, enabled bool, project *string) *cobra.Command {
	summaryVerb := "Enabled"
	if !enabled {
		summaryVerb = "Disabled"
	}

	return &cobra.Command{
		Use:   use,
		Short: strings.ToLower(summaryVerb) + " a project gauge",
		Long:  summaryVerb + " the gauge for a project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			projectID, err := resolveGaugeProjectID(cmd, app, *project)
			if err != nil {
				return err
			}

			if err := app.Account().Gauges().Toggle(cmd.Context(), projectID, enabled); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"enabled": enabled},
				output.WithSummary(fmt.Sprintf("%s gauge for project #%d", summaryVerb, projectID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "needles",
						Cmd:         "basecamp gauges needles --in <project>",
						Description: "List gauge needles",
					},
				),
			)
		},
	}
}

func resolveGaugeProjectID(cmd *cobra.Command, app *appctx.App, project string) (int64, error) {
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return 0, err
		}
		projectID = app.Config.ProjectID
	}

	resolvedID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(resolvedID, 10, 64)
	if err != nil {
		return 0, output.ErrUsage("Invalid project ID")
	}
	return value, nil
}

func validateGaugeColor(color string) error {
	switch color {
	case "", "green", "yellow", "red":
		return nil
	default:
		return output.ErrUsage("--color must be green, yellow, or red")
	}
}

func validateGaugeNotify(notify string) error {
	switch notify {
	case "", "everyone", "working_on", "custom":
		return nil
	default:
		return output.ErrUsage("--notify must be everyone, working_on, or custom")
	}
}

func resolveGaugeSubscriptions(cmd *cobra.Command, app *appctx.App, subscriptions []string, notify string) ([]int64, error) {
	if len(subscriptions) == 0 {
		return nil, nil
	}
	if notify != "" && notify != "custom" {
		return nil, output.ErrUsage("--subscribe requires --notify custom")
	}

	ids := make([]int64, 0, len(subscriptions))
	for _, person := range subscriptions {
		id, _, err := app.Names.ResolvePerson(cmd.Context(), person)
		if err != nil {
			return nil, err
		}
		value, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, output.ErrUsage("Invalid person ID")
		}
		ids = append(ids, value)
	}
	return ids, nil
}

func gaugeDescriptionHTML(cmd *cobra.Command, app *appctx.App, description string) (string, string, error) {
	if description == "" {
		return "", "", nil
	}

	html := richtext.MarkdownToHTML(description)
	var err error
	html, err = resolveLocalImages(cmd, app, html)
	if err != nil {
		return "", "", err
	}
	mentionResult, err := resolveMentions(cmd.Context(), app.Names, html)
	if err != nil {
		return "", "", err
	}
	return mentionResult.HTML, unresolvedMentionWarning(mentionResult.Unresolved), nil
}
