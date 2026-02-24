package views

import (
	"context"
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

func testHomeView() *Home {
	styles := tui.NewStyles()

	list := widget.NewList(styles)
	list.SetEmptyText("Welcome to Basecamp.")
	list.SetFocused(true)
	list.SetSize(80, 24)
	list.SetItems([]widget.ListItem{
		{ID: "1", Title: "Project Alpha"},
		{ID: "2", Title: "Project Beta"},
	})

	return &Home{
		styles:   styles,
		list:     list,
		itemMeta: make(map[string]homeItemMeta),
		width:    80,
		height:   24,
	}
}

func TestHome_EmptyState_ShowsWelcome(t *testing.T) {
	styles := tui.NewStyles()
	list := widget.NewList(styles)
	list.SetEmptyText("Welcome to Basecamp.")
	list.SetFocused(true)
	list.SetSize(80, 24)
	// No items — simulates empty state with resolved pools

	v := &Home{
		styles:   styles,
		list:     list,
		itemMeta: make(map[string]homeItemMeta),
		width:    80,
		height:   24,
	}

	output := v.View()
	assert.Contains(t, output, "Welcome to Basecamp")
	assert.Contains(t, output, "Browse projects")
	assert.Contains(t, output, "Search across everything")
	assert.Contains(t, output, "ctrl+p")
}

func TestHome_WithData_HidesWelcome(t *testing.T) {
	v := testHomeView() // has items
	output := v.View()
	assert.NotContains(t, output, "Browse projects")
	assert.Contains(t, output, "Project Alpha")
}

func TestHome_FilterDelegatesAllKeys(t *testing.T) {
	v := testHomeView()

	// Start filter
	v.list.StartFilter()
	require.True(t, v.list.Filtering())

	// Press 'p' — should be absorbed by filter, NOT trigger navigate to projects
	updated, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})

	// Verify no navigation command was produced
	if cmd != nil {
		// Run the cmd to get the message — navigate produces a NavigateMsg
		msg := cmd()
		_, isKey := msg.(tea.KeyMsg)
		// The command should NOT be a navigation; it should be nil or a filter-internal cmd
		assert.False(t, isKey, "should not produce a raw key msg")
	}

	home := updated.(*Home)
	assert.True(t, home.list.Filtering(), "filter should still be active after 'p'")
}

func TestHome_PoolError_ClearsSection_ReportsError(t *testing.T) {
	styles := tui.NewStyles()

	fetchErr := fmt.Errorf("network timeout")

	heyPool := data.NewPool[[]data.ActivityEntryInfo](
		"hey:global",
		data.PoolConfig{FreshTTL: time.Hour},
		func(context.Context) ([]data.ActivityEntryInfo, error) {
			return nil, fetchErr
		},
	)
	// Trigger error state by fetching
	heyPool.Fetch(context.Background())()
	require.Equal(t, data.StateError, heyPool.Get().State)

	assignPool := data.NewPool[[]data.AssignmentInfo](
		"assign:global",
		data.PoolConfig{FreshTTL: time.Hour},
		func(context.Context) ([]data.AssignmentInfo, error) {
			return []data.AssignmentInfo{}, nil
		},
	)
	assignPool.Set([]data.AssignmentInfo{})

	projectPool := data.NewPool[[]data.ProjectInfo](
		"projects:global",
		data.PoolConfig{FreshTTL: time.Hour},
		func(context.Context) ([]data.ProjectInfo, error) {
			return []data.ProjectInfo{}, nil
		},
	)
	projectPool.Set([]data.ProjectInfo{})

	list := widget.NewList(styles)
	list.SetEmptyText("Welcome to Basecamp.")
	list.SetFocused(true)
	list.SetSize(80, 24)

	v := &Home{
		styles:      styles,
		heyPool:     heyPool,
		assignPool:  assignPool,
		projectPool: projectPool,
		list:        list,
		itemMeta:    make(map[string]homeItemMeta),
		width:       80,
		height:      24,
	}

	// Pre-populate heyItems so we can verify they get cleared
	v.heyItems = []widget.ListItem{{ID: "old", Title: "stale"}}
	v.rebuildList()
	require.Greater(t, v.list.Len(), 0, "should have items before error")

	// Send PoolUpdatedMsg for the errored hey pool
	_, cmd := v.Update(data.PoolUpdatedMsg{Key: heyPool.Key()})

	// 1. Section items cleared
	assert.Nil(t, v.heyItems, "heyItems should be nil after pool error")

	// 2. List rebuilt (no stale rows)
	assert.Equal(t, 0, v.list.Len(), "list should be empty after clearing the only section")

	// 3. Returned cmd produces workspace.ErrorMsg
	require.NotNil(t, cmd, "should return an error report cmd")
	msg := cmd()
	errMsg, ok := msg.(workspace.ErrorMsg)
	require.True(t, ok, "cmd should produce workspace.ErrorMsg, got %T", msg)
	assert.Equal(t, fetchErr, errMsg.Err)
	assert.Equal(t, "loading Hey! activity", errMsg.Context)
}
