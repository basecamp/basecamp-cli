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
	"github.com/basecamp/basecamp-cli/internal/dateparse"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewTemplatesCmd creates the templates command for managing project and to-do list templates.
func NewTemplatesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "templates",
		Short: "Manage project and to-do list templates",
		Long: `Manage the account's template library, grouped by kind of template.

  projects    Project templates: construct a whole project from a blueprint.
  todolists   To-do list templates: duplicate a reusable list into a project.

The pre-grouping spellings (templates list, templates library, templates copy,
and the rest) still work and resolve to the same commands.`,
		Annotations: map[string]string{"agent_notes": "Project construction and to-do list template duplication are asynchronous. Poll templates projects construction or templates todolists duplication until status=completed. construct --start-date anchors the template's relative dates to the Sunday of that week; without it they anchor to the week of construction. Duplicating grants referenced people project access only with --confirm-adding-people."},
	}

	cmd.AddCommand(
		newTemplatesProjectsCmd(),
		newTemplatesTodolistsCmd(),
	)
	cmd.AddCommand(templatesBackCompatCmds()...)

	return cmd
}

func newTemplatesProjectsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "projects",
		Short: "Manage project templates",
		Long: `Manage project templates.

A project template is a blueprint for a whole project: its tools, structure,
and starting content. Constructing one creates a new project.`,
	}

	cmd.AddCommand(
		newTemplatesListCmd(),
		newTemplatesShowCmd(),
		newTemplatesCreateCmd(),
		newTemplatesUpdateCmd(),
		newTemplatesDeleteCmd(),
		newTemplatesConstructCmd(),
		newTemplatesConstructionCmd(),
	)

	return cmd
}

func newTemplatesTodolistsCmd() *cobra.Command {
	kind := todolistTemplateKind()

	cmd := &cobra.Command{
		Use:   "todolists",
		Short: "Manage to-do list templates",
		Long: `Manage the account's to-do list template library.

A to-do list template is a reusable list. Duplicating one adds it to an
existing project's To-dos tool.`,
	}

	cmd.AddCommand(
		newTemplatesLibraryListCmd("list"),
		newTemplatesDuplicateCmd(kind, "duplicate <template_id>", "copy"),
		newTemplatesDuplicationCmd(kind, "duplication <duplication_id>", "copy-status"),
	)

	return cmd
}

func templatesBackCompatCmds() []*cobra.Command {
	kind := todolistTemplateKind()

	// Registered a second time rather than aliased: cobra aliases rename a
	// command where it sits, and these moved a level down the tree.
	return []*cobra.Command{
		newTemplatesListCmd(),
		newTemplatesShowCmd(),
		newTemplatesCreateCmd(),
		newTemplatesUpdateCmd(),
		newTemplatesDeleteCmd(),
		newTemplatesConstructCmd(),
		newTemplatesConstructionCmd(),
		newTemplatesLibraryListCmd("library"),
		newTemplatesDuplicateCmd(kind, "copy <template_id>"),
		newTemplatesDuplicationCmd(kind, "copy-status <copy_id>"),
	}
}

// What differs by kind when duplicating: how a pinned container is checked, and
// what the finished copy points at. Basecamp resolves the container itself from
// the destination project, so the unpinned path is the same for every kind.
type templateCopyKind struct {
	noun                    string
	group                   string
	containerFlag           string
	containerUsage          string
	resolveContainer        func(cmd *cobra.Command, app *appctx.App, projectID, container string) (int64, error)
	describeCompletedResult func(templateCopy *basecamp.TemplateLibraryCopy, contextArgs string) (string, []output.Breadcrumb, bool)
}

func todolistTemplateKind() templateCopyKind {
	return templateCopyKind{
		noun:                    "to-do list",
		group:                   "todolists",
		containerFlag:           "todoset",
		containerUsage:          "To-dos tool ID (auto-detected from project)",
		resolveContainer:        resolveTemplateCopyTodoset,
		describeCompletedResult: describeTodolistDuplication,
	}
}

func describeTodolistDuplication(templateCopy *basecamp.TemplateLibraryCopy, contextArgs string) (string, []output.Breadcrumb, bool) {
	list := templateCopy.DestinationTodolist
	if list == nil {
		return "", nil, false
	}

	breadcrumbs := []output.Breadcrumb{
		{
			Action:      "show",
			Cmd:         fmt.Sprintf("basecamp todolists show %d --in %d%s", list.ID, list.Bucket.ID, contextArgs),
			Description: "View the duplicated to-do list",
		},
	}

	return fmt.Sprintf("Template duplication complete: %s (to-do list #%d)", list.Name, list.ID), breadcrumbs, true
}

