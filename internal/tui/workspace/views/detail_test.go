package views

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

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
	assert.Nil(t, cmd, "x on non-todo should be a no-op")
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

// -- Edit title tests --

func TestDetail_EditTitle_EnterSubmits(t *testing.T) {
	v := testDetailWithSession("Todo", false)

	// Press 'e' to enter edit mode
	cmd := v.handleKey(runeKey('e'))
	require.NotNil(t, cmd, "e should return a cmd (textinput.Blink)")
	assert.True(t, v.editing, "should be in editing mode after pressing e")

	// Type a new title
	v.editInput.SetValue("Updated Title")

	// Press Enter to submit
	cmd = v.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd, "enter with changed title should return a cmd")

	msg := cmd()
	result, ok := msg.(editTitleResultMsg)
	require.True(t, ok, "cmd should produce editTitleResultMsg")
	assert.Equal(t, "Updated Title", result.title)
	// Error expected since test session has nil SDK
	assert.Error(t, result.err)
}

func TestDetail_EditTitle_EscCancels(t *testing.T) {
	v := testDetailWithSession("Todo", false)

	// Enter edit mode
	v.handleKey(runeKey('e'))
	assert.True(t, v.editing)

	// Press Esc to cancel
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Nil(t, cmd, "esc should return nil cmd")
	assert.False(t, v.editing, "editing should be false after esc")
}

func TestDetail_EditTitle_InputCapturer(t *testing.T) {
	v := testDetailWithSession("Todo", false)

	assert.False(t, v.InputActive(), "should not capture input before editing")

	// Enter edit mode
	v.handleKey(runeKey('e'))
	assert.True(t, v.InputActive(), "should capture input while editing")

	// Cancel editing
	v.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, v.InputActive(), "should not capture input after cancel")
}

// -- Subscribe toggle tests --

func TestDetail_Subscribe_Toggle(t *testing.T) {
	// Subscribe: subscribed=false → should call subscribe
	v := testDetailWithSession("Todo", false)
	v.data.subscribed = false

	cmd := v.handleKey(runeKey('s'))
	require.NotNil(t, cmd, "s should return a cmd")

	msg := cmd()
	result, ok := msg.(subscribeResultMsg)
	require.True(t, ok, "cmd should produce subscribeResultMsg")
	assert.True(t, result.subscribed, "should request subscribe when currently unsubscribed")
	// Error expected since test session has nil SDK
	assert.Error(t, result.err)

	// Unsubscribe: subscribed=true → should call unsubscribe
	v2 := testDetailWithSession("Todo", false)
	v2.data.subscribed = true

	cmd = v2.handleKey(runeKey('s'))
	require.NotNil(t, cmd, "s should return a cmd")

	msg = cmd()
	result, ok = msg.(subscribeResultMsg)
	require.True(t, ok, "cmd should produce subscribeResultMsg")
	assert.False(t, result.subscribed, "should request unsubscribe when currently subscribed")
	assert.Error(t, result.err)
}

// -- Due date tests --

func TestDetail_DueDate_OpensInput_ForTodo(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	require.NotNil(t, cmd, "D should return blink cmd")
	assert.True(t, v.settingDue)
	assert.True(t, v.InputActive())
	assert.True(t, v.IsModal())
}

func TestDetail_DueDate_Ignored_ForMessage(t *testing.T) {
	v := testDetailWithSession("Message", false)
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	assert.Nil(t, cmd, "D on Message should do nothing")
	assert.False(t, v.settingDue)
}

func TestDetail_DueDate_EscCancels(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	v.settingDue = true

	cmd := v.handleDetailSettingDueKey(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Nil(t, cmd)
	assert.False(t, v.settingDue)
}

func TestDetail_DueDate_EnterSubmits(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	v.settingDue = true
	v.dueInput = newDetailTextInput("tomorrow")

	cmd := v.handleDetailSettingDueKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	assert.False(t, v.settingDue)

	msg := cmd()
	result, ok := msg.(detailDueUpdatedMsg)
	require.True(t, ok, "cmd should produce detailDueUpdatedMsg")
	assert.Error(t, result.err) // nil SDK
}

func TestDetail_DueDate_EmptyClears(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	v.settingDue = true
	v.dueInput = newDetailTextInput("")

	cmd := v.handleDetailSettingDueKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)

	msg := cmd()
	result, ok := msg.(detailDueUpdatedMsg)
	require.True(t, ok)
	assert.Error(t, result.err) // nil SDK
}

func TestDetail_DueDate_InvalidDate(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	v.settingDue = true
	v.dueInput = newDetailTextInput("not-a-date")

	cmd := v.handleDetailSettingDueKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)

	msg := cmd()
	status, ok := msg.(workspace.StatusMsg)
	require.True(t, ok, "invalid date should produce StatusMsg")
	assert.Contains(t, status.Text, "Unrecognized date")
}

// -- Assign tests --

func TestDetail_Assign_OpensInput_ForTodo(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	cmd := v.handleKey(runeKey('a'))
	require.NotNil(t, cmd, "a should return blink cmd")
	assert.True(t, v.assigning)
	assert.True(t, v.InputActive())
}

