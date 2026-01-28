package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// NewTodolistsCmd creates the todolists command group.
func NewTodolistsCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:     "todolists",
		Aliases: []string{"todolist"},
		Short:   "Manage todolists",
		Long: `Manage todolists in a project.

A "todoset" is the container; "todolists" are the actual lists inside it.
Each project has one todoset containing multiple todolists.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default to list when called without subcommand
			return runTodolistsList(cmd, project)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	cmd.AddCommand(
		newTodolistsListCmd(&project),
		newTodolistsShowCmd(&project),
		newTodolistsCreateCmd(&project),
		newTodolistsUpdateCmd(&project),
	)

	return cmd
}

func newTodolistsListCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List todolists",
		Long:  "List all todolists in a project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTodolistsList(cmd, *project)
		},
	}
}

func runTodolistsList(cmd *cobra.Command, project string) error {
	app := appctx.FromContext(cmd.Context())
	if app == nil {
		return fmt.Errorf("app not initialized")
	}

	// Resolve project
	projectID := project
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

	// Get todoset from project dock
	todosetIDStr, err := getTodosetID(cmd, app, resolvedProjectID)
	if err != nil {
		return err
	}

	// Parse IDs as int64
	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}
	todosetID, err := strconv.ParseInt(todosetIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid todoset ID")
	}

	// Get todolists via SDK
	todolists, err := app.SDK.Todolists().List(cmd.Context(), bucketID, todosetID, nil)
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(todolists,
		output.WithSummary(fmt.Sprintf("%d todolists", len(todolists))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "todos",
				Cmd:         fmt.Sprintf("bcq todos --list <id> --in %s", resolvedProjectID),
				Description: "List todos in list",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("bcq todolists create --name <name> --in %s", resolvedProjectID),
				Description: "Create todolist",
			},
		),
	)
}

func newTodolistsShowCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show todolist details",
		Long:  "Display detailed information about a todolist.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			todolistIDStr := args[0]

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

			// Parse IDs as int64
			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}
			todolistID, err := strconv.ParseInt(todolistIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todolist ID")
			}

			// Get todolist via SDK
			todolist, err := app.SDK.Todolists().Get(cmd.Context(), bucketID, todolistID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(todolist,
				output.WithSummary(fmt.Sprintf("Todolist: %s", todolist.Name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "todos",
						Cmd:         fmt.Sprintf("bcq todos --list %s --in %s", todolistIDStr, resolvedProjectID),
						Description: "List todos",
					},
					output.Breadcrumb{
						Action:      "create",
						Cmd:         fmt.Sprintf("bcq todos create --content <text> --list %s --in %s", todolistIDStr, resolvedProjectID),
						Description: "Create todo",
					},
				),
			)
		},
	}
	return cmd
}

func newTodolistsCreateCmd(project *string) *cobra.Command {
	var name string
	var description string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new todolist",
		Long:  "Create a new todolist in a project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
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

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Get todoset from project dock
			todosetIDStr, err := getTodosetID(cmd, app, resolvedProjectID)
			if err != nil {
				return err
			}

			// Parse IDs as int64
			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}
			todosetID, err := strconv.ParseInt(todosetIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todoset ID")
			}

			// Build SDK request
			req := &basecamp.CreateTodolistRequest{
				Name:        name,
				Description: description,
			}

			// Create todolist via SDK
			todolist, err := app.SDK.Todolists().Create(cmd.Context(), bucketID, todosetID, req)
			if err != nil {
				return convertSDKError(err)
			}

			todolistIDStr := fmt.Sprintf("%d", todolist.ID)

			return app.OK(todolist,
				output.WithSummary(fmt.Sprintf("Created todolist #%s: %s", todolistIDStr, name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq todolists show %s --in %s", todolistIDStr, resolvedProjectID),
						Description: "View todolist",
					},
					output.Breadcrumb{
						Action:      "add_todo",
						Cmd:         fmt.Sprintf("bcq todos create --content <text> --list %s --in %s", todolistIDStr, resolvedProjectID),
						Description: "Add todo",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Todolist name (required)")
	cmd.Flags().StringVarP(&description, "description", "d", "", "Todolist description")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func newTodolistsUpdateCmd(project *string) *cobra.Command {
	var name string
	var description string

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a todolist",
		Long:  "Update an existing todolist's name or description.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			todolistIDStr := args[0]

			if name == "" && description == "" {
				return output.ErrUsage("at least one of --name or --description is required")
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

			// Parse IDs as int64
			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}
			todolistID, err := strconv.ParseInt(todolistIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todolist ID")
			}

			// Build SDK request
			req := &basecamp.UpdateTodolistRequest{
				Name:        name,
				Description: description,
			}

			// Update todolist via SDK
			todolist, err := app.SDK.Todolists().Update(cmd.Context(), bucketID, todolistID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(todolist,
				output.WithSummary(fmt.Sprintf("Updated todolist #%s", todolistIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq todolists show %s --in %s", todolistIDStr, resolvedProjectID),
						Description: "View todolist",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "New todolist name")
	cmd.Flags().StringVarP(&description, "description", "d", "", "New todolist description")

	return cmd
}

// getTodosetID retrieves the todoset ID from a project's dock, handling multi-dock projects.
func getTodosetID(cmd *cobra.Command, app *appctx.App, projectID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "todoset", "", "todoset")
}
