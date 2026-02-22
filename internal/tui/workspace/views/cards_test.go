package views

import (
	"context"
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

func sampleColumns() []data.CardColumnInfo {
	return []data.CardColumnInfo{
		{
			ID: 1, Title: "Triage", Color: "#ff0000",
			Cards: []data.CardInfo{
				{ID: 100, Title: "Fix bug", Position: 1},
				{ID: 101, Title: "Add tests", Position: 2},
			},
			CardsCount: 2,
		},
		{
			ID: 2, Title: "In Progress", Color: "#00ff00",
			Cards: []data.CardInfo{
				{ID: 200, Title: "Build feature", Position: 1},
			},
			CardsCount: 1,
		},
		{
			ID: 3, Title: "Done", Color: "#0000ff",
			Deferred:   true,
			CardsCount: 5,
		},
	}
}

// testCardsView creates a Cards view with pre-populated columns for unit testing.
func testCardsView() *Cards {
	session := workspace.NewTestSessionWithScope(workspace.Scope{
		AccountID: "acct1",
		ProjectID: 42,
		ToolID:    10,
	})

	styles := tui.NewStyles()
	cols := sampleColumns()

	pool := data.NewMutatingPool[[]data.CardColumnInfo](
		"cards:42:10",
		data.PoolConfig{FreshTTL: time.Hour},
		func(context.Context) ([]data.CardColumnInfo, error) {
			return cols, nil
		},
	)
	pool.Set(cols)

	kanban := widget.NewKanban(styles)
	kanban.SetSize(120, 24)

	v := &Cards{
		session: session,
		pool:    pool,
		styles:  styles,
		keys:    defaultCardsKeyMap(),
		kanban:  kanban,
		loading: false,
		columns: cols,
		width:   120,
		height:  24,
	}

	v.syncKanban()
	return v
}

// --- Init ---

func TestCards_Init_SyncsKanban(t *testing.T) {
	v := testCardsView()
	cmd := v.Init()

	// Pool is pre-populated and fresh, so Init should sync the kanban.
	assert.False(t, v.loading, "should not be loading when pool is pre-populated")

	// Kanban should have columns set
	card := v.kanban.FocusedCard()
	require.NotNil(t, card, "kanban should have a focused card after init")
	assert.Equal(t, "100", card.ID)
	assert.Equal(t, "Fix bug", card.Title)

	_ = cmd
}

// --- Navigation h/j/k/l ---

func TestCards_Navigation_HJKL(t *testing.T) {
	v := testCardsView()

	// Initially focused on column 0, card 0 ("Fix bug")
	assert.Equal(t, 0, v.kanban.FocusedColumn())
	card := v.kanban.FocusedCard()
	require.NotNil(t, card)
	assert.Equal(t, "Fix bug", card.Title)

	// j moves down within column
	v.handleKey(runeKey('j'))
	card = v.kanban.FocusedCard()
	require.NotNil(t, card)
	assert.Equal(t, "Add tests", card.Title)

	// k moves back up
	v.handleKey(runeKey('k'))
	card = v.kanban.FocusedCard()
	require.NotNil(t, card)
	assert.Equal(t, "Fix bug", card.Title)

	// l moves to next column
	v.handleKey(runeKey('l'))
	assert.Equal(t, 1, v.kanban.FocusedColumn())
	card = v.kanban.FocusedCard()
	require.NotNil(t, card)
	assert.Equal(t, "Build feature", card.Title)

	// h moves back to previous column
	v.handleKey(runeKey('h'))
	assert.Equal(t, 0, v.kanban.FocusedColumn())
}

// --- Move mode ---

func TestCards_MoveMode_Flow(t *testing.T) {
	v := testCardsView()

	// Focus on "Fix bug" (col 0, card 0)
	card := v.kanban.FocusedCard()
	require.NotNil(t, card)
	assert.Equal(t, "Fix bug", card.Title)

	// Press 'm' to enter move mode
	cmd := v.handleKey(runeKey('m'))
	require.NotNil(t, cmd, "m should return a status cmd")
	assert.True(t, v.moving, "should be in move mode")
	assert.True(t, v.IsModal(), "should be modal during move mode")
	assert.Equal(t, 0, v.moveSourceCol)
	assert.Equal(t, int64(100), v.moveSourceCard)
	assert.Equal(t, 0, v.moveTargetCol)

	// Press 'l' to move target to column 1
	v.handleMoveKey(runeKey('l'))
	assert.Equal(t, 1, v.moveTargetCol)

	// Press 'l' again to move target to column 2
	v.handleMoveKey(runeKey('l'))
	assert.Equal(t, 2, v.moveTargetCol)

	// Press 'h' to move target back to column 1
	v.handleMoveKey(runeKey('h'))
	assert.Equal(t, 1, v.moveTargetCol)

	// Verify the status cmd produced by enterMoveMode
	msg := cmd()
	status, ok := msg.(workspace.StatusMsg)
	require.True(t, ok, "m should produce StatusMsg")
	assert.Contains(t, status.Text, "Move mode")
}

func TestCards_MoveMode_EscCancels(t *testing.T) {
	v := testCardsView()

	// Enter move mode
	v.handleKey(runeKey('m'))
	assert.True(t, v.moving)

	// Move target right
	v.handleMoveKey(runeKey('l'))
	assert.Equal(t, 1, v.moveTargetCol)

	// Press Esc to cancel
	v.handleMoveKey(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, v.moving, "moving should be false after esc")
}

// --- Inline create ---

func TestCards_InlineCreate_Flow(t *testing.T) {
	v := testCardsView()

	// Simulate entering create mode (bypass textinput.Focus which requires
	// a running tea.Program for cursor blink)
	v.creating = true
	assert.True(t, v.InputActive(), "InputActive should be true during create")
	assert.True(t, v.IsModal(), "should be modal during create")

	// Empty Enter exits create mode without submitting
	cmd := v.handleCreatingKey(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Nil(t, cmd, "enter with empty input should return nil")
	assert.False(t, v.creating, "creating should be false after empty submit")
}

func TestCards_InlineCreate_EscCancels(t *testing.T) {
	v := testCardsView()

	// Simulate entering create mode
	v.creating = true
	v.createInput.SetValue("Draft card")

	cmd := v.handleCreatingKey(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Nil(t, cmd, "esc should return nil cmd")
	assert.False(t, v.creating, "creating should be false after esc")
}

func TestCards_InlineCreate_DeferredColumnBlocked(t *testing.T) {
	v := testCardsView()

	// Navigate to the deferred column (index 2)
	v.kanban.FocusColumn(2)
	assert.Equal(t, 2, v.kanban.FocusedColumn())

	// enterCreateMode checks for deferred and returns a status cmd
	cmd := v.enterCreateMode()
	require.NotNil(t, cmd, "n on deferred column should return a status cmd")
	assert.False(t, v.creating, "should not enter create mode on deferred column")

	msg := cmd()
	status, ok := msg.(workspace.StatusMsg)
	require.True(t, ok, "should produce StatusMsg")
	assert.Contains(t, status.Text, "deferred")
}

// --- Pool update preserves cursor ---

func TestCards_PoolUpdate_PreservesCursor(t *testing.T) {
	v := testCardsView()

	// Focus on "Add tests" (col 0, card 1)
	v.kanban.MoveDown()
	card := v.kanban.FocusedCard()
	require.NotNil(t, card)
	assert.Equal(t, "Add tests", card.Title)

	// Simulate a pool update with same data plus a new card
	updatedCols := []data.CardColumnInfo{
		{
			ID: 1, Title: "Triage", Color: "#ff0000",
			Cards: []data.CardInfo{
				{ID: 100, Title: "Fix bug", Position: 1},
				{ID: 101, Title: "Add tests", Position: 2},
				{ID: 103, Title: "Refactor", Position: 3},
			},
			CardsCount: 3,
		},
		{
			ID: 2, Title: "In Progress", Color: "#00ff00",
			Cards: []data.CardInfo{
				{ID: 200, Title: "Build feature", Position: 1},
			},
			CardsCount: 1,
		},
	}
	v.columns = updatedCols
	v.syncKanban()

	// Focus should be preserved on "Add tests" (same ID)
	card = v.kanban.FocusedCard()
	require.NotNil(t, card)
	assert.Equal(t, "Add tests", card.Title, "cursor should be preserved on pool update")
}

// --- Boost focused card ---

func TestCards_BoostFocusedCard(t *testing.T) {
	v := testCardsView()

	// Focus on first card
	card := v.kanban.FocusedCard()
	require.NotNil(t, card)
	assert.Equal(t, "Fix bug", card.Title)

	for _, r := range []rune{'b', 'B'} {
		cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		require.NotNil(t, cmd, "expected boost cmd for %q", string(r))

		msg := cmd()
		open, ok := msg.(workspace.OpenBoostPickerMsg)
		require.True(t, ok, "expected OpenBoostPickerMsg for %q", string(r))
		assert.Equal(t, int64(42), open.Target.ProjectID)
		assert.Equal(t, int64(100), open.Target.RecordingID)
		assert.Equal(t, "Fix bug", open.Target.Title)
	}
}

// --- Move to deferred focuses next ---

func TestCards_MoveToDeferred_TargetDetected(t *testing.T) {
	v := testCardsView()

	// Focus on "Fix bug" (col 0, card 0)
	card := v.kanban.FocusedCard()
	require.NotNil(t, card)
	assert.Equal(t, "Fix bug", card.Title)

	// Enter move mode
	v.handleKey(runeKey('m'))
	require.True(t, v.moving)
	assert.Equal(t, 0, v.moveSourceCol)
	assert.Equal(t, int64(100), v.moveSourceCard)

	// Move target to the deferred column (index 2)
	v.handleMoveKey(runeKey('l')) // target col 1
	v.handleMoveKey(runeKey('l')) // target col 2
	assert.Equal(t, 2, v.moveTargetCol)

	// The target column is deferred
	assert.True(t, v.columns[v.moveTargetCol].Deferred,
		"column 2 should be detected as deferred")

	// Cannot go past the last column
	v.handleMoveKey(runeKey('l'))
	assert.Equal(t, 2, v.moveTargetCol, "should clamp at last column")
}

// --- Title ---

func TestCards_Title(t *testing.T) {
	v := testCardsView()
	assert.Equal(t, "Card Table", v.Title())
}

// --- Modal semantics ---

func TestCards_IsModal_ReflectsMoveAndCreate(t *testing.T) {
	v := testCardsView()

	assert.False(t, v.IsModal(), "not modal in normal mode")

	v.moving = true
	assert.True(t, v.IsModal(), "modal during move mode")
	v.moving = false

	v.creating = true
	assert.True(t, v.IsModal(), "modal during create mode")
	v.creating = false

	assert.False(t, v.IsModal())
}

// --- Move same column is no-op ---

func TestCards_MoveMode_SameColumn_EscReverts(t *testing.T) {
	v := testCardsView()

	v.handleKey(runeKey('m'))
	require.True(t, v.moving)
	assert.Equal(t, 0, v.moveTargetCol)

	// Without changing target, Esc to cancel — same effect as confirming same column
	v.handleMoveKey(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, v.moving, "esc should exit move mode")
}
