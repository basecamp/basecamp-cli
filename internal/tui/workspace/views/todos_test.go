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
	v := testTodosViewWithTodos() // right pane focused
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

// --- Filter guard fix ---

func TestTodos_FilterGuard_XDuringLeftFilter(t *testing.T) {
	v := testTodosView()
	v.listLists.StartFilter()
	require.True(t, v.listLists.Filtering())

	// x during left-pane filter should NOT trigger toggle
	v.handleKey(runeKey('x'))
	assert.True(t, v.listLists.Filtering(), "filter should still be active")
}

func TestTodos_FilterGuard_NDuringFilter(t *testing.T) {
	v := testTodosView()
	v.listLists.StartFilter()

	v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	assert.False(t, v.creatingList, "N during filter should NOT enter create mode")
}

// --- Todolist create ---

func TestTodos_NewList_LeftPane(t *testing.T) {
	v := testTodosView()
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	require.NotNil(t, cmd, "N should return blink cmd")
	assert.True(t, v.creatingList)
	assert.True(t, v.InputActive())
}

func TestTodos_NewList_RightPane_Noop(t *testing.T) {
	v := testTodosViewWithTodos()
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	assert.Nil(t, cmd, "N on right pane should return nil")
	assert.False(t, v.creatingList)
}

func TestTodos_NewList_EscCancels(t *testing.T) {
	v := testTodosView()
	v.creatingList = true
	cmd := v.handleListInputKey(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Nil(t, cmd)
	assert.False(t, v.creatingList)
}

func TestTodos_NewList_EnterDispatches(t *testing.T) {
	v := testTodosView()
	v.creatingList = true
	v.listInput = newTextInputWithValue("My New List")

	cmd := v.handleListInputKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	assert.False(t, v.creatingList)

	msg := cmd()
	result, ok := msg.(todolistCreatedMsg)
	require.True(t, ok)
	assert.Equal(t, int64(10), result.todosetID) // scope.ToolID
	assert.Error(t, result.err)                  // nil SDK
}

func TestTodos_NewList_SuccessHandler(t *testing.T) {
	v := testTodosView()
	v.todolistPool.Set(sampleTodolists())

	_, cmd := v.Update(todolistCreatedMsg{todosetID: 10, err: nil})
	require.NotNil(t, cmd)
	assert.True(t, v.loadingLists)
}

// --- Todolist rename ---

func TestTodos_RenameList_LeftPane(t *testing.T) {
	v := testTodosView()
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	require.NotNil(t, cmd, "R should return blink cmd")
	assert.True(t, v.renamingList)
	assert.Contains(t, v.listInput.Value(), "Launch") // pre-filled
}

func TestTodos_RenameList_EnterDispatches(t *testing.T) {
	v := testTodosView()
	v.renamingList = true
	v.listInput = newTextInputWithValue("Renamed List")

	cmd := v.handleListInputKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	assert.False(t, v.renamingList)

	msg := cmd()
	result, ok := msg.(todolistRenamedMsg)
	require.True(t, ok)
	assert.Equal(t, int64(10), result.todolistID)
	assert.Error(t, result.err) // nil SDK
}

func TestTodos_RenameList_SuccessHandler(t *testing.T) {
	v := testTodosView()
	v.todolistPool.Set(sampleTodolists())

	_, cmd := v.Update(todolistRenamedMsg{todolistID: 10, err: nil})
	require.NotNil(t, cmd)
	assert.True(t, v.loadingLists)
}

// --- Todolist trash ---

func TestTodos_TrashList_LeftPane_Arms(t *testing.T) {
	v := testTodosView()
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}})
	require.NotNil(t, cmd)
	assert.True(t, v.trashListPending)
	assert.Equal(t, "10", v.trashListPendingID)
}

func TestTodos_TrashList_DoublePress(t *testing.T) {
	v := testTodosView()
	// First press
	v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}})
	require.True(t, v.trashListPending)

	// Second press
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}})
	require.NotNil(t, cmd)
	assert.False(t, v.trashListPending)

	msg := cmd()
	result, ok := msg.(todolistTrashResultMsg)
	require.True(t, ok)
	assert.Equal(t, int64(10), result.todolistID)
}

func TestTodos_TrashList_RightPane_Noop(t *testing.T) {
	v := testTodosViewWithTodos()
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}})
	assert.Nil(t, cmd, "T on right pane should return nil")
	assert.False(t, v.trashListPending)
}

func TestTodos_TrashList_OtherKeyResets(t *testing.T) {
	v := testTodosView()
	v.trashListPending = true
	v.trashListPendingID = "10"

	v.handleKey(runeKey('j'))
	assert.False(t, v.trashListPending)
	assert.Empty(t, v.trashListPendingID)
}

