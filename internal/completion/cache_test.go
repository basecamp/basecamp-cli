package completion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	cache := &Cache{
		Projects: []CachedProject{
			{ID: 1, Name: "Project One", Purpose: "hq", Bookmarked: true},
			{ID: 2, Name: "Project Two", Bookmarked: false},
		},
		People: []CachedPerson{
			{ID: 100, Name: "Alice", EmailAddress: "alice@example.com"},
			{ID: 200, Name: "Bob", EmailAddress: "bob@example.com"},
		},
	}

	// Save
	if err := store.Save(cache); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(store.Path()); os.IsNotExist(err) {
		t.Fatal("Cache file was not created")
	}

	// Load
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Verify data
	if len(loaded.Projects) != 2 {
		t.Errorf("Expected 2 projects, got %d", len(loaded.Projects))
	}
	if loaded.Projects[0].Name != "Project One" {
		t.Errorf("Expected 'Project One', got %q", loaded.Projects[0].Name)
	}
	if loaded.Projects[0].Purpose != "hq" {
		t.Errorf("Expected purpose 'hq', got %q", loaded.Projects[0].Purpose)
	}
	if !loaded.Projects[0].Bookmarked {
		t.Error("Expected Project One to be bookmarked")
	}

	if len(loaded.People) != 2 {
		t.Errorf("Expected 2 people, got %d", len(loaded.People))
	}
	if loaded.People[0].EmailAddress != "alice@example.com" {
		t.Errorf("Expected alice@example.com, got %q", loaded.People[0].EmailAddress)
	}

	// Verify metadata was set
	if loaded.Version != CacheVersion {
		t.Errorf("Expected version %d, got %d", CacheVersion, loaded.Version)
	}
	if loaded.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set")
	}
}

func TestStore_LoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Load from non-existent file
	cache, err := store.Load()
	if err != nil {
		t.Fatalf("Load should not error on missing file: %v", err)
	}

	// Should return empty cache
	if len(cache.Projects) != 0 {
		t.Errorf("Expected empty projects, got %d", len(cache.Projects))
	}
	if len(cache.People) != 0 {
		t.Errorf("Expected empty people, got %d", len(cache.People))
	}
	if cache.Version != CacheVersion {
		t.Errorf("Expected version %d, got %d", CacheVersion, cache.Version)
	}
}

func TestStore_LoadCorruptedFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Write corrupted JSON
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), []byte("not valid json{"), 0600); err != nil {
		t.Fatal(err)
	}

	// Load should succeed with empty cache
	cache, err := store.Load()
	if err != nil {
		t.Fatalf("Load should not error on corrupted file: %v", err)
	}

	if len(cache.Projects) != 0 {
		t.Errorf("Expected empty projects on corrupted file, got %d", len(cache.Projects))
	}
}

func TestStore_UpdateProjects(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Save initial cache with people
	initial := &Cache{
		People: []CachedPerson{
			{ID: 100, Name: "Alice"},
		},
	}
	if err := store.Save(initial); err != nil {
		t.Fatal(err)
	}

	// Update just projects
	projects := []CachedProject{
		{ID: 1, Name: "New Project"},
	}
	if err := store.UpdateProjects(projects); err != nil {
		t.Fatalf("UpdateProjects failed: %v", err)
	}

	// Verify both are present
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}

	if len(loaded.Projects) != 1 {
		t.Errorf("Expected 1 project, got %d", len(loaded.Projects))
	}
	if len(loaded.People) != 1 {
		t.Errorf("Expected 1 person (preserved), got %d", len(loaded.People))
	}
}

func TestStore_UpdatePeople(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Save initial cache with projects
	initial := &Cache{
		Projects: []CachedProject{
			{ID: 1, Name: "Existing Project"},
		},
	}
	if err := store.Save(initial); err != nil {
		t.Fatal(err)
	}

	// Update just people
	people := []CachedPerson{
		{ID: 100, Name: "New Person"},
	}
	if err := store.UpdatePeople(people); err != nil {
		t.Fatalf("UpdatePeople failed: %v", err)
	}

	// Verify both are present
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}

	if len(loaded.People) != 1 {
		t.Errorf("Expected 1 person, got %d", len(loaded.People))
	}
	if len(loaded.Projects) != 1 {
		t.Errorf("Expected 1 project (preserved), got %d", len(loaded.Projects))
	}
}

