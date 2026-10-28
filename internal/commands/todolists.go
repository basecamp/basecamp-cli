package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
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
		newTodolistsShowCmd(),
		newTodolistsCreateCmd(&project, &todosetID),
		newTodolistsUpdateCmd(&project),
		newTodolistsPositionCmd(&project),
		newTodolistsCopyCmd(&project),
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

func newTodolistsShowCmd() *cobra.Command {
	var cf *commentFlags

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

			// The list is read by its account-wide ID, so no project is needed.
			todolistIDStr, _, err := extractWithProject(args[0])
			if err != nil {
				return err
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
			// A moved list's old id redirects to the list at its new id, and
			// only the new id takes its to-dos and comments.
			todolistIDStr = strconv.FormatInt(todolist.ID, 10)

			enrichment := fetchCommentsForRecording(cmd.Context(), app, todolistIDStr, cf)
			data, commentOpts := enrichment.apply(todolist, "")

			opts := make([]output.ResponseOption, 0, 3+len(commentOpts))
			opts = append(opts,
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
						Cmd:         fmt.Sprintf("basecamp todos create <content> --list %s", todolistIDStr),
						Description: "Add todo",
					},
				),
			)
			opts = append(opts, commentOpts...)

			return app.OK(data, opts...)
		},
	}

	cf = addCommentFlags(cmd, false)

	return cmd
}

