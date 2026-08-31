package workspace

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// testSortCases are the names that pin down the order: the ones a plain string
// compare gets wrong.
func testSortCases() []project {
	return []project{
		{id: 1, name: "@basecamp.com → @37signals.com", description: "Retiring basecamp.com"},
		{id: 2, name: "[Test Project] #2026"},
		{id: 3, name: "37signals HQ", description: "36signals.com was taken"},
		{id: 4, name: "2025-12-05 Cloudflare Outage", description: "Plan remediations"},
		{id: 5, name: "Accessibility"},
		{id: 6, name: "All Cars", description: "This is now a Volvo stan account"},
		{id: 7, name: "Basecamp Web", description: "HQ for the Basecamp Web team"},
		{id: 8, name: "Ángel's project", description: "Accents fold"},
	}
}

// testDirectory is a directory long enough to earn its letter separators, with
// the interesting names among the filler. The filler files under C so the groups
// the tests name — #, 0-9, A, B — are the ones the fixture puts there.
func testDirectory() []project {
	found := testSortCases()
	for index := range minRowsForLetters {
		found = append(found, project{
			id:   int64(100 + index),
			name: fmt.Sprintf("Cycle %d", index+1),
		})
	}
	sortProjects(found)
	return found
}

// openDirectory is the project directory on screen, filled and sized. The read
// is delivered through the model rather than straight to the screen: the model
// owns the spinner, and only its own Update puts it away.
func openDirectory(t *testing.T, height int) (model, *listScreen) {
	t.Helper()

	m := resize(t, newTestModel(t), 96, height)
	l := newAllProjects(m.ctx)
	m.push(l)

	m = load(t, m, directoryLoadedMsg{projects: testDirectory()})
	return m, l
}

// load hands a message to the model and lays the frame out again.
func load(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()

	updated, _ := m.Update(msg)
	m = updated.(model)
	m.relayout()
	return m
}

// --- Sorting ---

// The directory comes out in Basecamp's own order, which is not the order a
// plain string compare gives.
func TestDirectorySortsLikeBasecamp(t *testing.T) {
	found := testSortCases()
	sortProjects(found)

	names := make([]string, len(found))
	for index, one := range found {
		names[index] = one.name
	}

	assert.Equal(t, []string{
		// Names that do not start with a letter or a digit sort above the
		// alphabet.
		"@basecamp.com → @37signals.com",
		"[Test Project] #2026",
		// 37 is less than 2025, however the two read character by character.
		"37signals HQ",
		"2025-12-05 Cloudflare Outage",
		"Accessibility",
		"All Cars",
		// Á folds to A, so it files with the As rather than after Z.
		"Ángel's project",
		"Basecamp Web",
	}, names)
}

func TestSortNameInitial(t *testing.T) {
	assert.Equal(t, "#", newSortName("@basecamp.com").initial())
	assert.Equal(t, "#", newSortName("[Test Project]").initial())
	assert.Equal(t, "#", newSortName("🚨🚨🚨").initial())
	assert.Equal(t, "0-9", newSortName("37signals HQ").initial())
	assert.Equal(t, "0-9", newSortName("2025-12-05 Cloudflare").initial())
	assert.Equal(t, "A", newSortName("All Cars").initial())
	assert.Equal(t, "A", newSortName("Ángel").initial())
	assert.Equal(t, "A", newSortName("  accessibility").initial())
	assert.Equal(t, "#", newSortName("").initial())
}

// Runs of digits compare as numbers, so 2 files before 10.
func TestSortNameCountsNaturally(t *testing.T) {
	assert.True(t, newSortName("Cycle 2").before(newSortName("Cycle 10")))
	assert.False(t, newSortName("Cycle 10").before(newSortName("Cycle 2")))

	// A number sorts above a letter, which is what puts the digits between the
	// symbols and the alphabet.
	assert.True(t, newSortName("2025 Review").before(newSortName("Accessibility")))
}

// A name is only a prefix of the other, so the shorter one comes first.
func TestSortNamePrefixes(t *testing.T) {
	assert.True(t, newSortName("All").before(newSortName("All Cars")))
	assert.False(t, newSortName("All Cars").before(newSortName("All")))
	assert.False(t, newSortName("All").before(newSortName("All")))
}

// --- Letter separators ---

