// Package completion provides tab completion support for the bcq CLI.
// It implements a file-based cache for projects and people data that enables
// fast, offline-capable shell completions without requiring API calls.
package completion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CachedProject holds project data for tab completion.
// Fields are chosen to support ranking (HQ, Bookmarked, Recent) and display.
type CachedProject struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Purpose    string    `json:"purpose,omitempty"` // "hq", "team", or empty
	Bookmarked bool      `json:"bookmarked,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

// CachedPerson holds person data for tab completion.
type CachedPerson struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	EmailAddress string `json:"email_address,omitempty"`
}

// Cache stores completion data with metadata for staleness detection.
type Cache struct {
	Projects          []CachedProject `json:"projects,omitempty"`
	People            []CachedPerson  `json:"people,omitempty"`
	ProjectsUpdatedAt time.Time       `json:"projects_updated_at,omitempty"`
	PeopleUpdatedAt   time.Time       `json:"people_updated_at,omitempty"`
	UpdatedAt         time.Time       `json:"updated_at"` // Legacy, kept for backwards compat
	Version           int             `json:"version"`    // Schema version for future migrations
}

const (
	// CacheVersion is the current cache schema version.
	CacheVersion = 1

	// DefaultMaxAge is the default cache staleness threshold (1 hour).
	DefaultMaxAge = time.Hour

	// CacheFileName is the default cache file name.
	CacheFileName = "completion.json"
)

// Store handles reading and writing the completion cache.
type Store struct {
	dir string
	mu  sync.RWMutex
}

// NewStore creates a new cache store.
// If dir is empty, it uses the default location (~/.cache/bcq/).
func NewStore(dir string) *Store {
	if dir == "" {
		dir = defaultCacheDir()
	}
	return &Store{dir: dir}
}

// defaultCacheDir returns the default cache directory path.
// This matches the default from internal/config/config.go.
func defaultCacheDir() string {
	cacheDir := os.Getenv("XDG_CACHE_HOME")
	if cacheDir == "" {
		home, _ := os.UserHomeDir()
		cacheDir = filepath.Join(home, ".cache")
	}
	return filepath.Join(cacheDir, "bcq")
}

// Dir returns the cache directory path.
func (s *Store) Dir() string {
	return s.dir
}

// Path returns the full path to the cache file.
func (s *Store) Path() string {
	return filepath.Join(s.dir, CacheFileName)
}

// Load reads the cache from disk.
// Returns an empty cache if the file doesn't exist or is invalid.
func (s *Store) Load() (*Cache, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.loadUnsafe()
}

// loadUnsafe reads the cache without locking (caller must hold lock).
func (s *Store) loadUnsafe() (*Cache, error) {
	data, err := os.ReadFile(s.Path())
	if err != nil {
		if os.IsNotExist(err) {
			// Return empty cache, not an error
			return &Cache{Version: CacheVersion}, nil
		}
		return nil, err
	}

	var cache Cache
	if err := json.Unmarshal(data, &cache); err != nil {
		// Invalid JSON - return empty cache rather than error
		// This handles corrupted files gracefully
		return &Cache{Version: CacheVersion}, nil
	}

	return &cache, nil
}

// Save writes the cache to disk atomically.
// Sets all timestamps to now.
func (s *Store) Save(cache *Cache) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	cache.ProjectsUpdatedAt = now
	cache.PeopleUpdatedAt = now
	cache.UpdatedAt = now
	return s.saveUnsafe(cache)
}

// saveUnsafe writes the cache without locking (caller must hold lock).
// Does not modify timestamps - caller is responsible for setting them.
func (s *Store) saveUnsafe(cache *Cache) error {
	// Ensure directory exists
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}

	cache.Version = CacheVersion

	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}

	// Write atomically via temp file
	tmpPath := s.Path() + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}

	return os.Rename(tmpPath, s.Path())
}

// UpdateProjects updates just the projects in the cache.
// Only updates ProjectsUpdatedAt, preserving PeopleUpdatedAt.
func (s *Store) UpdateProjects(projects []CachedProject) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cache, err := s.loadUnsafe()
	if err != nil {
		cache = &Cache{Version: CacheVersion}
	}

	cache.Projects = projects
	cache.ProjectsUpdatedAt = time.Now()
	// Update legacy field to oldest of the two
	cache.UpdatedAt = oldestTime(cache.ProjectsUpdatedAt, cache.PeopleUpdatedAt)
	return s.saveUnsafe(cache)
}

// UpdatePeople updates just the people in the cache.
// Only updates PeopleUpdatedAt, preserving ProjectsUpdatedAt.
func (s *Store) UpdatePeople(people []CachedPerson) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cache, err := s.loadUnsafe()
	if err != nil {
		cache = &Cache{Version: CacheVersion}
	}

	cache.People = people
	cache.PeopleUpdatedAt = time.Now()
	// Update legacy field to oldest of the two
	cache.UpdatedAt = oldestTime(cache.ProjectsUpdatedAt, cache.PeopleUpdatedAt)
	return s.saveUnsafe(cache)
}

// oldestTime returns the oldest time, treating zero as infinitely old.
// This ensures a missing section (zero timestamp) makes the cache appear stale.
func oldestTime(a, b time.Time) time.Time {
	// Zero means "never populated" - treat as oldest possible time
	if a.IsZero() || b.IsZero() {
		return time.Time{} // Return zero to trigger staleness
	}
	if a.Before(b) {
		return a
	}
	return b
}

// IsStale returns true if the cache is older than maxAge or incomplete.
// A cache is considered stale if:
// - It doesn't exist or can't be loaded
// - Either per-section timestamp is missing (legacy cache or incomplete)
// - The oldest section timestamp exceeds maxAge
func (s *Store) IsStale(maxAge time.Duration) bool {
	cache, err := s.Load()
	if err != nil {
		return true
	}
	// Both sections must have timestamps (handles legacy caches without per-section timestamps)
	if cache.ProjectsUpdatedAt.IsZero() || cache.PeopleUpdatedAt.IsZero() {
		return true
	}
	// Check the oldest section against maxAge
	oldest := oldestTime(cache.ProjectsUpdatedAt, cache.PeopleUpdatedAt)
	return time.Since(oldest) > maxAge
}

// Clear removes the cache file.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := os.Remove(s.Path())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Projects returns cached projects, or nil if cache is empty/missing.
func (s *Store) Projects() []CachedProject {
	cache, err := s.Load()
	if err != nil {
		return nil
	}
	return cache.Projects
}

// People returns cached people, or nil if cache is empty/missing.
func (s *Store) People() []CachedPerson {
	cache, err := s.Load()
	if err != nil {
		return nil
	}
	return cache.People
}