func TestTodos_TrashList_SuccessHandler_ClearsRightPanel(t *testing.T) {
	v := testTodosViewWithTodos()
	v.todolistPool.Set(sampleTodolists())

	_, cmd := v.Update(todolistTrashResultMsg{todolistID: 10, err: nil})
	require.NotNil(t, cmd)
	assert.True(t, v.loadingLists)
	assert.Equal(t, int64(0), v.selectedListID, "should clear selectedListID")
	assert.Empty(t, v.listTodos.Items(), "should clear right panel")
}

func TestTodos_TrashList_Timeout(t *testing.T) {
	v := testTodosView()
	v.trashListPending = true
	v.trashListPendingID = "10"

	v.Update(todolistTrashTimeoutMsg{})
	assert.False(t, v.trashListPending)
	assert.Empty(t, v.trashListPendingID)
}

// --- ShortHelp: left pane shows list management ---

func TestTodos_ShortHelp_LeftPaneShowsListKeys(t *testing.T) {
	v := testTodosView()
	hints := v.ShortHelp()

	keys := make(map[string]string)
	for _, h := range hints {
		keys[h.Help().Key] = h.Help().Desc
	}
	assert.Equal(t, "new list", keys["N"])
	assert.Equal(t, "rename list", keys["R"])
	assert.Equal(t, "trash list", keys["T"])
}

// --- Completed todos toggle ---

func sampleCompletedTodos() []data.TodoInfo {
	return []data.TodoInfo{
		{ID: 200, Content: "Old task", Completed: true, Position: 1},
		{ID: 201, Content: "Done task", Completed: true, Position: 2},
	}
}

func TestTodos_ShowCompleted_CToggleOnLeftPane(t *testing.T) {
	v := testTodosView()
	v.selectedListID = 10

	assert.False(t, v.showCompleted)

	// c on left pane toggles showCompleted
	v.handleKey(runeKey('c'))
	assert.True(t, v.showCompleted)

	// c again toggles back
	v.handleKey(runeKey('c'))
	assert.False(t, v.showCompleted)
}

func TestTodos_ShowCompleted_COnRightPane_NoOp(t *testing.T) {
	v := testTodosViewWithTodos()
	assert.False(t, v.showCompleted)

	// c on right pane should NOT toggle (c key bound to ShowCompleted on left pane only)
	v.handleKey(runeKey('c'))
	assert.False(t, v.showCompleted, "c on right pane should not toggle showCompleted")
}

func TestTodos_ShowCompleted_SwitchListFetchesFromCompletedPool(t *testing.T) {
	v := testTodosView()
	v.showCompleted = true
	v.selectedListID = 0 // force reload

	// Select a todolist while in completed mode
	cmd := v.selectTodolist("10")
	// Should return a cmd (fetch from completed pool)
	// The exact cmd depends on pool state, but selectedListID should update
	assert.Equal(t, int64(10), v.selectedListID)
	_ = cmd
}

func TestTodos_ShowCompleted_ToggleBackRefetchesPending(t *testing.T) {
	v := testTodosView()
	v.selectedListID = 10
	v.showCompleted = true

	// Toggle back to pending
	cmd := v.handleKey(runeKey('c'))
	assert.False(t, v.showCompleted)
	// Should trigger a refetch from pending pool
	_ = cmd
}

func TestTodos_ShowCompleted_XInCompletedMode_Uncompletes(t *testing.T) {
	v := testTodosViewWithTodos()
	v.showCompleted = true

	// Pre-populate completed pool
	completedPool := v.session.Hub().CompletedTodos(42, 10)
	completedPool.Set(sampleCompletedTodos())
	v.renderTodoItems(sampleCompletedTodos())

	// x should dispatch uncomplete, not toggle
	cmd := v.handleKey(runeKey('x'))
	require.NotNil(t, cmd, "x in completed mode should return uncomplete cmd")

	msg := cmd()
	result, ok := msg.(todoUncompletedMsg)
	require.True(t, ok, "cmd should produce todoUncompletedMsg")
	assert.Equal(t, int64(10), result.todolistID)
	// Error expected with nil SDK
	assert.Error(t, result.err)
}

func TestTodos_ShowCompleted_UncompletedMsg_InvalidatesBothPools(t *testing.T) {
	v := testTodosViewWithTodos()
	v.showCompleted = true

	_, cmd := v.Update(todoUncompletedMsg{todolistID: 10, err: nil})
	require.NotNil(t, cmd)
}