func newTodolistsCreateCmd(project, todosetID *string) *cobra.Command {
	var description string
	var visibleToClients bool

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

			if err := requireNumericID(*todosetID, "todoset ID"); err != nil {
				return err
			}

			description, err := resolveContentValue(cmd, description, -1, "--description")
			if err != nil {
				return err
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

			// Set client visibility only when the flag was provided. Omitting it
			// uses the server's default: team-only when posting as a team member,
			// but a client-authenticated caller always creates client-visible
			// records (an explicit false is overridden server-side).
			if cmd.Flags().Changed("visible-to-clients") {
				req.VisibleToClients = &visibleToClients
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
						Cmd:         fmt.Sprintf("basecamp todos create <content> --list %s", todolistIDStr),
						Description: "Add todo",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(todosetID, "todoset", "t", "", "Todoset ID (for projects with multiple todosets)")
	cmd.Flags().StringVarP(&description, "description", "d", "", "Todolist description (rich text HTML); use - to read from stdin")
	cmd.Flags().BoolVar(&visibleToClients, "visible-to-clients", false, "Make the todolist visible to clients on the project (omit for the server default; client-authenticated callers always post client-visible)")

	allowDash(cmd, "flag:description")

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

			// Extract ID and project from URL if provided
			todolistIDStr, urlProjectID, err := extractWithProject(args[0])
			if err != nil {
				return err
			}

			// Parse todolist ID as int64
			todolistID, err := strconv.ParseInt(todolistIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid todolist ID")
			}

			// Syntactic checks first, then "-", then account and network: a
			// malformed ID is answered without waiting on the producer, and a
			// blank pipe cannot mask it.
			description, err := resolveContentValue(cmd, description, -1, "--description")
			if err != nil {
				return err
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
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
	cmd.Flags().StringVarP(&description, "description", "d", "", "New todolist description (rich text HTML); use - to read from stdin")

	allowDash(cmd, "flag:description")

	return cmd
}

func newTodolistsPositionCmd(project *string) *cobra.Command {
	var position int
	var dest todolistDestination

	cmd := &cobra.Command{
		Use:     "position <id|url>...",
		Aliases: []string{"move", "reorder"},
		Short:   "Move a to-do list or group to another project, or reorder lists",
		Long: `Move a to-do list, with its to-dos, to another project as the web's Move…
does, or reorder lists within their To-dos. Both are "todolists move".

A to-do list goes into a project's To-dos. It gets a new ID there; the command
waits for Basecamp and prints the moved list:
  basecamp todolists move 789 --to-project "Marketing"
  basecamp todolists move 789 --to-project "Marketing" --to-todoset 1069478882

A group (a list inside a list) goes into a to-do list and stays a group there:
  basecamp todolists move 456 --to-project "Marketing" --to-list "Launch"
  basecamp todolists move 456 --to-list https://3.basecamp.com/1/buckets/2/todolists/321

Reorder within its To-dos. Position is 1-based (1 = top).

  basecamp todolists position 789 --to 1
  basecamp todolists position https://3.basecamp.com/1/buckets/2/todolists/789 --to 1

Pass several todolists to set their order, top to bottom. Bulk reordering always
places them at the top of the todoset; every list must live in the same todoset
and be incomplete (completed lists are positioned separately by Basecamp):

  basecamp todolists position 701 702 703 704 705

Positioning is relative and cascades: sibling lists shift to make room, and the
server translates the position for loose to-dos and hidden completed lists.
Confirm with ` + "`basecamp todolists list`" + `.`,
		Annotations: map[string]string{
			"agent_notes": "--to-project/--to-todoset/--to-list move one list or group to another project (the web's Move…); --to/--position reorder lists within their To-dos and cannot be combined with them. " +
				todolistFilingNotes + " The old id stops accepting changes, and reading it follows the move.",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>...")
			}
			if destinationFlagChanged(cmd) {
				if err := rejectRepositionWithFiling(cmd, "--to, --position"); err != nil {
					return err
				}
				if len(args) > 1 {
					return output.ErrUsage("Move one to-do list or group at a time to another project")
				}
				return fileTodolist(cmd, basecamp.FilingMove, args[0], *project, dest, false)
			}

			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// A pasted URL for a different recording type (a todo, card, or a
			// collection URL) still extracts a trailing numeric ID, which would
			// silently reposition an unrelated todolist. Require any URL arg to
			// be a non-collection todolists URL before trusting its ID.
			for _, arg := range args {
				for _, token := range strings.Split(arg, ",") {
					token = strings.TrimSpace(token)
					if token == "" {
						continue
					}
					if parsed := urlarg.Parse(token); parsed != nil {
						if parsed.RecordingID == "" || parsed.Type != "todolists" || parsed.IsCollection {
							return output.ErrUsage("Expected a todolist URL (.../todolists/<id>), or pass a todolist ID.")
						}
					}
				}
			}

			// ExtractIDs drops empty segments, so a bare "," yields zero IDs.
			ids, err := extractIDs(args)
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				return missingArg(cmd, "<id|url>...")
			}

			// Distinguish an omitted flag from an explicit --to 0.
			supplied := cmd.Flags().Changed("to") || cmd.Flags().Changed("position")
			if supplied && position < 1 {
				return output.ErrUsage("--to must be at least 1 (1 = top)")
			}
			switch {
			case len(ids) == 1 && !supplied:
				return output.ErrUsage("--to is required (1 = top)")
			case len(ids) > 1 && supplied && position != 1:
				return output.ErrUsage("Reordering multiple todolists is only supported at position 1; " +
					"drop --to or pass --to 1")
			case !supplied:
				position = 1 // bulk default
			}

			// Validate every ID and reject duplicates before mutating anything.
			parsed := make([]int64, 0, len(ids))
			seen := make(map[int64]bool, len(ids))
			for _, idStr := range ids {
				id, err := strconv.ParseInt(idStr, 10, 64)
				if err != nil {
					return output.ErrUsage("Invalid todolist ID")
				}
				if seen[id] {
					return output.ErrUsage(fmt.Sprintf("Duplicate todolist ID %d", id))
				}
				seen[id] = true
				parsed = append(parsed, id)
			}

			// Sibling preflight (bulk only): every list must belong to the same
			// todoset and bucket, and be incomplete, before any PUT is issued.
			var bucketID, parentID int64
			if len(parsed) > 1 {
				for _, id := range parsed {
					tl, err := app.Account().Todolists().Get(cmd.Context(), id)
					if err != nil {
						return convertSDKError(err)
					}
					if tl.Parent == nil || tl.Bucket == nil {
						return output.ErrUsageHint(
							fmt.Sprintf("Todolist #%d is missing its todoset or project context.", id),
							"Reorder one todoset at a time.",
						)
					}
					if tl.Completed {
						return output.ErrUsageHint(
							fmt.Sprintf("Todolist #%d (%s) is completed; bulk reordering accepts incomplete lists only.", id, tl.Name),
							"Basecamp positions completed lists separately — reorder the incomplete lists on their own.",
						)
					}
					if parentID == 0 {
						parentID = tl.Parent.ID
						bucketID = tl.Bucket.ID
					} else if tl.Parent.ID != parentID || tl.Bucket.ID != bucketID {
						return output.ErrUsageHint(
							fmt.Sprintf("Todolist #%d belongs to a different todoset (#%d) than the others.", id, tl.Parent.ID),
							"Reorder one todoset at a time.",
						)
					}
				}
			}

			// Apply in reverse so the typed order lands top→bottom onto position 1.
			// Stop at the first failure: a half-applied reorder is a wrong order.
			applied := 0
			for i := len(parsed) - 1; i >= 0; i-- {
				if err := app.Account().Todolists().Reposition(cmd.Context(), parsed[i], position); err != nil {
					converted := convertSDKError(err)
					// No PUT has landed yet (single-list mode always takes this
					// path): the todoset is untouched. Surface the underlying
					// error unchanged — keep its code/category/hint and don't
					// claim an intermediate order that doesn't exist.
					if applied == 0 {
						return converted
					}
					// A partial reorder has landed: the todoset is now in an
					// intermediate order. Add applied-count accounting and a
					// rerun hint while preserving the underlying error's
					// classification (never reclassify a transport/runtime
					// failure as a usage error).
					msg := fmt.Sprintf("Reordered %d of %d todolists; failed at #%d", applied, len(parsed), parsed[i])
					const rerun = "Rerun the whole command once the cause is fixed; the todoset is now in an intermediate order."
					var outErr *output.Error
					if errors.As(converted, &outErr) {
						hint := rerun
						if outErr.Hint != "" {
							hint = outErr.Hint + " " + rerun
						}
						return &output.Error{
							Code:       outErr.Code,
							Message:    fmt.Sprintf("%s: %s", msg, outErr.Message),
							Hint:       hint,
							HTTPStatus: outErr.HTTPStatus,
							Retryable:  outErr.Retryable,
							Cause:      outErr,
						}
					}
					// Raw transport/runtime failure (e.g. a dropped connection or
					// a canceled context): no *output.Error to preserve, but the
					// partial reorder still demands the rerun warning. Wrap it in
					// a non-usage structured error that carries the hint and the
					// raw cause (Unwrap keeps errors.Is/As working).
					return &output.Error{
						Code:    output.CodeAPI,
						Message: fmt.Sprintf("%s: %s", msg, converted.Error()),
						Hint:    rerun,
						Cause:   converted,
					}
				}
				applied++
			}

			var summary string
			var breadcrumbs []output.Breadcrumb
			if len(parsed) == 1 {
				summary = fmt.Sprintf("Moved todolist #%d to position %d", parsed[0], position)
				// No preflight Get in single mode: resolve project from ambient
				// context (URL > group flag > flags > config) for the breadcrumbs.
				_, urlProjectID, err := extractWithProject(args[0])
				if err != nil {
					return err
				}
				proj := urlProjectID
				if proj == "" {
					proj = *project
				}
				if proj == "" {
					proj = app.Flags.Project
				}
				if proj == "" {
					proj = app.Config.ProjectID
				}
				if proj != "" {
					breadcrumbs = []output.Breadcrumb{
						{
							Action:      "show",
							Cmd:         fmt.Sprintf("basecamp todolists show %d --in %s", parsed[0], proj),
							Description: "View todolist",
						},
						{
							Action:      "list",
							Cmd:         fmt.Sprintf("basecamp todolists list --in %s", proj),
							Description: "List todolists",
						},
					}
				}
			} else {
				summary = fmt.Sprintf("Reordered %d todolists to the top of the todoset", len(parsed))
				// Build from authoritative preflight data, not ambient project.
				breadcrumbs = []output.Breadcrumb{
					{
						Action:      "list",
						Cmd:         fmt.Sprintf("basecamp todolists list --in %d --todoset %d", bucketID, parentID),
						Description: "List todolists",
					},
				}
			}

			opts := []output.ResponseOption{output.WithSummary(summary)}
			if len(breadcrumbs) > 0 {
				opts = append(opts, output.WithBreadcrumbs(breadcrumbs...))
			}

			return app.OK(map[string]any{
				"repositioned": true,
				"position":     position,
				"todolist_ids": parsed,
			}, opts...)
		},
	}

	cmd.Flags().IntVar(&position, "to", 0, "Target position, 1-based (1 = top)")
	cmd.Flags().IntVar(&position, "position", 0, "Target position (alias for --to)")
	dest.addFlags(cmd, "Move")

	return guardFilingFlags(cmd)
}

// rejectRepositionWithFiling refuses the reorder flags next to the flags that
// move a list or group to another project.
func rejectRepositionWithFiling(cmd *cobra.Command, reorderFlags string) error {
	for _, name := range []string{"to", "position", "pos"} {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			return output.ErrUsage(fmt.Sprintf("--to-project, --to-todoset and --to-list cannot be combined with %s", reorderFlags))
		}
	}
	return nil
}

