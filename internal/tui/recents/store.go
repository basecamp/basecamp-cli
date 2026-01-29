// Package recents provides a store for tracking recently used items.
package recents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Item represents a recently used item.
type Item struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Type        string    `json:"type"`
	AccountID   string    `json:"account_id,omitempty"`
	ProjectID   string    `json:"project_id,omitempty"`
	UsedAt      time.Time `json:"used_at"`
}

// Store manages recently used items.
type Store struct {
	mu        sync.RWMutex
	items     map[string][]Item // keyed by type (e.g., "project", "todolist", "recording")
	maxItems  int
	path      string
	lastError error // last error from save(), for debugging
}

// NewStore creates a new recent items store.
// The store file is located at <cacheDir>/recents.json.
func NewStore(cacheDir string) *Store {
	s := &Store{
		items:    make(map[string][]Item),
		maxItems: 10,
		path:     filepath.Join(cacheDir, "recents.json"),
	}
	s.load()
	return s
}

// Add adds or updates an item in the recent items list.
func (s *Store) Add(item Item) {
	item.UsedAt = time.Now()

	// Copy state while holding the lock, then release before I/O
	var snapshot map[string][]Item
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		items := s.items[item.Type]

		// Remove existing item with same ID
		filtered := make([]Item, 0, len(items))
		for _, existing := range items {
			if existing.ID != item.ID {
				filtered = append(filtered, existing)
			}
		}

		// Add new item at the front
		items = append([]Item{item}, filtered...)

		// Trim to max size
		if len(items) > s.maxItems {
			items = items[:s.maxItems]
		}

		s.items[item.Type] = items

		// Copy state for saving outside lock
		snapshot = s.copyItems()
	}()

	// Save outside the lock to avoid blocking readers during I/O
	s.saveSnapshot(snapshot)
}

// Get returns recent items of the specified type, optionally filtered by account/project.
// Returns a copy of the items to prevent callers from mutating internal state.
func (s *Store) Get(itemType string, accountID, projectID string) []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := s.items[itemType]
	if accountID == "" && projectID == "" {
		// Return a copy to prevent mutation of internal state
		result := make([]Item, len(items))
		copy(result, items)
		return result
	}

	// Filter by account/project (filtering already creates a new slice)
	var filtered []Item
	for _, item := range items {
		if accountID != "" && item.AccountID != accountID {
			continue
		}
		if projectID != "" && item.ProjectID != projectID {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// Clear removes all items of the specified type.
func (s *Store) Clear(itemType string) {
	var snapshot map[string][]Item
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.items, itemType)
		snapshot = s.copyItems()
	}()
	s.saveSnapshot(snapshot)
}

// ClearAll removes all recent items.
func (s *Store) ClearAll() {
	var snapshot map[string][]Item
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.items = make(map[string][]Item)
		snapshot = s.copyItems()
	}()
	s.saveSnapshot(snapshot)
}

// copyItems returns a deep copy of the items map (must be called with lock held).
func (s *Store) copyItems() map[string][]Item {
	result := make(map[string][]Item, len(s.items))
	for k, v := range s.items {
		copied := make([]Item, len(v))
		copy(copied, v)
		result[k] = copied
	}
	return result
}

// load reads the store from disk.
func (s *Store) load() {
	data, err := os.ReadFile(s.path) //nolint:gosec // G304: Path is from trusted config
	if err != nil {
		return
	}

	var items map[string][]Item
	if err := json.Unmarshal(data, &items); err != nil {
		return
	}

	s.items = items
}

// saveSnapshot writes the given items snapshot to disk.
// Errors are stored in lastError for debugging (recents are non-critical).
// This method is safe to call without holding the lock.
func (s *Store) saveSnapshot(items map[string][]Item) {
	// Ensure directory exists
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		s.mu.Lock()
		s.lastError = err
		s.mu.Unlock()
		return
	}

	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		s.mu.Lock()
		s.lastError = err
		s.mu.Unlock()
		return
	}

	if err := os.WriteFile(s.path, data, 0600); err != nil {
		s.mu.Lock()
		s.lastError = err
		s.mu.Unlock()
		return
	}

	s.mu.Lock()
	s.lastError = nil
	s.mu.Unlock()
}

// LastError returns the last error from a save operation, if any.
// Useful for debugging persistence issues.
func (s *Store) LastError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastError
}

// ItemTypes for common entities.
const (
	TypeProject   = "project"
	TypeTodolist  = "todolist"
	TypeRecording = "recording"
	TypePerson    = "person"
)