func TestStore_IsStale(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Empty cache is stale
	if !store.IsStale(time.Hour) {
		t.Error("Empty cache should be stale")
	}

	// Save fresh cache
	if err := store.Save(&Cache{Projects: []CachedProject{{ID: 1, Name: "Test"}}}); err != nil {
		t.Fatal(err)
	}

	// Fresh cache is not stale
	if store.IsStale(time.Hour) {
		t.Error("Fresh cache should not be stale")
	}

	// Fresh cache with very short maxAge is stale
	if !store.IsStale(time.Nanosecond) {
		t.Error("Cache should be stale with nanosecond maxAge")
	}
}

func TestStore_Clear(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Save something
	if err := store.Save(&Cache{Projects: []CachedProject{{ID: 1, Name: "Test"}}}); err != nil {
		t.Fatal(err)
	}

	// Verify it exists
	if _, err := os.Stat(store.Path()); os.IsNotExist(err) {
		t.Fatal("Cache file should exist")
	}

	// Clear
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}

	// Verify it's gone
	if _, err := os.Stat(store.Path()); !os.IsNotExist(err) {
		t.Error("Cache file should be removed")
	}

	// Clearing again should not error
	if err := store.Clear(); err != nil {
		t.Errorf("Clear should be idempotent: %v", err)
	}
}

func TestStore_Projects(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Empty cache returns nil
	if projects := store.Projects(); projects != nil {
		t.Errorf("Expected nil, got %v", projects)
	}

	// Save and retrieve
	expected := []CachedProject{{ID: 1, Name: "Test"}}
	if err := store.Save(&Cache{Projects: expected}); err != nil {
		t.Fatal(err)
	}

	projects := store.Projects()
	if len(projects) != 1 {
		t.Errorf("Expected 1 project, got %d", len(projects))
	}
}

func TestStore_People(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Empty cache returns nil
	if people := store.People(); people != nil {
		t.Errorf("Expected nil, got %v", people)
	}

	// Save and retrieve
	expected := []CachedPerson{{ID: 100, Name: "Alice"}}
	if err := store.Save(&Cache{People: expected}); err != nil {
		t.Fatal(err)
	}

	people := store.People()
	if len(people) != 1 {
		t.Errorf("Expected 1 person, got %d", len(people))
	}
}

func TestStore_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Save should write atomically
	cache := &Cache{Projects: []CachedProject{{ID: 1, Name: "Test"}}}
	if err := store.Save(cache); err != nil {
		t.Fatal(err)
	}

	// Temp file should not exist
	tmpPath := store.Path() + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("Temp file should be cleaned up")
	}
}

func TestStore_Dir(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if store.Dir() != dir {
		t.Errorf("Expected dir %q, got %q", dir, store.Dir())
	}
}

func TestStore_Path(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	expected := filepath.Join(dir, CacheFileName)
	if store.Path() != expected {
		t.Errorf("Expected path %q, got %q", expected, store.Path())
	}
}

func TestNewStore_DefaultDir(t *testing.T) {
	store := NewStore("")
	if store.Dir() == "" {
		t.Error("Default dir should not be empty")
	}
}

func TestCache_JSONFormat(t *testing.T) {
	// Verify JSON field names match expectations
	cache := &Cache{
		Projects: []CachedProject{
			{ID: 1, Name: "Test", Purpose: "hq", Bookmarked: true},
		},
		People: []CachedPerson{
			{ID: 100, Name: "Alice", EmailAddress: "alice@example.com"},
		},
		UpdatedAt: time.Now(),
		Version:   1,
	}

	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}

	// Unmarshal to map to check field names
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	// Check top-level fields
	if _, ok := m["projects"]; !ok {
		t.Error("Expected 'projects' field")
	}
	if _, ok := m["people"]; !ok {
		t.Error("Expected 'people' field")
	}
	if _, ok := m["updated_at"]; !ok {
		t.Error("Expected 'updated_at' field")
	}
	if _, ok := m["version"]; !ok {
		t.Error("Expected 'version' field")
	}
}

