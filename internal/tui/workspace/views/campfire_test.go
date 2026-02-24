package views

import (
	"context"
	"testing"

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
			name:  "emoji rune count",
			line:  "🎉🎊🎈 party time celebrations",
			width: 15,
			want:  "🎉🎊🎈 party time\ncelebrations",
		},
		{
			name:  "long emoji word",
			line:  "🎉🎊🎈🎆🎇🧨✨🎃",
			width: 4,
			want:  "🎉🎊🎈🎆\n🎇🧨✨🎃",
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
