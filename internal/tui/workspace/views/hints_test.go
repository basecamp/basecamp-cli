package views

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

func TestFilterHints_ReturnsCorrectBindings(t *testing.T) {
	hints := filterHints()

	require.Len(t, hints, 3)
	assert.Equal(t, "esc", hints[0].Help().Key)
	assert.Equal(t, "cancel", hints[0].Help().Desc)
	assert.Equal(t, "enter", hints[1].Help().Key)
	assert.Equal(t, "apply", hints[1].Help().Desc)
	assert.Equal(t, "j/k", hints[2].Help().Key)
	assert.Equal(t, "results", hints[2].Help().Desc)
}

// testPool creates a minimal pool for testing. If preload is true, the pool
// is pre-populated with data so it reports as non-pending.
func testPool[T any](key string, val T, preload bool) *data.Pool[T] {
	p := data.NewPool[T](key, data.PoolConfig{
		FreshTTL: time.Hour,
	}, func(ctx context.Context) (T, error) {
		return val, nil
	})
	if preload {
		p.Set(val)
	}
	return p
}

// testHome creates a Home with enough structure to test ShortHelp().
func testHome(preloadPools bool) *Home {
	styles := tui.NewStyles()
	list := widget.NewList(styles)
	list.SetSize(80, 20)
	list.SetFocused(true)

	heyPool := testPool("hey", []data.ActivityEntryInfo(nil), preloadPools)
	assignPool := testPool("assign", []data.AssignmentInfo(nil), preloadPools)
	projectPool := testPool("project", []data.ProjectInfo(nil), preloadPools)

	return &Home{
		styles:      styles,
		list:        list,
		heyPool:     heyPool,
		assignPool:  assignPool,
		projectPool: projectPool,
		itemMeta:    make(map[string]homeItemMeta),
	}
}

func TestHomeShortHelp_LoadingReturnsNil(t *testing.T) {
	v := testHome(false) // pools pending, list empty
	hints := v.ShortHelp()
	assert.Nil(t, hints, "loading state should return nil hints")
}

func TestHomeShortHelp_DefaultHintsWhenHasItems(t *testing.T) {
	v := testHome(true) // pools loaded (non-pending)
	v.list.SetItems([]widget.ListItem{
		{ID: "item:1", Title: "Something"},
	})

	hints := v.ShortHelp()
	require.NotNil(t, hints)
	// Should be default hints since no meta for this item
	require.Len(t, hints, 3)
	assert.Equal(t, "j/k", hints[0].Help().Key)
	assert.Equal(t, "enter", hints[1].Help().Key)
	assert.Equal(t, "p", hints[2].Help().Key)
}

func TestHomeShortHelp_SectionHeader(t *testing.T) {
	v := testHome(true)
	v.list.SetItems([]widget.ListItem{
		{Title: "RECENTS", Header: true},
		{ID: "item:1", Title: "Something"},
	})
	// List selects first non-header by default, so force cursor to header
	// by making all items headers
	v.list.SetItems([]widget.ListItem{
		{Title: "RECENTS", Header: true},
	})

	hints := v.ShortHelp()
	require.NotNil(t, hints)
	require.Len(t, hints, 2)
	assert.Equal(t, "j/k", hints[0].Help().Key)
	assert.Equal(t, "p", hints[1].Help().Key)
}

func TestHomeShortHelp_ProjectItem(t *testing.T) {
	v := testHome(true)
	v.itemMeta["project:1"] = homeItemMeta{
		viewTarget: workspace.ViewDock,
		projectID:  1,
	}
	v.list.SetItems([]widget.ListItem{
		{ID: "project:1", Title: "My Project"},
	})

	hints := v.ShortHelp()
	require.NotNil(t, hints)
	require.Len(t, hints, 2)
	assert.Equal(t, "enter", hints[0].Help().Key)
	assert.Equal(t, "open project", hints[0].Help().Desc)
	assert.Equal(t, "p", hints[1].Help().Key)
}

func TestHomeShortHelp_CampfireItem(t *testing.T) {
	v := testHome(true)
	v.itemMeta["campfire:1"] = homeItemMeta{
		viewTarget: workspace.ViewCampfire,
		projectID:  1,
	}
	v.list.SetItems([]widget.ListItem{
		{ID: "campfire:1", Title: "Campfire Room"},
	})

	hints := v.ShortHelp()
	require.NotNil(t, hints)
	require.Len(t, hints, 2)
	assert.Equal(t, "open campfire", hints[0].Help().Desc)
}

func TestHomeShortHelp_RecordingWithType(t *testing.T) {
	v := testHome(true)
	v.itemMeta["rec:1"] = homeItemMeta{
		viewTarget: workspace.ViewDetail,
		recordType: "Message",
	}
	v.list.SetItems([]widget.ListItem{
		{ID: "rec:1", Title: "Weekly Update"},
	})

	hints := v.ShortHelp()
	require.NotNil(t, hints)
	assert.Equal(t, "open message", hints[0].Help().Desc)
}

func TestHomeShortHelp_RecordTypeTruncatedAt15(t *testing.T) {
	v := testHome(true)
	v.itemMeta["rec:1"] = homeItemMeta{
		viewTarget: workspace.ViewDetail,
		recordType: "AutomaticCheckin", // 16 chars
	}
	v.list.SetItems([]widget.ListItem{
		{ID: "rec:1", Title: "Friday Check-in"},
	})

	hints := v.ShortHelp()
	require.NotNil(t, hints)
	desc := hints[0].Help().Desc
	// "open " + 15 chars max
	assert.LessOrEqual(t, len(desc), 20)
	assert.True(t, len(desc) > len("open "))
}

func TestHomeShortHelp_FilteringActive(t *testing.T) {
	v := testHome(true)
	v.list.SetItems([]widget.ListItem{
		{ID: "item:1", Title: "Something"},
	})
	v.list.StartFilter()

	hints := v.ShortHelp()
	require.Len(t, hints, 3)
	assert.Equal(t, "esc", hints[0].Help().Key)
	assert.Equal(t, "cancel", hints[0].Help().Desc)
}
