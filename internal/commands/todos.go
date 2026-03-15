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
	"github.com/basecamp/basecamp-cli/internal/completion"
	"github.com/basecamp/basecamp-cli/internal/dateparse"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// todosListFlags holds the flags for the todos list command.
type todosListFlags struct {
	project   string
	todolist  string
	todoset   string
	assignee  string
	status    string
	completed bool
	overdue   bool
	limit     int
	page      int
	all       bool
	sortField string
	reverse   bool
}

// NewTodosCmd creates the todos command group.
func NewTodosCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "todos",
		Short:       "Manage todos",
		Long:        "List, show, create, and manage Basecamp todos.",
		Annotations: map[string]string{"agent_notes": "--assignee only works on todos, not cards or other content types\nbasecamp done accepts multiple IDs: basecamp done 1 2 3\n--assignee and --overdue require a project (--in, global flag, or config default); for cross-project use basecamp reports assigned/overdue"},
	}

	cmd.AddCommand(
		newTodosListCmd(),
		newTodosShowCmd(),
		newTodosCreateCmd(),
		newTodosCompleteCmd(),
		newTodosUncompleteCmd(),
		newTodosSweepCmd(),
		newTodosPositionCmd(),
		newRecordableTrashCmd("todo"),
		newRecordableArchiveCmd("todo"),
		newRecordableRestoreCmd("todo"),
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
	var project string
	var todolist string
	var todoset string
	var assignee string
	var due string
	var description string
	var attachFiles []string

	cmd := &cobra.Command{
		Use:   "todo <content>",
		Short: "Create a new todo (shortcut for 'todos create')",
		Long:  "Create a new todo in a project. Shortcut for 'basecamp todos create'.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			// Show help when invoked with no content
			if len(args) == 0 {
				return missingArg(cmd, "<content>")
			}
			content := strings.Join(args, " ")
			if strings.TrimSpace(content) == "" {
				return cmd.Help()
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Use project from flag or config, with interactive fallback
			if project == "" {
				project = app.Flags.Project
			}
			if project == "" {
				project = app.Config.ProjectID
			}
			if project == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				project = app.Config.ProjectID
			}

			// Resolve project name to ID
			resolvedProject, _, err := app.Names.ResolveProject(cmd.Context(), project)
			if err != nil {
				return err
			}
			project = resolvedProject

			// Use todolist from flag, config, or interactive prompt
			if todolist == "" {
				todolist = app.Flags.Todolist
			}
			if todolist == "" {
				todolist = app.Config.TodolistID
			}
			// If still no todolist, try interactive selection (todoset-scoped)
			if todolist == "" {
				selectedTodolist, err := ensureTodolist(cmd, app, project, todoset)
				if err != nil {
					return err
				}
				todolist = selectedTodolist
			}

			if todolist == "" {
				return output.ErrUsage("--list is required (no default todolist found)")
			}

			// Resolve todolist name to ID, scoped to --todoset when provided
			resolvedTodolist, err := resolveTodolistInTodoset(cmd, app, todolist, project, todoset)
			if err != nil {
				return err
			}

			// Build SDK request
			// Content is plain text (todo title) - do not wrap in HTML
			req := &basecamp.CreateTodoRequest{
				Content: content,
			}

			// Process description with Markdown + attachments
			if description != "" || len(attachFiles) > 0 {
				descHTML := richtext.MarkdownToHTML(description)

				// Resolve inline images
				descHTML, descErr := resolveLocalImages(cmd, app, descHTML)
				if descErr != nil {
					return descErr
				}

				// Upload explicit --attach files and embed
				if len(attachFiles) > 0 {
					refs, attachErr := uploadAttachments(cmd, app, attachFiles)
					if attachErr != nil {
						return attachErr
					}
					descHTML = richtext.EmbedAttachments(descHTML, refs)
				}

				req.Description = descHTML
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

			todolistID, err := strconv.ParseInt(resolvedTodolist, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todolist ID")
			}

			todo, err := app.Account().Todos().Create(cmd.Context(), todolistID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(todo,
				output.WithEntity("todo"),
				output.WithSummary(fmt.Sprintf("Created todo #%d", todo.ID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "view",
						Cmd:         fmt.Sprintf("basecamp todos show %d", todo.ID),
						Description: "View todo",
					},
					output.Breadcrumb{
						Action:      "complete",
						Cmd:         fmt.Sprintf("basecamp done %d", todo.ID),
						Description: "Complete todo",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("basecamp todos --in %s", project),
						Description: "List todos",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().StringVarP(&todolist, "list", "l", "", "Todolist ID")
	cmd.Flags().StringVarP(&todoset, "todoset", "t", "", "Todoset ID (for projects with multiple todosets)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Assignee ID or name")
	cmd.Flags().StringVar(&assignee, "to", "", "Assignee (alias for --assignee)")
	cmd.Flags().StringVarP(&due, "due", "d", "", "Due date")
	cmd.Flags().StringVar(&description, "description", "", "Extended description (Markdown)")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")

	// Register tab completion for flags
	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("project", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("in", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("assignee", completer.PeopleNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("to", completer.PeopleNameCompletion())

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
	cmd.Flags().StringVar(&flags.project, "in", "", "Project ID or name")
	cmd.Flags().StringVarP(&flags.todolist, "list", "l", "", "Todolist ID")
	cmd.Flags().StringVarP(&flags.todoset, "todoset", "t", "", "Todoset ID (for projects with multiple todosets)")
	cmd.Flags().StringVar(&flags.assignee, "assignee", "", "Filter by assignee")
	cmd.Flags().StringVarP(&flags.status, "status", "s", "", "Filter by status (completed, pending)")
	cmd.Flags().BoolVar(&flags.completed, "completed", false, "Show completed todos (shorthand for --status completed)")
	cmd.Flags().BoolVar(&flags.overdue, "overdue", false, "Filter overdue todos")
	cmd.Flags().IntVarP(&flags.limit, "limit", "n", 0, "Maximum number of todos to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&flags.all, "all", false, "Fetch all todos (no limit)")
	cmd.Flags().IntVar(&flags.page, "page", 0, "Fetch a single page (use --all for everything)")
	cmd.Flags().StringVar(&flags.sortField, "sort", "", "Sort by field (title, created, updated, position, due)")
	cmd.Flags().BoolVar(&flags.reverse, "reverse", false, "Reverse sort order")

	// Register tab completion for flags
	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("in", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("assignee", completer.PeopleNameCompletion())

	return cmd
}

func runTodosList(cmd *cobra.Command, flags todosListFlags) error {
	app := appctx.FromContext(cmd.Context())
	if app == nil {
		return fmt.Errorf("app not initialized")
	}

	// Validate flag combinations
	if flags.completed && flags.status != "" {
		return output.ErrUsage("--completed and --status are mutually exclusive")
	}
	if flags.completed {
		flags.status = "completed"
	}
	if flags.all && flags.limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if flags.page > 0 && (flags.all || flags.limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}
	if flags.page > 1 {
		return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
	}
	if flags.sortField != "" {
		if err := validateSortField(flags.sortField, []string{"title", "created", "updated", "position", "due"}); err != nil {
			return err
		}
	}

	// Resolve account (enables interactive prompt if needed)
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// --assignee and --overdue filter within a single project. When no
	// project is set anywhere (flag, global flag, config), the interactive
	// picker would silently scope results to one arbitrary project. Error
	// early and point to the Reports API for cross-project queries.
	projectKnown := flags.project != "" || app.Flags.Project != "" || app.Config.ProjectID != ""
	if !projectKnown {
		if flags.assignee != "" {
			return output.ErrUsageHint(
				"--assignee requires a project (--in or default config)",
				"For cross-project assigned todos: basecamp reports assigned")
		}
		if flags.overdue {
			return output.ErrUsageHint(
				"--overdue requires a project (--in or default config)",
				"For cross-project overdue todos: basecamp reports overdue")
		}
	}

	// Use project from flag or config, with interactive fallback
	project := flags.project
	if project == "" {
		project = app.Flags.Project
	}
	if project == "" {
		project = app.Config.ProjectID
	}

	// If no project specified, try interactive resolution
	if project == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		project = app.Config.ProjectID
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
		return listTodosInList(cmd, app, project, todolist, flags.status, flags.limit, flags.page, flags.all, flags.sortField, flags.reverse)
	}

	// --page is not meaningful when aggregating across todolists
	// Each todolist has its own pages; there's no single "page 2" for all todos
	if flags.page > 0 {
		return output.ErrUsage("--page is only meaningful when listing a single todolist (--list); use --limit to cap results instead")
	}

	// Otherwise, get all todos from project's todoset
	return listAllTodos(cmd, app, project, flags.todoset, flags.assignee, flags.status, flags.overdue, flags.limit, flags.all, flags.sortField, flags.reverse)
}

func listTodosInList(cmd *cobra.Command, app *appctx.App, project, todolist, status string, limit, page int, all bool, sortField string, reverse bool) error {
	// Resolve todolist name to ID
	resolvedTodolist, _, err := app.Names.ResolveTodolist(cmd.Context(), todolist, project)
	if err != nil {
		return err
	}

	todolistID, err := strconv.ParseInt(resolvedTodolist, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid todolist ID")
	}

	opts := &basecamp.TodoListOptions{}
	if status != "" {
		opts.Status = status
	}

	// Apply pagination options
	if all {
		opts.Limit = -1 // SDK treats -1 as unlimited for todos
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}

	todosResult, err := app.Account().Todos().List(cmd.Context(), todolistID, opts)
	if err != nil {
		return convertSDKError(err)
	}
	todos := todosResult.Todos

	// Apply client-side sort when requested (field already validated in runTodosList)
	if sortField != "" {
		sortTodos(todos, sortField, reverse)
	}

	// Build response options
	respOpts := []output.ResponseOption{
		output.WithEntity("todo"),
		output.WithSummary(fmt.Sprintf("%d todos", len(todos))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("basecamp todo <content> --list %s", resolvedTodolist),
				Description: "Create a todo",
			},
			output.Breadcrumb{
				Action:      "complete",
				Cmd:         "basecamp done <id>",
				Description: "Complete a todo",
			},
		),
	}

	// Add truncation notice if results may be limited
	if notice := output.TruncationNoticeWithTotal(len(todos), todosResult.Meta.TotalCount); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}

	return app.OK(todos, respOpts...)
}

func listAllTodos(cmd *cobra.Command, app *appctx.App, project, todosetFlag, assignee, status string, overdue bool, limit int, all bool, sortField string, reverse bool) error {
	// Position is only meaningful within a single todolist — reject before
	// the --all check so users get the right error message.
	if sortField == "position" {
		return output.ErrUsage("--sort position requires --list (position is per-todolist)")
	}
	// Sorting the aggregate path without --all is misleading because results
	// are silently sampled per-todolist using default SDK paging.
	if sortField != "" && !all {
		return output.ErrUsage("--sort requires --all when listing across todolists (results are sampled per list without it)")
	}
	// Resolve assignee name to ID if provided
	var assigneeID int64
	if assignee != "" {
		resolvedID, _, err := app.Names.ResolvePerson(cmd.Context(), assignee)
		if err != nil {
			return fmt.Errorf("failed to resolve assignee '%s': %w", assignee, err)
		}
		assigneeID, _ = strconv.ParseInt(resolvedID, 10, 64)
	}

	// Get todoset ID from project dock (with interactive fallback for multi-todoset projects)
	todosetIDStr, err := ensureTodoset(cmd, app, project, todosetFlag)
	if err != nil {
		return err
	}
	todosetID, err := strconv.ParseInt(todosetIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid todoset ID")
	}

	// Get todolists via SDK
	todolistsResult, err := app.Account().Todolists().List(cmd.Context(), todosetID, nil)
	if err != nil {
		return convertSDKError(err)
	}

	// Build pagination options for todo fetching
	todoOpts := &basecamp.TodoListOptions{}
	if all {
		todoOpts.Limit = -1 // SDK treats -1 as unlimited for todos
	} else if limit > 0 {
		todoOpts.Limit = limit
	}

	// Aggregate todos from all todolists
	var allTodos []basecamp.Todo
	for _, tl := range todolistsResult.Todolists {
		todosResult, err := app.Account().Todos().List(cmd.Context(), tl.ID, todoOpts)
		if err != nil {
			continue // Skip failed todolists
		}
		allTodos = append(allTodos, todosResult.Todos...)
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

	// Apply client-side sort when requested (field validated early in runTodosList,
	// position rejected above)
	if sortField != "" {
		sortTodos(result, sortField, reverse)
	}

	// Build response options
	respOpts := []output.ResponseOption{
		output.WithEntity("todo"),
		output.WithSummary(fmt.Sprintf("%d todos", len(result))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "create",
				Cmd:         "basecamp todo <content> --list <list>",
				Description: "Create a todo",
			},
			output.Breadcrumb{
				Action:      "complete",
				Cmd:         "basecamp done <id>",
				Description: "Complete a todo",
			},
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "basecamp todos show <id>",
				Description: "Show todo details",
			},
		),
	}

	// Note: truncation notice is not shown when aggregating across todolists
	// because limit is applied per-list, not globally. Use --list for accurate notices.

	return app.OK(result, respOpts...)
}

func newTodosShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show todo details",
		Long: `Display detailed information about a todo.

You can pass either a todo ID or a Basecamp URL:
  basecamp todos show 789
  basecamp todos show https://3.basecamp.com/123/buckets/456/todos/789`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>")
			}

			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			todoIDStr := extractID(args[0])

			todoID, err := strconv.ParseInt(todoIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todo ID")
			}

			todo, err := app.Account().Todos().Get(cmd.Context(), todoID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(todo,
				output.WithEntity("todo"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "complete",
						Cmd:         fmt.Sprintf("basecamp done %d", todoID),
						Description: "Complete this todo",
					},
					output.Breadcrumb{
						Action:      "comment",
						Cmd:         fmt.Sprintf("basecamp comment %d <text>", todoID),
						Description: "Add comment",
					},
				),
			)
		},
	}

	return cmd
}

func newTodosCreateCmd() *cobra.Command {
	var project string
	var todolist string
	var todoset string
	var assignee string
	var due string
	var description string
	var attachFiles []string

	cmd := &cobra.Command{
		Use:   "create <content>",
		Short: "Create a new todo",
		Long:  "Create a new todo in a project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			// Show help when invoked with no content
			if len(args) == 0 {
				return missingArg(cmd, "<content>")
			}
			content := strings.Join(args, " ")
			if strings.TrimSpace(content) == "" {
				return cmd.Help()
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Use project from flag or config, with interactive fallback
			if project == "" {
				project = app.Flags.Project
			}
			if project == "" {
				project = app.Config.ProjectID
			}
			if project == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				project = app.Config.ProjectID
			}

			// Resolve project name to ID
			resolvedProject, _, err := app.Names.ResolveProject(cmd.Context(), project)
			if err != nil {
				return err
			}
			project = resolvedProject

			// Use todolist from flag, config, or interactive prompt
			if todolist == "" {
				todolist = app.Flags.Todolist
			}
			if todolist == "" {
				todolist = app.Config.TodolistID
			}
			// If still no todolist, try interactive selection (todoset-scoped)
			if todolist == "" {
				selectedTodolist, err := ensureTodolist(cmd, app, project, todoset)
				if err != nil {
					return err
				}
				todolist = selectedTodolist
			}

			if todolist == "" {
				return output.ErrUsage("--list is required (no default todolist found)")
			}

			// Resolve todolist name to ID, scoped to --todoset when provided
			resolvedTodolist, err := resolveTodolistInTodoset(cmd, app, todolist, project, todoset)
			if err != nil {
				return err
			}

			// Build SDK request
			// Content is plain text (todo title) - do not wrap in HTML
			req := &basecamp.CreateTodoRequest{
				Content: content,
			}

			// Process description with Markdown + attachments
			if description != "" || len(attachFiles) > 0 {
				descHTML := richtext.MarkdownToHTML(description)

				// Resolve inline images
				descHTML, descErr := resolveLocalImages(cmd, app, descHTML)
				if descErr != nil {
					return descErr
				}

				// Upload explicit --attach files and embed
				if len(attachFiles) > 0 {
					refs, attachErr := uploadAttachments(cmd, app, attachFiles)
					if attachErr != nil {
						return attachErr
					}
					descHTML = richtext.EmbedAttachments(descHTML, refs)
				}

				req.Description = descHTML
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

			todolistID, err := strconv.ParseInt(resolvedTodolist, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todolist ID")
			}

			todo, err := app.Account().Todos().Create(cmd.Context(), todolistID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(todo,
				output.WithEntity("todo"),
				output.WithSummary(fmt.Sprintf("Created todo #%d", todo.ID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "view",
						Cmd:         fmt.Sprintf("basecamp todos show %d", todo.ID),
						Description: "View todo",
					},
					output.Breadcrumb{
						Action:      "complete",
						Cmd:         fmt.Sprintf("basecamp done %d", todo.ID),
						Description: "Complete todo",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("basecamp todos --in %s", project),
						Description: "List todos",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().StringVarP(&todolist, "list", "l", "", "Todolist ID")
	cmd.Flags().StringVarP(&todoset, "todoset", "t", "", "Todoset ID (for projects with multiple todosets)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Assignee ID")
	cmd.Flags().StringVar(&assignee, "to", "", "Assignee ID (alias for --assignee)")
	cmd.Flags().StringVarP(&due, "due", "d", "", "Due date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&description, "description", "", "Extended description (Markdown)")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")

	// Register tab completion for flags
	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("project", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("in", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("assignee", completer.PeopleNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("to", completer.PeopleNameCompletion())

	return cmd
}

func newTodosCompleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "complete <id|url>...",
		Short: "Complete todo(s)",
		Long: `Mark one or more todos as completed.

You can pass todo IDs, Basecamp URLs, or comma-separated IDs:
  basecamp todos complete 789
  basecamp todos complete 789 012 345
  basecamp todos complete 789,012,345
  basecamp todos complete https://3.basecamp.com/123/buckets/456/todos/789`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>...")
			}
			return completeTodos(cmd, args)
		},
	}

	return cmd
}

func newDoneCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "done <id|url>...",
		Short: "Complete todo(s)",
		Long: `Mark one or more todos as completed.

You can pass todo IDs, Basecamp URLs, or comma-separated IDs:
  basecamp done 789
  basecamp done 789 012 345
  basecamp done 789,012,345
  basecamp done https://3.basecamp.com/123/buckets/456/todos/789`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>...")
			}
			return completeTodos(cmd, args)
		},
	}

	return cmd
}

func completeTodos(cmd *cobra.Command, todoIDs []string) error {
	app := appctx.FromContext(cmd.Context())
	if app == nil {
		return fmt.Errorf("app not initialized")
	}

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Extract IDs from URLs (handles both plain IDs and URLs)
	extractedIDs := extractIDs(todoIDs)

	var completedTodos []basecamp.Todo
	var failed []string
	var firstAPIErr error

	for _, todoIDStr := range extractedIDs {
		todoID, err := strconv.ParseInt(todoIDStr, 10, 64)
		if err != nil {
			failed = append(failed, todoIDStr)
			continue
		}
		err = app.Account().Todos().Complete(cmd.Context(), todoID)
		if err != nil {
			failed = append(failed, todoIDStr)
			if firstAPIErr == nil {
				firstAPIErr = err
			}
			continue
		}
		// Fetch the completed todo to show it
		todo, err := app.Account().Todos().Get(cmd.Context(), todoID)
		if err != nil {
			// Completed but couldn't fetch — still count it
			completedTodos = append(completedTodos, basecamp.Todo{ID: todoID})
		} else {
			completedTodos = append(completedTodos, *todo)
		}
	}

	// If all operations failed, return an error for automation
	if len(completedTodos) == 0 && len(failed) > 0 {
		if firstAPIErr != nil {
			converted := convertSDKError(firstAPIErr)
			var outErr *output.Error
			if errors.As(converted, &outErr) {
				return &output.Error{
					Code:       outErr.Code,
					Message:    fmt.Sprintf("Failed to complete todos %s: %s", strings.Join(failed, ", "), outErr.Message),
					Hint:       outErr.Hint,
					HTTPStatus: outErr.HTTPStatus,
					Retryable:  outErr.Retryable,
					Cause:      outErr,
				}
			}
			return fmt.Errorf("failed to complete todos %s: %w", strings.Join(failed, ", "), converted)
		}
		return output.ErrUsage(fmt.Sprintf("Invalid todo ID(s): %s", strings.Join(failed, ", ")))
	}

	summary := fmt.Sprintf("Completed %d todo(s)", len(completedTodos))
	if len(failed) > 0 {
		summary = fmt.Sprintf("Completed %d, failed %d", len(completedTodos), len(failed))
	}

	breadcrumbs := []output.Breadcrumb{
		{
			Action:      "reopen",
			Cmd:         fmt.Sprintf("basecamp reopen %s", extractedIDs[0]),
			Description: "Reopen todo",
		},
	}

	// Return single todo directly (like basecamp todo does), list for multiple
	if len(completedTodos) == 1 {
		return app.OK(completedTodos[0],
			output.WithEntity("todo"),
			output.WithSummary(summary),
			output.WithBreadcrumbs(breadcrumbs...),
		)
	}

	return app.OK(completedTodos,
		output.WithEntity("todo"),
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}

func newTodosUncompleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "uncomplete <id|url>...",
		Aliases: []string{"reopen"},
		Short:   "Reopen todo(s)",
		Long: `Reopen one or more completed todos.

You can pass todo IDs, Basecamp URLs, or comma-separated IDs:
  basecamp todos uncomplete 789
  basecamp todos uncomplete 789 012 345
  basecamp todos uncomplete 789,012,345
  basecamp todos uncomplete https://3.basecamp.com/123/buckets/456/todos/789`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>...")
			}
			return reopenTodos(cmd, args)
		},
	}

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
	var todoset string
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
  basecamp todos sweep --in <project> --overdue --dry-run

  # Complete all overdue todos with a comment
  basecamp todos sweep --in <project> --overdue --complete --comment "Cleaning up overdue items"

  # Add comment to all todos assigned to me
  basecamp todos sweep --in <project> --assignee me --comment "Following up"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
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

			// Resolve project from flag, global flag, or config default.
			// Don't fall through to interactive picker for sweep — acting
			// on an arbitrary project chosen mid-flow is too risky.
			if project == "" {
				project = app.Flags.Project
			}
			if project == "" {
				project = app.Config.ProjectID
			}
			if project == "" {
				return output.ErrUsageHint(
					"Sweep requires a project",
					"Use --in <project> or set a default with: basecamp config set project <name>")
			}

			// Resolve project name to ID
			resolvedProject, _, err := app.Names.ResolveProject(cmd.Context(), project)
			if err != nil {
				return err
			}
			project = resolvedProject

			// Get matching todos using existing listAllTodos logic
			matchingTodos, err := getTodosForSweep(cmd, app, project, todoset, assignee, overdueOnly)
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
					_, commentErr := app.Account().Comments().Create(cmd.Context(), todoID, req)
					if commentErr != nil {
						result.CommentFailed = append(result.CommentFailed, todoID)
					} else {
						result.Commented = append(result.Commented, todoID)
					}
				}

				// Complete if specified
				if complete {
					completeErr := app.Account().Todos().Complete(cmd.Context(), todoID)
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
						Cmd:         fmt.Sprintf("basecamp todos --in %s", project),
						Description: "List todos",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().StringVarP(&todoset, "todoset", "t", "", "Todoset ID (for projects with multiple todosets)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Filter by assignee")
	cmd.Flags().BoolVar(&overdueOnly, "overdue", false, "Filter overdue todos")
	cmd.Flags().StringVarP(&comment, "comment", "c", "", "Comment to add to matching todos")
	cmd.Flags().BoolVar(&complete, "complete", false, "Mark matching todos as complete")
	cmd.Flags().BoolVar(&complete, "done", false, "Mark matching todos as complete (alias)")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "Preview without making changes")

	// Register tab completion for flags
	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("project", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("in", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("assignee", completer.PeopleNameCompletion())

	return cmd
}

// getTodosForSweep gets todos matching the sweep filters.
func getTodosForSweep(cmd *cobra.Command, app *appctx.App, project, todosetFlag, assignee string, overdue bool) ([]basecamp.Todo, error) {
	// Resolve assignee name to ID if provided
	var assigneeID int64
	if assignee != "" {
		resolvedID, _, err := app.Names.ResolvePerson(cmd.Context(), assignee)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve assignee '%s': %w", assignee, err)
		}
		assigneeID, _ = strconv.ParseInt(resolvedID, 10, 64)
	}

	// Get todoset ID from project dock (with interactive fallback for multi-todoset projects)
	todosetIDStr, err := ensureTodoset(cmd, app, project, todosetFlag)
	if err != nil {
		return nil, err
	}
	todosetID, err := strconv.ParseInt(todosetIDStr, 10, 64)
	if err != nil {
		return nil, output.ErrUsage("Invalid todoset ID")
	}

	// Get todolists via SDK
	todolistsResult, err := app.Account().Todolists().List(cmd.Context(), todosetID, nil)
	if err != nil {
		return nil, convertSDKError(err)
	}

	// Aggregate todos from all todolists
	var allTodos []basecamp.Todo
	for _, tl := range todolistsResult.Todolists {
		todosResult, err := app.Account().Todos().List(cmd.Context(), tl.ID, nil)
		if err != nil {
			continue // Skip failed todolists
		}
		allTodos = append(allTodos, todosResult.Todos...)
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
	cmd := &cobra.Command{
		Use:   "reopen <id|url>...",
		Short: "Reopen todo(s)",
		Long: `Reopen one or more completed todos.

You can pass todo IDs, Basecamp URLs, or comma-separated IDs:
  basecamp reopen 789
  basecamp reopen 789 012 345
  basecamp reopen 789,012,345
  basecamp reopen https://3.basecamp.com/123/buckets/456/todos/789`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>...")
			}
			return reopenTodos(cmd, args)
		},
	}

	return cmd
}