// addToListFlags adds --to-project and --to-list, which name the to-do list a
// to-do or a group is moved or copied into.
func addToListFlags(cmd *cobra.Command, toProject, toList *string, verb string) {
	cmd.Flags().StringVar(toProject, "to-project", "", verb+" into this project (ID or name); add --to-list unless it has one to-do list")
	cmd.Flags().StringVar(toList, "to-list", "", verb+" into this to-do list (ID, URL, or a name with --to-project)")
}

// validateToListFlags checks --to-project and --to-list before any request.
func validateToListFlags(toProject, toList string) error {
	if toProject == "" && toList == "" {
		return output.ErrUsage("--to-project or --to-list needs a value")
	}
	if toProject == "" && !isNumericID(toList) && !urlarg.IsURL(toList) {
		return output.ErrUsageHint(
			fmt.Sprintf("To-do list %q is a name, which needs a project to look it up in", toList),
			"Add --to-project <project>, or pass the list's ID or URL",
		)
	}
	return nil
}

// resolveFilingTodolist finds the to-do list that --to-project and --to-list
// name for a to-do or a group: a list ID or URL on its own, a project's only
// list, or one of its lists picked by ID or name. Only lists in the to-dos
// tools on the project's dock count, as in the web's dialogs. A URL is held to
// parseFilingRef's host and account checks, and a list's project must agree
// with --to-project.
func resolveFilingTodolist(cmd *cobra.Command, app *appctx.App, toProject, toList string) (*basecamp.Todolist, error) {
	var urlProjectID string
	if urlarg.IsURL(toList) {
		ref, err := parseFilingRef(app, toList, "to-do list", "todolists", "…/todolists/<id>")
		if err != nil {
			return nil, err
		}
		toList, urlProjectID = strconv.FormatInt(ref.id, 10), ref.project
	}

	var projectID, projectName string
	if toProject != "" {
		var err error
		if projectID, projectName, err = app.Names.ResolveProject(cmd.Context(), toProject); err != nil {
			return nil, err
		}
		if urlProjectID != "" && !sameNumericID(urlProjectID, projectID) {
			return nil, output.ErrUsageHint(
				fmt.Sprintf("The --to-list URL is in project %s, but --to-project points at project %s", urlProjectID, projectID),
				"Drop --to-project, since the list URL names its own project",
			)
		}
	}

	if isNumericID(toList) {
		id, err := strconv.ParseInt(toList, 10, 64)
		if err != nil || id <= 0 {
			return nil, output.ErrUsage("--to-list must be a positive to-do list ID")
		}
		list, err := app.Account().Todolists().Get(cmd.Context(), id)
		if err != nil {
			return nil, notATodolistError(cmd, app, id, err)
		}
		listProject := strconv.FormatInt(todolistProjectID(list), 10)
		if urlProjectID != "" && !sameNumericID(urlProjectID, listProject) {
			return nil, output.ErrUsage(fmt.Sprintf("The --to-list URL names project %s, but to-do list %d is in project %s", urlProjectID, list.ID, listProject))
		}
		if projectID != "" && !sameNumericID(projectID, listProject) {
			return nil, output.ErrUsageHint(
				fmt.Sprintf("To-do list %d is in project %s, not in --to-project %s", list.ID, listProject, projectID),
				"Drop --to-project, since a list ID names its own project",
			)
		}
		return list, requireFilingDestinationList(list)
	}

	lists, todosets, allTodosets, err := projectTodolists(cmd, app, projectID)
	if err != nil {
		return nil, err
	}
	if len(todosets) == 0 {
		return nil, noFilingContainerError(todosetContainer, allTodosets, projectID, projectLabel(cmd, app, projectID, projectName))
	}
	if toList == "" {
		switch len(lists) {
		case 0:
			return nil, &output.Error{
				Code:    output.CodeNotFound,
				Message: fmt.Sprintf("Project %s has no to-do list", projectLabel(cmd, app, projectID, projectName)),
				Hint:    fmt.Sprintf("Add one first with: basecamp todolists create <name> --in %s", projectID),
			}
		case 1:
			return &lists[0], nil
		default:
			return nil, ambiguousTodolistsError(lists)
		}
	}
	var named []basecamp.Todolist
	for _, list := range lists {
		if strings.EqualFold(list.Name, toList) {
			named = append(named, list)
		}
	}
	switch len(named) {
	case 1:
		return &named[0], nil
	case 0:
		return nil, output.ErrUsageHint(
			fmt.Sprintf("To-do list '%s' not found in project %s", toList, projectLabel(cmd, app, projectID, projectName)),
			"Available lists: "+formatTodolists(lists),
		)
	default:
		return nil, ambiguousTodolistsError(named)
	}
}

