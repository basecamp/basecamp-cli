package commands

import (
	"fmt"
	"strconv"

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
				"list is account-wide. needles/create/enable/disable are project-scoped (--in).\n" +
				"Colors: green, yellow, red. Notify: everyone, working_on, custom.",
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	cmd.AddCommand(
		newGaugesListCmd(),
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

func newGaugesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all gauges across the account",
		Long:  "List all gauges across every project in the account.",
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
				output.WithSummary(fmt.Sprintf("%d gauge(s)", len(gauges))),
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

			needles, err := app.Account().Gauges().ListNeedles(cmd.Context(), projectID)
			if err != nil {
				return convertSDKError(err)
			}

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

			if !cmd.Flags().Changed("position") {
				return output.ErrUsage("--position is required")
			}
			if position < 0 || position > 100 {
				return output.ErrUsage("--position must be between 0 and 100")
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
					if len(subscriptions) == 0 {
						return output.ErrUsage("--subscriptions required when using --notify custom")
					}
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
	cmd.Flags().StringVar(&description, "description", "", "Description (rich text HTML)")
	cmd.Flags().StringVar(&notify, "notify", "", "Notification mode: everyone, working_on, or custom")
	cmd.Flags().Int64SliceVar(&subscriptions, "subscriptions", nil, "Person IDs to notify (used with --notify custom)")

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
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			needleID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid needle ID")
			}

			if !cmd.Flags().Changed("description") {
				return output.ErrUsage("No changes specified (use --description)")
			}

			req := &basecamp.UpdateGaugeNeedleRequest{
				Description: description,
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

	cmd.Flags().StringVar(&description, "description", "", "New description (rich text HTML)")

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
