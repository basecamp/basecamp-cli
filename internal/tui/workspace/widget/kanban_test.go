package widget

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func testKanban() *Kanban {
	k := NewKanban(tui.NewStyles())
	k.SetSize(120, 20)
	k.SetFocused(true)
	return k
}

func sampleColumns() []KanbanColumn {
	return []KanbanColumn{
		{
			ID: "1", Title: "Triage", Color: "blue", Count: 2,
			Items: []KanbanCard{
				{ID: "10", Title: "Card A"},
				{ID: "11", Title: "Card B", Completed: true},
			},
		},
		{
			ID: "2", Title: "In Progress", Color: "green", Count: 1,
			Items: []KanbanCard{
				{ID: "20", Title: "Card C", Assignees: "Alice", DueOn: "Mar 15", StepsProgress: "2/5", CommentsCount: 3},
			},
		},
		{
			ID: "3", Title: "Done", Deferred: true, Count: 47,
		},
	}
}

func TestKanban_SetColumns_ClampsFocus(t *testing.T) {
	k := testKanban()
	k.colIdx = 5
	k.cardIdx = 10
	k.SetColumns(sampleColumns())

	assert.Equal(t, 2, k.colIdx, "colIdx should clamp to last column")
	assert.Equal(t, 0, k.cardIdx, "cardIdx should clamp to 0 for deferred column")
}

func TestKanban_SetColumns_PreservesIdentity(t *testing.T) {
	k := testKanban()
	k.SetColumns(sampleColumns())

	// Focus on Card B (col 0, card 1)
	k.cardIdx = 1
	assert.Equal(t, "11", k.FocusedCard().ID)

	// Reorder: swap columns 0 and 1, Card B stays in Triage
	reordered := []KanbanColumn{
		{
			ID: "2", Title: "In Progress", Color: "green", Count: 1,
			Items: []KanbanCard{{ID: "20", Title: "Card C"}},
		},
		{
			ID: "1", Title: "Triage", Color: "blue", Count: 2,
			Items: []KanbanCard{
				{ID: "10", Title: "Card A"},
				{ID: "11", Title: "Card B"},
			},
		},
		{ID: "3", Title: "Done", Deferred: true, Count: 47},
	}
	k.SetColumns(reordered)

	// Cursor should follow Triage (now at index 1) and Card B (still at index 1)
	assert.Equal(t, 1, k.colIdx, "colIdx should follow Triage column by ID")
	card := k.FocusedCard()
	require.NotNil(t, card)
	assert.Equal(t, "11", card.ID, "cursor should follow Card B by ID")
}

func TestKanban_EmptyColumnRoundTrip(t *testing.T) {
	k := testKanban()
	cols := []KanbanColumn{
		{
			ID: "1", Title: "Left", Count: 3,
			Items: []KanbanCard{
				{ID: "10", Title: "A"},
				{ID: "11", Title: "B"},
				{ID: "12", Title: "C"},
			},
		},
		{ID: "2", Title: "Empty", Count: 0, Items: nil},
		{
			ID: "3", Title: "Right", Count: 2,
			Items: []KanbanCard{
				{ID: "30", Title: "X"},
				{ID: "31", Title: "Y"},
			},
		},
	}
	k.SetColumns(cols)

	// Focus card C (index 2) in Left
	k.cardIdx = 2
	assert.Equal(t, "12", k.FocusedCard().ID)

	// Move right through empty column to Right
	k.MoveRight() // now on Empty
	assert.Equal(t, 1, k.colIdx)
	k.MoveRight() // now on Right
	assert.Equal(t, 2, k.colIdx)

	// Move left back through empty column to Left
	k.MoveLeft() // back on Empty
	assert.Equal(t, 1, k.colIdx)
	k.MoveLeft() // back on Left
	assert.Equal(t, 0, k.colIdx)

	// cardIdx should be restored to 2 (Card C)
	assert.Equal(t, 2, k.cardIdx, "cardIdx should be restored after round-trip through empty column")
	assert.Equal(t, "12", k.FocusedCard().ID)
}

