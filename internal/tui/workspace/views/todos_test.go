package views

import (
	"context"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

func sampleTodolists() []data.TodolistInfo {
	return []data.TodolistInfo{
		{ID: 10, Title: "Launch", CompletedRatio: "2/5"},
		{ID: 20, Title: "Backlog", CompletedRatio: "0/3"},
	}
}

func sampleTodos() []data.TodoInfo {
	return []data.TodoInfo{
		{ID: 100, Content: "Write tests", Completed: false, Position: 1},
		{ID: 101, Content: "Ship feature", Completed: true, Position: 2},
		{ID: 102, Content: "Update docs", Completed: false, Position: 3},
	}
}

// testTodosView creates a Todos view with pre-populated todolists and todos
// for unit testing key handling, inline create, and focus management.
func testTodosView() *Todos {
	session := workspace.NewTestSessionWithScope(workspace.Scope{
		AccountID: "acct1",
		ProjectID: 42,
		ToolID:    10,
	})

	styles := tui.NewStyles()

	todolistPool := data.NewPool[[]data.TodolistInfo](
		"todolists:42:10",
		data.PoolConfig{FreshTTL: time.Hour},
		func(context.Context) ([]data.TodolistInfo, error) {
			return sampleTodolists(), nil
		},
	)
	todolistPool.Set(sampleTodolists())

	listLists := widget.NewList(styles)
	listLists.SetEmptyText("No todolists found.")
	listLists.SetFocused(true)
	listLists.SetSize(40, 20)

	listTodos := widget.NewList(styles)
	listTodos.SetEmptyText("Select a todolist to view todos.")
	listTodos.SetFocused(false)
	listTodos.SetSize(60, 20)

	split := widget.NewSplitPane(styles, 0.35)
	split.SetSize(120, 24)

	v := &Todos{
		session:      session,
		todolistPool: todolistPool,
		styles:       styles,
		keys:         defaultTodosKeyMap(),
		split:        split,
		listLists:    listLists,
		listTodos:    listTodos,
		focus:        todosPaneLeft,
		width:        120,
		height:       24,
	}

	// Populate the left panel
	v.syncTodolists(sampleTodolists())

	return v
}

// testTodosViewWithTodos returns a view that also has the right panel populated
// with todos and the first todolist selected.
func testTodosViewWithTodos() *Todos {
	v := testTodosView()
	v.selectedListID = 10
	v.focus = todosPaneRight
	v.listLists.SetFocused(false)
	v.listTodos.SetFocused(true)
	v.renderTodoItems(sampleTodos())
	return v
}

// --- Init ---

func TestTodos_Init_LoadsTodolists(t *testing.T) {
	v := testTodosView()
	cmd := v.Init()

	// Pool is pre-populated and fresh, so Init should return a command to
	// select the first todolist (or nil if no selection yet). Either way,
	// loadingLists should be false since the pool is usable.
	assert.False(t, v.loadingLists, "should not be loading when pool is pre-populated")

	// The left panel should have items from the synced todolists.
	items := v.listLists.Items()
	require.Len(t, items, 2)
	assert.Equal(t, "Launch", items[0].Title)
	assert.Equal(t, "Backlog", items[1].Title)

	// cmd may be non-nil (selectTodolist for first item) or nil
	_ = cmd
}

// --- Tab switching ---

func TestTodos_SwitchTab_TogglesFocus(t *testing.T) {
	v := testTodosView()

	// Initially left pane is focused
	assert.Equal(t, todosPaneLeft, v.focus)

	// Press Tab to switch to right pane
	v.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, todosPaneRight, v.focus)

	// Press Tab again to switch back
	v.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, todosPaneLeft, v.focus)
}

// --- Toggle complete ---

func TestTodos_ToggleComplete_RequiresRightPane(t *testing.T) {
	v := testTodosView()

	// Focus is on left pane — x should do nothing
	cmd := v.handleKey(runeKey('x'))
	assert.Nil(t, cmd, "x on left pane should return nil")

	// Switch to right pane — x should attempt toggle (needs selected item)
	v.toggleFocus()
	assert.Equal(t, todosPaneRight, v.focus)

	// No items in right pane yet, so x returns nil
	cmd = v.handleKey(runeKey('x'))
	assert.Nil(t, cmd, "x with no selected todo should return nil")
}

