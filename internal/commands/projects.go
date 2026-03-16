package commands

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/completion"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewProjectsCmd creates the projects command group.
func NewProjectsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "projects",
		Aliases:     []string{"project"},
		Short:       "Manage projects",
		Long:        "Manage Basecamp projects.",
		Annotations: map[string]string{"agent_notes": "Project IDs appear in Basecamp URLs as the buckets segment: /buckets/<project_id>/...\nbasecamp config project sets the default project for the current repo\nCreating a project returns its ID — use it with basecamp config set project_id <id>"},
		Example: `  $ basecamp projects list
  $ basecamp projects list --status archived
  $ basecamp projects show 12345
  $ basecamp projects create "New project"`,
	}

	cmd.AddCommand(
		newProjectsListCmd(),
		newProjectsShowCmd(),
		newProjectsCreateCmd(),
		newProjectsUpdateCmd(),
		newProjectsDeleteCmd(),
	)

	return cmd
}

func newProjectsListCmd() *cobra.Command {
	var status string
	var limit, page int
	var all bool
	var sortField string
	var reverse bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List projects",
		Long:  "List all accessible projects in the account.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectsList(cmd, status, limit, page, all, sortField, reverse)
		},
	}

	cmd.Flags().StringVar(&status, "status", "", "Filter by status (active, archived, trashed)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of projects to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all projects (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")
	cmd.Flags().StringVar(&sortField, "sort", "", "Sort by field (title, created, updated)")
	cmd.Flags().BoolVar(&reverse, "reverse", false, "Reverse sort order")

	return cmd
}

func runProjectsList(cmd *cobra.Command, status string, limit, page int, all bool, sortField string, reverse bool) error {
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
		if err := validateSortField(sortField, []string{"title", "created", "updated"}); err != nil {
			return err
		}
	}

	// Resolve account if not configured (enables interactive prompt)
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	opts := &basecamp.ProjectListOptions{}
	if status != "" {
		opts.Status = basecamp.ProjectStatus(status)
	}

	// Apply pagination options
	if all {
		opts.Limit = 0 // SDK treats 0 as "fetch all"
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}

	result, err := app.Account().Projects().List(cmd.Context(), opts)
	if err != nil {
		return convertSDKError(err)
	}

	projects := result.Projects

	if sortField != "" {
		sortProjects(projects, sortField, reverse)
	} else if page == 0 && limit == 0 {
		// Default: sort alphabetically by name (API returns reverse_chronologically).
		// Only sort when we have the full result set — alphabetizing a partial
		// page would create a misleading view.
		sort.Slice(projects, func(i, j int) bool {
			return strings.ToLower(projects[i].Name) < strings.ToLower(projects[j].Name)
		})
		if reverse {
			slices.Reverse(projects)
		}
	}

	// Opportunistic cache refresh: update completion cache as a side-effect.
	// Only cache when listing all active projects (no filter/pagination), as filtered
	// results wouldn't be suitable for general-purpose completion.
	// Done synchronously to ensure write completes before process exits.
	if status == "" && page == 0 && (limit == 0 || all) {
		updateProjectsCache(projects, app.Config.CacheDir)
	}

	// Build summary with total count if available
	summary := fmt.Sprintf("%d projects", len(projects))
	if result.Meta.TotalCount > 0 && result.Meta.TotalCount != len(projects) {
		summary = fmt.Sprintf("%d of %d projects", len(projects), result.Meta.TotalCount)
	}

	respOpts := []output.ResponseOption{
		output.WithEntity("project"),
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "basecamp projects show <id>",
				Description: "Show project details",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         "basecamp projects create <name>",
				Description: "Create a new project",
			},
		),
	}

	// Add truncation notice if results were truncated (using API's total count)
	if notice := output.TruncationNoticeWithTotal(len(projects), result.Meta.TotalCount); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}

	return app.OK(projects, respOpts...)
}

// updateProjectsCache updates the completion cache with fresh project data.
// Runs synchronously; errors are ignored (best-effort).
func updateProjectsCache(projects []basecamp.Project, cacheDir string) {
	store := completion.NewStore(cacheDir)
	cached := make([]completion.CachedProject, len(projects))
	for i, p := range projects {
		cached[i] = completion.CachedProject{
			ID:         p.ID,
			Name:       p.Name,
			Purpose:    p.Purpose,
			Bookmarked: p.Bookmarked,
			UpdatedAt:  p.UpdatedAt,
		}
	}
	_ = store.UpdateProjects(cached) // Ignore errors - this is best-effort
}

func newProjectsShowCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show project details",
		Long:  "Display detailed information about a project including its dock (the set of enabled tools: message board, to-dos, schedule, etc.).\n\nBy default only enabled tools are shown. Use --all to include disabled tools.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			// Resolve account if not configured (enables interactive prompt)
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			projectID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			project, err := app.Account().Projects().Get(cmd.Context(), projectID)
			if err != nil {
				return convertSDKError(err)
			}

			if !all {
				project.Dock = filterEnabledDock(project.Dock)
			}

			return app.OK(project,
				output.WithEntity("project"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "todos",
						Cmd:         fmt.Sprintf("basecamp todos --project %d", projectID),
						Description: "List todos in this project",
					},
					output.Breadcrumb{
						Action:      "messages",
						Cmd:         fmt.Sprintf("basecamp messages --project %d", projectID),
						Description: "List messages in this project",
					},
				),
			)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Show all dock tools including disabled")

	return cmd
}

// filterEnabledDock returns only the enabled dock items.
func filterEnabledDock(dock []basecamp.DockItem) []basecamp.DockItem {
	var enabled []basecamp.DockItem
	for _, item := range dock {
		if item.Enabled {
			enabled = append(enabled, item)
		}
	}
	return enabled
}

func newProjectsCreateCmd() *cobra.Command {
	var description string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new project",
		Long:  "Create a new Basecamp project.",
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

			// Resolve account if not configured (enables interactive prompt)
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			req := &basecamp.CreateProjectRequest{
				Name:        name,
				Description: description,
			}

			project, err := app.Account().Projects().Create(cmd.Context(), req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(project,
				output.WithEntity("project"),
				output.WithSummary(fmt.Sprintf("Created project: %s", name)),
			)
		},
	}

	cmd.Flags().StringVarP(&description, "description", "d", "", "Project description")

	return cmd
}

func newProjectsUpdateCmd() *cobra.Command {
	var name string
	var description string

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a project",
		Long: `Update an existing project's name or description.

Examples:
  basecamp projects update 12345 --name "New Name"
  basecamp projects update 12345 --description "New description"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" && description == "" {
				return noChanges(cmd)
			}

			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			// Resolve account if not configured (enables interactive prompt)
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			projectID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			// For update, we need to provide name (required by SDK)
			// If only description is provided, we need to fetch current name first
			updateName := name
			if updateName == "" {
				// Fetch current project to get the name
				current, err := app.Account().Projects().Get(cmd.Context(), projectID)
				if err != nil {
					return convertSDKError(err)
				}
				updateName = current.Name
			}

			req := &basecamp.UpdateProjectRequest{
				Name:        updateName,
				Description: description,
			}

			project, err := app.Account().Projects().Update(cmd.Context(), projectID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(project,
				output.WithEntity("project"),
				output.WithSummary("Project updated"),
			)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "New name")
	cmd.Flags().StringVarP(&description, "description", "d", "", "New description")

	return cmd
}

func newProjectsDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"trash"},
		Short:   "Delete (trash) a project",
		Long:    "Move a project to the trash. Can be restored later.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			// Resolve account if not configured (enables interactive prompt)
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			projectID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			if err := app.Account().Projects().Trash(cmd.Context(), projectID); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{
				"id":     projectID,
				"status": "trashed",
			}, output.WithSummary("Project moved to trash"))
		},
	}
}

// convertSDKError converts SDK errors to output errors for consistent CLI error handling.
func convertSDKError(err error) error {
	if err == nil {
		return nil
	}

	// Handle resilience sentinel errors (use errors.Is for wrapped errors)
	if errors.Is(err, basecamp.ErrRateLimited) {
		return &output.Error{
			Code:      basecamp.CodeRateLimit,
			Message:   "Rate limit exceeded",
			Hint:      "Too many requests. Please wait before trying again.",
			Retryable: true,
		}
	}
	if errors.Is(err, basecamp.ErrCircuitOpen) {
		return &output.Error{
			Code:      basecamp.CodeAPI,
			Message:   "Service temporarily unavailable",
			Hint:      "The circuit breaker is open due to recent failures. Please wait before trying again.",
			Retryable: true,
		}
	}
	if errors.Is(err, basecamp.ErrBulkheadFull) {
		return &output.Error{
			Code:      basecamp.CodeRateLimit,
			Message:   "Too many concurrent requests",
			Hint:      "Maximum concurrent operations reached. Please wait for other operations to complete.",
			Retryable: true,
		}
	}

	// Handle structured SDK errors
	var sdkErr *basecamp.Error
	if errors.As(err, &sdkErr) {
		return &output.Error{
			Code:       sdkErr.Code,
			Message:    sdkErr.Message,
			Hint:       sdkErr.Hint,
			HTTPStatus: sdkErr.HTTPStatus,
			Retryable:  sdkErr.Retryable,
		}
	}
	return err
}