func TestStore_PerSectionTimestamps(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// UpdateProjects sets ProjectsUpdatedAt but not PeopleUpdatedAt
	projects := []CachedProject{{ID: 1, Name: "Project"}}
	if err := store.UpdateProjects(projects); err != nil {
		t.Fatal(err)
	}

	cache, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}

	if cache.ProjectsUpdatedAt.IsZero() {
		t.Error("ProjectsUpdatedAt should be set after UpdateProjects")
	}
	if !cache.PeopleUpdatedAt.IsZero() {
		t.Error("PeopleUpdatedAt should not be set after UpdateProjects only")
	}

	// UpdatePeople sets PeopleUpdatedAt, preserves ProjectsUpdatedAt
	people := []CachedPerson{{ID: 100, Name: "Person"}}
	if err := store.UpdatePeople(people); err != nil {
		t.Fatal(err)
	}

	cache, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}

	if cache.ProjectsUpdatedAt.IsZero() {
		t.Error("ProjectsUpdatedAt should still be set")
	}
	if cache.PeopleUpdatedAt.IsZero() {
		t.Error("PeopleUpdatedAt should be set after UpdatePeople")
	}
}

func TestStore_StalenessWithMissingSection(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Update only projects
	projects := []CachedProject{{ID: 1, Name: "Project"}}
	if err := store.UpdateProjects(projects); err != nil {
		t.Fatal(err)
	}

	// Cache should be considered stale because PeopleUpdatedAt is zero
	if !store.IsStale(time.Hour) {
		t.Error("Cache with missing people section should be stale")
	}

	// Now add people
	people := []CachedPerson{{ID: 100, Name: "Person"}}
	if err := store.UpdatePeople(people); err != nil {
		t.Fatal(err)
	}

	// Cache should now be fresh
	if store.IsStale(time.Hour) {
		t.Error("Cache with both sections should not be stale")
	}
}

func TestOldestTime(t *testing.T) {
	now := time.Now()
	old := now.Add(-time.Hour)
	zero := time.Time{}

	tests := []struct {
		name     string
		a, b     time.Time
		wantZero bool
	}{
		{"both zero", zero, zero, true},
		{"a zero", zero, now, true},
		{"b zero", now, zero, true},
		{"a older", old, now, false},
		{"b older", now, old, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := oldestTime(tt.a, tt.b)
			if tt.wantZero {
				if !result.IsZero() {
					t.Errorf("expected zero time, got %v", result)
				}
			} else {
				if result.IsZero() {
					t.Error("expected non-zero time")
				}
				if !result.Equal(old) {
					t.Errorf("expected older time %v, got %v", old, result)
				}
			}
		})
	}
}

func TestStore_UpdatedAtReflectsOldestSection(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Update projects first
	projects := []CachedProject{{ID: 1, Name: "Project"}}
	if err := store.UpdateProjects(projects); err != nil {
		t.Fatal(err)
	}

	cache, _ := store.Load()
	// With only projects, UpdatedAt should be zero (people is missing)
	if !cache.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be zero when one section is missing")
	}

	// Small delay to ensure different timestamps
	time.Sleep(10 * time.Millisecond)

	// Update people
	people := []CachedPerson{{ID: 100, Name: "Person"}}
	if err := store.UpdatePeople(people); err != nil {
		t.Fatal(err)
	}

	cache, _ = store.Load()
	// Now both are populated - UpdatedAt should be the older one (projects)
	if cache.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should not be zero when both sections exist")
	}
	if !cache.UpdatedAt.Equal(cache.ProjectsUpdatedAt) {
		t.Error("UpdatedAt should equal the older timestamp (projects)")
	}
}
