package views

import (
	"fmt"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// testSearchView builds a Search view with pre-populated state for unit tests.
// Session is nil — only the structural fields (list, textInput, focus) are
// exercised. Tests that trigger SDK calls (fetchResults) are not covered here.
func testSearchView() *Search {
	styles := tui.NewStyles()

	list := widget.NewList(styles)
	list.SetEmptyText("Type a query and press Enter to search.")
	list.SetFocused(false)
	list.SetSize(80, 20)

	return &Search{
		styles:     styles,
		keys:       defaultSearchKeyMap(),
		textInput:  newTestTextInput(),
		list:       list,
		focus:      searchFocusInput,
		resultMeta: make(map[string]workspace.SearchResultInfo),
		width:      80,
		height:     24,
	}
}

// testSearchViewWithResults returns a Search view pre-loaded with search results.
func testSearchViewWithResults() *Search {
	v := testSearchView()

	results := []workspace.SearchResultInfo{
		{ID: 1, Title: "Fix login bug", Type: "Todo", Project: "Alpha", ProjectID: 10, AccountID: "a1"},
		{ID: 2, Title: "Weekly update", Type: "Message", Project: "Beta", ProjectID: 20, AccountID: "a1"},
		{ID: 3, Title: "Design review", Type: "Todo", Project: "Gamma", ProjectID: 30, AccountID: "a2"},
	}

	v.query = "test"
	v.resultMeta = make(map[string]workspace.SearchResultInfo, len(results))
	items := make([]widget.ListItem, 0, len(results))
	for _, r := range results {
		id := formatID(r.ID)
		v.resultMeta[id] = r
		items = append(items, widget.ListItem{
			ID:          id,
			Title:       r.Title,
			Description: r.Project,
		})
	}
	v.list.SetItems(items)

	return v
}

func newTestTextInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "Search Basecamp..."
	ti.CharLimit = 256
	ti.Focus()
	return ti
}

func formatID(id int64) string {
	return fmt.Sprintf("%d", id)
}

// --- InputActive ---

func TestSearch_InputActive(t *testing.T) {
	v := testSearchView()

	// Input is focused by default — InputActive should be true.
	assert.True(t, v.InputActive(), "InputActive should be true when text input has focus")

	// Switch focus to list — InputActive should be false (unless filtering).
	v.toggleFocus()
	assert.False(t, v.InputActive(), "InputActive should be false when list has focus and not filtering")

	// Start filtering on the list — InputActive should be true again.
	v.list.StartFilter()
	assert.True(t, v.InputActive(), "InputActive should be true when list is filtering")
}

// --- Tab switches focus ---

func TestSearch_TabSwitchesFocus(t *testing.T) {
	v := testSearchView()
	require.Equal(t, searchFocusInput, v.focus, "initial focus should be on input")

	// Tab toggles to list
	v.Update(tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, searchFocusList, v.focus, "Tab should switch focus to results list")

	// Tab toggles back to input
	v.Update(tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, searchFocusInput, v.focus, "second Tab should return focus to input")
}

// --- Enter opens result ---

func TestSearch_EnterOpensResult(t *testing.T) {
	v := testSearchViewWithResults()

	// Switch to list focus so Enter opens the selected result.
	v.toggleFocus()
	require.Equal(t, searchFocusList, v.focus)

	// Need a session for openSelected to build scope and navigate.
	v.session = workspace.NewTestSession()

	cmd := v.openSelected()
	require.NotNil(t, cmd, "Enter on a result should return a navigate cmd")

	msg := cmd()
	nav, ok := msg.(workspace.NavigateMsg)
	require.True(t, ok, "cmd should produce NavigateMsg")
	assert.Equal(t, workspace.ViewDetail, nav.Target)
	assert.Equal(t, int64(1), nav.Scope.RecordingID, "should navigate to first result")
	assert.Equal(t, int64(10), nav.Scope.ProjectID)
	assert.Equal(t, "a1", nav.Scope.AccountID)
}

// --- Empty query ---

func TestSearch_EmptyQuery(t *testing.T) {
	v := testSearchView()

	// Leave textInput empty and press Enter — submitQuery should return nil.
	cmd := v.submitQuery()
	assert.Nil(t, cmd, "empty query should not trigger search")
	assert.Empty(t, v.query, "query should remain empty")
	assert.False(t, v.searching, "searching should not be set")
}

func TestSearch_WhitespaceOnlyQuery(t *testing.T) {
	v := testSearchView()
	v.textInput.SetValue("   ")

	cmd := v.submitQuery()
	assert.Nil(t, cmd, "whitespace-only query should not trigger search")
}

// --- IsModal ---

func TestSearch_IsModal_ReflectsListFocus(t *testing.T) {
	v := testSearchView()

	assert.False(t, v.IsModal(), "not modal when input focused")

	v.toggleFocus()
	assert.True(t, v.IsModal(), "modal when list focused")

	v.toggleFocus()
	assert.False(t, v.IsModal(), "not modal after returning to input")
}

// --- Esc behavior ---

func TestSearch_EscFromInput_NavigatesBack(t *testing.T) {
	v := testSearchView()
	require.Equal(t, searchFocusInput, v.focus)

	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd)

	msg := cmd()
	_, ok := msg.(workspace.NavigateBackMsg)
	assert.True(t, ok, "Esc from input should produce NavigateBackMsg")
}

func TestSearch_EscFromList_ReturnsFocusToInput(t *testing.T) {
	v := testSearchView()
	v.toggleFocus()
	require.Equal(t, searchFocusList, v.focus)

	v.handleKey(tea.KeyMsg{Type: tea.KeyEsc})

	assert.Equal(t, searchFocusInput, v.focus, "Esc from list should return focus to input")
}

// --- Debounce deduplication ---

func TestSearch_DuplicateQuerySkipsSearch(t *testing.T) {
	v := testSearchView()
	v.query = "test"
	v.textInput.SetValue("test")

	cmd := v.submitQuery()
	assert.Nil(t, cmd, "submitting the same query again should be a no-op")
}
