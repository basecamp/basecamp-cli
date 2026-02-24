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

func sampleQuestions() []data.CheckinQuestionInfo {
	return []data.CheckinQuestionInfo{
		{ID: 1, Title: "What did you work on today?", Frequency: "every_day", AnswersCount: 5},
		{ID: 2, Title: "What will you do this week?", Frequency: "every_week", AnswersCount: 3, Paused: true},
	}
}

func sampleAnswers() []data.CheckinAnswerInfo {
	return []data.CheckinAnswerInfo{
		{ID: 100, Creator: "Alice", CreatedAt: time.Date(2026, 2, 20, 10, 0, 0, 0, time.UTC), Content: "<p>Shipped the TUI feature.</p>", CommentsCount: 2},
		{ID: 101, Creator: "Bob", CreatedAt: time.Date(2026, 2, 21, 14, 0, 0, 0, time.UTC), Content: "<p>Reviewed pull requests and fixed bugs.</p>", CommentsCount: 0},
	}
}

// testCheckinsView creates a Checkins view with pre-populated questions.
func testCheckinsView() *Checkins {
	session := workspace.NewTestSessionWithScope(workspace.Scope{
		AccountID: "acct1",
		ProjectID: 42,
		ToolID:    10,
	})

	styles := tui.NewStyles()

	questionPool := data.NewPool[[]data.CheckinQuestionInfo](
		"checkins:42:10",
		data.PoolConfig{FreshTTL: time.Hour},
		func(context.Context) ([]data.CheckinQuestionInfo, error) {
			return sampleQuestions(), nil
		},
	)
	questionPool.Set(sampleQuestions())

	listQuestions := widget.NewList(styles)
	listQuestions.SetEmptyText("No check-in questions found.")
	listQuestions.SetFocused(true)
	listQuestions.SetSize(40, 20)

	listAnswers := widget.NewList(styles)
	listAnswers.SetEmptyText("Select a question to view answers.")
	listAnswers.SetFocused(false)
	listAnswers.SetSize(60, 20)

	split := widget.NewSplitPane(styles, 0.35)
	split.SetSize(120, 24)

	composer := widget.NewComposer(styles,
		widget.WithMode(widget.ComposerRich),
		widget.WithAutoExpand(false),
		widget.WithPlaceholder("Your answer (Markdown)..."),
	)

	v := &Checkins{
		session:       session,
		pool:          questionPool,
		styles:        styles,
		split:         split,
		listQuestions: listQuestions,
		listAnswers:   listAnswers,
		focus:         checkinsPaneLeft,
		width:         120,
		height:        24,
		composer:      composer,
	}

	v.questions = sampleQuestions()
	v.syncQuestions()
	return v
}

// testCheckinsViewWithAnswers creates a view with answers populated in the right pane.
func testCheckinsViewWithAnswers() *Checkins {
	v := testCheckinsView()
	v.selectedQuestionID = 1
	v.focus = checkinsPaneRight
	v.listQuestions.SetFocused(false)
	v.listAnswers.SetFocused(true)
	v.syncAnswers(1, sampleAnswers())
	return v
}

// --- Init ---

func TestCheckins_Init_LoadsQuestions(t *testing.T) {
	v := testCheckinsView()
	v.Init()

	assert.False(t, v.loading, "should not be loading when pool is pre-populated")

	items := v.listQuestions.Items()
	require.Len(t, items, 2)
	assert.Equal(t, "What did you work on today?", items[0].Title)
	assert.Equal(t, "What will you do this week?", items[1].Title)
}

// --- Tab switching ---

func TestCheckins_SwitchTab_TogglesFocus(t *testing.T) {
	v := testCheckinsView()

	assert.Equal(t, checkinsPaneLeft, v.focus)

	v.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, checkinsPaneRight, v.focus)

	v.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, checkinsPaneLeft, v.focus)
}

// --- Select question ---

func TestCheckins_SelectQuestion_SetsSelectedID(t *testing.T) {
	v := testCheckinsView()

	v.selectQuestion("1")
	assert.Equal(t, int64(1), v.selectedQuestionID)
}

func TestCheckins_SelectQuestion_SameID_NoOp(t *testing.T) {
	v := testCheckinsView()
	v.selectedQuestionID = 1

	cmd := v.selectQuestion("1")
	assert.Nil(t, cmd, "selecting same question should be no-op")
}

