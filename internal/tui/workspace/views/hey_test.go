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

func testHey(entries []data.ActivityEntryInfo) *Hey {
	styles := tui.NewStyles()
	list := widget.NewList(styles)
	list.SetEmptyText("No recent activity.")
	list.SetFocused(true)
	list.SetSize(80, 20)
	pool := testPool("hey:activity", entries, true)
	session := workspace.NewTestSessionWithHub()

	v := &Hey{
		session:   session,
		pool:      pool,
		styles:    styles,
		list:      list,
		loading:   false,
		entryMeta: make(map[string]workspace.ActivityEntryInfo),
		excluded:  make(map[string]bool),
	}
	v.syncEntries(entries)
	return v
}

var testHeyEntries = []data.ActivityEntryInfo{
	{ID: 1, Title: "Fix login", Type: "Todo", AccountID: "acct1", ProjectID: 10, UpdatedAtTS: 100},
	{ID: 2, Title: "Weekly update", Type: "Message", AccountID: "acct1", ProjectID: 10, UpdatedAtTS: 90},
}

func TestHey_CompleteSelected_Todo(t *testing.T) {
	v := testHey(testHeyEntries)
	cmd := v.completeSelected()
	require.NotNil(t, cmd, "completeSelected should return a cmd for Todo")

	msg := cmd()
	result, ok := msg.(heyCompleteResultMsg)
	require.True(t, ok, "cmd should produce heyCompleteResultMsg")
	assert.Equal(t, "acct1:1", result.itemID)
	assert.Error(t, result.err) // nil SDK
}

func TestHey_CompleteSelected_NonTodo(t *testing.T) {
	entries := []data.ActivityEntryInfo{
		{ID: 2, Title: "Weekly update", Type: "Message", AccountID: "acct1", ProjectID: 10, UpdatedAtTS: 100},
	}
	v := testHey(entries)
	cmd := v.completeSelected()
	require.NotNil(t, cmd)

	msg := cmd()
	status, ok := msg.(workspace.StatusMsg)
	require.True(t, ok, "should produce StatusMsg for non-todo")
	assert.Contains(t, status.Text, "Can only complete todos")
}

func TestHey_Trash_DoublePress(t *testing.T) {
	v := testHey(testHeyEntries)

	// First press arms
	cmd := v.trashSelected()
	require.NotNil(t, cmd)
	assert.True(t, v.trashPending)
	assert.Equal(t, "acct1:1", v.trashPendingID)

	// Second press fires
	cmd = v.trashSelected()
	require.NotNil(t, cmd)
	assert.False(t, v.trashPending)

	msg := cmd()
	result, ok := msg.(heyTrashResultMsg)
	require.True(t, ok)
	assert.Equal(t, "acct1:1", result.itemID)
}

func TestHey_FilterBlocksMutationKeys(t *testing.T) {
	v := testHey(testHeyEntries)
	v.list.StartFilter()

	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd != nil {
		msg := cmd()
		_, isComplete := msg.(heyCompleteResultMsg)
		assert.False(t, isComplete, "x during filter should not trigger completion")
	}
}

func TestHey_FilterBlocksTrashArming(t *testing.T) {
	v := testHey(testHeyEntries)
	v.list.StartFilter()

	v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	assert.False(t, v.trashPending, "t during filter should not arm trash")
}

func TestHey_ShortHelp_IncludesActions(t *testing.T) {
	v := testHey(testHeyEntries)
	hints := v.ShortHelp()

	keys := make(map[string]string)
	for _, h := range hints {
		keys[h.Help().Key] = h.Help().Desc
	}
	assert.Equal(t, "complete", keys["x"])
	assert.Equal(t, "trash", keys["t"])
}
