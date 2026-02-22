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

func sampleScheduleEntries() []data.ScheduleEntryInfo {
	return []data.ScheduleEntryInfo{
		{ID: 1, Summary: "Team sync", StartsAt: "Mar 1, 2026", EndsAt: "Mar 1, 2026", AllDay: true},
		{ID: 2, Summary: "Launch party", StartsAt: "Mar 5, 2026", EndsAt: "Mar 6, 2026", AllDay: true},
	}
}

func testScheduleView() *Schedule {
	session := workspace.NewTestSessionWithScope(workspace.Scope{
		AccountID: "acct1",
		ProjectID: 42,
		ToolID:    10,
	})

	styles := tui.NewStyles()

	pool := data.NewPool[[]data.ScheduleEntryInfo](
		"schedule-entries:42:10",
		data.PoolConfig{FreshTTL: time.Hour},
		func(context.Context) ([]data.ScheduleEntryInfo, error) {
			return sampleScheduleEntries(), nil
		},
	)
	pool.Set(sampleScheduleEntries())

	list := widget.NewList(styles)
	list.SetEmptyText("No schedule entries found.")
	list.SetFocused(true)
	list.SetSize(80, 24)

	v := &Schedule{
		session: session,
		pool:    pool,
		styles:  styles,
		list:    list,
		entries: sampleScheduleEntries(),
		width:   80,
		height:  24,
	}
	v.syncList()
	return v
}

// --- Create: n enters create mode ---

func TestSchedule_Create_NEntersCreateMode(t *testing.T) {
	v := testScheduleView()
	cmd := v.handleKey(runeKey('n'))
	require.NotNil(t, cmd, "n should return blink cmd")
	assert.True(t, v.creating)
	assert.Equal(t, 0, v.createStep)
	assert.True(t, v.InputActive())
	assert.True(t, v.IsModal())
}

// --- Create: enter with summary advances to step 1 ---

func TestSchedule_Create_SummaryAdvancesToStep1(t *testing.T) {
	v := testScheduleView()
	v.creating = true
	v.createStep = 0
	v.createInput = newTextInputWithValue("Team sync")

	cmd := v.handleCreateKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd, "enter should return blink cmd for next step")
	assert.True(t, v.creating)
	assert.Equal(t, 1, v.createStep)
	assert.Equal(t, "Team sync", v.createSummary)
}

// --- Create: enter with date advances to step 2 ---

func TestSchedule_Create_StartDateAdvancesToStep2(t *testing.T) {
	v := testScheduleView()
	v.creating = true
	v.createStep = 1
	v.createSummary = "Team sync"
	v.createInput = newTextInputWithValue("2026-03-15")

	cmd := v.handleCreateKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd, "enter should return blink cmd for next step")
	assert.True(t, v.creating)
	assert.Equal(t, 2, v.createStep)
	assert.Equal(t, "2026-03-15", v.createStart)
}

// --- Create: enter on step 2 dispatches ---

func TestSchedule_Create_EndDateDispatches(t *testing.T) {
	v := testScheduleView()
	v.creating = true
	v.createStep = 2
	v.createSummary = "Team sync"
	v.createStart = "2026-03-15"
	v.createInput = newTextInputWithValue("2026-03-16")

	cmd := v.handleCreateKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd, "enter should dispatch create")
	assert.False(t, v.creating)

	msg := cmd()
	result, ok := msg.(scheduleEntryCreatedMsg)
	require.True(t, ok, "cmd should produce scheduleEntryCreatedMsg")
	// Error expected with nil SDK
	assert.Error(t, result.err)
}

// --- Create: esc at any step cancels ---

func TestSchedule_Create_EscCancels(t *testing.T) {
	for _, step := range []int{0, 1, 2} {
		v := testScheduleView()
		v.creating = true
		v.createStep = step

		cmd := v.handleCreateKey(tea.KeyMsg{Type: tea.KeyEsc})
		assert.Nil(t, cmd, "esc should return nil at step %d", step)
		assert.False(t, v.creating, "creating should be false after esc at step %d", step)
	}
}

// --- Create: invalid date shows error status ---

func TestSchedule_Create_InvalidDateShowsError(t *testing.T) {
	v := testScheduleView()
	v.creating = true
	v.createStep = 1
	v.createSummary = "Team sync"
	v.createInput = newTextInputWithValue("not-a-date")

	cmd := v.handleCreateKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	assert.True(t, v.creating, "should stay in create mode on invalid date")
	assert.Equal(t, 1, v.createStep, "should stay on step 1")

	msg := cmd()
	status, ok := msg.(workspace.StatusMsg)
	require.True(t, ok, "should produce StatusMsg")
	assert.Contains(t, status.Text, "Unrecognized date")
}

