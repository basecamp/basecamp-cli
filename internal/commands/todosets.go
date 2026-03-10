package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewTodosetsCmd creates the todosets command for viewing todoset containers.
func NewTodosetsCmd() *cobra.Command {
	var project string
	var todosetID string

	cmd := &cobra.Command{
		Use:   "todosets",
		Short: "View todoset container",
		Long: `View todoset container for a project.

A todoset is the container that holds all todolists in a project.
Most projects have one todoset, but some may have multiple. Use --todoset
to select a specific one.`,
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVarP(&todosetID, "todoset", "t", "", "Todoset ID (auto-detected from project)")

	cmd.AddCommand(newTodosetShowCmd(&project, &todosetID))

	return cmd
}

func newTodosetShowCmd(project, todosetID *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show [id]",
		Short: "Show todoset details",
		Long:  "Display detailed information about a todoset.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := *todosetID
			if len(args) > 0 {
				id = args[0]
			}
			return runTodosetShow(cmd, *project, id)
		},
	}
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
