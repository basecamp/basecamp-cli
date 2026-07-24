package commands

import (
	"errors"
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
		newTodolistsPositionCmd(&project),
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
						Cmd:         fmt.Sprintf("basecamp todos create <content> --list %s", todolistIDStr),
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

func newTodolistsPositionCmd(project *string) *cobra.Command {
	var position int

	cmd := &cobra.Command{
		Use:     "position <id|url>...",
		Aliases: []string{"move", "reorder"},
		Short:   "Change todolist position",
		Long: `Reorder a todolist within its todoset. Position is 1-based (1 = top).

  basecamp todolists position 789 --to 1
  basecamp todolists position https://3.basecamp.com/1/buckets/2/todolists/789 --to 1

Pass several todolists to set their order, top to bottom. Bulk reordering always
places them at the top of the todoset; every list must live in the same todoset
and be incomplete (completed lists are positioned separately by Basecamp):

  basecamp todolists position 701 702 703 704 705

Positioning is relative and cascades: sibling lists shift to make room, and the
server translates the position for loose to-dos and hidden completed lists.
Confirm with ` + "`basecamp todolists list`" + `.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>...")
			}

			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// ExtractIDs drops empty segments, so a bare "," yields zero IDs.
			ids := extractIDs(args)
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
					msg := fmt.Sprintf("Reordered %d of %d todolists; failed at #%d", applied, len(parsed), parsed[i])
					hint := "Rerun the whole command once the cause is fixed; the todoset is now in an intermediate order."
					var outErr *output.Error
					if errors.As(converted, &outErr) {
						return &output.Error{
							Code:       outErr.Code,
							Message:    fmt.Sprintf("%s: %s", msg, outErr.Message),
							Hint:       hint,
							HTTPStatus: outErr.HTTPStatus,
							Retryable:  outErr.Retryable,
							Cause:      outErr,
						}
					}
					return output.ErrUsageHint(fmt.Sprintf("%s: %s", msg, converted.Error()), hint)
				}
				applied++
			}

			var summary string
			var breadcrumbs []output.Breadcrumb
			if len(parsed) == 1 {
				summary = fmt.Sprintf("Moved todolist #%d to position %d", parsed[0], position)
				// No preflight Get in single mode: resolve project from ambient
				// context (URL > group flag > flags > config) for the breadcrumbs.
				_, urlProjectID := extractWithProject(args[0])
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

	return cmd
}
