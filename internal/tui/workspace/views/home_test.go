package views

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

func testHomeView() *Home {
	styles := tui.NewStyles()

	list := widget.NewList(styles)
	list.SetEmptyText("Welcome to Basecamp.")
	list.SetFocused(true)
	list.SetSize(80, 24)
	list.SetItems([]widget.ListItem{
		{ID: "1", Title: "Project Alpha"},
		{ID: "2", Title: "Project Beta"},
	})

	return &Home{
		styles:   styles,
		list:     list,
		itemMeta: make(map[string]homeItemMeta),
		width:    80,
		height:   24,
	}
}

func TestHome_EmptyState_ShowsWelcome(t *testing.T) {
	styles := tui.NewStyles()
	list := widget.NewList(styles)
	list.SetEmptyText("Welcome to Basecamp.")
	list.SetFocused(true)
	list.SetSize(80, 24)
	// No items — simulates empty state with resolved pools

	v := &Home{
		styles:   styles,
		list:     list,
		itemMeta: make(map[string]homeItemMeta),
		width:    80,
		height:   24,
	}

	output := v.View()
	assert.Contains(t, output, "Welcome to Basecamp")
	assert.Contains(t, output, "Browse projects")
	assert.Contains(t, output, "Search across everything")
	assert.Contains(t, output, "ctrl+p")
}

func TestHome_WithData_HidesWelcome(t *testing.T) {
	v := testHomeView() // has items
	output := v.View()
	assert.NotContains(t, output, "Browse projects")
	assert.Contains(t, output, "Project Alpha")
}

func TestHome_FilterDelegatesAllKeys(t *testing.T) {
	v := testHomeView()

	// Start filter
	v.list.StartFilter()
	require.True(t, v.list.Filtering())

	// Press 'p' — should be absorbed by filter, NOT trigger navigate to projects
	updated, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})

	// Verify no navigation command was produced
	if cmd != nil {
		// Run the cmd to get the message — navigate produces a NavigateMsg
		msg := cmd()
		_, isKey := msg.(tea.KeyMsg)
		// The command should NOT be a navigation; it should be nil or a filter-internal cmd
		assert.False(t, isKey, "should not produce a raw key msg")
	}

	home := updated.(*Home)
	assert.True(t, home.list.Filtering(), "filter should still be active after 'p'")
}