func TestDirectoryGroupsByInitial(t *testing.T) {
	_, l := openDirectory(t, 40)
	rendered := ansi.Strip(l.View())

	for _, initial := range []string{"#", "0-9", "A", "B"} {
		assert.Contains(t, rendered, initial+" ─", "no separator for %q", initial)
	}

	// One separator per letter, not one per project.
	assert.Equal(t, 1, strings.Count(rendered, "\nA ─"))
}

// A separator runs a rule out to the right edge.
func TestDirectorySeparatorsAreRuled(t *testing.T) {
	_, l := openDirectory(t, 40)

	for _, line := range strings.Split(ansi.Strip(l.View()), "\n") {
		if strings.HasPrefix(line, "A ─") {
			assert.Equal(t, l.width, tui.DisplayWidth(line))
			return
		}
	}
	t.Fatal("no letter separator on screen")
}

// --- Rows ---

// A project is one line: its name, then its description after a dash.
func TestDirectoryRows(t *testing.T) {
	m, _ := openDirectory(t, 40)
	rendered := screen(m)

	assert.Contains(t, rendered, "All Cars")
	assert.Contains(t, rendered, "This is now a Volvo stan account")
	assert.Contains(t, rendered, "› ")
	assert.Contains(t, m.nav.trail(), "All projects")
}

// A name is never cut to make room for a description.
func TestDirectoryKeepsTheNameWhole(t *testing.T) {
	m := resize(t, newTestModel(t), 48, 26)
	l := newAllProjects(m.ctx)
	m.push(l)
	m = load(t, m, directoryLoadedMsg{projects: []project{
		{id: 1, name: "A project with a rather long name", description: "and a description"},
	}})

	rendered := ansi.Strip(l.View())
	assert.Contains(t, rendered, "A project with a rather long name")
	assert.NotContains(t, rendered, "and a description")
}

func TestDirectoryRowsFitTheColumn(t *testing.T) {
	for _, width := range []int{40, 60, 96} {
		m := resize(t, newTestModel(t), width, 26)
		l := newAllProjects(m.ctx)
		m.push(l)
		m = load(t, m, directoryLoadedMsg{projects: testDirectory()})

		for _, line := range strings.Split(ansi.Strip(l.View()), "\n") {
			assert.LessOrEqual(t, tui.DisplayWidth(line), l.width, "at terminal width %d", width)
		}
	}
}

// --- Archived and trashed ---

func TestArchivedAndTrashedAreOffToStart(t *testing.T) {
	_, l := openDirectory(t, 40)

	assert.False(t, l.inactive)
	assert.Contains(t, ansi.Strip(l.View()), "Show archived and trashed")
}

// The switch is a switch: the knob sits at one end of its track and moves to
// the other when it is flicked.
func TestTheSwitchLooksLikeOne(t *testing.T) {
	_, l := openDirectory(t, 40)
	assert.Equal(t, "⬤━", ansi.Strip(l.toggle()))

	l.inactive = true
	assert.Equal(t, "━⬤", ansi.Strip(l.toggle()))

	// One column per glyph, so the label beside it lines up either way.
	assert.Equal(t, 2, tui.DisplayWidth(ansi.Strip(l.toggle())))
}

// The find field is laid out the way the jump menu's is: its name on the left,
// the key that reaches it on the right.
func TestFindRowNamesItselfAndItsKey(t *testing.T) {
	_, l := openDirectory(t, 40)

	row := ansi.Strip(l.findRow())
	assert.True(t, strings.HasPrefix(row, "Find a project"), "row was %q", row)
	assert.True(t, strings.HasSuffix(row, findKey), "row was %q", row)
	assert.Equal(t, l.width-2, tui.DisplayWidth(row))

	// The rule under it is the bottom of the box the web draws around its input.
	assert.Equal(t, strings.Repeat("─", l.width), ansi.Strip(l.findRule()))
}

// The key flips the switch and reads the directory again — the inactive ones
// come from their own reads, so there is nothing here to filter.
func TestTogglingArchivedRereads(t *testing.T) {
	m, l := openDirectory(t, 40)

	m, cmd := press(t, m, inactiveKey)
	assert.True(t, l.inactive)
	assert.True(t, l.loading)
	require.NotNil(t, cmd, "flipping the switch did not read again")
	assert.Contains(t, ansi.Strip(l.View()), "━⬤", "the switch did not move")

	_ = load(t, m, directoryLoadedMsg{inactive: true, projects: []project{
		{id: 9, name: "Cycle 3: Video", status: "archived"},
	}})

	rendered := ansi.Strip(l.View())
	assert.Contains(t, rendered, "Cycle 3: Video")
	assert.Contains(t, rendered, "archived", "an inactive project did not say so")
}

