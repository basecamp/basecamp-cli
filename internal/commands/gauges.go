package commands

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewGaugesCmd creates the gauges command group.
func NewGaugesCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "gauges",
		Short: "Manage gauges",
		Long: `Manage gauges for tracking project progress.

A gauge shows the current state of a project via needles positioned on a
0–100 scale, each colored green, yellow, or red.`,
		Annotations: map[string]string{
			"agent_notes": "Gauges track project progress with colored needles (0-100 scale).\n" +
				"list is account-wide; --in <project> or --projects a,b (not both) narrows it, in the order named.\n" +
				"A configured default project never narrows list. needles/create/enable/disable are project-scoped (--in).\n" +
				"Colors: green, yellow, red. Notify: everyone, working_on, custom.",
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	cmd.AddCommand(
		newGaugesListCmd(&project),
		newGaugesNeedlesCmd(&project),
		newGaugesNeedleCmd(),
		newGaugesCreateCmd(&project),
		newGaugesUpdateCmd(),
		newGaugesDeleteCmd(),
		newGaugesEnableCmd(&project),
		newGaugesDisableCmd(&project),
	)

	return cmd
}

func newGaugesListCmd(project *string) *cobra.Command {
	var projects repeatedString

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List gauges across the account, or in chosen projects",
		Long: `List gauges across every project in the account, riskiest first.

--in narrows the list to one project, and --projects to several, given as
names or IDs, comma-separated or repeated; use one or the other. Gauges then
come back in the order the projects were named. A configured default project
does not narrow it.

  basecamp gauges list
  basecamp gauges list --in MyProject
  basecamp gauges list --projects 123,456`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Only a project named on this command narrows the report:
			// --project/--in is shared with the group, so ask whether it was
			// given rather than reading a default.
			single := cmd.Flags().Changed("project") || cmd.Flags().Changed("in")
			several := cmd.Flags().Changed("projects")
			if single && several {
				// The API answers in the order named, and the two flags'
				// relative order on the command line is not recoverable.
				return output.ErrUsage("Use --in for one project or --projects for several, not both")
			}
			named := []string(projects)
			if single {
				named = []string{*project}
			}

			bucketIDs, err := resolveGaugeProjects(cmd, app, named)
			if err != nil {
				return err
			}

			var opts *basecamp.GaugeListOptions
			if len(bucketIDs) > 0 {
				opts = &basecamp.GaugeListOptions{BucketIDs: bucketIDs}
			}

			result, err := app.Account().Gauges().List(cmd.Context(), opts)
			if err != nil {
				return convertSDKError(err)
			}
			gauges := result.Gauges

			summary := fmt.Sprintf("%d gauge(s)", len(gauges))
			if len(bucketIDs) > 0 {
				summary = fmt.Sprintf("%d gauge(s) in %d project(s)", len(gauges), len(bucketIDs))
			}

			return app.OK(gauges,
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "needles",
						Cmd:         "basecamp gauges needles --in <project>",
						Description: "View needles for a project",
					},
				),
			)
		},
	}

	// Not StringSliceVar: it drops an empty value, which would widen
	// `--projects ""` to the whole account instead of refusing it.
	cmd.Flags().Var(&projects, "projects", "Only these projects' gauges, in this order: names or IDs (comma-separated or repeated)")

	return cmd
}

// resolveGaugeProjects turns the projects named on gauges list into bucket
// IDs, keeping the order they were named in (the API answers in that order)
// and dropping repeats. None named means the whole account.
func resolveGaugeProjects(cmd *cobra.Command, app *appctx.App, named []string) ([]int64, error) {
	var ids []int64
	for _, raw := range named {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, output.ErrUsage("Empty project in --in or --projects")
		}
		resolved, _, err := app.Names.ResolveProject(cmd.Context(), value)
		if err != nil {
			return nil, err
		}
		id, err := strconv.ParseInt(resolved, 10, 64)
		if err != nil || id <= 0 {
			return nil, output.ErrUsage(fmt.Sprintf("Invalid project ID: %s", value))
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func newGaugesNeedlesCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "needles",
		Short: "List needles for a project's gauge",
		Long: `List all needles on a project's gauge.

  basecamp gauges needles --in MyProject`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			resolvedProjectID, err := resolveProjectID(cmd, app, *project)
			if err != nil {
				return err
			}

			projectID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			result, err := app.Account().Gauges().ListNeedles(cmd.Context(), projectID, nil)
			if err != nil {
				return convertSDKError(err)
			}
			needles := result.Needles

			return app.OK(needles,
				output.WithSummary(fmt.Sprintf("%d needle(s)", len(needles))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "create",
						Cmd:         fmt.Sprintf("basecamp gauges create --position 50 --in %s", resolvedProjectID),
						Description: "Add a needle",
					},
				),
			)
		},
	}
}

func newGaugesNeedleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "needle <id>",
		Short: "Show a needle",
		Long:  "Show details for a specific gauge needle.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			needleID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid needle ID")
			}

			needle, err := app.Account().Gauges().GetNeedle(cmd.Context(), needleID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(needle,
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("basecamp gauges update %d --description '...'", needleID),
						Description: "Update needle",
					},
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("basecamp gauges delete %d", needleID),
						Description: "Delete needle",
					},
				),
			)
		},
	}
}