// notATodolistError explains a --to-list that names a project's To-dos, which
// Basecamp does not file into, rather than reporting it as not found.
func notATodolistError(cmd *cobra.Command, app *appctx.App, id int64, err error) error {
	if basecamp.AsError(err).Code != basecamp.CodeNotFound {
		return convertSDKError(err)
	}
	todoset, todosetErr := app.Account().Todosets().Get(cmd.Context(), id)
	if todosetErr != nil || todoset.Bucket == nil {
		return convertSDKError(err)
	}
	return output.ErrUsageHint(
		fmt.Sprintf("%d is To-dos in project %d, not a to-do list", todoset.ID, todoset.Bucket.ID),
		fmt.Sprintf("--to-list names a to-do list. See the lists there with: basecamp todolists list --in %d --todoset %d", todoset.Bucket.ID, todoset.ID),
	)
}

// requireFilingDestinationList refuses a destination Basecamp refuses: a group
// (to-dos and groups go into a list, never a group) or a list that is not
// active.
func requireFilingDestinationList(list *basecamp.Todolist) error {
	if list.Type != "" && list.Type != "Todolist" {
		return output.ErrUsage(fmt.Sprintf("%d is a %s, not a to-do list", list.ID, list.Type))
	}
	if isTodolistGroup(list) {
		return output.ErrUsageHint(
			fmt.Sprintf("%d (%s) is a group in to-do list %d (%s); Basecamp files into a list, not a group", list.ID, list.Name, list.Parent.ID, list.Parent.Title),
			fmt.Sprintf("Use its list: --to-list %d", list.Parent.ID),
		)
	}
	if list.Status != "" && list.Status != "active" {
		return output.ErrUsage(fmt.Sprintf("To-do list %d (%s) is %s; Basecamp files only into an active list", list.ID, list.Name, list.Status))
	}
	return nil
}

