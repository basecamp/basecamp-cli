package workspace

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func testAccounts() []account {
	return []account{
		{id: "123456", name: "37signals"},
		{id: "234567", name: "Basecamp HQ"},
		{id: "345678", name: "Honcho"},
		{id: "456789", name: "Shape Up Readers"},
	}
}

// newTestPicker returns a picker already holding the test accounts, laid out
// for a terminal of the given size.
func newTestPicker(t *testing.T, width, height int, canCancel bool) (model, *accountPicker) {
	t.Helper()

	m := newTestModel(t)
	p := newAccountPicker(m.ctx, canCancel)
	m.push(p)
	p.Resize(width, height)
	p.Update(accountsLoadedMsg{accounts: testAccounts()})
	return m, p
}

func TestPickerListsAccounts(t *testing.T) {
	_, p := newTestPicker(t, 80, 40, true)

	view := ansi.Strip(p.View())
	for _, a := range testAccounts() {
		assert.Contains(t, view, a.name)
		assert.Contains(t, view, a.id)
	}
	assert.Contains(t, view, "Search accounts")
	assert.Contains(t, view, "╭")
	assert.Contains(t, view, "╰")
}

// The box is one width whatever is in it, and the rows inside it line up.
func TestPickerBoxIsOneWidth(t *testing.T) {
	_, p := newTestPicker(t, 80, 40, true)

	lines := strings.Split(ansi.Strip(p.box()), "\n")
	require.NotEmpty(t, lines)
	for _, line := range lines {
		assert.Equal(t, pickerBoxWidth, lipgloss.Width(line))
	}
}

// A terminal narrower than the box gets a box that fits it rather than one that
// spills across the screen.
func TestPickerBoxNarrowsWithTheTerminal(t *testing.T) {
	_, p := newTestPicker(t, 30, 20, true)

	for _, line := range strings.Split(ansi.Strip(p.box()), "\n") {
		assert.Equal(t, 30, lipgloss.Width(line))
	}
}

func TestPickerCentersItsBlock(t *testing.T) {
	_, p := newTestPicker(t, 80, 40, true)

	lines := strings.Split(ansi.Strip(p.View()), "\n")
	assert.Len(t, lines, 40)

	var boxTop string
	for _, line := range lines {
		if strings.Contains(line, "╭") {
			boxTop = line
			break
		}
	}
	require.NotEmpty(t, boxTop)

	left := len(boxTop) - len(strings.TrimLeft(boxTop, " "))
	right := len(boxTop) - len(strings.TrimRight(boxTop, " "))
	assert.InDelta(t, left, right, 1)
}

// The logo goes over the box when the screen has room for it and the list both.
func TestPickerShowsTheLogoWhenThereIsRoom(t *testing.T) {
	_, tall := newTestPicker(t, 80, 40, false)
	assert.True(t, tall.showsLogo())
	assert.Len(t, strings.Split(ansi.Strip(tall.View()), "\n"), 40)
	assert.Contains(t, ansi.Strip(tall.View()), "░")

	_, short := newTestPicker(t, 80, 17, false)
	assert.False(t, short.showsLogo())
	assert.NotContains(t, ansi.Strip(short.View()), "░")

	_, narrow := newTestPicker(t, tui.SnowglobeWidth-1, 40, false)
	assert.False(t, narrow.showsLogo())
}

// Switching accounts partway through is not the front door, so the logo stays
// off however much room the screen has — it would only push the list down.
func TestPickerHidesTheLogoWhenSwitching(t *testing.T) {
	_, p := newTestPicker(t, 80, 40, true)

	assert.False(t, p.showsLogo())
	assert.NotContains(t, ansi.Strip(p.View()), "░")
	assert.Contains(t, ansi.Strip(p.View()), "37signals")
}

// Giving the logo its rows must not squeeze the list below what is worth
// showing — that is the trade the height check exists to make.
func TestPickerKeepsEnoughRowsToPickFrom(t *testing.T) {
	for height := 10; height <= 60; height++ {
		_, p := newTestPicker(t, 80, height, false)
		if p.showsLogo() {
			assert.GreaterOrEqual(t, p.visibleRows(), min(pickerMinRows, len(p.filtered)),
				"logo drawn at height %d leaves only %d rows", height, p.visibleRows())
		}
		assert.LessOrEqual(t, lipgloss.Height(p.View()), height, "overflows at height %d", height)
	}
}

