package completion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// RefreshResult contains the outcome of a refresh operation.
type RefreshResult struct {
	ProjectsCount int
	PeopleCount   int
	ProjectsErr   error
	PeopleErr     error
}

// HasError returns true if any refresh operation failed.
func (r RefreshResult) HasError() bool {
	return r.ProjectsErr != nil || r.PeopleErr != nil
}

// Error returns a combined error message if any operation failed.
func (r RefreshResult) Error() error {
	if r.ProjectsErr != nil && r.PeopleErr != nil {
		return errors.Join(
			fmt.Errorf("projects: %w", r.ProjectsErr),
			fmt.Errorf("people: %w", r.PeopleErr),
		)
	}
	if r.ProjectsErr != nil {
		return fmt.Errorf("projects: %w", r.ProjectsErr)
	}
	if r.PeopleErr != nil {
		return fmt.Errorf("people: %w", r.PeopleErr)
	}
	return nil
}

// Refresher handles background cache refresh operations.
type Refresher struct {
	store *Store
	sdk   *basecamp.AccountClient

	mu         sync.Mutex
	refreshing bool
}

// NewRefresher creates a new cache refresher.
func NewRefresher(store *Store, sdk *basecamp.AccountClient) *Refresher {
	return &Refresher{
		store: store,
		sdk:   sdk,
	}
}

// RefreshIfStale triggers a background refresh if the cache is stale.
// Returns immediately - the refresh happens asynchronously.
// If a refresh is already in progress, this is a no-op.
func (r *Refresher) RefreshIfStale(maxAge time.Duration) {
	if !r.store.IsStale(maxAge) {
		return
	}

	r.mu.Lock()
	if r.refreshing {
		r.mu.Unlock()
		return
	}
	r.refreshing = true
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			r.refreshing = false
			r.mu.Unlock()
		}()

		// Use a detached context with timeout for background refresh
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Errors are intentionally ignored in background refresh - this is best-effort
		r.RefreshAll(ctx)
	}()
}

// RefreshAll fetches fresh data from the API and updates the cache.
// This is a synchronous operation - use RefreshIfStale for async.
// On partial failure, preserves existing cached data for the failed portion.
// Returns RefreshResult with counts and any errors encountered.
func (r *Refresher) RefreshAll(ctx context.Context) RefreshResult {
	var result RefreshResult

	// Fetch projects and people in parallel
	var projects []basecamp.Project
	var people []basecamp.Person

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		projects, result.ProjectsErr = r.sdk.Projects().List(ctx, nil)
	}()

	go func() {
		defer wg.Done()
		people, result.PeopleErr = r.sdk.People().List(ctx, nil)
	}()

	wg.Wait()

	// Update each portion independently to preserve existing data on partial failure
	if result.ProjectsErr == nil && projects != nil {
		converted := convertProjects(projects)
		if err := r.store.UpdateProjects(converted); err != nil {
			result.ProjectsErr = err
		} else {
			result.ProjectsCount = len(converted)
		}
	}

	if result.PeopleErr == nil && people != nil {
		converted := convertPeople(people)
		if err := r.store.UpdatePeople(converted); err != nil {
			result.PeopleErr = err
		} else {
			result.PeopleCount = len(converted)
		}
	}

	return result
}

// RefreshProjects fetches fresh project data and updates the cache.
func (r *Refresher) RefreshProjects(ctx context.Context) error {
	projects, err := r.sdk.Projects().List(ctx, nil)
	if err != nil {
		return err
	}

	return r.store.UpdateProjects(convertProjects(projects))
}

// RefreshPeople fetches fresh people data and updates the cache.
func (r *Refresher) RefreshPeople(ctx context.Context) error {
	people, err := r.sdk.People().List(ctx, nil)
	if err != nil {
		return err
	}

	return r.store.UpdatePeople(convertPeople(people))
}

// convertProjects converts SDK projects to cached projects.
func convertProjects(projects []basecamp.Project) []CachedProject {
	result := make([]CachedProject, len(projects))
	for i, p := range projects {
		result[i] = CachedProject{
			ID:         p.ID,
			Name:       p.Name,
			Purpose:    p.Purpose,
			Bookmarked: p.Bookmarked,
			UpdatedAt:  p.UpdatedAt,
		}
	}
	return result
}

// convertPeople converts SDK people to cached people.
func convertPeople(people []basecamp.Person) []CachedPerson {
	result := make([]CachedPerson, len(people))
	for i, p := range people {
		result[i] = CachedPerson{
			ID:           p.ID,
			Name:         p.Name,
			EmailAddress: p.EmailAddress,
		}
	}
	return result
}

// IsRefreshing returns true if a background refresh is in progress.
func (r *Refresher) IsRefreshing() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refreshing
}
