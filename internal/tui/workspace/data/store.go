// Package data provides the data layer between workspace views and the SDK.
package data

import (
	"sync"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// Store provides an in-memory cache for workspace data.
// Views read from the store; background commands refresh it.
type Store struct {
	mu sync.RWMutex

	projects    []basecamp.Project
	projectsAt  time.Time
	projectByID map[int64]*basecamp.Project

	cache *Cache
}

// NewStore creates an empty data store.
func NewStore() *Store {
	return &Store{
		projectByID: make(map[int64]*basecamp.Project),
		cache:       NewCache(),
	}
}

// Clear resets the store to an empty state, discarding all cached data.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.projects = nil
	s.projectsAt = time.Time{}
	s.projectByID = make(map[int64]*basecamp.Project)
	s.cache.Clear()
}

// Cache returns the response cache for stale-while-revalidate patterns.
func (s *Store) Cache() *Cache {
	return s.cache
}

// SetProjects replaces the cached project list.
func (s *Store) SetProjects(projects []basecamp.Project) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.projects = projects
	s.projectsAt = time.Now()
	s.projectByID = make(map[int64]*basecamp.Project, len(projects))
	for i := range projects {
		s.projectByID[projects[i].ID] = &projects[i]
	}
}

// Projects returns a copy of the cached project list.
func (s *Store) Projects() []basecamp.Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make([]basecamp.Project, len(s.projects))
	copy(cp, s.projects)
	return cp
}

// Project returns a cached project by ID (returns a copy).
func (s *Store) Project(id int64) *basecamp.Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := s.projectByID[id]
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

// UpsertProject inserts or replaces a single project in the cache.
func (s *Store) UpsertProject(project basecamp.Project) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.projectsAt = time.Now()

	// Replace if exists
	for i := range s.projects {
		if s.projects[i].ID == project.ID {
			s.projects[i] = project
			s.projectByID[project.ID] = &s.projects[i]
			return
		}
	}
	// Insert
	s.projects = append(s.projects, project)
	s.projectByID[project.ID] = &s.projects[len(s.projects)-1]
}

// ProjectsAge returns how old the cached project data is.
func (s *Store) ProjectsAge() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.projectsAt.IsZero() {
		return time.Duration(0)
	}
	return time.Since(s.projectsAt)
}

// HasProjects returns true if projects have been loaded at least once.
func (s *Store) HasProjects() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.projectsAt.IsZero()
}
