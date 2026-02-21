package views

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
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

func testMessagesViewWithSession() *Messages {
	styles := tui.NewStyles()
	session := workspace.NewTestSessionWithScope(workspace.Scope{
		AccountID: "acct1",
		ProjectID: 42,
	})

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

	pool := data.NewPool[[]data.MessageInfo](
		"messages:test",
		data.PoolConfig{},
		func(context.Context) ([]data.MessageInfo, error) {
			return nil, nil
		},
	)

	return &Messages{
		session:      session,
		pool:         pool,
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

// -- Pin/Unpin tests --

func TestMessages_PinKey_CallsPin(t *testing.T) {
	v := testMessagesViewWithSession()

	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	require.NotNil(t, cmd, "P should return a cmd when a message is selected")

	msg := cmd()
	result, ok := msg.(pinResultMsg)
	require.True(t, ok, "cmd should produce pinResultMsg")
	assert.True(t, result.pinned, "should request pin")
	// Error expected since test session has nil SDK
	assert.Error(t, result.err)
}

func TestMessages_UnpinKey_CallsUnpin(t *testing.T) {
	v := testMessagesViewWithSession()

	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'U'}})
	require.NotNil(t, cmd, "U should return a cmd when a message is selected")

	msg := cmd()
	result, ok := msg.(pinResultMsg)
	require.True(t, ok, "cmd should produce pinResultMsg")
	assert.False(t, result.pinned, "should request unpin")
	// Error expected since test session has nil SDK
	assert.Error(t, result.err)
}

func TestMessages_PinResult_Success(t *testing.T) {
	v := testMessagesViewWithSession()

	// Pre-populate pool with fresh data so Invalidate has something to mark stale.
	v.pool.Set([]data.MessageInfo{{ID: 1, Subject: "Welcome"}})
	require.Equal(t, data.StateFresh, v.pool.Get().State)

	_, cmd := v.Update(pinResultMsg{pinned: true, err: nil})
	require.NotNil(t, cmd, "success should return batch cmd for status + refetch")

	// Pool should be loading: Invalidate() marks stale, then Fetch() transitions to loading.
	assert.Equal(t, data.StateLoading, v.pool.Get().State, "pool should be refetching after pin success")

	// The batch produces inner cmds: SetStatus + pool.Fetch.
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	require.True(t, ok, "should produce a BatchMsg")
	require.Len(t, batch, 2, "batch should contain status + fetch cmds")

	// First cmd is SetStatus("Pinned", false).
	statusMsg := batch[0]()
	status, ok := statusMsg.(workspace.StatusMsg)
	require.True(t, ok, "first batch item should produce StatusMsg")
	assert.Equal(t, "Pinned", status.Text)
	assert.False(t, status.IsError)
}

func TestMessages_UnpinResult_Success(t *testing.T) {
	v := testMessagesViewWithSession()
	v.pool.Set([]data.MessageInfo{{ID: 1, Subject: "Welcome"}})

	_, cmd := v.Update(pinResultMsg{pinned: false, err: nil})
	require.NotNil(t, cmd)

	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	require.True(t, ok)
	require.Len(t, batch, 2)

	statusMsg := batch[0]()
	status, ok := statusMsg.(workspace.StatusMsg)
	require.True(t, ok)
	assert.Equal(t, "Unpinned", status.Text)
}

func TestMessages_PinResult_Error(t *testing.T) {
	v := testMessagesViewWithSession()

	_, cmd := v.Update(pinResultMsg{pinned: true, err: assert.AnError})
	require.NotNil(t, cmd, "error should return error report cmd")

	msg := cmd()
	errMsg, ok := msg.(workspace.ErrorMsg)
	require.True(t, ok, "should produce ErrorMsg")
	assert.Contains(t, errMsg.Context, "pinning")
	assert.Equal(t, assert.AnError, errMsg.Err)
}

func TestMessages_UnpinResult_Error(t *testing.T) {
	v := testMessagesViewWithSession()

	_, cmd := v.Update(pinResultMsg{pinned: false, err: assert.AnError})
	require.NotNil(t, cmd)

	msg := cmd()
	errMsg, ok := msg.(workspace.ErrorMsg)
	require.True(t, ok)
	assert.Contains(t, errMsg.Context, "unpinning")
}
