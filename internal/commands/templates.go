package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

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

	if err := app.RequireAccount(); err != nil {
		return err
	}

	var templates []basecamp.Template
	var err error

	// SDK List() defaults to active status (API default)
	// For archived/trashed, use raw API with status parameter
	if status == "active" || status == "" {
		templates, err = app.Account().Templates().List(cmd.Context())
		if err != nil {
			return convertSDKError(err)
		}
	} else {
		// Fall back to raw API for non-active statuses
		path := fmt.Sprintf("/templates.json?status=%s", status)
		resp, err := app.Account().Get(cmd.Context(), path)
		if err != nil {
			return convertSDKError(err)
		}
		if err := resp.UnmarshalData(&templates); err != nil {
			return fmt.Errorf("failed to parse templates: %w", err)
		}
	}

	return app.OK(templates,
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

			if err := app.RequireAccount(); err != nil {
				return err
			}

			templateID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid template ID")
			}

			template, err := app.Account().Templates().Get(cmd.Context(), templateID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(template,
				output.WithSummary(template.Name),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "construct",
						Cmd:         fmt.Sprintf("bcq templates construct %d --name \"Project Name\"", templateID),
						Description: "Create project from template",
					},
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("bcq templates update %d --name \"New Name\"", templateID),
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

			if err := app.RequireAccount(); err != nil {
				return err
			}

			// Name from positional arg or flag
			if len(args) > 0 && name == "" {
				name = args[0]
			}

			if name == "" {
				return output.ErrUsage("Template name is required")
			}

			req := &basecamp.CreateTemplateRequest{
				Name:        name,
				Description: description,
			}

			template, err := app.Account().Templates().Create(cmd.Context(), req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(template,
				output.WithSummary(fmt.Sprintf("Created template #%d: %s", template.ID, name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq templates show %d", template.ID),
						Description: "View template",
					},
					output.Breadcrumb{
						Action:      "construct",
						Cmd:         fmt.Sprintf("bcq templates construct %d --name \"Project Name\"", template.ID),
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

			if err := app.RequireAccount(); err != nil {
				return err
			}

			templateID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid template ID")
			}

			if name == "" && description == "" {
				return output.ErrUsage("Use --name or --description to update")
			}

			// SDK requires name for update, fetch current if not provided
			updateName := name
			if updateName == "" {
				current, err := app.Account().Templates().Get(cmd.Context(), templateID)
				if err != nil {
					return convertSDKError(err)
				}
				updateName = current.Name
			}

			req := &basecamp.UpdateTemplateRequest{
				Name:        updateName,
				Description: description,
			}

			template, err := app.Account().Templates().Update(cmd.Context(), templateID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(template,
				output.WithSummary(fmt.Sprintf("Updated template #%d", templateID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq templates show %d", templateID),
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

			if err := app.RequireAccount(); err != nil {
				return err
			}

			templateID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid template ID")
			}

			err = app.Account().Templates().Delete(cmd.Context(), templateID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"deleted": true},
				output.WithSummary(fmt.Sprintf("Trashed template #%d", templateID)),
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

			if err := app.RequireAccount(); err != nil {
				return err
			}

			templateID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid template ID")
			}

			if projectName == "" {
				return output.ErrUsage("--name is required (project name)")
			}

			construction, err := app.Account().Templates().CreateProject(cmd.Context(), templateID, projectName, projectDesc)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(construction,
				output.WithSummary(fmt.Sprintf("Started project construction #%d (%s)", construction.ID, construction.Status)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "status",
						Cmd:         fmt.Sprintf("bcq templates construction %d %d", templateID, construction.ID),
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

			if err := app.RequireAccount(); err != nil {
				return err
			}

			templateID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid template ID")
			}

			constructionID, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid construction ID")
			}

			construction, err := app.Account().Templates().GetConstruction(cmd.Context(), templateID, constructionID)
			if err != nil {
				return convertSDKError(err)
			}

			var summary string
			var breadcrumbs []output.Breadcrumb

			if construction.Status == "completed" && construction.Project != nil {
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
						Cmd:         fmt.Sprintf("bcq templates construction %d %d", templateID, constructionID),
						Description: "Check again",
					},
				}
			}

			return app.OK(construction,
				output.WithSummary(summary),
				output.WithBreadcrumbs(breadcrumbs...),
			)
		},
	}
}
