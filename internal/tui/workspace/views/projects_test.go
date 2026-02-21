package views

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// testProjectsView builds a Projects view with pre-populated data for unit
// testing focus management and key routing. Session is nil — tests that
// trigger navigation (openTool, navigateToTool) are not covered here.
func testProjectsView(projects []data.ProjectInfo) *Projects {
	styles := tui.NewStyles()

	list := widget.NewList(styles)
	list.SetEmptyText("No projects")
	list.SetFocused(true)
	list.SetSize(40, 20)

	toolList := widget.NewList(styles)
	toolList.SetEmptyText("No tools")
	toolList.SetFocused(false)
	toolList.SetSize(40, 20)

	split := widget.NewSplitPane(styles, 0.35)
	split.SetSize(120, 30)

	pool := testPool("projects", projects, true)

	v := &Projects{
		pool:            pool,
		styles:          styles,
		list:            list,
		toolList:        toolList,
		split:           split,
		dockKeys:        defaultDockKeyMap(),
		projects:        projects,
		projectAccounts: make(map[string]string),
		width:           120,
		height:          30,
	}

	for _, p := range projects {
		v.projectAccounts[fmt.Sprintf("%d", p.ID)] = p.AccountID
	}

	// Build project list items and set initial selection
	v.syncProjectList()
	v.updateSelectedProject()

	return v
}

func sampleProjects() []data.ProjectInfo {
	return []data.ProjectInfo{
		{
			ID: 1, Name: "Alpha", AccountID: "a1", AccountName: "Acme",
			Dock: []data.DockToolInfo{
				{ID: 10, Name: "todoset", Title: "Todos", Enabled: true},
				{ID: 11, Name: "chat", Title: "Campfire", Enabled: true},
				{ID: 12, Name: "kanban_board", Title: "Card Table", Enabled: true},
			},
		},
		{
			ID: 2, Name: "Beta", AccountID: "a1", AccountName: "Acme",
			Dock: []data.DockToolInfo{
				{ID: 20, Name: "message_board", Title: "Messages", Enabled: true},
				{ID: 21, Name: "schedule", Title: "Schedule", Enabled: true},
			},
		},
	}
}

// --- Focus management ---

func TestProjects_EnterShiftsFocusRight(t *testing.T) {
	v := testProjectsView(sampleProjects())

	assert.False(t, v.focusRight)
	assert.True(t, v.list.Filtering() == false)

	// Press Enter to enter dock
	v.Update(tea.KeyMsg{Type: tea.KeyEnter})

	assert.True(t, v.focusRight)
	assert.NotNil(t, v.selectedProject)
	assert.Equal(t, int64(1), v.selectedProject.ID)
}

func TestProjects_LKeyShiftsFocusRight(t *testing.T) {
	v := testProjectsView(sampleProjects())

	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})

	assert.True(t, v.focusRight)
}

func TestProjects_EscReturnsFocusLeft(t *testing.T) {
	v := testProjectsView(sampleProjects())

	// Enter dock
	v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.True(t, v.focusRight)

	// Esc exits dock
	v.Update(tea.KeyMsg{Type: tea.KeyEsc})

	assert.False(t, v.focusRight)
}

func TestProjects_HKeyReturnsFocusLeft(t *testing.T) {
	v := testProjectsView(sampleProjects())

	v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.True(t, v.focusRight)

	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})

	assert.False(t, v.focusRight)
}

// --- Modal semantics ---

func TestProjects_IsModal_ReflectsFocusRight(t *testing.T) {
	v := testProjectsView(sampleProjects())

	assert.False(t, v.IsModal(), "not modal when left panel focused")

	v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	assert.True(t, v.IsModal(), "modal when right panel focused")

	v.Update(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, v.IsModal(), "not modal after returning left")
}

// --- Tool list population ---

func TestProjects_ToolListPopulatesOnEnter(t *testing.T) {
	v := testProjectsView(sampleProjects())

	v.Update(tea.KeyMsg{Type: tea.KeyEnter})

	items := v.toolList.Items()
	require.Len(t, items, 3)
	assert.Equal(t, "Todos", items[0].Title)
	assert.Equal(t, "t", items[0].Extra)
	assert.Equal(t, "Campfire", items[1].Title)
	assert.Equal(t, "c", items[1].Extra)
	assert.Equal(t, "Card Table", items[2].Title)
	assert.Equal(t, "k", items[2].Extra)
}

