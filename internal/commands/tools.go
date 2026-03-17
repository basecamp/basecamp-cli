package commands

import (
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewToolsCmd creates the tools command for managing project dock tools.
func NewToolsCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "tools [action]",
		Short: "Manage project dock tools",
		Long: `Manage project dock tools (Chat, Schedule, Docs & Files, etc.).

Every project has a "dock" with tools like Message Board, To-dos, Docs & Files,
Chat, Schedule, etc. Tool IDs can be found in the project's dock array
(see 'basecamp projects show <id>').

Tools can be created by cloning existing ones (e.g., create a second Chat).
Disabling a tool hides it from the dock but preserves its content.`,
		Annotations: map[string]string{"agent_notes": "Dock tools are the sidebar navigation items in a project\nEnable/disable controls visibility without deleting\nEach tool has a type (e.g., Todoset, Schedule, MessageBoard, Vault, Chat::Campfire)"},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name (for breadcrumbs)")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID or name (alias for --project)")

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

// resolveToolsProject optionally resolves a project for breadcrumb display.
// Returns the resolved project ID string, or "" if no project was specified.
// If the user explicitly provided a project via flag (--in/--project) that
// cannot be resolved, the error is returned. Config defaults are best-effort:
// resolution failures are silently ignored since the user didn't ask for
// project context on this invocation.
func resolveToolsProject(cmd *cobra.Command, app *appctx.App, project string) (string, error) {
	explicit := project != ""

	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
		if projectID != "" {
			explicit = true
		}
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return "", nil
	}

	resolved, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		if explicit {
			return "", err
		}
		return "", nil
	}
	return resolved, nil
}

// toolBreadcrumbFlag returns " --in <id>" if projectID is non-empty, or "".
func toolBreadcrumbFlag(projectID string) string {
	if projectID == "" {
		return ""
	}
	return " --in " + projectID
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

			resolvedProjectID, err := resolveToolsProject(cmd, app, *project)
			if err != nil {
				return err
			}
			inFlag := toolBreadcrumbFlag(resolvedProjectID)

			tool, err := app.Account().Tools().Get(cmd.Context(), toolID)
			if err != nil {
				return convertSDKError(err)
			}

			posStr := "disabled"
			if tool.Position != nil {
				posStr = fmt.Sprintf("%d", *tool.Position)
			}
			summary := fmt.Sprintf("%s (%s) at position %s", tool.Title, tool.Name, posStr)

			crumbs := []output.Breadcrumb{
				{
					Action:      "rename",
					Cmd:         fmt.Sprintf("basecamp tools update %d \"New Name\"%s", toolID, inFlag),
					Description: "Rename tool",
				},
				{
					Action:      "reposition",
					Cmd:         fmt.Sprintf("basecamp tools reposition %d --position 1%s", toolID, inFlag),
					Description: "Move tool",
				},
			}
			if resolvedProjectID != "" {
				crumbs = append(crumbs, output.Breadcrumb{
					Action:      "project",
					Cmd:         fmt.Sprintf("basecamp projects show %s", resolvedProjectID),
					Description: "View project",
				})
			}

			return app.OK(tool,
				output.WithSummary(summary),
				output.WithBreadcrumbs(crumbs...),
			)
		},
	}
}

