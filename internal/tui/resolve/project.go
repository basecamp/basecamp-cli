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

	// Ensure account is configured
	if r.config.AccountID == "" {
		return nil, output.ErrUsage("Account must be resolved before fetching projects")
	}

	// 3. Non-interactive mode requires explicit project
	if !r.IsInteractive() {
		return nil, output.ErrUsageHint("Project ID required", "Use --project or set in .basecamp/config.json")
	}

	// 4. Interactive mode - show picker with loading spinner
	// Capture accountID for the loader closure
	accountID := r.config.AccountID
	loader := func() ([]tui.PickerItem, error) {
		projects, err := r.sdk.ForAccount(accountID).Projects().List(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch projects: %w", err)
		}

		if len(projects) == 0 {
			return nil, output.ErrNotFoundHint("projects", "", "No projects found in this account")
		}

		// Sort projects: bookmarked first, then by name
		sortProjectsForPicker(projects)

		// Convert to picker items
		items := make([]tui.PickerItem, len(projects))
		for i, proj := range projects {
			items[i] = projectToPickerItem(proj)
		}
		return items, nil
	}

	// Show picker with loading state (auto-selects if only one project)
	selected, err := tui.NewPickerWithLoader(loader,
		tui.WithPickerTitle("Select a project"),
		tui.WithEmptyMessage("No projects found"),
		tui.WithAutoSelectSingle(),
		tui.WithLoading("Loading projects..."),
	).Run()

	if err != nil {
		return nil, fmt.Errorf("project selection failed: %w", err)
	}
	if selected == nil {
		return nil, output.ErrUsage("project selection canceled")
	}

	return &ResolvedValue{
		Value:  selected.ID,
		Source: SourcePrompt,
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
