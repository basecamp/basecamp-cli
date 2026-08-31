package workspace

import (
	"errors"
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
	assert.Contains(t, screen(m), "Activity")
}

// Every row the menu covers is masked the full width of the screen: a box
// composited straight onto the frame leaves the far end of a rule and a slice of
// the sidebar showing beside it.
func TestMenuMasksTheRowsItCovers(t *testing.T) {
	m := resize(t, newTestModel(t), 92, 24)
	updated, _ := m.Update(readingsLoadedMsg{readings: testReadings()})
	m = updated.(model)
	require.Contains(t, screen(m), sidebarHintText)

	m, _ = press(t, m, menuKey)

	lines := strings.Split(screen(m), "\n")
	for index := menuTopRow; index < menuTopRow+3; index++ {
		row := lines[index]
		assert.NotContains(t, row, "sidebar", "the divider showed through row %d", index)
		assert.NotContains(t, row, "Bubbled", "the sidebar showed through row %d", index)
	}
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

// openMenu is a menu laid out for a terminal of the given size.
func openMenu(t *testing.T, width, height int) menu {
	t.Helper()

	n := newMenu(plainStyles(t))
	n.open = true
	n.resize(width, height)
	return n
}

// The number is underlined, the way HEY marks the key that jumps to a section —
// and only the number, not the label after it.
func TestMenuUnderlinesTheNumbers(t *testing.T) {
	rendered := openMenu(t, 80, 30).view()

	for _, s := range sections {
		assert.True(t, underlined(rendered, s.key), "%s is not underlined", s.key)
		assert.False(t, underlined(rendered, s.label), "%s should not be underlined", s.label)
	}
}

// The box takes three fifths of the screen, and every row of it is that width.
func TestMenuIsThreeFifthsWide(t *testing.T) {
	for _, screenWidth := range []int{60, 80, 120} {
		lines := strings.Split(ansi.Strip(openMenu(t, screenWidth, 30).view()), "\n")
		require.NotEmpty(t, lines)
		for _, line := range lines {
			assert.Equal(t, screenWidth*3/5, lipgloss.Width(line), "at screen width %d", screenWidth)
		}
	}
}

// A screen too narrow for three fifths to hold a label gets the floor instead,
// and never a box wider than the screen.
func TestMenuWidthHasAFloorAndACeiling(t *testing.T) {
	narrow := ansi.Strip(openMenu(t, 20, 30).view())
	for _, line := range strings.Split(narrow, "\n") {
		assert.Equal(t, 20, lipgloss.Width(line))
	}

	assert.Equal(t, menuMinWidth-4, menuInnerWidth(40))
	assert.Equal(t, 1, menuInnerWidth(1))
}

func TestClosedMenuDrawsNothing(t *testing.T) {
	assert.Equal(t, "", newMenu(plainStyles(t)).view())
}

// Home is the first entry, so the first move down lands on Activity.
func TestMenuEnterOpensTheCursorsEntry(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, menuKey)
	m, _ = press(t, m, "down")

	m, _ = press(t, m, "enter")
	assert.False(t, m.menu.open)
	assert.Equal(t, "Activity", m.nav.current().Title())
}

// Home sits above the numbered places, indented past where their numbers go, and
// says which key also reaches it.
func TestMenuListsHomeFirst(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, menuKey)

	rendered := screen(m)
	assert.Contains(t, rendered, "Places")
	assert.Less(t, strings.Index(rendered, "Home"), strings.Index(rendered, "Activity"))
	assert.Contains(t, rendered, homeHintText)

	// The label lines up with the numbered ones rather than with their numbers.
	var home, activity string
	for _, line := range strings.Split(rendered, "\n") {
		switch {
		case strings.Contains(line, "Home") && home == "":
			home = line
		case strings.Contains(line, "Activity") && activity == "":
			activity = line
		}
	}
	require.NotEmpty(t, home)
	require.NotEmpty(t, activity)
	assert.Equal(t, columnOf(t, activity, "Activity"), columnOf(t, home, "Home"))
}

// Enter on Home goes home, the way Shift+H does.
func TestMenuHomeEntry(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, "3")
	require.Equal(t, "Reports", m.nav.current().Title())

	m, _ = press(t, m, menuKey)
	m, _ = press(t, m, "enter")
	assert.Equal(t, "Home", m.nav.current().Title())
}

