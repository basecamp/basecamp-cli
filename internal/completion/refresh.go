package completion

import (
	"context"
	"sync"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// Refresher handles background cache refresh operations.
type Refresher struct {
	store *Store
	sdk   *basecamp.Client

	mu         sync.Mutex
	refreshing bool
}

// NewRefresher creates a new cache refresher.
func NewRefresher(store *Store, sdk *basecamp.Client) *Refresher {
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
		_ = r.RefreshAll(ctx)
	}()
}

// RefreshAll fetches fresh data from the API and updates the cache.
// This is a synchronous operation - use RefreshIfStale for async.
// On partial failure, preserves existing cached data for the failed portion.
func (r *Refresher) RefreshAll(ctx context.Context) error {
	// Fetch projects and people in parallel
	var projects []basecamp.Project
	var people []basecamp.Person
	var projectsErr, peopleErr error

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		projects, projectsErr = r.sdk.Projects().List(ctx, nil)
	}()

	go func() {
		defer wg.Done()
		people, peopleErr = r.sdk.People().List(ctx)
	}()

	wg.Wait()

	// Update each portion independently to preserve existing data on partial failure
	projectsOK := false
	if projectsErr == nil && projects != nil {
		if err := r.store.UpdateProjects(convertProjects(projects)); err != nil {
			projectsErr = err
		} else {
			projectsOK = true
		}
	}

	peopleOK := false
	if peopleErr == nil && people != nil {
		if err := r.store.UpdatePeople(convertPeople(people)); err != nil {
			peopleErr = err
		} else {
			peopleOK = true
		}
	}

	// Only return error if both failed; partial success is acceptable
	if !projectsOK && !peopleOK {
		if projectsErr != nil {
			return projectsErr
		}
		return peopleErr
	}
	return nil
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
	people, err := r.sdk.People().List(ctx)
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