// projectTodolists lists the active to-do lists in the to-dos tools on a
// project's dock.
// projectTodolists returns the lists in a project's enabled To-dos tools,
// along with those tools and every To-dos tool on its dock.
func projectTodolists(cmd *cobra.Command, app *appctx.App, projectID string) (lists []basecamp.Todolist, todosets, all []DockTool, err error) {
	todosets, all, err = getDockTools(cmd.Context(), app, projectID, "todoset")
	if err != nil {
		return nil, nil, nil, err
	}
	for _, todoset := range todosets {
		result, err := app.Account().Todolists().List(cmd.Context(), todoset.ID, nil)
		if err != nil {
			return nil, nil, nil, convertSDKError(err)
		}
		lists = append(lists, result.Todolists...)
	}
	return lists, todosets, all, nil
}

func ambiguousTodolistsError(lists []basecamp.Todolist) error {
	lines := make([]string, 0, len(lists))
	for _, list := range lists {
		lines = append(lines, fmt.Sprintf("  %d  %s", list.ID, list.Name))
	}
	return &output.Error{
		Code:    output.CodeAmbiguous,
		Message: "Multiple to-do lists found",
		Hint:    fmt.Sprintf("Specify one with --to-list <id>:\n%s", strings.Join(lines, "\n")),
	}
}

func formatTodolists(lists []basecamp.Todolist) string {
	if len(lists) == 0 {
		return "none"
	}
	names := make([]string, len(lists))
	for i, list := range lists {
		names[i] = fmt.Sprintf("%d (%s)", list.ID, list.Name)
	}
	return strings.Join(names, ", ")
}