func TestMenuCursorStopsAtTheEnds(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, menuKey)

	m, _ = press(t, m, "up")
	assert.Equal(t, 0, m.menu.cursor)

	// Home plus the numbered places.
	for range len(sections) + 5 {
		m, _ = press(t, m, "down")
	}
	assert.Equal(t, len(sections), m.menu.cursor)
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
	assert.Contains(t, view, "/ search")
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

// --- Search ---

// The field is idle until "/" reaches for it: the numbers are what the menu is
// for, and typing is the fallback.
func TestSlashStartsSearching(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, menuKey)
	require.False(t, m.menu.searching)
	assert.Contains(t, screen(m), "Search")

	m, _ = press(t, m, searchKey)
	assert.True(t, m.menu.searching)
	assert.Contains(t, screen(m), "esc stop searching")
}

func TestSearchNarrowsTheMenu(t *testing.T) {
	m := menuWithProjects(t)
	m, _ = press(t, m, searchKey)

	for _, key := range strings.Split("cal", "") {
		m, _ = press(t, m, key)
	}

	rendered := screen(m)
	assert.Contains(t, rendered, "Calendar")
	assert.NotContains(t, rendered, "Reports")
	assert.NotContains(t, rendered, "Website redesign")
}

// A section with nothing left in it is left out rather than drawn empty.
func TestSearchDropsEmptySections(t *testing.T) {
	m := menuWithProjects(t)
	m, _ = press(t, m, searchKey)

	for _, key := range strings.Split("redesign", "") {
		m, _ = press(t, m, key)
	}

	rendered := screen(m)
	assert.Contains(t, rendered, "Projects")
	assert.Contains(t, rendered, "Website redesign")
	assert.NotContains(t, rendered, "Places")
}

func TestSearchWithNoMatches(t *testing.T) {
	m := menuWithProjects(t)
	m, _ = press(t, m, searchKey)
	m, _ = press(t, m, "z")

	assert.Contains(t, screen(m), "Nothing matches that")
	assert.Equal(t, 0, m.menu.count())
}

// While typing, a number is a number rather than a jump.
func TestNumbersTypeWhileSearching(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, menuKey)
	m, _ = press(t, m, searchKey)

	m, _ = press(t, m, "2")
	assert.Equal(t, "2", m.menu.search.Value())
	assert.True(t, m.menu.open)
	assert.Equal(t, "Home", m.nav.current().Title())
}

// The first escape puts the field down; the second closes the menu.
func TestEscapeStopsSearchingBeforeItCloses(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, menuKey)
	m, _ = press(t, m, searchKey)

	m, _ = press(t, m, "esc")
	assert.False(t, m.menu.searching)
	assert.True(t, m.menu.open)
	assert.Equal(t, "", m.menu.search.Value())

	m, _ = press(t, m, "esc")
	assert.False(t, m.menu.open)
}

// Reopening starts with the field idle and empty.
func TestReopeningClearsTheSearch(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, menuKey)
	m, _ = press(t, m, searchKey)
	m, _ = press(t, m, "c")

	m, _ = press(t, m, menuKey)
	m, _ = press(t, m, menuKey)
	assert.False(t, m.menu.searching)
	assert.Equal(t, "", m.menu.search.Value())
}

// --- Projects ---

// menuWithProjects is a workspace with the menu open and its projects in.
func menuWithProjects(t *testing.T) model {
	t.Helper()

	m := resize(t, newTestModel(t), 92, 24)
	m, _ = press(t, m, menuKey)

	updated, _ := m.Update(projectsLoadedMsg{page: 1, projects: []project{
		{id: 1, name: "Website redesign"},
		{id: 2, name: "Marketing site"},
	}})
	m = updated.(model)

	// The box is not full yet, so the model will keep asking. An empty page is
	// the server saying that is all of them.
	updated, _ = m.Update(projectsLoadedMsg{page: 2})
	return updated.(model)
}

