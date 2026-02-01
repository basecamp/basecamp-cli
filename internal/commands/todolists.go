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
	var limit, page int
	var all bool

	cmd := &cobra.Command{
		Use:     "todolists",
		Aliases: []string{"todolist"},
		Short:   "Manage todolists",
		Long: `Manage todolists in a project.

A "todoset" is the container; "todolists" are the actual lists inside it.
Each project has one todoset containing multiple todolists.`,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			return ensureAccount(cmd, app)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default to list when called without subcommand
			return runTodolistsList(cmd, project, limit, page, all)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of todolists to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all todolists (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Disable pagination and return first page only")

	cmd.AddCommand(
		newTodolistsListCmd(&project),
		newTodolistsShowCmd(&project),
		newTodolistsCreateCmd(&project),
		newTodolistsUpdateCmd(&project),
	)

	return cmd
}

func newTodolistsListCmd(project *string) *cobra.Command {
	var limit, page int
	var all bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List todolists",
		Long:  "List all todolists in a project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTodolistsList(cmd, *project, limit, page, all)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of todolists to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all todolists (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Disable pagination and return first page only")

	return cmd
}

func runTodolistsList(cmd *cobra.Command, project string, limit, page int, all bool) error {
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

	// Build pagination options
	opts := &basecamp.TodolistListOptions{}
	if all {
		opts.Limit = 0 // SDK treats 0 as "fetch all" for todolists
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}

	// Get todolists via SDK
	todolistsResult, err := app.Account().Todolists().List(cmd.Context(), bucketID, todosetID, opts)
	if err != nil {
		return convertSDKError(err)
	}
	todolists := todolistsResult.Todolists

	respOpts := []output.ResponseOption{
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
  bcq todolists show 789 --in my-project
  bcq todolists show https://3.basecamp.com/123/buckets/456/todolists/789`,
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
				projectID = app.Config.ProjectID
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
			todolist, err := app.Account().Todolists().Get(cmd.Context(), bucketID, todolistID)
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
			todolist, err := app.Account().Todolists().Create(cmd.Context(), bucketID, todosetID, req)
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
		Use:   "update <id|url>",
		Short: "Update a todolist",
		Long: `Update an existing todolist's name or description.

You can pass either a todolist ID or a Basecamp URL:
  bcq todolists update 789 --name "new name" --in my-project
  bcq todolists update https://3.basecamp.com/123/buckets/456/todolists/789 --name "new name"`,
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

			if name == "" && description == "" {
				return output.ErrUsage("at least one of --name or --description is required")
			}

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
				projectID = app.Config.ProjectID
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
			todolist, err := app.Account().Todolists().Update(cmd.Context(), bucketID, todolistID, req)
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
