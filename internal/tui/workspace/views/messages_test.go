package views

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

func testMessagesView() *Messages {
	styles := tui.NewStyles()

	list := widget.NewList(styles)
	list.SetEmptyText("No messages.")
	list.SetFocused(true)
	list.SetSize(80, 24)
	list.SetItems([]widget.ListItem{
		{ID: "1", Title: "Welcome"},
		{ID: "2", Title: "Updates"},
	})

	preview := widget.NewPreview(styles)
	split := widget.NewSplitPane(styles, 0.35)
	split.SetSize(120, 30)

	return &Messages{
		styles:       styles,
		list:         list,
		preview:      preview,
		split:        split,
		messages:     []workspace.MessageInfo{{ID: 1, Subject: "Welcome"}, {ID: 2, Subject: "Updates"}},
		cachedDetail: make(map[int64]*workspace.MessageDetailLoadedMsg),
		width:        120,
		height:       30,
	}
}

func TestMessages_FilterDelegatesAllKeys(t *testing.T) {
	v := testMessagesView()

	v.list.StartFilter()
	require.True(t, v.list.Filtering())

	// 'b', 'B', 'n' should all be absorbed by the filter
	for _, r := range []rune{'b', 'B', 'n'} {
		v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		assert.True(t, v.list.Filtering(), "filter should still be active after %q", string(r))
	}
}
