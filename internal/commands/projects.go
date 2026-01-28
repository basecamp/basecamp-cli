package commands

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
	"github.com/basecamp/bcq/internal/pickers"
	basecamp "github.com/basecamp/go-basecamp"
	"github.com/spf13/cobra"
)

// NewProjectsCmd creates the projects command and its subcommands.
func NewProjectsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "projects",
		Aliases: []string{"project", "proj", "p"},
		Short:   "Manage projects",
		Long:    "List, show, create, update, or archive projects.",
	}

	cmd.AddCommand(newProjectsListCmd())
	cmd.AddCommand(newProjectsShowCmd())
	cmd.AddCommand(newProjectsCreateCmd())
	cmd.AddCommand(newProjectsUpdateCmd())
	cmd.AddCommand(newProjectsArchiveCmd())
	cmd.AddCommand(newProjectsUnarchiveCmd())
	cmd.AddCommand(newProjectsTrashCmd())

	return cmd
}

func newProjectsListCmd() *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all projects",
		Long: `List all projects accessible to you.

By default, shows active projects. Use --status to filter:
  --status=active   Active projects (default)
  --status=archived Archived projects
  --status=trashed  Trashed projects`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			app := appctx.FromContext(ctx)

			opts := basecamp.ProjectListOptions{}
			if status != "" {
				opts.Status = status
			}

			projects, err := app.Client.Projects.List(ctx, &opts)
			if err != nil {
				return convertSDKError(err)
			}

			return output.Render(ctx, projects)
		},
	}

	cmd.Flags().StringVar(&status, "status", "", "Filter by status (active, archived, trashed)")

	return cmd
}

func newProjectsShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show [project-id]",
		Short: "Show project details",
		Long: `Show details for a specific project.

If no project ID is provided, you will be prompted to select one.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			app := appctx.FromContext(ctx)

			var projectID int64
			var err error

			if len(args) > 0 {
				projectID, err = strconv.ParseInt(args[0], 10, 64)
				if err != nil {
					return &output.Error{
						Code:    basecamp.CodeInvalidRequest,
						Message: "Invalid project ID",
						Hint:    fmt.Sprintf("'%s' is not a valid project ID. Use 'bcq projects list' to see available projects.", args[0]),
					}
				}
			} else {
				projectID, err = pickers.SelectProject(ctx, app.Client)
				if err != nil {
					return err
				}
			}

			project, err := app.Client.Projects.Get(ctx, projectID)
			if err != nil {
				return convertSDKError(err)
			}

			return output.Render(ctx, project)
		},
	}

	return cmd
}

func newProjectsCreateCmd() *cobra.Command {
	var name, description string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new project",
		Long: `Create a new project.

The --name flag is required. Description is optional.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			app := appctx.FromContext(ctx)

			if name == "" {
				return &output.Error{
					Code:    basecamp.CodeInvalidRequest,
					Message: "Project name is required",
					Hint:    "Use --name to specify the project name",
				}
			}

			req := &basecamp.ProjectCreateRequest{
				Name:        name,
				Description: description,
			}

			project, err := app.Client.Projects.Create(ctx, req)
			if err != nil {
				return convertSDKError(err)
			}

			return output.Render(ctx, project, output.WithSummary("Project created"))
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Project name (required)")
	cmd.Flags().StringVar(&description, "description", "", "Project description")

	return cmd
}