// An answer to the previous state of the switch is dropped rather than shown
// under the new one.
func TestADirectoryReadForTheOtherSwitchIsDropped(t *testing.T) {
	_, l := openDirectory(t, 40)
	before := len(l.projects)

	_, claimed := l.Update(directoryLoadedMsg{inactive: true, projects: []project{{id: 9, name: "Late"}}})
	assert.True(t, claimed)
	assert.Len(t, l.projects, before)
}

// --- Looking into a folder ---

// openFolderScreen is one folder's projects on screen.
func openFolderScreen(t *testing.T, projects []project) (model, *listScreen) {
	t.Helper()

	m := resize(t, newTestModel(t), 96, 26)
	l := newFolder(m.ctx, folder{id: 7, name: "Ops", projects: []int64{1, 2}})
	m.push(l)

	m = load(t, m, directoryLoadedMsg{projects: projects})
	return m, l
}

// A folder used to open onto nothing. It shows what is filed in it, sorted and
// laid out the way the directory is.
func TestAFolderShowsWhatIsFiledInIt(t *testing.T) {
	m, l := openFolderScreen(t, []project{
		{id: 1, name: "Zebra"},
		{id: 2, name: "Anchor", description: "First by name, second by id"},
	})

	rendered := ansi.Strip(l.View())
	assert.Contains(t, rendered, "Anchor")
	assert.Contains(t, rendered, "First by name, second by id")
	assert.Less(t, strings.Index(rendered, "Anchor"), strings.Index(rendered, "Zebra"),
		"a folder's projects came back unsorted")
	assert.Equal(t, []string{"Home", "Ops"}, m.nav.trail())
}

// A folder holds active projects and nothing else, so it has no switch — and
// pressing the key that would flick one does nothing.
func TestAFolderHasNoArchivedSwitch(t *testing.T) {
	m, l := openFolderScreen(t, []project{{id: 1, name: "Anchor"}})

	rendered := ansi.Strip(l.View())
	assert.NotContains(t, rendered, "Show archived and trashed")
	assert.NotContains(t, screen(m), "a archived")

	_, cmd := press(t, m, inactiveKey)
	assert.False(t, l.inactive)
	assert.Nil(t, cmd, "a folder read itself again over a switch it does not have")
}

func TestAnEmptyFolderSaysSo(t *testing.T) {
	_, l := openFolderScreen(t, nil)

	assert.Contains(t, ansi.Strip(l.View()), "This folder is empty.")
}

// A folder still has the find field — it is the same list, just a shorter one.
func TestAFolderCanBeSearched(t *testing.T) {
	m, l := openFolderScreen(t, []project{
		{id: 1, name: "Anchor"},
		{id: 2, name: "Zebra"},
	})

	m, _ = press(t, m, findKey)
	for _, key := range strings.Split("zeb", "") {
		m, _ = press(t, m, key)
	}

	rendered := ansi.Strip(l.View())
	assert.Contains(t, rendered, "Zebra")
	assert.NotContains(t, rendered, "Anchor")
}

// --- Letter separators earn their space ---

// A short list reads quicker without an index. The separators are for skimming
// a hundred names, and a folder holding four is not that.
func TestAShortListSkipsTheLetters(t *testing.T) {
	_, l := openFolderScreen(t, []project{
		{id: 1, name: "Anchor"},
		{id: 2, name: "Zebra"},
	})

	rendered := ansi.Strip(l.View())
	assert.Contains(t, rendered, "Anchor")
	assert.NotContains(t, rendered, "A ─")
	assert.NotContains(t, rendered, "Z ─")
}

// --- Finding ---

func TestDirectoryFind(t *testing.T) {
	m, l := openDirectory(t, 40)
	require.Contains(t, ansi.Strip(l.View()), "Accessibility")

	m, _ = press(t, m, findKey)
	require.True(t, l.finding)
	for _, key := range strings.Split("volvo", "") {
		m, _ = press(t, m, key)
	}

	rendered := ansi.Strip(l.View())
	assert.Contains(t, rendered, "All Cars", "the description was not searched")
	assert.NotContains(t, rendered, "Accessibility")
}

func TestDirectoryFindSaysWhenNothingMatches(t *testing.T) {
	m, l := openDirectory(t, 40)

	m, _ = press(t, m, findKey)
	for _, key := range strings.Split("zzz", "") {
		m, _ = press(t, m, key)
	}
	assert.Contains(t, ansi.Strip(l.View()), "Nothing matches zzz.")
}

