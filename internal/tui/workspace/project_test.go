package workspace

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

func at(position int) *int { return &position }

// testDock is a real dock, in the order the API hands it over: unsorted, with
// the tools nobody turned on mixed in.
func testDock() []basecamp.DockItem {
	return []basecamp.DockItem{
		{ID: 1, Name: "message_board", Title: "Message Board", Enabled: true, Position: at(1)},
		{ID: 2, Name: "todoset", Title: "To-dos", Enabled: false},
		{ID: 3, Name: "vault", Title: "Recipes", Enabled: true, Position: at(8)},
		{ID: 4, Name: "schedule", Title: "Calendar", Enabled: false},
		{ID: 5, Name: "chat", Title: "Chat", Enabled: true, Position: at(4)},
		{ID: 6, Name: "kanban_board", Title: "HEY CLI", Enabled: true, Position: at(2)},
		{ID: 7, Name: "vault", Title: "Screenshots", Enabled: true, Position: at(9)},
		{ID: 8, Name: "kanban_board", Title: "Basecamp CLI", Enabled: true, Position: at(3)},
	}
}

func testProjectActivity() []activity {
	return []activity{
		{who: "Stanko K.", what: "On “Basecamp CLI”, Stanko K. added a card", where: "CLIs",
			at: testNow.Add(-3 * time.Minute)},
		{who: "Rob Z.", what: "Completed: [CLI] The skill tells agents to log in", where: "CLIs",
			at: testNow.Add(-10 * time.Hour)},
	}
}

// testDoors are the project's external links as the recordings query answers
// them: newest first, which is nobody's arrangement.
func testDoors() []basecamp.Recording {
	return []basecamp.Recording{
		{ID: 30, Title: "HEY repo", URL: "https://github.com/basecamp/haystack/", Position: 7,
			Service: &basecamp.DoorService{Name: "GitHub", Code: "github"}},
		{ID: 20, Title: "SDK repo", URL: "https://github.com/basecamp/hey-sdk", Position: 6,
			Service: &basecamp.DoorService{Name: "GitHub", Code: "github"}},
		{ID: 10, Title: "CLI repo", URL: "https://github.com/basecamp/hey-cli", Position: 5,
			Service: &basecamp.DoorService{Name: "GitHub", Code: "github"}},
	}
}

// openProjectScreen is a project on screen with all three reads in.
func openProjectScreen(t *testing.T, width int) (model, *projectScreen) {
	t.Helper()

	m := resize(t, newTestModel(t), width, 26)
	p := newProject(m.ctx, project{id: 48521764, name: "CLIs"})
	p.now = func() time.Time { return testNow }
	m.push(p)

	p.Update(projectLoadedMsg{
		project: project{id: 48521764, name: "CLIs", description: "For the nerds"},
		tools:   toTools(testDock()),
	})
	p.Update(projectActivityMsg{entries: testProjectActivity()})
	p.Update(projectLinksMsg{links: toLinks(testDoors())})
	m.relayout()
	return m, p
}

// --- The dock ---

// The dock arrives unsorted with the tools nobody turned on mixed in. What the
// screen shows is what the web's grid shows: the ones that are on, in the order
// the reader dragged them into.
func TestTheDockIsSortedAndWinnowed(t *testing.T) {
	tools := toTools(testDock())

	names := make([]string, len(tools))
	for index, on := range tools {
		names[index] = on.name
	}
	assert.Equal(t, []string{
		"Message Board", "HEY CLI", "Basecamp CLI", "Chat", "Recipes", "Screenshots",
	}, names)
}

// A tool says what kind it is only when the reader has renamed it. A card table
// called "HEY CLI" needs saying; a chat called "Chat" does not.
func TestAToolNamesItsKindOnlyWhenRenamed(t *testing.T) {
	name, kind := tool{kind: "kanban_board", name: "HEY CLI"}.label()
	assert.Equal(t, "HEY CLI", name)
	assert.Equal(t, "Card table", kind)

	name, kind = tool{kind: "chat", name: "Chat"}.label()
	assert.Equal(t, "Chat", name)
	assert.Equal(t, "", kind, "a tool with its own name repeated it")

	// Case is not a rename.
	_, kind = tool{kind: "message_board", name: "Message Board"}.label()
	assert.Equal(t, "", kind)

	// A kind this version has not heard of says nothing rather than guessing.
	_, kind = tool{kind: "holodeck", name: "Holodeck"}.label()
	assert.Equal(t, "", kind)
}