func newToolsCreateCmd(project *string) *cobra.Command {
	var sourceID string

	cmd := &cobra.Command{
		Use:   "create [title]",
		Short: "Create a new dock tool by cloning",
		Long: `Create a new dock tool by cloning an existing one.

For example, clone a Chat to create a second chat room in the same project.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sourceID == "" {
				return output.ErrUsage("--source or --clone is required (ID of tool to clone)")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			sourceToolID, err := strconv.ParseInt(sourceID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid source tool ID")
			}

			title := ""
			if len(args) > 0 {
				title = args[0]
			}

			if title != "" {
				if n := utf8.RuneCountInString(title); n > 64 {
					return output.ErrUsage(fmt.Sprintf("Tool name too long (%d characters, max 64)", n))
				}
			}

			resolvedProjectID, err := resolveToolsProject(cmd, app, *project)
			if err != nil {
				return err
			}
			inFlag := toolBreadcrumbFlag(resolvedProjectID)

			var cloneOpts *basecamp.CloneToolOptions
			if title != "" {
				cloneOpts = &basecamp.CloneToolOptions{Title: title}
			}

			created, err := app.Account().Tools().Create(cmd.Context(), sourceToolID, cloneOpts)
			if err != nil {
				return convertSDKError(err)
			}

			crumbs := []output.Breadcrumb{
				{
					Action:      "tool",
					Cmd:         fmt.Sprintf("basecamp tools show %d%s", created.ID, inFlag),
					Description: "View tool",
				},
			}
			if resolvedProjectID != "" {
				crumbs = append(crumbs, output.Breadcrumb{
					Action:      "project",
					Cmd:         fmt.Sprintf("basecamp projects show %s", resolvedProjectID),
					Description: "View project",
				})
			}

			return app.OK(created,
				output.WithSummary(fmt.Sprintf("Created: %s", created.Title)),
				output.WithBreadcrumbs(crumbs...),
			)
		},
	}

	cmd.Flags().StringVarP(&sourceID, "source", "s", "", "Source tool ID to clone (required)")
	cmd.Flags().StringVar(&sourceID, "clone", "", "Source tool ID (alias for --source)")

	return cmd
}

func newToolsUpdateCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "update <id> <title>",
		Aliases: []string{"rename"},
		Short:   "Rename a dock tool",
		Long:    "Update a dock tool's title.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with insufficient arguments
			if len(args) == 0 {
				return missingArg(cmd, "<id>")
			}
			if len(args) < 2 {
				return missingArg(cmd, "<title>")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			toolID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid tool ID")
			}

			title := args[1]

			if n := utf8.RuneCountInString(title); n > 64 {
				return output.ErrUsage(fmt.Sprintf("Tool name too long (%d characters, max 64)", n))
			}

			resolvedProjectID, err := resolveToolsProject(cmd, app, *project)
			if err != nil {
				return err
			}
			inFlag := toolBreadcrumbFlag(resolvedProjectID)

			tool, err := app.Account().Tools().Update(cmd.Context(), toolID, title)
			if err != nil {
				return convertSDKError(err)
			}

			crumbs := []output.Breadcrumb{
				{
					Action:      "tool",
					Cmd:         fmt.Sprintf("basecamp tools show %d%s", toolID, inFlag),
					Description: "View tool",
				},
			}
			if resolvedProjectID != "" {
				crumbs = append(crumbs, output.Breadcrumb{
					Action:      "project",
					Cmd:         fmt.Sprintf("basecamp projects show %s", resolvedProjectID),
					Description: "View project",
				})
			}

			return app.OK(tool,
				output.WithSummary(fmt.Sprintf("Renamed to: %s", tool.Title)),
				output.WithBreadcrumbs(crumbs...),
			)
		},
	}

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

			resolvedProjectID, err := resolveToolsProject(cmd, app, *project)
			if err != nil {
				return err
			}

			err = app.Account().Tools().Delete(cmd.Context(), toolID)
			if err != nil {
				return convertSDKError(err)
			}

			var crumbs []output.Breadcrumb
			if resolvedProjectID != "" {
				crumbs = append(crumbs, output.Breadcrumb{
					Action:      "project",
					Cmd:         fmt.Sprintf("basecamp projects show %s", resolvedProjectID),
					Description: "View project",
				})
			}

			return app.OK(map[string]any{"trashed": true},
				output.WithSummary(fmt.Sprintf("Tool %d trashed", toolID)),
				output.WithBreadcrumbs(crumbs...),
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

			resolvedProjectID, err := resolveToolsProject(cmd, app, *project)
			if err != nil {
				return err
			}
			inFlag := toolBreadcrumbFlag(resolvedProjectID)

			err = app.Account().Tools().Enable(cmd.Context(), toolID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"enabled": true},
				output.WithSummary(fmt.Sprintf("Tool %d enabled in dock", toolID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "tool",
						Cmd:         fmt.Sprintf("basecamp tools show %d%s", toolID, inFlag),
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

The tool is not deleted - just hidden. Use 'basecamp tools enable' to restore.`,
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

			resolvedProjectID, err := resolveToolsProject(cmd, app, *project)
			if err != nil {
				return err
			}
			inFlag := toolBreadcrumbFlag(resolvedProjectID)

			err = app.Account().Tools().Disable(cmd.Context(), toolID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"disabled": true},
				output.WithSummary(fmt.Sprintf("Tool %d disabled (hidden from dock)", toolID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "enable",
						Cmd:         fmt.Sprintf("basecamp tools enable %d%s", toolID, inFlag),
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
			if position == 0 {
				return output.ErrUsage("--position is required (1-based)")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			toolID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid tool ID")
			}

			resolvedProjectID, err := resolveToolsProject(cmd, app, *project)
			if err != nil {
				return err
			}
			inFlag := toolBreadcrumbFlag(resolvedProjectID)

			err = app.Account().Tools().Reposition(cmd.Context(), toolID, position)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"repositioned": true, "position": position},
				output.WithSummary(fmt.Sprintf("Tool %d moved to position %d", toolID, position)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "tool",
						Cmd:         fmt.Sprintf("basecamp tools show %d%s", toolID, inFlag),
						Description: "View tool",
					},
				),
			)
		},
	}

	cmd.Flags().IntVar(&position, "position", 0, "New position, 1-based (required)")
	cmd.Flags().IntVar(&position, "pos", 0, "New position (alias)")

	return cmd
}
