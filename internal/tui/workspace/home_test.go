package workspace

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func testFolders() []folder {
	return []folder{
		{id: 1, name: "Cycle 4", projects: []int64{101, 102, 103, 104, 105, 106}},
		{id: 2, name: "Ops", projects: []int64{201, 202}},
	}
}

func testProjects() []project {
	return []project{
		{id: 10, name: "Website redesign", appURL: "https://3.basecamp.com/1234567/projects/10"},
		{id: 11, name: "Marketing site", appURL: "https://3.basecamp.com/1234567/projects/11"},
	}
}

// Basecamp words an event itself, and words it with the actor's name in it.
func testActivity() []activity {
	return []activity{
		{who: "Rob Zolkos", what: "Rob Z. posted Platform Documentation", where: "CLIs", when: "Aug 30"},
		{who: "Andy Smith", what: "Andy S. completed Ship the installer", where: "Cycle 4", when: "09:14"},
	}
}

// filledHome is a workspace whose home screen has all three reads in.
func filledHome(t *testing.T) (model, *home) {
	t.Helper()

	m := resize(t, newTestModel(t), 96, 26)
	h, ok := m.nav.current().(*home)
	require.True(t, ok)

	h.Update(homeFoldersMsg{folders: testFolders()})
	h.Update(homeProjectsMsg{projects: testProjects()})
	h.Update(homeActivityMsg{entries: testActivity()})
	m.relayout()
	return m, h
}

// The activity comes first and the projects under it, each with the button that
// leads to a screen of its own.
func TestHomeSections(t *testing.T) {
	m, _ := filledHome(t)
	rendered := screen(m)

	recent := strings.Index(rendered, "Recent activity")
	viewAll := strings.Index(rendered, "View all")
	projects := strings.Index(rendered, "Projects ")
	seeAll := strings.Index(rendered, "See all projects")

	require.GreaterOrEqual(t, recent, 0)
	assert.Less(t, recent, viewAll, "the button is under the feed it belongs to")
	assert.Less(t, viewAll, projects)
	assert.Less(t, projects, seeAll, "the button is under the list it belongs to")

	// The event's own wording is the line; who and when go quietly underneath.
	assert.Contains(t, rendered, "Rob Z. posted Platform Documentation")
	assert.Contains(t, rendered, "Aug 30 · Rob Zolkos · CLIs")
	assert.NotContains(t, rendered, "Rob Zolkos Rob Z.", "the actor was named twice")
}

// Folders and projects are one list, the way the web's card grid is.
func TestHomeFoldersAndProjectsAreOneList(t *testing.T) {
	m, _ := filledHome(t)
	rendered := screen(m)

	assert.NotContains(t, rendered, "Folders", "folders got a heading of their own")
	for _, f := range testFolders() {
		assert.Contains(t, rendered, f.name)
	}
	for _, p := range testProjects() {
		assert.Contains(t, rendered, p.name)
	}
}

// A folder wears an icon and says how many projects are filed in it. A project
// wears neither, and its name lines up with the folders' all the same.
func TestHomeFolderAndProjectRows(t *testing.T) {
	m, h := filledHome(t)
	h.projects[0].description = "A redesign of the marketing site"
	m.relayout()

	lines := strings.Split(screen(m), "\n")
	var folderName, folderCount, projectName string
	for index, line := range lines {
		switch {
		case strings.Contains(line, "Cycle 4") && strings.Contains(line, folderIcon):
			folderName, folderCount = line, lines[index+1]
		case strings.Contains(line, "Website redesign"):
			projectName = line
		}
	}
	require.NotEmpty(t, folderName)
	require.NotEmpty(t, projectName)

	assert.Contains(t, folderCount, "6 projects")
	assert.NotContains(t, folderCount, folderIcon, "the icon repeated under itself")
	assert.NotContains(t, projectName, folderIcon)

	// The names line up: a project takes the room an icon would.
	assert.Equal(t, columnOf(t, folderName, "Cycle 4"), columnOf(t, projectName, "Website redesign"))
	assert.Contains(t, screen(m), "A redesign of the marketing site")
}

