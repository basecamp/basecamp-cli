package widget

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/empty"
)

func testList() *List {
	l := NewList(tui.NewStyles())
	l.SetSize(80, 20)
	l.SetFocused(true)
	return l
}

func sampleItems(n int) []ListItem {
	items := make([]ListItem, n)
	for i := range n {
		items[i] = ListItem{
			ID:    string(rune('a' + i)),
			Title: strings.Repeat(string(rune('A'+i)), 1),
		}
	}
	return items
}

func downKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyDown}
}

func upKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyUp}
}

func TestList_SetItems(t *testing.T) {
	l := testList()
	items := []ListItem{
		{ID: "1", Title: "Alpha"},
		{ID: "2", Title: "Beta"},
		{ID: "3", Title: "Gamma"},
	}
	l.SetItems(items)

	assert.Equal(t, 3, l.Len())

	sel := l.Selected()
	require.NotNil(t, sel)
	assert.Equal(t, "Alpha", sel.Title, "first item should be selected by default")
	assert.Equal(t, 0, l.SelectedIndex())
}

func TestList_Navigation(t *testing.T) {
	l := testList()
	l.SetItems(sampleItems(5))

	// Move down
	l.Update(downKey())
	assert.Equal(t, 1, l.SelectedIndex())

	l.Update(downKey())
	assert.Equal(t, 2, l.SelectedIndex())

	// Move up
	l.Update(upKey())
	assert.Equal(t, 1, l.SelectedIndex())

	// Up at top clamps
	l.Update(upKey())
	assert.Equal(t, 0, l.SelectedIndex())
	l.Update(upKey())
	assert.Equal(t, 0, l.SelectedIndex(), "should not go below 0")
}

func TestList_NavigationBoundsDown(t *testing.T) {
	l := testList()
	l.SetItems(sampleItems(3))

	l.Update(downKey())
	l.Update(downKey())
	assert.Equal(t, 2, l.SelectedIndex())

	// At last item, down should not advance
	l.Update(downKey())
	assert.Equal(t, 2, l.SelectedIndex(), "should not exceed item count")
}

func TestList_EmptyState(t *testing.T) {
	l := testList()
	l.SetEmptyText("Nothing here")
	l.SetItems(nil)

	view := l.View()
	assert.Contains(t, view, "Nothing here")
	assert.Nil(t, l.Selected())
}

func TestList_Loading(t *testing.T) {
	l := testList()
	l.SetLoading(true)

	view := l.View()
	assert.Contains(t, view, "Loading…")

	// SetItems clears loading
	l.SetItems(sampleItems(2))
	view = l.View()
	assert.NotContains(t, view, "Loading…")
}

func TestList_UnfocusedIgnoresKeys(t *testing.T) {
	l := testList()
	l.SetItems(sampleItems(3))
	l.SetFocused(false)

	cmd := l.Update(downKey())
	assert.Nil(t, cmd)
	assert.Equal(t, 0, l.SelectedIndex(), "unfocused list should not respond to keys")
}

func TestList_SetItemsClampsCursor(t *testing.T) {
	l := testList()
	l.SetItems(sampleItems(5))

	// Move cursor to position 4
	for range 4 {
		l.Update(downKey())
	}
	assert.Equal(t, 4, l.SelectedIndex())

	// Replace with fewer items — cursor should clamp
	l.SetItems(sampleItems(2))
	assert.Equal(t, 1, l.SelectedIndex(), "cursor should clamp to last item")
}

func TestList_HeaderSkipping(t *testing.T) {
	l := testList()
	l.SetItems([]ListItem{
		{Title: "Section A", Header: true},
		{ID: "1", Title: "Item 1"},
		{ID: "2", Title: "Item 2"},
		{Title: "Section B", Header: true},
		{ID: "3", Title: "Item 3"},
	})

	// SetItems should land on first non-header item
	sel := l.Selected()
	require.NotNil(t, sel)
	assert.Equal(t, "Item 1", sel.Title, "should skip header on SetItems")
	assert.Equal(t, 1, l.SelectedIndex())
}

