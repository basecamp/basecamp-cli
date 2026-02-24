package views

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
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
		session: workspace.NewTestSession(),
		styles:  styles,
		list:    list,
		keys:    defaultDockKeyMap(),
		width:   80,
		height:  24,
	}
}

func TestDock_FilterDelegatesAllKeys(t *testing.T) {
	v := testDockView()

	v.list.StartFilter()
	require.True(t, v.list.Filtering())

	// Each hotkey letter should be absorbed by the filter, not trigger navigation
	for _, r := range []rune{'t', 'c', 'm', 'k', 's', 'a'} {
		v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		assert.True(t, v.list.Filtering(), "filter should still be active after %q", string(r))
	}
}

func TestDock_ShortHelp_IncludesActivity(t *testing.T) {
	v := testDockView()
	hints := v.ShortHelp()

	found := false
	for _, h := range hints {
		if h.Help().Desc == "activity" {
			found = true
			break
		}
	}
	assert.True(t, found, "ShortHelp should include activity binding")
}

func TestDock_ActivityHotkey_ProducesNavigateMsg(t *testing.T) {
	v := testDockView()

	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	require.NotNil(t, cmd, "'a' key should produce a command")

	msg := cmd()
	nav, isNav := msg.(workspace.NavigateMsg)
	require.True(t, isNav, "should produce NavigateMsg")
	assert.Equal(t, workspace.ViewTimeline, nav.Target, "should navigate to ViewTimeline")
}

func TestDock_ColdLoad_RecordsRecents(t *testing.T) {
	store := recents.NewStore(t.TempDir())
	session := workspace.NewTestSessionWithRecents(store)
	session.SetScope(workspace.Scope{AccountID: "acct1", ProjectID: 99})

	styles := tui.NewStyles()
	list := widget.NewList(styles)
	list.SetSize(80, 24)

	v := &Dock{
		session: session,
		styles:  styles,
		list:    list,
		keys:    defaultDockKeyMap(),
		loading: true,
		width:   80,
		height:  24,
	}

	v.Update(workspace.DockLoadedMsg{
		Project: basecamp.Project{
			ID:   99,
			Name: "Test Project",
			Dock: []basecamp.DockItem{},
		},
	})

	items := store.Get(recents.TypeProject, "acct1", "")
	require.Len(t, items, 1, "recents should contain the loaded project")
	assert.Equal(t, "99", items[0].ID)
	assert.Equal(t, "Test Project", items[0].Title)
	assert.Equal(t, recents.TypeProject, items[0].Type)
}