// A project with no description still gets the line under it, so every row is
// the same height and the column reads as a list.
func TestHomeProjectWithoutADescription(t *testing.T) {
	_, h := filledHome(t)

	rows := h.itemRows("", "Marketing site", "", 99)
	assert.Len(t, rows, 2)
	assert.Equal(t, "", strings.TrimSpace(ansi.Strip(rows[1])))
}

// Each read answers on its own, so the screen fills in as they land rather than
// waiting on the slowest.
func TestHomeFillsInAsReadsLand(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	h := m.nav.current().(*home)

	h.Init()
	require.Equal(t, 3, h.pending, "opening the screen did not send the three reads")

	h.Update(homeFoldersMsg{folders: testFolders()})
	m.relayout()
	assert.Contains(t, screen(m), "Cycle 4")
	assert.Equal(t, 2, h.pending)
}

// --- The cursor ---

// The cursor walks the screen top to bottom: the activity button, the folders
// and projects in one list, then the projects button. The feed's own rows are
// not among them.
func TestHomeCursorWalksTheScreen(t *testing.T) {
	m, h := filledHome(t)
	assert.Equal(t, 1+len(testFolders())+len(testProjects())+1, h.itemCount())

	want := []string{"View all", "Cycle 4", "Ops", "Website redesign", "Marketing site", "See all projects"}
	for index, label := range want {
		assert.Equal(t, index, h.cursor)
		assert.Contains(t, screen(m), "› ", "nothing marked at %q", label)

		spot, _ := h.spotAt(index)
		switch index {
		case 0:
			assert.Equal(t, spotAllActivity, spot)
		case len(want) - 1:
			assert.Equal(t, spotAllProjects, spot)
		}
		m, _ = press(t, m, "down")
	}

	// It stops at the end rather than running off it.
	for range 5 {
		m, _ = press(t, m, "down")
	}
	assert.Equal(t, h.itemCount()-1, h.cursor)
}

// The buttons lead to screens of their own, which hang off home the way a
// project does.
func TestHomeButtonsOpenTheirScreens(t *testing.T) {
	m, _ := filledHome(t)

	m, cmd := press(t, m, "enter")
	m = deliver(t, m, cmd)
	assert.Equal(t, []string{"Home", "Latest activity"}, m.nav.trail())

	m, _ = press(t, m, "esc")
	h := m.nav.current().(*home)
	for range h.itemCount() - 1 {
		m, _ = press(t, m, "down")
	}

	m, cmd = press(t, m, "enter")
	m = deliver(t, m, cmd)
	assert.Equal(t, []string{"Home", "All projects"}, m.nav.trail())
}

func TestHomeEnterOpensAFolder(t *testing.T) {
	m, _ := filledHome(t)
	m, _ = press(t, m, "down")

	m, cmd := press(t, m, "enter")
	m = deliver(t, m, cmd)

	assert.Equal(t, []string{"Home", "Cycle 4"}, m.nav.trail())
}

func TestHomeEnterOpensAProject(t *testing.T) {
	m, _ := filledHome(t)
	for range 1 + len(testFolders()) {
		m, _ = press(t, m, "down")
	}

	m, cmd := press(t, m, "enter")
	m = deliver(t, m, cmd)

	assert.Equal(t, []string{"Home", "Website redesign"}, m.nav.trail())
}

// A list that comes back shorter must not leave the cursor pointing past the end
// of it.
func TestHomeClampsTheCursor(t *testing.T) {
	m, h := filledHome(t)
	for range h.itemCount() {
		m, _ = press(t, m, "down")
	}
	require.Positive(t, h.cursor)

	h.Update(homeProjectsMsg{})
	assert.Equal(t, h.itemCount()-1, h.cursor)
}

// --- The keys the web puts on buttons ---

func TestHomeNamingAProject(t *testing.T) {
	m, h := filledHome(t)

	m, _ = press(t, m, newProjectKey)
	assert.Equal(t, namingProject, h.naming)
	assert.True(t, h.CapturingInput())
	assert.Contains(t, screen(m), "New project name")
	assert.Contains(t, screen(m), "enter create")
	assert.Contains(t, screen(m), "esc cancel")

	for _, key := range strings.Split("Fizzy", "") {
		m, _ = press(t, m, key)
	}
	assert.Equal(t, "Fizzy", h.name.Value())
}