// projectLabel names a project for a message: its name when known or
// readable, otherwise its ID.
func projectLabel(cmd *cobra.Command, app *appctx.App, projectID, projectName string) string {
	if projectName != "" {
		return projectName
	}
	if id, err := strconv.ParseInt(projectID, 10, 64); err == nil {
		if name, err := app.Names.ProjectName(cmd.Context(), id); err == nil && name != "" {
			return name
		}
	}
	return projectID
}

// isTodolistGroup reports whether a to-do list is a group, a list inside a list.
func isTodolistGroup(list *basecamp.Todolist) bool {
	return list.Parent != nil && list.Parent.Type == "Todolist"
}

func todolistProjectID(list *basecamp.Todolist) int64 {
	if list.Bucket == nil {
		return 0
	}
	return list.Bucket.ID
}

// todolistDestination holds the flags that name where a to-do list or a group
// is moved or copied: a list goes into a project's to-dos tool (a todoset), a
// group into a to-do list.
type todolistDestination struct {
	toProject, toTodoset, toList string
}

func (d *todolistDestination) addFlags(cmd *cobra.Command, verb string) {
	cmd.Flags().StringVar(&d.toProject, "to-project", "", verb+" into this project (ID or name): a list into its To-dos, a group into its only to-do list")
	cmd.Flags().StringVar(&d.toTodoset, "to-todoset", "", "For a to-do list: the To-dos tool to "+strings.ToLower(verb)+" it into (ID, URL, or a name with --to-project)")
	cmd.Flags().StringVar(&d.toList, "to-list", "", "For a group: the to-do list to "+strings.ToLower(verb)+" it into, where it stays a group (ID, URL, or a name with --to-project)")
}

func destinationFlagChanged(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("to-project") || cmd.Flags().Changed("to-todoset") || cmd.Flags().Changed("to-list")
}

const todolistFilingNotes = "A top-level to-do list goes into a project's To-dos (--to-project, plus --to-todoset when the project has several); " +
	"a group (a list inside a list) goes into a to-do list (--to-list, or --to-project when the project has one list) and stays a group there. " +
	"--to-todoset for a group, or --to-list for a list, is refused. " +
	"The new list or group comes with its to-dos and has a NEW id (data.id; the original's is data.source_id). " +
	"Basecamp refuses a group as the destination and an archived list."

func newTodolistsCopyCmd(project *string) *cobra.Command {
	var dest todolistDestination
	var includeComments bool

	cmd := &cobra.Command{
		Use:   "copy <id|url>",
		Short: "Copy a to-do list or group to this or another project",
		Long: `Copy a to-do list, with its to-dos, as the web's Copy… does. The original
stays where it is.

A to-do list goes into a project's To-dos; with no destination, into its own:
  basecamp todolists copy 789
  basecamp todolists copy 789 --to-project "Marketing"
  basecamp todolists copy 789 --to-project "Marketing" --to-todoset 1069478882

A group (a list inside a list) goes into a to-do list and stays a group there;
with no destination, into its own list:
  basecamp todolistgroups copy 456 --to-project "Marketing" --to-list "Launch"
  basecamp todolistgroups copy 456 --to-list 1069479999 --include-comments

Basecamp makes the copy in the background; the command waits for it and shows
the new list or group.`,
		Args: cobra.ExactArgs(1),
		Annotations: map[string]string{
			"agent_notes": "Without a destination the copy goes next to the original. " + todolistFilingNotes +
				" Never re-run a copy that failed with in_progress or a may-have-started hint: each run makes another copy; check an in_progress one with basecamp filings show <filing id>.",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return fileTodolist(cmd, basecamp.FilingCopy, args[0], *project, dest, includeComments)
		},
	}

	dest.addFlags(cmd, "Copy")
	cmd.Flags().BoolVar(&includeComments, "include-comments", false, "Copy the comments too")

	return guardFilingFlags(cmd)
}

