package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/names"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// missingArg shows help in interactive TTY mode, returns a structured
// usage error naming the missing argument in machine/agent mode.
// The hint includes both the usage pattern and a concrete example
// (if cmd.Example is set).
func missingArg(cmd *cobra.Command, arg string) error {
	if isMachineOutput(cmd) {
		hint := "Usage: " + cmd.UseLine()
		if cmd.Example != "" {
			if first, _, ok := strings.Cut(cmd.Example, "\n"); ok {
				hint += "\nExample: " + strings.TrimSpace(first)
			} else {
				hint += "\nExample: " + strings.TrimSpace(cmd.Example)
			}
		}
		return output.ErrUsageHint(arg+" required", hint)
	}
	return cmd.Help()
}

// noChanges shows help in interactive TTY mode, returns a structured
// usage error in machine/agent mode when an update command has no fields.
func noChanges(cmd *cobra.Command) error {
	if isMachineOutput(cmd) {
		hint := "Usage: " + cmd.UseLine()
		if cmd.Example != "" {
			if first, _, ok := strings.Cut(cmd.Example, "\n"); ok {
				hint += "\nExample: " + strings.TrimSpace(first)
			} else {
				hint += "\nExample: " + strings.TrimSpace(cmd.Example)
			}
		}
		return output.ErrUsageHint("No update fields specified", hint)
	}
	return cmd.Help()
}

// isMachineOutput returns true when the command is running in a non-interactive
// context: --agent, --json, --quiet, piped stdout, etc.
func isMachineOutput(cmd *cobra.Command) bool {
	if app := appctx.FromContext(cmd.Context()); app != nil {
		if app.IsMachineOutput() {
			return true
		}
	}
	// Fallback: check output-mode flags directly (app may not be initialized)
	pf := cmd.Root().PersistentFlags()
	for _, flag := range []string{"agent", "json", "quiet", "ids-only", "count"} {
		if v, _ := pf.GetBool(flag); v {
			return true
		}
	}
	// Non-TTY stdout → machine consumer (matches FormatAuto behavior)
	if f, ok := cmd.OutOrStdout().(*os.File); ok {
		fi, err := f.Stat()
		if err == nil && (fi.Mode()&os.ModeCharDevice) == 0 {
			return true
		}
	}
	return false
}

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

	// Find all matching enabled tools
	var matches []DockTool
	for _, tool := range project.Dock {
		if tool.Name == dockName && tool.Enabled {
			matches = append(matches, tool)
		}
	}

	// Handle cases
	switch len(matches) {
	case 0:
		// Check if tool exists but is disabled
		for _, tool := range project.Dock {
			if tool.Name == dockName && !tool.Enabled {
				return "", output.ErrNotFoundHint(friendlyName, projectID,
					fmt.Sprintf("%s is disabled for this project", strings.ToUpper(friendlyName[:1])+friendlyName[1:]))
			}
		}
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
		hint := fmt.Sprintf("Specify the ID directly. Available:\n  - %s", strings.Join(toolList, "\n  - "))
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

// ensureTodoset resolves the todoset ID from a project, with interactive fallback.
// If explicitTodosetID is provided (e.g. from --todoset flag), it is used directly.
// Otherwise, auto-selects when one todoset exists, or prompts when multiple exist.
func ensureTodoset(cmd *cobra.Command, app *appctx.App, projectID, explicitTodosetID string) (string, error) {
	resolved, err := app.Resolve().Todoset(cmd.Context(), projectID, explicitTodosetID)
	if err != nil {
		return "", err
	}
	return resolved.ToolID, nil
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

// resolvePersonIDs splits a comma-separated input string and resolves each
// token (name, email, ID, or "me") to a person ID via the name resolver.
func resolvePersonIDs(ctx context.Context, resolver *names.Resolver, input string) ([]int64, error) {
	var ids []int64
	for token := range strings.SplitSeq(input, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		idStr, _, err := resolver.ResolvePerson(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("resolving %q: %w", token, err)
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid person ID %q for %q: %w", idStr, token, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// applySubscribeFlags interprets --subscribe / --no-subscribe flag values and
// returns the SDK Subscriptions pointer:
//   - Both set → usage error (mutually exclusive)
//   - --no-subscribe → &[]int64{} (empty list, no one else subscribed)
//   - --subscribe "X,Y" → resolve each → &[]int64{id1, id2}
//   - --subscribe "" (explicitly set but empty) → usage error
//   - Neither → nil (omit, server default: everyone)
//
// subscribeChanged should be true when the --subscribe flag was explicitly
// provided on the command line (i.e. cmd.Flags().Changed("subscribe")).
func applySubscribeFlags(ctx context.Context, resolver *names.Resolver, subscribe string, subscribeChanged, noSubscribe bool) (*[]int64, error) {
	if subscribeChanged && noSubscribe {
		return nil, output.ErrUsage("--subscribe and --no-subscribe are mutually exclusive")
	}
	if noSubscribe {
		empty := []int64{}
		return &empty, nil
	}
	if subscribeChanged {
		ids, err := resolvePersonIDs(ctx, resolver, subscribe)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, output.ErrUsage("--subscribe requires at least one person")
		}
		return &ids, nil
	}
	return nil, nil
}