func TestKanban_Navigation(t *testing.T) {
	k := testKanban()
	k.SetColumns(sampleColumns())

	// Start at col 0, card 0
	assert.Equal(t, 0, k.colIdx)
	assert.Equal(t, 0, k.cardIdx)

	// Move down within first column
	k.MoveDown()
	assert.Equal(t, 1, k.cardIdx)

	// Can't move past last card
	k.MoveDown()
	assert.Equal(t, 1, k.cardIdx)

	// Move up
	k.MoveUp()
	assert.Equal(t, 0, k.cardIdx)

	// Move right to second column
	k.MoveRight()
	assert.Equal(t, 1, k.colIdx)
	assert.Equal(t, 0, k.cardIdx) // clamped

	// Move right to deferred column
	k.MoveRight()
	assert.Equal(t, 2, k.colIdx)
	assert.Equal(t, 0, k.cardIdx)

	// Can't move past last column
	k.MoveRight()
	assert.Equal(t, 2, k.colIdx)

	// Move left
	k.MoveLeft()
	assert.Equal(t, 1, k.colIdx)
}

func TestKanban_FocusedCard(t *testing.T) {
	k := testKanban()
	k.SetColumns(sampleColumns())

	card := k.FocusedCard()
	require.NotNil(t, card)
	assert.Equal(t, "10", card.ID)

	// Deferred column returns nil
	k.colIdx = 2
	card = k.FocusedCard()
	assert.Nil(t, card)
}

func TestKanban_View_MultiColumn(t *testing.T) {
	k := testKanban()
	k.SetColumns(sampleColumns())

	view := k.View()
	require.NotEmpty(t, view)

	// All column headers should appear
	assert.Contains(t, view, "Triage")
	assert.Contains(t, view, "In Progress")
	assert.Contains(t, view, "Done")

	// Deferred column shows count
	assert.Contains(t, view, "47 cards")
}

func TestKanban_View_FocusedLoupe(t *testing.T) {
	k := testKanban()
	k.SetColumns(sampleColumns())

	view := k.View()

	// Focused card should show cursor
	assert.Contains(t, view, "▸")
	assert.Contains(t, view, "Card A")
}

func TestKanban_View_DetailLine(t *testing.T) {
	k := testKanban()
	k.SetColumns(sampleColumns())

	// Focus on the card with rich metadata
	k.colIdx = 1
	k.cardIdx = 0
	view := k.View()

	// The loupe detail line should contain the structured info
	assert.Contains(t, view, "Alice")
	assert.Contains(t, view, "Mar 15")
	assert.Contains(t, view, "2/5")
	assert.Contains(t, view, "3 comments")
}

func TestKanban_NarrowWidth_WindowsColumns(t *testing.T) {
	k := testKanban()
	// 6 columns, 60 chars wide — can't fit all at minColWidth (20)
	cols := make([]KanbanColumn, 6)
	for i := range cols {
		cols[i] = KanbanColumn{
			ID: string(rune('1' + i)), Title: strings.Repeat("Col", 1),
			Count: 1,
			Items: []KanbanCard{{ID: "1", Title: "Card"}},
		}
	}
	k.SetColumns(cols)
	k.SetSize(60, 20)

	view := k.View()
	require.NotEmpty(t, view)

	// Should have overflow indicator
	assert.Contains(t, view, "▸")
}

func TestKanban_VisibleRange(t *testing.T) {
	k := testKanban()

	// All fit
	start, end := k.visibleRange(3, 5)
	assert.Equal(t, 0, start)
	assert.Equal(t, 3, end)

	// Windowed, focused at start
	k.colIdx = 0
	start, end = k.visibleRange(6, 3)
	assert.Equal(t, 0, start)
	assert.Equal(t, 3, end)

	// Windowed, focused in middle
	k.colIdx = 3
	start, end = k.visibleRange(6, 3)
	assert.Equal(t, 2, start)
	assert.Equal(t, 5, end)

	// Windowed, focused at end
	k.colIdx = 5
	start, end = k.visibleRange(6, 3)
	assert.Equal(t, 3, start)
	assert.Equal(t, 6, end)
}

func TestKanban_ScrollIndicators(t *testing.T) {
	k := testKanban()
	// Column with many cards, small height
	cards := make([]KanbanCard, 20)
	for i := range cards {
		cards[i] = KanbanCard{ID: string(rune('a' + i)), Title: "Card"}
	}
	k.SetColumns([]KanbanColumn{
		{ID: "1", Title: "Backlog", Count: 20, Items: cards},
	})
	k.SetSize(40, 10)

	view := k.View()
	// Should have scroll-down indicator since not all cards fit
	assert.Contains(t, view, "▼")
}

