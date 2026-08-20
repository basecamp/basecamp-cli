package commands

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
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
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// todosListFlags holds the flags for the todos list command.
type todosListFlags struct {
	project     string
	allProjects bool
	todolist    string
	todoset     string
	assignees   []string
	due         string
	status      string
	completed   bool
	overdue     bool
	unassigned  bool
	noDueDate   bool
	limit       int
	page        int
	all         bool
	sortField   string
	reverse     bool
}

// NewTodosCmd creates the todos command group.
func NewTodosCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "todos",
		Short:       "Manage todos",
		Long:        "List, show, create, and manage Basecamp todos.",
		Annotations: map[string]string{"agent_notes": "basecamp todos complete accepts multiple IDs: basecamp todos complete 1 2 3\nbasecamp todos list without a project lists every project's todos; --all-projects forces that over a configured default\n--assignee works in both scopes but differently: account-wide (--all-projects, or no project in scope) it is a server-side filter; inside a project it is applied client-side. --due is account-wide only"},
	}

	cmd.AddCommand(
		newTodosListCmd(),
		newTodosShowCmd(),
		newTodosCreateCmd(),
		newTodosUpdateCmd(),
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

func newTodosListCmd() *cobra.Command {
	var flags todosListFlags

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List todos",
		Long: `List todos in a project or todolist.

With no project in scope, todos are listed across every project you can see.
--all-projects forces that listing over a configured default project, and
--unassigned/--no-due-date select account-wide filters that have no
project-scoped equivalent.

--assignee is repeatable and matches a todo assigned to any of the named
people. Account-wide it is a server-side filter; within a project the API has
no assignee parameter, so it is applied client-side over an unlimited fetch —
same results, very different cost. Assignees on nested steps are not
considered.

--due (with, without, overdue) filters the account-wide listing only, and
cannot be combined with --overdue or --no-due-date, which each select their own
listing on that same axis. --assignee cannot be combined with --unassigned:
nothing can match both.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTodosList(cmd, flags)
		},
	}

	// Note: can't use -a for assignee since it conflicts with global -a for account
	cmd.Flags().StringVar(&flags.project, "in", "", "Project ID or name")
	cmd.Flags().BoolVar(&flags.allProjects, "all-projects", false, "List todos across every project (overrides a configured project)")
	cmd.Flags().StringVarP(&flags.todolist, "list", "l", "", "Todolist ID")
	cmd.Flags().StringVarP(&flags.todoset, "todoset", "t", "", "Todoset ID (for projects with multiple todosets)")
	// Repeatable, and account-wide it is a real server-side filter. Widening
	// from StringVar changes the .surface type line, which reads as a removal —
	// acknowledged in .surface-breaking.
	cmd.Flags().StringArrayVar(&flags.assignees, "assignee", nil, "Filter by assignee (repeatable; account-wide it is server-side)")
	cmd.Flags().StringVar(&flags.due, "due", "", "Filter by due date: with, without, overdue (account-wide only)")
	cmd.Flags().StringVarP(&flags.status, "status", "s", "", "Filter by status (completed, incomplete, archived, trashed)")
	cmd.Flags().BoolVar(&flags.completed, "completed", false, "Show completed todos (shorthand for --status completed)")
	cmd.Flags().BoolVar(&flags.overdue, "overdue", false, "Filter overdue todos")
	cmd.Flags().BoolVar(&flags.unassigned, "unassigned", false, "Unassigned todos (across all projects only)")
	cmd.Flags().BoolVar(&flags.noDueDate, "no-due-date", false, "Todos with no due date (across all projects only)")
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

	// Validate the flag combinations that hold under either scope.
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
	if flags.sortField != "" {
		if err := validateSortField(flags.sortField, []string{"title", "created", "updated", "position", "due"}); err != nil {
			return err
		}
	}

	// Scope- and account-independent filter validation: an explicitly empty
	// --due/--assignee, and an unknown --due token. Before ensureAccount, so a
	// usage error that needs no account does not demand one.
	if err := validateTaskFilterValues(cmd, flags.due, flags.assignees); err != nil {
		return err
	}

	// Pick the scope before validating against it: the account-wide endpoints
	// take any positive page, while the project path only permits page 1.
	if flags.allProjects && (flags.project != "" || app.Flags.Project != "") {
		return output.ErrUsageHint(
			"--all-projects cannot be combined with a project (--in/--project)",
			"Drop one: --all-projects lists every project, --in lists one.")
	}
	accountWide := flags.allProjects || !projectKnown(app, flags.project)

	// Resolve account (enables interactive prompt if needed)
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	if accountWide {
		return listTodosAcrossProjects(cmd, app, flags)
	}

	// Project scope from here down.
	if flags.unassigned {
		return output.ErrUsageHint(
			"--unassigned lists across all projects and has no project-scoped equivalent",
			"Drop the project to list unassigned todos across every project")
	}
	if flags.noDueDate {
		return output.ErrUsageHint(
			"--no-due-date lists across all projects and has no project-scoped equivalent",
			"Drop the project to list todos with no due date across every project")
	}
	if flags.page > 1 {
		return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
	}

	sdkStatus, sdkCompleted, err := resolveStatusFilter(flags.status)
	if err != nil {
		return err
	}

	// --due is a parameter on the account-wide aggregates only; the
	// project-scoped listing has no equivalent, so it is refused rather than
	// dropped.
	if flags.due != "" {
		return output.ErrUsageHint(
			"--due filters the account-wide listing only",
			"Drop --project/--in to filter across all projects, or use --overdue within this one")
	}

	// Use project from flag, global flag, or config. One of the three is set —
	// otherwise the account-wide branch above answered the listing, so there is
	// nothing left to prompt for.
	project := flags.project
	if project == "" {
		project = app.Flags.Project
	}
	if project == "" {
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
		return listTodosInList(cmd, app, project, todolist, flags.assignees, sdkStatus, sdkCompleted, flags.limit, flags.all, flags.sortField, flags.reverse)
	}

	// --page is not meaningful when aggregating across todolists
	// Each todolist has its own pages; there's no single "page 2" for all todos
	if flags.page > 0 {
		return output.ErrUsage("--page is only meaningful when listing a single todolist (--list); use --limit to cap results instead")
	}

	// Otherwise, get all todos from project's todoset
	return listAllTodos(cmd, app, project, flags.todoset, flags.assignees, sdkStatus, sdkCompleted, flags.overdue, flags.limit, flags.all, flags.sortField, flags.reverse)
}

// todosAccountWideFilter names the account-wide todo aggregate a listing maps
// onto. Each is a distinct endpoint, so exactly one can be selected per run.
type todosAccountWideFilter int

const (
	todosFilterOpen todosAccountWideFilter = iota
	todosFilterCompleted
	todosFilterUnassigned
	todosFilterNoDueDate
	todosFilterOverdue
)

// listTodosAcrossProjects answers `todos list` from the account-wide aggregates
// when no project is in scope, or when --all-projects overrides a configured
// one. Flags that only mean something inside one project are rejected here
// rather than quietly dropped.
func listTodosAcrossProjects(cmd *cobra.Command, app *appctx.App, flags todosListFlags) error {
	if err := rejectProjectScopedTodosFlags(app, flags); err != nil {
		return err
	}

	filter, err := selectAccountWideTodosFilter(flags)
	if err != nil {
		return err
	}

	if err := validateAccountWideTaskFilters(flags.assignees, flags.due, flags.unassigned,
		flags.overdue, flags.noDueDate, "todos"); err != nil {
		return err
	}

	if flags.limit < 0 {
		return output.ErrUsage("--limit cannot be negative")
	}
	if flags.reverse && flags.sortField == "" {
		return output.ErrUsage("--reverse requires --sort")
	}

	if filter == todosFilterOverdue {
		return listOverdueTodosAcrossProjects(cmd, app, flags)
	}

	// An explicit --page 0 means "unset" to Cobra but "every page" to the API;
	// --all is the spelling for that.
	if cmd.Flags().Changed("page") && flags.page < 1 {
		return output.ErrUsage("--page must be 1 or greater; use --all to fetch every page")
	}
	// The endpoints take an int32 page. Clamping a larger value would serve a
	// page the user did not ask for, so say it is out of range instead.
	if flags.page > math.MaxInt32 {
		return output.ErrUsage("--page is out of range")
	}
	if flags.sortField != "" {
		return output.ErrUsageHint(
			"--sort is not supported when listing across all projects (results are grouped by project)",
			fmt.Sprintf("Sort within one project: basecamp todos list --in <project> --sort %s", flags.sortField))
	}

	return listGroupedTodosAcrossProjects(cmd, app, flags, filter)
}

// rejectProjectScopedTodosFlags returns a usage error for each flag that names
// something inside a single project. A configured todolist is treated the same
// way a configured project is: --all-projects overrides it, but without that
// the user never said to ignore it, and silently dropping it would hand back a
// listing they did not ask for.
func rejectProjectScopedTodosFlags(app *appctx.App, flags todosListFlags) error {
	switch {
	case flags.todolist != "":
		return output.ErrUsageHint(
			"--list names a todolist inside one project, which has no meaning across all projects",
			fmt.Sprintf("List that todolist: basecamp todos list --in <project> --list %s", flags.todolist))
	case app.Flags.Todolist != "":
		return output.ErrUsageHint(
			"--todolist names a todolist inside one project, which has no meaning across all projects",
			"Drop --todolist to list todos across every project")
	case app.Config.TodolistID != "" && !flags.allProjects:
		return output.ErrUsageHint(
			"a default todolist is configured, which has no meaning across all projects",
			"Pass --all-projects to ignore it, or clear it with: basecamp config unset todolist_id")
	case flags.todoset != "":
		return output.ErrUsageHint(
			"--todoset names a todoset inside one project, which has no meaning across all projects",
			fmt.Sprintf("List that todoset: basecamp todos list --in <project> --todoset %s", flags.todoset))
	}
	return nil
}

// selectAccountWideTodosFilter maps the endpoint selectors onto the aggregate
// they pick. The selectors are mutually exclusive — no endpoint combines two.
func selectAccountWideTodosFilter(flags todosListFlags) (todosAccountWideFilter, error) {
	completedSpelling := "--status completed"
	if flags.completed {
		completedSpelling = "--completed"
	}

	switch flags.status {
	case "", "incomplete", "completed":
	case "archived", "trashed":
		return todosFilterOpen, output.ErrUsageHint(
			fmt.Sprintf("--status %s has no account-wide equivalent", flags.status),
			fmt.Sprintf("List them in one project: basecamp todos list --in <project> --status %s", flags.status))
	default:
		return todosFilterOpen, output.ErrUsage(
			fmt.Sprintf("unknown --status value %q (expected completed, incomplete, archived, or trashed)", flags.status))
	}

	selectors := []struct {
		name     string
		selected bool
		filter   todosAccountWideFilter
	}{
		{completedSpelling, flags.status == "completed", todosFilterCompleted},
		{"--unassigned", flags.unassigned, todosFilterUnassigned},
		{"--no-due-date", flags.noDueDate, todosFilterNoDueDate},
		{"--overdue", flags.overdue, todosFilterOverdue},
	}

	filter := todosFilterOpen
	chosen := ""
	for _, s := range selectors {
		if !s.selected {
			continue
		}
		if chosen != "" {
			return todosFilterOpen, output.ErrUsage(
				fmt.Sprintf("%s and %s are mutually exclusive (each selects a different listing)", chosen, s.name))
		}
		chosen, filter = s.name, s.filter
	}

	return filter, nil
}

// listGroupedTodosAcrossProjects fetches one of the paginated aggregates, whose
// payload is nested by project.
func listGroupedTodosAcrossProjects(cmd *cobra.Command, app *appctx.App, flags todosListFlags, filter todosAccountWideFilter) error {
	// Server-side here, unlike the project-scoped path: these become
	// assignee_ids[] and due= on the request, so the server narrows the listing
	// before it is paginated. That does not fix the request count — the bounded
	// walk's cap counts items, so a narrower result can take an extra page to
	// fill it. See the note on accountWideTaskFilters.
	taskFilters, err := accountWideTaskFilters(cmd.Context(), app, flags.assignees, flags.due)
	if err != nil {
		return err
	}

	limit := flags.limit
	if limit == 0 {
		limit = accountWideDefaultLimit
	}

	var groups []basecamp.BucketTodosGroup
	capped := false
	truncated := false

	if flags.all || flags.page > 0 {
		sdkPage, err := accountWidePage(flags.page, flags.all)
		if err != nil {
			return err
		}
		page, err := fetchAccountWideTodoGroups(cmd.Context(), app, filter, sdkPage, taskFilters)
		if err != nil {
			return convertSDKError(err)
		}
		groups = page.Groups
		truncated = page.Meta.Truncated
	} else {
		collected, more, err := collectAccountWideTodoGroups(cmd.Context(), app, filter, limit, taskFilters)
		if err != nil {
			return convertSDKError(err)
		}
		groups, capped = truncateAccountWideTodoGroups(collected, limit), more
	}

	// Meta.TotalCount counts project groups rather than todos, so the item
	// total and its notice are computed here instead of from the SDK's meta.
	count := countAccountWideTodos(groups)

	// --json and --agent keep the grouping the SDK returned; every other
	// consumer gets flat rows. Nested groups have no id and no title of their
	// own, so a renderer handed them produces unreadable cells, and --ids and
	// --count read right past the todos to count projects.
	respOpts := []output.ResponseOption{
		output.WithDisplayData(flattenAccountWideTodos(groups)),
		output.WithSummary(fmt.Sprintf("%d todos across %d projects", count, len(groups))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "basecamp todos show <id>",
				Description: "Show todo details",
			},
			output.Breadcrumb{
				Action:      "list",
				Cmd:         "basecamp todos list --in <project>",
				Description: "List one project's todos",
			},
		),
	}
	switch {
	case capped:
		respOpts = append(respOpts, output.WithNotice(fmt.Sprintf(
			"Showing the first %d todos; more may exist (use --all for every page, or --limit to raise the cap)", count)))
	case truncated:
		// The SDK stopped following pages before the listing ran out. Saying
		// nothing would present a partial result as a complete one.
		respOpts = append(respOpts, output.WithNotice("More pages are available; results were truncated"))
	}

	return app.OK(groups, respOpts...)
}

// listOverdueTodosAcrossProjects fetches the overdue aggregate, which is a flat
// oldest-due-first array rather than a paginated, project-grouped listing.
func listOverdueTodosAcrossProjects(cmd *cobra.Command, app *appctx.App, flags todosListFlags) error {
	if cmd.Flags().Changed("page") {
		return output.ErrUsageHint(
			"--page is not supported with --overdue (the overdue listing is not paginated)",
			"Cap the results instead: basecamp todos list --overdue --limit <n>")
	}
	if flags.sortField == "position" {
		return output.ErrUsage("--sort position requires --list (position is per-todolist)")
	}

	taskFilters, err := accountWideTaskFilters(cmd.Context(), app, flags.assignees, flags.due)
	if err != nil {
		return err
	}

	todos, err := app.Account().Everything().OverdueTodos(cmd.Context(), taskFilters)
	if err != nil {
		return convertSDKError(err)
	}
	total := len(todos)

	// Sort before truncating — truncating first would sort only the survivors.
	if flags.sortField != "" {
		sortTodos(todos, flags.sortField, flags.reverse)
	}

	// The endpoint is unpaginated, so the complete array is already in hand and
	// --all costs nothing beyond skipping the cap. --limit trims locally.
	limit := flags.limit
	if limit == 0 {
		limit = accountWideDefaultLimit
	}
	if !flags.all && len(todos) > limit {
		todos = todos[:limit]
	}

	// No WithEntity here: the todo schema renders a task list, which has no
	// column for a project, and the cards arrive from every project. Flat rows
	// carrying the bucket name are what makes an account-wide overdue listing
	// attributable — the generic renderers skip a nested bucket by name.
	respOpts := []output.ResponseOption{
		output.WithDisplayData(flattenOverdueTodos(todos)),
		output.WithSummary(fmt.Sprintf("%d overdue todos across all projects", len(todos))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "basecamp todos show <id>",
				Description: "Show todo details",
			},
			output.Breadcrumb{
				Action:      "complete",
				Cmd:         "basecamp todos complete <id>",
				Description: "Complete a todo",
			},
		),
	}
	if total > len(todos) {
		respOpts = append(respOpts, output.WithNotice(fmt.Sprintf(
			"Showing %d of %d overdue todos (use --all for the complete list, or --limit to raise the cap)",
			len(todos), total)))
	}

	return app.OK(todos, respOpts...)
}

// collectAccountWideTodoGroups walks positive pages until it has collected the
// requested number of todos, which is cheaper than fetching every page only to
// truncate. The second return reports that collection stopped at the cap rather
// than at the end of the listing.
func collectAccountWideTodoGroups(ctx context.Context, app *appctx.App, filter todosAccountWideFilter, limit int, taskFilters *basecamp.EverythingTaskFilters) ([]basecamp.BucketTodosGroup, bool, error) {
	groups, capped, _, err := accountWideCollect(
		func(page int32) ([]basecamp.BucketTodosGroup, basecamp.ListMeta, error) {
			result, err := fetchAccountWideTodoGroups(ctx, app, filter, page, taskFilters)
			if err != nil {
				return nil, basecamp.ListMeta{}, err
			}
			return result.Groups, result.Meta, nil
		},
		countAccountWideTodos,
		limit,
	)
	return groups, capped, err
}

// fetchAccountWideTodoGroups calls the aggregate the filter selects. Page 0
// follows the Link header across every page.
func fetchAccountWideTodoGroups(ctx context.Context, app *appctx.App, filter todosAccountWideFilter, page int32, taskFilters *basecamp.EverythingTaskFilters) (*basecamp.BucketTodosGroupsPage, error) {
	everything := app.Account().Everything()
	switch filter {
	case todosFilterCompleted:
		return everything.CompletedTodos(ctx, page, taskFilters)
	case todosFilterUnassigned:
		return everything.UnassignedTodos(ctx, page, taskFilters)
	case todosFilterNoDueDate:
		return everything.NoDueDateTodos(ctx, page, taskFilters)
	default:
		return everything.OpenTodos(ctx, page, taskFilters)
	}
}

// truncateAccountWideTodoGroups caps a grouped listing at limit todos. The cap
// counts todos rather than groups — truncating groups would drop whole projects
// from the listing.
func truncateAccountWideTodoGroups(groups []basecamp.BucketTodosGroup, limit int) []basecamp.BucketTodosGroup {
	kept := make([]basecamp.BucketTodosGroup, 0, len(groups))
	remaining := limit

	for _, group := range groups {
		if remaining <= 0 {
			break
		}
		if len(group.Todos) > remaining {
			group.Todos = group.Todos[:remaining]
		}
		remaining -= len(group.Todos)
		kept = append(kept, group)
	}

	return kept
}

// countAccountWideTodos totals the todos inside a grouped listing.
func countAccountWideTodos(groups []basecamp.BucketTodosGroup) int {
	count := 0
	for _, group := range groups {
		count += len(group.Todos)
	}
	return count
}

// flattenAccountWideTodos turns the project-grouped payload into flat rows for
// styled output, which renders nested groups as unreadable cells. Machine
// formats keep the grouping.
// flattenOverdueTodos builds display rows for the flat overdue aggregate, which
// returns todos from every project rather than groups.
func flattenOverdueTodos(todos []basecamp.Todo) []map[string]any {
	rows := make([]map[string]any, 0, len(todos))
	for _, todo := range todos {
		status := "incomplete"
		if todo.Completed {
			status = "completed"
		}
		row := map[string]any{
			"id":     todo.ID,
			"title":  todo.Title,
			"status": status,
			"due":    todo.DueOn,
		}
		if todo.Bucket != nil {
			row["project"] = todo.Bucket.Name
		}
		rows = append(rows, row)
	}
	return rows
}

func flattenAccountWideTodos(groups []basecamp.BucketTodosGroup) []map[string]any {
	rows := make([]map[string]any, 0, countAccountWideTodos(groups))
	for _, group := range groups {
		for _, todo := range group.Todos {
			status := "incomplete"
			if todo.Completed {
				status = "completed"
			}
			rows = append(rows, map[string]any{
				"project": group.Bucket.Name,
				"id":      todo.ID,
				"title":   todo.Title,
				"status":  status,
				"due":     todo.DueOn,
			})
		}
	}
	return rows
}

// resolveStatusFilter maps the user-facing --status value to the SDK's
// (Status, Completed) pair. Status is lifecycle-only ("archived", "trashed",
// or empty); Completed handles the completion filter. The empty/"incomplete"
// case lets the SDK apply its API default (incomplete todos only).
func resolveStatusFilter(status string) (sdkStatus string, completed bool, err error) {
	switch status {
	case "", "incomplete":
		// API default: incomplete only.
	case "completed":
		completed = true
	case "archived", "trashed":
		sdkStatus = status
	default:
		return "", false, output.ErrUsage(
			fmt.Sprintf("unknown --status value %q (expected completed, incomplete, archived, or trashed)", status))
	}
	return sdkStatus, completed, nil
}

// fetchTodosIncludingGroups fetches all todos from a todolist, including
// those nested inside todolist groups. Groups and direct todos share the
// same position space; this function merges them by position so the output
// order matches the Basecamp UI.
//
// totalCount is the total number of matching todos before any limit cap:
// for the no-groups path it is the server-reported Meta.TotalCount; for the
// groups path it is the full flattened count (since we fetch everything for
// correct position merge).
//
// limit controls pagination: -1 fetches all, 0 uses SDK default, positive
// values cap results. In the no-groups path the limit is passed directly to
// the SDK. In the groups path all todos are fetched for position-correct
// merge, then capped to limit before returning (0 defaults to 100).
//
// When failOnGroupError is true, any error fetching groups or their todos is
// fatal. When false, group errors are silently skipped (suitable for cross-list
// aggregation where partial results are acceptable).
func fetchTodosIncludingGroups(ctx context.Context, app *appctx.App, todolistID int64, status string, completed bool, limit int, failOnGroupError bool) (todos []basecamp.Todo, totalCount int, err error) {
	groupsResult, groupsErr := app.Account().TodolistGroups().List(ctx, todolistID, nil)
	if groupsErr != nil {
		if failOnGroupError {
			return nil, 0, groupsErr
		}
		// Fall through — treat as zero groups.
		groupsResult = nil
	}

	hasGroups := groupsResult != nil && len(groupsResult.Groups) > 0

	if !hasGroups {
		// No groups — straightforward fetch with caller's limit.
		opts := &basecamp.TodoListOptions{}
		if status != "" {
			opts.Status = status
		}
		if completed {
			opts.Completed = true
		}
		if limit != 0 {
			opts.Limit = limit
		}
		directResult, err := app.Account().Todos().List(ctx, todolistID, opts)
		if err != nil {
			return nil, 0, err
		}
		return directResult.Todos, directResult.Meta.TotalCount, nil
	}

	// Groups present — fetch everything (Limit: -1) for correct
	// position-ordered merge, then cap to limit before returning.
	directOpts := &basecamp.TodoListOptions{Limit: -1}
	if status != "" {
		directOpts.Status = status
	}
	if completed {
		directOpts.Completed = true
	}
	directResult, err := app.Account().Todos().List(ctx, todolistID, directOpts)
	if err != nil {
		return nil, 0, err
	}

	type positioned struct {
		position int
		todos    []basecamp.Todo
	}

	var items []positioned
	for i := range directResult.Todos {
		t := directResult.Todos[i]
		items = append(items, positioned{position: t.Position, todos: []basecamp.Todo{t}})
	}

	groupOpts := &basecamp.TodoListOptions{Limit: -1}
	if status != "" {
		groupOpts.Status = status
	}
	if completed {
		groupOpts.Completed = true
	}
	for _, g := range groupsResult.Groups {
		groupTodos, err := app.Account().Todos().List(ctx, g.ID, groupOpts)
		if err != nil {
			if failOnGroupError {
				return nil, 0, err
			}
			continue
		}
		items = append(items, positioned{position: g.Position, todos: groupTodos.Todos})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].position < items[j].position
	})

	var result []basecamp.Todo
	for _, item := range items {
		result = append(result, item.todos...)
	}

	totalCount = len(result)
	if limit == 0 {
		// No explicit limit and not --all: apply the same default cap (100)
		// that the SDK uses for the no-groups path.
		limit = 100
	}
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}

	return result, totalCount, nil
}

func listTodosInList(cmd *cobra.Command, app *appctx.App, project, todolist string, assignees []string, sdkStatus string, sdkCompleted bool, limit int, all bool, sortField string, reverse bool) error {
	resolvedTodolist, _, err := app.Names.ResolveTodolist(cmd.Context(), todolist, project)
	if err != nil {
		return err
	}

	todolistID, err := strconv.ParseInt(resolvedTodolist, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid todolist ID")
	}

	// --page 1 is the only valid value (runTodosList rejects 2+) and is the
	// SDK default, so it's always a no-op — no special handling needed.

	// Determine the SDK limit to pass through. fetchTodosIncludingGroups
	// uses this for the no-groups fast path and for cross-list aggregation.
	// When assignee filtering is active, fetch all so client-side filtering
	// doesn't miss matches beyond the default cap.
	sdkLimit := 0 // SDK default
	if all || len(assignees) > 0 {
		sdkLimit = -1
	} else if limit > 0 {
		sdkLimit = limit
	}

	todos, totalCount, err := fetchTodosIncludingGroups(cmd.Context(), app, todolistID, sdkStatus, sdkCompleted, sdkLimit, true)
	if err != nil {
		return convertSDKError(err)
	}

	// Project-scoped --assignee is a client-side filter: this endpoint has no
	// server-side assignee parameter, which is why the fetch above is unlimited
	// whenever one is set. Account-wide the same flag is a real assignee_ids[]
	// query parameter — same spelling, very different cost.
	if len(assignees) > 0 {
		assigneeIDs, err := resolveAssigneeFilterIDs(cmd.Context(), app, assignees)
		if err != nil {
			return err
		}
		if len(assigneeIDs) > 0 {
			filtered := todos[:0]
			for _, todo := range todos {
				if todoMatchesAnyAssignee(todo, assigneeIDs) {
					filtered = append(filtered, todo)
				}
			}
			todos = filtered
			totalCount = len(todos)
		}
	}

	// Apply --limit after client-side filtering so the cap reflects
	// the filtered set, not the pre-filter fetch.
	if len(assignees) > 0 && !all && limit > 0 && len(todos) > limit {
		todos = todos[:limit]
	}

	// Apply client-side sort when requested (field already validated in runTodosList)
	if sortField != "" {
		sortTodos(todos, sortField, reverse)
	}

	respOpts := []output.ResponseOption{
		output.WithEntity("todo"),
		output.WithSummary(fmt.Sprintf("%d todos", len(todos))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("basecamp todos create <content> --list %s", resolvedTodolist),
				Description: "Create a todo",
			},
			output.Breadcrumb{
				Action:      "complete",
				Cmd:         "basecamp todos complete <id>",
				Description: "Complete a todo",
			},
		),
	}

	if notice := output.TruncationNoticeWithTotal(len(todos), totalCount); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}

	return app.OK(todos, respOpts...)
}

// resolveAssigneeFilterIDs resolves the repeatable --assignee into person ids.
// Each value may itself be comma-separated, so both spellings work.
func resolveAssigneeFilterIDs(ctx context.Context, app *appctx.App, assignees []string) ([]int64, error) {
	ids := make([]int64, 0, len(assignees))
	for _, assignee := range assignees {
		resolved, err := resolvePersonRoleIDs(ctx, app, assignee, "Assignee")
		if err != nil {
			return nil, err
		}
		ids = append(ids, resolved...)
	}
	return ids, nil
}

// todoMatchesAnyAssignee reports whether the todo is assigned to any of the
// given people. Any rather than all: --assignee ann --assignee bob asks for
// what either of them is on, matching the server-side assignee_ids[] semantics
// the account-wide path gets for free.
func todoMatchesAnyAssignee(todo basecamp.Todo, assigneeIDs []int64) bool {
	for _, a := range todo.Assignees {
		for _, id := range assigneeIDs {
			if a.ID == id {
				return true
			}
		}
	}
	return false
}

func listAllTodos(cmd *cobra.Command, app *appctx.App, project, todosetFlag string, assignees []string, sdkStatus string, sdkCompleted bool, overdue bool, limit int, all bool, sortField string, reverse bool) error {
	// Position is only meaningful within a single todolist — reject before
	// the --all check so users get the right error message.
	if sortField == "position" {
		return output.ErrUsage("--sort position requires --list (position is per-todolist)")
	}
	// Sorting the aggregate path is only meaningful when the full set is
	// fetched. That happens with --all, or when a client-side filter
	// (assignee/overdue) forces an unlimited per-list fetch below. Otherwise
	// results are sampled per-todolist using default SDK paging and a sort
	// would be misleading.
	if sortField != "" && !all && len(assignees) == 0 && !overdue {
		return output.ErrUsage("--sort requires --all (or --assignee/--overdue) when listing across todolists (results are otherwise sampled per list)")
	}
	// Resolve assignee names to IDs if provided. Client-side again: a todo
	// matches when any one of the named people is on it.
	var assigneeIDs []int64
	if len(assignees) > 0 {
		var err error
		if assigneeIDs, err = resolveAssigneeFilterIDs(cmd.Context(), app, assignees); err != nil {
			return err
		}
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

	// Determine per-list limit to pass through to each fetch (todolists and the
	// listless-todo recordings scan alike). When a client-side filter
	// (assignee/overdue) is active, fetch everything so the post-fetch filter
	// doesn't miss matches beyond the default cap — mirroring the single-list
	// path. Any explicit --limit is then applied after filtering, below.
	sdkLimit := 0 // SDK default
	if all || len(assignees) > 0 || overdue {
		sdkLimit = -1
	} else if limit > 0 {
		sdkLimit = limit
	}

	// Aggregate todos from all todolists, including group-nested todos.
	// The server applies the status/completed filter directly — no client-side
	// status filter is needed (the API is the single source of truth).
	var allTodos []basecamp.Todo
	for _, tl := range todolistsResult.Todolists {
		todos, _, err := fetchTodosIncludingGroups(cmd.Context(), app, tl.ID, sdkStatus, sdkCompleted, sdkLimit, false)
		if err != nil {
			continue // Skip failed todolists
		}
		allTodos = append(allTodos, todos...)
	}

	// Basecamp 5 lets todos live directly under the Todoset without a
	// Todolist. Those "listless" todos are invisible to the per-todolist
	// enumeration above, so fetch them via the Recordings API and merge them
	// in. Assignee/overdue filters below apply to them too. project is already
	// resolved to a numeric ID by this point, so a parse failure signals a bug
	// rather than user input — error out instead of silently dropping them.
	projectID, err := strconv.ParseInt(project, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}
	allTodos = append(allTodos,
		fetchTodosetLevelTodos(cmd.Context(), app, projectID, todosetID, sdkStatus, sdkCompleted, sdkLimit)...)

	// Apply filters
	var result []basecamp.Todo
	for _, todo := range allTodos {
		// Filter by assignee (any of the resolved IDs)
		if len(assigneeIDs) > 0 && !todoMatchesAnyAssignee(todo, assigneeIDs) {
			continue
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

	// When a client-side filter forced an unlimited fetch above, apply the
	// explicit --limit after filtering so the cap reflects the filtered set
	// rather than the pre-filter fetch (mirrors the single-list path).
	if (len(assignees) > 0 || overdue) && !all && limit > 0 && len(result) > limit {
		result = result[:limit]
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
				Cmd:         "basecamp todos create <content> --list <list>",
				Description: "Create a todo",
			},
			output.Breadcrumb{
				Action:      "complete",
				Cmd:         "basecamp todos complete <id>",
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

// fetchTodosetLevelTodos returns todos that live directly under the project's
// Todoset rather than inside a Todolist. Basecamp 5 allows creating such
// "listless" todos; the /todolists/{id}/todos.json index endpoint the SDK uses
// to enumerate a todoset's lists cannot see them (it 404s when handed a Todoset
// ID). They are only reachable via the Recordings API, which returns every Todo
// in the bucket regardless of parent. We fetch those recordings, keep the ones
// parented directly to this todoset, and hydrate each into a full Todo — the
// Recording payload the SDK exposes lacks the completion/assignee/due fields the
// list output and its filters need.
//
// completed mirrors the server-side completion filter applied to todolist todos.
// The Recordings status is lifecycle-only (active/archived/trashed) and does not
// distinguish completed from incomplete, so that split is applied client-side.
//
// limit mirrors the per-list limit the todolist path uses (0 = SDK default of
// 100, -1 = all, positive = cap) and governs how many *listless* todos are kept.
// The cap is applied after filtering by parent — not to the raw recordings,
// which also include Todolist-parented todos that would otherwise consume the
// budget and hide listless todos sorted behind them.
//
// The recordings endpoint has no parent-type filter, so listless todos can only
// be found by scanning Todo recordings. To avoid a full-project traversal on
// ordinary runs, the scan itself is bounded: --all scans everything for an
// exhaustive result, while limited/default runs scan only a window of the most
// recent Todo recordings (at least DefaultRecordingLimit, more when --limit asks
// for more). Listless todos outside that window require --all — the same
// best-effort tradeoff the aggregate path already documents for per-list limits.
//
// Errors are non-fatal: the caller still gets the todolist todos.
func fetchTodosetLevelTodos(ctx context.Context, app *appctx.App, projectID, todosetID int64, sdkStatus string, completed bool, limit int) []basecamp.Todo {
	recStatus := sdkStatus
	if recStatus == "" {
		recStatus = "active"
	}

	// Cap on kept listless todos: 0 → SDK default of 100, negative (--all) →
	// unlimited, positive → explicit cap.
	maxKept := limit
	if maxKept == 0 {
		maxKept = basecamp.DefaultRecordingLimit
	}

	// Bound how many recordings we scan. --all (limit < 0) scans everything;
	// otherwise scan a window sized to what we might keep, with a floor so a
	// small --limit still scans a useful slice rather than stopping at the first
	// few recordings (which could all be Todolist-parented).
	recScan := -1 // unlimited
	if limit >= 0 {
		recScan = maxKept
		if recScan < basecamp.DefaultRecordingLimit {
			recScan = basecamp.DefaultRecordingLimit
		}
	}

	// Sort/Direction are set explicitly so the bounded scan window is a stable
	// "most recently created first" slice rather than relying on SDK defaults.
	result, err := app.Account().Recordings().List(ctx, basecamp.RecordingTypeTodo, &basecamp.RecordingsListOptions{
		Bucket:    []int64{projectID},
		Status:    recStatus,
		Sort:      "created_at",
		Direction: "desc",
		Limit:     recScan,
	})
	if err != nil {
		return nil
	}

	var todos []basecamp.Todo
	for _, rec := range result.Recordings {
		if rec.Parent == nil || rec.Parent.Type != "Todoset" || rec.Parent.ID != todosetID {
			continue
		}
		if maxKept >= 0 && len(todos) >= maxKept {
			break
		}

		todo, err := app.Account().Todos().Get(ctx, rec.ID)
		if err != nil {
			continue // Skip todos we can't hydrate.
		}

		// Recordings' lifecycle status can't express completed vs incomplete,
		// so apply that split here to match the todolist path. Only do so for
		// the active view (sdkStatus == ""); archived/trashed views return all
		// matching todos regardless of completion, just like the todolist path.
		if sdkStatus == "" && todo.Completed != completed {
			continue
		}

		todos = append(todos, *todo)
	}

	return todos
}

func newTodosShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show todo details",
		Long: `Display detailed information about a todo.

You can pass either a todo ID or a Basecamp URL:
  basecamp todos show 789
  basecamp todos show https://3.basecamp.com/123/buckets/456/todos/789`,
		Args: cobra.ExactArgs(1),
	}

	dlDir := addDownloadAttachmentsFlag(cmd)
	cf := addCommentFlags(cmd, false)

	cmd.RunE = func(cmd *cobra.Command, args []string) error {

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

		enrichment := fetchCommentsForRecording(cmd.Context(), app, todoIDStr, cf)

		opts := []output.ResponseOption{
			output.WithEntity("todo"),
			output.WithBreadcrumbs(
				output.Breadcrumb{
					Action:      "update",
					Cmd:         fmt.Sprintf("basecamp todos update %d --title <title>", todoID),
					Description: "Update this todo",
				},
				output.Breadcrumb{
					Action:      "complete",
					Cmd:         fmt.Sprintf("basecamp todos complete %d", todoID),
					Description: "Complete this todo",
				},
				output.Breadcrumb{
					Action:      "comment",
					Cmd:         fmt.Sprintf("basecamp comments create %d <text>", todoID),
					Description: "Add comment",
				},
			),
		}

		if len(todo.Steps) > 0 {
			opts = append(opts, output.WithBreadcrumbs(
				output.Breadcrumb{
					Action:      "steps",
					Cmd:         "basecamp cards step complete <step-id>",
					Description: "Complete a step (step IDs in --json output)",
				},
			))
		}

		data := any(todo)
		attachmentNotice := ""
		attachments := downloadableAttachments(richtext.ParseAttachments(todo.Description))
		if len(attachments) > 0 {
			dl := runDownloadAttachments(cmd, app, attachments, dlDir)
			var dlResults []attachmentResult
			if dl != nil {
				dlResults = dl.Results
			}
			data = withAttachmentMeta(todo, "description", attachments, dlResults)
			attachmentNotice = fmt.Sprintf("%d attachment(s) — download: basecamp attachments download %s",
				len(attachments), todoIDStr)
			if dl != nil && dl.Notice != "" {
				attachmentNotice += "; " + dl.Notice
			}
			opts = append(opts,
				output.WithBreadcrumbs(attachmentBreadcrumb(todoIDStr, len(attachments))),
			)
		}

		data, extraOpts := enrichment.apply(data, attachmentNotice)
		opts = append(opts, extraOpts...)

		return app.OK(data, opts...)
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
	var notifyOnCompletion string
	var loose bool

	cmd := &cobra.Command{
		Use:   "create <content>",
		Short: "Create a new todo",
		Long: `Create a new todo in a project.

By default a todo goes into a to-do list. --loose creates it directly on the
project's to-do set instead, outside any list:

  basecamp todos create "Call the vendor back" --loose --in <project>

--loose needs no list, so it neither prompts for one nor accepts --list.

Use - as the content argument to read the todo title from stdin:
  printf 'Call the vendor back' | basecamp todos create - --in <project>`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			// Show help when invoked with no content
			if len(args) == 0 {
				return missingArg(cmd, "<content>")
			}

			// --loose and a named list are mutually exclusive, and that is
			// knowable from the flags alone. Decide it before the pipe is
			// drained: a doomed invocation should not make the caller wait on a
			// producer, and a blank pipe must not answer "stdin is empty"
			// instead of naming the conflict. The destination resolution below
			// still repeats the check, since a configured todolist is only one
			// of its inputs.
			if loose && (cmd.Flags().Changed("list") || todolist != "" || app.Flags.Todolist != "") {
				return output.ErrUsageHint(
					"--loose creates a todo outside any list, so it cannot be combined with --list",
					"Drop --list to create on the to-do set, or drop --loose to create in that list")
			}

			// Attachment paths are readable or not regardless of the body, so
			// check them before the pipe is drained.
			if err := validateAttachPaths(attachFiles); err != nil {
				return err
			}

			content, err := resolveContentArg(cmd, args, 0)
			if err != nil {
				return err
			}
			if strings.TrimSpace(content) == "" {
				return cmd.Help()
			}

			description, err := resolveContentValue(cmd, description, -1, "--description")
			if err != nil {
				return err
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

			// --loose creates directly on the to-do set, outside any list, so it
			// resolves a todoset and skips todolist resolution entirely — there
			// is no list to name, prompt for, or fall back to.
			var resolvedTodolist, resolvedTodoset string
			if loose {
				if cmd.Flags().Changed("list") || app.Flags.Todolist != "" {
					return output.ErrUsageHint(
						"--loose creates a todo outside any list, so it cannot be combined with --list",
						"Drop --list to create on the to-do set, or drop --loose to create in that list")
				}

				resolvedTodoset, err = ensureTodoset(cmd, app, project, todoset)
				if err != nil {
					return err
				}
			} else {
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
				resolvedTodolist, err = resolveTodolistInTodoset(cmd, app, todolist, project, todoset)
				if err != nil {
					return err
				}
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
			if strings.TrimSpace(notifyOnCompletion) != "" {
				subscriberIDs, err := resolveCompletionSubscriberIDs(cmd.Context(), app, notifyOnCompletion)
				if err != nil {
					return err
				}
				req.CompletionSubscriberIDs = subscriberIDs
			}

			var todo *basecamp.Todo
			if loose {
				projectID, parseErr := strconv.ParseInt(project, 10, 64)
				if parseErr != nil {
					return output.ErrUsage("Invalid project ID")
				}
				todosetID, parseErr := strconv.ParseInt(resolvedTodoset, 10, 64)
				if parseErr != nil {
					return output.ErrUsage("Invalid todoset ID")
				}

				// Creates are not idempotent and the SDK does not retry them, so
				// a transient failure here surfaces as a plain error rather than
				// risking a duplicate todo.
				todo, err = app.Account().Todos().CreateInTodoset(cmd.Context(), projectID, todosetID, req)
			} else {
				todolistID, parseErr := strconv.ParseInt(resolvedTodolist, 10, 64)
				if parseErr != nil {
					return output.ErrUsage("Invalid todolist ID")
				}

				todo, err = app.Account().Todos().Create(cmd.Context(), todolistID, req)
			}
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
						Cmd:         fmt.Sprintf("basecamp todos complete %d", todo.ID),
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
	cmd.Flags().StringVar(&description, "description", "", "Extended description (Markdown); use - to read from stdin")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")
	cmd.Flags().StringVar(&notifyOnCompletion, "notify-on-completion", "", "People to notify when done (names or IDs, comma-separated)")
	// Not --todoset: that flag already means "which to-do set", and this one
	// means "no list at all".
	cmd.Flags().BoolVar(&loose, "loose", false, "Create on the to-do set, outside any list")

	allowDash(cmd, "arg:0+", "flag:description")

	// Register tab completion for flags
	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("project", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("in", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("assignee", completer.PeopleNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("to", completer.PeopleNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("notify-on-completion", completer.PeopleNameCompletion())

	return cmd
}

func newTodosUpdateCmd() *cobra.Command {
	var title string
	var description string
	var assignee string
	var due string
	var startsOn string
	var notify bool
	var noDue bool
	var noStartsOn bool
	var noDescription bool
	var notifyOnCompletion string
	var noNotifyOnCompletion bool

	cmd := &cobra.Command{
		Use:   "update <id|url> [title]",
		Short: "Update a todo",
		Long: `Update an existing todo.

You can pass either a todo ID or a Basecamp URL:
  basecamp todos update 789 "New title"
  basecamp todos update 789 --title "New title"
  basecamp todos update 789 --due "next friday"
  basecamp todos update https://3.basecamp.com/123/buckets/456/todos/789 --description "Details"

Clear a field by passing its --no- flag or an empty value:
  basecamp todos update 789 --no-due
  basecamp todos update 789 --due ""
  basecamp todos update 789 --no-description

Set or clear the people notified when the todo is completed:
  basecamp todos update 789 --notify-on-completion "Jane Smith,Bob"
  basecamp todos update 789 --no-notify-on-completion`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>")
			}

			// Conflict detection: --no-X and --X with a value are contradictory
			if noDue && strings.TrimSpace(due) != "" {
				return output.ErrUsage("--no-due and --due cannot be used together")
			}
			if noStartsOn && strings.TrimSpace(startsOn) != "" {
				return output.ErrUsage("--no-starts-on and --starts-on cannot be used together")
			}
			if noDescription && strings.TrimSpace(description) != "" {
				return output.ErrUsage("--no-description and --description cannot be used together")
			}
			if noNotifyOnCompletion && strings.TrimSpace(notifyOnCompletion) != "" {
				return output.ErrUsage("--no-notify-on-completion and --notify-on-completion cannot be used together")
			}
			// Detect clear intent: explicit --no-X flag or empty value via --X ""
			clearDue := noDue || (cmd.Flags().Changed("due") && strings.TrimSpace(due) == "")
			clearStarts := noStartsOn || (cmd.Flags().Changed("starts-on") && strings.TrimSpace(startsOn) == "")
			clearDescription := noDescription || (cmd.Flags().Changed("description") && strings.TrimSpace(description) == "")
			clearSubscribers := noNotifyOnCompletion || (cmd.Flags().Changed("notify-on-completion") && strings.TrimSpace(notifyOnCompletion) == "")

			// Clearing due while setting starts is contradictory (Basecamp enforces starts <= due)
			if clearDue && strings.TrimSpace(startsOn) != "" {
				return output.ErrUsage("cannot clear due date and set start date together (Basecamp requires a due date when a start date is set)")
			}

			// Positional title: args[1:] joined
			positionalTitle := strings.Join(args[1:], " ")

			// Effective title: positional takes precedence over --title flag
			effectiveTitle := title
			if strings.TrimSpace(positionalTitle) != "" {
				effectiveTitle = positionalTitle
			}

			// No-op guard: at least one effective field required
			assigneeChanged := (cmd.Flags().Changed("assignee") || cmd.Flags().Changed("to")) && strings.TrimSpace(assignee) != ""
			subscribersChanged := cmd.Flags().Changed("notify-on-completion") && strings.TrimSpace(notifyOnCompletion) != ""
			if strings.TrimSpace(effectiveTitle) == "" &&
				strings.TrimSpace(description) == "" &&
				strings.TrimSpace(due) == "" && strings.TrimSpace(startsOn) == "" &&
				!assigneeChanged && !subscribersChanged &&
				(!cmd.Flags().Changed("notify") || !notify) &&
				!clearDue && !clearStarts && !clearDescription && !clearSubscribers {
				return noChanges(cmd)
			}

			// Extract ID from URL if provided
			todoIDStr := extractID(args[0])
			todoID, err := strconv.ParseInt(todoIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todo ID")
			}

			// Syntactic checks first, then "-", then account and network: a
			// malformed ID is answered without waiting on the producer, and a
			// blank pipe cannot mask it. Only an exact "-" reads stdin;
			// --description "" stays the clear idiom.
			description, err := resolveContentValue(cmd, description, -1, "--description")
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Pre-Edit validation and resolution — no todo HTTP happens here.
			// Image uploads are deferred into the Edit closure so a missing
			// todo can't orphan uploaded attachments.
			var descHTML string
			if !clearDescription && description != "" {
				descHTML = richtext.MarkdownToHTML(description)
			}

			var parsedDue string
			if !clearDue && strings.TrimSpace(due) != "" {
				parsedDue = dateparse.Parse(due)
				if _, err := time.Parse("2006-01-02", parsedDue); err != nil {
					return output.ErrUsage(fmt.Sprintf("Invalid due date: %q", due))
				}
			}
			var parsedStarts string
			if !clearStarts && !clearDue && strings.TrimSpace(startsOn) != "" {
				parsedStarts = dateparse.Parse(startsOn)
				if _, err := time.Parse("2006-01-02", parsedStarts); err != nil {
					return output.ErrUsage(fmt.Sprintf("Invalid start date: %q", startsOn))
				}
			}

			var assigneeIDs []int64
			if assigneeChanged {
				if assigneeIDs, err = resolveAssigneeIDs(cmd.Context(), app, assignee); err != nil {
					return err
				}
			}
			var subscriberIDs []int64
			if subscribersChanged {
				if subscriberIDs, err = resolveCompletionSubscriberIDs(cmd.Context(), app, notifyOnCompletion); err != nil {
					return err
				}
			}

			todo, err := app.Account().Todos().Edit(cmd.Context(), todoID, func(f *basecamp.TodoFields) error {
				// Fail closed on unverifiable preserved subscriber state
				// (#538): field presence is the server/SDK contract, but the
				// CLI still refuses to write back subscriber IDs it can't
				// trust.
				if !subscribersChanged && !clearSubscribers {
					for _, id := range f.CompletionSubscriberIDs {
						if id <= 0 {
							return fmt.Errorf("cannot verify current completion subscribers for todo %d: subscriber with missing or invalid id", todoID)
						}
					}
				}
				if effectiveTitle != "" {
					f.Content = effectiveTitle
				}
				if clearDescription {
					f.Description = ""
				} else if descHTML != "" {
					// Uploads happen here, after Edit's GET confirmed the
					// todo exists.
					resolved, err := resolveLocalImages(cmd, app, descHTML)
					if err != nil {
						return err
					}
					f.Description = resolved
				}
				// Clearing due also clears starts (Basecamp enforces
				// starts <= due).
				if clearDue {
					f.DueOn, f.StartsOn = "", ""
				} else if parsedDue != "" {
					f.DueOn = parsedDue
				}
				if clearStarts {
					f.StartsOn = ""
				} else if !clearDue && parsedStarts != "" {
					f.StartsOn = parsedStarts
				}
				if assigneeChanged {
					f.AssigneeIDs = assigneeIDs
				}
				if subscribersChanged {
					f.CompletionSubscriberIDs = subscriberIDs
				} else if clearSubscribers {
					f.CompletionSubscriberIDs = []int64{}
				}
				if cmd.Flags().Changed("notify") && notify {
					f.Notify = true
				}
				return nil
			})
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(todo,
				output.WithEntity("todo"),
				output.WithSummary(fmt.Sprintf("Updated todo #%s", todoIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp todos show %s", todoIDStr),
						Description: "View todo",
					},
					output.Breadcrumb{
						Action:      "complete",
						Cmd:         fmt.Sprintf("basecamp todos complete %s", todoIDStr),
						Description: "Complete todo",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "Todo title (plain text)")
	cmd.Flags().StringVar(&description, "description", "", "Extended description (Markdown); use - to read from stdin")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Assignees (names or IDs, comma-separated)")
	cmd.Flags().StringVar(&assignee, "to", "", "Assignees (alias for --assignee)")
	cmd.Flags().StringVarP(&due, "due", "d", "", "Due date (natural language or YYYY-MM-DD)")
	cmd.Flags().StringVar(&startsOn, "starts-on", "", "Start date (natural language or YYYY-MM-DD)")
	cmd.Flags().BoolVar(&notify, "notify", false, "Notify assignees")
	cmd.Flags().BoolVar(&noDue, "no-due", false, "Clear the due date")
	cmd.Flags().BoolVar(&noStartsOn, "no-starts-on", false, "Clear the start date")
	cmd.Flags().BoolVar(&noDescription, "no-description", false, "Clear the description")
	cmd.Flags().StringVar(&notifyOnCompletion, "notify-on-completion", "", "People to notify when done (names or IDs, comma-separated)")
	cmd.Flags().BoolVar(&noNotifyOnCompletion, "no-notify-on-completion", false, "Clear the people notified when done")

	// Register tab completion for people flags
	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("assignee", completer.PeopleNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("to", completer.PeopleNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("notify-on-completion", completer.PeopleNameCompletion())

	allowDash(cmd, "flag:description")

	return cmd
}

// resolveCompletionSubscriberIDs resolves --notify-on-completion values
// (comma-separated names or IDs) with completion-subscriber wording in errors.
func resolveCompletionSubscriberIDs(ctx context.Context, app *appctx.App, input string) ([]int64, error) {
	return resolvePersonRoleIDs(ctx, app, input, "Completion subscriber")
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
			Cmd:         fmt.Sprintf("basecamp todos uncomplete %s", extractedIDs[0]),
			Description: "Reopen todo",
		},
	}

	// Return single todo directly (like basecamp todos create does), list for multiple
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

			// Require at least one filter. This gate decides the invocation on
			// its own, so it runs before the pipe is drained: otherwise the
			// caller waits on a producer whose output is already discarded, and
			// a blank pipe answers "stdin is empty" instead of naming the
			// missing filter.
			if !overdueOnly && assignee == "" {
				return output.ErrUsageHint("Sweep requires a filter", "Use --overdue or --assignee to select todos")
			}

			comment, err := resolveContentValue(cmd, comment, -1, "--comment")
			if err != nil {
				return err
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
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

			// Convert comment through rich text pipeline
			commentHTML := comment
			var mentionNotice string
			if comment != "" {
				commentHTML = richtext.MarkdownToHTML(comment)
				var pipelineErr error
				commentHTML, pipelineErr = resolveLocalImages(cmd, app, commentHTML)
				if pipelineErr != nil {
					return pipelineErr
				}
				mentionResult, pipelineErr := resolveMentions(cmd.Context(), app.Names, commentHTML)
				if pipelineErr != nil {
					return pipelineErr
				}
				commentHTML = mentionResult.HTML
				mentionNotice = unresolvedMentionWarning(mentionResult.Unresolved)
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
					req := &basecamp.CreateCommentRequest{Content: commentHTML}
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

			respOpts := []output.ResponseOption{
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("basecamp todos --in %s", project),
						Description: "List todos",
					},
				),
			}
			if mentionNotice != "" {
				respOpts = append(respOpts, output.WithDiagnostic(mentionNotice))
			}
			return app.OK(result, respOpts...)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().StringVarP(&todoset, "todoset", "t", "", "Todoset ID (for projects with multiple todosets)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Filter by assignee")
	cmd.Flags().BoolVar(&overdueOnly, "overdue", false, "Filter overdue todos")
	cmd.Flags().StringVarP(&comment, "comment", "c", "", "Comment to add to matching todos; use - to read from stdin")
	cmd.Flags().BoolVar(&complete, "complete", false, "Mark matching todos as complete")
	cmd.Flags().BoolVar(&complete, "done", false, "Mark matching todos as complete (alias)")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "Preview without making changes")

	allowDash(cmd, "flag:comment")

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
			Cmd:         fmt.Sprintf("basecamp todos complete %s", extractedIDs[0]),
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
	var (
		position int
		list     string
	)

	cmd := &cobra.Command{
		Use:     "position <id|url>",
		Aliases: []string{"move", "reorder"},
		Short:   "Change todo position or move between lists",
		Long: `Reorder a todo within its todolist, or move it to a different list in the
same project. Position is 1-based (1 = top).

You can pass either a todo ID or a Basecamp URL:
  basecamp todos position 789 --to 1
  basecamp todos position https://3.basecamp.com/123/buckets/456/todos/789 --to 1

Move to a different todolist in the same project:
  basecamp todos position 789 --to 1 --list "Sprint 1" --in myproject
  basecamp todos position 789 --to 1 --list 321
  basecamp todos position <todo-url> --to 1 --list <todolist-url>`,
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

			// Extract todo ID and project from URL if provided
			todoIDStr, todoProjectID := extractWithProject(args[0])

			todoID, err := strconv.ParseInt(todoIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todo ID")
			}

			// Resolve destination todolist when --list is provided
			var parentID *int64
			if list != "" {
				listIDStr, listProjectID := extractWithProject(list)

				// When --list is a URL, validate it's a todolist URL — not a
				// todo, project, or collection URL that would silently extract
				// the wrong ID.
				if parsed := urlarg.Parse(list); parsed != nil {
					if parsed.RecordingID == "" || parsed.Type != "todolists" || parsed.IsCollection {
						return output.ErrUsage("Expected a todolist URL (.../todolists/<id>), " +
							"or pass a todolist ID or name.")
					}
				}

				// Build project context: todo URL > --in flag > config
				project := todoProjectID
				if project == "" {
					project = app.Flags.Project
				}
				if project == "" {
					project = app.Config.ProjectID
				}

				// Resolve project name to numeric ID only when needed:
				// cross-project URL validation or todolist name resolution.
				resolvedProject := project
				needsResolve := (todoProjectID != "" && listProjectID != "") || !isNumeric(listIDStr)
				if needsResolve && project != "" && !isNumeric(project) {
					rp, _, resolveErr := app.Names.ResolveProject(cmd.Context(), project)
					if resolveErr != nil {
						return resolveErr
					}
					resolvedProject = rp
				}

				// Cross-project moves are not supported by the reposition endpoint.
				// Only enforce when the todo's project comes from its URL (high
				// confidence). Config/flag project is a default context — it may
				// not match where a bare-ID todo actually lives.
				if todoProjectID != "" && listProjectID != "" && resolvedProject != listProjectID {
					return output.ErrUsageHint(
						"Cannot move a todo to a list in a different project.",
						"Pass a todolist from the same project; cross-project moves are not supported.",
					)
				}

				// Resolve todolist name to ID when not already numeric
				if !isNumeric(listIDStr) {
					if resolvedProject == "" {
						return output.ErrUsage("--in is required to resolve todolist names")
					}
					resolved, resolveErr := resolveTodolistInTodoset(cmd, app, listIDStr, resolvedProject, "")
					if resolveErr != nil {
						return resolveErr
					}
					listIDStr = resolved
				}

				listID, parseErr := strconv.ParseInt(listIDStr, 10, 64)
				if parseErr != nil {
					return output.ErrUsage("Invalid todolist ID")
				}
				parentID = &listID
			}

			err = app.Account().Todos().Reposition(cmd.Context(), todoID, position, parentID)
			if err != nil {
				return convertSDKError(err)
			}

			summary := fmt.Sprintf("Moved todo #%d to position %d", todoID, position)
			if parentID != nil {
				summary = fmt.Sprintf("Moved todo #%d to list #%d at position %d", todoID, *parentID, position)
			}

			response := map[string]any{"repositioned": true, "position": position}
			if parentID != nil {
				response["todolist_id"] = *parentID
			}

			return app.OK(response,
				output.WithSummary(summary),
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
	cmd.Flags().StringVarP(&list, "list", "l", "", "Destination todolist ID, name, or URL (move to a different list)")

	return cmd
}
