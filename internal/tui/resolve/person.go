package resolve

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/output"
	"github.com/basecamp/bcq/internal/tui"
)

// Person resolves a person ID interactively.
// Unlike Account/Project/Todolist, Person doesn't have a config fallback
// since it's typically used for assignment operations where the user
// explicitly selects someone.
//
// Returns the resolved person ID and the source it came from.
func (r *Resolver) Person(ctx context.Context) (*ResolvedValue, error) {
	// Try interactive prompt if available
	if !r.IsInteractive() {
		return nil, output.ErrUsage("Person ID required")
	}

	// Ensure account is configured
	if r.config.AccountID == "" {
		return nil, output.ErrUsage("Account must be resolved before fetching people")
	}

	// Create a page fetcher for the paginated picker.
	var cachedPeople []basecamp.Person
	fetcher := func(ctx context.Context, cursor string) (*tui.PageResult, error) {
		// Only fetch on the first call (cursor is empty)
		if cursor != "" {
			return &tui.PageResult{
				Items:   nil,
				HasMore: false,
			}, nil
		}

		// Fetch all people using SDK
		people, err := r.sdk.ForAccount(r.config.AccountID).People().List(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch people: %w", err)
		}

		if len(people) == 0 {
			return nil, output.ErrNotFoundHint("people", "", "No people found in this account")
		}

		// Cache for potential single-person shortcut
		cachedPeople = people

		// Sort people alphabetically by name
		sortPeopleForPicker(people)

		// Convert to picker items
		items := make([]tui.PickerItem, len(people))
		for i, p := range people {
			items[i] = personToPickerItem(p)
		}

		return &tui.PageResult{
			Items:      items,
			HasMore:    false,
			NextCursor: "done",
		}, nil
	}

	// Use paginated picker with loading spinner
	selected, err := tui.NewPaginatedPicker(ctx, fetcher,
		tui.WithPaginatedPickerTitle("Select a person"),
		tui.WithLoadingMessage("Loading people..."),
	).Run()

	if err != nil {
		return nil, fmt.Errorf("person selection failed: %w", err)
	}
	if selected == nil {
		return nil, output.ErrUsage("person selection canceled")
	}

	// If only one person was available, note the source as default
	source := SourcePrompt
	if len(cachedPeople) == 1 {
		source = SourceDefault
	}

	return &ResolvedValue{
		Value:  selected.ID,
		Source: source,
	}, nil
}

// PersonInProject resolves a person ID interactively from project members.
// This is useful when you want to limit the selection to people who have
// access to a specific project.
func (r *Resolver) PersonInProject(ctx context.Context, projectID string) (*ResolvedValue, error) {
	// Try interactive prompt if available
	if !r.IsInteractive() {
		return nil, output.ErrUsage("Person ID required")
	}

	// Ensure account is configured
	if r.config.AccountID == "" {
		return nil, output.ErrUsage("Account must be resolved before fetching people")
	}

	if projectID == "" {
		return nil, output.ErrUsage("Project must be resolved before fetching project people")
	}

	// Create a page fetcher for the paginated picker.
	var cachedPeople []basecamp.Person
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

		// Fetch project people using SDK
		people, err := r.sdk.ForAccount(r.config.AccountID).People().ListProjectPeople(ctx, bucketID)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch project people: %w", err)
		}

		if len(people) == 0 {
			return nil, output.ErrNotFoundHint("people", projectID, "No members found in this project")
		}

		// Cache for potential single-person shortcut
		cachedPeople = people

		// Sort people alphabetically by name
		sortPeopleForPicker(people)

		// Convert to picker items
		items := make([]tui.PickerItem, len(people))
		for i, p := range people {
			items[i] = personToPickerItem(p)
		}

		return &tui.PageResult{
			Items:      items,
			HasMore:    false,
			NextCursor: "done",
		}, nil
	}

	// Use paginated picker with loading spinner
	selected, err := tui.NewPaginatedPicker(ctx, fetcher,
		tui.WithPaginatedPickerTitle("Select a person"),
		tui.WithLoadingMessage("Loading project members..."),
	).Run()

	if err != nil {
		return nil, fmt.Errorf("person selection failed: %w", err)
	}
	if selected == nil {
		return nil, output.ErrUsage("person selection canceled")
	}

	// If only one person was available, note the source as default
	source := SourcePrompt
	if len(cachedPeople) == 1 {
		source = SourceDefault
	}

	return &ResolvedValue{
		Value:  selected.ID,
		Source: source,
	}, nil
}

// personToPickerItem converts a Basecamp person to a picker item.
func personToPickerItem(p basecamp.Person) tui.PickerItem {
	description := fmt.Sprintf("#%d", p.ID)
	if p.EmailAddress != "" {
		description = p.EmailAddress + " " + description
	}
	if p.Title != "" {
		description = p.Title + " - " + description
	}

	return tui.PickerItem{
		ID:          fmt.Sprintf("%d", p.ID),
		Title:       p.Name,
		Description: description,
	}
}

// sortPeopleForPicker sorts people in place alphabetically by name.
func sortPeopleForPicker(people []basecamp.Person) {
	sort.Slice(people, func(i, j int) bool {
		return people[i].Name < people[j].Name
	})
}
