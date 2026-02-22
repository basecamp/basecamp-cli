package recents

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStore_AddAndGet(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Add an item
	item := Item{
		ID:        "123",
		Title:     "Test Project",
		Type:      TypeProject,
		AccountID: "456",
	}
	store.Add(item)

	// Get items
	items := store.Get(TypeProject, "", "")
	require.Len(t, items, 1)
	assert.Equal(t, "123", items[0].ID)
	assert.Equal(t, "Test Project", items[0].Title)
	assert.Equal(t, "456", items[0].AccountID)
	assert.False(t, items[0].UsedAt.IsZero())
}

func TestStore_AddUpdatesExisting(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Add same item twice with different titles
	store.Add(Item{ID: "1", Title: "First", Type: TypeProject})
	time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	store.Add(Item{ID: "1", Title: "Updated", Type: TypeProject})

	items := store.Get(TypeProject, "", "")
	require.Len(t, items, 1, "should deduplicate by ID")
	assert.Equal(t, "Updated", items[0].Title)
}

func TestStore_MaintainsOrder(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Add items in order
	store.Add(Item{ID: "1", Title: "First", Type: TypeProject})
	store.Add(Item{ID: "2", Title: "Second", Type: TypeProject})
	store.Add(Item{ID: "3", Title: "Third", Type: TypeProject})

	items := store.Get(TypeProject, "", "")
	require.Len(t, items, 3)
	// Most recent should be first
	assert.Equal(t, "3", items[0].ID)
	assert.Equal(t, "2", items[1].ID)
	assert.Equal(t, "1", items[2].ID)
}

func TestStore_ReaddMovesToFront(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	store.Add(Item{ID: "1", Title: "First", Type: TypeProject})
	store.Add(Item{ID: "2", Title: "Second", Type: TypeProject})
	store.Add(Item{ID: "1", Title: "First Again", Type: TypeProject}) // Re-add first

	items := store.Get(TypeProject, "", "")
	require.Len(t, items, 2)
	assert.Equal(t, "1", items[0].ID, "re-added item should be first")
	assert.Equal(t, "First Again", items[0].Title)
	assert.Equal(t, "2", items[1].ID)
}

func TestStore_FilterByAccount(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	store.Add(Item{ID: "1", Title: "Acct A", Type: TypeProject, AccountID: "A"})
	store.Add(Item{ID: "2", Title: "Acct B", Type: TypeProject, AccountID: "B"})
	store.Add(Item{ID: "3", Title: "Acct A", Type: TypeProject, AccountID: "A"})

	items := store.Get(TypeProject, "A", "")
	require.Len(t, items, 2)
	for _, item := range items {
		assert.Equal(t, "A", item.AccountID)
	}
}

func TestStore_FilterByProject(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	store.Add(Item{ID: "1", Title: "Proj X", Type: TypeTodolist, ProjectID: "X"})
	store.Add(Item{ID: "2", Title: "Proj Y", Type: TypeTodolist, ProjectID: "Y"})
	store.Add(Item{ID: "3", Title: "Proj X", Type: TypeTodolist, ProjectID: "X"})

	items := store.Get(TypeTodolist, "", "X")
	require.Len(t, items, 2)
	for _, item := range items {
		assert.Equal(t, "X", item.ProjectID)
	}
}

func TestStore_MaxItems(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Add more than max items (default is 10)
	for i := range 15 {
		store.Add(Item{ID: string(rune('A' + i)), Title: "Item", Type: TypeProject})
	}

	items := store.Get(TypeProject, "", "")
	assert.Len(t, items, 10, "should cap at maxItems")
	// Most recent should be first
	assert.Equal(t, "O", items[0].ID) // 15th item (0-indexed: 14, 'A'+14='O')
}

func TestStore_SeparateTypes(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	store.Add(Item{ID: "1", Title: "Project", Type: TypeProject})
	store.Add(Item{ID: "2", Title: "Todolist", Type: TypeTodolist})
	store.Add(Item{ID: "3", Title: "Person", Type: TypePerson})

	assert.Len(t, store.Get(TypeProject, "", ""), 1)
	assert.Len(t, store.Get(TypeTodolist, "", ""), 1)
	assert.Len(t, store.Get(TypePerson, "", ""), 1)
	assert.Len(t, store.Get(TypeRecording, "", ""), 0)
}

func TestStore_Clear(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	store.Add(Item{ID: "1", Type: TypeProject})
	store.Add(Item{ID: "2", Type: TypeTodolist})

	store.Clear(TypeProject)

	assert.Len(t, store.Get(TypeProject, "", ""), 0)
	assert.Len(t, store.Get(TypeTodolist, "", ""), 1, "other types unaffected")
}

func TestStore_ClearAll(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	store.Add(Item{ID: "1", Type: TypeProject})
	store.Add(Item{ID: "2", Type: TypeTodolist})

	store.ClearAll()

	assert.Len(t, store.Get(TypeProject, "", ""), 0)
	assert.Len(t, store.Get(TypeTodolist, "", ""), 0)
}

func TestStore_Persistence(t *testing.T) {
	tmpDir := t.TempDir()

	// Create store and add items
	store1 := NewStore(tmpDir)
	store1.Add(Item{ID: "1", Title: "Persisted", Type: TypeProject})

	// Create new store from same directory
	store2 := NewStore(tmpDir)
	items := store2.Get(TypeProject, "", "")

	require.Len(t, items, 1)
	assert.Equal(t, "1", items[0].ID)
	assert.Equal(t, "Persisted", items[0].Title)
}

func TestStore_PersistenceFileLocation(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)
	store.Add(Item{ID: "1", Type: TypeProject})

	// Verify file was created
	expectedPath := filepath.Join(tmpDir, "recents.json")
	_, err := os.Stat(expectedPath)
	assert.NoError(t, err, "recents.json should exist")
}

func TestStore_GetReturnsCopy(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)
	store.Add(Item{ID: "1", Title: "Original", Type: TypeProject})

	// Get items and modify the returned slice
	items := store.Get(TypeProject, "", "")
	items[0].Title = "Modified"

	// Get again - should still be original
	items2 := store.Get(TypeProject, "", "")
	assert.Equal(t, "Original", items2[0].Title, "Get should return a copy")
}

func TestStore_LastError(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Initially no error
	assert.Nil(t, store.LastError())

	// Successful add should keep lastError nil
	store.Add(Item{ID: "1", Type: TypeProject})
	assert.Nil(t, store.LastError())
}

func TestStore_HandlesEmptyLoad(t *testing.T) {
	tmpDir := t.TempDir()

	// Create store with no existing file
	store := NewStore(tmpDir)
	items := store.Get(TypeProject, "", "")
	assert.Empty(t, items)
}

func TestStore_HandlesCorruptFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Write corrupt JSON
	corruptPath := filepath.Join(tmpDir, "recents.json")
	err := os.WriteFile(corruptPath, []byte("not valid json"), 0600)
	require.NoError(t, err)

	// Store should handle gracefully
	store := NewStore(tmpDir)
	items := store.Get(TypeProject, "", "")
	assert.Empty(t, items, "should start fresh on corrupt file")
}
