package views

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
)

func testCampfirePool() *data.Pool[data.CampfireLinesResult] {
	return data.NewPool[data.CampfireLinesResult](
		"campfire:test",
		data.PoolConfig{},
		func(context.Context) (data.CampfireLinesResult, error) {
			return data.CampfireLinesResult{}, nil
		},
	)
}

func TestCampfire_PoolUpdatedSetsBoostTargetToLatestLine(t *testing.T) {
	pool := testCampfirePool()
	pool.Set(data.CampfireLinesResult{
		Lines: []data.CampfireLineInfo{
			{ID: 100, Body: "one", Creator: "Alice", CreatedAt: "9:00am"},
			{ID: 200, Body: "two", Creator: "Bob", CreatedAt: "9:01am"},
			{ID: 300, Body: "three", Creator: "Carol", CreatedAt: "9:02am"},
		},
		TotalCount: 3,
	})

	v := &Campfire{
		pool:           pool,
		styles:         tui.NewStyles(),
		viewport:       viewport.New(80, 20),
		selectedLineID: 100, // stale target before refresh
		lastID:         100,
	}

	model, cmd := v.Update(data.PoolUpdatedMsg{Key: pool.Key()})
	require.NotNil(t, model)
	assert.Nil(t, cmd)
	assert.Equal(t, int64(300), v.selectedLineID, "pool updates should retarget boost to the newest line")
}

func TestCampfire_ScrollModeBoostHotkeyOpensPickerForSelectedLine(t *testing.T) {
	session := workspace.NewTestSession()
	session.SetScope(workspace.Scope{ProjectID: 42})

	v := &Campfire{
		session:        session,
		keys:           defaultCampfireKeyMap(),
		mode:           campfireModeScroll,
		lines:          []workspace.CampfireLineInfo{{ID: 10}, {ID: 20}},
		selectedLineID: 20,
	}

	for _, r := range []rune{'b', 'B'} {
		cmd := v.handleScrollKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		require.NotNil(t, cmd, "expected boost cmd for %q", string(r))

		msg := cmd()
		open, ok := msg.(workspace.OpenBoostPickerMsg)
		require.True(t, ok, "expected OpenBoostPickerMsg for %q", string(r))
		assert.Equal(t, int64(42), open.Target.ProjectID)
		assert.Equal(t, int64(20), open.Target.RecordingID)
		assert.Equal(t, "Campfire line", open.Target.Title)
	}
}

func TestWrapLine_Unicode(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		width int
		want  string
	}{
		{
			name:  "ASCII fits",
			line:  "hello world",
			width: 20,
			want:  "hello world",
		},
		{
			name:  "ASCII wraps",
			line:  "hello world foo",
			width: 11,
			want:  "hello world\nfoo",
		},
		{
			name:  "emoji display width",
			line:  "🎉🎊🎈 party time celebrations",
			width: 15,
			want:  "🎉🎊🎈 party\ntime\ncelebrations",
		},
		{
			name:  "long emoji word",
			line:  "🎉🎊🎈🎆🎇🧨✨🎃",
			width: 4,
			want:  "🎉🎊\n🎈🎆\n🎇🧨\n✨🎃",
		},
		{
			name:  "accented characters",
			line:  "café résumé naïve",
			width: 10,
			want:  "café\nrésumé\nnaïve",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapLine(tt.line, tt.width)
			assert.Equal(t, tt.want, got)
		})
	}
}

func testCampfireWithLines(lines []workspace.CampfireLineInfo) *Campfire {
	pool := testCampfirePool()
	return &Campfire{
		pool:     pool,
		styles:   tui.NewStyles(),
		viewport: viewport.New(80, 20),
		lines:    lines,
		width:    80,
		height:   20,
	}
}

func TestCampfire_MessageGrouping(t *testing.T) {
	now := time.Now()
	v := testCampfireWithLines([]workspace.CampfireLineInfo{
		{ID: 1, Body: "hello", Creator: "Alice", CreatedAt: "9:00am", CreatedAtTS: now},
		{ID: 2, Body: "world", Creator: "Alice", CreatedAt: "9:00am", CreatedAtTS: now.Add(30 * time.Second)},
		{ID: 3, Body: "again", Creator: "Alice", CreatedAt: "9:01am", CreatedAtTS: now.Add(60 * time.Second)},
	})

	v.renderMessages()
	content := v.viewport.View()

	// "Alice" should appear exactly once — grouped header
	assert.Equal(t, 1, strings.Count(content, "Alice"),
		"consecutive messages from same sender within 5 min should show one header")
}