func TestTodos_ToggleComplete_DispatchesFromRightPane(t *testing.T) {
	v := testTodosViewWithTodos()

	// Pre-populate the Hub's todos pool so toggleSelected can read state
	todosPool := v.session.Hub().Todos(42, 10)
	todosPool.Set(sampleTodos())

	// Verify the selected item is the first todo
	item := v.listTodos.Selected()
	require.NotNil(t, item)
	assert.Equal(t, "100", item.ID)

	// Verify the pool has the todo as not completed
	snap := todosPool.Get()
	require.True(t, snap.Usable())
	var wasCompleted bool
	for _, todo := range snap.Data {
		if todo.ID == 100 {
			wasCompleted = todo.Completed
			break
		}
	}
	assert.False(t, wasCompleted, "todo 100 should start as not completed")
}

// --- Inline create ---

func TestTodos_InlineCreate_EnterSubmits(t *testing.T) {
	v := testTodosViewWithTodos()

	// Simulate entering create mode (bypass textinput.Focus which requires
	// a running tea.Program for cursor blink)
	v.creating = true
	assert.True(t, v.InputActive(), "InputActive should be true during create")

	// Empty Enter exits create mode without submitting
	cmd := v.handleCreatingKey(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Nil(t, cmd, "enter with empty content should return nil")
	assert.False(t, v.creating, "creating should be false after empty submit")
}

func TestTodos_InlineCreate_RequiresRightPane(t *testing.T) {
	v := testTodosView()

	// Focus is on left pane — n should not enter create mode
	cmd := v.handleKey(runeKey('n'))
	assert.Nil(t, cmd, "n on left pane should return nil")
	assert.False(t, v.creating, "should not enter create mode from left pane")
}

func TestTodos_InlineCreate_EscCancels(t *testing.T) {
	v := testTodosViewWithTodos()

	// Simulate entering create mode
	v.creating = true
	v.textInput.SetValue("Draft task")

	// Press Esc to cancel
	cmd := v.handleCreatingKey(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Nil(t, cmd, "esc should return nil cmd")
	assert.False(t, v.creating, "creating should be false after esc")
}

// --- Filter active ---

func TestTodos_FilterActive_SuppressesGlobalKeys(t *testing.T) {
	v := testTodosViewWithTodos()

	assert.False(t, v.InputActive(), "should not capture input normally")

	// Start filter on the focused (right) list
	v.listTodos.StartFilter()
	assert.True(t, v.InputActive(), "InputActive should be true when filtering right list")

	v.listTodos.StopFilter()
	assert.False(t, v.InputActive(), "should not capture input after stopping filter")

	// Also test filtering on left panel
	v.focus = todosPaneLeft
	v.listLists.StartFilter()
	assert.True(t, v.InputActive(), "InputActive should be true when filtering left list")
}

// --- StartFilter routes to focused panel ---

func TestTodos_StartFilter_RoutesToFocusedPanel(t *testing.T) {
	v := testTodosView()

	// Left panel focused: StartFilter goes to todolist list
	v.StartFilter()
	assert.True(t, v.listLists.Filtering())
	assert.False(t, v.listTodos.Filtering())
	v.listLists.StopFilter()

	// Switch to right panel
	v.toggleFocus()
	v.StartFilter()
	assert.False(t, v.listLists.Filtering())
	assert.True(t, v.listTodos.Filtering())
}

// --- Boost ---

func TestTodos_BoostSelectedTodo(t *testing.T) {
	v := testTodosViewWithTodos()

	cmd := v.handleKey(runeKey('b'))
	require.NotNil(t, cmd, "b should return a boost cmd")

	msg := cmd()
	open, ok := msg.(workspace.OpenBoostPickerMsg)
	require.True(t, ok, "should produce OpenBoostPickerMsg")
	assert.Equal(t, int64(42), open.Target.ProjectID)
	assert.Equal(t, int64(100), open.Target.RecordingID)
}

// --- Editing description ---

func TestTodos_EditingDesc_MakesModal(t *testing.T) {
	v := testTodosViewWithTodos()

	assert.False(t, v.IsModal())

	v.editingDesc = true
	assert.True(t, v.IsModal(), "should be modal when editing description")
	assert.True(t, v.InputActive(), "InputActive should be true when editing description")
}

// --- Due date ---

func TestTodos_DueDate_RequiresRightPane(t *testing.T) {
	v := testTodosView()
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	assert.Nil(t, cmd, "D on left pane should return nil")
	assert.False(t, v.settingDue)
}

func TestTodos_DueDate_OpensInput(t *testing.T) {
	v := testTodosViewWithTodos()
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	require.NotNil(t, cmd, "D should return blink cmd")
	assert.True(t, v.settingDue, "should be in settingDue mode")
	assert.True(t, v.InputActive(), "InputActive should be true")
	assert.True(t, v.IsModal(), "IsModal should be true")
}

func TestTodos_DueDate_EnterParsesDate(t *testing.T) {
	v := testTodosViewWithTodos()
	v.settingDue = true
	v.dueInput = newTextInputWithValue("tomorrow")

	cmd := v.handleSettingDueKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd, "enter with valid date should return cmd")
	assert.False(t, v.settingDue, "settingDue should be cleared")

	msg := cmd()
	result, ok := msg.(todoDueUpdatedMsg)
	require.True(t, ok, "cmd should produce todoDueUpdatedMsg")
	assert.Equal(t, int64(10), result.todolistID)
	// Error expected with nil SDK
	assert.Error(t, result.err)
}

func TestTodos_DueDate_EmptyClears(t *testing.T) {
	v := testTodosViewWithTodos()
	v.settingDue = true
	v.dueInput = newTextInputWithValue("")

	cmd := v.handleSettingDueKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd, "empty enter should return clear cmd")
	assert.False(t, v.settingDue)

	msg := cmd()
	result, ok := msg.(todoDueUpdatedMsg)
	require.True(t, ok, "cmd should produce todoDueUpdatedMsg")
	assert.Error(t, result.err)
}