// --- Left-pane enter selects question ---

func TestCheckins_EnterOnLeftPane_SelectsQuestion(t *testing.T) {
	v := testCheckinsView()

	// No question selected yet
	assert.Equal(t, int64(0), v.selectedQuestionID)

	// Press enter on left pane — should select the first question
	v.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, int64(1), v.selectedQuestionID, "enter on left pane should select the first question")
	assert.True(t, v.loadingAnswers, "should be loading answers after selection")
}

// --- PoolUpdatedMsg for answers ---

func TestCheckins_PoolUpdatedMsg_SyncsAnswers(t *testing.T) {
	v := testCheckinsViewWithAnswers()

	// Pre-create the answers pool and populate it
	answersPool := v.session.Hub().CheckinAnswers(42, 1)
	answersPool.Set(sampleAnswers())

	v.Update(data.PoolUpdatedMsg{Key: answersPool.Key()})

	items := v.listAnswers.Items()
	require.Len(t, items, 2)
	assert.Contains(t, items[0].Title, "Alice")
	assert.Contains(t, items[1].Title, "Bob")
}

func TestCheckins_PoolUpdatedMsg_WrongQuestionIgnored(t *testing.T) {
	v := testCheckinsViewWithAnswers()

	// Create a pool for a different question
	otherPool := v.session.Hub().CheckinAnswers(42, 999)
	otherPool.Set(sampleAnswers())

	// Should not update the right pane
	prevItems := v.listAnswers.Items()
	v.Update(data.PoolUpdatedMsg{Key: otherPool.Key()})
	assert.Equal(t, prevItems, v.listAnswers.Items())
}

// --- loadAnswers stale-while-revalidate ---

func TestCheckins_LoadAnswers_StaleCache_KeepsItemsVisible(t *testing.T) {
	v := testCheckinsView()

	// Pre-populate the answers pool for question 1, then mark stale
	answersPool := v.session.Hub().CheckinAnswers(42, 1)
	answersPool.Set(sampleAnswers()) // Fresh
	answersPool.Invalidate()         // Fresh → Stale
	require.Equal(t, data.StateStale, answersPool.Get().State)
	require.True(t, answersPool.Get().Usable(), "stale snapshot should be usable")

	// Select question 1 — triggers loadAnswers
	cmd := v.selectQuestion("1")

	// Stale cache should be rendered immediately, no spinner
	assert.False(t, v.loadingAnswers, "should not show spinner for stale cache")
	items := v.listAnswers.Items()
	require.Len(t, items, 2, "stale cached answers should be visible")
	assert.Contains(t, items[0].Title, "Alice")

	// Should still return a fetch cmd for background revalidation
	assert.NotNil(t, cmd, "should return a background fetch cmd")
}

func TestCheckins_LoadAnswers_NoCache_ShowsSpinner(t *testing.T) {
	v := testCheckinsView()

	// No pre-populated answers for question 1 — pool is empty
	cmd := v.selectQuestion("1")

	assert.True(t, v.loadingAnswers, "should show spinner with no cache")
	assert.Empty(t, v.listAnswers.Items(), "should have no items")
	assert.NotNil(t, cmd, "should return a fetch cmd")
}

// --- Enter on right pane opens answer in Detail ---

func TestCheckins_EnterOnRightPane_NavigatesToDetail(t *testing.T) {
	v := testCheckinsViewWithAnswers()

	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd, "enter should return a nav command")

	msg := cmd()
	navMsg, ok := msg.(workspace.NavigateMsg)
	require.True(t, ok, "should produce NavigateMsg, got %T", msg)
	assert.Equal(t, workspace.ViewDetail, navMsg.Target)
	assert.Equal(t, "Question::Answer", navMsg.Scope.RecordingType)
	assert.Equal(t, int64(100), navMsg.Scope.RecordingID)
}

// --- InputActive ---

func TestCheckins_InputActive_FilteringLeftPane(t *testing.T) {
	v := testCheckinsView()

	assert.False(t, v.InputActive())

	v.listQuestions.StartFilter()
	assert.True(t, v.InputActive())
}

func TestCheckins_InputActive_FilteringRightPane(t *testing.T) {
	v := testCheckinsViewWithAnswers()

	v.listAnswers.StartFilter()
	assert.True(t, v.InputActive())
}