func resolveTemplateCopyTodoset(cmd *cobra.Command, app *appctx.App, projectID, todoset string) (int64, error) {
	if err := validateTemplateCopyTodoset(cmd, app, todoset, projectID); err != nil {
		return 0, err
	}

	todosetID, err := strconv.ParseInt(todoset, 10, 64)
	if err != nil {
		return 0, output.ErrUsage("Invalid todoset ID")
	}

	return todosetID, nil
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
				Cmd:         "basecamp templates projects show <id>",
				Description: "View template details",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         "basecamp templates projects create \"Name\"",
				Description: "Create new template",
			},
			output.Breadcrumb{
				Action:      "construct",
				Cmd:         "basecamp templates projects construct <id> --name \"Project Name\"",
				Description: "Create project from template",
			},
		),
	)
}

func newTemplatesLibraryListCmd(use string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: "List to-do list templates",
		Long:  "List the account's active to-do list templates.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			persistentAccount := hasPersistentAccount(app.Config)
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			contextArgs := templateCommandContextArgs(
				app.Config.ActiveProfile,
				persistentAccount,
				app.Config.AccountID,
			)

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
						Action:      "duplicate",
						Cmd:         "basecamp templates todolists duplicate <template-id> --in <project>" + contextArgs,
						Description: "Duplicate a template into a project",
					},
				),
			)
		},
	}
}

