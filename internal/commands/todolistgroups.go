package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// NewTodolistgroupsCmd creates the todolistgroups command group.
func NewTodolistgroupsCmd() *cobra.Command {
	var project string
	var todolist string

	cmd := &cobra.Command{
		Use:     "todolistgroups",
		Aliases: []string{"todolistgroup", "tlgroups", "tlgroup"},
		Short:   "Manage todolist groups",
		Long: `Manage todolist groups (folders for organizing todolists).

Todolist groups allow you to organize todolists into collapsible sections.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default to list when called without subcommand
			return runTodolistgroupsList(cmd, project, todolist)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVarP(&todolist, "list", "l", "", "Todolist ID")

	cmd.AddCommand(
		newTodolistgroupsListCmd(&project, &todolist),
		newTodolistgroupsShowCmd(&project),
		newTodolistgroupsCreateCmd(&project, &todolist),
		newTodolistgroupsUpdateCmd(&project),
		newTodolistgroupsPositionCmd(&project),
	)

	return cmd
}

func newTodolistgroupsListCmd(project, todolist *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List todolist groups",
		Long:  "List all groups in a todolist.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTodolistgroupsList(cmd, *project, *todolist)
		},
	}
}

func runTodolistgroupsList(cmd *cobra.Command, project, todolist string) error {
	app := appctx.FromContext(cmd.Context())
	if err := app.API.RequireAccount(); err != nil {
		return err
	}

	// Resolve project
	if project == "" {
		project = app.Flags.Project
	}
	if project == "" {
		project = app.Config.ProjectID
	}
	if project == "" {
		return output.ErrUsage("--project is required")
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), project)
	if err != nil {
		return err
	}

	// Resolve todolist - fall back to config
	if todolist == "" {
		todolist = app.Flags.Todolist
	}
	if todolist == "" {
		todolist = app.Config.TodolistID
	}
	if todolist == "" {
		return output.ErrUsage("--list is required")
	}

	resolvedTodolistID, _, err := app.Names.ResolveTodolist(cmd.Context(), todolist, resolvedProjectID)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/buckets/%s/todolists/%s/groups.json", resolvedProjectID, resolvedTodolistID)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var groups []any
	if err := json.Unmarshal(resp.Data, &groups); err != nil {
		return fmt.Errorf("failed to parse groups: %w", err)
	}

	return app.Output.OK(json.RawMessage(resp.Data),
		output.WithSummary(fmt.Sprintf("%d groups in todolist #%s", len(groups), todolist)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("bcq todolistgroups create \"name\" --list %s --in %s", todolist, resolvedProjectID),
				Description: "Create group",
			},
			output.Breadcrumb{
				Action:      "todolist",
				Cmd:         fmt.Sprintf("bcq todolists show %s --in %s", todolist, resolvedProjectID),
				Description: "View parent todolist",
			},
		),
	)
}

func newTodolistgroupsShowCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show todolist group details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			groupID := args[0]

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

			path := fmt.Sprintf("/buckets/%s/todolist_groups/%s.json", resolvedProjectID, groupID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var group struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(resp.Data, &group); err != nil {
				return fmt.Errorf("failed to parse group: %w", err)
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(group.Name),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("bcq todolistgroups update %s --name \"New Name\" --in %s", groupID, resolvedProjectID),
						Description: "Rename group",
					},
				),
			)
		},
	}
}

func newTodolistgroupsCreateCmd(project, todolist *string) *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a todolist group",
		Long:  "Create a new group in a todolist.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			if name == "" {
				return output.ErrUsage("--name is required")
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

			// Resolve todolist - fall back to config
			todolistID := *todolist
			if todolistID == "" {
				todolistID = app.Flags.Todolist
			}
			if todolistID == "" {
				todolistID = app.Config.TodolistID
			}
			if todolistID == "" {
				return output.ErrUsage("--list is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			resolvedTodolistID, _, err := app.Names.ResolveTodolist(cmd.Context(), todolistID, resolvedProjectID)
			if err != nil {
				return err
			}

			body := map[string]string{"name": name}

			path := fmt.Sprintf("/buckets/%s/todolists/%s/groups.json", resolvedProjectID, resolvedTodolistID)
			resp, err := app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var group struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(resp.Data, &group); err != nil {
				return fmt.Errorf("failed to parse group: %w", err)
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Created group: %s", group.Name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "group",
						Cmd:         fmt.Sprintf("bcq todolistgroups show %d --in %s", group.ID, resolvedProjectID),
						Description: "View group",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Group name (required)")
	cmd.MarkFlagRequired("name")

	return cmd
}

func newTodolistgroupsUpdateCmd(project *string) *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:     "update <id>",
		Aliases: []string{"rename"},
		Short:   "Update a todolist group",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			groupID := args[0]

			if name == "" {
				return output.ErrUsage("--name is required")
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

			body := map[string]string{"name": name}

			path := fmt.Sprintf("/buckets/%s/todolist_groups/%s.json", resolvedProjectID, groupID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var group struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(resp.Data, &group); err != nil {
				return fmt.Errorf("failed to parse group: %w", err)
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Renamed to: %s", group.Name)),
			)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "New name (required)")
	cmd.MarkFlagRequired("name")

	return cmd
}

func newTodolistgroupsPositionCmd(project *string) *cobra.Command {
	var position int

	cmd := &cobra.Command{
		Use:     "position <id>",
		Aliases: []string{"move"},
		Short:   "Change group position",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			groupID := args[0]

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

			path := fmt.Sprintf("/buckets/%s/recordings/%s/position.json", resolvedProjectID, groupID)
			_, err = app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			return app.Output.OK(map[string]any{"repositioned": true, "position": position},
				output.WithSummary(fmt.Sprintf("Group moved to position %d", position)),
			)
		},
	}

	cmd.Flags().IntVar(&position, "position", 0, "New position, 1-based (required)")
	cmd.Flags().IntVar(&position, "pos", 0, "New position (alias)")
	cmd.MarkFlagRequired("position")

	return cmd
}