func TestHomeNamingAFolder(t *testing.T) {
	m, h := filledHome(t)

	m, _ = press(t, m, newFolderKey)
	assert.Equal(t, namingFolder, h.naming)
	assert.Contains(t, screen(m), "New folder name")
}

// Escape drops the name and leaves the screen as it was.
func TestHomeNamingCancels(t *testing.T) {
	m, h := filledHome(t)
	m, _ = press(t, m, newProjectKey)
	m, _ = press(t, m, "z")

	m, cmd := press(t, m, "esc")
	assert.Nil(t, cmd)
	assert.Equal(t, namingNothing, h.naming)
	assert.Equal(t, "", h.name.Value())
	assert.Equal(t, 1, m.nav.depth(), "esc canceled the name and popped a screen too")
}

// An empty name creates nothing rather than a project called "".
func TestHomeNamingNothingCreatesNothing(t *testing.T) {
	m, h := filledHome(t)
	m, _ = press(t, m, newProjectKey)

	_, cmd := press(t, m, "enter")
	assert.Nil(t, cmd)
	assert.Equal(t, namingNothing, h.naming)
}

// While a name is being typed every key belongs to it, shortcuts included.
func TestHomeNamingTakesEveryKey(t *testing.T) {
	m, h := filledHome(t)
	m, _ = press(t, m, newFolderKey)

	for _, key := range []string{"n", "f", "i", "a", "1"} {
		m, _ = press(t, m, key)
	}

	assert.Equal(t, "nfia1", h.name.Value())
	assert.Equal(t, "Home", m.nav.current().Title())
	assert.Equal(t, namingFolder, h.naming)
}

// What was made goes into the list it was made for, which means reading that
// list again rather than guessing where the server filed it.
func TestHomeRereadsAfterCreating(t *testing.T) {
	_, h := filledHome(t)

	cmd, claimed := h.Update(homeMadeMsg{what: "the project", name: "Fizzy"})
	assert.True(t, claimed)
	assert.NotNil(t, cmd)
	assert.Equal(t, 3, h.pending, "the three reads did not go out again")
}

func TestHomeSaysWhenCreatingFailed(t *testing.T) {
	_, h := filledHome(t)

	cmd, _ := h.Update(homeMadeMsg{what: "the folder", err: errors.New("no route to host")})
	require.NotNil(t, cmd)

	raised, ok := cmd().(notifyMsg)
	require.True(t, ok)
	assert.Equal(t, toastError, raised.kind)
	assert.Contains(t, raised.text, "Could not create the folder")
}

// --- The web-only pages ---

// The account's address on the web comes from a project's own app_url: config
// holds the API host, which is a different one.
func TestHomeFindsTheAccountRoot(t *testing.T) {
	_, h := filledHome(t)

	assert.Equal(t, "https://3.basecamp.com/1234567", h.accountRoot())
}

func TestHomeWithoutAnAccountRoot(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	h := m.nav.current().(*home)

	assert.Equal(t, "", h.accountRoot())

	// Rather than opening the wrong address, it says why it cannot.
	cmd := h.openWeb("account", "Adminland")
	require.NotNil(t, cmd)
	assert.Contains(t, cmd().(notifyMsg).text, "until the projects have loaded")
}

// --- A read that failed ---

// The first complaint is kept: a second is the same outage said twice.
func TestHomeKeepsTheFirstNotice(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	h := m.nav.current().(*home)

	h.Update(homeFoldersMsg{err: errors.New("no route to host")})
	h.Update(homeProjectsMsg{err: errors.New("still no route")})
	m.relayout()

	assert.Contains(t, h.notice, "Could not load the folders")
	assert.NotContains(t, h.notice, "Could not load the projects")
	assert.Contains(t, screen(m), "Could not load")
	assert.Nil(t, m.err, "a home read put an error box over the screen")
}

