package resolve

import (
	"context"
	"fmt"
	"strconv"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// Todolist resolves the todolist ID using the following precedence:
// 1. CLI flag (--todolist)
// 2. Config file (todolist_id)
// 3. Auto-select if exactly one todolist exists (non-interactive fallback)
// 4. Interactive prompt (if terminal is interactive)
// 5. Error (if no todolist can be determined)
//
// The project must be resolved before calling this method.
// Returns the resolved todolist ID and the source it came from.
func (r *Resolver) Todolist(ctx context.Context, projectID, explicitTodosetID string) (*ResolvedValue, error) {
	// 1. Check CLI flag
	if r.flags.Todolist != "" {
		return &ResolvedValue{
			Value:  r.flags.Todolist,
			Source: SourceFlag,
		}, nil
	}

	// 2. Check config
	if r.config.TodolistID != "" {
		return &ResolvedValue{
			Value:  r.config.TodolistID,
			Source: SourceConfig,
		}, nil
	}

	// Ensure project is configured before fetching
	if projectID == "" {
		return nil, output.ErrUsage("Project must be resolved before fetching todolists")
	}

	// Fetch todolists to check count (needed for both interactive and non-interactive paths)
	// Pass through explicit todoset ID if provided (e.g. from --todoset flag)
	todolists, err := r.fetchTodolists(ctx, projectID, explicitTodosetID)
	if err != nil {
		return nil, err
	}

	// 3. Auto-select if exactly one todolist exists
	if len(todolists) == 1 {
		return &ResolvedValue{
			Value:  fmt.Sprintf("%d", todolists[0].ID),
			Source: SourceDefault,
		}, nil
	}

	// No todolists found — create a default "Tasks" list
	if len(todolists) == 0 {
		created, err := r.createDefaultTodolist(ctx, projectID, explicitTodosetID)
		if err != nil {
			return nil, err
		}
		return &ResolvedValue{
			Value:  fmt.Sprintf("%d", created.ID),
			Label:  created.Name,
			Source: SourceDefault,
		}, nil
	}

	// 4. Multiple todolists - need interactive prompt
	if !r.IsInteractive() {
		return nil, output.ErrUsageHint("No todolist specified", "Use --list (or --todolist) or set todolist_id in .basecamp/config.json")
	}

	// Convert to picker items for interactive selection
	items := make([]tui.PickerItem, len(todolists))
	for i, tl := range todolists {
		items[i] = todolistToPickerItem(tl)
	}

	// Show picker
	selected, err := tui.NewPicker(items,
		tui.WithPickerTitle("Select a todolist"),
		tui.WithEmptyMessage("No todolists found"),
	).Run()

	if err != nil {
		return nil, fmt.Errorf("todolist selection failed: %w", err)
	}
	if selected == nil {
		return nil, output.ErrUsage("todolist selection canceled")
	}

	return &ResolvedValue{
		Value:  selected.ID,
		Source: SourcePrompt,
	}, nil
}

// fetchTodolists retrieves all todolists for a project.
// explicitTodosetID, if non-empty, selects a specific todoset; otherwise
// the todoset is resolved automatically (prompting on multi-todoset projects).
func (r *Resolver) fetchTodolists(ctx context.Context, projectID, explicitTodosetID string) ([]basecamp.Todolist, error) {
	// Ensure account is configured
	if r.config.AccountID == "" {
		return nil, output.ErrUsage("Account must be resolved before fetching todolists")
	}

	// Parse project ID
	projectIDInt, err := strconv.ParseInt(projectID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid project ID: %w", err)
	}

	// Get todoset ID from project dock
	todosetID, err := r.getTodosetID(ctx, projectIDInt, explicitTodosetID)
	if err != nil {
		return nil, err
	}

	// Fetch todolists using SDK
	result, err := r.sdk.ForAccount(r.config.AccountID).Todolists().List(ctx, todosetID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch todolists: %w", err)
	}

	return result.Todolists, nil
}

// TodolistWithPersist resolves the todolist ID and optionally prompts to save it.
func (r *Resolver) TodolistWithPersist(ctx context.Context, projectID, explicitTodosetID string) (*ResolvedValue, error) {
	resolved, err := r.Todolist(ctx, projectID, explicitTodosetID)
	if err != nil {
		return nil, err
	}

	// Only prompt to persist if it was selected interactively
	if resolved.Source == SourcePrompt {
		_, _ = PromptAndPersistTodolistID(resolved.Value)
	}

	return resolved, nil
}

// todolistToPickerItem converts a Basecamp todolist to a picker item.
func todolistToPickerItem(tl basecamp.Todolist) tui.PickerItem {
	description := fmt.Sprintf("#%d", tl.ID)

	return tui.PickerItem{
		ID:          fmt.Sprintf("%d", tl.ID),
		Title:       tl.Name,
		Description: description,
	}
}

// createDefaultTodolist creates a "Tasks" todolist in the project's todoset.
func (r *Resolver) createDefaultTodolist(ctx context.Context, projectID, explicitTodosetID string) (*basecamp.Todolist, error) {
	projectIDInt, err := strconv.ParseInt(projectID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid project ID: %w", err)
	}

	todosetID, err := r.getTodosetID(ctx, projectIDInt, explicitTodosetID)
	if err != nil {
		return nil, err
	}

	todolist, err := r.sdk.ForAccount(r.config.AccountID).Todolists().Create(ctx, todosetID, &basecamp.CreateTodolistRequest{
		Name: "Tasks",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create default todolist: %w", err)
	}

	return todolist, nil
}

// getTodosetID resolves the todoset ID from a project's dock.
// Routes through Resolver.Todoset() to handle multi-todoset projects correctly.
// explicitTodosetID, if non-empty, bypasses resolution and uses the given ID.
func (r *Resolver) getTodosetID(ctx context.Context, projectID int64, explicitTodosetID string) (int64, error) {
	result, err := r.Todoset(ctx, fmt.Sprintf("%d", projectID), explicitTodosetID)
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(result.ToolID, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid todoset ID: %w", err)
	}
	return id, nil
}