func TestProjects_ToolListUpdatesOnCursorMove(t *testing.T) {
	v := testProjectsView(sampleProjects())

	// Initially on project Alpha
	require.NotNil(t, v.selectedProject)
	assert.Equal(t, "Alpha", v.selectedProject.Name)
	assert.Len(t, v.toolList.Items(), 3)

	// Move cursor down to Beta
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})

	require.NotNil(t, v.selectedProject)
	assert.Equal(t, "Beta", v.selectedProject.Name)
	assert.Len(t, v.toolList.Items(), 2)
}

// --- Filter interaction ---

func TestProjects_LeaveDockClearsToolFilter(t *testing.T) {
	v := testProjectsView(sampleProjects())

	// Enter dock and start filter
	v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v.toolList.StartFilter()
	require.True(t, v.toolList.Filtering())

	// Leave dock — filter must be cleared
	v.leaveDock()

	assert.False(t, v.toolList.Filtering(), "tool filter should be cleared on leaveDock")
	assert.False(t, v.InputActive(), "InputActive should be false after leaveDock")
}

func TestProjects_ToolFilterDelegatesAllKeys(t *testing.T) {
	v := testProjectsView(sampleProjects())

	// Enter dock
	v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.True(t, v.focusRight)

	// Start filter on tool list
	v.toolList.StartFilter()
	require.True(t, v.toolList.Filtering())

	// Press 't' — should be absorbed by filter (typing), NOT trigger todos hotkey
	v.handleToolKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})

	// Still filtering, still in dock (hotkey was NOT triggered)
	assert.True(t, v.focusRight, "should still be in dock after typing in filter")
	assert.True(t, v.toolList.Filtering(), "filter should still be active")
}

func TestProjects_ProjectFilterDelegatesAllKeys(t *testing.T) {
	v := testProjectsView(sampleProjects())

	// Start filter on project list (left panel)
	v.list.StartFilter()
	require.True(t, v.list.Filtering())

	// Press 'b' — should be absorbed by filter, NOT trigger bookmark
	v.handleProjectKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	assert.True(t, v.list.Filtering(), "filter should still be active after 'b'")
	assert.False(t, v.focusRight, "should not have entered dock")

	// Press 'l' — should be absorbed by filter, NOT enter dock
	v.handleProjectKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	assert.True(t, v.list.Filtering(), "filter should still be active after 'l'")
	assert.False(t, v.focusRight, "should not have entered dock")
}

func TestProjects_InputActiveReflectsBothLists(t *testing.T) {
	v := testProjectsView(sampleProjects())

	assert.False(t, v.InputActive())

	// Left list filtering
	v.list.StartFilter()
	assert.True(t, v.InputActive(), "InputActive when left list filtering")
	v.list.StopFilter()

	// Right list filtering
	v.toolList.StartFilter()
	assert.True(t, v.InputActive(), "InputActive when right list filtering")
	v.toolList.StopFilter()

	assert.False(t, v.InputActive())
}

func TestProjects_StartFilterRoutesToActivePanel(t *testing.T) {
	v := testProjectsView(sampleProjects())

	// Left panel focused: StartFilter goes to project list
	v.StartFilter()
	assert.True(t, v.list.Filtering())
	assert.False(t, v.toolList.Filtering())
	v.list.StopFilter()

	// Enter dock: StartFilter goes to tool list
	v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v.StartFilter()
	assert.False(t, v.list.Filtering())
	assert.True(t, v.toolList.Filtering())
}

// --- Pool refresh ---

func TestProjects_AfterPoolUpdate_RebindsSelectedProject(t *testing.T) {
	v := testProjectsView(sampleProjects())

	// Enter dock on Alpha
	v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, int64(1), v.selectedProject.ID)
	require.Len(t, v.toolList.Items(), 3)

	// Simulate pool refresh: Alpha gets a new tool
	updatedProjects := []data.ProjectInfo{
		{
			ID: 1, Name: "Alpha", AccountID: "a1", AccountName: "Acme",
			Dock: []data.DockToolInfo{
				{ID: 10, Name: "todoset", Title: "Todos", Enabled: true},
				{ID: 11, Name: "chat", Title: "Campfire", Enabled: true},
				{ID: 12, Name: "kanban_board", Title: "Card Table", Enabled: true},
				{ID: 13, Name: "vault", Title: "Docs & Files", Enabled: true},
			},
		},
		{ID: 2, Name: "Beta", AccountID: "a1"},
	}
	v.projects = updatedProjects
	v.syncProjectList()
	v.afterPoolUpdate()

	// selectedProject should be rebound to fresh slice with 4 tools
	assert.Equal(t, int64(1), v.selectedProject.ID)
	assert.Len(t, v.toolList.Items(), 4, "tool list should reflect refreshed data")
}