// fileTodolist moves or copies a to-do list or a group, as the web's Move… and
// Copy… do. Which it is decides where it may go: a list into a todoset, a
// group into a list. With no destination, a copy goes next to the original.
func fileTodolist(cmd *cobra.Command, kind basecamp.FilingType, arg, flagProject string, dest todolistDestination, includeComments bool) error {
	destinationSet := destinationFlagChanged(cmd)
	if destinationSet {
		if err := validateTodolistDestination(dest); err != nil {
			return err
		}
	}

	app := appctx.FromContext(cmd.Context())
	scope := newFollowUpScope(app)
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	ref, err := parseFilingRef(app, arg, "to-do list", "todolists", "…/todolists/<id>")
	if err != nil {
		return err
	}
	source, err := app.Account().Todolists().Get(cmd.Context(), ref.id)
	if err != nil {
		return convertSDKError(err)
	}
	if source.Bucket == nil || source.Parent == nil {
		return output.ErrNotFound("to-do list", strconv.FormatInt(ref.id, 10))
	}
	if source.Type != "" && source.Type != "Todolist" {
		return output.ErrUsage(fmt.Sprintf("%d is a %s, not a to-do list", ref.id, source.Type))
	}
	if err := rejectProjectConflict(cmd, app, "to-do list", flagProject, ref.project, source.Bucket.ID); err != nil {
		return err
	}

	var f recordingFiling
	if isTodolistGroup(source) {
		f, err = groupFiling(cmd, app, kind, source, dest, destinationSet)
	} else {
		f, err = listFiling(cmd, app, kind, source, dest, destinationSet)
	}
	if err != nil {
		return err
	}
	f.includeComments = includeComments
	return runFiling(cmd, app, scope, f)
}

// validateTodolistDestination checks the destination flags before any request.
func validateTodolistDestination(dest todolistDestination) error {
	if dest.toTodoset != "" && dest.toList != "" {
		return output.ErrUsageHint(
			"--to-todoset and --to-list cannot be combined",
			"A to-do list goes into a To-dos tool (--to-todoset); a group goes into a to-do list (--to-list)",
		)
	}
	if dest.toProject == "" && dest.toTodoset == "" && dest.toList == "" {
		return output.ErrUsage("--to-project, --to-todoset or --to-list needs a value")
	}
	for flag, value := range map[string]string{"--to-todoset": dest.toTodoset, "--to-list": dest.toList} {
		if value != "" && dest.toProject == "" && !isNumericID(value) && !urlarg.IsURL(value) {
			return output.ErrUsageHint(
				fmt.Sprintf("%s %q is a name, which needs a project to look it up in", flag, value),
				"Add --to-project <project>, or pass an ID or URL",
			)
		}
	}
	return nil
}

// listFiling files a top-level to-do list into a todoset.
func listFiling(cmd *cobra.Command, app *appctx.App, kind basecamp.FilingType, list *basecamp.Todolist, dest todolistDestination, destinationSet bool) (recordingFiling, error) {
	if dest.toList != "" {
		return recordingFiling{}, output.ErrUsageHint(
			fmt.Sprintf("%d (%s) is a to-do list, which goes into a project's To-dos, not into a list", list.ID, list.Name),
			"Use --to-project <project>, and --to-todoset <id> when it has several To-dos",
		)
	}
	todoset := &basecamp.Todoset{ID: list.Parent.ID, Title: list.Parent.Title, Bucket: list.Bucket}
	if destinationSet {
		var err error
		if todoset, err = resolveFilingTodoset(cmd, app, dest.toProject, dest.toTodoset); err != nil {
			return recordingFiling{}, err
		}
	}
	if kind == basecamp.FilingMove && todoset.ID == list.Parent.ID {
		return recordingFiling{}, output.ErrUsageHint(
			fmt.Sprintf("To-do list %d is already in To-dos %d", list.ID, todoset.ID),
			fmt.Sprintf("Reorder it there with: basecamp todolists move %d --to <position>", list.ID),
		)
	}
	projectID := todosetProjectID(todoset)
	return recordingFiling{
		kind:          kind,
		sourceID:      list.ID,
		noun:          "to-do list",
		destinationID: todoset.ID,
		destination:   fmt.Sprintf("To-dos %d in project %d", todoset.ID, projectID),
		showCmd:       "basecamp todolists show",
		lookFor:       copyLookFor(filingSource{id: list.ID, typ: "Todolist", status: list.Status, title: list.Name}, filingTarget{id: todoset.ID, title: todoset.Title, appURL: todoset.AppURL, projectID: projectID}),
		read:          readFiledTodolist(app, list.ID),
	}, nil
}

