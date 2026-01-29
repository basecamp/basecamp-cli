package resolve

import (
	"context"
	"fmt"
	"sort"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/output"
	"github.com/basecamp/bcq/internal/tui"
)

// Project resolves the project ID using the following precedence:
// 1. CLI flag (--project)
// 2. Config file (project_id)
// 3. Interactive prompt (if terminal is interactive)
// 4. Error (if no project can be determined)
//
// The account must be resolved before calling this method.
// Returns the resolved project ID and the source it came from.
func (r *Resolver) Project(ctx context.Context) (*ResolvedValue, error) {
	// 1. Check CLI flag
	if r.flags.Project != "" {
		return &ResolvedValue{
			Value:  r.flags.Project,
			Source: SourceFlag,
		}, nil
	}

	// 2. Check config
	if r.config.ProjectID != "" {
		return &ResolvedValue{
			Value:  r.config.ProjectID,
			Source: SourceConfig,
		}, nil
	}

	// 3. Try interactive prompt if available
	if !r.IsInteractive() {
		return nil, output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
	}

	// Ensure account is configured
	if r.config.AccountID == "" {
		return nil, output.ErrUsage("Account must be resolved before fetching projects")
	}

	// Create a page fetcher for the paginated picker.
	// The Basecamp SDK handles pagination internally, so we load all projects
	// in the first fetch. The paginated picker still provides a nice loading
	// spinner and progressive UI.
	var cachedProjects []basecamp.Project
	fetcher := func(ctx context.Context, cursor string) (*tui.PageResult, error) {
		// Only fetch on the first call (cursor is empty)
		if cursor != "" {
			// Already loaded all projects
			return &tui.PageResult{
				Items:   nil,
				HasMore: false,
			}, nil
		}

		// Fetch all projects using SDK (SDK handles pagination internally)
		projects, err := r.sdk.ForAccount(r.config.AccountID).Projects().List(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch projects: %w", err)
		}

		if len(projects) == 0 {
			return &tui.PageResult{
				Items:   nil,
				HasMore: false,
			}, nil
		}

		// Cache for potential single-project shortcut
		cachedProjects = projects

		// Sort projects: bookmarked first, then by name
		sortProjectsForPicker(projects)

		// Convert to picker items
		items := make([]tui.PickerItem, len(projects))
		for i, proj := range projects {
			items[i] = projectToPickerItem(proj)
		}

		return &tui.PageResult{
			Items:      items,
			HasMore:    false,
			NextCursor: "done", // Mark as done so we don't fetch again
		}, nil
	}

	// Use paginated picker with loading spinner
	selected, err := tui.NewPaginatedPicker(ctx, fetcher,
		tui.WithPaginatedPickerTitle("Select a project"),
		tui.WithLoadingMessage("Loading projects..."),
	).Run()

	if err != nil {
		return nil, fmt.Errorf("project selection failed: %w", err)
	}
	if selected == nil {
		return nil, output.ErrUsage("project selection canceled")
	}

	// If only one project was available, note the source as default
	source := SourcePrompt
	if len(cachedProjects) == 1 {
		source = SourceDefault
	}

	return &ResolvedValue{
		Value:  selected.ID,
		Source: source,
	}, nil
}

// ProjectWithPersist resolves the project ID and optionally prompts to save it.
// This is useful for commands that want to offer to save the selected project.
func (r *Resolver) ProjectWithPersist(ctx context.Context) (*ResolvedValue, error) {
	resolved, err := r.Project(ctx)
	if err != nil {
		return nil, err
	}

	// Only prompt to persist if it was selected interactively
	if resolved.Source == SourcePrompt {
		_, _ = PromptAndPersistProjectID(resolved.Value)
	}

	return resolved, nil
}

// projectToPickerItem converts a Basecamp project to a picker item.
func projectToPickerItem(proj basecamp.Project) tui.PickerItem {
	title := proj.Name
	if proj.Bookmarked {
		title = "★ " + title
	}

	description := fmt.Sprintf("#%d", proj.ID)
	if proj.Purpose != "" {
		description = proj.Purpose + " " + description
	}
	if proj.Status != "" && proj.Status != "active" {
		description = fmt.Sprintf("[%s] %s", proj.Status, description)
	}

	return tui.PickerItem{
		ID:          fmt.Sprintf("%d", proj.ID),
		Title:       title,
		Description: description,
	}
}

// sortProjectsForPicker sorts projects in place with bookmarked first, then alphabetically by name.
func sortProjectsForPicker(projects []basecamp.Project) {
	sort.Slice(projects, func(i, j int) bool {
		// Bookmarked projects come first
		if projects[i].Bookmarked != projects[j].Bookmarked {
			return projects[i].Bookmarked
		}
		// Same bookmark status - sort alphabetically
		return projects[i].Name < projects[j].Name
	})
}
