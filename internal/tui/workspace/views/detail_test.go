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

func testDetail(originView, originHint string) *Detail {
	styles := tui.NewStyles()
	return &Detail{
		styles:        styles,
		recordingID:   100,
		recordingType: "Todo",
		originView:    originView,
		originHint:    originHint,
		preview:       widget.NewPreview(styles),
		data: &detailData{
			title:      "Test Todo",
			recordType: "Todo",
			creator:    "Alice",
		},
	}
}

func testDetailWithSession(recordType string, completed bool) *Detail {
	styles := tui.NewStyles()
	session := workspace.NewTestSessionWithScope(workspace.Scope{
		AccountID: "acct1",
		ProjectID: 42,
	})
	return &Detail{
		session:       session,
		styles:        styles,
		recordingID:   100,
		recordingType: recordType,
		preview:       widget.NewPreview(styles),
		data: &detailData{
			title:      "Test " + recordType,
			recordType: recordType,
			creator:    "Alice",
			completed:  completed,
		},
	}
}

func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestDetail_OriginContext_RenderedInPreview(t *testing.T) {
	v := testDetail("Activity", "completed Todo")
	v.syncPreview()

	fields := v.preview.Fields()
	found := false
	for _, f := range fields {
		if f.Key == "From" {
			found = true
			assert.Equal(t, "Activity · completed Todo", f.Value)
			break
		}
	}
	assert.True(t, found, "preview should contain From field when origin is set")
}

func TestDetail_NoOrigin_NoFromField(t *testing.T) {
	v := testDetail("", "")
	v.syncPreview()

	fields := v.preview.Fields()
	for _, f := range fields {
		assert.NotEqual(t, "From", f.Key, "preview should not contain From field when origin is empty")
	}
}

func TestDetail_OriginViewOnly_NoHint(t *testing.T) {
	v := testDetail("Home", "")
	v.syncPreview()

	fields := v.preview.Fields()
	found := false
	for _, f := range fields {
		if f.Key == "From" {
			found = true
			assert.Equal(t, "Home", f.Value)
			break
		}
	}
	assert.True(t, found, "preview should contain From field with just origin view")
}

// -- Complete toggle tests --

func TestDetail_CompleteToggle_Todo(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	cmd := v.handleKey(runeKey('x'))
	require.NotNil(t, cmd, "x on todo should return a cmd")

	msg := cmd()
	result, ok := msg.(todoToggleResultMsg)
	require.True(t, ok, "cmd should produce todoToggleResultMsg")
	assert.True(t, result.completed, "should request completion")
	// Error expected since test session has nil SDK
	assert.Error(t, result.err)
}

func TestDetail_CompleteToggle_ReopensTodo(t *testing.T) {
	v := testDetailWithSession("Todo", true)
	cmd := v.handleKey(runeKey('x'))
	require.NotNil(t, cmd)

	msg := cmd()
	result, ok := msg.(todoToggleResultMsg)
	require.True(t, ok)
	assert.False(t, result.completed, "should request uncomplete")
}

func TestDetail_CompleteToggle_NonTodo(t *testing.T) {
	v := testDetailWithSession("Message", false)
	cmd := v.handleKey(runeKey('x'))
	require.NotNil(t, cmd, "x on non-todo should produce status cmd")

	msg := cmd()
	status, ok := msg.(workspace.StatusMsg)
	require.True(t, ok, "should produce StatusMsg")
	assert.Contains(t, status.Text, "Can only complete todos")
}

// -- Trash tests --

func TestDetail_Trash_DoublePress(t *testing.T) {
	v := testDetailWithSession("Todo", false)

	// First press arms trash
	cmd := v.handleKey(runeKey('t'))
	require.NotNil(t, cmd)
	assert.True(t, v.trashPending, "first t should arm trash")

	// Second press fires
	cmd = v.handleKey(runeKey('t'))
	require.NotNil(t, cmd, "second t should return trash cmd")
	assert.False(t, v.trashPending, "trashPending should be cleared after confirm")

	msg := cmd()
	result, ok := msg.(trashResultMsg)
	require.True(t, ok, "cmd should produce trashResultMsg")
	// Error expected since test session has nil SDK
	assert.Error(t, result.err)
}

func TestDetail_Trash_SinglePress_OtherKeyResets(t *testing.T) {
	v := testDetailWithSession("Todo", false)

	v.handleKey(runeKey('t'))
	assert.True(t, v.trashPending)

	v.handleKey(runeKey('j'))
	assert.False(t, v.trashPending, "non-t key should reset trashPending")
}

func TestDetail_Trash_Timeout(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	v.trashPending = true

	v.Update(trashTimeoutMsg{})
	assert.False(t, v.trashPending, "timeout should reset trashPending")
}

// -- GoToProject tests --

func TestDetail_GoToProject(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	cmd := v.handleKey(runeKey('g'))
	require.NotNil(t, cmd)

	msg := cmd()
	nav, ok := msg.(workspace.NavigateMsg)
	require.True(t, ok, "g should produce NavigateMsg")
	assert.Equal(t, workspace.ViewDock, nav.Target)
}

func TestDetail_GoToProject_NoProject(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	// Override scope to have no project
	v.session.SetScope(workspace.Scope{AccountID: "acct1"})

	cmd := v.handleKey(runeKey('g'))
	require.NotNil(t, cmd)

	msg := cmd()
	status, ok := msg.(workspace.StatusMsg)
	require.True(t, ok, "g without project should produce StatusMsg")
	assert.Contains(t, status.Text, "No project context")
}

// -- ShortHelp tests --

func TestDetail_ShortHelp_ShowsComplete_ForTodo(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	hints := v.ShortHelp()

	found := false
	for _, h := range hints {
		if h.Help().Key == "x" {
			found = true
			assert.Equal(t, "complete", h.Help().Desc)
		}
	}
	assert.True(t, found, "Todo detail should show x/complete hint")
}

func TestDetail_ShortHelp_ShowsReopen_ForCompletedTodo(t *testing.T) {
	v := testDetailWithSession("Todo", true)
	hints := v.ShortHelp()

	for _, h := range hints {
		if h.Help().Key == "x" {
			assert.Equal(t, "reopen", h.Help().Desc)
			return
		}
	}
	t.Fatal("completed todo should show x/reopen hint")
}

func TestDetail_ShortHelp_HidesComplete_ForMessage(t *testing.T) {
	v := testDetailWithSession("Message", false)
	hints := v.ShortHelp()

	for _, h := range hints {
		assert.NotEqual(t, "x", h.Help().Key, "Message detail should not show x hint")
	}
}

func TestDetail_ShortHelp_ShowsProject_WhenScoped(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	hints := v.ShortHelp()

	found := false
	for _, h := range hints {
		if h.Help().Key == "g" {
			found = true
			assert.Equal(t, "project", h.Help().Desc)
		}
	}
	assert.True(t, found, "should show g/project hint when project is scoped")
}

func TestDetail_ShortHelp_HidesProject_WithoutScope(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	v.session.SetScope(workspace.Scope{AccountID: "acct1"})

	hints := v.ShortHelp()
	for _, h := range hints {
		assert.NotEqual(t, "g", h.Help().Key, "should not show g hint without project")
	}
}
