package views

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

func testTimeline(entries []data.TimelineEventInfo) *Timeline {
	styles := tui.NewStyles()
	list := widget.NewList(styles)
	list.SetEmptyText("No recent activity for this project.")
	list.SetFocused(true)
	list.SetSize(80, 20)

	pool := testPool("project-timeline:42", entries, true)

	v := &Timeline{
		session:   workspace.NewTestSession(),
		pool:      pool,
		projectID: 42,
		styles:    styles,
		list:      list,
		loading:   false,
		entryMeta: make(map[string]workspace.TimelineEventInfo),
	}

	v.syncEntries(entries)
	return v
}

func TestTimeline_SyncEntries_ProjectScoped_NoAccountBadges(t *testing.T) {
	entries := sampleTimeline()
	v := testTimeline(entries)

	// No account badges — Extra should only contain SummaryExcerpt (empty here)
	for _, item := range v.list.Items() {
		if item.Header {
			continue
		}
		// sampleTimeline() has no SummaryExcerpt, so Extra should be empty
		assert.Empty(t, item.Extra, "project-scoped timeline should not have account badges")
	}
}

func TestTimeline_SyncEntries_TimeBucketing(t *testing.T) {
	now := time.Now()
	entries := []data.TimelineEventInfo{
		{ID: 1, CreatedAtTS: now.Add(-1 * time.Minute).Unix(), Action: "completed", Target: "Todo", Title: "A", AccountID: "a1"},
		{ID: 2, CreatedAtTS: now.Add(-2 * time.Hour).Unix(), Action: "created", Target: "Message", Title: "B", AccountID: "a1"},
	}

	v := testTimeline(entries)

	// Should have at least 2 headers and 2 items
	headers := 0
	items := 0
	for _, item := range v.list.Items() {
		if item.Header {
			headers++
		} else {
			items++
		}
	}
	assert.Equal(t, 2, items, "should have 2 entry items")
	assert.GreaterOrEqual(t, headers, 2, "entries in different time buckets should produce separate headers")
}

func TestTimeline_PoolKey_ContainsProjectID(t *testing.T) {
	entries := sampleTimeline()
	v := testTimeline(entries)

	assert.Contains(t, v.pool.Key(), "42", "pool key should contain project ID")
}

func TestTimeline_Title(t *testing.T) {
	v := testTimeline(nil)
	assert.Equal(t, "Project Activity", v.Title())
}

func TestTimeline_OpenSelected_ZeroRecordingID(t *testing.T) {
	now := time.Now()
	entries := []data.TimelineEventInfo{
		{ID: 1, RecordingID: 0, CreatedAtTS: now.Unix(), Action: "archived", Target: "Project", AccountID: "a1"},
	}
	v := testTimeline(entries)

	cmd := v.openSelected()
	require.NotNil(t, cmd)

	msg := cmd()
	_, isNav := msg.(workspace.NavigateMsg)
	assert.False(t, isNav, "should not navigate when RecordingID is 0")
}

func TestTimeline_OpenSelected_ValidRecordingID(t *testing.T) {
	entries := sampleTimeline()
	v := testTimeline(entries)

	cmd := v.openSelected()
	require.NotNil(t, cmd)

	msg := cmd()
	nav, isNav := msg.(workspace.NavigateMsg)
	require.True(t, isNav)
	assert.Equal(t, int64(5001), nav.Scope.RecordingID)
	assert.Equal(t, workspace.ViewDetail, nav.Target)
}