func TestCheckins_InputActive_Answering(t *testing.T) {
	v := testCheckinsViewWithAnswers()

	v.answering = true
	assert.True(t, v.InputActive())
}

// --- IsModal ---

func TestCheckins_IsModal_WhenAnswering(t *testing.T) {
	v := testCheckinsViewWithAnswers()

	assert.False(t, v.IsModal())

	v.answering = true
	assert.True(t, v.IsModal())
}

// --- StartFilter routes to focused pane ---

func TestCheckins_StartFilter_RoutesToFocusedPane(t *testing.T) {
	v := testCheckinsView()

	v.StartFilter()
	assert.True(t, v.listQuestions.Filtering())
	assert.False(t, v.listAnswers.Filtering())
	v.listQuestions.StopFilter()

	v.toggleFocus()
	v.StartFilter()
	assert.False(t, v.listQuestions.Filtering())
	assert.True(t, v.listAnswers.Filtering())
}

// --- Filter guard ---

func TestCheckins_FilterGuard_KeysDuringFilterDontTriggerNav(t *testing.T) {
	v := testCheckinsViewWithAnswers()

	v.listAnswers.StartFilter()
	require.True(t, v.listAnswers.Filtering())

	// 'n' should be absorbed by filter, not trigger new answer
	v.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	assert.True(t, v.listAnswers.Filtering(), "filter should still be active after 'n'")
	assert.False(t, v.answering, "should not enter answering mode during filter")
}

// --- New answer (n key) ---

func TestCheckins_NewAnswer_RequiresRightPane(t *testing.T) {
	v := testCheckinsView()

	// Focus on left pane, n should do nothing
	cmd := v.handleKey(runeKey('n'))
	assert.Nil(t, cmd, "n on left pane should return nil")
	assert.False(t, v.answering)
}

func TestCheckins_NewAnswer_RequiresSelectedQuestion(t *testing.T) {
	v := testCheckinsView()
	v.focus = checkinsPaneRight
	v.selectedQuestionID = 0

	cmd := v.handleKey(runeKey('n'))
	assert.Nil(t, cmd, "n with no selected question should return nil")
	assert.False(t, v.answering)
}

func TestCheckins_NewAnswer_EntersAnsweringMode(t *testing.T) {
	v := testCheckinsViewWithAnswers()

	cmd := v.handleKey(runeKey('n'))
	require.NotNil(t, cmd, "n should return a blink cmd")
	assert.True(t, v.answering)
}

// --- Answering: Esc cancels ---