func TestList_HeaderSkippingDown(t *testing.T) {
	l := testList()
	l.SetItems([]ListItem{
		{ID: "1", Title: "Item 1"},
		{ID: "2", Title: "Item 2"},
		{Title: "Section", Header: true},
		{ID: "3", Title: "Item 3"},
	})

	// Navigate down past the header
	l.Update(downKey()) // → Item 2
	assert.Equal(t, 1, l.SelectedIndex())

	l.Update(downKey()) // → should skip header (index 2) and land on Item 3 (index 3)
	assert.Equal(t, 3, l.SelectedIndex())
	assert.Equal(t, "Item 3", l.Selected().Title)
}

func TestList_HeaderSkippingUp(t *testing.T) {
	l := testList()
	l.SetItems([]ListItem{
		{ID: "1", Title: "Item 1"},
		{Title: "Section", Header: true},
		{ID: "2", Title: "Item 2"},
	})

	// Move to Item 2 (index 2)
	l.Update(downKey())
	assert.Equal(t, 2, l.SelectedIndex())

	// Move up — should skip header and land on Item 1
	l.Update(upKey())
	assert.Equal(t, 0, l.SelectedIndex())
	assert.Equal(t, "Item 1", l.Selected().Title)
}

func TestList_HeaderRendering(t *testing.T) {
	l := testList()
	l.SetItems([]ListItem{
		{Title: "My Section", Header: true},
		{ID: "1", Title: "Item 1"},
	})

	view := l.View()
	assert.Contains(t, view, "My Section", "header should be rendered")
	assert.Contains(t, view, "Item 1", "item should be rendered")
}

func TestList_AllHeaders(t *testing.T) {
	l := testList()
	l.SetItems([]ListItem{
		{Title: "Only Header", Header: true},
	})

	// Cursor should stay at 0 even though it's a header (no non-header items)
	sel := l.Selected()
	// When all items are headers, Selected returns the header item
	require.NotNil(t, sel)
	assert.True(t, sel.Header)
}

func TestList_LongTitle_Truncated(t *testing.T) {
	l := NewList(tui.NewStyles())
	l.SetSize(30, 10)
	l.SetFocused(true)
	l.SetItems([]ListItem{
		{ID: "1", Title: "This is a very long title that should be truncated"},
	})

	view := l.View()
	assert.Contains(t, view, "…")
	// The full title should NOT appear since the width is only 30
	assert.NotContains(t, view, "truncated")
}

func TestList_LongTitle_WithExtra_Truncated(t *testing.T) {
	l := NewList(tui.NewStyles())
	l.SetSize(40, 10)
	l.SetFocused(true)
	l.SetItems([]ListItem{
		{ID: "1", Title: "This is a very long title that overflows", Extra: "5 items"},
	})

	view := l.View()
	assert.Contains(t, view, "…")
	assert.Contains(t, view, "5 items", "extra should still be visible")
}

func TestList_EmptyMessage_RendersHints(t *testing.T) {
	l := testList()
	l.SetEmptyMessage(empty.Message{
		Title: "No projects found",
		Body:  "You don't have access to any Basecamp projects.",
		Hints: []string{
			"Ask your administrator",
			"Create a new project",
		},
	})
	l.SetItems(nil)

	view := l.View()
	assert.Contains(t, view, "No projects found")
	assert.Contains(t, view, "You don't have access")
	assert.Contains(t, view, "Ask your administrator")
	assert.Contains(t, view, "Create a new project")
}

func TestList_EmptyText_FallbackWhenNoMessage(t *testing.T) {
	l := testList()
	l.SetEmptyText("Custom fallback text")
	l.SetItems(nil)

	view := l.View()
	assert.Contains(t, view, "Custom fallback text")
	assert.NotContains(t, view, "No projects found")
}

func TestList_ScrollIndicator_NoOverflow(t *testing.T) {
	l := NewList(tui.NewStyles())
	l.SetSize(80, 10)
	l.SetFocused(true)
	l.SetItems(sampleItems(15))

	view := l.View()
	lines := strings.Split(view, "\n")
	assert.LessOrEqual(t, len(lines), 10, "list view should not exceed widget height")
}

func TestList_DescriptionWithExtra_BothVisible(t *testing.T) {
	l := NewList(tui.NewStyles())
	l.SetSize(80, 10)
	l.SetFocused(true)
	l.SetItems([]ListItem{
		{ID: "1", Title: "Fix login bug", Description: "The login form crashes", Extra: "Todo"},
	})

	view := l.View()
	assert.Contains(t, view, "Fix login bug")
	assert.Contains(t, view, "Todo", "Extra badge should be visible")
	assert.Contains(t, view, "login form", "Description should be visible alongside Extra")
}