// --- Create: end date before start date shows error ---

func TestSchedule_Create_EndBeforeStartShowsError(t *testing.T) {
	v := testScheduleView()
	v.creating = true
	v.createStep = 2
	v.createSummary = "Team sync"
	v.createStart = "2026-03-15"
	v.createInput = newTextInputWithValue("2026-03-10")

	cmd := v.handleCreateKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	assert.True(t, v.creating, "should stay in create mode")
	assert.Equal(t, 2, v.createStep, "should stay on step 2")

	msg := cmd()
	status, ok := msg.(workspace.StatusMsg)
	require.True(t, ok, "should produce StatusMsg")
	assert.Contains(t, status.Text, "End date must be on or after start date")
}

// --- Create: empty end date defaults to start date ---

func TestSchedule_Create_EmptyEndDefaultsToStart(t *testing.T) {
	v := testScheduleView()
	v.creating = true
	v.createStep = 2
	v.createSummary = "Team sync"
	v.createStart = "2026-03-15"
	v.createInput = newTextInputWithValue("")

	cmd := v.handleCreateKey(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd, "should dispatch create with default end")
	assert.False(t, v.creating)

	msg := cmd()
	_, ok := msg.(scheduleEntryCreatedMsg)
	assert.True(t, ok, "cmd should produce scheduleEntryCreatedMsg")
}

// --- Create: success handler invalidates pool ---

func TestSchedule_Create_SuccessInvalidatesPool(t *testing.T) {
	v := testScheduleView()
	_, cmd := v.Update(scheduleEntryCreatedMsg{err: nil})
	require.NotNil(t, cmd)
}

// --- Create: empty summary exits create mode ---

func TestSchedule_Create_EmptySummaryExits(t *testing.T) {
	v := testScheduleView()
	v.creating = true
	v.createStep = 0
	v.createInput = newTextInputWithValue("")

	cmd := v.handleCreateKey(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Nil(t, cmd)
	assert.False(t, v.creating)
}

// --- Trash: filter guard ---

func TestSchedule_FilterGuard_TDuringFilter(t *testing.T) {
	v := testScheduleView()
	v.list.StartFilter()
	require.True(t, v.list.Filtering())

	v.handleKey(runeKey('t'))
	assert.False(t, v.trashPending, "t during filter should NOT arm trash")
}

// --- Trash: double-press ---

func TestSchedule_Trash_DoublePressArmsAndFires(t *testing.T) {
	v := testScheduleView()

	// First press arms
	cmd := v.handleKey(runeKey('t'))
	require.NotNil(t, cmd)
	assert.True(t, v.trashPending)
	assert.Equal(t, "1", v.trashPendingID)

	// Second press fires
	cmd = v.handleKey(runeKey('t'))
	require.NotNil(t, cmd)
	assert.False(t, v.trashPending)

	msg := cmd()
	result, ok := msg.(scheduleTrashResultMsg)
	require.True(t, ok, "cmd should produce scheduleTrashResultMsg")
	assert.Error(t, result.err) // nil SDK
}

// --- Trash: other key resets ---

func TestSchedule_Trash_OtherKeyResets(t *testing.T) {
	v := testScheduleView()
	v.trashPending = true
	v.trashPendingID = "1"

	v.handleKey(runeKey('j'))
	assert.False(t, v.trashPending)
	assert.Empty(t, v.trashPendingID)
}

// --- Trash: timeout resets ---

func TestSchedule_Trash_TimeoutResets(t *testing.T) {
	v := testScheduleView()
	v.trashPending = true
	v.trashPendingID = "1"

	v.Update(scheduleTrashTimeoutMsg{})
	assert.False(t, v.trashPending)
	assert.Empty(t, v.trashPendingID)
}

// --- Trash: success handler invalidates pool ---

func TestSchedule_Trash_SuccessInvalidatesPool(t *testing.T) {
	v := testScheduleView()
	_, cmd := v.Update(scheduleTrashResultMsg{err: nil})
	require.NotNil(t, cmd)
}

// --- ShortHelp ---

func TestSchedule_ShortHelp_IncludesNewAndTrash(t *testing.T) {
	v := testScheduleView()
	hints := v.ShortHelp()

	keys := make(map[string]string)
	for _, h := range hints {
		keys[h.Help().Key] = h.Help().Desc
	}
	assert.Equal(t, "new event", keys["n"])
	assert.Equal(t, "trash", keys["t"])
}
