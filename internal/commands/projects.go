package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/completion"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
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
		newProjectsWallpaperCmd(),
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

			description, err := resolveContentValue(cmd, description, -1, "--description")
			if err != nil {
				return err
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

	cmd.Flags().StringVarP(&description, "description", "d", "", "Project description; use - to read from stdin")

	allowDash(cmd, "flag:description")

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

			projectID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			// Syntactic checks first, then "-", then account and network.
			description, err := resolveContentValue(cmd, description, -1, "--description")
			if err != nil {
				return err
			}

			// Resolve account if not configured (enables interactive prompt)
			if err := ensureAccount(cmd, app); err != nil {
				return err
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

			respOpts := []output.ResponseOption{
				output.WithEntity("project"),
				output.WithSummary("Project updated"),
			}
			freshProject, err := app.Account().Projects().Get(cmd.Context(), projectID)
			if err != nil {
				respOpts = append(respOpts, output.WithDiagnostic(fmt.Sprintf("Project updated, but fetching the latest project state failed: %v", err)))
			} else {
				project = freshProject
			}

			return app.OK(project, respOpts...)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "New name")
	cmd.Flags().StringVarP(&description, "description", "d", "", "New description; use - to read from stdin")

	allowDash(cmd, "flag:description")

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

// wallpaperFormats match the server's shared image validation, not the narrower web picker.
var wallpaperFormats = []struct{ mime, name string }{
	{"image/png", "PNG"},
	{"image/jpeg", "JPEG"},
	{"image/gif", "GIF"},
	{"image/webp", "WebP"},
	{"image/avif", "AVIF"},
	{"image/heif", "HEIF"},
	{"image/heic", "HEIC"},
}

const maxWallpaperMB = 10

var wallpaperModes = []string{"fill", "tile"}

func wallpaperAgentNotes() map[string]string {
	return map[string]string{"agent_notes": "People and personal assistants follow project-edit permissions; shared agents are refused (403)\n.data.wallpaper is {url, mode}, absent when there is none; url requires authentication and redirects to the image"}
}

func newProjectsWallpaperCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "wallpaper",
		Short:       "Manage a project's wallpaper",
		Annotations: wallpaperAgentNotes(),
		Long: `Set, lay out, or remove a project's wallpaper.

The project comes from --project (or --in), then the configured default.
People and personal assistants with project-edit permissions can change its wallpaper.
Shared agents are refused.`,
		Example: `  $ basecamp projects wallpaper set skyline.jpg --mode fill --project 12345
  $ basecamp projects wallpaper mode tile --project 12345
  $ basecamp projects wallpaper remove --project 12345`,
	}

	cmd.AddCommand(
		newProjectsWallpaperSetCmd(),
		newProjectsWallpaperModeCmd(),
		newProjectsWallpaperRemoveCmd(),
	)

	return cmd
}

func newProjectsWallpaperSetCmd() *cobra.Command {
	var mode string

	cmd := &cobra.Command{
		Use:         "set <file>",
		Annotations: wallpaperAgentNotes(),
		Short:       "Upload or replace a project's wallpaper",
		Long: fmt.Sprintf(`Upload or replace the project's wallpaper.

Accepts %s files up to %d MB.
--mode lays the image out (%s); without it the project keeps
its current mode.`, wallpaperFormatNames(), maxWallpaperMB, strings.Join(wallpaperModes, " or ")),
		Example: "  $ basecamp projects wallpaper set skyline.jpg --mode fill --project 12345",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<file>")
			}
			if mode != "" {
				if err := validateWallpaperMode(mode); err != nil {
					return err
				}
			}
			path, contentType, err := wallpaperFile(args[0])
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			projectID, err := wallpaperProjectID(cmd, app)
			if err != nil {
				return err
			}

			f, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			defer f.Close()

			filename := filepath.Base(path)
			opts := &basecamp.UpdateProjectWallpaperOptions{Mode: mode}
			if err := app.Account().Projects().UpdateWallpaper(cmd.Context(), projectID, f, filename, contentType, opts); err != nil {
				return convertSDKError(err)
			}

			summary := "Wallpaper set: " + filename
			if mode != "" {
				summary += " (" + mode + ")"
			}
			return respondWithWallpaperProject(cmd, app, projectID, map[string]any{"id": projectID}, summary,
				output.Breadcrumb{
					Action:      "mode",
					Cmd:         fmt.Sprintf("basecamp projects wallpaper mode <fill|tile> --project %d", projectID),
					Description: "Change how the wallpaper is laid out",
				},
				output.Breadcrumb{
					Action:      "remove",
					Cmd:         fmt.Sprintf("basecamp projects wallpaper remove --project %d", projectID),
					Description: "Remove the wallpaper",
				},
			)
		},
	}

	cmd.Flags().StringVar(&mode, "mode", "", "Lay the image out: "+strings.Join(wallpaperModes, " or ")+" (default: keep the current mode)")
	_ = cmd.RegisterFlagCompletionFunc("mode", cobra.FixedCompletions(wallpaperModes, cobra.ShellCompDirectiveNoFileComp))

	return cmd
}

func newProjectsWallpaperModeCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "mode <fill|tile>",
		Annotations: wallpaperAgentNotes(),
		Short:       "Change how a project's wallpaper is laid out",
		Long: `Change how the project's wallpaper is laid out. fill scales one copy to
cover the page; tile repeats the image at its own size.

A project with no wallpaper keeps the mode for the next one uploaded.`,
		Example:   "  $ basecamp projects wallpaper mode tile --project 12345",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: wallpaperModes,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<fill|tile>")
			}
			mode := args[0]
			if err := validateWallpaperMode(mode); err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			projectID, err := wallpaperProjectID(cmd, app)
			if err != nil {
				return err
			}

			// The SDK's update requires a name, so send the current one back.
			current, err := app.Account().Projects().Get(cmd.Context(), projectID)
			if err != nil {
				return convertSDKError(err)
			}
			req := &basecamp.UpdateProjectRequest{Name: current.Name, WallpaperMode: mode}
			project, err := app.Account().Projects().Update(cmd.Context(), projectID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return respondWithWallpaperProject(cmd, app, projectID, project, "Wallpaper mode set to "+mode,
				output.Breadcrumb{
					Action:      "set",
					Cmd:         fmt.Sprintf("basecamp projects wallpaper set <file> --project %d", projectID),
					Description: "Upload a wallpaper",
				},
			)
		},
	}
}

func newProjectsWallpaperRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "remove",
		Annotations: wallpaperAgentNotes(),
		Short:       "Remove a project's wallpaper",
		Long:        "Remove the project's wallpaper. Removing it from a project that has none succeeds.",
		Example:     "  $ basecamp projects wallpaper remove --project 12345",
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			projectID, err := wallpaperProjectID(cmd, app)
			if err != nil {
				return err
			}

			if err := app.Account().Projects().RemoveWallpaper(cmd.Context(), projectID); err != nil {
				return convertSDKError(err)
			}

			return respondWithWallpaperProject(cmd, app, projectID, map[string]any{"id": projectID}, "Wallpaper removed",
				output.Breadcrumb{
					Action:      "set",
					Cmd:         fmt.Sprintf("basecamp projects wallpaper set <file> --project %d", projectID),
					Description: "Upload a new wallpaper",
				},
			)
		},
	}
}

// wallpaperFile checks the size and MIME hint before uploading. The server's
// shared image validations remain authoritative.
func wallpaperFile(arg string) (path, contentType string, err error) {
	path = richtext.NormalizeDragPath(arg)
	if err := richtext.ValidateFile(path); err != nil {
		return "", "", fmt.Errorf("%s: %w", arg, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", arg, err)
	}
	name := filepath.Base(path)
	if info.Size() > maxWallpaperMB<<20 {
		return "", "", output.ErrUsage(fmt.Sprintf("%s exceeds maximum wallpaper size of %d MB", name, maxWallpaperMB))
	}
	contentType = richtext.DetectMIME(path)
	for _, f := range wallpaperFormats {
		if f.mime == contentType {
			return path, contentType, nil
		}
	}
	return "", "", output.ErrUsage(fmt.Sprintf("%s: unsupported image type %s (use %s)", name, contentType, wallpaperFormatNames()))
}

// wallpaperFormatNames lists the accepted formats for help and validation errors.
func wallpaperFormatNames() string {
	names := make([]string, len(wallpaperFormats))
	for i, f := range wallpaperFormats {
		names[i] = f.name
	}
	return strings.Join(names[:len(names)-1], ", ") + ", or " + names[len(names)-1]
}

func validateWallpaperMode(mode string) error {
	if slices.Contains(wallpaperModes, mode) {
		return nil
	}
	return output.ErrUsageHint(fmt.Sprintf("unknown wallpaper mode %q", mode), "Use "+strings.Join(wallpaperModes, " or "))
}

func wallpaperProjectID(cmd *cobra.Command, app *appctx.App) (int64, error) {
	if err := ensureAccount(cmd, app); err != nil {
		return 0, err
	}
	resolved, err := resolveProjectID(cmd, app, "")
	if err != nil {
		return 0, err
	}
	projectID, err := strconv.ParseInt(resolved, 10, 64)
	if err != nil {
		return 0, output.ErrUsage("Invalid project ID")
	}
	return projectID, nil
}

// respondWithWallpaperProject answers with the project as it now stands, so
// the output carries its wallpaper. The change has already happened, so a
// failed read falls back to what the caller has, with a diagnostic, rather
// than failing the command.
func respondWithWallpaperProject(cmd *cobra.Command, app *appctx.App, projectID int64, fallback any, summary string, crumbs ...output.Breadcrumb) error {
	opts := []output.ResponseOption{
		output.WithEntity("project"),
		output.WithSummary(summary),
		output.WithBreadcrumbs(append([]output.Breadcrumb{{
			Action:      "show",
			Cmd:         fmt.Sprintf("basecamp projects show %d", projectID),
			Description: "View the project",
		}}, crumbs...)...),
	}
	data := fallback
	project, err := app.Account().Projects().Get(cmd.Context(), projectID)
	if err != nil {
		opts = append(opts, output.WithDiagnostic(fmt.Sprintf("%s, but fetching the latest project state failed: %v", summary, err)))
	} else {
		data = project
	}
	return app.OK(data, opts...)
}

// convertSDKError converts SDK errors to output errors for consistent CLI error handling.
func convertSDKError(err error) error {
	if err == nil {
		return nil
	}

	// A gate that queued and gave up says which limit, how long, and what to do
	if gateErr := output.AsGateError(err); gateErr != nil {
		return gateErr
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
		return output.WithAuthHint(&output.Error{
			Code:       sdkErr.Code,
			Message:    sdkErr.Message,
			Hint:       sdkErr.Hint,
			HTTPStatus: sdkErr.HTTPStatus,
			Retryable:  sdkErr.Retryable,
			Cause:      sdkErr,
		})
	}
	return err
}