func TestCampfire_DifferentSender_BreaksGroup(t *testing.T) {
	now := time.Now()
	v := testCampfireWithLines([]workspace.CampfireLineInfo{
		{ID: 1, Body: "hi", Creator: "Alice", CreatedAt: "9:00am", CreatedAtTS: now},
		{ID: 2, Body: "hey", Creator: "Bob", CreatedAt: "9:01am", CreatedAtTS: now.Add(time.Minute)},
	})

	v.renderMessages()
	content := v.viewport.View()

	assert.Contains(t, content, "Alice")
	assert.Contains(t, content, "Bob")
}

func TestCampfire_DateSeparator(t *testing.T) {
	// Use dates far enough in the past that neither is "Today" or "Yesterday"
	day1 := time.Date(2025, 6, 10, 14, 0, 0, 0, time.UTC)
	day2 := time.Date(2025, 6, 11, 9, 0, 0, 0, time.UTC)

	v := testCampfireWithLines([]workspace.CampfireLineInfo{
		{ID: 1, Body: "old msg", Creator: "Alice", CreatedAt: "2:00pm", CreatedAtTS: day1},
		{ID: 2, Body: "new msg", Creator: "Alice", CreatedAt: "9:00am", CreatedAtTS: day2},
	})

	v.renderMessages()
	content := v.viewport.View()

	// Should have two date headers (one per day)
	assert.Contains(t, content, "Jun 10", "should show date for first day")
	assert.Contains(t, content, "Jun 11", "should show date for second day")
}

func TestCampfire_MidnightBoundary_ForcesHeader(t *testing.T) {
	// Same sender, within 5 minutes, but crossing local midnight — header should still appear
	beforeMidnight := time.Date(2025, 6, 10, 23, 58, 0, 0, time.Local)
	afterMidnight := time.Date(2025, 6, 11, 0, 1, 0, 0, time.Local)

	v := testCampfireWithLines([]workspace.CampfireLineInfo{
		{ID: 1, Body: "late night", Creator: "Alice", CreatedAt: "11:58pm", CreatedAtTS: beforeMidnight},
		{ID: 2, Body: "early morning", Creator: "Alice", CreatedAt: "12:01am", CreatedAtTS: afterMidnight},
	})

	v.renderMessages()
	content := v.viewport.View()

	// Both messages should have sender headers (day change forces it)
	assert.Equal(t, 2, strings.Count(content, "Alice"), "both messages should show sender header across day boundary")
	// Both days should have date separators
	assert.Contains(t, content, "Jun 10")
	assert.Contains(t, content, "Jun 11")
}

func TestCampfire_UTCTimestamps_LocalDaySeparators(t *testing.T) {
	// API timestamps arrive in UTC. Day separators should follow local-day
	// boundaries, not UTC boundaries. Use two UTC timestamps that fall on
	// different UTC days but the same local day when local is UTC+5 or similar.
	// Since we can't control the test machine's timezone, we verify the simpler
	// invariant: two UTC timestamps on the same local day produce no separator,
	// while two on different local days do.
	now := time.Now()
	localNoon := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.Local)
	// Convert to UTC — the actual hour will differ, but the local day is the same
	utcNoon := localNoon.UTC()
	utcNoonPlus1 := utcNoon.Add(time.Hour)

	v := testCampfireWithLines([]workspace.CampfireLineInfo{
		{ID: 1, Body: "first", Creator: "Alice", CreatedAt: "12:00pm", CreatedAtTS: utcNoon},
		{ID: 2, Body: "second", Creator: "Bob", CreatedAt: "1:00pm", CreatedAtTS: utcNoonPlus1},
	})

	v.renderMessages()
	content := v.viewport.View()

	// Both messages are on the same local day — should see exactly one date separator
	dateSepCount := strings.Count(content, "──")
	assert.Equal(t, 2, dateSepCount, "same local day should produce one date separator (2 dashes)")
}

func TestSameTimeGroup(t *testing.T) {
	now := time.Now()
	assert.True(t, sameTimeGroup(now, now.Add(4*time.Minute)), "within 5 min should group")
	assert.True(t, sameTimeGroup(now, now.Add(5*time.Minute)), "exactly 5 min should group")
	assert.False(t, sameTimeGroup(now, now.Add(6*time.Minute)), "over 5 min should not group")
	assert.False(t, sameTimeGroup(now, now.Add(-1*time.Minute)), "negative delta should not group")
	assert.False(t, sameTimeGroup(time.Time{}, now), "zero time should not group")
}
