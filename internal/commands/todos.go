package commands

import (
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/dateparse"
	"github.com/basecamp/bcq/internal/output"
)

// todosListFlags holds the flags for the todos list command.
type todosListFlags struct {
	project  string
	todolist string
	assignee string
	status   string
	overdue  bool
}

// NewTodosCmd creates the todos command group.
func NewTodosCmd() *cobra.Command {
	var flags todosListFlags

	cmd := &cobra.Command{
		Use:   "todos",
		Short: "Manage todos",
		Long:  "List, show, create, and manage Basecamp todos.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default to list when called without subcommand
			return runTodosList(cmd, flags)
		},
	}

	// Allow flags on root command for default list behavior
	// Note: can't use -a for assignee since it conflicts with global -a for account
	cmd.Flags().StringVar(&flags.project, "in", "", "Project ID")
	cmd.Flags().StringVarP(&flags.todolist, "list", "l", "", "Todolist ID")
	cmd.Flags().StringVar(&flags.assignee, "assignee", "", "Filter by assignee")
	cmd.Flags().StringVarP(&flags.status, "status", "s", "", "Filter by status (completed, pending)")
	cmd.Flags().BoolVar(&flags.overdue, "overdue", false, "Filter overdue todos")

	cmd.AddCommand(
		newTodosListCmd(),
		newTodosShowCmd(),
		newTodosCreateCmd(),
		newTodosCompleteCmd(),
		newTodosUncompleteCmd(),
		newTodosSweepCmd(),
		newTodosPositionCmd(),
	)

	return cmd
}

// NewDoneCmd creates the 'done' command as an alias for 'todos complete'.
func NewDoneCmd() *cobra.Command {
	return newDoneCmd()
}

// NewReopenCmd creates the 'reopen' command as an alias for 'todos uncomplete'.
func NewReopenCmd() *cobra.Command {
	return newReopenCmd()
}

