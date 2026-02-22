package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// DockTool represents a tool in a project's dock.
type DockTool struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	ID      int64  `json:"id"`
	Enabled bool   `json:"enabled"`
}

// getDockToolID retrieves a dock tool ID from a project, handling the multi-dock case.
//
// When multiple tools of the same type exist in the project:
//   - If explicitID is provided, it is returned as-is
//   - Otherwise, an error is returned listing the available tools
//
// When exactly one tool exists, its ID is returned.
// When no tools of the type exist, a not found error is returned.
func getDockToolID(ctx context.Context, app *appctx.App, projectID, dockName, explicitID, friendlyName string) (string, error) {
	// If explicit ID provided, use it directly
	if explicitID != "" {
		return explicitID, nil
	}

	// Account must already be resolved by calling command
	if app.Config.AccountID == "" {
		return "", output.ErrUsage("Account must be resolved before accessing dock tools")
	}

	// Fetch project to get dock
	path := fmt.Sprintf("/projects/%s.json", projectID)
	resp, err := app.Account().Get(ctx, path)
	if err != nil {
		return "", convertSDKError(err)
	}

	var project struct {
		Dock []DockTool `json:"dock"`
	}
	if err := json.Unmarshal(resp.Data, &project); err != nil {
		return "", fmt.Errorf("failed to parse project: %w", err)
	}

	// Find all matching tools
	var matches []DockTool
	for _, tool := range project.Dock {
		if tool.Name == dockName {
			matches = append(matches, tool)
		}
	}

	// Handle cases
	switch len(matches) {
	case 0:
		return "", output.ErrNotFoundHint(friendlyName, projectID, fmt.Sprintf("Project has no %s", friendlyName))

	case 1:
		return fmt.Sprintf("%d", matches[0].ID), nil

	default:
		// Multiple tools found - require explicit selection
		var toolList []string
		for _, tool := range matches {
			title := tool.Title
			if title == "" {
				title = friendlyName
			}
			toolList = append(toolList, fmt.Sprintf("%s (ID: %d)", title, tool.ID))
		}
		hint := fmt.Sprintf("Specify ID directly. Available:\n  - %s", strings.Join(toolList, "\n  - "))
		return "", &output.Error{
			Code:    output.CodeAmbiguous,
			Message: fmt.Sprintf("Project has %d %ss", len(matches), friendlyName),
			Hint:    hint,
		}
	}
}

// isNumeric checks if a string contains only digits (for ID detection).
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ensureAccount resolves the account ID if not already configured.
// This enables interactive prompts when --account flag and config are both missing.
// After resolution, validates the account ID is numeric and updates the name resolver.
func ensureAccount(cmd *cobra.Command, app *appctx.App) error {
	if app.Config.AccountID != "" {
		// Still need to validate and sync with name resolver
		if err := app.RequireAccount(); err != nil {
			return err
		}
		app.Names.SetAccountID(app.Config.AccountID)
		return nil
	}
	resolved, err := app.Resolve().Account(cmd.Context())
	if err != nil {
		return err
	}
	app.Config.AccountID = resolved.Value

	// Validate the resolved account ID is numeric (required by SDK.ForAccount)
	if err := app.RequireAccount(); err != nil {
		return err
	}

	// Update the name resolver with the new account ID
	app.Names.SetAccountID(resolved.Value)
	return nil
}

// ensureProject resolves the project ID if not already configured.
// This enables interactive prompts when --project flag and config are both missing.
// The account must be resolved first (call ensureAccount before this).
func ensureProject(cmd *cobra.Command, app *appctx.App) error {
	// Check if project is already set via flag or config
	if app.Flags.Project != "" {
		app.Config.ProjectID = app.Flags.Project
		return nil
	}
	if app.Config.ProjectID != "" {
		return nil
	}

	// Try interactive resolution
	resolved, err := app.Resolve().Project(cmd.Context())
	if err != nil {
		return err
	}
	app.Config.ProjectID = resolved.Value
	return nil
}

// getTodosetID retrieves the todoset ID from a project's dock.
func getTodosetID(cmd *cobra.Command, app *appctx.App, projectID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "todoset", "", "todoset")
}

// ensureTodolist resolves the todolist ID if not already configured.
// This enables interactive prompts when --list flag and config are both missing.
// The project must be resolved first (call ensureProject before this).
func ensureTodolist(cmd *cobra.Command, app *appctx.App, projectID string) (string, error) {
	// Check if todolist is already set via flag or config
	if app.Flags.Todolist != "" {
		return app.Flags.Todolist, nil
	}
	if app.Config.TodolistID != "" {
		return app.Config.TodolistID, nil
	}

	// Try interactive resolution
	resolved, err := app.Resolve().Todolist(cmd.Context(), projectID)
	if err != nil {
		return "", err
	}
	return resolved.Value, nil
}

// ensurePersonInProject resolves a person ID interactively from project members.
// This is useful when you want to limit the selection to people who have
// access to a specific project.
func ensurePersonInProject(cmd *cobra.Command, app *appctx.App, projectID string) (string, error) {
	// Try interactive resolution
	resolved, err := app.Resolve().PersonInProject(cmd.Context(), projectID)
	if err != nil {
		return "", err
	}
	return resolved.Value, nil
}

// extractID extracts the primary ID from an argument.
// If the argument is a Basecamp URL (copy/pasted from browser), extracts the recording ID.
// Otherwise, returns the argument as-is (assumed to be an ID).
//
// This allows users to paste URLs directly:
//
//	basecamp todos show https://3.basecamp.com/123/buckets/456/todos/789
//
// Instead of having to manually extract the ID:
//
//	basecamp todos show 789 --in 456
func extractID(arg string) string {
	return urlarg.ExtractID(arg)
}

// extractWithProject extracts both recording ID and project ID from an argument.
// If the argument is a Basecamp URL, extracts both; if the URL contains a project,
// it can be used to auto-populate the --in flag.
// Returns (recordingID, projectID). If projectID is empty, it wasn't in the URL.
func extractWithProject(arg string) (recordingID, projectID string) {
	return urlarg.ExtractWithProject(arg)
}

// extractCommentWithProject extracts a comment ID and project ID from an argument.
// For URLs with a fragment (#__recording_N), returns the comment ID instead of the recording ID.
func extractCommentWithProject(arg string) (id, projectID string) {
	return urlarg.ExtractCommentWithProject(arg)
}

// extractIDs extracts IDs from multiple arguments, handling URLs.
func extractIDs(args []string) []string {
	return urlarg.ExtractIDs(args)
}