func TestCheckins_Answering_EscCancels(t *testing.T) {
	v := testCheckinsViewWithAnswers()
	v.answering = true

	cmd := v.handleAnsweringKey(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Nil(t, cmd)
	assert.False(t, v.answering)
}

// --- ComposerSubmitMsg dispatches create ---

func TestCheckins_ComposerSubmitMsg_DispatchesCreate(t *testing.T) {
	v := testCheckinsViewWithAnswers()
	v.answering = true

	_, cmd := v.Update(widget.ComposerSubmitMsg{
		Content: widget.ComposerContent{Markdown: "My update"},
	})
	require.NotNil(t, cmd, "submit should return a create cmd")
	assert.True(t, v.answering, "answering should remain true while submit is in-flight")
	assert.True(t, v.submitting, "submitting should be true during in-flight")

	// The cmd is a batch (spinner.Tick + createAnswer). Unwrap to find
	// the createAnswer result among the batch messages.
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	require.True(t, ok, "should produce BatchMsg (spinner + create), got %T", msg)

	var found bool
	for _, c := range batch {
		if c == nil {
			continue
		}
		if result, ok := c().(checkinAnswerCreatedMsg); ok {
			found = true
			assert.Equal(t, int64(1), result.questionID)
			assert.Error(t, result.err, "should error since test session has nil SDK")
		}
	}
	assert.True(t, found, "batch should contain checkinAnswerCreatedMsg")
}

// --- checkinAnswerCreatedMsg ---

func TestCheckins_AnswerCreated_Success_ClosesComposer(t *testing.T) {
	v := testCheckinsViewWithAnswers()
	v.answering = true
	v.submitting = true
	v.submitID = 5

	// Pre-populate the answers pool
	answersPool := v.session.Hub().CheckinAnswers(42, 1)
	answersPool.Set(sampleAnswers())
	require.Equal(t, data.StateFresh, answersPool.Get().State)

	_, cmd := v.Update(checkinAnswerCreatedMsg{questionID: 1, submitID: 5, err: nil})
	require.NotNil(t, cmd, "success should return batch cmd")

	assert.False(t, v.answering, "answering should be false on success")
	assert.False(t, v.submitting, "submitting should be false on success")
	// Pool should now be loading (invalidated + fetch started)
	assert.Equal(t, data.StateLoading, answersPool.Get().State)
}

func TestCheckins_AnswerCreated_Error_KeepsDraft(t *testing.T) {
	v := testCheckinsViewWithAnswers()
	v.answering = true
	v.submitting = true
	v.submitID = 3

	_, cmd := v.Update(checkinAnswerCreatedMsg{questionID: 1, submitID: 3, err: fmt.Errorf("network error")})
	require.NotNil(t, cmd, "error should return error report cmd")

	assert.True(t, v.answering, "answering should remain true on error to preserve draft")
	assert.False(t, v.submitting, "submitting should be cleared on error")
}

// --- Submit in-flight ---

func TestCheckins_Submitting_EscCancelsRequest(t *testing.T) {
	v := testCheckinsViewWithAnswers()
	v.answering = true
	v.submitting = true
	canceled := false
	v.submitCancel = func() { canceled = true }

	cmd := v.handleAnsweringKey(tea.KeyMsg{Type: tea.KeyEsc})
	assert.NotNil(t, cmd, "esc during submit should return a status cmd")
	assert.True(t, canceled, "should have called cancel func")
	assert.False(t, v.submitting, "submitting should be cleared")
	assert.True(t, v.answering, "answering should remain true to preserve draft")
}

func TestCheckins_Submitting_BlocksOtherKeys(t *testing.T) {
	v := testCheckinsViewWithAnswers()
	v.answering = true
	v.submitting = true

	cmd := v.handleAnsweringKey(runeKey('a'))
	assert.Nil(t, cmd, "non-esc keys should be blocked during submit")
}

func TestCheckins_CancelledRequest_KeepsDraftNoError(t *testing.T) {
	v := testCheckinsViewWithAnswers()
	v.answering = true
	v.submitting = true
	v.submitID = 2

	_, cmd := v.Update(checkinAnswerCreatedMsg{questionID: 1, submitID: 2, err: context.Canceled})
	assert.False(t, v.submitting, "submitting should be cleared")
	assert.True(t, v.answering, "answering should remain true after cancellation")
	assert.Nil(t, cmd, "canceled request should not produce error or status cmd")
}

func TestCheckins_StaleCompletion_IgnoredAfterCancelAndResubmit(t *testing.T) {
	v := testCheckinsViewWithAnswers()
	v.answering = true
	v.submitting = true
	v.submitID = 1 // request A
	v.submitCancel = func() {}

	// User cancels request A (esc)
	v.handleAnsweringKey(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, v.submitting)
	assert.True(t, v.answering)

	// User submits request B — submitID advances to 2
	v.submitting = true
	v.submitID = 2
	v.submitCancel = func() {}

	// Request A returns late with an error
	_, cmd := v.Update(checkinAnswerCreatedMsg{questionID: 1, submitID: 1, err: fmt.Errorf("canceled")})
	assert.Nil(t, cmd, "stale completion should be silently ignored")
	assert.True(t, v.submitting, "request B should still be in-flight")
	assert.Equal(t, uint64(2), v.submitID, "submitID should not be altered by stale completion")
}

func TestCheckins_WrappedCanceledError_NoErrorToast(t *testing.T) {
	v := testCheckinsViewWithAnswers()
	v.answering = true
	v.submitting = true
	v.submitID = 1

	wrapped := fmt.Errorf("request failed: %w", context.Canceled)
	_, cmd := v.Update(checkinAnswerCreatedMsg{questionID: 1, submitID: 1, err: wrapped})
	assert.False(t, v.submitting)
	assert.True(t, v.answering, "draft should be preserved")
	assert.Nil(t, cmd, "wrapped context.Canceled should be absorbed, not shown as error toast")
}

func TestCheckins_CancelEsc_ButRequestSucceeds_ClosesComposer(t *testing.T) {
	v := testCheckinsViewWithAnswers()
	v.answering = true
	v.submitting = true
	v.submitID = 1
	v.submitCancel = func() {} // cancel is a no-op for this test

	// User hits esc — clears submitting, but submitID stays at 1
	v.handleAnsweringKey(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, v.submitting)
	assert.True(t, v.answering)

	// The request wasn't actually canceled and returns success
	answersPool := v.session.Hub().CheckinAnswers(42, 1)
	answersPool.Set(sampleAnswers())

	_, cmd := v.Update(checkinAnswerCreatedMsg{questionID: 1, submitID: 1, err: nil})
	require.NotNil(t, cmd, "successful post should still be processed")
	assert.False(t, v.answering, "composer should close on success even after esc")
	assert.False(t, v.submitting)
}

// --- syncQuestions reconciles cursor by ID ---

func TestCheckins_SyncQuestions_ReorderPreservesCursor(t *testing.T) {
	v := testCheckinsViewWithAnswers()

	// selectedQuestionID is 1, cursor is on question 1 (index 0)
	assert.Equal(t, int64(1), v.selectedQuestionID)

	// Simulate refresh where question order is reversed
	v.questions = []data.CheckinQuestionInfo{
		{ID: 2, Title: "What will you do this week?"},
		{ID: 1, Title: "What did you work on today?"},
	}
	v.syncQuestions()

	// Cursor should follow question 1 to its new position (index 1)
	selected := v.listQuestions.Selected()
	require.NotNil(t, selected)
	assert.Equal(t, "1", selected.ID, "cursor should follow selectedQuestionID after reorder")
	assert.Equal(t, int64(1), v.selectedQuestionID, "selectedQuestionID should be preserved")
}

func TestCheckins_PoolUpdatedMsg_RemovedQuestion_AutoSelectsFirst(t *testing.T) {
	v := testCheckinsViewWithAnswers()
	assert.Equal(t, int64(1), v.selectedQuestionID)

	// Simulate a questions pool refresh where question 1 is gone.
	// Update the pool backing data to only contain question 2.
	remaining := []data.CheckinQuestionInfo{
		{ID: 2, Title: "What will you do this week?"},
	}
	v.pool.Set(remaining)

	// Deliver the PoolUpdatedMsg through Update (the real runtime path).
	// syncQuestions clears selectedQuestionID because question 1 is gone,
	// then the auto-select at line 215 picks the first remaining question.
	_, cmd := v.Update(data.PoolUpdatedMsg{Key: v.pool.Key()})

	assert.Equal(t, int64(2), v.selectedQuestionID, "should auto-select first remaining question")
	assert.NotNil(t, cmd, "should return a loadAnswers cmd for the newly selected question")

	// Cursor should be on question 2
	selected := v.listQuestions.Selected()
	require.NotNil(t, selected)
	assert.Equal(t, "2", selected.ID)
}

// --- Help hint conditional ---

func TestCheckins_ShortHelp_RightPane_HidesNewAnswerWithoutSelection(t *testing.T) {
	v := testCheckinsView()
	v.focus = checkinsPaneRight
	v.selectedQuestionID = 0

	hints := v.ShortHelp()
	for _, h := range hints {
		keys := h.Keys()
		for _, k := range keys {
			assert.NotEqual(t, "n", k, "n hint should not appear without selected question")
		}
	}
}

func TestCheckins_ShortHelp_RightPane_ShowsNewAnswerWithSelection(t *testing.T) {
	v := testCheckinsViewWithAnswers()

	hints := v.ShortHelp()
	found := false
	for _, h := range hints {
		for _, k := range h.Keys() {
			if k == "n" {
				found = true
			}
		}
	}
	assert.True(t, found, "n hint should appear when question is selected")
}

// --- truncateContent ---

func TestTruncateContent(t *testing.T) {
	assert.Equal(t, "hello", truncateContent("hello", 80))
	assert.Equal(t, "first line", truncateContent("first line\nsecond line", 80))
	assert.Equal(t, "abcde...", truncateContent("abcdefghij", 5))
	assert.Equal(t, "", truncateContent("", 80))
	assert.Equal(t, "trimmed", truncateContent("  trimmed  ", 80))
}