func TestPickerFiltersAsYouType(t *testing.T) {
	_, p := newTestPicker(t, 80, 40, true)

	for _, key := range strings.Split("hon", "") {
		p.HandleKey(keyPress(key))
	}

	assert.Equal(t, []account{{id: "345678", name: "Honcho"}}, p.filtered)
	assert.NotContains(t, ansi.Strip(p.View()), "37signals")
}

// The id is searchable too: it is what the header shows and what config holds.
func TestPickerFiltersOnID(t *testing.T) {
	_, p := newTestPicker(t, 80, 40, true)

	for _, key := range strings.Split("1234", "") {
		p.HandleKey(keyPress(key))
	}

	assert.Equal(t, []account{{id: "123456", name: "37signals"}}, p.filtered)
}

func TestPickerSaysWhenNothingMatches(t *testing.T) {
	_, p := newTestPicker(t, 80, 40, true)
	p.HandleKey(keyPress("z"))

	assert.Empty(t, p.filtered)
	assert.Contains(t, ansi.Strip(p.View()), "Nothing matches that")
}

// A filter that empties the list must not leave the cursor pointing past its
// end, where enter would panic.
func TestPickerCursorSurvivesFiltering(t *testing.T) {
	_, p := newTestPicker(t, 80, 40, true)
	p.HandleKey(keyPress("down"))
	p.HandleKey(keyPress("down"))
	p.HandleKey(keyPress("down"))
	require.Equal(t, 3, p.cursor)

	p.HandleKey(keyPress("z"))
	assert.Equal(t, 0, p.cursor)
	assert.Nil(t, p.HandleKey(keyPress("enter")))
}

func TestPickerCursorStopsAtTheEnds(t *testing.T) {
	_, p := newTestPicker(t, 80, 40, true)

	p.HandleKey(keyPress("up"))
	assert.Equal(t, 0, p.cursor)

	for range 10 {
		p.HandleKey(keyPress("down"))
	}
	assert.Equal(t, len(testAccounts())-1, p.cursor)
}

// A list longer than the box scrolls to keep the cursor on screen.
func TestPickerScrolls(t *testing.T) {
	_, p := newTestPicker(t, 80, 7, true)
	require.Less(t, p.visibleRows(), len(p.filtered))

	for range len(testAccounts()) {
		p.HandleKey(keyPress("down"))
	}

	assert.GreaterOrEqual(t, p.cursor, p.offset)
	assert.Less(t, p.cursor, p.offset+p.visibleRows())
	assert.Contains(t, ansi.Strip(p.View()), "Shape Up Readers")
}

// The cursor opens on the account the workspace is already reading, so enter is
// a no-op rather than a surprise.
func TestPickerOpensOnTheCurrentAccount(t *testing.T) {
	m := newTestModel(t)
	m.ctx.app.Config.AccountID = "345678"

	p := newAccountPicker(m.ctx, true)
	p.Resize(80, 40)
	p.Update(accountsLoadedMsg{accounts: testAccounts()})

	assert.Equal(t, 2, p.cursor)
	assert.Equal(t, "Honcho", p.filtered[p.cursor].name)
}

// The account already open is marked even when the cursor has moved off it.
func TestPickerMarksTheCurrentAccount(t *testing.T) {
	m := newTestModel(t)
	m.ctx.app.Config.AccountID = "123456"

	p := newAccountPicker(m.ctx, true)
	p.Resize(80, 40)
	p.Update(accountsLoadedMsg{accounts: testAccounts()})
	p.HandleKey(keyPress("down"))

	view := ansi.Strip(p.View())
	assert.Contains(t, view, "• 37signals")
	assert.Contains(t, view, "› Basecamp HQ")
}

func TestPickerSaysWhileItIsLoading(t *testing.T) {
	m := newTestModel(t)
	p := newAccountPicker(m.ctx, true)
	p.Resize(80, 40)

	assert.True(t, p.loading)
	assert.Contains(t, ansi.Strip(p.View()), "Loading accounts…")

	// The model must not swap the screen for its own spinner: the box says it
	// is loading, and the logo would go with it.
	assert.False(t, p.Loading())
}

