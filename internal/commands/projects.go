package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// Project represents a Basecamp project.
type Project struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Purpose     string `json:"purpose"`
	Bookmarked  bool   `json:"bookmarked"`
	URL         string `json:"url"`
	AppURL      string `json:"app_url"`
}

// NewProjectsCmd creates the projects command group.
func NewProjectsCmd() *cobra.Command {
	var status string

	cmd := &cobra.Command{
		Use:     "projects",
		Aliases: []string{"project"},
		Short:   "Manage projects",
		Long:    "List, show, create, and manage Basecamp projects.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default to list when called without subcommand
			return runProjectsList(cmd, status)
		},
	}

	// Allow --status flag on root command for default list behavior
	cmd.Flags().StringVar(&status, "status", "", "Filter by status (active, archived, trashed)")

	cmd.AddCommand(
		newProjectsListCmd(),
		newProjectsShowCmd(),
		newProjectsCreateCmd(),
		newProjectsUpdateCmd(),
		newProjectsDeleteCmd(),
	)

	return cmd
}

func newProjectsListCmd() *cobra.Command {
	var status string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List projects",
		Long:  "List all accessible projects in the account.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectsList(cmd, status)
		},
	}

	cmd.Flags().StringVar(&status, "status", "", "Filter by status (active, archived, trashed)")

	return cmd
}

func runProjectsList(cmd *cobra.Command, status string) error {
	app := appctx.FromContext(cmd.Context())
	if app == nil {
		return fmt.Errorf("app not initialized")
	}

	if err := app.API.RequireAccount(); err != nil {
		return err
	}

	path := "/projects.json"
	if status != "" {
		path = fmt.Sprintf("/projects.json?status=%s", status)
	}

	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var projects []Project
	if err := resp.UnmarshalData(&projects); err != nil {
		return fmt.Errorf("failed to parse projects: %w", err)
	}

	return app.Output.OK(projects,
		output.WithSummary(fmt.Sprintf("%d projects", len(projects))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "bcq projects show <id>",
				Description: "Show project details",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         "bcq projects create --name <name>",
				Description: "Create a new project",
			},
		),
	)
}

func newProjectsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show project details",
		Long:  "Display detailed information about a project including dock items.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			projectID := args[0]
			path := fmt.Sprintf("/projects/%s.json", projectID)

			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var project json.RawMessage
			if err := resp.UnmarshalData(&project); err != nil {
				return fmt.Errorf("failed to parse project: %w", err)
			}

			return app.Output.OK(project,
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "todos",
						Cmd:         fmt.Sprintf("bcq todos --project %s", projectID),
						Description: "List todos in this project",
					},
					output.Breadcrumb{
						Action:      "messages",
						Cmd:         fmt.Sprintf("bcq messages --project %s", projectID),
						Description: "List messages in this project",
					},
				),
			)
		},
	}
}

func newProjectsCreateCmd() *cobra.Command {
	var name string
	var description string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new project",
		Long:  "Create a new Basecamp project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			if name == "" {
				return output.ErrUsage("--name is required")
			}

			body := map[string]string{
				"name": name,
			}
			if description != "" {
				body["description"] = description
			}

			resp, err := app.API.Post(cmd.Context(), "/projects.json", body)
			if err != nil {
				return err
			}

			var project json.RawMessage
			if err := resp.UnmarshalData(&project); err != nil {
				return fmt.Errorf("failed to parse project: %w", err)
			}

			return app.Output.OK(project,
				output.WithSummary(fmt.Sprintf("Created project: %s", name)),
			)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Project name (required)")
	cmd.Flags().StringVarP(&description, "description", "d", "", "Project description")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func newProjectsUpdateCmd() *cobra.Command {
	var name string
	var description string

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a project",
		Long:  "Update an existing project's name or description.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			projectID := args[0]

			body := make(map[string]string)
			if name != "" {
				body["name"] = name
			}
			if description != "" {
				body["description"] = description
			}

			if len(body) == 0 {
				return output.ErrUsage("At least one of --name or --description is required")
			}

			path := fmt.Sprintf("/projects/%s.json", projectID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var project json.RawMessage
			if err := resp.UnmarshalData(&project); err != nil {
				return fmt.Errorf("failed to parse project: %w", err)
			}

			return app.Output.OK(project,
				output.WithSummary("Project updated"),
			)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "New project name")
	cmd.Flags().StringVarP(&description, "description", "d", "", "New project description")

	return cmd
}

func newProjectsDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"trash"},
		Short:   "Delete (trash) a project",
		Long:    "Move a project to the trash. Can be restored later.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			projectID := args[0]
			path := fmt.Sprintf("/projects/%s.json", projectID)

			_, err := app.API.Delete(cmd.Context(), path)
			if err != nil {
				return err
			}

			return app.Output.OK(map[string]string{
				"id":     projectID,
				"status": "trashed",
			}, output.WithSummary("Project moved to trash"))
		},
	}
}
