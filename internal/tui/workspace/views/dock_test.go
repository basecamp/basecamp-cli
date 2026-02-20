package views

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

func testDockView() *Dock {
	styles := tui.NewStyles()

	list := widget.NewList(styles)
	list.SetEmptyText("No tools.")
	list.SetFocused(true)
	list.SetSize(80, 24)
	list.SetItems([]widget.ListItem{
		{ID: "10", Title: "Todos", Extra: "t"},
		{ID: "11", Title: "Campfire", Extra: "c"},
		{ID: "12", Title: "Messages", Extra: "m"},
	})

	return &Dock{
		styles: styles,
		list:   list,
		keys:   defaultDockKeyMap(),
		width:  80,
		height: 24,
	}
}

func TestDock_FilterDelegatesAllKeys(t *testing.T) {
	v := testDockView()

	v.list.StartFilter()
	require.True(t, v.list.Filtering())

	// Each hotkey letter should be absorbed by the filter, not trigger navigation
	for _, r := range []rune{'t', 'c', 'm', 'k', 's'} {
		v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		assert.True(t, v.list.Filtering(), "filter should still be active after %q", string(r))
	}
}
