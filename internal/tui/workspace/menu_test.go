package workspace

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMenuOpensOnEitherKey(t *testing.T) {
	for _, key := range []string{menuKey, menuAltKey} {
		m := newTestModel(t)
		require.False(t, m.menu.open)

		m, _ = press(t, m, key)
		assert.True(t, m.menu.open, "%s did not open the menu", key)

		m, _ = press(t, m, key)
		assert.False(t, m.menu.open, "%s did not close the menu", key)
	}
}

func TestMenuClosesOnEscape(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, menuKey)

	m, _ = press(t, m, "esc")
	assert.False(t, m.menu.open)
	assert.Equal(t, 1, m.nav.depth(), "esc closed the menu and popped a screen too")
}

// The caret sits beside the account name and the hint on the right says which
// key opens it.
func TestMenuCaretAndHint(t *testing.T) {
	m := newTestModel(t)

	top := strings.Split(screen(m), "\n")[0]
	assert.Contains(t, top, "1234567 "+chevronClosed)
	assert.True(t, strings.HasSuffix(top, " "+menuHintText+" ──"))
}

// The menu hangs one row below the top line, so the account and its caret stay
// on screen — the caret turning over is what says the menu is open.
func TestMenuDrawsBelowTheTopLine(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, menuKey)

	lines := strings.Split(screen(m), "\n")
	assert.Contains(t, lines[0], "1234567 "+chevronOpen)
	assert.True(t, strings.HasSuffix(lines[0], " "+menuHintText+" ──"))
	assert.Contains(t, lines[menuTopRow], "╭")
	assert.Contains(t, lines[menuTopRow+1], "Activity")
}

func TestMenuListsTheSections(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, menuKey)

	view := screen(m)
	for _, s := range sections {
		assert.Contains(t, view, s.key+" "+s.label)
	}
	assert.Contains(t, view, "1 Activity")
	assert.Contains(t, view, "4 Everything")
}

// underlined reports whether the escape run immediately before text switches
// underline on. lipgloss folds the attribute in with everything else the style
// carries — "\x1b[1;4;4m" on the cursor row — so it is looked for among the
// parameters rather than as a sequence of its own.
func underlined(rendered, text string) bool {
	match := regexp.MustCompile(`\x1b\[([0-9;]*)m` + regexp.QuoteMeta(text)).FindStringSubmatch(rendered)
	if match == nil {
		return false
	}
	return slices.Contains(strings.Split(match[1], ";"), "4")
}

// The number is underlined, the way HEY marks the key that jumps to a section —
// and only the number, not the label after it.
func TestMenuUnderlinesTheNumbers(t *testing.T) {
	rendered := menu{open: true}.view(plainStyles(t), 80)

	for _, s := range sections {
		assert.True(t, underlined(rendered, s.key), "%s is not underlined", s.key)
		assert.False(t, underlined(rendered, s.label), "%s should not be underlined", s.label)
	}
}

// The box takes three fifths of the screen, and every row of it is that width.
func TestMenuIsThreeFifthsWide(t *testing.T) {
	open := menu{open: true}

	for _, screenWidth := range []int{60, 80, 120} {
		lines := strings.Split(ansi.Strip(open.view(plainStyles(t), screenWidth)), "\n")
		require.NotEmpty(t, lines)
		for _, line := range lines {
			assert.Equal(t, screenWidth*3/5, lipgloss.Width(line), "at screen width %d", screenWidth)
		}
	}
}

// A screen too narrow for three fifths to hold a label gets the floor instead,
// and never a box wider than the screen.
func TestMenuWidthHasAFloorAndACeiling(t *testing.T) {
	open := menu{open: true}

	narrow := ansi.Strip(open.view(plainStyles(t), 20))
	for _, line := range strings.Split(narrow, "\n") {
		assert.Equal(t, 20, lipgloss.Width(line))
	}

	assert.Equal(t, menuMinWidth-4, menuInnerWidth(40))
	assert.Equal(t, 1, menuInnerWidth(1))
}

func TestClosedMenuDrawsNothing(t *testing.T) {
	assert.Equal(t, "", menu{}.view(plainStyles(t), 80))
}

func TestMenuEnterOpensTheCursorsSection(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, menuKey)
	m, _ = press(t, m, "down")

	m, _ = press(t, m, "enter")
	assert.False(t, m.menu.open)
	assert.Equal(t, "Calendar", m.nav.current().Title())
}

