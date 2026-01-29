package completion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, store.Save(cache))

	// Verify file exists
	_, err := os.Stat(store.Path())
	assert.False(t, os.IsNotExist(err))

	// Load
	loaded, err := store.Load()
	require.NoError(t, err)

	// Verify data
	assert.Equal(t, 2, len(loaded.Projects))
	assert.Equal(t, "Project One", loaded.Projects[0].Name)
	assert.Equal(t, "hq", loaded.Projects[0].Purpose)
	assert.True(t, loaded.Projects[0].Bookmarked)

	assert.Equal(t, 2, len(loaded.People))
	assert.Equal(t, "alice@example.com", loaded.People[0].EmailAddress)

	// Verify metadata was set
	assert.Equal(t, CacheVersion, loaded.Version)
	assert.False(t, loaded.UpdatedAt.IsZero())
}

func TestStore_LoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Load from non-existent file
	cache, err := store.Load()
	require.NoError(t, err)

	// Should return empty cache
	assert.Equal(t, 0, len(cache.Projects))
	assert.Equal(t, 0, len(cache.People))
	assert.Equal(t, CacheVersion, cache.Version)
}

func TestStore_LoadCorruptedFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Write corrupted JSON
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(store.Path(), []byte("not valid json{"), 0600))

	// Load should succeed with empty cache
	cache, err := store.Load()
	require.NoError(t, err)

	assert.Equal(t, 0, len(cache.Projects))
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
	require.NoError(t, store.Save(initial))

	// Update just projects
	projects := []CachedProject{
		{ID: 1, Name: "New Project"},
	}
	require.NoError(t, store.UpdateProjects(projects))

	// Verify both are present
	loaded, err := store.Load()
	require.NoError(t, err)

	assert.Equal(t, 1, len(loaded.Projects))
	assert.Equal(t, 1, len(loaded.People))
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
	require.NoError(t, store.Save(initial))

	// Update just people
	people := []CachedPerson{
		{ID: 100, Name: "New Person"},
	}
	require.NoError(t, store.UpdatePeople(people))

	// Verify both are present
	loaded, err := store.Load()
	require.NoError(t, err)

	assert.Equal(t, 1, len(loaded.People))
	assert.Equal(t, 1, len(loaded.Projects))
}

func TestStore_IsStale(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Empty cache is stale
	assert.True(t, store.IsStale(time.Hour))

	// Save fresh cache
	require.NoError(t, store.Save(&Cache{Projects: []CachedProject{{ID: 1, Name: "Test"}}}))

	// Fresh cache is not stale
	assert.False(t, store.IsStale(time.Hour))

	// Fresh cache with very short maxAge is stale
	assert.True(t, store.IsStale(time.Nanosecond))
}

func TestStore_Clear(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Save something
	require.NoError(t, store.Save(&Cache{Projects: []CachedProject{{ID: 1, Name: "Test"}}}))

	// Verify it exists
	_, err := os.Stat(store.Path())
	assert.False(t, os.IsNotExist(err))

	// Clear
	require.NoError(t, store.Clear())

	// Verify it's gone
	_, err = os.Stat(store.Path())
	assert.True(t, os.IsNotExist(err))

	// Clearing again should not error
	assert.NoError(t, store.Clear())
}

func TestStore_Projects(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Empty cache returns nil
	assert.Nil(t, store.Projects())

	// Save and retrieve
	expected := []CachedProject{{ID: 1, Name: "Test"}}
	require.NoError(t, store.Save(&Cache{Projects: expected}))

	projects := store.Projects()
	assert.Equal(t, 1, len(projects))
}

func TestStore_People(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Empty cache returns nil
	assert.Nil(t, store.People())

	// Save and retrieve
	expected := []CachedPerson{{ID: 100, Name: "Alice"}}
	require.NoError(t, store.Save(&Cache{People: expected}))

	people := store.People()
	assert.Equal(t, 1, len(people))
}

func TestStore_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Save should write atomically
	cache := &Cache{Projects: []CachedProject{{ID: 1, Name: "Test"}}}
	require.NoError(t, store.Save(cache))

	// Temp file should not exist
	tmpPath := store.Path() + ".tmp"
	_, err := os.Stat(tmpPath)
	assert.True(t, os.IsNotExist(err))
}

func TestStore_Dir(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	assert.Equal(t, dir, store.Dir())
}

func TestStore_Path(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	expected := filepath.Join(dir, CacheFileName)
	assert.Equal(t, expected, store.Path())
}

func TestNewStore_DefaultDir(t *testing.T) {
	store := NewStore("")
	assert.NotEqual(t, "", store.Dir())
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
	require.NoError(t, err)

	// Unmarshal to map to check field names
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))

	// Check top-level fields
	_, ok := m["projects"]
	assert.True(t, ok)
	_, ok = m["people"]
	assert.True(t, ok)
	_, ok = m["updated_at"]
	assert.True(t, ok)
	_, ok = m["version"]
	assert.True(t, ok)
}