// --- Turning timeline events into activity ---

func TestToActivity(t *testing.T) {
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.Local)
	created := time.Date(2026, 8, 31, 8, 29, 0, 0, time.Local)

	entry := toActivity(basecamp.TimelineEvent{
		Kind:      "message_created",
		Action:    "Rob Z. added a message",
		Title:     "Rob Z. posted Platform Documentation",
		Creator:   &basecamp.Person{Name: "Rob Zolkos"},
		Bucket:    &basecamp.Bucket{Name: "CLIs"},
		CreatedAt: &created,
	}, now)

	assert.Equal(t, activity{
		who:   "Rob Zolkos",
		what:  "Rob Z. posted Platform Documentation",
		where: "CLIs",
		when:  "08:29",
	}, entry)
}

// An event with no title of its own falls back to its action, then to its kind,
// rather than drawing a blank row.
func TestToActivityFallsBack(t *testing.T) {
	action := toActivity(basecamp.TimelineEvent{Kind: "dock_created", Action: "added the dock"}, time.Now())
	assert.Equal(t, "added the dock", action.what)

	kind := toActivity(basecamp.TimelineEvent{Kind: "dock_created"}, time.Now())
	assert.Equal(t, "dock_created", kind.what)
	assert.Equal(t, "", kind.who)
}

// A title is what someone typed into Basecamp, so it must not be able to repaint
// the screen.
func TestToActivitySanitizes(t *testing.T) {
	entry := toActivity(basecamp.TimelineEvent{
		Action:  "posted",
		Title:   "Ship\x1b[2Jit",
		Creator: &basecamp.Person{Name: "Rob\x1b[2JZolkos"},
	}, time.Now())

	assert.NotContains(t, entry.what, "\x1b")
	assert.NotContains(t, entry.who, "\x1b")
}

// --- Layout ---

// Every row fits the content column: one column over and the row wraps, which
// shoves the sidebar's rows out of line with it.
func TestHomeRowsFitTheColumn(t *testing.T) {
	for _, width := range []int{40, 60, 96, 140} {
		m := resize(t, newTestModel(t), width, 26)
		h := m.nav.current().(*home)
		h.Update(homeFoldersMsg{folders: testFolders()})
		h.Update(homeProjectsMsg{projects: testProjects()})
		h.Update(homeActivityMsg{entries: testActivity()})
		m.relayout()

		for _, line := range strings.Split(ansi.Strip(h.View()), "\n") {
			assert.LessOrEqual(t, len([]rune(line)), h.width, "at terminal width %d", width)
		}
	}
}

// A column too short for everything draws what fits and stops.
func TestHomeClipsToItsHeight(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 12)
	h := m.nav.current().(*home)
	h.Update(homeFoldersMsg{folders: testFolders()})
	h.Update(homeProjectsMsg{projects: testProjects()})
	h.Update(homeActivityMsg{entries: testActivity()})
	m.relayout()

	assert.LessOrEqual(t, len(strings.Split(h.View(), "\n")), h.height)
}

// Walking down a list longer than the column scrolls it.
func TestHomeScrollsToTheCursor(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 10)
	h := m.nav.current().(*home)

	many := make([]project, 20)
	for index := range many {
		many[index] = project{id: int64(index), name: "Project " + string(rune('A'+index))}
	}
	h.Update(homeProjectsMsg{projects: many})
	m.relayout()

	for range len(many) {
		m, _ = press(t, m, "down")
	}

	rows := h.layout()
	at := -1
	for index, row := range rows {
		if row.item == h.cursor {
			at = index
			break
		}
	}
	require.GreaterOrEqual(t, at, 0)
	assert.GreaterOrEqual(t, at, h.offset, "the cursor scrolled off the top")
	assert.Less(t, at, h.offset+h.height, "the cursor scrolled off the bottom")
}

// --- Arriving with an account ---