func TestDetail_Assign_Ignored_ForMessage(t *testing.T) {
	v := testDetailWithSession("Message", false)
	cmd := v.handleKey(runeKey('a'))
	assert.Nil(t, cmd, "a on Message should do nothing")
	assert.False(t, v.assigning)
}

func TestDetail_Assign_EscCancels(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	v.assigning = true

	cmd := v.handleDetailAssigningKey(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Nil(t, cmd)
	assert.False(t, v.assigning)
}

func TestDetail_Unassign_Dispatches_ForTodo(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}})
	require.NotNil(t, cmd, "A should return a cmd")

	msg := cmd()
	result, ok := msg.(detailAssignResultMsg)
	require.True(t, ok, "cmd should produce detailAssignResultMsg")
	assert.Error(t, result.err) // nil SDK
}

func TestDetail_Unassign_Ignored_ForMessage(t *testing.T) {
	v := testDetailWithSession("Message", false)
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}})
	assert.Nil(t, cmd, "A on Message should do nothing")
}

// -- ShortHelp due/assign hints --

func TestDetail_ShortHelp_ShowsDueAndAssign_ForTodo(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	hints := v.ShortHelp()

	keys := make(map[string]string)
	for _, h := range hints {
		keys[h.Help().Key] = h.Help().Desc
	}
	assert.Equal(t, "due date", keys["D"])
	assert.Equal(t, "assign", keys["a"])
}

func TestDetail_ShortHelp_HidesDueAndAssign_ForMessage(t *testing.T) {
	v := testDetailWithSession("Message", false)
	hints := v.ShortHelp()

	for _, h := range hints {
		assert.NotEqual(t, "D", h.Help().Key, "Message should not show D hint")
		assert.NotEqual(t, "a", h.Help().Key, "Message should not show a hint")
	}
}

func newDetailTextInput(val string) textinput.Model {
	ti := textinput.New()
	ti.SetValue(val)
	return ti
}

func TestDetail_FetchSubscriptionState(t *testing.T) {
	// Directly exercises the fallback logic extracted from fetchDetail() line 834.
	// fetchSubscriptionState(sub, err) returns false on error or nil response.

	t.Run("error returns false", func(t *testing.T) {
		got := fetchSubscriptionState(nil, assert.AnError)
		assert.False(t, got)
	})

	t.Run("nil subscription returns false", func(t *testing.T) {
		got := fetchSubscriptionState(nil, nil)
		assert.False(t, got)
	})

	t.Run("subscribed true returns true", func(t *testing.T) {
		got := fetchSubscriptionState(&basecamp.Subscription{Subscribed: true}, nil)
		assert.True(t, got)
	})

	t.Run("subscribed false returns false", func(t *testing.T) {
		got := fetchSubscriptionState(&basecamp.Subscription{Subscribed: false}, nil)
		assert.False(t, got)
	})
}

// -- Comment focus navigation tests --

func detailWithComments() *Detail {
	v := testDetailWithSession("Todo", false)
	v.data.comments = []detailComment{
		{id: 1, creator: "Alice", content: "<p>First comment</p>"},
		{id: 2, creator: "Bob", content: "<p>Second comment</p>"},
	}
	v.focusedComment = -1
	return v
}

func TestDetail_CommentFocus_Navigation(t *testing.T) {
	v := detailWithComments()

	// ] moves to first comment
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	require.NotNil(t, cmd)
	assert.Equal(t, 0, v.focusedComment)

	// ] again moves to second comment
	cmd = v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	require.NotNil(t, cmd)
	assert.Equal(t, 1, v.focusedComment)

	// ] clamps at last comment
	cmd = v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	require.NotNil(t, cmd)
	assert.Equal(t, 1, v.focusedComment, "should clamp at last comment")

	// [ moves back
	cmd = v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	require.NotNil(t, cmd)
	assert.Equal(t, 0, v.focusedComment)

	// [ again unfocuses
	cmd = v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	require.NotNil(t, cmd)
	assert.Equal(t, -1, v.focusedComment, "should unfocus when going past first")
}

func TestDetail_CommentEdit_OpensInput(t *testing.T) {
	v := detailWithComments()
	v.focusedComment = 0

	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}})
	require.NotNil(t, cmd, "E should return blink cmd")
	assert.True(t, v.editingComment, "should be in comment editing mode")
	assert.True(t, v.InputActive(), "input should be active during comment edit")
	assert.True(t, v.IsModal(), "should be modal during comment edit")
}

func TestDetail_CommentEdit_Ignored_WhenNoFocus(t *testing.T) {
	v := detailWithComments()
	assert.Equal(t, -1, v.focusedComment)

	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}})
	assert.Nil(t, cmd, "E should do nothing when no comment focused")
	assert.False(t, v.editingComment)
}

