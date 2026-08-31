package workspace

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func listItems(n int) []listItem {
	items := make([]listItem, n)
	for index := range items {
		chosen := project{id: int64(index), name: fmt.Sprintf("Project %02d", index)}
		items[index] = listItem{
			label:    chosen.name,
			subtitle: fmt.Sprintf("Description %02d", index),
			project:  &chosen,
		}
	}
	return items
}

func openList(t *testing.T, count int) (model, *listScreen) {
	t.Helper()

	m := resize(t, newTestModel(t), 96, 26)
	screen := newAllProjects(m.ctx)
	m.push(screen)
	screen.Update(listPageMsg{kind: listProjects, page: 1, items: listItems(count)})
	screen.Update(listPageMsg{kind: listProjects, page: 2})
	m.relayout()
	return m, screen
}

// Every row is its label and the quieter line under it.
func TestListRendersRows(t *testing.T) {
	m, _ := openList(t, 4)
	rendered := screen(m)

	assert.Contains(t, rendered, "Project 00")
	assert.Contains(t, rendered, "Description 00")
	assert.Contains(t, rendered, "› Project 00")
	assert.Contains(t, m.nav.trail(), "All projects")
}

func TestListCursorWalksAndStops(t *testing.T) {
	m, l := openList(t, 4)

	for range 10 {
		m, _ = press(t, m, "down")
	}
	assert.Equal(t, 3, l.cursor)

	for range 10 {
		m, _ = press(t, m, "up")
	}
	assert.Equal(t, 0, l.cursor)
}

func TestListEnterOpensAProject(t *testing.T) {
	m, _ := openList(t, 4)
	m, _ = press(t, m, "down")

	m, cmd := press(t, m, "enter")
	m = deliver(t, m, cmd)

	assert.Equal(t, []string{"Home", "Project 01"}, m.nav.trail())
}

// A row with nothing behind it leads nowhere, so enter does nothing rather than
// pretending it went somewhere.
func TestListRowsWithNoProjectDoNotOpen(t *testing.T) {
	m, l := openList(t, 4)
	l.items[0].project = nil

	_, cmd := press(t, m, "enter")
	assert.Nil(t, cmd)
}

// A page for the other list is dropped rather than appended to whatever is on
// screen now.
func TestListIgnoresTheOtherListsPages(t *testing.T) {
	_, l := openList(t, 4)
	before := len(l.items)

	cmd, claimed := l.Update(listPageMsg{kind: listActivity, page: 2, items: listItems(3)})
	assert.False(t, claimed)
	assert.Nil(t, cmd)
	assert.Len(t, l.items, before)
}

// An empty page is the end of the list, and the walk stops.
func TestListEmptyPageEndsTheWalk(t *testing.T) {
	_, l := openList(t, 4)

	assert.True(t, l.done)
	assert.Nil(t, l.readMore())
}

// Walking into the tail asks for the page below it.
func TestListPagesInAsYouWalk(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	l := newAllProjects(m.ctx)
	m.push(l)

	l.Update(listPageMsg{kind: listProjects, page: 1, items: listItems(40)})
	l.paging = false
	m.relayout()

	// Near the top there is nothing to ask for yet.
	assert.Nil(t, l.readMore())

	for range len(l.items) {
		m, _ = press(t, m, "down")
	}
	assert.True(t, l.paging, "reaching the end did not ask for another page")
	assert.Equal(t, 2, l.page+1)
}

// A page that failed to arrive stops the walk and leaves the rows already on
// screen alone — they are still good.
func TestListFailedPageKeepsWhatItHas(t *testing.T) {
	m, l := openList(t, 4)
	l.done = false

	l.Update(listPageMsg{kind: listProjects, page: 2, err: errors.New("no route to host")})
	m.relayout()

	assert.True(t, l.done)
	assert.Empty(t, l.notice, "a failed page put a notice over rows that were fine")
	assert.Contains(t, screen(m), "Project 00")
}

// A first page that fails has nothing to keep, so it says why.
func TestListFirstPageFailure(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	l := newAllProjects(m.ctx)
	m.push(l)

	l.Update(listPageMsg{kind: listProjects, page: 1, err: errors.New("no route to host")})
	m.relayout()

	assert.Contains(t, l.notice, "Could not load all projects")
	assert.Contains(t, screen(m), "Could not load")
	assert.Nil(t, m.err, "a list read put an error box over the screen")
}

// Every row fits the content column.
func TestListRowsFitTheColumn(t *testing.T) {
	for _, width := range []int{40, 60, 96} {
		m := resize(t, newTestModel(t), width, 26)
		l := newAllProjects(m.ctx)
		m.push(l)
		l.Update(listPageMsg{kind: listProjects, page: 1, items: listItems(10)})
		m.relayout()

		for _, line := range strings.Split(ansi.Strip(l.View()), "\n") {
			assert.LessOrEqual(t, len([]rune(line)), l.width, "at terminal width %d", width)
		}
	}
}

// A list longer than the column scrolls to keep the cursor on screen.
func TestListScrollsToTheCursor(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 12)
	l := newAllProjects(m.ctx)
	m.push(l)
	l.Update(listPageMsg{kind: listProjects, page: 1, items: listItems(30)})
	l.done = true
	m.relayout()

	for range 20 {
		m, _ = press(t, m, "down")
	}

	require.GreaterOrEqual(t, l.cursor, l.offset, "the cursor scrolled off the top")
	assert.Less(t, l.cursor, l.offset+l.height/rowsPerListItem, "the cursor scrolled off the bottom")
}

func TestListWhileEmpty(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	l := newAllProjects(m.ctx)
	m.push(l)
	m.relayout()

	assert.Contains(t, ansi.Strip(l.View()), "Loading…")

	l.Update(listPageMsg{kind: listProjects, page: 1})
	assert.Contains(t, ansi.Strip(l.View()), "Nothing here.")
}