func TestProjectShowsItsDock(t *testing.T) {
	m, _ := openProjectScreen(t, 110)
	rendered := ansi.Strip(screen(m))

	assert.Contains(t, rendered, "Message Board")
	assert.Contains(t, rendered, "HEY CLI — Card table")
	assert.Contains(t, rendered, "Recipes — Docs & Files")

	// The ones nobody turned on are not on the dock.
	assert.NotContains(t, rendered, "To-dos")
	assert.NotContains(t, rendered, "Calendar")
}

// --- The external links ---

// The dock leaves the doors out on purpose, so they come from their own read —
// newest first, which is not the order anybody dragged them into.
func TestExternalLinksReadInDockOrder(t *testing.T) {
	links := toLinks(testDoors())

	titles := make([]string, len(links))
	for index, out := range links {
		titles[index] = out.title
	}
	assert.Equal(t, []string{"CLI repo", "SDK repo", "HEY repo"}, titles)
	assert.Equal(t, "https://github.com/basecamp/hey-cli", links[0].url)
	assert.Equal(t, "GitHub", links[0].service)
}

// They get their own section, the way the web gives them one: every other row on
// this screen opens a screen, and these leave for the browser.
func TestProjectShowsItsExternalLinks(t *testing.T) {
	m, _ := openProjectScreen(t, 110)
	m = resize(t, m, 110, 44)
	rendered := ansi.Strip(screen(m))

	assert.Contains(t, rendered, "External links")
	assert.Contains(t, rendered, "CLI repo — https://github.com/basecamp/hey-cli")

	links := strings.Index(rendered, "External links")
	tools := strings.Index(rendered, "Tools ")
	activity := strings.Index(rendered, "Recent activity")
	assert.Less(t, tools, links, "the links are under the dock they hang off")
	assert.Less(t, links, activity, "the feed is under the whole dock, as it is on the web")
}

// A project with no links out of it gets no heading for them.
func TestProjectWithoutExternalLinks(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	p := newProject(m.ctx, project{id: 1, name: "Bare"})
	m.push(p)
	p.Update(projectLoadedMsg{project: project{id: 1, name: "Bare"}, tools: toTools(testDock())})
	p.Update(projectLinksMsg{})
	m.relayout()

	assert.NotContains(t, ansi.Strip(p.View()), "External links")
}

// --- Header and feed ---

// The name leads and what the project is for sits under it, the way the web's
// own header reads.
func TestProjectHeader(t *testing.T) {
	m, p := openProjectScreen(t, 110)

	assert.Equal(t, []string{"Home", "CLIs"}, m.nav.trail())

	lines := strings.Split(ansi.Strip(p.View()), "\n")
	assert.Equal(t, "CLIs", strings.TrimSpace(lines[0]))
	assert.Equal(t, "For the nerds", strings.TrimSpace(lines[1]))
}

// A project with nothing said about it gets no empty line where a description
// would be.
func TestProjectWithoutADescription(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	p := newProject(m.ctx, project{id: 1, name: "Bare"})
	m.push(p)
	p.Update(projectLoadedMsg{project: project{id: 1, name: "Bare"}, tools: toTools(testDock())})
	m.relayout()

	lines := strings.Split(ansi.Strip(p.View()), "\n")
	assert.Equal(t, "Bare", strings.TrimSpace(lines[0]))
	assert.Equal(t, "", strings.TrimSpace(lines[1]))
	assert.Contains(t, lines[2], "Tools")
}

// The feed is the project's own, drawn with the same row as everywhere else.
func TestProjectFeed(t *testing.T) {
	m, p := openProjectScreen(t, 110)
	m = resize(t, m, 110, 44)
	rendered := ansi.Strip(screen(m))

	assert.Contains(t, rendered, "3m ago On “Basecamp CLI”, Stanko K. added a card")
	assert.Contains(t, rendered, "10h ago Completed: [CLI] The skill tells agents to log in")

	for _, line := range activityRows(p.ctx.Styles(), testProjectActivity()[0], testNow, p.width, false) {
		assert.Contains(t, ansi.Strip(p.View()), ansi.Strip(line))
	}
}

func TestProjectWithAQuietFeed(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	p := newProject(m.ctx, project{id: 1, name: "Bare"})
	m.push(p)
	p.Update(projectLoadedMsg{project: project{id: 1, name: "Bare"}})
	p.Update(projectActivityMsg{})
	p.Update(projectLinksMsg{})
	m.relayout()

	rendered := ansi.Strip(p.View())
	assert.Contains(t, rendered, "Nothing yet.")
	assert.Contains(t, rendered, "Nothing on the dock.")
}