func TestDetail_CommentTrash_DoublePress(t *testing.T) {
	v := detailWithComments()
	v.focusedComment = 1

	// First T arms
	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}})
	require.NotNil(t, cmd)
	assert.True(t, v.commentTrashPending, "first T should arm comment trash")

	// Second T fires
	cmd = v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}})
	require.NotNil(t, cmd, "second T should return trash cmd")
	assert.False(t, v.commentTrashPending, "should be cleared after confirm")

	msg := cmd()
	result, ok := msg.(commentTrashResultMsg)
	require.True(t, ok, "cmd should produce commentTrashResultMsg")
	// Error expected since test session has nil SDK
	assert.Error(t, result.err)
}

func TestDetail_CommentTrash_Ignored_WhenNoFocus(t *testing.T) {
	v := detailWithComments()
	assert.Equal(t, -1, v.focusedComment)

	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}})
	assert.Nil(t, cmd, "T should do nothing when no comment focused")
	assert.False(t, v.commentTrashPending)
}

func TestDetail_CommentTrash_Timeout(t *testing.T) {
	v := detailWithComments()
	v.commentTrashPending = true

	v.Update(commentTrashTimeoutMsg{})
	assert.False(t, v.commentTrashPending, "timeout should reset commentTrashPending")
}

func TestDetail_ShortHelp_ShowsCommentKeys(t *testing.T) {
	v := detailWithComments()
	hints := v.ShortHelp()

	keys := make(map[string]string)
	for _, h := range hints {
		keys[h.Help().Key] = h.Help().Desc
	}
	assert.Equal(t, "comment nav", keys["]/["])
	assert.Equal(t, "edit comment", keys["E"])
	assert.Equal(t, "trash comment", keys["T"])
}

func TestDetail_ShortHelp_HidesCommentKeys_WhenNoComments(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	// No comments set
	hints := v.ShortHelp()

	for _, h := range hints {
		assert.NotEqual(t, "]/[", h.Help().Key, "should not show comment nav without comments")
		assert.NotEqual(t, "E", h.Help().Key, "should not show edit comment without comments")
		assert.NotEqual(t, "T", h.Help().Key, "should not show trash comment without comments")
	}
}

// -- Edit key guards --

func TestDetail_E_NoOp_ForMessage(t *testing.T) {
	v := testDetailWithSession("Message", false)
	cmd := v.handleKey(runeKey('e'))
	assert.Nil(t, cmd, "e on Message should be a no-op")
	assert.False(t, v.editing, "should not enter editing mode for Message")
}

func TestDetail_ShortHelp_HidesEdit_ForMessage(t *testing.T) {
	v := testDetailWithSession("Message", false)
	hints := v.ShortHelp()

	for _, h := range hints {
		assert.NotEqual(t, "e", h.Help().Key, "Message should not show e hint")
	}
}

func TestDetail_ShortHelp_ShowsEdit_ForTodo(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	hints := v.ShortHelp()

	found := false
	for _, h := range hints {
		if h.Help().Key == "e" {
			found = true
			assert.Equal(t, "edit title", h.Help().Desc)
		}
	}
	assert.True(t, found, "Todo should show e/edit title hint")
}

// -- Category field for Message --

func TestDetail_SyncPreview_CategoryField_ForMessage(t *testing.T) {
	v := testDetail("", "")
	v.data.recordType = "Message"
	v.data.category = "Announcement"
	v.data.dueOn = ""
	v.syncPreview()

	fields := v.preview.Fields()
	hasCat, hasDue := false, false
	for _, f := range fields {
		if f.Key == "Category" && f.Value == "Announcement" {
			hasCat = true
		}
		if f.Key == "Due" {
			hasDue = true
		}
	}
	assert.True(t, hasCat, "Message should have Category field")
	assert.False(t, hasDue, "Message should not have Due field")
}

// -- Body clear on empty content --

// -- Esc suppressed during submit --

func TestDetail_Esc_NoOp_WhileSubmitting(t *testing.T) {
	v := testDetailWithSession("Todo", false)
	v.composing = true
	v.submitting = true
	v.composer = widget.NewComposer(v.styles, widget.WithMode(widget.ComposerRich))

	cmd := v.handleComposingKey(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Nil(t, cmd, "Esc should be no-op while submitting")
	assert.True(t, v.composing, "composing should remain true when submit is in flight")
}

// -- Comment edit error preserves editing state --

func TestDetail_CommentEditError_PreservesEditing(t *testing.T) {
	v := detailWithComments()
	v.editingComment = true

	_, cmd := v.Update(commentEditResultMsg{err: assert.AnError})
	require.NotNil(t, cmd, "error should produce a report cmd")
	assert.True(t, v.editingComment, "editing state should be preserved on error")
}

// -- Body clear on empty content --

func TestDetail_SyncPreview_ClearsBody(t *testing.T) {
	v := testDetail("", "")
	v.preview.SetBody("<p>old content</p>")
	v.data.content = ""
	v.data.comments = nil
	v.syncPreview()

	// After syncPreview with empty content and no comments, body should be cleared.
	// We verify by checking the preview renders without "old content".
	v.preview.SetSize(80, 24)
	output := v.preview.View()
	assert.NotContains(t, output, "old content", "stale body should be cleared")
}
