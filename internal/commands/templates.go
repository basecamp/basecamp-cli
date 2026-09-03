package commands

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewTemplatesCmd creates the templates command for managing project and to-do list templates.
func NewTemplatesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "templates",
		Short: "Manage project and to-do list templates",
		Long: `Manage project templates and the account's to-do list template library.

Project templates create projects with predefined structure, tools, and content.
Library templates copy a reusable to-do list into an existing project.`,
		Annotations: map[string]string{"agent_notes": "Project construction and to-do list template copies are asynchronous. Poll construction or copy-status until status=completed. Copy grants referenced people project access only with --confirm-adding-people."},
	}

	cmd.AddCommand(
		newTemplatesListCmd(),
		newTemplatesShowCmd(),
		newTemplatesCreateCmd(),
		newTemplatesUpdateCmd(),
		newTemplatesDeleteCmd(),
		newTemplatesConstructCmd(),
		newTemplatesConstructionCmd(),
		newTemplatesLibraryCmd(),
		newTemplatesCopyCmd(),
		newTemplatesCopyStatusCmd(),
	)

	return cmd
}

func newTemplatesListCmd() *cobra.Command {
	var status string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List templates",
		Long:  "List all project templates.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTemplatesList(cmd, status)
		},
	}

	cmd.Flags().StringVar(&status, "status", "active", "Filter: active, archived, trashed")

	return cmd
}

func runTemplatesList(cmd *cobra.Command, status string) error {
	// Validate before the value reaches the request URL — only the lifecycle
	// filters the API understands are allowed.
	switch status {
	case "", "active", "archived", "trashed":
	default:
		return output.ErrUsage(
			fmt.Sprintf("unknown --status value %q (expected active, archived, or trashed)", status))
	}

	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	var templates []basecamp.Template

	// SDK List() defaults to active status (API default)
	// For archived/trashed, use raw API with status parameter
	if status == "active" || status == "" {
		templatesResult, err := app.Account().Templates().List(cmd.Context(), nil)
		if err != nil {
			return convertSDKError(err)
		}
		templates = templatesResult.Templates
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

	// Strip status and updated_at — they're noise in list output
	type templateListItem struct {
		ID          int64     `json:"id"`
		Name        string    `json:"name"`
		Description string    `json:"description"`
		CreatedAt   time.Time `json:"created_at"`
	}
	items := make([]templateListItem, len(templates))
	for i, t := range templates {
		items[i] = templateListItem{
			ID:          t.ID,
			Name:        t.Name,
			Description: t.Description,
			CreatedAt:   t.CreatedAt,
		}
	}

	return app.OK(items,
		output.WithSummary(fmt.Sprintf("%d templates", len(templates))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "basecamp templates show <id>",
				Description: "View template details",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         "basecamp templates create \"Name\"",
				Description: "Create new template",
			},
			output.Breadcrumb{
				Action:      "construct",
				Cmd:         "basecamp templates construct <id> --name \"Project Name\"",
				Description: "Create project from template",
			},
		),
	)
}

func newTemplatesLibraryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "library",
		Short: "List to-do list templates",
		Long:  "List the account's active to-do list templates.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			library, err := app.Account().Templates().GetLibrary(cmd.Context())
			if err != nil {
				return convertSDKError(err)
			}

			display := make([]struct {
				ID    int64  `json:"id"`
				Title string `json:"title"`
			}, len(library.Todolists))
			for i, todolist := range library.Todolists {
				display[i].ID = todolist.ID
				display[i].Title = todolist.Title
			}

			return app.OK(library,
				output.WithDisplayData(display),
				output.WithSummary(fmt.Sprintf("%d active to-do list templates", len(library.Todolists))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "copy",
						Cmd:         "basecamp templates copy <template-id> --in <project>",
						Description: "Copy a template into a project",
					},
				),
			)
		},
	}
}

