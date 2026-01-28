package commands

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/completion"
	"github.com/basecamp/bcq/internal/output"
)

// NewProjectsCmd creates the projects command group.
func NewProjectsCmd() *cobra.Command {
	var status string

	cmd := &cobra.Command{
		Use:     "projects",
		Aliases: []string{"project"},
		Short:   "Manage projects",
		Long:    "List, show, create, and manage Basecamp projects.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default to list when called without subcommand
			return runProjectsList(cmd, status)
		},
	}

	// Allow --status flag on root command for default list behavior
	cmd.Flags().StringVar(&status, "status", "", "Filter by status (active, archived, trashed)")

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

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List projects",
		Long:  "List all accessible projects in the account.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectsList(cmd, status)
		},
	}

	cmd.Flags().StringVar(&status, "status", "", "Filter by status (active, archived, trashed)")

	return cmd
}

func runProjectsList(cmd *cobra.Command, status string) error {
	app := appctx.FromContext(cmd.Context())
	if app == nil {
		return fmt.Errorf("app not initialized")
	}

	opts := &basecamp.ProjectListOptions{}
	if status != "" {
		opts.Status = basecamp.ProjectStatus(status)
	}

	projects, err := app.SDK.Projects().List(cmd.Context(), opts)
	if err != nil {
		return convertSDKError(err)
	}

	// Opportunistic cache refresh: update completion cache as a side-effect.
	// Only cache when listing all active projects (no filter), as filtered
	// results wouldn't be suitable for general-purpose completion.
	// Done synchronously to ensure write completes before process exits.
	if status == "" {
		updateProjectsCache(projects, app.Config.CacheDir)
	}

	return app.OK(projects,
		output.WithSummary(fmt.Sprintf("%d projects", len(projects))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "bcq projects show <id>",
				Description: "Show project details",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         "bcq projects create --name <name>",
				Description: "Create a new project",
			},
		),
	)
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
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show project details",
		Long:  "Display detailed information about a project including dock items.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			projectID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			project, err := app.SDK.Projects().Get(cmd.Context(), projectID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(project,
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "todos",
						Cmd:         fmt.Sprintf("bcq todos --project %d", projectID),
						Description: "List todos in this project",
					},
					output.Breadcrumb{
						Action:      "messages",
						Cmd:         fmt.Sprintf("bcq messages --project %d", projectID),
						Description: "List messages in this project",
					},
				),
			)
		},
	}
}

func newProjectsCreateCmd() *cobra.Command {
	var name string
	var description string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new project",
		Long:  "Create a new Basecamp project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			if name == "" {
				return output.ErrUsage("--name is required")
			}

			req := &basecamp.CreateProjectRequest{
				Name:        name,
				Description: description,
			}

			project, err := app.SDK.Projects().Create(cmd.Context(), req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(project,
				output.WithSummary(fmt.Sprintf("Created project: %s", name)),
			)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Project name (required)")
	cmd.Flags().StringVarP(&description, "description", "d", "", "Project description")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func newProjectsUpdateCmd() *cobra.Command {
	var name string
	var description string

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a project",
		Long:  "Update an existing project's name or description.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}

			projectID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			if name == "" && description == "" {
				return output.ErrUsage("At least one of --name or --description is required")
			}

			// For update, we need to provide name (required by SDK)
			// If only description is provided, we need to fetch current name first
			updateName := name
			if updateName == "" {
				// Fetch current project to get the name
				current, err := app.SDK.Projects().Get(cmd.Context(), projectID)
				if err != nil {
					return convertSDKError(err)
				}
				updateName = current.Name
			}

			req := &basecamp.UpdateProjectRequest{
				Name:        updateName,
				Description: description,
			}

			project, err := app.SDK.Projects().Update(cmd.Context(), projectID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(project,
				output.WithSummary("Project updated"),
			)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "New project name")
	cmd.Flags().StringVarP(&description, "description", "d", "", "New project description")

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

			projectID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			if err := app.SDK.Projects().Trash(cmd.Context(), projectID); err != nil {
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
