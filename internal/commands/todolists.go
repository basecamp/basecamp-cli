package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewTodolistsCmd creates the todolists command group.
func NewTodolistsCmd() *cobra.Command {
	var project string
	var todosetID string

	cmd := &cobra.Command{
		Use:     "todolists",
		Aliases: []string{"todolist"},
		Short:   "Manage todolists",
		Long: `Manage todolists in a project.

A "todoset" is the container; "todolists" are the actual lists inside it.
Most projects have one todoset, but some may have multiple. Use --todoset
to disambiguate when needed.`,
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	cmd.AddCommand(
		newTodolistsListCmd(&project, &todosetID),
		newTodolistsShowCmd(&project),
		newTodolistsCreateCmd(&project, &todosetID),
		newTodolistsUpdateCmd(&project),
		newRecordableTrashCmd("todolist"),
		newRecordableArchiveCmd("todolist"),
		newRecordableRestoreCmd("todolist"),
	)

	return cmd
}

func newTodolistsListCmd(project, todosetID *string) *cobra.Command {
	var limit, page int
	var all, archived bool
	var sortField string
	var reverse bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List todolists",
		Long:  "List all todolists in a project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTodolistsList(cmd, *project, *todosetID, limit, page, all, archived, sortField, reverse)
		},
	}

	cmd.Flags().StringVarP(todosetID, "todoset", "t", "", "Todoset ID (for projects with multiple todosets)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of todolists to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all todolists (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")
	cmd.Flags().BoolVar(&archived, "archived", false, "Show archived todolists")
	cmd.Flags().StringVar(&sortField, "sort", "", "Sort by field (title, created, updated, position)")
	cmd.Flags().BoolVar(&reverse, "reverse", false, "Reverse sort order")

	return cmd
}

func runTodolistsList(cmd *cobra.Command, project, todosetFlag string, limit, page int, all, archived bool, sortField string, reverse bool) error {
	app := appctx.FromContext(cmd.Context())
	if app == nil {
		return fmt.Errorf("app not initialized")
	}

	// Validate flag combinations
	if all && limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if page > 0 && (all || limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}
	if page > 1 {
		return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
	}
	if sortField != "" {
		if err := validateSortField(sortField, []string{"title", "created", "updated", "position"}); err != nil {
			return err
		}
	}

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Resolve project, with interactive fallback
	projectID := project
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

	// Get todoset from project dock (with interactive fallback for multi-todoset projects)
	todosetIDStr, err := ensureTodoset(cmd, app, resolvedProjectID, todosetFlag)
	if err != nil {
		return err
	}

	// Parse todoset ID as int64
	todosetID, err := strconv.ParseInt(todosetIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid todoset ID")
	}

	// Build pagination options
	opts := &basecamp.TodolistListOptions{}
	if archived {
		opts.Status = "archived"
	}
	if all {
		opts.Limit = 0 // SDK treats 0 as "fetch all" for todolists
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}

	// Get todolists via SDK
	todolistsResult, err := app.Account().Todolists().List(cmd.Context(), todosetID, opts)
	if err != nil {
		return convertSDKError(err)
	}
	todolists := todolistsResult.Todolists

	if sortField != "" {
		sortTodolists(todolists, sortField, reverse)
	}

	respOpts := []output.ResponseOption{
		output.WithEntity("todolist"),
		output.WithSummary(fmt.Sprintf("%d todolists", len(todolists))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "todos",
				Cmd:         "basecamp todos --list <id>",
				Description: "List todos in list",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("basecamp todolists create <name> --in %s", resolvedProjectID),
				Description: "Create todolist",
			},
		),
	}

	// Add truncation notice if results may be limited
	if notice := output.TruncationNoticeWithTotal(len(todolists), todolistsResult.Meta.TotalCount); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}

	return app.OK(todolists, respOpts...)
}

func newTodolistsShowCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show todolist details",
		Long: `Display detailed information about a todolist.

You can pass either a todolist ID or a Basecamp URL:
  basecamp todolists show 789 --in my-project
  basecamp todolists show https://3.basecamp.com/123/buckets/456/todolists/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			todolistIDStr, urlProjectID := extractWithProject(args[0])

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
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
			}

			// Parse todolist ID as int64
			todolistID, err := strconv.ParseInt(todolistIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todolist ID")
			}

			// Get todolist via SDK
			todolist, err := app.Account().Todolists().Get(cmd.Context(), todolistID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(todolist,
				output.WithEntity("todolist"),
				output.WithSummary(fmt.Sprintf("Todolist: %s", todolist.Name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "todos",
						Cmd:         fmt.Sprintf("basecamp todos --list %s", todolistIDStr),
						Description: "List todos",
					},
					output.Breadcrumb{
						Action:      "add_todo",
						Cmd:         fmt.Sprintf("basecamp todo <content> --list %s", todolistIDStr),
						Description: "Add todo",
					},
				),
			)
		},
	}
	return cmd
}

func newTodolistsCreateCmd(project, todosetID *string) *cobra.Command {
	var description string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new todolist",
		Long:  "Create a new todolist in a project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no arguments
			if len(args) == 0 {
				return missingArg(cmd, "<name>")
			}

			name := args[0]

			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
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

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Get todoset from project dock (with interactive fallback for multi-todoset projects)
			todosetIDStr, err := ensureTodoset(cmd, app, resolvedProjectID, *todosetID)
			if err != nil {
				return err
			}

			// Parse todoset ID as int64
			tsID, err := strconv.ParseInt(todosetIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todoset ID")
			}

			// Build SDK request
			req := &basecamp.CreateTodolistRequest{
				Name:        name,
				Description: description,
			}

			// Create todolist via SDK
			todolist, err := app.Account().Todolists().Create(cmd.Context(), tsID, req)
			if err != nil {
				return convertSDKError(err)
			}

			todolistIDStr := fmt.Sprintf("%d", todolist.ID)

			return app.OK(todolist,
				output.WithEntity("todolist"),
				output.WithSummary(fmt.Sprintf("Created todolist #%s: %s", todolistIDStr, name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp todolists show %s", todolistIDStr),
						Description: "View todolist",
					},
					output.Breadcrumb{
						Action:      "add_todo",
						Cmd:         fmt.Sprintf("basecamp todo <content> --list %s", todolistIDStr),
						Description: "Add todo",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(todosetID, "todoset", "t", "", "Todoset ID (for projects with multiple todosets)")
	cmd.Flags().StringVarP(&description, "description", "d", "", "Todolist description")

	return cmd
}

func newTodolistsUpdateCmd(project *string) *cobra.Command {
	var name string
	var description string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a todolist",
		Long: `Update an existing todolist's name or description.

You can pass either a todolist ID or a Basecamp URL:
  basecamp todolists update 789 --name "new name" --in my-project
  basecamp todolists update 789 --description "new desc" --in my-project`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" && description == "" {
				return noChanges(cmd)
			}

			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			todolistIDStr, urlProjectID := extractWithProject(args[0])

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
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
			}

			// Parse todolist ID as int64
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
			todolist, err := app.Account().Todolists().Update(cmd.Context(), todolistID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(todolist,
				output.WithEntity("todolist"),
				output.WithSummary(fmt.Sprintf("Updated todolist #%s", todolistIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp todolists show %s", todolistIDStr),
						Description: "View todolist",
					},
					output.Breadcrumb{
						Action:      "todos",
						Cmd:         fmt.Sprintf("basecamp todos --list %s", todolistIDStr),
						Description: "List todos",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "New name")
	cmd.Flags().StringVarP(&description, "description", "d", "", "New description")

	return cmd
}
