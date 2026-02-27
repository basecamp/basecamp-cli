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
func (r *Resolver) Todolist(ctx context.Context, projectID string) (*ResolvedValue, error) {
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
	todolists, err := r.fetchTodolists(ctx, projectID)
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

	// No todolists found
	if len(todolists) == 0 {
		return nil, output.ErrNotFoundHint("todolists", projectID, "Create a todolist first")
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
func (r *Resolver) fetchTodolists(ctx context.Context, projectID string) ([]basecamp.Todolist, error) {
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
	todosetID, err := r.getTodosetID(ctx, projectIDInt)
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
func (r *Resolver) TodolistWithPersist(ctx context.Context, projectID string) (*ResolvedValue, error) {
	resolved, err := r.Todolist(ctx, projectID)
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

// getTodosetID retrieves the todoset ID from a project's dock.
func (r *Resolver) getTodosetID(ctx context.Context, projectID int64) (int64, error) {
	project, err := r.sdk.ForAccount(r.config.AccountID).Projects().Get(ctx, projectID)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch project: %w", err)
	}

	// Find enabled todoset in dock
	for _, tool := range project.Dock {
		if tool.Name == "todoset" && tool.Enabled {
			return tool.ID, nil
		}
	}

	return 0, output.ErrNotFoundHint("todoset", fmt.Sprintf("%d", projectID), "Project has no todoset enabled")
}