func newTemplatesCopyCmd() *cobra.Command {
	var project string
	var todoset string
	var confirmAddingPeople bool

	cmd := &cobra.Command{
		Use:   "copy <template_id>",
		Short: "Copy a to-do list template into a project",
		Long: `Start copying a to-do list template into a project's To-dos tool.

The copy runs asynchronously. Use 'templates copy-status' with the returned
copy ID to check its progress. Referenced people receive project access only
when --confirm-adding-people is explicitly provided.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			templateID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid template ID")
			}
			if err := requireNumericID(todoset, "todoset ID"); err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			persistentAccount := hasPersistentAccount(app.Config)
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			resolvedProjectID, err := resolveProjectID(cmd, app, project)
			if err != nil {
				return err
			}
			if todoset != "" {
				if err := validateTemplateCopyTodoset(cmd, app, todoset, resolvedProjectID); err != nil {
					return err
				}
			}
			resolvedTodosetID, err := ensureTodoset(cmd, app, resolvedProjectID, todoset)
			if err != nil {
				return err
			}
			destinationParentID, err := strconv.ParseInt(resolvedTodosetID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todoset ID")
			}

			templateCopy, err := app.Account().Templates().CreateLibraryCopy(cmd.Context(), &basecamp.CreateTemplateLibraryCopyRequest{
				TemplateRecordingID:   templateID,
				DestinationParentID:   destinationParentID,
				AddingPeopleConfirmed: confirmAddingPeople,
			})
			if err != nil {
				return templateCopyError(
					err,
					templateID,
					resolvedProjectID,
					resolvedTodosetID,
					app.Config.ActiveProfile,
					replyAccountArg(persistentAccount, app.Config.AccountID),
				)
			}

			return app.OK(templateCopy,
				output.WithSummary(fmt.Sprintf("Started template copy #%d (%s)", templateCopy.ID, templateCopy.Status)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "status",
						Cmd:         fmt.Sprintf("basecamp templates copy-status %d", templateCopy.ID),
						Description: "Check copy status",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID or name (alias for --project)")
	cmd.Flags().StringVar(&todoset, "todoset", "", "To-dos tool ID (auto-detected from project)")
	cmd.Flags().BoolVar(&confirmAddingPeople, "confirm-adding-people", false, "Grant referenced people access to the destination project")

	return cmd
}

func validateTemplateCopyTodoset(cmd *cobra.Command, app *appctx.App, todosetID, projectID string) error {
	if err := validateTodosetOwnership(cmd, app, todosetID, projectID); err != nil {
		return err
	}

	enabled, all, err := getDockTools(cmd.Context(), app, projectID, "todoset")
	if err != nil {
		return err
	}
	todosetNum, err := strconv.ParseInt(todosetID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid todoset ID")
	}
	for _, tool := range enabled {
		if tool.ID == todosetNum {
			return nil
		}
	}
	for _, tool := range all {
		if tool.ID == todosetNum {
			return output.ErrUsage(fmt.Sprintf("--todoset %s is disabled for project %s", todosetID, projectID))
		}
	}
	return output.ErrUsage(fmt.Sprintf("--todoset %s is not a To-dos tool in project %s", todosetID, projectID))
}

func templateCopyError(err error, templateID int64, projectID, todosetID, profile, accountArg string) error {
	var confirmationErr *basecamp.PeopleConfirmationRequiredError
	if !errors.As(err, &confirmationErr) {
		return convertSDKError(err)
	}

	people := make([]string, 0, len(confirmationErr.People))
	for _, person := range confirmationErr.People {
		people = append(people, fmt.Sprintf("%s (#%d)", person.Name, person.ID))
	}

	rerun := fmt.Sprintf("basecamp templates copy %d --in %s --todoset %s", templateID, projectID, todosetID)
	if profile != "" {
		rerun += " --profile " + profile
	}
	rerun += accountArg
	rerun += " --confirm-adding-people"

	converted := output.AsError(err)
	return &output.Error{
		Code:       converted.Code,
		Message:    fmt.Sprintf("Adding referenced people requires confirmation: %s", strings.Join(people, ", ")),
		Hint:       "Review the people above, then rerun with explicit confirmation: " + rerun,
		HTTPStatus: converted.HTTPStatus,
		Retryable:  converted.Retryable,
		Cause:      err,
	}
}

func newTemplatesCopyStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "copy-status <copy_id>",
		Short: "Check a to-do list template copy",
		Long:  "Check whether a to-do list template copy is pending, processing, completed, or failed.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			copyID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid copy ID")
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			templateCopy, err := app.Account().Templates().GetLibraryCopy(cmd.Context(), copyID)
			if err != nil {
				return convertSDKError(err)
			}

			summary, breadcrumbs := templateCopyStatusOutput(templateCopy)
			return app.OK(templateCopy,
				output.WithSummary(summary),
				output.WithBreadcrumbs(breadcrumbs...),
			)
		},
	}
}

func templateCopyStatusOutput(templateCopy *basecamp.TemplateLibraryCopy) (string, []output.Breadcrumb) {
	switch templateCopy.Status {
	case "completed":
		if templateCopy.DestinationTodolist != nil {
			list := templateCopy.DestinationTodolist
			breadcrumbs := []output.Breadcrumb{
				{
					Action:      "show",
					Cmd:         fmt.Sprintf("basecamp todolists show %d --in %d", list.ID, list.Bucket.ID),
					Description: "View copied to-do list",
				},
			}
			return fmt.Sprintf("Template copy complete: %s (to-do list #%d)", list.Name, list.ID), breadcrumbs
		}
		return fmt.Sprintf("Template copy #%d completed", templateCopy.ID), nil
	case "failed":
		return fmt.Sprintf("Template copy #%d failed", templateCopy.ID), []output.Breadcrumb{
			{
				Action:      "library",
				Cmd:         "basecamp templates library",
				Description: "List available templates",
			},
		}
	case "pending", "processing":
		return fmt.Sprintf("Template copy #%d is %s", templateCopy.ID, templateCopy.Status), []output.Breadcrumb{
			{
				Action:      "poll",
				Cmd:         fmt.Sprintf("basecamp templates copy-status %d", templateCopy.ID),
				Description: "Check again",
			},
		}
	default:
		return fmt.Sprintf("Template copy #%d status: %s", templateCopy.ID, templateCopy.Status), nil
	}
}

func newTemplatesShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show template details",
		Long:  "Display detailed information about a template.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
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
						Cmd:         fmt.Sprintf("basecamp templates construct %d --name \"Project Name\"", templateID),
						Description: "Create project from template",
					},
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("basecamp templates update %d --name \"New Name\"", templateID),
						Description: "Update template",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp templates",
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
		Use:   "create <name>",
		Short: "Create a new template",
		Long:  "Create a new project template.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Name from positional arg or flag
			if len(args) > 0 && name == "" {
				name = args[0]
			}

			// Show help when invoked with no arguments
			if name == "" {
				return missingArg(cmd, "<name>")
			}

			app := appctx.FromContext(cmd.Context())

			description, err := resolveContentValue(cmd, description, -1, "--description")
			if err != nil {
				return err
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
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
						Cmd:         fmt.Sprintf("basecamp templates show %d", template.ID),
						Description: "View template",
					},
					output.Breadcrumb{
						Action:      "construct",
						Cmd:         fmt.Sprintf("basecamp templates construct %d --name \"Project Name\"", template.ID),
						Description: "Create project from template",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Template name")
	cmd.Flags().StringVar(&description, "description", "", "Template description; use - to read from stdin")
	cmd.Flags().StringVar(&description, "desc", "", "Template description (alias)")

	allowDash(cmd, "flag:description", "flag:desc")

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
			if name == "" && description == "" {
				return noChanges(cmd)
			}

			templateID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid template ID")
			}

			// Syntactic checks first, then "-", then account and network.
			description, err := resolveContentValue(cmd, description, -1, "--description")
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
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
						Cmd:         fmt.Sprintf("basecamp templates show %d", templateID),
						Description: "View template",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "New name")
	cmd.Flags().StringVar(&description, "description", "", "New description; use - to read from stdin")
	cmd.Flags().StringVar(&description, "desc", "", "New description (alias)")

	allowDash(cmd, "flag:description", "flag:desc")

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

			if err := ensureAccount(cmd, app); err != nil {
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
						Cmd:         "basecamp templates",
						Description: "List templates",
					},
					output.Breadcrumb{
						Action:      "trashed",
						Cmd:         "basecamp templates --status trashed",
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
			if projectName == "" {
				return output.ErrUsage("--name is required (project name)")
			}

			templateID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid template ID")
			}

			// Syntactic checks first, then "-", then account and network.
			projectDesc, err := resolveContentValue(cmd, projectDesc, -1, "--description")
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
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
						Cmd:         fmt.Sprintf("basecamp templates construction %d %d", templateID, construction.ID),
						Description: "Check construction status",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&projectName, "name", "", "Project name (required)")
	cmd.Flags().StringVar(&projectDesc, "description", "", "Project description; use - to read from stdin")
	cmd.Flags().StringVar(&projectDesc, "desc", "", "Project description (alias)")
	_ = cmd.MarkFlagRequired("name")

	allowDash(cmd, "flag:description", "flag:desc")

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

			if err := ensureAccount(cmd, app); err != nil {
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
						Cmd:         fmt.Sprintf("basecamp projects show %d", construction.Project.ID),
						Description: "View created project",
					},
				}
			} else {
				summary = fmt.Sprintf("Construction status: %s", construction.Status)
				breadcrumbs = []output.Breadcrumb{
					{
						Action:      "poll",
						Cmd:         fmt.Sprintf("basecamp templates construction %d %d", templateID, constructionID),
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
