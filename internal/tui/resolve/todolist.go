package resolve

import (
	"context"
	"fmt"
	"strconv"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/output"
	"github.com/basecamp/bcq/internal/tui"
)

// Todolist resolves the todolist ID using the following precedence:
// 1. CLI flag (--todolist or --list)
// 2. Config file (todolist_id)
// 3. Interactive prompt (if terminal is interactive)
// 4. Error (if no todolist can be determined)
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

	// 3. Try interactive prompt if available
	if !r.IsInteractive() {
		return nil, output.ErrUsageHint("No todolist specified", "Use --list or set todolist_id in .basecamp/config.json")
	}

	// Ensure project is configured
	if projectID == "" {
		return nil, output.ErrUsage("Project must be resolved before fetching todolists")
	}

	// Create a page fetcher for the paginated picker.
	var cachedTodolists []basecamp.Todolist
	fetcher := func(ctx context.Context, cursor string) (*tui.PageResult, error) {
		// Only fetch on the first call (cursor is empty)
		if cursor != "" {
			return &tui.PageResult{
				Items:   nil,
				HasMore: false,
			}, nil
		}

		// Parse project ID
		bucketID, err := strconv.ParseInt(projectID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid project ID: %w", err)
		}

		// Get todoset ID from project dock
		todosetID, err := r.getTodosetID(ctx, bucketID)
		if err != nil {
			return nil, err
		}

		// Fetch todolists using SDK
		todolists, err := r.sdk.ForAccount(r.config.AccountID).Todolists().List(ctx, bucketID, todosetID, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch todolists: %w", err)
		}

		if len(todolists) == 0 {
			return &tui.PageResult{
				Items:   nil,
				HasMore: false,
			}, nil
		}

		// Cache for potential single-todolist shortcut
		cachedTodolists = todolists

		// Convert to picker items
		items := make([]tui.PickerItem, len(todolists))
		for i, tl := range todolists {
			items[i] = todolistToPickerItem(tl)
		}

		return &tui.PageResult{
			Items:      items,
			HasMore:    false,
			NextCursor: "done",
		}, nil
	}

	// Use paginated picker with loading spinner
	selected, err := tui.NewPaginatedPicker(ctx, fetcher,
		tui.WithPaginatedPickerTitle("Select a todolist"),
		tui.WithLoadingMessage("Loading todolists..."),
	).Run()

	if err != nil {
		return nil, fmt.Errorf("todolist selection failed: %w", err)
	}
	if selected == nil {
		return nil, output.ErrUsage("todolist selection canceled")
	}

	// If only one todolist was available, note the source as default
	source := SourcePrompt
	if len(cachedTodolists) == 1 {
		source = SourceDefault
	}

	return &ResolvedValue{
		Value:  selected.ID,
		Source: source,
	}, nil
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
func (r *Resolver) getTodosetID(ctx context.Context, bucketID int64) (int64, error) {
	project, err := r.sdk.ForAccount(r.config.AccountID).Projects().Get(ctx, bucketID)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch project: %w", err)
	}

	// Find todoset in dock
	for _, tool := range project.Dock {
		if tool.Name == "todoset" {
			return tool.ID, nil
		}
	}

	return 0, output.ErrNotFoundHint("todoset", fmt.Sprintf("%d", bucketID), "Project has no todoset enabled")
}
