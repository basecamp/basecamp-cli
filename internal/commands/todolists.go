package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// Todolist represents a Basecamp todolist.
type Todolist struct {
	ID                  int64  `json:"id"`
	Name                string `json:"name"`
	Description         string `json:"description,omitempty"`
	TodosRemainingCount int    `json:"todos_remaining_count"`
	CompletedRatio      string `json:"completed_ratio"`
}

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
	if err := app.API.RequireAccount(); err != nil {
		return err
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
	todosetID, err := getTodosetID(cmd, app, resolvedProjectID)
	if err != nil {
		return err
	}

	// Get todolists
	path := fmt.Sprintf("/buckets/%s/todosets/%s/todolists.json", resolvedProjectID, todosetID)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var todolists []Todolist
	if err := resp.UnmarshalData(&todolists); err != nil {
		return fmt.Errorf("failed to parse todolists: %w", err)
	}

	return app.Output.OK(todolists,
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			todolistID := args[0]

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

			path := fmt.Sprintf("/buckets/%s/todolists/%s.json", resolvedProjectID, todolistID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var todolist map[string]any
			if err := json.Unmarshal(resp.Data, &todolist); err != nil {
				return err
			}

			name := ""
			if n, ok := todolist["name"].(string); ok {
				name = n
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Todolist: %s", name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "todos",
						Cmd:         fmt.Sprintf("bcq todos --list %s --in %s", todolistID, resolvedProjectID),
						Description: "List todos",
					},
					output.Breadcrumb{
						Action:      "create",
						Cmd:         fmt.Sprintf("bcq todos create --content <text> --list %s --in %s", todolistID, resolvedProjectID),
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

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Get todoset from project dock
			todosetID, err := getTodosetID(cmd, app, resolvedProjectID)
			if err != nil {
				return err
			}

			// Build request body
			body := map[string]string{
				"name": name,
			}
			if description != "" {
				body["description"] = description
			}

			path := fmt.Sprintf("/buckets/%s/todosets/%s/todolists.json", resolvedProjectID, todosetID)
			resp, err := app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var todolist map[string]any
			if err := json.Unmarshal(resp.Data, &todolist); err != nil {
				return err
			}

			todolistID := ""
			if id, ok := todolist["id"].(float64); ok {
				todolistID = fmt.Sprintf("%.0f", id)
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Created todolist #%s: %s", todolistID, name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq todolists show %s --in %s", todolistID, resolvedProjectID),
						Description: "View todolist",
					},
					output.Breadcrumb{
						Action:      "add_todo",
						Cmd:         fmt.Sprintf("bcq todos create --content <text> --list %s --in %s", todolistID, resolvedProjectID),
						Description: "Add todo",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Todolist name (required)")
	cmd.Flags().StringVarP(&description, "description", "d", "", "Todolist description")
	cmd.MarkFlagRequired("name")

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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			todolistID := args[0]

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

			// Build request body
			body := make(map[string]string)
			if name != "" {
				body["name"] = name
			}
			if description != "" {
				body["description"] = description
			}

			path := fmt.Sprintf("/buckets/%s/todolists/%s.json", resolvedProjectID, todolistID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Updated todolist #%s", todolistID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq todolists show %s --in %s", todolistID, resolvedProjectID),
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
