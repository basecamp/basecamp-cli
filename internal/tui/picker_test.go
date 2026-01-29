package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPickerModel_GetOriginalItem(t *testing.T) {
	t.Run("returns item from originalItems map", func(t *testing.T) {
		m := pickerModel{
			originalItems: map[string]PickerItem{
				"123": {ID: "123", Title: "Original Title", Description: "Original Desc"},
			},
			items: []PickerItem{
				{ID: "123", Title: "Modified Title", Description: "Modified Desc"},
			},
		}

		got := m.getOriginalItem("123")
		if got == nil {
			t.Fatal("getOriginalItem returned nil")
		}
		if got.Title != "Original Title" {
			t.Errorf("Title = %q, want %q", got.Title, "Original Title")
		}
		if got.Description != "Original Desc" {
			t.Errorf("Description = %q, want %q", got.Description, "Original Desc")
		}
	})

	t.Run("falls back to items list when not in map", func(t *testing.T) {
		m := pickerModel{
			originalItems: map[string]PickerItem{}, // empty map
			items: []PickerItem{
				{ID: "456", Title: "From Items", Description: "Items Desc"},
			},
		}

		got := m.getOriginalItem("456")
		if got == nil {
			t.Fatal("getOriginalItem returned nil")
		}
		if got.Title != "From Items" {
			t.Errorf("Title = %q, want %q", got.Title, "From Items")
		}
	})

	t.Run("returns nil when map is nil and item not in list", func(t *testing.T) {
		m := pickerModel{
			originalItems: nil,
			items:         []PickerItem{},
		}

		got := m.getOriginalItem("999")
		if got != nil {
			t.Errorf("getOriginalItem = %v, want nil", got)
		}
	})

	t.Run("returns nil when item not found anywhere", func(t *testing.T) {
		m := pickerModel{
			originalItems: map[string]PickerItem{
				"123": {ID: "123", Title: "Exists"},
			},
			items: []PickerItem{
				{ID: "123", Title: "Exists"},
			},
		}

		got := m.getOriginalItem("nonexistent")
		if got != nil {
			t.Errorf("getOriginalItem = %v, want nil", got)
		}
	})
}

func TestPickerModel_AsyncLoaderPopulatesOriginalItems(t *testing.T) {
	t.Run("PickerItemsLoadedMsg updates originalItems", func(t *testing.T) {
		// Start with empty model (as async loader would)
		m := pickerModel{
			loading:       true,
			originalItems: make(map[string]PickerItem),
			items:         nil,
		}

		// Simulate items loaded message
		loadedItems := []PickerItem{
			{ID: "1", Title: "Item One", Description: "Desc One"},
			{ID: "2", Title: "Item Two", Description: "Desc Two"},
		}
		msg := PickerItemsLoadedMsg{Items: loadedItems}

		// Process the message
		newModel, _ := m.Update(msg)
		updated := newModel.(pickerModel)

		// Verify originalItems was populated
		if len(updated.originalItems) != 2 {
			t.Errorf("originalItems length = %d, want 2", len(updated.originalItems))
		}

		item1 := updated.getOriginalItem("1")
		if item1 == nil {
			t.Fatal("getOriginalItem(\"1\") returned nil")
		}
		if item1.Title != "Item One" {
			t.Errorf("Item 1 Title = %q, want %q", item1.Title, "Item One")
		}

		item2 := updated.getOriginalItem("2")
		if item2 == nil {
			t.Fatal("getOriginalItem(\"2\") returned nil")
		}
		if item2.Title != "Item Two" {
			t.Errorf("Item 2 Title = %q, want %q", item2.Title, "Item Two")
		}
	})

	t.Run("PickerItemsLoadedMsg with nil originalItems creates map", func(t *testing.T) {
		m := pickerModel{
			loading:       true,
			originalItems: nil, // nil map
			items:         nil,
		}

		msg := PickerItemsLoadedMsg{Items: []PickerItem{
			{ID: "1", Title: "Test"},
		}}

		newModel, _ := m.Update(msg)
		updated := newModel.(pickerModel)

		if updated.originalItems == nil {
			t.Error("originalItems should not be nil after loading")
		}
		if len(updated.originalItems) != 1 {
			t.Errorf("originalItems length = %d, want 1", len(updated.originalItems))
		}
	})
}