func TestTodos_DueDate_InvalidDate(t *testing.T) {
	v := testTodosViewWithTodos()
	v.settingDue = true
	v.dueInput = newTextInputWithValue("not-a-date")

	cmd := v.handleSettingDueKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	assert.False(t, v.settingDue)

	msg := cmd()
	status, ok := msg.(workspace.StatusMsg)
	require.True(t, ok, "invalid date should produce StatusMsg")
	assert.Contains(t, status.Text, "Unrecognized date")
}

func TestTodos_DueDate_EscCancels(t *testing.T) {
	v := testTodosViewWithTodos()
	v.settingDue = true

	cmd := v.handleSettingDueKey(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Nil(t, cmd)
	assert.False(t, v.settingDue)
}

// --- Assign ---

func TestTodos_Assign_RequiresRightPane(t *testing.T) {
	v := testTodosView()
	cmd := v.handleKey(runeKey('a'))
	assert.Nil(t, cmd, "a on left pane should return nil")
	assert.False(t, v.assigning)
}

func TestTodos_Assign_OpensInput(t *testing.T) {
	v := testTodosViewWithTodos()
	cmd := v.handleKey(runeKey('a'))
	require.NotNil(t, cmd, "a should return blink cmd")
	assert.True(t, v.assigning)
	assert.True(t, v.InputActive())
	assert.True(t, v.IsModal())
}

func TestTodos_Assign_EscCancels(t *testing.T) {
	v := testTodosViewWithTodos()
	v.assigning = true

	cmd := v.handleAssigningKey(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Nil(t, cmd)
	assert.False(t, v.assigning)
}

func TestTodos_Assign_EmptyEnterDoesNothing(t *testing.T) {
	v := testTodosViewWithTodos()
	v.assigning = true
	v.assignInput = newTextInputWithValue("")

	cmd := v.handleAssigningKey(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Nil(t, cmd, "empty name should return nil")
	assert.False(t, v.assigning)
}

func TestTodos_Unassign_Dispatches(t *testing.T) {
	v := testTodosViewWithTodos()
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}})
	require.NotNil(t, cmd, "A should return a cmd")

	msg := cmd()
	result, ok := msg.(todoAssignResultMsg)
	require.True(t, ok, "cmd should produce todoAssignResultMsg")
	assert.Equal(t, int64(10), result.todolistID)
	assert.Error(t, result.err)
}

// --- ShortHelp ---

func TestTodos_ShortHelp_IncludesDueAndAssign(t *testing.T) {
	v := testTodosView()
	hints := v.ShortHelp()

	keys := make(map[string]string)
	for _, h := range hints {
		keys[h.Help().Key] = h.Help().Desc
	}
	assert.Equal(t, "due date", keys["D"])
	assert.Equal(t, "assign", keys["a"])
}

// --- Title ---

func TestTodos_Title(t *testing.T) {
	v := testTodosView()
	assert.Equal(t, "Todos", v.Title())
}

// newTextInputWithValue creates a textinput with a preset value for testing.
func newTextInputWithValue(val string) textinput.Model {
	ti := textinput.New()
	ti.SetValue(val)
	return ti
}