func newProjectsUpdateCmd() *cobra.Command {
	var name, description string

	cmd := &cobra.Command{
		Use:   "update [project-id]",
		Short: "Update an existing project",
		Long: `Update a project's name or description.

If no project ID is provided, you will be prompted to select one.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			app := appctx.FromContext(ctx)

			var projectID int64
			var err error

			if len(args) > 0 {
				projectID, err = strconv.ParseInt(args[0], 10, 64)
				if err != nil {
					return &output.Error{
						Code:    basecamp.CodeInvalidRequest,
						Message: "Invalid project ID",
						Hint:    fmt.Sprintf("'%s' is not a valid project ID. Use 'bcq projects list' to see available projects.", args[0]),
					}
				}
			} else {
				projectID, err = pickers.SelectProject(ctx, app.Client)
				if err != nil {
					return err
				}
			}

			if name == "" && description == "" {
				return &output.Error{
					Code:    basecamp.CodeInvalidRequest,
					Message: "Nothing to update",
					Hint:    "Use --name and/or --description to specify what to update",
				}
			}

			// Get current project to preserve unchanged fields
			current, err := app.Client.Projects.Get(ctx, projectID)
			if err != nil {
				return convertSDKError(err)
			}

			req := &basecamp.ProjectUpdateRequest{}
			if name != "" {
				req.Name = name
			} else {
				req.Name = current.Name
			}
			if description != "" {
				req.Description = description
			} else if current.Description != nil {
				req.Description = *current.Description
			}

			project, err := app.Client.Projects.Update(ctx, projectID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return output.Render(ctx, project, output.WithSummary("Project updated"))
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "New project name")
	cmd.Flags().StringVar(&description, "description", "", "New project description")

	return cmd
}

func newProjectsArchiveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "archive [project-id]",
		Short: "Archive a project",
		Long: `Archive a project.

If no project ID is provided, you will be prompted to select one.
Archived projects can be unarchived with 'bcq projects unarchive'.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			app := appctx.FromContext(ctx)

			var projectID int64
			var err error

			if len(args) > 0 {
				projectID, err = strconv.ParseInt(args[0], 10, 64)
				if err != nil {
					return &output.Error{
						Code:    basecamp.CodeInvalidRequest,
						Message: "Invalid project ID",
						Hint:    fmt.Sprintf("'%s' is not a valid project ID. Use 'bcq projects list' to see available projects.", args[0]),
					}
				}
			} else {
				projectID, err = pickers.SelectProject(ctx, app.Client)
				if err != nil {
					return err
				}
			}

			project, err := app.Client.Projects.Archive(ctx, projectID)
			if err != nil {
				return convertSDKError(err)
			}

			return output.Render(ctx, project, output.WithSummary("Project archived"))
		},
	}

	return cmd
}

func newProjectsUnarchiveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unarchive [project-id]",
		Short: "Unarchive a project",
		Long: `Unarchive a previously archived project.

If no project ID is provided, you will be prompted to select one.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			app := appctx.FromContext(ctx)

			var projectID int64
			var err error

			if len(args) > 0 {
				projectID, err = strconv.ParseInt(args[0], 10, 64)
				if err != nil {
					return &output.Error{
						Code:    basecamp.CodeInvalidRequest,
						Message: "Invalid project ID",
						Hint:    fmt.Sprintf("'%s' is not a valid project ID. Use 'bcq projects list --status=archived' to see archived projects.", args[0]),
					}
				}
			} else {
				projectID, err = pickers.SelectProject(ctx, app.Client, pickers.WithStatus("archived"))
				if err != nil {
					return err
				}
			}

			project, err := app.Client.Projects.Unarchive(ctx, projectID)
			if err != nil {
				return convertSDKError(err)
			}

			return output.Render(ctx, project, output.WithSummary("Project unarchived"))
		},
	}

	return cmd
}

func newProjectsTrashCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trash [project-id]",
		Short: "Move a project to trash",
		Long: `Move a project to trash.

If no project ID is provided, you will be prompted to select one.
Trashed projects are permanently deleted after 30 days.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			app := appctx.FromContext(ctx)

			var projectID int64
			var err error

			if len(args) > 0 {
				projectID, err = strconv.ParseInt(args[0], 10, 64)
				if err != nil {
					return &output.Error{
						Code:    basecamp.CodeInvalidRequest,
						Message: "Invalid project ID",
						Hint:    fmt.Sprintf("'%s' is not a valid project ID. Use 'bcq projects list' to see available projects.", args[0]),
					}
				}
			} else {
				projectID, err = pickers.SelectProject(ctx, app.Client)
				if err != nil {
					return err
				}
			}

			project, err := app.Client.Projects.Trash(ctx, projectID)
			if err != nil {
				return convertSDKError(err)
			}

			return output.Render(ctx, project, output.WithSummary("Project moved to trash"))
		},
	}

	return cmd
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

// projectNameOrID attempts to parse a string as either a project ID or returns it as a name.
func projectNameOrID(s string) (int64, string) {
	if id, err := strconv.ParseInt(s, 10, 64); err == nil {
		return id, ""
	}
	return 0, strings.TrimSpace(s)
}