func TestProjects_AfterPoolUpdate_ProjectDisappears(t *testing.T) {
	v := testProjectsView(sampleProjects())

	// Enter dock on Alpha
	v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.True(t, v.focusRight)

	// Simulate pool refresh: Alpha removed
	v.projects = []data.ProjectInfo{
		{ID: 2, Name: "Beta", AccountID: "a1"},
	}
	v.syncProjectList()
	v.afterPoolUpdate()

	assert.False(t, v.focusRight, "should exit dock when project disappears")
	assert.Nil(t, v.selectedProject)
}

// --- ShortHelp context ---

func TestProjects_ShortHelp_LeftPanel(t *testing.T) {
	v := testProjectsView(sampleProjects())

	hints := v.ShortHelp()
	require.Len(t, hints, 3)
	assert.Equal(t, "navigate", hints[0].Help().Desc)
	assert.Equal(t, "open", hints[1].Help().Desc)
	assert.Equal(t, "bookmark", hints[2].Help().Desc)
}

func TestProjects_ShortHelp_RightPanel(t *testing.T) {
	v := testProjectsView(sampleProjects())
	v.Update(tea.KeyMsg{Type: tea.KeyEnter})

	hints := v.ShortHelp()
	require.Len(t, hints, 7)
	assert.Equal(t, "open", hints[0].Help().Desc)
	assert.Equal(t, "back", hints[1].Help().Desc)
	assert.Equal(t, "todos", hints[2].Help().Desc)
	assert.Equal(t, "campfire", hints[3].Help().Desc)
	assert.Equal(t, "activity", hints[6].Help().Desc)
}

// --- Collapsed mode ---

func TestProjects_CollapsedMode_FocusRightShowsToolList(t *testing.T) {
	v := testProjectsView(sampleProjects())
	// Make it narrow enough to collapse
	v.width = 60
	v.split.SetSize(60, 20)

	require.True(t, v.split.IsCollapsed())

	// Enter dock
	v.enterDock("1")
	require.True(t, v.focusRight)

	// View should render without panic and contain the tool list
	view := v.View()
	assert.NotEmpty(t, view)
}

// --- Activity hotkey ---

func TestProjects_ActivityHotkey_NilGuard(t *testing.T) {
	v := testProjectsView(sampleProjects())

	// Enter dock, then leave (so selectedProject is cleared via leaveDock)
	v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v.leaveDock()
	v.selectedProject = nil

	// focusRight = true but selectedProject = nil — should not panic
	v.focusRight = true
	cmd := v.handleToolKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	assert.Nil(t, cmd, "pressing 'a' with nil selectedProject should return nil")
}

// --- testPool helper ---

func testProjectPool(projects []data.ProjectInfo) *data.Pool[[]data.ProjectInfo] {
	return testPool("projects", projects, true)
}

// --- Utility ---

func TestToolHotkey(t *testing.T) {
	assert.Equal(t, "t", toolHotkey("todoset"))
	assert.Equal(t, "c", toolHotkey("chat"))
	assert.Equal(t, "m", toolHotkey("message_board"))
	assert.Equal(t, "k", toolHotkey("kanban_board"))
	assert.Equal(t, "s", toolHotkey("schedule"))
	assert.Equal(t, "", toolHotkey("vault"))
}

func TestToolNameToView(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"todoset", true},
		{"chat", true},
		{"message_board", true},
		{"kanban_board", true},
		{"schedule", true},
		{"vault", true},
		{"questionnaire", true},
		{"inbox", true},
		{"unknown_tool", false},
	}
	for _, tt := range tests {
		_, ok := toolNameToView(tt.name)
		assert.Equal(t, tt.ok, ok, "toolNameToView(%q)", tt.name)
	}
}