func TestMenuListsProjects(t *testing.T) {
	m := menuWithProjects(t)

	rendered := screen(m)
	assert.Contains(t, rendered, "Projects")
	assert.Contains(t, rendered, "Website redesign")
	assert.Less(t, strings.Index(rendered, "Places"), strings.Index(rendered, "Projects"))
}

// The first page goes out when the menu opens, and one page is in flight at a
// time however many times it is reopened.
func TestFirstPageGoesOutOnOpen(t *testing.T) {
	m := resize(t, newTestModel(t), 92, 24)

	m, cmd := press(t, m, menuKey)
	require.NotNil(t, cmd, "opening the menu did not read a page of projects")
	assert.True(t, m.menu.projectsPaging)

	// A second open while the first page is still out must not ask again.
	m, _ = press(t, m, menuKey)
	_, cmd = press(t, m, menuKey)
	assert.Nil(t, cmd)
}

// The server running out is what ends the walk, and reopening does not restart
// it: the pages already read are still there.
func TestAnEmptyPageEndsTheProjectWalk(t *testing.T) {
	m := menuWithProjects(t)
	require.True(t, m.menu.projectsDone)
	require.Len(t, m.menu.projects, 2)

	m, _ = press(t, m, menuKey)
	_, cmd := press(t, m, menuKey)
	assert.Nil(t, cmd, "reopening asked for another page after the list ran out")
}

// The heading is there before the first page is, with a dashed rule saying more
// is coming — so a menu opened mid-read does not look like an account with no
// projects in it.
func TestProjectsHeadingArrivesBeforeTheProjects(t *testing.T) {
	m := resize(t, newTestModel(t), 92, 24)
	m, _ = press(t, m, menuKey)
	require.True(t, m.menu.projectsPaging)

	assert.Contains(t, screen(m), "Projects ┄")
	assert.Contains(t, screen(m), "Places ─", "only the growing section is dashed")
}

// Once a page is in and more is on the way, the rule under the projects stays
// dashed until the server runs out of them.
func TestProjectsRuleDashesWhileMoreIsComing(t *testing.T) {
	m := resize(t, newTestModel(t), 92, 24)
	m, _ = press(t, m, menuKey)

	updated, _ := m.Update(projectsLoadedMsg{page: 1, projects: []project{{id: 1, name: "Website redesign"}}})
	m = updated.(model)
	assert.Contains(t, screen(m), "Projects ┄")
	assert.Contains(t, screen(m), "Website redesign")

	updated, _ = m.Update(projectsLoadedMsg{page: 2})
	m = updated.(model)
	assert.Contains(t, screen(m), "Projects ─")
	assert.NotContains(t, screen(m), "┄")
}

// Opening a project puts it on the stack under home, the way a section goes.
func TestOpeningAProject(t *testing.T) {
	m := menuWithProjects(t)
	for range m.menu.count() - 2 {
		m, _ = press(t, m, "down")
	}
	require.Equal(t, "Website redesign", m.menu.entries()[m.menu.cursor].label)

	m, _ = press(t, m, "enter")
	assert.False(t, m.menu.open)
	assert.Equal(t, []string{"Home", "Website redesign"}, m.nav.trail())
}

// The places above them are still reachable, so a projects read that failed says
// so under the list rather than closing the menu.
func TestProjectsThatWillNotLoad(t *testing.T) {
	m := resize(t, newTestModel(t), 92, 24)
	m, _ = press(t, m, menuKey)

	updated, _ := m.Update(projectsLoadedMsg{err: errors.New("no route to host")})
	m = updated.(model)

	assert.True(t, m.menu.open)
	assert.Contains(t, screen(m), "Could not load the projects")
	assert.Contains(t, screen(m), "Activity")
	assert.Nil(t, m.err, "a projects read put an error box over the screen")
}

// A project name is what someone typed into Basecamp, so it must not be able to
// repaint the menu.
func TestProjectNamesAreSanitized(t *testing.T) {
	m := resize(t, newTestModel(t), 92, 24)
	m, _ = press(t, m, menuKey)

	updated, _ := m.Update(projectsLoadedMsg{page: 1, projects: []project{{id: 1, name: "Ship\x1b[2Jit"}}})
	m = updated.(model)

	assert.NotContains(t, m.View().Content, "\x1b[2J")
}