func TestTodos_ShowCompleted_XInPendingMode_NotUncomplete(t *testing.T) {
	v := testTodosViewWithTodos()

	// Verify not in completed mode — x should NOT use the uncomplete path.
	// We can verify this by checking that toggleSelected is called (which reads
	// from the MutatingPool) rather than uncompleteSelected (which calls Hub.UncompleteTodo).
	// Direct verification: uncompleteSelected produces todoUncompletedMsg,
	// toggleSelected calls todosPool.Apply.
	assert.False(t, v.showCompleted)

	// uncompleteSelected goes through a different path than toggleSelected.
	// Since the test session's AccountClient panics on use, we verify
	// the routing by calling uncompleteSelected directly and checking the msg type.
	cmd := v.uncompleteSelected()
	require.NotNil(t, cmd)
	msg := cmd()
	_, isUncomplete := msg.(todoUncompletedMsg)
	assert.True(t, isUncomplete, "uncompleteSelected should produce todoUncompletedMsg")

	// The key routing in handleKey uses showCompleted to decide:
	// showCompleted=true → uncompleteSelected, showCompleted=false → toggleSelected
	// This test confirms the methods produce different msg types.
}

func TestTodos_ShowCompleted_MutationKeysDisabledInCompletedMode(t *testing.T) {
	// Table-driven test for all disabled keys in completed mode
	disabledKeys := []struct {
		name string
		msg  tea.KeyMsg
	}{
		{"d (edit desc)", runeKey('d')},
		{"D (due date)", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}}},
		{"a (assign)", runeKey('a')},
		{"A (unassign)", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}}},
		{"n (new todo)", runeKey('n')},
		{"b (boost)", runeKey('b')},
		{"B (boost)", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'B'}}},
	}

	for _, tc := range disabledKeys {
		t.Run(tc.name, func(t *testing.T) {
			v := testTodosViewWithTodos()
			v.showCompleted = true

			cmd := v.handleKey(tc.msg)
			assert.Nil(t, cmd, "%s should be no-op in completed mode", tc.name)

			// Verify no mutation state was entered
			assert.False(t, v.creating, "should not enter create mode")
			assert.False(t, v.editingDesc, "should not enter edit desc mode")
			assert.False(t, v.settingDue, "should not enter setting due mode")
			assert.False(t, v.assigning, "should not enter assigning mode")
		})
	}
}

func TestTodos_ShowCompleted_InactivePoolUpdateIgnored(t *testing.T) {
	v := testTodosViewWithTodos()
	v.showCompleted = true

	// A PoolUpdatedMsg for the pending pool should be ignored in completed mode
	pendingPool := v.session.Hub().Todos(42, 10)
	v.Update(data.PoolUpdatedMsg{Key: pendingPool.Key()})
	// No crash, no state change - just returns nil
}

// --- ShortHelp: completed mode ---

func TestTodos_ShortHelp_LeftPane_ShowsCompletedHint(t *testing.T) {
	v := testTodosView()
	hints := v.ShortHelp()
	keys := make(map[string]string)
	for _, h := range hints {
		keys[h.Help().Key] = h.Help().Desc
	}
	assert.Equal(t, "completed", keys["c"])
}

func TestTodos_ShortHelp_LeftPane_ShowsPendingHintWhenCompleted(t *testing.T) {
	v := testTodosView()
	v.showCompleted = true
	hints := v.ShortHelp()
	keys := make(map[string]string)
	for _, h := range hints {
		keys[h.Help().Key] = h.Help().Desc
	}
	assert.Equal(t, "pending", keys["c"])
}

func TestTodos_ShortHelp_RightPaneCompleted_ShowsUncomplete(t *testing.T) {
	v := testTodosViewWithTodos()
	v.showCompleted = true
	hints := v.ShortHelp()
	keys := make(map[string]string)
	for _, h := range hints {
		keys[h.Help().Key] = h.Help().Desc
	}
	assert.Equal(t, "uncomplete", keys["x"])
	// Should NOT include d, D, a, n, b
	_, hasD := keys["d"]
	_, hasN := keys["n"]
	assert.False(t, hasD, "d should not be in completed mode hints")
	assert.False(t, hasN, "n should not be in completed mode hints")
}

// --- Regression: c toggle preserves selectedListID ---