func reopenTodos(cmd *cobra.Command, todoIDs []string) error {
	app := appctx.FromContext(cmd.Context())
	if app == nil {
		return fmt.Errorf("app not initialized")
	}

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Extract IDs from URLs (handles both plain IDs and URLs)
	extractedIDs := extractIDs(todoIDs)

	var reopenedTodos []basecamp.Todo
	var failed []string
	var firstAPIErr error

	for _, todoIDStr := range extractedIDs {
		todoID, err := strconv.ParseInt(todoIDStr, 10, 64)
		if err != nil {
			failed = append(failed, todoIDStr)
			continue
		}
		err = app.Account().Todos().Uncomplete(cmd.Context(), todoID)
		if err != nil {
			failed = append(failed, todoIDStr)
			if firstAPIErr == nil {
				firstAPIErr = err
			}
			continue
		}
		// Fetch the reopened todo to show it
		todo, err := app.Account().Todos().Get(cmd.Context(), todoID)
		if err != nil {
			reopenedTodos = append(reopenedTodos, basecamp.Todo{ID: todoID})
		} else {
			reopenedTodos = append(reopenedTodos, *todo)
		}
	}

	// If all operations failed, return an error for automation
	if len(reopenedTodos) == 0 && len(failed) > 0 {
		if firstAPIErr != nil {
			converted := convertSDKError(firstAPIErr)
			var outErr *output.Error
			if errors.As(converted, &outErr) {
				return &output.Error{
					Code:       outErr.Code,
					Message:    fmt.Sprintf("Failed to reopen todos %s: %s", strings.Join(failed, ", "), outErr.Message),
					Hint:       outErr.Hint,
					HTTPStatus: outErr.HTTPStatus,
					Retryable:  outErr.Retryable,
					Cause:      outErr,
				}
			}
			return fmt.Errorf("failed to reopen todos %s: %w", strings.Join(failed, ", "), converted)
		}
		return output.ErrUsage(fmt.Sprintf("Invalid todo ID(s): %s", strings.Join(failed, ", ")))
	}

	summary := fmt.Sprintf("Reopened %d todo(s)", len(reopenedTodos))
	if len(failed) > 0 {
		summary = fmt.Sprintf("Reopened %d, failed %d", len(reopenedTodos), len(failed))
	}

	breadcrumbs := []output.Breadcrumb{
		{
			Action:      "complete",
			Cmd:         fmt.Sprintf("basecamp done %s", extractedIDs[0]),
			Description: "Complete todo",
		},
	}

	if len(reopenedTodos) == 1 {
		return app.OK(reopenedTodos[0],
			output.WithEntity("todo"),
			output.WithSummary(summary),
			output.WithBreadcrumbs(breadcrumbs...),
		)
	}

	return app.OK(reopenedTodos,
		output.WithEntity("todo"),
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}

func newTodosPositionCmd() *cobra.Command {
	var position int

	cmd := &cobra.Command{
		Use:     "position <id|url>",
		Aliases: []string{"move", "reorder"},
		Short:   "Change todo position",
		Long: `Reorder a todo within its todolist. Position is 1-based (1 = top).

You can pass either a todo ID or a Basecamp URL:
  basecamp todos position 789 --to 1
  basecamp todos position https://3.basecamp.com/123/buckets/456/todos/789 --to 1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>")
			}

			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if position == 0 {
				return output.ErrUsage("--to is required (1 = top)")
			}

			// Extract ID from URL if provided
			todoIDStr := extractID(args[0])

			todoID, err := strconv.ParseInt(todoIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todo ID")
			}

			err = app.Account().Todos().Reposition(cmd.Context(), todoID, position)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"repositioned": true, "position": position},
				output.WithSummary(fmt.Sprintf("Moved todo #%d to position %d", todoID, position)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp todos show %d", todoID),
						Description: "View todo",
					},
				),
			)
		},
	}

	cmd.Flags().IntVar(&position, "to", 0, "Target position, 1-based (1 = top)")
	cmd.Flags().IntVar(&position, "position", 0, "Target position (alias for --to)")

	return cmd
}