// With no account settled the picker opens over home, so it is the picker's
// Init the model calls and home's that never runs. Choosing an account has to
// start it: popping back hands a screen over in the state it was left, which is
// right when the reader walked back and wrong when the account changed under it.
func TestHomeReadsOnceAnAccountIsChosen(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newModelWithAccount(t, "")
	m = resize(t, m, 96, 26)
	require.Equal(t, "Accounts", m.nav.current().Title())

	// The picker is what Init reaches, so home has read nothing.
	m.Init()
	h := m.nav.stack[0].(*home)
	require.Equal(t, 0, h.pending, "home read before there was an account to read for")

	updated, cmd := m.Update(accountChosenMsg{account: account{id: "1234567", name: "37signals"}})
	m = updated.(model)

	require.NotNil(t, cmd)
	assert.Equal(t, "Home", m.nav.current().Title())
	assert.Equal(t, 3, h.pending, "home did not read once the account arrived")
}

// Switching accounts is the same situation: what is on screen belongs to the
// account that was open a moment ago.
func TestSwitchingAccountsRereadsEverything(t *testing.T) {
	m, h := filledHome(t)
	m.menu.projects = testProjects()
	m.menu.projectsLoaded = true
	m.sidebar.loaded = true
	m.sidebar.replace(testReadings())

	updated, _ := m.Update(accountChosenMsg{account: account{id: "7654321", name: "Honcho"}})
	m = updated.(model)

	assert.Equal(t, 3, h.pending, "home kept the old account's folders and projects")
	assert.False(t, m.sidebar.loaded, "the sidebar kept the old account's notifications")
	assert.Empty(t, m.menu.projects, "the menu kept the old account's projects")
	assert.False(t, m.menu.projectsLoaded)
}

// --- Folder colors ---

// Basecamp's color names land on ANSI slots rather than on hex, so a terminal
// retint carries a folder's color along with everything else.
func TestFolderColors(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	h := m.nav.current().(*home)
	h.ctx.styles.UpdateTheme(tui.DefaultTheme(true))

	assert.Equal(t, lipgloss.Red, h.folderColor(folder{color: "red"}))
	assert.Equal(t, lipgloss.Magenta, h.folderColor(folder{color: "purple"}))

	// White is Basecamp's default and means uncolored, as is anything unset or
	// a name this version has not heard of.
	assert.Nil(t, h.folderColor(folder{color: "white"}))
	assert.Nil(t, h.folderColor(folder{}))
	assert.Nil(t, h.folderColor(folder{color: "chartreuse"}))
}

// A palette with no colors in it paints none, color name or not.
func TestFolderColorsUnderNoColor(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	h := m.nav.current().(*home)

	assert.Nil(t, h.folderColor(folder{color: "red"}))
}

// The color goes on the name, not the icon. An emoji carries its own colors:
// the terminal paints it from the font and ignores the foreground it was handed.
func TestFolderColorPaintsTheName(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	h := m.nav.current().(*home)
	h.ctx.styles.UpdateTheme(tui.DefaultTheme(true))
	h.Update(homeFoldersMsg{folders: []folder{
		{id: 1, name: "Ops", color: "red"},
		{id: 2, name: "Plain"},
	}})
	m.relayout()

	rendered := h.View()
	assert.Contains(t, rendered, lipgloss.NewStyle().Foreground(lipgloss.Red).Render("Ops"))
	assert.NotContains(t, rendered, lipgloss.NewStyle().Foreground(lipgloss.Red).Render(folderIcon))

	// An uncolored folder keeps the ordinary foreground.
	theme := tui.DefaultTheme(true)
	assert.Contains(t, rendered, lipgloss.NewStyle().Foreground(theme.Foreground).Render("Plain"))
}

// The cursor wins the row it is on: where the reader is standing has to read at
// a glance, and a folder's color is not that.
func TestCursorOutranksAFolderColor(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	h := m.nav.current().(*home)
	h.ctx.styles.UpdateTheme(tui.DefaultTheme(true))
	h.Update(homeFoldersMsg{folders: []folder{{id: 1, name: "Ops", color: "red"}}})
	h.cursor = 1
	m.relayout()

	theme := tui.DefaultTheme(true)
	assert.Contains(t, h.View(),
		lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Render("Ops"))
}

// --- Projects filed in a folder ---

