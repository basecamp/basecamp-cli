package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// NewToolsCmd creates the tools command for managing project dock tools.
func NewToolsCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "tools",
		Short: "Manage project dock tools",
		Long: `Manage project dock tools (Campfire, Schedule, Docs & Files, etc.).

Every project has a "dock" with tools like Message Board, To-dos, Docs & Files,
Campfire, Schedule, etc. Tool IDs can be found in the project's dock array
(see 'bcq projects show <id>').

Tools can be created by cloning existing ones (e.g., create a second Campfire).
Disabling a tool hides it from the dock but preserves its content.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return output.ErrUsageHint("Action required", "Run: bcq tools --help")
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	cmd.AddCommand(
		newToolsShowCmd(&project),
		newToolsCreateCmd(&project),
		newToolsUpdateCmd(&project),
		newToolsTrashCmd(&project),
		newToolsEnableCmd(&project),
		newToolsDisableCmd(&project),
		newToolsRepositionCmd(&project),
	)

	return cmd
}

func newToolsShowCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show tool details",
		Long:  "Display detailed information about a dock tool.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			toolID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid tool ID")
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			tool, err := app.Account().Tools().Get(cmd.Context(), bucketID, toolID)
			if err != nil {
				return convertSDKError(err)
			}

			posStr := "disabled"
			if tool.Position != nil {
				posStr = fmt.Sprintf("%d", *tool.Position)
			}
			summary := fmt.Sprintf("%s (%s) at position %s", tool.Title, tool.Name, posStr)

			return app.OK(tool,
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "rename",
						Cmd:         fmt.Sprintf("bcq tools update %d --title \"New Name\" --in %s", toolID, resolvedProjectID),
						Description: "Rename tool",
					},
					output.Breadcrumb{
						Action:      "reposition",
						Cmd:         fmt.Sprintf("bcq tools reposition %d --position 1 --in %s", toolID, resolvedProjectID),
						Description: "Move tool",
					},
					output.Breadcrumb{
						Action:      "project",
						Cmd:         fmt.Sprintf("bcq projects show %s", resolvedProjectID),
						Description: "View project",
					},
				),
			)
		},
	}
}

func newToolsCreateCmd(project *string) *cobra.Command {
	var sourceID string
	var title string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new dock tool by cloning",
		Long: `Create a new dock tool by cloning an existing one.

For example, clone a Campfire to create a second chat room in the same project.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if sourceID == "" {
				return output.ErrUsage("--source is required (ID of tool to clone)")
			}

			sourceToolID, err := strconv.ParseInt(sourceID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid source tool ID")
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			created, err := app.Account().Tools().Create(cmd.Context(), bucketID, sourceToolID)
			if err != nil {
				return convertSDKError(err)
			}

			// Rename the tool if a title was provided
			if title != "" {
				created, err = app.Account().Tools().Update(cmd.Context(), bucketID, created.ID, title)
				if err != nil {
					return convertSDKError(err)
				}
			}

			return app.OK(created,
				output.WithSummary(fmt.Sprintf("Created: %s", created.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "tool",
						Cmd:         fmt.Sprintf("bcq tools show %d --in %s", created.ID, resolvedProjectID),
						Description: "View tool",
					},
					output.Breadcrumb{
						Action:      "project",
						Cmd:         fmt.Sprintf("bcq projects show %s", resolvedProjectID),
						Description: "View project",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&sourceID, "source", "s", "", "Source tool ID to clone (required)")
	cmd.Flags().StringVar(&sourceID, "clone", "", "Source tool ID (alias for --source)")
	cmd.Flags().StringVarP(&title, "title", "t", "", "Name for the new tool (optional)")
	_ = cmd.MarkFlagRequired("source")

	return cmd
}

func newToolsUpdateCmd(project *string) *cobra.Command {
	var title string

	cmd := &cobra.Command{
		Use:     "update <id>",
		Aliases: []string{"rename"},
		Short:   "Rename a dock tool",
		Long:    "Update a dock tool's title.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			toolID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid tool ID")
			}

			if title == "" {
				return output.ErrUsage("--title is required")
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			tool, err := app.Account().Tools().Update(cmd.Context(), bucketID, toolID, title)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(tool,
				output.WithSummary(fmt.Sprintf("Renamed to: %s", tool.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "tool",
						Cmd:         fmt.Sprintf("bcq tools show %d --in %s", toolID, resolvedProjectID),
						Description: "View tool",
					},
					output.Breadcrumb{
						Action:      "project",
						Cmd:         fmt.Sprintf("bcq projects show %s", resolvedProjectID),
						Description: "View project",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "New title (required)")
	_ = cmd.MarkFlagRequired("title")

	return cmd
}

func newToolsTrashCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "trash <id>",
		Aliases: []string{"delete"},
		Short:   "Permanently trash a dock tool",
		Long: `Permanently trash a dock tool.

WARNING: This permanently removes the tool and all its content.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			toolID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid tool ID")
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			err = app.Account().Tools().Delete(cmd.Context(), bucketID, toolID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"trashed": true},
				output.WithSummary(fmt.Sprintf("Tool %d trashed", toolID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "project",
						Cmd:         fmt.Sprintf("bcq projects show %s", resolvedProjectID),
						Description: "View project",
					},
				),
			)
		},
	}

	return cmd
}

func newToolsEnableCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "enable <id>",
		Short: "Enable a tool in the dock",
		Long:  "Enable a tool to make it visible in the project dock.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			toolID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid tool ID")
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			err = app.Account().Tools().Enable(cmd.Context(), bucketID, toolID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"enabled": true},
				output.WithSummary(fmt.Sprintf("Tool %d enabled in dock", toolID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "tool",
						Cmd:         fmt.Sprintf("bcq tools show %d --in %s", toolID, resolvedProjectID),
						Description: "View tool",
					},
				),
			)
		},
	}
}

func newToolsDisableCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "disable <id>",
		Short: "Disable a tool (hide from dock)",
		Long: `Disable a tool to hide it from the project dock.

The tool is not deleted - just hidden. Use 'bcq tools enable' to restore.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			toolID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid tool ID")
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			err = app.Account().Tools().Disable(cmd.Context(), bucketID, toolID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"disabled": true},
				output.WithSummary(fmt.Sprintf("Tool %d disabled (hidden from dock)", toolID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "enable",
						Cmd:         fmt.Sprintf("bcq tools enable %d --in %s", toolID, resolvedProjectID),
						Description: "Re-enable tool",
					},
				),
			)
		},
	}
}

func newToolsRepositionCmd(project *string) *cobra.Command {
	var position int

	cmd := &cobra.Command{
		Use:     "reposition <id>",
		Aliases: []string{"move"},
		Short:   "Change a tool's position in the dock",
		Long:    "Move a tool to a different position in the project dock.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			toolID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid tool ID")
			}

			if position == 0 {
				return output.ErrUsage("--position is required (1-based)")
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			err = app.Account().Tools().Reposition(cmd.Context(), bucketID, toolID, position)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"repositioned": true, "position": position},
				output.WithSummary(fmt.Sprintf("Tool %d moved to position %d", toolID, position)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "tool",
						Cmd:         fmt.Sprintf("bcq tools show %d --in %s", toolID, resolvedProjectID),
						Description: "View tool",
					},
				),
			)
		},
	}

	cmd.Flags().IntVar(&position, "position", 0, "New position, 1-based (required)")
	cmd.Flags().IntVar(&position, "pos", 0, "New position (alias)")
	_ = cmd.MarkFlagRequired("position")

	return cmd
}