func TestPickerSaysWhenThereAreNoAccounts(t *testing.T) {
	m := newTestModel(t)
	p := newAccountPicker(m.ctx, true)
	p.Resize(80, 40)
	p.Update(accountsLoadedMsg{})

	assert.Contains(t, ansi.Strip(p.View()), "No accounts on this login")
	assert.Nil(t, p.HandleKey(keyPress("enter")))
}

// A read that fails has to be retryable: the opening picker has no esc, so
// without this the reader is stuck with ctrl+c.
func TestPickerRetriesAFailedRead(t *testing.T) {
	m := newTestModel(t)
	p := newAccountPicker(m.ctx, false)
	p.Resize(80, 40)
	p.Update(accountsLoadedMsg{err: errors.New("the network is unreachable")})

	assert.Contains(t, p.notice, "Could not load the accounts")
	assert.Contains(t, p.notice, "the network is unreachable")

	view := ansi.Strip(p.View())
	assert.Contains(t, view, "Could not load the accounts")
	assert.Contains(t, view, "ctrl+r to try again")
	assert.Equal(t, []helpBinding{{"ctrl+r", "try again"}}, p.HelpBindings())

	assert.NotNil(t, p.HandleKey(keyPress("ctrl+r")))
	assert.True(t, p.loading)
	assert.Empty(t, p.notice)
}

// --- The picker in the workspace ---

func TestWorkspaceOpensOnThePickerWithNoAccount(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newModelWithAccount(t, "")

	assert.Equal(t, 2, m.nav.depth())
	assert.Equal(t, "Accounts", m.nav.current().Title())

	picker, ok := m.nav.current().(*accountPicker)
	require.True(t, ok)
	assert.False(t, picker.canCancel)

	// Esc has nowhere to go: there is no account to fall back to.
	m, cmd := press(t, m, "esc")
	assert.Nil(t, cmd)
	assert.Equal(t, 2, m.nav.depth())
}

func TestWorkspaceOpensOnHomeWithAnAccount(t *testing.T) {
	m := newTestModel(t)

	assert.Equal(t, 1, m.nav.depth())
	assert.Equal(t, "Home", m.nav.current().Title())
}

func TestCtrlAOpensThePicker(t *testing.T) {
	m := newTestModel(t)

	m, _ = press(t, m, "ctrl+a")
	assert.Equal(t, "Accounts", m.nav.current().Title())
	picker, ok := m.nav.current().(*accountPicker)
	require.True(t, ok)
	assert.True(t, picker.canCancel)

	m, cmd := press(t, m, "esc")
	m = deliver(t, m, cmd)
	assert.Equal(t, "Home", m.nav.current().Title())
}

// ctrl+a on the picker is a no-op rather than a second copy of it.
func TestCtrlAOnThePickerDoesNotStack(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, "ctrl+a")
	m, _ = press(t, m, "ctrl+a")

	assert.Equal(t, 2, m.nav.depth())
}

func TestChoosingAnAccountSetsItForTheSession(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newModelWithAccount(t, "")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	m = updated.(model)

	picker, ok := m.nav.current().(*accountPicker)
	require.True(t, ok)
	picker.Update(accountsLoadedMsg{accounts: testAccounts()})

	updated, cmd := m.Update(accountChosenMsg{account: testAccounts()[2]})
	m = updated.(model)

	assert.Equal(t, "345678", m.ctx.AccountID())
	assert.Equal(t, "Home", m.nav.current().Title())
	assert.Contains(t, screen(m), "Honcho")
	require.NotNil(t, cmd)
}

// Picking the account already open is not news, so it raises no toast.
func TestChoosingTheOpenAccountSaysNothing(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, "ctrl+a")

	updated, cmd := m.Update(accountChosenMsg{account: account{id: "1234567", name: "Basecamp HQ"}})
	m = updated.(model)

	assert.Equal(t, "Home", m.nav.current().Title())
	assert.Nil(t, cmd)
}

// Every key the picker does not claim is search text, so a shortcut typed into
// it filters rather than firing.
func TestPickerSwallowsShortcuts(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, "ctrl+a")
	picker := m.nav.current().(*accountPicker)
	picker.Update(accountsLoadedMsg{accounts: testAccounts()})

	m, _ = press(t, m, "q")
	m, _ = press(t, m, "?")

	assert.Equal(t, "q?", picker.search.Value())
	assert.Equal(t, 2, m.nav.depth())
	assert.False(t, m.help.hidden)
}