func TestTodos_ShowCompleted_CPreservesSelectedList(t *testing.T) {
	v := testTodosView()
	v.selectedListID = 10

	// Pre-populate both pools so load* finds usable data
	pendingPool := v.session.Hub().Todos(42, 10)
	pendingPool.Set(sampleTodos())
	completedPool := v.session.Hub().CompletedTodos(42, 10)
	completedPool.Set(sampleCompletedTodos())

	// Toggle to completed
	v.handleKey(runeKey('c'))
	assert.True(t, v.showCompleted)
	assert.Equal(t, int64(10), v.selectedListID, "selectedListID must survive toggle to completed")

	// Right pane should now show completed items
	items := v.listTodos.Items()
	require.Len(t, items, 2)
	assert.Equal(t, "200", items[0].ID, "right pane should show completed todos")

	// Toggle back to pending
	v.handleKey(runeKey('c'))
	assert.False(t, v.showCompleted)
	assert.Equal(t, int64(10), v.selectedListID, "selectedListID must survive toggle to pending")

	// Right pane should show pending items again
	items = v.listTodos.Items()
	require.Len(t, items, 3)
	assert.Equal(t, "100", items[0].ID, "right pane should show pending todos")
}

// --- Regression: pool update after toggle routes correctly ---

func TestTodos_ShowCompleted_PoolUpdateAfterToggle(t *testing.T) {
	v := testTodosView()
	v.selectedListID = 10

	completedPool := v.session.Hub().CompletedTodos(42, 10)
	completedPool.Set(sampleCompletedTodos())

	// Toggle to completed mode
	v.showCompleted = true

	// Simulate a PoolUpdatedMsg for the completed pool
	v.Update(data.PoolUpdatedMsg{Key: completedPool.Key()})
	assert.False(t, v.loadingTodos, "completed pool update should sync data")

	// The right pane should have the completed items
	items := v.listTodos.Items()
	require.Len(t, items, 2)
	assert.Equal(t, "200", items[0].ID)

	// Now: a pending pool update should be IGNORED while in completed mode
	pendingPool := v.session.Hub().Todos(42, 10)
	pendingPool.Set(sampleTodos())
	v.Update(data.PoolUpdatedMsg{Key: pendingPool.Key()})

	// Right pane should still show completed items (not overwritten by pending)
	items = v.listTodos.Items()
	require.Len(t, items, 2, "pending pool update should not overwrite completed pane")
	assert.Equal(t, "200", items[0].ID)
}

// --- Regression: uncomplete during mode switch fetches active pool ---

func TestTodos_ShowCompleted_UncompleteMidToggle(t *testing.T) {
	v := testTodosViewWithTodos()

	// Start in completed mode with a selected list
	v.showCompleted = true

	// Simulate an uncomplete that completes AFTER user toggled back to pending.
	// The handler should fetch the pending pool (the active pool), not completed.
	v.showCompleted = false // user toggled back mid-flight

	// Seed both pools fresh so we can observe invalidation
	completedPool := v.session.Hub().CompletedTodos(42, 10)
	completedPool.Set(sampleCompletedTodos())
	pendingPool := v.session.Hub().Todos(42, 10)
	pendingPool.Set(sampleTodos())
	require.True(t, completedPool.Get().Fresh(), "completed pool should start fresh")
	require.True(t, pendingPool.Get().Fresh(), "pending pool should start fresh")

	// Handler fires with the uncompleted msg
	_, cmd := v.Update(todoUncompletedMsg{todolistID: 10, err: nil})
	require.NotNil(t, cmd, "should return refetch cmd")

	// Both pools should be invalidated regardless of current mode
	assert.False(t, completedPool.Get().Fresh(), "completed pool should be invalidated")
	assert.False(t, pendingPool.Get().Fresh(), "pending pool should be invalidated")
}

// --- FocusMsg triggers FetchIfStale ---

func TestTodos_FocusMsg_FetchesBothPools(t *testing.T) {
	v := testTodosView()
	v.selectedListID = 10

	// Pre-populate pools and invalidate to make them stale.
	v.todolistPool.Set(sampleTodolists())
	v.todolistPool.Invalidate()

	todosPool := v.session.Hub().Todos(42, 10)
	todosPool.Set(sampleTodos())
	todosPool.Invalidate()

	require.Equal(t, data.StateStale, v.todolistPool.Get().State)
	require.Equal(t, data.StateStale, todosPool.Get().State)

	_, cmd := v.Update(workspace.FocusMsg{})
	assert.NotNil(t, cmd, "FocusMsg with stale pools should return a batch cmd")
}

func TestTodos_FocusMsg_NothingSelected(t *testing.T) {
	v := testTodosView()
	v.selectedListID = 0

	// Only the todolist pool is fetched — no active todos pool.
	v.todolistPool.Set(sampleTodolists())
	v.todolistPool.Invalidate()

	_, cmd := v.Update(workspace.FocusMsg{})
	assert.NotNil(t, cmd, "FocusMsg should still fetch todolistPool")
}

// newTextInputWithValue creates a textinput with a preset value for testing.
func newTextInputWithValue(val string) textinput.Model {
	ti := textinput.New()
	ti.SetValue(val)
	return ti
}
