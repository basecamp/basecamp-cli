package commands

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewTodosetsCmd creates the todosets command for managing todoset containers.
func NewTodosetsCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "todosets",
		Short: "Manage todoset containers",
		Long: `Manage todoset containers for a project.

A todoset is the container that holds all todolists in a project.
Most projects have one todoset, but some may have multiple.`,
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	cmd.AddCommand(
		newTodosetListCmd(&project),
		newTodosetShowCmd(&project),
	)

	return cmd
}

func newTodosetListCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List todosets in a project",
		Long:  "List all todoset containers in a project, including enabled and disabled ones.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTodosetList(cmd, *project)
		},
	}
}

// TodosetEntry represents a todoset in the list output.
type TodosetEntry struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Title   string `json:"title"`
	Enabled bool   `json:"enabled"`
}

func runTodosetList(cmd *cobra.Command, project string) error {
	app := appctx.FromContext(cmd.Context())
	if app == nil {
		return fmt.Errorf("app not initialized")
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

	// Fetch project to get dock
	path := fmt.Sprintf("/projects/%s.json", resolvedProjectID)
	resp, err := app.Account().Get(cmd.Context(), path)
	if err != nil {
		return convertSDKError(err)
	}

	var projectData struct {
		Dock []struct {
			Name    string `json:"name"`
			Title   string `json:"title"`
			ID      int64  `json:"id"`
			Enabled bool   `json:"enabled"`
		} `json:"dock"`
	}
	if err := json.Unmarshal(resp.Data, &projectData); err != nil {
		return fmt.Errorf("failed to parse project: %w", err)
	}

	// Filter dock for todosets (both enabled and disabled)
	var todosets []TodosetEntry
	for _, tool := range projectData.Dock {
		if tool.Name == "todoset" {
			title := tool.Title
			if title == "" {
				title = "To-dos"
			}
			todosets = append(todosets, TodosetEntry{
				ID:      tool.ID,
				Name:    tool.Name,
				Title:   title,
				Enabled: tool.Enabled,
			})
		}
	}

	summary := fmt.Sprintf("%d todoset(s)", len(todosets))

	return app.OK(todosets,
		output.WithEntity("todoset"),
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp todosets show <id> --in %s", resolvedProjectID),
				Description: "Show todoset details",
			},
			output.Breadcrumb{
				Action:      "todolists",
				Cmd:         fmt.Sprintf("basecamp todolists --in %s", resolvedProjectID),
				Description: "List todolists",
			},
		),
	)
}

func newTodosetShowCmd(project *string) *cobra.Command {
	var todosetID string

	cmd := &cobra.Command{
		Use:   "show [id]",
		Short: "Show todoset details",
		Long:  "Display detailed information about a todoset.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := todosetID
			if len(args) > 0 {
				id = args[0]
			}
			return runTodosetShow(cmd, *project, id)
		},
	}

	cmd.Flags().StringVarP(&todosetID, "todoset", "t", "", "Todoset ID (auto-detected from project)")

	return cmd
}

func runTodosetShow(cmd *cobra.Command, project, todosetID string) error {
	app := appctx.FromContext(cmd.Context())
	if app == nil {
		return fmt.Errorf("app not initialized")
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

	// Get todoset ID - use provided ID or resolve from project dock
	resolvedTodosetID, err := ensureTodoset(cmd, app, resolvedProjectID, todosetID)
	if err != nil {
		return err
	}

	// Parse todoset ID as int64
	tsID, err := strconv.ParseInt(resolvedTodosetID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid todoset ID")
	}

	// Use SDK to get todoset
	todoset, err := app.Account().Todosets().Get(cmd.Context(), tsID)
	if err != nil {
		return convertSDKError(err)
	}

	completedRatio := todoset.CompletedRatio
	if completedRatio == "" {
		completedRatio = "0.0"
	}

	return app.OK(todoset,
		output.WithSummary(fmt.Sprintf("%d todolists (%s%% complete)", todoset.TodolistsCount, completedRatio)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "todolists",
				Cmd:         fmt.Sprintf("basecamp todolists --in %s", resolvedProjectID),
				Description: "List all todolists",
			},
			output.Breadcrumb{
				Action:      "project",
				Cmd:         fmt.Sprintf("basecamp projects show %s", resolvedProjectID),
				Description: "View project details",
			},
		),
	)
}