func TestList_DescriptionWithExtra_NarrowWidth(t *testing.T) {
	l := NewList(tui.NewStyles())
	l.SetSize(30, 10)
	l.SetFocused(true)
	l.SetItems([]ListItem{
		{ID: "1", Title: "Short", Description: "Long description text here", Extra: "Badge"},
	})

	view := l.View()
	assert.Contains(t, view, "Short")
	assert.Contains(t, view, "Badge", "Extra should render even at narrow width")
}

func TestList_HeightPadding_FewItems(t *testing.T) {
	l := NewList(tui.NewStyles())
	l.SetSize(60, 10)
	l.SetFocused(true)
	l.SetItems(sampleItems(2)) // only 2 items in a 10-line viewport

	view := l.View()
	lines := strings.Split(view, "\n")
	assert.Equal(t, 10, len(lines), "output should fill allocated height even with few items")
}

func TestList_HeightPadding_Empty(t *testing.T) {
	l := NewList(tui.NewStyles())
	l.SetSize(60, 10)
	l.SetFocused(true)
	l.SetEmptyText("Nothing here")
	l.SetItems(nil)

	view := l.View()
	lines := strings.Split(view, "\n")
	assert.Equal(t, 10, len(lines), "empty list should fill allocated height")
	assert.Contains(t, view, "Nothing here")
}

func TestList_HeightPadding_Loading(t *testing.T) {
	l := NewList(tui.NewStyles())
	l.SetSize(60, 10)
	l.SetFocused(true)
	l.SetLoading(true)

	view := l.View()
	lines := strings.Split(view, "\n")
	assert.Equal(t, 10, len(lines), "loading state should fill allocated height")
	assert.Contains(t, view, "Loading")
}

func TestList_HeightPadding_NoMatchesFilter(t *testing.T) {
	l := NewList(tui.NewStyles())
	l.SetSize(60, 10)
	l.SetFocused(true)
	l.SetItems(sampleItems(5))
	l.StartFilter()
	// Type something that matches nothing
	for _, r := range "zzzzz" {
		l.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	view := l.View()
	lines := strings.Split(view, "\n")
	assert.Equal(t, 10, len(lines), "no-matches filter should fill allocated height")
	assert.Contains(t, view, "No matches")
}

func TestList_BoostWithExtra_Alignment(t *testing.T) {
	l := NewList(tui.NewStyles())
	l.SetSize(60, 10)
	l.SetFocused(true)
	l.SetItems([]ListItem{
		{ID: "1", Title: "Short", Boosts: 5, Extra: "3 items"},
		{ID: "2", Title: "Another short title", Boosts: 12, Extra: "today"},
	})

	view := l.View()
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		w := lipgloss.Width(line)
		assert.LessOrEqual(t, w, 60, "list line %d overflows: width %d > 60", i, w)
	}
	assert.Contains(t, view, "3 items", "Extra should be visible")
	assert.Contains(t, view, "boosts", "Boost count should be visible")
}

func TestList_BoostSingularPlural(t *testing.T) {
	l := NewList(tui.NewStyles())
	l.SetSize(60, 10)
	l.SetFocused(true)

	l.SetItems([]ListItem{
		{ID: "1", Title: "One boost item", Boosts: 1},
		{ID: "2", Title: "Many boost item", Boosts: 5},
	})

	view := l.View()
	assert.Contains(t, view, "1 boost")
	assert.NotContains(t, view, "1 boosts")
	assert.Contains(t, view, "5 boosts")
}

func TestList_BoostLabel(t *testing.T) {
	assert.Equal(t, "1 boost", boostLabel(1))
	assert.Equal(t, "2 boosts", boostLabel(2))
	assert.Equal(t, "99 boosts", boostLabel(99))
}

func TestList_LongFilter_NoOverflow(t *testing.T) {
	l := NewList(tui.NewStyles())
	l.SetSize(40, 20)
	l.SetFocused(true)
	l.SetItems(sampleItems(5))

	// Start filter and type a very long string
	l.StartFilter()
	require.True(t, l.Filtering())
	for _, r := range strings.Repeat("abcdefghij", 10) {
		l.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	view := l.View()
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		w := lipgloss.Width(line)
		assert.LessOrEqual(t, w, 40, "list line %d overflows: width %d > 40", i, w)
	}
}