// groupFiling files a group into a to-do list, where it stays a group.
func groupFiling(cmd *cobra.Command, app *appctx.App, kind basecamp.FilingType, group *basecamp.Todolist, dest todolistDestination, destinationSet bool) (recordingFiling, error) {
	if dest.toTodoset != "" {
		return recordingFiling{}, output.ErrUsageHint(
			fmt.Sprintf("%d (%s) is a group, which goes into a to-do list, not into To-dos", group.ID, group.Name),
			"Use --to-list <list> (with --to-project <project> for a list name)",
		)
	}
	into := &basecamp.Todolist{ID: group.Parent.ID, Name: group.Parent.Title, Bucket: group.Bucket}
	if destinationSet {
		var err error
		if into, err = resolveFilingTodolist(cmd, app, dest.toProject, dest.toList); err != nil {
			return recordingFiling{}, err
		}
	}
	if kind == basecamp.FilingMove && into.ID == group.Parent.ID {
		return recordingFiling{}, output.ErrUsageHint(
			fmt.Sprintf("Group %d is already in to-do list %d (%s)", group.ID, into.ID, into.Name),
			fmt.Sprintf("Reorder it there with: basecamp todolistgroups move %d --position <position>", group.ID),
		)
	}
	return recordingFiling{
		kind:          kind,
		sourceID:      group.ID,
		noun:          "to-do group",
		destinationID: into.ID,
		destination:   fmt.Sprintf("to-do list %d (%s)", into.ID, into.Name),
		showCmd:       "basecamp todolists show",
		lookFor:       copyLookFor(filingSource{id: group.ID, typ: "Todolist", status: group.Status, title: group.Name}, filingTarget{id: into.ID, title: into.Name, appURL: into.AppURL, projectID: todolistProjectID(into)}),
		read:          readFiledTodolist(app, group.ID),
	}, nil
}

// filedTodolistResult is a to-do list or group that a move or copy made, with
// the id of the one it came from.
type filedTodolistResult struct {
	*basecamp.Todolist
	SourceID int64 `json:"source_id"`
}

func readFiledTodolist(app *appctx.App, sourceID int64) func(context.Context, int64) (filedRecording, error) {
	return func(ctx context.Context, id int64) (filedRecording, error) {
		list, err := app.Account().Todolists().Get(ctx, id)
		if err != nil {
			return filedRecording{}, err
		}
		var in string
		if list.Bucket != nil {
			in = fmt.Sprintf(" --in %d", list.Bucket.ID)
		}
		return filedRecording{
			result: filedTodolistResult{Todolist: list, SourceID: sourceID},
			id:     list.ID,
			bucket: list.Bucket,
			parent: list.Parent,
			breadcrumbs: []output.Breadcrumb{
				{Action: "show", Cmd: fmt.Sprintf("basecamp todolists show %d%s", list.ID, in), Description: "View it"},
				{Action: "todos", Cmd: fmt.Sprintf("basecamp todos list --list %d%s", list.ID, in), Description: "List its to-dos"},
			},
		}, nil
	}
}

// todosetContainer is a project's To-dos tool, where a to-do list is filed.
var todosetContainer = filingContainer{
	dockName: "todoset",
	noun:     "To-dos",
	plural:   "To-dos tools",
	flag:     "--to-todoset",
	urlKind:  "todosets",
	urlShape: "…/todosets/<id>",
	get: func(ctx context.Context, app *appctx.App, id int64) (filingTarget, error) {
		todoset, err := app.Account().Todosets().Get(ctx, id)
		if err != nil {
			return filingTarget{}, err
		}
		target := filingTarget{id: todoset.ID, title: todoset.Title}
		if todoset.Bucket != nil {
			target.projectID, target.projectName = todoset.Bucket.ID, todoset.Bucket.Name
		}
		return target, nil
	},
}

// resolveFilingTodoset finds the To-dos tool that --to-project and
// --to-todoset name for a to-do list.
func resolveFilingTodoset(cmd *cobra.Command, app *appctx.App, toProject, toTodoset string) (*basecamp.Todoset, error) {
	target, err := resolveFilingTarget(cmd, app, todosetContainer, toProject, toTodoset)
	if err != nil {
		return nil, err
	}
	return &basecamp.Todoset{ID: target.id, Title: target.title, AppURL: target.appURL, Bucket: &basecamp.Bucket{ID: target.projectID, Name: target.projectName}}, nil
}

func todosetProjectID(todoset *basecamp.Todoset) int64 {
	if todoset.Bucket == nil {
		return 0
	}
	return todoset.Bucket.ID
}
