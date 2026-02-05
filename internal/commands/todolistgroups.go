package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
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
		PreRunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			return ensureAccount(cmd, app)
		},
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
		newTodolistgroupsUpdateCmd(),
		newTodolistgroupsPositionCmd(),
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
	if app == nil {
		return fmt.Errorf("app not initialized")
	}

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Resolve project, with interactive fallback
	if project == "" {
		project = app.Flags.Project
	}
	if project == "" {
		project = app.Config.ProjectID
	}
	if project == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		project = app.Config.ProjectID
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

	// Parse IDs as int64
	todolistID, err := strconv.ParseInt(resolvedTodolistID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid todolist ID")
	}

	// Get groups via SDK
	groupsResult, err := app.Account().TodolistGroups().List(cmd.Context(), todolistID)
	if err != nil {
		return convertSDKError(err)
	}
	groups := groupsResult.Groups

	return app.OK(groups,
		output.WithSummary(fmt.Sprintf("%d groups in todolist #%s", len(groups), todolist)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("basecamp todolistgroups create \"name\" --list %s --in %s", todolist, resolvedProjectID),
				Description: "Create group",
			},
			output.Breadcrumb{
				Action:      "todolist",
				Cmd:         fmt.Sprintf("basecamp todolists show %s --in %s", todolist, resolvedProjectID),
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
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			groupIDStr := args[0]

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

			// Parse IDs as int64

			groupID, err := strconv.ParseInt(groupIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid group ID")
			}

			// Get group via SDK
			group, err := app.Account().TodolistGroups().Get(cmd.Context(), groupID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(group,
				output.WithSummary(group.Name),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("basecamp todolistgroups update %s --name \"New Name\" --in %s", groupIDStr, resolvedProjectID),
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
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if name == "" {
				return output.ErrUsage("--name is required")
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

			// Resolve todolist - fall back to config
			todolistIDStr := *todolist
			if todolistIDStr == "" {
				todolistIDStr = app.Flags.Todolist
			}
			if todolistIDStr == "" {
				todolistIDStr = app.Config.TodolistID
			}
			if todolistIDStr == "" {
				return output.ErrUsage("--list is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			resolvedTodolistID, _, err := app.Names.ResolveTodolist(cmd.Context(), todolistIDStr, resolvedProjectID)
			if err != nil {
				return err
			}

			// Parse IDs as int64

			todolistID, err := strconv.ParseInt(resolvedTodolistID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todolist ID")
			}

			// Build SDK request
			req := &basecamp.CreateTodolistGroupRequest{
				Name: name,
			}

			// Create group via SDK
			group, err := app.Account().TodolistGroups().Create(cmd.Context(), todolistID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(group,
				output.WithSummary(fmt.Sprintf("Created group: %s", group.Name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "group",
						Cmd:         fmt.Sprintf("basecamp todolistgroups show %d --in %s", group.ID, resolvedProjectID),
						Description: "View group",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Group name (required)")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func newTodolistgroupsUpdateCmd() *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:     "update <id>",
		Aliases: []string{"rename"},
		Short:   "Update a todolist group",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			groupIDStr := args[0]

			if name == "" {
				return output.ErrUsage("--name is required")
			}

			groupID, err := strconv.ParseInt(groupIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid group ID")
			}

			// Build SDK request
			req := &basecamp.UpdateTodolistGroupRequest{
				Name: name,
			}

			// Update group via SDK
			group, err := app.Account().TodolistGroups().Update(cmd.Context(), groupID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(group,
				output.WithSummary(fmt.Sprintf("Renamed to: %s", group.Name)),
			)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "New name (required)")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func newTodolistgroupsPositionCmd() *cobra.Command {
	var position int

	cmd := &cobra.Command{
		Use:     "position <id>",
		Aliases: []string{"move"},
		Short:   "Change group position",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			groupIDStr := args[0]

			if position == 0 {
				return output.ErrUsage("--position is required (1-based)")
			}

			groupID, err := strconv.ParseInt(groupIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid group ID")
			}

			// Reposition group via SDK
			err = app.Account().TodolistGroups().Reposition(cmd.Context(), groupID, position)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"repositioned": true, "position": position},
				output.WithSummary(fmt.Sprintf("Group moved to position %d", position)),
			)
		},
	}

	cmd.Flags().IntVar(&position, "position", 0, "New position, 1-based (required)")
	cmd.Flags().IntVar(&position, "pos", 0, "New position (alias)")
	_ = cmd.MarkFlagRequired("position")

	return cmd
}
