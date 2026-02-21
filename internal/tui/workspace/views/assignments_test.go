package views

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

func testAssignments(entries []data.AssignmentInfo) *Assignments {
	styles := tui.NewStyles()
	list := widget.NewList(styles)
	list.SetEmptyText("No assignments found.")
	list.SetFocused(true)
	list.SetSize(80, 20)
	pool := testPool("assignments", entries, true)
	session := workspace.NewTestSessionWithHub()

	v := &Assignments{
		session:        session,
		pool:           pool,
		styles:         styles,
		list:           list,
		loading:        false,
		assignmentMeta: make(map[string]workspace.AssignmentInfo),
		excluded:       make(map[string]bool),
	}
	v.syncAssignments(entries)
	return v
}

var testAssignmentEntries = []data.AssignmentInfo{
	{ID: 1, Content: "Fix bug", AccountID: "acct1", ProjectID: 10},
	{ID: 2, Content: "Write tests", AccountID: "acct1", ProjectID: 10},
}

func TestAssignments_CompleteSelected(t *testing.T) {
	v := testAssignments(testAssignmentEntries)
	cmd := v.completeSelected()
	require.NotNil(t, cmd, "completeSelected should return a cmd")

	msg := cmd()
	result, ok := msg.(assignmentCompleteResultMsg)
	require.True(t, ok, "cmd should produce assignmentCompleteResultMsg")
	assert.Equal(t, "acct1:1", result.itemID)
	// Error expected since test session has nil SDK
	assert.Error(t, result.err)
}

func TestAssignments_CompleteSelected_ExcludesItem(t *testing.T) {
	v := testAssignments(testAssignmentEntries)
	// 1 header ("NO DUE DATE") + 2 items = 3
	assert.Equal(t, 3, v.list.Len())

	// Simulate successful completion
	v.Update(assignmentCompleteResultMsg{itemID: "acct1:1", err: nil})
	assert.True(t, v.excluded["acct1:1"])
	// 1 header + 1 remaining item = 2
	assert.Equal(t, 2, v.list.Len())
}

func TestAssignments_CompleteSelected_PoolRefreshClearsExclusions(t *testing.T) {
	v := testAssignments(testAssignmentEntries)
	v.excluded["acct1:1"] = true

	// Simulate a fresh pool update
	v.pool.Set(testAssignmentEntries)
	v.Update(data.PoolUpdatedMsg{Key: v.pool.Key()})
	assert.Empty(t, v.excluded, "StateFresh should clear exclusions")
}

func TestAssignments_Trash_DoublePress(t *testing.T) {
	v := testAssignments(testAssignmentEntries)

	// First press arms trash
	cmd := v.trashSelected()
	require.NotNil(t, cmd)
	assert.True(t, v.trashPending)
	assert.Equal(t, "acct1:1", v.trashPendingID)

	// Second press fires
	cmd = v.trashSelected()
	require.NotNil(t, cmd)
	assert.False(t, v.trashPending)

	msg := cmd()
	result, ok := msg.(assignmentTrashResultMsg)
	require.True(t, ok)
	assert.Equal(t, "acct1:1", result.itemID)
}

func TestAssignments_Trash_OtherKeyResets(t *testing.T) {
	v := testAssignments(testAssignmentEntries)
	v.trashPending = true
	v.trashPendingID = "acct1:1"

	// Send a non-t key
	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	assert.False(t, v.trashPending)
	assert.Empty(t, v.trashPendingID)
}

func TestAssignments_FilterBlocksMutationKeys(t *testing.T) {
	v := testAssignments(testAssignmentEntries)
	v.list.StartFilter()

	// Send "x" key while filtering — should go to list.Update (filter input), not completeSelected
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	// The cmd from list.Update during filtering is not an assignmentCompleteResultMsg
	if cmd != nil {
		msg := cmd()
		_, isComplete := msg.(assignmentCompleteResultMsg)
		assert.False(t, isComplete, "x during filter should not trigger completion")
	}
}

func TestAssignments_FilterBlocksTrashArming(t *testing.T) {
	v := testAssignments(testAssignmentEntries)
	v.list.StartFilter()

	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	assert.False(t, v.trashPending, "t during filter should not arm trash")
}

func TestAssignments_ShortHelp_IncludesActions(t *testing.T) {
	v := testAssignments(testAssignmentEntries)
	hints := v.ShortHelp()

	keys := make(map[string]string)
	for _, h := range hints {
		keys[h.Help().Key] = h.Help().Desc
	}
	assert.Equal(t, "complete", keys["x"])
	assert.Equal(t, "trash", keys["t"])
}

func TestAssignments_ShortHelp_FilteringHidesActions(t *testing.T) {
	v := testAssignments(testAssignmentEntries)
	v.list.StartFilter()
	hints := v.ShortHelp()

	for _, h := range hints {
		assert.NotEqual(t, "x", h.Help().Key, "filter mode should not show x")
		assert.NotEqual(t, "t", h.Help().Key, "filter mode should not show t")
	}
}
