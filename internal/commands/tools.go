package commands

import (
	"encoding/json"
	"fmt"

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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			toolID := args[0]

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			path := fmt.Sprintf("/buckets/%s/dock/tools/%s.json", resolvedProjectID, toolID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var tool struct {
				Title    string `json:"title"`
				Type     string `json:"type"`
				Position int    `json:"position"`
			}
			if err := json.Unmarshal(resp.Data, &tool); err != nil {
				return fmt.Errorf("failed to parse tool: %w", err)
			}

			summary := fmt.Sprintf("%s (%s) at position %d", tool.Title, tool.Type, tool.Position)

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "rename",
						Cmd:         fmt.Sprintf("bcq tools update %s --title \"New Name\" --in %s", toolID, resolvedProjectID),
						Description: "Rename tool",
					},
					output.Breadcrumb{
						Action:      "reposition",
						Cmd:         fmt.Sprintf("bcq tools reposition %s --position 1 --in %s", toolID, resolvedProjectID),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			if sourceID == "" {
				return output.ErrUsage("--source is required (ID of tool to clone)")
			}
			if title == "" {
				return output.ErrUsage("--title is required")
			}

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			body := map[string]any{
				"source_recording_id": sourceID,
				"title":               title,
			}

			path := fmt.Sprintf("/buckets/%s/dock/tools.json", resolvedProjectID)
			resp, err := app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var created struct {
				ID    int64  `json:"id"`
				Title string `json:"title"`
			}
			if err := json.Unmarshal(resp.Data, &created); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}

			return app.Output.OK(json.RawMessage(resp.Data),
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
	cmd.Flags().StringVarP(&title, "title", "t", "", "Name for the new tool (required)")
	cmd.MarkFlagRequired("source")
	cmd.MarkFlagRequired("title")

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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			toolID := args[0]

			if title == "" {
				return output.ErrUsage("--title is required")
			}

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			body := map[string]string{"title": title}

			path := fmt.Sprintf("/buckets/%s/dock/tools/%s.json", resolvedProjectID, toolID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var tool struct {
				Title string `json:"title"`
			}
			if err := json.Unmarshal(resp.Data, &tool); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Renamed to: %s", tool.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "tool",
						Cmd:         fmt.Sprintf("bcq tools show %s --in %s", toolID, resolvedProjectID),
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
	cmd.MarkFlagRequired("title")

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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			toolID := args[0]

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			path := fmt.Sprintf("/buckets/%s/dock/tools/%s.json", resolvedProjectID, toolID)
			_, err = app.API.Delete(cmd.Context(), path)
			if err != nil {
				return err
			}

			return app.Output.OK(map[string]any{"trashed": true},
				output.WithSummary(fmt.Sprintf("Tool %s trashed", toolID)),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			toolID := args[0]

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			path := fmt.Sprintf("/buckets/%s/recordings/%s/position.json", resolvedProjectID, toolID)
			_, err = app.API.Post(cmd.Context(), path, map[string]any{})
			if err != nil {
				return err
			}

			return app.Output.OK(map[string]any{"enabled": true},
				output.WithSummary(fmt.Sprintf("Tool %s enabled in dock", toolID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "tool",
						Cmd:         fmt.Sprintf("bcq tools show %s --in %s", toolID, resolvedProjectID),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			toolID := args[0]

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			path := fmt.Sprintf("/buckets/%s/recordings/%s/position.json", resolvedProjectID, toolID)
			_, err = app.API.Delete(cmd.Context(), path)
			if err != nil {
				return err
			}

			return app.Output.OK(map[string]any{"disabled": true},
				output.WithSummary(fmt.Sprintf("Tool %s disabled (hidden from dock)", toolID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "enable",
						Cmd:         fmt.Sprintf("bcq tools enable %s --in %s", toolID, resolvedProjectID),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			toolID := args[0]

			if position == 0 {
				return output.ErrUsage("--position is required (1-based)")
			}

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			body := map[string]int{"position": position}

			path := fmt.Sprintf("/buckets/%s/recordings/%s/position.json", resolvedProjectID, toolID)
			_, err = app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			return app.Output.OK(map[string]any{"repositioned": true, "position": position},
				output.WithSummary(fmt.Sprintf("Tool %s moved to position %d", toolID, position)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "tool",
						Cmd:         fmt.Sprintf("bcq tools show %s --in %s", toolID, resolvedProjectID),
						Description: "View tool",
					},
				),
			)
		},
	}

	cmd.Flags().IntVar(&position, "position", 0, "New position, 1-based (required)")
	cmd.Flags().IntVar(&position, "pos", 0, "New position (alias)")
	cmd.MarkFlagRequired("position")

	return cmd
}