func newGaugesCreateCmd(project *string) *cobra.Command {
	var position int32
	var color string
	var description string
	var notify string
	var subscriptions []int64

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a gauge needle",
		Long: `Create a new needle on a project's gauge.

  basecamp gauges create --position 75 --color green --in MyProject
  basecamp gauges create --position 50 --color yellow --description "Halfway there" --in MyProject`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("position") {
				return output.ErrUsage("--position is required")
			}
			if position < 0 || position > 100 {
				return output.ErrUsage("--position must be between 0 and 100")
			}

			// --notify custom without --subscriptions cannot succeed, and that
			// is knowable from the flags alone, so it is decided before the pipe
			// is drained rather than at request-building time.
			if notify == "custom" && len(subscriptions) == 0 {
				return output.ErrUsage("--subscriptions required when using --notify custom")
			}

			// Local validation, then "-", then account: a bad stdin gets the
			// stdin error rather than "--account is required".
			description, err := resolveContentValue(cmd, description, -1, "--description")
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			resolvedProjectID, err := resolveProjectID(cmd, app, *project)
			if err != nil {
				return err
			}

			projectID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			req := &basecamp.CreateGaugeNeedleRequest{
				Position: position,
			}
			if color != "" {
				req.Color = color
			}
			if description != "" {
				req.Description = description
			}
			if notify != "" {
				req.Notify = notify
				if notify == "custom" {
					req.Subscriptions = subscriptions
				}
			}

			needle, err := app.Account().Gauges().CreateNeedle(cmd.Context(), projectID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(needle,
				output.WithSummary(fmt.Sprintf("Created needle #%d at position %d", needle.ID, needle.Position)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "needles",
						Cmd:         fmt.Sprintf("basecamp gauges needles --in %s", resolvedProjectID),
						Description: "View all needles",
					},
				),
			)
		},
	}

	cmd.Flags().Int32Var(&position, "position", 0, "Position on gauge (0-100, required)")
	cmd.Flags().StringVar(&color, "color", "", "Needle color: green, yellow, or red")
	cmd.Flags().StringVar(&description, "description", "", "Description (rich text HTML); use - to read from stdin")
	cmd.Flags().StringVar(&notify, "notify", "", "Notification mode: everyone, working_on, or custom")
	cmd.Flags().Int64SliceVar(&subscriptions, "subscriptions", nil, "Person IDs to notify (used with --notify custom)")

	allowDash(cmd, "flag:description")

	return cmd
}

func newGaugesUpdateCmd() *cobra.Command {
	var description string

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a gauge needle",
		Long: `Update a needle's description.

  basecamp gauges update 12345 --description "Updated status"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("description") {
				return output.ErrUsage("No changes specified (use --description)")
			}

			needleID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid needle ID")
			}

			// Syntactic checks first, then "-", then account and network.
			description, err := resolveContentValue(cmd, description, -1, "--description")
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			req := &basecamp.UpdateGaugeNeedleRequest{
				Description: basecamp.Ptr(description),
			}

			needle, err := app.Account().Gauges().UpdateNeedle(cmd.Context(), needleID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(needle,
				output.WithSummary(fmt.Sprintf("Updated needle #%d", needleID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "view",
						Cmd:         fmt.Sprintf("basecamp gauges needle %d", needleID),
						Description: "View needle",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&description, "description", "", "New description (rich text HTML); use - to read from stdin")

	allowDash(cmd, "flag:description")

	return cmd
}

func newGaugesDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a gauge needle",
		Long:  "Delete a needle from a project's gauge.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			needleID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid needle ID")
			}

			err = app.Account().Gauges().DestroyNeedle(cmd.Context(), needleID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"deleted": true, "id": needleID},
				output.WithSummary(fmt.Sprintf("Deleted needle #%d", needleID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp gauges list",
						Description: "View all gauges",
					},
				),
			)
		},
	}
}

func newGaugesEnableCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "enable",
		Short: "Enable gauge on a project",
		Long: `Enable the gauge tool on a project.

  basecamp gauges enable --in MyProject`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			resolvedProjectID, err := resolveProjectID(cmd, app, *project)
			if err != nil {
				return err
			}

			projectID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			err = app.Account().Gauges().Toggle(cmd.Context(), projectID, true)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"enabled": true},
				output.WithSummary(fmt.Sprintf("Gauge enabled on project %s", resolvedProjectID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "needles",
						Cmd:         fmt.Sprintf("basecamp gauges needles --in %s", resolvedProjectID),
						Description: "View needles",
					},
					output.Breadcrumb{
						Action:      "create",
						Cmd:         fmt.Sprintf("basecamp gauges create --position 50 --in %s", resolvedProjectID),
						Description: "Add a needle",
					},
				),
			)
		},
	}
}

func newGaugesDisableCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Disable gauge on a project",
		Long: `Disable the gauge tool on a project.

The gauge is not deleted — just hidden. Use 'basecamp gauges enable' to restore.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			resolvedProjectID, err := resolveProjectID(cmd, app, *project)
			if err != nil {
				return err
			}

			projectID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			err = app.Account().Gauges().Toggle(cmd.Context(), projectID, false)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"enabled": false},
				output.WithSummary(fmt.Sprintf("Gauge disabled on project %s", resolvedProjectID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "enable",
						Cmd:         fmt.Sprintf("basecamp gauges enable --in %s", resolvedProjectID),
						Description: "Re-enable gauge",
					},
				),
			)
		},
	}
}