func TestMenuCursorStopsAtTheEnds(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, menuKey)

	m, _ = press(t, m, "up")
	assert.Equal(t, 0, m.menu.cursor)

	for range len(sections) + 3 {
		m, _ = press(t, m, "down")
	}
	assert.Equal(t, len(sections)-1, m.menu.cursor)
}

// Reopening starts at the top rather than wherever it was left.
func TestMenuReopensAtTheTop(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, menuKey)
	m, _ = press(t, m, "down")
	m, _ = press(t, m, menuKey)
	m, _ = press(t, m, menuKey)

	assert.Equal(t, 0, m.menu.cursor)
}

func TestMenuHelpBar(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, menuKey)

	view := screen(m)
	assert.Contains(t, view, "↑↓ move")
	assert.Contains(t, view, "1-4 go")
	assert.Contains(t, view, "enter open")
	assert.Contains(t, view, "esc close")
}

// The menu stands over the screen while it is up: keys that would act on what is
// behind it do nothing.
func TestMenuSwallowsKeys(t *testing.T) {
	m := newTestModel(t)
	view := &stubView{title: "Projects"}
	m.push(view)
	m, _ = press(t, m, menuKey)

	m, _ = press(t, m, "j")
	m, _ = press(t, m, "?")

	assert.Empty(t, view.keys)
	assert.False(t, m.help.hidden)
	assert.True(t, m.menu.open)
}

// --- Section keys ---

// The numbers reach their sections with the menu shut. That is the whole point
// of the menu: it shows the keys until the reader stops needing it.
func TestSectionKeysWorkWithTheMenuClosed(t *testing.T) {
	for _, s := range sections {
		m := newTestModel(t)
		require.False(t, m.menu.open)

		m, _ = press(t, m, s.key)
		assert.Equal(t, s.label, m.nav.current().Title())
		assert.Contains(t, screen(m), "Home › "+s.label)
	}
}

func TestSectionKeysWorkWithTheMenuOpen(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, menuKey)

	m, _ = press(t, m, "3")
	assert.False(t, m.menu.open)
	assert.Equal(t, "Reports", m.nav.current().Title())
}

// Sections are siblings, not a ladder: going from one to another comes back to
// home first, however deep the reader was.
func TestSectionsAreSiblings(t *testing.T) {
	m := newTestModel(t)

	m, _ = press(t, m, "1")
	m.push(&stubView{title: "Some detail"})
	require.Equal(t, 3, m.nav.depth())

	m, _ = press(t, m, "2")
	assert.Equal(t, 2, m.nav.depth())
	assert.Equal(t, []string{"Home", "Calendar"}, m.nav.trail())
}

// Pressing the number for the section already open does nothing rather than
// stacking a second copy of it.
func TestSectionKeyForTheOpenSectionIsANoOp(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, "1")

	m, cmd := press(t, m, "1")
	assert.Nil(t, cmd)
	assert.Equal(t, 2, m.nav.depth())
}

// Esc from a section comes back to home, the way it does from any screen.
func TestEscapeLeavesASection(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, "2")

	m, _ = press(t, m, "esc")
	assert.Equal(t, "Home", m.nav.current().Title())
}

// Shift+H unwinds the whole stack in one step, however deep the reader walked —
// esc would take a press per level.
func TestShiftHGoesHome(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, "1")
	m.push(&stubView{title: "Some detail"})
	m.push(&stubView{title: "Deeper still"})
	require.Equal(t, 4, m.nav.depth())

	m, _ = press(t, m, homeKey)
	assert.Equal(t, 1, m.nav.depth())
	assert.Equal(t, "Home", m.nav.current().Title())
}

func TestShiftHAtHomeDoesNothing(t *testing.T) {
	m := newTestModel(t)

	m, cmd := press(t, m, homeKey)
	assert.Nil(t, cmd)
	assert.Equal(t, 1, m.nav.depth())
}

// A capital typed into a search box is search text, not a jump.
func TestShiftHDoesNotFireWhileTyping(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, "ctrl+a")
	picker := m.nav.current().(*accountPicker)

	m, _ = press(t, m, homeKey)

	assert.Equal(t, homeKey, picker.search.Value())
	assert.Equal(t, "Accounts", m.nav.current().Title())
}

// A digit typed into a search box is search text, not a jump.
func TestSectionKeysDoNotFireWhileTyping(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, "ctrl+a")
	picker := m.nav.current().(*accountPicker)
	picker.Update(accountsLoadedMsg{accounts: testAccounts()})

	m, _ = press(t, m, "1")

	assert.Equal(t, "1", picker.search.Value())
	assert.Equal(t, "Accounts", m.nav.current().Title())
}