func TestBuildDetailLine(t *testing.T) {
	tests := []struct {
		name string
		card KanbanCard
		want string
	}{
		{
			name: "all fields",
			card: KanbanCard{Assignees: "Alice, Bob", DueOn: "Mar 15", StepsProgress: "3/5", CommentsCount: 2},
			want: "Alice, Bob \u00b7 Mar 15 \u00b7 3/5 \u00b7 2 comments",
		},
		{
			name: "empty",
			card: KanbanCard{},
			want: "",
		},
		{
			name: "only assignees",
			card: KanbanCard{Assignees: "Alice"},
			want: "Alice",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildDetailLine(tt.card)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestKanban_TinyWidth_NoPanic(t *testing.T) {
	k := testKanban()
	k.SetColumns(sampleColumns())

	// Various tiny sizes that previously panicked
	for _, size := range [][2]int{{4, 5}, {1, 1}, {3, 3}, {8, 2}, {0, 10}} {
		k.SetSize(size[0], size[1])
		assert.NotPanics(t, func() {
			k.View()
		}, "View() panicked at size %dx%d", size[0], size[1])
	}
}

func TestKanban_FocusedNoDetail_SingleLine(t *testing.T) {
	k := testKanban()
	// Cards with no metadata — focused should be 1 line, not 2
	k.SetColumns([]KanbanColumn{
		{
			ID: "1", Title: "Backlog", Count: 5,
			Items: []KanbanCard{
				{ID: "1", Title: "A"},
				{ID: "2", Title: "B"},
				{ID: "3", Title: "C"},
				{ID: "4", Title: "D"},
				{ID: "5", Title: "E"},
			},
		},
	})
	// Height = 5: header(1) + divider(1) + card area(3)
	// 5 cards with 3 lines of card area: ▼ takes 1 line, leaving 2 card lines.
	// With no-detail fix: focused A = 1 line, B = 1 line → 2 visible cards.
	// Without fix (always 2 lines): focused A = 2 lines → only 1 visible card.
	k.SetSize(40, 5)
	view := k.View()

	// Both A and B should be visible (proves focused card takes 1 line, not 2)
	assert.Contains(t, view, "A")
	assert.Contains(t, view, "B")

	// Now compare: a card with detail DOES take 2 lines
	k.SetColumns([]KanbanColumn{
		{
			ID: "1", Title: "Backlog", Count: 5,
			Items: []KanbanCard{
				{ID: "1", Title: "A", Assignees: "Alice"}, // has detail → 2 lines
				{ID: "2", Title: "B"},
				{ID: "3", Title: "C"},
				{ID: "4", Title: "D"},
				{ID: "5", Title: "E"},
			},
		},
	})
	view = k.View()

	// Focused A with detail = 2 lines, ▼ = 1 line → only A fits
	assert.Contains(t, view, "A")
	assert.NotContains(t, view, "  B") // B doesn't fit
}

func TestKanban_FocusedCardHeight(t *testing.T) {
	assert.Equal(t, 1, focusedCardHeight(KanbanCard{Title: "Plain"}))
	assert.Equal(t, 2, focusedCardHeight(KanbanCard{Title: "Rich", Assignees: "Alice"}))
	assert.Equal(t, 2, focusedCardHeight(KanbanCard{Title: "Rich", DueOn: "Mar 15"}))
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "", Truncate("hello", 0))
	assert.Equal(t, "h", Truncate("hello", 1))
	assert.Equal(t, "hel", Truncate("hello", 3))
	assert.Equal(t, "hello", Truncate("hello", 5))
	assert.Equal(t, "hello", Truncate("hello", 10))
	assert.Equal(t, "hell...", Truncate("hello world", 7))
}

func TestColumnColor_ValidNames(t *testing.T) {
	theme := tui.NewStyles().Theme()
	// Known colors should return non-theme-foreground values
	for _, name := range []string{"red", "orange", "yellow", "green", "blue", "aqua", "purple", "pink", "brown"} {
		c := columnColor(name, theme)
		assert.NotEqual(t, theme.Foreground, c, "color %q should differ from default foreground", name)
	}
	// Unknown falls back to foreground
	assert.Equal(t, theme.Foreground, columnColor("neon", theme))
}
