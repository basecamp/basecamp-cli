package views

import (
	"context"
	"net/http"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
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

// heyOverTransport is testHey with an SDK client that talks to transport.
func heyOverTransport(transport http.RoundTripper, entries []data.ActivityEntryInfo) *Hey {
	v := testHey(entries)
	sdk := basecamp.NewClient(&basecamp.Config{BaseURL: "https://3.basecampapi.com"}, starTestToken{},
		basecamp.WithTransport(transport), basecamp.WithMaxRetries(1))
	v.session = workspace.NewTestSessionWithClient(sdk, workspace.Scope{AccountID: "99999"})
	return v
}

// A message, to-do or document in Hey is trashed through the generic route.
func TestHey_Trash_TrashesARecording(t *testing.T) {
	transport := &routeTransport{routes: map[string]stubReply{
		"PUT /99999/recordings/2/status/trashed.json": {http.StatusNoContent, ""},
	}}
	v := heyOverTransport(transport, []data.ActivityEntryInfo{
		{ID: 2, Title: "Weekly update", Type: "Message", AccountID: "99999", ProjectID: 10, UpdatedAtTS: 100},
	})

	assert.Equal(t, "Press t again to trash", statusText(t, v.trashSelected()))
	result, ok := v.trashSelected()().(heyTrashResultMsg)
	require.True(t, ok)
	require.NoError(t, result.err)
	assert.False(t, result.deleted)
	assert.Equal(t, []string{"PUT /99999/recordings/2/status/trashed.json"}, transport.writes())
}

// Hey lists no Campfire lines today, but its trash decides by the entry's API
// type, so a line would be deleted through its Campfire, read to find it.
func TestHey_Trash_DeletesACampfireLine(t *testing.T) {
	transport := &routeTransport{routes: map[string]stubReply{
		"GET /99999/recordings/111.json":    {http.StatusOK, pingLineJSON},
		"DELETE /99999/chats/222/lines/111": {http.StatusNoContent, ""},
	}}
	v := heyOverTransport(transport, []data.ActivityEntryInfo{
		{ID: 111, Title: "Lunch?", Type: "Chat::Lines::Text", AccountID: "99999", ProjectID: 333, UpdatedAtTS: 100},
	})

	assert.Equal(t, "Press t again to delete this line for good", statusText(t, v.trashSelected()))
	result, ok := v.trashSelected()().(heyTrashResultMsg)
	require.True(t, ok)
	require.NoError(t, result.err)
	assert.True(t, result.deleted)
	assert.Equal(t, []string{"DELETE /99999/chats/222/lines/111"}, transport.writes())
}

func TestHey_FilterBlocksMutationKeys(t *testing.T) {
	v := testHey(testHeyEntries)
	v.list.StartFilter()

	_, cmd := v.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if cmd != nil {
		msg := cmd()
		_, isComplete := msg.(heyCompleteResultMsg)
		assert.False(t, isComplete, "x during filter should not trigger completion")
	}
}

func TestHey_FilterBlocksTrashArming(t *testing.T) {
	v := testHey(testHeyEntries)
	v.list.StartFilter()

	v.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
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

func testPollingHey() *Hey {
	styles := tui.NewStyles()
	list := widget.NewList(styles)
	list.SetEmptyText("No recent activity.")
	list.SetFocused(true)
	list.SetSize(80, 20)

	entries := testHeyEntries
	pool := data.NewPool[[]data.ActivityEntryInfo]("hey:activity", data.PoolConfig{
		FreshTTL: time.Hour,
		PollBase: 30 * time.Second,
	}, func(_ context.Context) ([]data.ActivityEntryInfo, error) {
		return entries, nil
	})
	pool.Set(entries)

	session := workspace.NewTestSessionWithHub()
	session.Hub().EnsureAccount("test-account")

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

func TestHey_StalePollGen_Dropped(t *testing.T) {
	v := testPollingHey()
	poolKey := v.pool.Key()

	cmd := v.schedulePoll()
	require.NotNil(t, cmd)
	assert.Equal(t, uint64(1), v.pollGen)

	v.Update(workspace.TerminalFocusMsg{})
	assert.Equal(t, uint64(2), v.pollGen)

	_, cmd = v.Update(data.PollMsg{Tag: poolKey, Gen: 1})
	assert.Nil(t, cmd, "stale gen PollMsg should be dropped")

	_, cmd = v.Update(data.PollMsg{Tag: poolKey, Gen: 2})
	assert.NotNil(t, cmd, "current gen PollMsg should be processed")
}