// NewTodoCmd creates the 'todo' command as a shortcut for 'todos create'.
func NewTodoCmd() *cobra.Command {
	var content string
	var project string
	var todolist string
	var assignee string
	var due string

	cmd := &cobra.Command{
		Use:   "todo",
		Short: "Create a new todo (shortcut for 'todos create')",
		Long:  "Create a new todo in a project. Shortcut for 'bcq todos create'.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			// Validate user input first, before checking account
			if content == "" {
				return output.ErrUsage("Todo content required")
			}

			// Validate assignee format early (before API calls)
			if assignee != "" && !isValidAssignee(assignee) {
				return output.ErrUsageHint(
					"Invalid assignee format",
					"Use a numeric person ID (run 'bcq people' to list)",
				)
			}

			if err := app.SDK.RequireAccount(); err != nil {
				return err
			}

			// Use project from flag or config
			if project == "" {
				project = app.Flags.Project
			}
			if project == "" {
				project = app.Config.ProjectID
			}
			if project == "" {
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			// Resolve project name to ID
			resolvedProject, _, err := app.Names.ResolveProject(cmd.Context(), project)
			if err != nil {
				return err
			}
			project = resolvedProject

			// Use todolist from flag or config
			if todolist == "" {
				todolist = app.Flags.Todolist
			}
			if todolist == "" {
				todolist = app.Config.TodolistID
			}
			// If still no todolist, get first one from project
			if todolist == "" {
				tlID, err := getFirstTodolistID(cmd, app, project)
				if err != nil {
					return err
				}
				todolist = fmt.Sprintf("%d", tlID)
			}

			if todolist == "" {
				return output.ErrUsage("--list is required (no default todolist found)")
			}

			// Resolve todolist name to ID (if it's not already numeric from getFirstTodolistID)
			resolvedTodolist, _, err := app.Names.ResolveTodolist(cmd.Context(), todolist, project)
			if err != nil {
				return err
			}

			// Build SDK request
			req := &basecamp.CreateTodoRequest{
				Content: content,
			}
			if due != "" {
				// Parse natural language date
				parsedDue := dateparse.Parse(due)
				if parsedDue != "" {
					req.DueOn = parsedDue
				}
			}
			if assignee != "" {
				// Resolve assignee name to ID
				assigneeID, _, err := app.Names.ResolvePerson(cmd.Context(), assignee)
				if err != nil {
					return fmt.Errorf("failed to resolve assignee '%s': %w", assignee, err)
				}
				assigneeIDInt, _ := strconv.ParseInt(assigneeID, 10, 64)
				req.AssigneeIDs = []int64{assigneeIDInt}
			}

			projectID, err := strconv.ParseInt(project, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}
			todolistID, err := strconv.ParseInt(resolvedTodolist, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todolist ID")
			}

			todo, err := app.SDK.Todos().Create(cmd.Context(), projectID, todolistID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(todo,
				output.WithSummary(fmt.Sprintf("Created todo #%d", todo.ID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "view",
						Cmd:         fmt.Sprintf("bcq todos show %d --project %s", todo.ID, project),
						Description: "View todo",
					},
					output.Breadcrumb{
						Action:      "complete",
						Cmd:         fmt.Sprintf("bcq done %d", todo.ID),
						Description: "Complete todo",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq todos --in %s", project),
						Description: "List todos",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&content, "content", "c", "", "Todo content (required)")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().StringVarP(&todolist, "list", "l", "", "Todolist ID")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Assignee ID or name")
	cmd.Flags().StringVar(&assignee, "to", "", "Assignee (alias for --assignee)")
	cmd.Flags().StringVarP(&due, "due", "d", "", "Due date")

	return cmd
}

func newTodosListCmd() *cobra.Command {
	var flags todosListFlags

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List todos",
		Long:  "List todos in a project or todolist.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTodosList(cmd, flags)
		},
	}

	// Note: can't use -a for assignee since it conflicts with global -a for account
	cmd.Flags().StringVar(&flags.project, "in", "", "Project ID")
	cmd.Flags().StringVarP(&flags.todolist, "list", "l", "", "Todolist ID")
	cmd.Flags().StringVar(&flags.assignee, "assignee", "", "Filter by assignee")
	cmd.Flags().StringVarP(&flags.status, "status", "s", "", "Filter by status (completed, pending)")
	cmd.Flags().BoolVar(&flags.overdue, "overdue", false, "Filter overdue todos")

	return cmd
}

func runTodosList(cmd *cobra.Command, flags todosListFlags) error {
	app := appctx.FromContext(cmd.Context())
	if app == nil {
		return fmt.Errorf("app not initialized")
	}

	// Use project from flag or config
	project := flags.project
	if project == "" {
		project = app.Flags.Project
	}
	if project == "" {
		project = app.Config.ProjectID
	}
	// Validate project before checking account
	if project == "" {
		return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
	}

	if err := app.SDK.RequireAccount(); err != nil {
		return err
	}

	// Resolve project name to ID
	resolvedProject, _, err := app.Names.ResolveProject(cmd.Context(), project)
	if err != nil {
		return err
	}
	project = resolvedProject

	// Use todolist from flag or config
	todolist := flags.todolist
	if todolist == "" {
		todolist = app.Flags.Todolist
	}
	if todolist == "" {
		todolist = app.Config.TodolistID
	}

	// If todolist is specified, list todos in that list
	if todolist != "" {
		return listTodosInList(cmd, app, project, todolist, flags.status)
	}

	// Otherwise, get all todos from project's todoset
	return listAllTodos(cmd, app, project, flags.assignee, flags.status, flags.overdue)
}

func listTodosInList(cmd *cobra.Command, app *appctx.App, project, todolist, status string) error {
	// Resolve todolist name to ID
	resolvedTodolist, _, err := app.Names.ResolveTodolist(cmd.Context(), todolist, project)
	if err != nil {
		return err
	}

	projectID, err := strconv.ParseInt(project, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}
	todolistID, err := strconv.ParseInt(resolvedTodolist, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid todolist ID")
	}

	opts := &basecamp.TodoListOptions{}
	if status != "" {
		opts.Status = status
	}

	todos, err := app.SDK.Todos().List(cmd.Context(), projectID, todolistID, opts)
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(todos,
		output.WithSummary(fmt.Sprintf("%d todos", len(todos))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("bcq todos create --content <text> --in %s --list %s", project, resolvedTodolist),
				Description: "Create a todo",
			},
			output.Breadcrumb{
				Action:      "complete",
				Cmd:         "bcq done <id>",
				Description: "Complete a todo",
			},
		),
	)
}

func listAllTodos(cmd *cobra.Command, app *appctx.App, project, assignee, status string, overdue bool) error {
	// Resolve assignee name to ID if provided
	var assigneeID int64
	if assignee != "" {
		resolvedID, _, err := app.Names.ResolvePerson(cmd.Context(), assignee)
		if err != nil {
			return fmt.Errorf("failed to resolve assignee '%s': %w", assignee, err)
		}
		assigneeID, _ = strconv.ParseInt(resolvedID, 10, 64)
	}

	// Parse project ID
	bucketID, err := strconv.ParseInt(project, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	// Get todoset ID from project dock
	todosetIDStr, err := getTodosetID(cmd, app, project)
	if err != nil {
		return err
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

	// Aggregate todos from all todolists
	var allTodos []basecamp.Todo
	for _, tl := range todolists {
		todos, err := app.SDK.Todos().List(cmd.Context(), bucketID, tl.ID, nil)
		if err != nil {
			continue // Skip failed todolists
		}
		allTodos = append(allTodos, todos...)
	}

	// Apply filters
	var result []basecamp.Todo
	for _, todo := range allTodos {
		// Filter by status
		if status != "" {
			if status == "completed" && !todo.Completed {
				continue
			}
			if status == "pending" && todo.Completed {
				continue
			}
		}

		// Filter by assignee (using resolved ID)
		if assigneeID != 0 {
			found := false
			for _, a := range todo.Assignees {
				if a.ID == assigneeID {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Filter overdue - check if due date is in the past and not completed
		if overdue {
			if todo.DueOn == "" || todo.Completed {
				continue
			}
			// Compare date strings directly (timezone-safe)
			today := time.Now().Format("2006-01-02")
			if todo.DueOn >= today {
				continue // Not overdue
			}
		}

		result = append(result, todo)
	}

	return app.OK(result,
		output.WithSummary(fmt.Sprintf("%d todos", len(result))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("bcq todos create --content <text> --in %s", project),
				Description: "Create a todo",
			},
			output.Breadcrumb{
				Action:      "complete",
				Cmd:         "bcq done <id>",
				Description: "Complete a todo",
			},
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "bcq todos show <id>",
				Description: "Show todo details",
			},
		),
	)
}

func newTodosShowCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show todo details",
		Long:  "Display detailed information about a todo.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			// Use project from flag or config
			if project == "" {
				project = app.Flags.Project
			}
			if project == "" {
				project = app.Config.ProjectID
			}
			if project == "" {
				return output.ErrUsage("--project is required")
			}

			// Resolve project name to ID
			resolvedProject, _, err := app.Names.ResolveProject(cmd.Context(), project)
			if err != nil {
				return err
			}

			projectID, err := strconv.ParseInt(resolvedProject, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			todoID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todo ID")
			}

			todo, err := app.SDK.Todos().Get(cmd.Context(), projectID, todoID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(todo,
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "complete",
						Cmd:         fmt.Sprintf("bcq done %d", todoID),
						Description: "Complete this todo",
					},
					output.Breadcrumb{
						Action:      "comment",
						Cmd:         fmt.Sprintf("bcq comment --on %d --content <text>", todoID),
						Description: "Add comment",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	return cmd
}

func newTodosCreateCmd() *cobra.Command {
	var content string
	var project string
	var todolist string
	var assignee string
	var due string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new todo",
		Long:  "Create a new todo in a project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			// Validate user input first, before checking account
			if content == "" {
				return output.ErrUsage("Todo content required")
			}

			// Validate assignee format early (before API calls)
			if assignee != "" && !isValidAssignee(assignee) {
				return output.ErrUsageHint(
					"Invalid assignee format",
					"Use a numeric person ID (run 'bcq people' to list)",
				)
			}

			if err := app.SDK.RequireAccount(); err != nil {
				return err
			}

			// Use project from flag or config
			if project == "" {
				project = app.Flags.Project
			}
			if project == "" {
				project = app.Config.ProjectID
			}
			if project == "" {
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			// Resolve project name to ID
			resolvedProject, _, err := app.Names.ResolveProject(cmd.Context(), project)
			if err != nil {
				return err
			}
			project = resolvedProject

			// Use todolist from flag or config
			if todolist == "" {
				todolist = app.Flags.Todolist
			}
			if todolist == "" {
				todolist = app.Config.TodolistID
			}
			// If still no todolist, get first one from project
			if todolist == "" {
				tlID, err := getFirstTodolistID(cmd, app, project)
				if err != nil {
					return err
				}
				todolist = fmt.Sprintf("%d", tlID)
			}

			if todolist == "" {
				return output.ErrUsage("--list is required (no default todolist found)")
			}

			// Resolve todolist name to ID (if it's not already numeric from getFirstTodolistID)
			resolvedTodolist, _, err := app.Names.ResolveTodolist(cmd.Context(), todolist, project)
			if err != nil {
				return err
			}

			// Build SDK request
			req := &basecamp.CreateTodoRequest{
				Content: content,
			}
			if due != "" {
				// Parse natural language date
				parsedDue := dateparse.Parse(due)
				if parsedDue != "" {
					req.DueOn = parsedDue
				}
			}
			if assignee != "" {
				// Resolve assignee name to ID
				assigneeID, _, err := app.Names.ResolvePerson(cmd.Context(), assignee)
				if err != nil {
					return fmt.Errorf("failed to resolve assignee '%s': %w", assignee, err)
				}
				assigneeIDInt, _ := strconv.ParseInt(assigneeID, 10, 64)
				req.AssigneeIDs = []int64{assigneeIDInt}
			}

			projectID, err := strconv.ParseInt(project, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}
			todolistID, err := strconv.ParseInt(resolvedTodolist, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todolist ID")
			}

			todo, err := app.SDK.Todos().Create(cmd.Context(), projectID, todolistID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(todo,
				output.WithSummary(fmt.Sprintf("Created todo #%d", todo.ID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "view",
						Cmd:         fmt.Sprintf("bcq todos show %d --project %s", todo.ID, project),
						Description: "View todo",
					},
					output.Breadcrumb{
						Action:      "complete",
						Cmd:         fmt.Sprintf("bcq done %d", todo.ID),
						Description: "Complete todo",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq todos --in %s", project),
						Description: "List todos",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&content, "content", "c", "", "Todo content (required)")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().StringVarP(&todolist, "list", "l", "", "Todolist ID")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Assignee ID")
	cmd.Flags().StringVar(&assignee, "to", "", "Assignee ID (alias for --assignee)")
	cmd.Flags().StringVarP(&due, "due", "d", "", "Due date (YYYY-MM-DD)")

	return cmd
}

func getFirstTodolistID(cmd *cobra.Command, app *appctx.App, project string) (int64, error) {
	// Parse project ID
	bucketID, err := strconv.ParseInt(project, 10, 64)
	if err != nil {
		return 0, output.ErrUsage("Invalid project ID")
	}

	// Get todoset ID from project dock
	todosetIDStr, err := getTodosetID(cmd, app, project)
	if err != nil {
		return 0, err
	}
	todosetID, err := strconv.ParseInt(todosetIDStr, 10, 64)
	if err != nil {
		return 0, output.ErrUsage("Invalid todoset ID")
	}

	// Get first todolist via SDK
	todolists, err := app.SDK.Todolists().List(cmd.Context(), bucketID, todosetID, nil)
	if err != nil {
		return 0, convertSDKError(err)
	}

	if len(todolists) == 0 {
		return 0, output.ErrNotFound("todolists", project)
	}

	return todolists[0].ID, nil
}

func newTodosCompleteCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "complete <id> [id...]",
		Short: "Complete todo(s)",
		Long:  "Mark one or more todos as completed.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return completeTodos(cmd, args, project)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	return cmd
}

func newDoneCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "done <id> [id...]",
		Short: "Complete todo(s)",
		Long:  "Mark one or more todos as completed.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return completeTodos(cmd, args, project)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	return cmd
}

func completeTodos(cmd *cobra.Command, todoIDs []string, project string) error {
	app := appctx.FromContext(cmd.Context())
	if app == nil {
		return fmt.Errorf("app not initialized")
	}

	// Use project from flag or config
	if project == "" {
		project = app.Flags.Project
	}
	if project == "" {
		project = app.Config.ProjectID
	}
	if project == "" {
		return output.ErrUsage("--project is required")
	}

	// Resolve project name to ID
	resolvedProject, _, err := app.Names.ResolveProject(cmd.Context(), project)
	if err != nil {
		return err
	}

	projectID, err := strconv.ParseInt(resolvedProject, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	var completed []string
	var failed []string

	for _, todoIDStr := range todoIDs {
		todoID, err := strconv.ParseInt(todoIDStr, 10, 64)
		if err != nil {
			failed = append(failed, todoIDStr)
			continue
		}
		err = app.SDK.Todos().Complete(cmd.Context(), projectID, todoID)
		if err != nil {
			failed = append(failed, todoIDStr)
		} else {
			completed = append(completed, todoIDStr)
		}
	}

	result := map[string]any{
		"completed": completed,
		"failed":    failed,
	}

	summary := fmt.Sprintf("Completed %d todo(s)", len(completed))
	if len(failed) > 0 {
		summary = fmt.Sprintf("Completed %d, failed %d", len(completed), len(failed))
	}

	return app.OK(result,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "list",
				Cmd:         fmt.Sprintf("bcq todos --in %s", resolvedProject),
				Description: "List remaining todos",
			},
			output.Breadcrumb{
				Action:      "reopen",
				Cmd:         fmt.Sprintf("bcq reopen %s", todoIDs[0]),
				Description: "Reopen a todo",
			},
		),
	)
}

func newTodosUncompleteCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:     "uncomplete <id> [id...]",
		Aliases: []string{"reopen"},
		Short:   "Reopen todo(s)",
		Long:    "Reopen one or more completed todos.",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return reopenTodos(cmd, args, project)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	return cmd
}

// SweepResult contains the results of a sweep operation.
type SweepResult struct {
	DryRun         bool    `json:"dry_run,omitempty"`
	WouldSweep     []int64 `json:"would_sweep,omitempty"`
	Swept          []int64 `json:"swept,omitempty"`
	Commented      []int64 `json:"commented,omitempty"`
	Completed      []int64 `json:"completed,omitempty"`
	CommentFailed  []int64 `json:"comment_failed,omitempty"`
	CompleteFailed []int64 `json:"complete_failed,omitempty"`
	Count          int     `json:"count"`
	Comment        string  `json:"comment,omitempty"`
	CompleteAction bool    `json:"complete,omitempty"`
}

func newTodosSweepCmd() *cobra.Command {
	var project string
	var assignee string
	var comment string
	var overdueOnly bool
	var complete bool
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "Bulk process matching todos",
		Long: `Sweep finds todos matching filters and applies actions to them.

Filters (at least one required):
  --overdue    Select todos past their due date
  --assignee   Select todos assigned to a specific person

Actions (at least one required):
  --comment    Add a comment to matching todos
  --complete   Mark matching todos as complete

Examples:
  # Preview overdue todos without taking action
  bcq todos sweep --overdue --dry-run

  # Complete all overdue todos with a comment
  bcq todos sweep --overdue --complete --comment "Cleaning up overdue items"

  # Add comment to all todos assigned to me
  bcq todos sweep --assignee me --comment "Following up"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.SDK.RequireAccount(); err != nil {
				return err
			}

			// Require at least one filter
			if !overdueOnly && assignee == "" {
				return output.ErrUsageHint("Sweep requires a filter", "Use --overdue or --assignee to select todos")
			}

			// Require at least one action
			if comment == "" && !complete {
				return output.ErrUsageHint("Sweep requires an action", "Use --comment and/or --complete")
			}

			// Resolve project
			if project == "" {
				project = app.Flags.Project
			}
			if project == "" {
				project = app.Config.ProjectID
			}
			if project == "" {
				return output.ErrUsage("--project is required")
			}

			// Resolve project name to ID
			resolvedProject, _, err := app.Names.ResolveProject(cmd.Context(), project)
			if err != nil {
				return err
			}
			project = resolvedProject

			// Get matching todos using existing listAllTodos logic
			matchingTodos, err := getTodosForSweep(cmd, app, project, assignee, overdueOnly)
			if err != nil {
				return err
			}

			if len(matchingTodos) == 0 {
				return app.OK(SweepResult{Count: 0},
					output.WithSummary("No todos match the filter"),
				)
			}

			// Extract IDs
			todoIDs := make([]int64, len(matchingTodos))
			for i, t := range matchingTodos {
				todoIDs[i] = t.ID
			}

			// Dry run - just show what would happen
			if dryRun {
				return app.OK(SweepResult{
					DryRun:         true,
					WouldSweep:     todoIDs,
					Count:          len(todoIDs),
					Comment:        comment,
					CompleteAction: complete,
				},
					output.WithSummary(fmt.Sprintf("Would sweep %d todo(s)", len(todoIDs))),
				)
			}

			// Parse project ID for SDK calls
			bucketID, err := strconv.ParseInt(project, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			// Execute actions
			result := SweepResult{
				Count:          len(todoIDs),
				Comment:        comment,
				CompleteAction: complete,
			}

			for _, todoID := range todoIDs {
				result.Swept = append(result.Swept, todoID)

				// Add comment if specified
				if comment != "" {
					req := &basecamp.CreateCommentRequest{Content: comment}
					_, commentErr := app.SDK.Comments().Create(cmd.Context(), bucketID, todoID, req)
					if commentErr != nil {
						result.CommentFailed = append(result.CommentFailed, todoID)
					} else {
						result.Commented = append(result.Commented, todoID)
					}
				}

				// Complete if specified
				if complete {
					completeErr := app.SDK.Todos().Complete(cmd.Context(), bucketID, todoID)
					if completeErr != nil {
						result.CompleteFailed = append(result.CompleteFailed, todoID)
					} else {
						result.Completed = append(result.Completed, todoID)
					}
				}
			}

			summary := fmt.Sprintf("Swept %d todo(s)", len(result.Swept))
			if len(result.Commented) > 0 {
				summary += fmt.Sprintf(", commented %d", len(result.Commented))
			}
			if len(result.Completed) > 0 {
				summary += fmt.Sprintf(", completed %d", len(result.Completed))
			}

			return app.OK(result,
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq todos --in %s", project),
						Description: "List todos",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Filter by assignee")
	cmd.Flags().BoolVar(&overdueOnly, "overdue", false, "Filter overdue todos")
	cmd.Flags().StringVarP(&comment, "comment", "c", "", "Comment to add to matching todos")
	cmd.Flags().BoolVar(&complete, "complete", false, "Mark matching todos as complete")
	cmd.Flags().BoolVar(&complete, "done", false, "Mark matching todos as complete (alias)")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "Preview without making changes")

	return cmd
}

// getTodosForSweep gets todos matching the sweep filters.
func getTodosForSweep(cmd *cobra.Command, app *appctx.App, project, assignee string, overdue bool) ([]basecamp.Todo, error) {
	// Resolve assignee name to ID if provided
	var assigneeID int64
	if assignee != "" {
		resolvedID, _, err := app.Names.ResolvePerson(cmd.Context(), assignee)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve assignee '%s': %w", assignee, err)
		}
		assigneeID, _ = strconv.ParseInt(resolvedID, 10, 64)
	}

	// Parse project ID
	bucketID, err := strconv.ParseInt(project, 10, 64)
	if err != nil {
		return nil, output.ErrUsage("Invalid project ID")
	}

	// Get todoset ID from project dock
	todosetIDStr, err := getTodosetID(cmd, app, project)
	if err != nil {
		return nil, err
	}
	todosetID, err := strconv.ParseInt(todosetIDStr, 10, 64)
	if err != nil {
		return nil, output.ErrUsage("Invalid todoset ID")
	}

	// Get todolists via SDK
	todolists, err := app.SDK.Todolists().List(cmd.Context(), bucketID, todosetID, nil)
	if err != nil {
		return nil, convertSDKError(err)
	}

	// Aggregate todos from all todolists
	var allTodos []basecamp.Todo
	for _, tl := range todolists {
		todos, err := app.SDK.Todos().List(cmd.Context(), bucketID, tl.ID, nil)
		if err != nil {
			continue // Skip failed todolists
		}
		allTodos = append(allTodos, todos...)
	}

	// Apply filters
	var result []basecamp.Todo
	for _, todo := range allTodos {
		// Skip completed todos
		if todo.Completed {
			continue
		}

		// Filter by assignee
		if assigneeID != 0 {
			found := false
			for _, a := range todo.Assignees {
				if a.ID == assigneeID {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Filter overdue
		if overdue {
			if todo.DueOn == "" {
				continue
			}
			// Compare date strings directly (timezone-safe)
			today := time.Now().Format("2006-01-02")
			if todo.DueOn >= today {
				continue // Not overdue
			}
		}

		result = append(result, todo)
	}

	return result, nil
}

func newReopenCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "reopen <id> [id...]",
		Short: "Reopen todo(s)",
		Long:  "Reopen one or more completed todos.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return reopenTodos(cmd, args, project)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	return cmd
}

func reopenTodos(cmd *cobra.Command, todoIDs []string, project string) error {
	app := appctx.FromContext(cmd.Context())
	if app == nil {
		return fmt.Errorf("app not initialized")
	}

	// Use project from flag or config
	if project == "" {
		project = app.Flags.Project
	}
	if project == "" {
		project = app.Config.ProjectID
	}
	if project == "" {
		return output.ErrUsage("--project is required")
	}

	// Resolve project name to ID
	resolvedProject, _, err := app.Names.ResolveProject(cmd.Context(), project)
	if err != nil {
		return err
	}

	projectID, err := strconv.ParseInt(resolvedProject, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	var reopened []string
	var failed []string

	for _, todoIDStr := range todoIDs {
		todoID, err := strconv.ParseInt(todoIDStr, 10, 64)
		if err != nil {
			failed = append(failed, todoIDStr)
			continue
		}
		err = app.SDK.Todos().Uncomplete(cmd.Context(), projectID, todoID)
		if err != nil {
			failed = append(failed, todoIDStr)
		} else {
			reopened = append(reopened, todoIDStr)
		}
	}

	result := map[string]any{
		"reopened": reopened,
		"failed":   failed,
	}

	summary := fmt.Sprintf("Reopened %d todo(s)", len(reopened))
	if len(failed) > 0 {
		summary = fmt.Sprintf("Reopened %d, failed %d", len(reopened), len(failed))
	}

	return app.OK(result,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "list",
				Cmd:         fmt.Sprintf("bcq todos --in %s", resolvedProject),
				Description: "List todos",
			},
			output.Breadcrumb{
				Action:      "complete",
				Cmd:         fmt.Sprintf("bcq done %s", todoIDs[0]),
				Description: "Complete again",
			},
		),
	)
}

func newTodosPositionCmd() *cobra.Command {
	var project string
	var position int

	cmd := &cobra.Command{
		Use:     "position <id>",
		Aliases: []string{"move", "reorder"},
		Short:   "Change todo position",
		Long:    "Reorder a todo within its todolist. Position is 1-based (1 = top).",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if position == 0 {
				return output.ErrUsage("--to is required (1 = top)")
			}

			// Use project from flag or config
			if project == "" {
				project = app.Flags.Project
			}
			if project == "" {
				project = app.Config.ProjectID
			}
			if project == "" {
				return output.ErrUsage("--project is required")
			}

			// Resolve project name to ID
			resolvedProject, _, err := app.Names.ResolveProject(cmd.Context(), project)
			if err != nil {
				return err
			}

			projectID, err := strconv.ParseInt(resolvedProject, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			todoID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todo ID")
			}

			err = app.SDK.Todos().Reposition(cmd.Context(), projectID, todoID, position)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"repositioned": true, "position": position},
				output.WithSummary(fmt.Sprintf("Moved todo #%d to position %d", todoID, position)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq todos show %d --in %s", todoID, resolvedProject),
						Description: "View todo",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq todos --in %s", resolvedProject),
						Description: "List todos",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().IntVar(&position, "to", 0, "Target position, 1-based (1 = top)")
	cmd.Flags().IntVar(&position, "position", 0, "Target position (alias for --to)")
	_ = cmd.MarkFlagRequired("to")

	return cmd
}
