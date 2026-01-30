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
	// Ensure account is configured
	if r.config.AccountID == "" {
		return nil, output.ErrUsage("Account must be resolved before fetching people")
	}

	// Non-interactive mode requires explicit person ID
	if !r.IsInteractive() {
		return nil, output.ErrUsage("Person ID required")
	}

	// Interactive mode - show picker with loading spinner
	accountID := r.config.AccountID
	loader := func() ([]tui.PickerItem, error) {
		result, err := r.sdk.ForAccount(accountID).People().List(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch people: %w", err)
		}
		people := result.People

		if len(people) == 0 {
			return nil, output.ErrNotFoundHint("people", "", "No people found in this account")
		}

		// Sort people alphabetically by name
		sortPeopleForPicker(people)

		// Convert to picker items
		items := make([]tui.PickerItem, len(people))
		for i, p := range people {
			items[i] = personToPickerItem(p)
		}
		return items, nil
	}

	// Show picker with loading state (auto-selects if only one person)
	selected, err := tui.NewPickerWithLoader(loader,
		tui.WithPickerTitle("Select a person"),
		tui.WithEmptyMessage("No people found"),
		tui.WithAutoSelectSingle(),
		tui.WithLoading("Loading people..."),
	).Run()

	if err != nil {
		return nil, fmt.Errorf("person selection failed: %w", err)
	}
	if selected == nil {
		return nil, output.ErrUsage("person selection canceled")
	}

	return &ResolvedValue{
		Value:  selected.ID,
		Source: SourcePrompt,
	}, nil
}

// PersonInProject resolves a person ID interactively from project members.
// This is useful when you want to limit the selection to people who have
// access to a specific project.
func (r *Resolver) PersonInProject(ctx context.Context, projectID string) (*ResolvedValue, error) {
	// Ensure account is configured
	if r.config.AccountID == "" {
		return nil, output.ErrUsage("Account must be resolved before fetching people")
	}

	if projectID == "" {
		return nil, output.ErrUsage("Project must be resolved before fetching project people")
	}

	// Non-interactive mode requires explicit person ID
	if !r.IsInteractive() {
		return nil, output.ErrUsage("Person ID required")
	}

	// Parse project ID
	bucketID, err := strconv.ParseInt(projectID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid project ID: %w", err)
	}

	// Interactive mode - show picker with loading spinner
	accountID := r.config.AccountID
	loader := func() ([]tui.PickerItem, error) {
		result, err := r.sdk.ForAccount(accountID).People().ListProjectPeople(ctx, bucketID, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch project people: %w", err)
		}
		people := result.People

		if len(people) == 0 {
			return nil, output.ErrNotFoundHint("people", projectID, "No members found in this project")
		}

		// Sort people alphabetically by name
		sortPeopleForPicker(people)

		// Convert to picker items
		items := make([]tui.PickerItem, len(people))
		for i, p := range people {
			items[i] = personToPickerItem(p)
		}
		return items, nil
	}

	// Show picker with loading state (auto-selects if only one person)
	selected, err := tui.NewPickerWithLoader(loader,
		tui.WithPickerTitle("Select a person"),
		tui.WithEmptyMessage("No people found"),
		tui.WithAutoSelectSingle(),
		tui.WithLoading("Loading people..."),
	).Run()

	if err != nil {
		return nil, fmt.Errorf("person selection failed: %w", err)
	}
	if selected == nil {
		return nil, output.ErrUsage("person selection canceled")
	}

	return &ResolvedValue{
		Value:  selected.ID,
		Source: SourcePrompt,
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