// A project that lives in a folder is on screen inside it already, so the loose
// list below leaves it out. The web's grid follows the same rule.
func TestProjectsInAFolderAreLeftOutOfTheLooseList(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	h := m.nav.current().(*home)

	h.Update(homeProjectsMsg{projects: []project{
		{id: 10, name: "Loose"},
		{id: 11, name: "Filed"},
	}})
	h.Update(homeFoldersMsg{folders: []folder{{id: 1, name: "Ops", projects: []int64{11}}}})
	m.relayout()

	assert.Equal(t, []project{{id: 10, name: "Loose"}}, h.projects)

	rendered := ansi.Strip(h.View())
	assert.Contains(t, rendered, "Loose")
	assert.Contains(t, rendered, "Ops")
	assert.NotContains(t, rendered, "Filed")
}

// The two reads race, so the winnowing has to survive the folders landing first.
func TestFoldersLandingFirstStillWinnowTheProjects(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	h := m.nav.current().(*home)

	h.Update(homeFoldersMsg{folders: []folder{{id: 1, name: "Ops", projects: []int64{11}}}})
	h.Update(homeProjectsMsg{projects: []project{
		{id: 10, name: "Loose"},
		{id: 11, name: "Filed"},
	}})

	assert.Equal(t, []project{{id: 10, name: "Loose"}}, h.projects)
}

// A folder's own row counts what is filed inside it.
func TestAFolderCountsWhatIsFiledInIt(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	h := m.nav.current().(*home)

	h.Update(homeFoldersMsg{folders: []folder{{id: 1, name: "Ops", projects: []int64{7, 8, 9}}}})
	m.relayout()

	assert.Contains(t, ansi.Strip(h.View()), "3 projects")
}

func TestToFolder(t *testing.T) {
	red := "Red"
	assert.Equal(t, folder{id: 7, name: "Ops", color: "red", projects: []int64{1, 2}},
		toFolder(basecamp.Folder{ID: 7, Name: "Ops", BucketIDs: []int64{1, 2}, Color: &red}))

	// The color is optional: nil is a folder the reader never customized.
	assert.Equal(t, folder{id: 7, name: "Ops"},
		toFolder(basecamp.Folder{ID: 7, Name: "Ops"}))
}

// --- Scrolling back up ---

// The rows above an item belong with it. Without that the first thing the cursor
// can stand on pins the window below everything above it — the whole activity
// feed sits over the button that opens it, so coming back up to that button
// would leave the feed off the top of the screen for good.
func TestHomeScrollsBackToTheTop(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 30)
	h := m.nav.current().(*home)
	h.Update(homeActivityMsg{entries: testActivity()})
	h.Update(homeProjectsMsg{projects: listProjectsFor(30)})
	m.relayout()

	for range 40 {
		m, _ = press(t, m, "down")
	}
	require.Positive(t, h.offset, "the list never scrolled")

	for range 40 {
		m, _ = press(t, m, "up")
	}
	assert.Equal(t, 0, h.cursor)
	assert.Equal(t, 0, h.offset, "the activity feed stayed off the top of the screen")
	assert.Contains(t, h.View(), "Recent activity")
}

// A window too short to hold both the cursor and the rows above it keeps the
// cursor: a section heading is worth having, and knowing where you are is worth
// more.
func TestHomeKeepsTheCursorOnAShortScreen(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 12)
	h := m.nav.current().(*home)
	h.Update(homeActivityMsg{entries: testActivity()})
	m.relayout()
	require.Less(t, h.height, len(h.layout()))

	for range 5 {
		m, _ = press(t, m, "up")
	}

	rows := h.layout()
	at := -1
	for index, row := range rows {
		if row.item == h.cursor {
			at = index
			break
		}
	}
	require.GreaterOrEqual(t, at, 0)
	assert.GreaterOrEqual(t, at, h.offset)
	assert.Less(t, at, h.offset+h.height)
}

func listProjectsFor(n int) []project {
	projects := make([]project, n)
	for index := range projects {
		projects[index] = project{id: int64(index), name: fmt.Sprintf("Project %02d", index)}
	}
	return projects
}
