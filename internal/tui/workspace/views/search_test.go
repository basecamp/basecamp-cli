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

// --- Submit behavior ---

func TestSearch_EmptyQuerySkipsSearch(t *testing.T) {
	v := testSearchView()
	v.query = "test"
	v.textInput.SetValue("")

	cmd := v.submitQuery()
	assert.Nil(t, cmd, "empty query should be a no-op")
}

func TestSearch_DebounceDeduplication(t *testing.T) {
	v := testSearchView()
	v.query = "test"
	v.debounceSeq = 5

	// Debounce with matching seq but same query is still a no-op
	_, cmd := v.Update(searchDebounceMsg{query: "test", seq: 5})
	assert.Nil(t, cmd, "debounce for same query should be skipped")

	// Debounce with stale seq should be discarded
	_, cmd = v.Update(searchDebounceMsg{query: "new", seq: 3})
	assert.Nil(t, cmd, "debounce with stale seq should be discarded")
}

// --- Partial failure surfacing ---

func TestSearch_PartialFailure_ShowsStatus(t *testing.T) {
	v := testSearchView()
	v.query = "widgets"

	msg := workspace.SearchResultsMsg{
		Results: []workspace.SearchResultInfo{
			{ID: 1, Title: "Found", AccountID: "a1"},
		},
		Query:      "widgets",
		PartialErr: fmt.Errorf("could not search: Acme Corp"),
	}

	cmd := v.handleResults(msg)
	require.NotNil(t, cmd, "partial failure should return a cmd")

	result := cmd()
	status, ok := result.(workspace.StatusMsg)
	require.True(t, ok, "cmd should produce StatusMsg")
	assert.Contains(t, status.Text, "Acme Corp")
	assert.True(t, status.IsError)
}

func TestSearch_PartialFailure_NoResults_ReportsFullError(t *testing.T) {
	v := testSearchView()
	v.query = "widgets"

	// When PartialErr is nil but Err is set, it's a full failure
	msg := workspace.SearchResultsMsg{
		Query: "widgets",
		Err:   fmt.Errorf("could not search: Acme Corp"),
	}

	cmd := v.handleResults(msg)
	require.NotNil(t, cmd, "full failure should return an error cmd")

	result := cmd()
	errMsg, ok := result.(workspace.ErrorMsg)
	require.True(t, ok, "cmd should produce ErrorMsg")
	assert.Contains(t, errMsg.Context, "searching")
}

// --- Excerpts ---

func TestSearch_HandleResults_ExcerptAsDescription(t *testing.T) {
	v := testSearchView()
	v.query = "login"

	msg := workspace.SearchResultsMsg{
		Results: []workspace.SearchResultInfo{
			{ID: 1, Title: "Fix login bug", Excerpt: "The login form crashes when...", Project: "Alpha", AccountID: "a1"},
			{ID: 2, Title: "Deploy notes", Project: "Beta", AccountID: "a1"},
		},
		Query: "login",
	}

	v.handleResults(msg)

	items := v.list.Items()
	require.Len(t, items, 2)
	assert.Equal(t, "The login form crashes when...", items[0].Description, "result with excerpt should use it as description")
	assert.Equal(t, "Beta", items[1].Description, "result without excerpt should fall back to project")
}

func TestTruncateExcerpt(t *testing.T) {
	assert.Equal(t, "hello", truncateExcerpt("hello", 10))
	assert.Equal(t, "hello worl…", truncateExcerpt("hello world here", 10))
	assert.Equal(t, "clean text", truncateExcerpt("<b>clean</b> text", 20))
}

// --- FocusMsg ---

func TestSearch_FocusMsg_RefocusesInput(t *testing.T) {
	v := testSearchView()

	// Blur the text input to simulate navigating away
	v.textInput.Blur()
	require.False(t, v.textInput.Focused())

	// FocusMsg should refocus the text input
	v.Update(workspace.FocusMsg{})
	assert.True(t, v.textInput.Focused(), "FocusMsg should refocus text input when input has focus")
}

func TestSearch_FocusMsg_ListFocused_NoOp(t *testing.T) {
	v := testSearchView()
	v.toggleFocus() // Switch to list focus
	require.Equal(t, searchFocusList, v.focus)

	_, cmd := v.Update(workspace.FocusMsg{})
	assert.Nil(t, cmd, "FocusMsg should be no-op when list is focused")
}

// --- Narrow width ---

func TestSearch_NarrowWidth_NoNegative(t *testing.T) {
	v := testSearchView()

	// SetSize with an extremely small width — must not panic.
	v.SetSize(2, 10)
	assert.GreaterOrEqual(t, v.textInput.Width, 0, "textInput.Width should never go negative")

	// Exercise View to confirm rendering doesn't panic either.
	out := v.View()
	assert.NotEmpty(t, out)
}