func TestStore_PerSectionTimestamps(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// UpdateProjects sets ProjectsUpdatedAt but not PeopleUpdatedAt
	projects := []CachedProject{{ID: 1, Name: "Project"}}
	require.NoError(t, store.UpdateProjects(projects))

	cache, err := store.Load()
	require.NoError(t, err)

	assert.False(t, cache.ProjectsUpdatedAt.IsZero())
	assert.True(t, cache.PeopleUpdatedAt.IsZero())

	// UpdatePeople sets PeopleUpdatedAt, preserves ProjectsUpdatedAt
	people := []CachedPerson{{ID: 100, Name: "Person"}}
	require.NoError(t, store.UpdatePeople(people))

	cache, err = store.Load()
	require.NoError(t, err)

	assert.False(t, cache.ProjectsUpdatedAt.IsZero())
	assert.False(t, cache.PeopleUpdatedAt.IsZero())
}

func TestStore_StalenessWithMissingSection(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Update only projects
	projects := []CachedProject{{ID: 1, Name: "Project"}}
	require.NoError(t, store.UpdateProjects(projects))

	// Cache should be considered stale because PeopleUpdatedAt is zero
	assert.True(t, store.IsStale(time.Hour))

	// Now add people
	people := []CachedPerson{{ID: 100, Name: "Person"}}
	require.NoError(t, store.UpdatePeople(people))

	// Cache should now be fresh
	assert.False(t, store.IsStale(time.Hour))
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
				assert.True(t, result.IsZero())
			} else {
				assert.False(t, result.IsZero())
				assert.True(t, result.Equal(old))
			}
		})
	}
}

func TestStore_UpdatedAtReflectsOldestSection(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Update projects first
	projects := []CachedProject{{ID: 1, Name: "Project"}}
	require.NoError(t, store.UpdateProjects(projects))

	cache, _ := store.Load()
	// With only projects, UpdatedAt should be zero (people is missing)
	assert.True(t, cache.UpdatedAt.IsZero())

	// Small delay to ensure different timestamps
	time.Sleep(10 * time.Millisecond)

	// Update people
	people := []CachedPerson{{ID: 100, Name: "Person"}}
	require.NoError(t, store.UpdatePeople(people))

	cache, _ = store.Load()
	// Now both are populated - UpdatedAt should be the older one (projects)
	assert.False(t, cache.UpdatedAt.IsZero())
	assert.True(t, cache.UpdatedAt.Equal(cache.ProjectsUpdatedAt))
}

func TestStore_UpdateAccounts(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Save initial cache with projects and people
	initial := &Cache{
		Projects: []CachedProject{{ID: 1, Name: "Project"}},
		People:   []CachedPerson{{ID: 100, Name: "Person"}},
	}
	require.NoError(t, store.Save(initial))

	// Update just accounts
	accounts := []CachedAccount{
		{ID: 1234567, Name: "Acme Corp"},
		{ID: 9876543, Name: "Beta Inc"},
	}
	require.NoError(t, store.UpdateAccounts(accounts))

	// Verify all sections are present
	loaded, err := store.Load()
	require.NoError(t, err)

	assert.Equal(t, 2, len(loaded.Accounts))
	assert.Equal(t, "Acme Corp", loaded.Accounts[0].Name)
	assert.Equal(t, 1, len(loaded.Projects))
	assert.Equal(t, 1, len(loaded.People))
	assert.False(t, loaded.AccountsUpdatedAt.IsZero())
}

func TestStore_Accounts(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Empty cache returns nil
	assert.Nil(t, store.Accounts())

	// Update and retrieve
	expected := []CachedAccount{{ID: 1234567, Name: "Acme Corp"}}
	require.NoError(t, store.UpdateAccounts(expected))

	accounts := store.Accounts()
	assert.Equal(t, 1, len(accounts))
	assert.Equal(t, int64(1234567), accounts[0].ID)
}

func TestStore_AccountsPreservedOnOtherUpdates(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Add accounts first
	accounts := []CachedAccount{{ID: 1234567, Name: "Acme Corp"}}
	require.NoError(t, store.UpdateAccounts(accounts))

	// Update projects
	require.NoError(t, store.UpdateProjects([]CachedProject{{ID: 1, Name: "Project"}}))

	// Accounts should still be there
	loaded, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, 1, len(loaded.Accounts))

	// Update people
	require.NoError(t, store.UpdatePeople([]CachedPerson{{ID: 100, Name: "Person"}}))

	// Accounts should still be there
	loaded, err = store.Load()
	require.NoError(t, err)
	assert.Equal(t, 1, len(loaded.Accounts))
}