func newTemplatesDuplicateCmd(kind templateCopyKind, use string, aliases ...string) *cobra.Command {
	var project string
	var container string
	var confirmAddingPeople bool

	cmd := &cobra.Command{
		Use:     use,
		Aliases: aliases,
		Short:   "Duplicate a " + kind.noun + " template into a project",
		Long: fmt.Sprintf(`Start duplicating a %s template into a project.

The duplication runs asynchronously. Use 'templates %s duplication' with the
returned ID to check its progress. Referenced people receive project access
only when --confirm-adding-people is explicitly provided.`, kind.noun, kind.group),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			templateID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid template ID")
			}
			if err := requireNumericID(container, kind.containerFlag+" ID"); err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			persistentAccount := hasPersistentAccount(app.Config)
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			contextArgs := templateCommandContextArgs(
				app.Config.ActiveProfile,
				persistentAccount,
				app.Config.AccountID,
			)

			resolvedProjectID, err := resolveProjectID(cmd, app, project)
			if err != nil {
				return err
			}

			req := &basecamp.CreateTemplateLibraryCopyRequest{
				TemplateRecordingID:   templateID,
				AddingPeopleConfirmed: confirmAddingPeople,
			}
			if container == "" {
				projectID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
				if err != nil {
					return output.ErrUsage("Invalid project ID")
				}
				req.DestinationProjectID = projectID
			} else {
				req.DestinationParentID, err = kind.resolveContainer(cmd, app, resolvedProjectID, container)
				if err != nil {
					return err
				}
			}

			templateCopy, err := app.Account().Templates().CreateLibraryCopy(cmd.Context(), req)
			if err != nil {
				return templateCopyError(err, kind, templateID, resolvedProjectID, container, contextArgs)
			}

			return app.OK(templateCopy,
				output.WithSummary(fmt.Sprintf("Started template duplication #%d (%s)", templateCopy.ID, templateCopy.Status)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "status",
						Cmd:         fmt.Sprintf("basecamp templates %s duplication %d%s", kind.group, templateCopy.ID, contextArgs),
						Description: "Check duplication status",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Destination project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Destination project ID or name (alias for --project)")
	cmd.Flags().StringVar(&container, kind.containerFlag, "", kind.containerUsage)
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

func templateCommandContextArgs(profile string, persistentAccount bool, accountID string) string {
	args := ""
	if profile != "" {
		args += " --profile " + shellQuote(profile)
	}
	return args + replyAccountArg(persistentAccount, accountID)
}

func templateCopyError(err error, kind templateCopyKind, templateID int64, projectID, container, contextArgs string) error {
	var confirmationErr *basecamp.PeopleConfirmationRequiredError
	if !errors.As(err, &confirmationErr) {
		return convertSDKError(err)
	}

	people := make([]string, 0, len(confirmationErr.People))
	for _, person := range confirmationErr.People {
		people = append(people, fmt.Sprintf("%s (#%d)", person.Name, person.ID))
	}

	pinned := ""
	if container != "" {
		pinned = fmt.Sprintf(" --%s %s", kind.containerFlag, container)
	}
	rerun := fmt.Sprintf("basecamp templates %s duplicate %d --in %s%s%s --confirm-adding-people",
		kind.group, templateID, projectID, pinned, contextArgs)

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

func newTemplatesDuplicationCmd(kind templateCopyKind, use string, aliases ...string) *cobra.Command {
	return &cobra.Command{
		Use:     use,
		Aliases: aliases,
		Short:   "Check a " + kind.noun + " template duplication",
		Long:    "Check whether a " + kind.noun + " template duplication is pending, processing, completed, or failed.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			copyID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid duplication ID")
			}

			app := appctx.FromContext(cmd.Context())
			persistentAccount := hasPersistentAccount(app.Config)
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			contextArgs := templateCommandContextArgs(
				app.Config.ActiveProfile,
				persistentAccount,
				app.Config.AccountID,
			)

			templateCopy, err := app.Account().Templates().GetLibraryCopy(cmd.Context(), copyID)
			if err != nil {
				return convertSDKError(err)
			}

			summary, breadcrumbs := templateCopyStatusOutput(templateCopy, kind, contextArgs)
			return app.OK(templateCopy,
				output.WithSummary(summary),
				output.WithBreadcrumbs(breadcrumbs...),
			)
		},
	}
}

func templateCopyStatusOutput(templateCopy *basecamp.TemplateLibraryCopy, kind templateCopyKind, contextArgs string) (string, []output.Breadcrumb) {
	switch templateCopy.Status {
	case "completed":
		if summary, breadcrumbs, ok := kind.describeCompletedResult(templateCopy, contextArgs); ok {
			return summary, breadcrumbs
		}
		return fmt.Sprintf("Template duplication #%d completed", templateCopy.ID), nil
	case "failed":
		return fmt.Sprintf("Template duplication #%d failed", templateCopy.ID), []output.Breadcrumb{
			{
				Action:      "list",
				Cmd:         fmt.Sprintf("basecamp templates %s list%s", kind.group, contextArgs),
				Description: "List available templates",
			},
		}
	case "pending", "processing":
		return fmt.Sprintf("Template duplication #%d is %s", templateCopy.ID, templateCopy.Status), []output.Breadcrumb{
			{
				Action:      "poll",
				Cmd:         fmt.Sprintf("basecamp templates %s duplication %d%s", kind.group, templateCopy.ID, contextArgs),
				Description: "Check again",
			},
		}
	default:
		return fmt.Sprintf("Template duplication #%d status: %s", templateCopy.ID, templateCopy.Status), nil
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
						Cmd:         fmt.Sprintf("basecamp templates projects construct %d --name \"Project Name\"", templateID),
						Description: "Create project from template",
					},
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("basecamp templates projects update %d --name \"New Name\"", templateID),
						Description: "Update template",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp templates projects list",
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
						Cmd:         fmt.Sprintf("basecamp templates projects show %d", template.ID),
						Description: "View template",
					},
					output.Breadcrumb{
						Action:      "construct",
						Cmd:         fmt.Sprintf("basecamp templates projects construct %d --name \"Project Name\"", template.ID),
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
						Cmd:         fmt.Sprintf("basecamp templates projects show %d", templateID),
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
						Cmd:         "basecamp templates projects list",
						Description: "List templates",
					},
					output.Breadcrumb{
						Action:      "trashed",
						Cmd:         "basecamp templates projects list --status trashed",
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
	var startDate string

	cmd := &cobra.Command{
		Use:   "construct <template_id>",
		Short: "Create project from template",
		Long: `Create a new project from a template.

This is an asynchronous operation. The command returns a construction ID
which can be polled via 'templates construction' until the status is "completed".

A template's dates are relative to the start of its first week, and template
weeks start on a Sunday. --start-date anchors them to the Sunday on or before
the given date; without it they anchor to the week the project is constructed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if projectName == "" {
				return output.ErrUsage("--name is required (project name)")
			}

			templateID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid template ID")
			}

			var parsedStart string
			if cmd.Flags().Changed("start-date") {
				parsedStart = dateparse.Parse(startDate)
				if _, err := time.Parse("2006-01-02", parsedStart); err != nil {
					return output.ErrUsage(fmt.Sprintf("Invalid start date: %q", startDate))
				}
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

			construction, err := app.Account().Templates().CreateProject(cmd.Context(), templateID, projectName, projectDesc,
				&basecamp.CreateProjectOptions{StartDate: parsedStart})
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(construction,
				output.WithSummary(fmt.Sprintf("Started project construction #%d (%s)", construction.ID, construction.Status)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "status",
						Cmd:         fmt.Sprintf("basecamp templates projects construction %d %d", templateID, construction.ID),
						Description: "Check construction status",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&projectName, "name", "", "Project name (required)")
	cmd.Flags().StringVar(&projectDesc, "description", "", "Project description; use - to read from stdin")
	cmd.Flags().StringVar(&projectDesc, "desc", "", "Project description (alias)")
	cmd.Flags().StringVar(&startDate, "start-date", "", "Project start date (YYYY-MM-DD or natural, e.g. \"next monday\"); template dates anchor to the Sunday of that week")
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
						Cmd:         fmt.Sprintf("basecamp templates projects construction %d %d", templateID, constructionID),
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