// While the box has the keys, a is a letter rather than the archived switch.
func TestDirectoryFindCapturesInput(t *testing.T) {
	m, l := openDirectory(t, 40)

	m, _ = press(t, m, findKey)
	_, _ = press(t, m, inactiveKey)

	assert.Equal(t, "a", l.find.Value())
	assert.False(t, l.inactive)
}

// Esc clears the filter before it pops the screen.
func TestDirectoryEscapeClearsTheFind(t *testing.T) {
	m, l := openDirectory(t, 26)
	require.Equal(t, 2, m.nav.depth())

	m, _ = press(t, m, findKey)
	m, _ = press(t, m, "a")
	m, _ = press(t, m, "esc")

	assert.Equal(t, "", l.find.Value())
	assert.Equal(t, 2, m.nav.depth())

	m, _ = press(t, m, "esc")
	assert.Equal(t, 1, m.nav.depth())
}

// --- Walking ---

func TestDirectoryCursorWalksAndStops(t *testing.T) {
	m, l := openDirectory(t, 40)

	steps := len(testDirectory()) + 5
	for range steps {
		m, _ = press(t, m, "down")
	}
	assert.Equal(t, len(testDirectory())-1, l.cursor)

	for range steps {
		m, _ = press(t, m, "up")
	}
	assert.Equal(t, 0, l.cursor)
}

func TestDirectoryEnterOpensAProject(t *testing.T) {
	m, _ := openDirectory(t, 40)
	m, _ = press(t, m, "down")

	m, cmd := press(t, m, "enter")
	m = deliver(t, m, cmd)

	assert.Equal(t, []string{"Home", "[Test Project] #2026"}, m.nav.trail())
}

// A find that hides where the cursor was standing brings it back in range, and
// enter still opens what is under it rather than what used to be.
func TestDirectoryFindMovesWhatEnterOpens(t *testing.T) {
	m, l := openDirectory(t, 40)
	for range 5 {
		m, _ = press(t, m, "down")
	}

	m, _ = press(t, m, findKey)
	for _, key := range strings.Split("volvo", "") {
		m, _ = press(t, m, key)
	}
	require.Equal(t, 1, l.visible())
	assert.Equal(t, 0, l.cursor)

	m, _ = press(t, m, "enter")
	m, cmd := press(t, m, "enter")
	m = deliver(t, m, cmd)
	assert.Equal(t, []string{"Home", "All Cars"}, m.nav.trail())
}

// Scrolling back up to a project brings its letter with it.
func TestDirectoryScrollingUpBringsTheLetterBack(t *testing.T) {
	m, l := openDirectory(t, 14)
	require.Less(t, l.height, len(l.layout()), "the directory fits, so nothing scrolls")

	for range 6 {
		m, _ = press(t, m, "down")
	}
	for range 6 {
		m, _ = press(t, m, "up")
	}

	assert.Contains(t, ansi.Strip(l.View()), "# ─")
}

// --- Loading and failure ---

func TestDirectoryWhileLoading(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	l := newAllProjects(m.ctx)
	l.loading = true
	m.push(l)
	m.relayout()

	assert.Contains(t, ansi.Strip(l.View()), "Loading…")
}

func TestDirectoryWhileEmpty(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	l := newAllProjects(m.ctx)
	m.push(l)
	m = load(t, m, directoryLoadedMsg{})

	assert.Contains(t, ansi.Strip(l.View()), "No projects.")
}

func TestDirectoryFailure(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	l := newAllProjects(m.ctx)
	m.push(l)

	m = load(t, m, directoryLoadedMsg{err: errors.New("no route to host")})

	assert.Contains(t, l.notice, "Could not load the projects")
	assert.Contains(t, screen(m), "Could not load")
	assert.Nil(t, m.err, "a directory read put an error box over the screen")
}

// A directory of every project is long. It is read in one go rather than a page
// at a time, because a list sorted a page at a time is sorted only against
// itself.
func TestDirectoryIsSortedAcrossTheWholeList(t *testing.T) {
	many := make([]project, 60)
	for index := range many {
		many[index] = project{id: int64(index), name: fmt.Sprintf("Project %02d", 59-index)}
	}
	sortProjects(many)

	assert.Equal(t, "Project 00", many[0].name)
	assert.Equal(t, "Project 59", many[len(many)-1].name)
}