func TestPickerModel_AutoSelectWithLoader(t *testing.T) {
	t.Run("auto-selects single item when autoSelectSingle is true", func(t *testing.T) {
		m := pickerModel{
			loading:          true,
			autoSelectSingle: true,
			originalItems:    make(map[string]PickerItem),
			items:            nil,
		}

		// Load exactly one item
		msg := PickerItemsLoadedMsg{Items: []PickerItem{
			{ID: "solo", Title: "Only Item", Description: "The only one"},
		}}

		newModel, cmd := m.Update(msg)
		updated := newModel.(pickerModel)

		// Should have selected the item
		if updated.selected == nil {
			t.Fatal("selected should not be nil with autoSelectSingle and 1 item")
		}
		if updated.selected.ID != "solo" {
			t.Errorf("selected.ID = %q, want %q", updated.selected.ID, "solo")
		}
		if updated.selected.Title != "Only Item" {
			t.Errorf("selected.Title = %q, want %q", updated.selected.Title, "Only Item")
		}

		// Should return quit command
		if cmd == nil {
			t.Error("cmd should not be nil (should be tea.Quit)")
		}
	})

	t.Run("does not auto-select when multiple items", func(t *testing.T) {
		m := pickerModel{
			loading:          true,
			autoSelectSingle: true,
			originalItems:    make(map[string]PickerItem),
			items:            nil,
		}

		// Load multiple items
		msg := PickerItemsLoadedMsg{Items: []PickerItem{
			{ID: "1", Title: "First"},
			{ID: "2", Title: "Second"},
		}}

		newModel, _ := m.Update(msg)
		updated := newModel.(pickerModel)

		// Should NOT have selected anything
		if updated.selected != nil {
			t.Errorf("selected should be nil with multiple items, got %v", updated.selected)
		}
	})

	t.Run("does not auto-select when autoSelectSingle is false", func(t *testing.T) {
		m := pickerModel{
			loading:          true,
			autoSelectSingle: false,
			originalItems:    make(map[string]PickerItem),
			items:            nil,
		}

		msg := PickerItemsLoadedMsg{Items: []PickerItem{
			{ID: "solo", Title: "Only Item"},
		}}

		newModel, _ := m.Update(msg)
		updated := newModel.(pickerModel)

		if updated.selected != nil {
			t.Errorf("selected should be nil when autoSelectSingle is false, got %v", updated.selected)
		}
	})
}

func TestPickerModel_RecentItemsDecoration(t *testing.T) {
	t.Run("recent items get decorated for display but original is preserved", func(t *testing.T) {
		items := []PickerItem{
			{ID: "1", Title: "Regular Item", Description: "Regular Desc"},
		}
		recentItems := []PickerItem{
			{ID: "2", Title: "Recent Item", Description: "Recent Desc"},
		}

		m := newPickerModel(items, WithRecentItems(recentItems))

		// Check that display items are decorated
		var foundRecent bool
		for _, item := range m.items {
			if item.ID == "2" {
				foundRecent = true
				if item.Title != "* Recent Item" {
					t.Errorf("Display title = %q, want %q", item.Title, "* Recent Item")
				}
				if item.Description != "(recent) Recent Desc" {
					t.Errorf("Display description = %q, want %q", item.Description, "(recent) Recent Desc")
				}
			}
		}
		if !foundRecent {
			t.Error("Recent item not found in m.items")
		}

		// But getOriginalItem should return undecorated version
		original := m.getOriginalItem("2")
		if original == nil {
			t.Fatal("getOriginalItem returned nil for recent item")
		}
		if original.Title != "Recent Item" {
			t.Errorf("Original title = %q, want %q (undecorated)", original.Title, "Recent Item")
		}
		if original.Description != "Recent Desc" {
			t.Errorf("Original description = %q, want %q (undecorated)", original.Description, "Recent Desc")
		}
	})

	t.Run("selecting recent item returns undecorated data", func(t *testing.T) {
		items := []PickerItem{
			{ID: "1", Title: "Regular", Description: "Desc"},
		}
		recentItems := []PickerItem{
			{ID: "2", Title: "Recent", Description: "Recent Desc"},
		}

		m := newPickerModel(items, WithRecentItems(recentItems))
		m.cursor = 0 // Recent items are first
		m.filtered = m.items

		// Simulate enter key press
		newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		updated := newModel.(pickerModel)

		if updated.selected == nil {
			t.Fatal("selected should not be nil")
		}
		// Should return original undecorated item
		if updated.selected.Title != "Recent" {
			t.Errorf("selected.Title = %q, want %q (undecorated)", updated.selected.Title, "Recent")
		}
		if updated.selected.Description != "Recent Desc" {
			t.Errorf("selected.Description = %q, want %q (undecorated)", updated.selected.Description, "Recent Desc")
		}
	})
}

func TestPickerModel_LoaderError(t *testing.T) {
	t.Run("loader error is captured and returned", func(t *testing.T) {
		m := pickerModel{
			loading:       true,
			originalItems: make(map[string]PickerItem),
		}

		testErr := &testError{msg: "load failed"}
		msg := PickerItemsLoadedMsg{Err: testErr}

		newModel, cmd := m.Update(msg)
		updated := newModel.(pickerModel)

		if !updated.quitting {
			t.Error("quitting should be true on error")
		}
		if updated.loadError == nil {
			t.Error("loadError should be set")
		}
		if updated.loadError.Error() != "load failed" {
			t.Errorf("loadError = %q, want %q", updated.loadError.Error(), "load failed")
		}
		if cmd == nil {
			t.Error("cmd should be tea.Quit on error")
		}
	})
}

// testError is a simple error implementation for testing
type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
