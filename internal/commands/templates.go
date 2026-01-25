package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// NewTemplatesCmd creates the templates command for managing project templates.
func NewTemplatesCmd() *cobra.Command {
	var status string

	cmd := &cobra.Command{
		Use:   "templates",
		Short: "Manage project templates",
		Long: `Manage project templates.

Templates allow you to create new projects with predefined structure,
tools, and content.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTemplatesList(cmd, status)
		},
	}

	cmd.PersistentFlags().StringVar(&status, "status", "active", "Filter: active, archived, trashed")

	cmd.AddCommand(
		newTemplatesListCmd(&status),
		newTemplatesShowCmd(),
		newTemplatesCreateCmd(),
		newTemplatesUpdateCmd(),
		newTemplatesDeleteCmd(),
		newTemplatesConstructCmd(),
		newTemplatesConstructionCmd(),
	)

	return cmd
}

func newTemplatesListCmd(status *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List templates",
		Long:  "List all project templates.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTemplatesList(cmd, *status)
		},
	}
}

func runTemplatesList(cmd *cobra.Command, status string) error {
	app := appctx.FromContext(cmd.Context())
	if err := app.API.RequireAccount(); err != nil {
		return err
	}

	path := fmt.Sprintf("/templates.json?status=%s", status)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var templates []json.RawMessage
	if err := json.Unmarshal(resp.Data, &templates); err != nil {
		return fmt.Errorf("failed to parse templates: %w", err)
	}

	return app.Output.OK(json.RawMessage(resp.Data),
		output.WithSummary(fmt.Sprintf("%d templates", len(templates))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "bcq templates show <id>",
				Description: "View template details",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         "bcq templates create \"Name\"",
				Description: "Create new template",
			},
			output.Breadcrumb{
				Action:      "construct",
				Cmd:         "bcq templates construct <id> --name \"Project Name\"",
				Description: "Create project from template",
			},
		),
	)
}

func newTemplatesShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show template details",
		Long:  "Display detailed information about a template.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			templateID := args[0]

			path := fmt.Sprintf("/templates/%s.json", templateID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var template struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(resp.Data, &template); err != nil {
				return fmt.Errorf("failed to parse template: %w", err)
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(template.Name),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "construct",
						Cmd:         fmt.Sprintf("bcq templates construct %s --name \"Project Name\"", templateID),
						Description: "Create project from template",
					},
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("bcq templates update %s --name \"New Name\"", templateID),
						Description: "Update template",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "bcq templates",
						Description: "List all templates",
					},
				),
			)
		},
	}
}

func newTemplatesCreateCmd() *cobra.Command {
	var name string
	var description string

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new template",
		Long:  "Create a new project template.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			// Name from positional arg or flag
			if len(args) > 0 && name == "" {
				name = args[0]
			}

			if name == "" {
				return output.ErrUsage("Template name is required")
			}

			body := map[string]any{
				"name": name,
			}
			if description != "" {
				body["description"] = description
			}

			resp, err := app.API.Post(cmd.Context(), "/templates.json", body)
			if err != nil {
				return err
			}

			var created struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(resp.Data, &created); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Created template #%d: %s", created.ID, name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq templates show %d", created.ID),
						Description: "View template",
					},
					output.Breadcrumb{
						Action:      "construct",
						Cmd:         fmt.Sprintf("bcq templates construct %d --name \"Project Name\"", created.ID),
						Description: "Create project from template",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Template name")
	cmd.Flags().StringVar(&description, "description", "", "Template description")
	cmd.Flags().StringVar(&description, "desc", "", "Template description (alias)")

	return cmd
}

func newTemplatesUpdateCmd() *cobra.Command {
	var name string
	var description string

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a template",
		Long:  "Update an existing template's name or description.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			templateID := args[0]

			if name == "" && description == "" {
				return output.ErrUsage("Use --name or --description to update")
			}

			body := map[string]any{}
			if name != "" {
				body["name"] = name
			}
			if description != "" {
				body["description"] = description
			}

			path := fmt.Sprintf("/templates/%s.json", templateID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Updated template #%s", templateID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq templates show %s", templateID),
						Description: "View template",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "New name")
	cmd.Flags().StringVar(&description, "description", "", "New description")
	cmd.Flags().StringVar(&description, "desc", "", "New description (alias)")

	return cmd
}

func newTemplatesDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete (trash) a template",
		Long:  "Move a template to trash.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			templateID := args[0]

			path := fmt.Sprintf("/templates/%s.json", templateID)
			_, err := app.API.Delete(cmd.Context(), path)
			if err != nil {
				return err
			}

			return app.Output.OK(map[string]any{"deleted": true},
				output.WithSummary(fmt.Sprintf("Trashed template #%s", templateID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "bcq templates",
						Description: "List templates",
					},
					output.Breadcrumb{
						Action:      "trashed",
						Cmd:         "bcq templates --status trashed",
						Description: "View trashed templates",
					},
				),
			)
		},
	}
}

func newTemplatesConstructCmd() *cobra.Command {
	var projectName string
	var projectDesc string

	cmd := &cobra.Command{
		Use:   "construct <template_id>",
		Short: "Create project from template",
		Long: `Create a new project from a template.

This is an asynchronous operation. The command returns a construction ID
which can be polled via 'templates construction' until the status is "completed".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			templateID := args[0]

			if projectName == "" {
				return output.ErrUsage("--name is required (project name)")
			}

			body := map[string]any{
				"project": map[string]any{
					"name": projectName,
				},
			}
			if projectDesc != "" {
				body["project"].(map[string]any)["description"] = projectDesc
			}

			path := fmt.Sprintf("/templates/%s/project_constructions.json", templateID)
			resp, err := app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var construction struct {
				ID     int64  `json:"id"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(resp.Data, &construction); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Started project construction #%d (%s)", construction.ID, construction.Status)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "status",
						Cmd:         fmt.Sprintf("bcq templates construction %s %d", templateID, construction.ID),
						Description: "Check construction status",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&projectName, "name", "", "Project name (required)")
	cmd.Flags().StringVar(&projectDesc, "description", "", "Project description")
	cmd.Flags().StringVar(&projectDesc, "desc", "", "Project description (alias)")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func newTemplatesConstructionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "construction <template_id> <construction_id>",
		Short: "Check construction status",
		Long: `Check the status of a project construction.

Poll this endpoint until the status is "completed". When complete,
the response includes the newly created project.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			templateID := args[0]
			constructionID := args[1]

			path := fmt.Sprintf("/templates/%s/project_constructions/%s.json", templateID, constructionID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var construction struct {
				Status  string `json:"status"`
				Project struct {
					ID   int64  `json:"id"`
					Name string `json:"name"`
				} `json:"project"`
			}
			if err := json.Unmarshal(resp.Data, &construction); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}

			var summary string
			var breadcrumbs []output.Breadcrumb

			if construction.Status == "completed" {
				summary = fmt.Sprintf("Construction complete: %s (project #%d)", construction.Project.Name, construction.Project.ID)
				breadcrumbs = []output.Breadcrumb{
					{
						Action:      "project",
						Cmd:         fmt.Sprintf("bcq projects show %d", construction.Project.ID),
						Description: "View created project",
					},
				}
			} else {
				summary = fmt.Sprintf("Construction status: %s", construction.Status)
				breadcrumbs = []output.Breadcrumb{
					{
						Action:      "poll",
						Cmd:         fmt.Sprintf("bcq templates construction %s %s", templateID, constructionID),
						Description: "Check again",
					},
				}
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(summary),
				output.WithBreadcrumbs(breadcrumbs...),
			)
		},
	}
}