// The feed is five lines and a way to the rest of it, so the bottom of it is
// never the end of what happened.
func TestProjectFeedOpensInFull(t *testing.T) {
	m, _ := openProjectScreen(t, 110)
	m = resize(t, m, 110, 44)
	assert.Contains(t, ansi.Strip(screen(m)), "View all activity →")

	for range 30 {
		m, _ = press(t, m, "down")
	}
	m, cmd := press(t, m, "enter")
	m = deliver(t, m, cmd)

	assert.Equal(t, []string{"Home", "CLIs", "Latest activity"}, m.nav.trail())
	feed, ok := m.nav.current().(*activityScreen)
	require.True(t, ok, "View all activity opened something else")
	require.NotNil(t, feed.inside, "the whole account's feed opened instead of the project's")
	assert.Equal(t, int64(48521764), feed.inside.id)
}

// --- Walking ---

// The cursor walks the dock, then the links, then the button. The feed's own rows
// are not among them: there is no screen for one entry.
func TestProjectCursorWalksTheRows(t *testing.T) {
	m, p := openProjectScreen(t, 110)

	spot, index := p.spotAt(p.cursor)
	assert.Equal(t, spotProjectTool, spot)
	assert.Equal(t, 0, index)

	for range 30 {
		m, _ = press(t, m, "down")
	}
	assert.Equal(t, len(toTools(testDock()))+len(testDoors()), p.cursor)

	spot, _ = p.spotAt(p.cursor)
	assert.Equal(t, spotProjectActivity, spot)

	for range 30 {
		m, _ = press(t, m, "up")
	}
	assert.Equal(t, 0, p.cursor)
}

// A tool hangs off the project rather than off home, so the trail says where the
// reader came from.
func TestOpeningAToolHangsOffTheProject(t *testing.T) {
	m, _ := openProjectScreen(t, 110)
	m, _ = press(t, m, "down")

	m, cmd := press(t, m, "enter")
	m = deliver(t, m, cmd)

	assert.Equal(t, []string{"Home", "CLIs", "HEY CLI"}, m.nav.trail())
}

// --- Filling in and failing ---

// Three reads, each answering on its own, so the screen fills in as they land.
func TestProjectFillsInAsReadsLand(t *testing.T) {
	m := resize(t, newTestModel(t), 110, 26)
	p := newProject(m.ctx, project{id: 1, name: "CLIs"})
	p.now = func() time.Time { return testNow }
	m.push(p)

	p.Init()
	require.Equal(t, 3, p.pending)

	p.Update(projectActivityMsg{entries: testProjectActivity()})
	m.relayout()
	assert.Contains(t, ansi.Strip(p.View()), "3m ago")
	assert.Equal(t, 2, p.pending)

	p.Update(projectLoadedMsg{project: project{id: 1, name: "CLIs"}, tools: toTools(testDock())})
	m.relayout()
	assert.Contains(t, ansi.Strip(p.View()), "Message Board")
	assert.Equal(t, 1, p.pending)

	p.Update(projectLinksMsg{links: toLinks(testDoors())})
	m.relayout()
	assert.Contains(t, ansi.Strip(p.View()), "CLI repo")
	assert.Equal(t, 0, p.pending)
}

// The read that failed says so on the screen, and keeps the first complaint —
// a later one is the same outage said twice.
func TestProjectReadFailure(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	p := newProject(m.ctx, project{id: 1, name: "CLIs"})
	m.push(p)

	p.Update(projectLoadedMsg{err: errors.New("no route to host")})
	p.Update(projectActivityMsg{err: errors.New("also down")})
	m.relayout()

	assert.Contains(t, p.notice, "Could not load the project")
	assert.NotContains(t, p.notice, "also down")
	assert.Nil(t, m.err, "a project read put an error box over the screen")
}

// A read that failed leaves the name that was already known alone, rather than
// blanking the screen the reader walked into.
func TestAFailedProjectReadKeepsTheName(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	p := newProject(m.ctx, project{id: 1, name: "CLIs"})
	m.push(p)

	p.Update(projectLoadedMsg{err: errors.New("no route to host")})
	m.relayout()

	assert.Equal(t, "CLIs", p.Title())
	assert.Equal(t, []string{"Home", "CLIs"}, m.nav.trail())
}

// --- Layout ---

func TestProjectRowsFitTheColumn(t *testing.T) {
	for _, width := range []int{40, 60, 96, 140} {
		m, p := openProjectScreen(t, width)
		_ = m
		for _, line := range strings.Split(ansi.Strip(p.View()), "\n") {
			assert.LessOrEqual(t, len([]rune(line)), p.width, "at terminal width %d", width)
		}
	}
}
