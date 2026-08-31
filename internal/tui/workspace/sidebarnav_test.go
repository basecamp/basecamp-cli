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
)

// focusedModel is a workspace with the notifications loaded and the sidebar
// focused, laid out wide enough for both columns.
func focusedModel(t *testing.T) model {
	t.Helper()

	m := resize(t, newTestModel(t), 90, 24)
	updated, _ := m.Update(readingsLoadedMsg{readings: testReadings()})
	m = updated.(model)

	m, _ = press(t, m, sidebarKey)
	require.True(t, m.sidebar.focused)
	return m
}

// x hands focus back to the screen and leaves the sidebar where it is.
func TestLeaveKeyReturnsFocusWithoutHiding(t *testing.T) {
	m := focusedModel(t)

	m, _ = press(t, m, sidebarLeaveKey)
	assert.False(t, m.sidebar.focused)
	assert.False(t, m.sidebar.hidden)
	assert.Greater(t, m.sidebarWidth(), 0)
	assert.Contains(t, screen(m), "Recently Bubbled Up")
}

// Esc leaves the sidebar rather than popping the screen behind it: the keys are
// over here, so this is what esc is closing.
func TestEscapeLeavesTheSidebar(t *testing.T) {
	m := focusedModel(t)
	m.push(&stubView{title: "Projects"})
	m.sidebar.focused = true
	require.Equal(t, 2, m.nav.depth())

	m, _ = press(t, m, "esc")
	assert.False(t, m.sidebar.focused)
	assert.Equal(t, 2, m.nav.depth(), "esc left the sidebar and popped a screen too")
}

func TestSidebarCursorWalksTheList(t *testing.T) {
	m := focusedModel(t)
	require.Equal(t, 0, m.sidebar.cursor)

	m, _ = press(t, m, "down")
	assert.Equal(t, 1, m.sidebar.cursor)

	m, _ = press(t, m, "up")
	assert.Equal(t, 0, m.sidebar.cursor)
}

// The cursor stops at both ends rather than wrapping or running off.
func TestSidebarCursorStopsAtTheEnds(t *testing.T) {
	m := focusedModel(t)

	m, _ = press(t, m, "up")
	assert.Equal(t, 0, m.sidebar.cursor)

	for range testReadings().count() + 5 {
		m, _ = press(t, m, "down")
	}
	assert.Equal(t, testReadings().count()-1, m.sidebar.cursor)
}

// The cursor walks straight through the section headings: they are labels, not
// somewhere to stand.
func TestSidebarCursorCrossesSections(t *testing.T) {
	m := focusedModel(t)
	items := testReadings().items()

	for index := range items {
		assert.Equal(t, index, m.sidebar.cursor)
		// The column truncates a long title, so the marker is checked against
		// as much of it as fits rather than the whole thing.
		title := []rune(items[index].title)
		head := string(title[:min(len(title), 12)])
		assert.Contains(t, screen(m), "› "+head, "cursor not on %q", items[index].title)
		m, _ = press(t, m, "down")
	}
}

// A cursor on a column nobody is driving points at nothing. Checked against the
// sidebar's own column: the home screen behind it draws a cursor of its own.
func TestSidebarMarkerOnlyShowsWhileFocused(t *testing.T) {
	m := focusedModel(t)
	require.Contains(t, ansi.Strip(m.sidebar.view()), "› ")

	m, _ = press(t, m, sidebarLeaveKey)
	assert.NotContains(t, ansi.Strip(m.sidebar.view()), "› ")
}

// Keys the sidebar does not claim still work from over there.
func TestSidebarLetsGlobalKeysThrough(t *testing.T) {
	m := focusedModel(t)

	m, _ = press(t, m, menuKey)
	assert.True(t, m.menu.open)
	m, _ = press(t, m, menuKey)

	// Going somewhere hands focus back: the reader asked for a screen, so that
	// is where the keys should go.
	m, _ = press(t, m, "2")
	assert.Equal(t, "Calendar", m.nav.current().Title())
	assert.False(t, m.sidebar.focused)
}

func TestShiftHTakesFocusBackFromTheSidebar(t *testing.T) {
	m := focusedModel(t)

	m, _ = press(t, m, homeKey)
	assert.False(t, m.sidebar.focused)
	assert.Equal(t, "Home", m.nav.current().Title())
}

// A sidebar that is not on screen cannot be the focused one.
func TestFocusGoesWithAHiddenSidebar(t *testing.T) {
	m := focusedModel(t)

	m = resize(t, m, 50, 24)
	assert.Equal(t, 0, m.sidebarWidth())
	assert.False(t, m.sidebar.focused)
}

// --- Scrolling ---

// A list longer than the column scrolls to keep the cursor's whole block on
// screen, rather than clipping its last line.
func TestSidebarScrollsToTheCursor(t *testing.T) {
	s := loadedSidebar(t, 30, 6)
	s.focused = true

	for range testReadings().count() {
		s.moveCursor(1)
	}

	rows := s.layout()
	first, last := -1, -1
	for index, row := range rows {
		if row.item == s.cursor {
			if first < 0 {
				first = index
			}
			last = index
		}
	}
	require.GreaterOrEqual(t, first, 0)
	assert.GreaterOrEqual(t, first, s.offset, "the cursor scrolled off the top")
	assert.Less(t, last, s.offset+s.height, "the cursor's last line was clipped")
}

// --- Infinite pagination ---

func TestNextPage(t *testing.T) {
	// Basecamp treats 0 and 1 as the same first page, so the first step is to 2.
	assert.Equal(t, int32(2), nextPage(0))
	assert.Equal(t, int32(3), nextPage(2))
	assert.Equal(t, int32(4), nextPage(3))
}

// Nearing the end of the list asks for the page below it, so the rows are there
// by the time the cursor arrives.
func TestNearingTheEndAsksForMore(t *testing.T) {
	s := loadedSidebar(t, 30, 20)
	assert.False(t, s.wantsMore() && s.cursor > 0)

	for range testReadings().count() {
		s.moveCursor(1)
	}
	assert.True(t, s.wantsMore())
}

// One page at a time: a cursor moving through the tail must not ask again while
// an answer is already on its way.
func TestOnlyOnePageIsAskedForAtATime(t *testing.T) {
	s := loadedSidebar(t, 30, 20)
	s.moveCursor(len(testReadings().items()))
	require.True(t, s.wantsMore())

	s.paging = true
	assert.False(t, s.wantsMore())
}

func TestAppendingAPage(t *testing.T) {
	s := loadedSidebar(t, 30, 20)
	before := len(s.readings.reads)

	s.appendReads(2, []reading{{title: "Older"}, {title: "Older still"}})

	assert.Equal(t, before+2, len(s.readings.reads))
	assert.Equal(t, int32(2), s.page)
	assert.False(t, s.exhausted)
	assert.Contains(t, ansi.Strip(s.view()), "Older")
}

// A page with nothing in it is the end of the list, and the walk stops.
func TestAnEmptyPageEndsTheWalk(t *testing.T) {
	s := loadedSidebar(t, 30, 20)
	s.moveCursor(len(testReadings().items()))
	require.True(t, s.wantsMore())

	s.appendReads(2, nil)
	assert.True(t, s.exhausted)
	assert.False(t, s.wantsMore())
}

// A fresh first page starts the walk over: the pages read so far were pages of a
// list that has since moved.
func TestAFreshReadRestartsTheWalk(t *testing.T) {
	s := loadedSidebar(t, 30, 20)
	s.appendReads(3, []reading{{title: "Older"}})
	s.exhausted = true
	require.Equal(t, int32(3), s.page)

	s.replace(testReadings())
	assert.Equal(t, int32(0), s.page)
	assert.False(t, s.exhausted)
	assert.Len(t, s.readings.reads, len(testReadings().reads))
}

// A fresh read that comes back shorter must not leave the cursor pointing past
// the end of it.
func TestAFreshReadClampsTheCursor(t *testing.T) {
	s := loadedSidebar(t, 30, 20)
	s.moveCursor(len(testReadings().items()))
	require.Positive(t, s.cursor)

	s.replace(readings{unreads: []reading{{title: "Only one"}}})
	assert.Equal(t, 0, s.cursor)
}

// The section's rule goes dashed while a page is on its way, so a pause before
// the rows appear reads as loading rather than as the end of the list.
func TestPagingDashesTheSectionRule(t *testing.T) {
	s := loadedSidebar(t, 30, 20)
	assert.NotContains(t, ansi.Strip(s.view()), "┄")

	s.paging = true
	rendered := ansi.Strip(s.view())
	assert.Contains(t, rendered, "┄")
	assert.Contains(t, rendered, "Previous notifications ┄")

	// Only the section that is growing: the others are complete.
	assert.Contains(t, rendered, "New for you ─")
}

// A page that failed to arrive stops the walk and leaves the rows already on
// screen alone — they are still good.
func TestAFailedPageStopsTheWalkQuietly(t *testing.T) {
	m := focusedModel(t)
	before := len(m.sidebar.readings.reads)

	updated, _ := m.Update(moreReadingsLoadedMsg{page: 2, err: errors.New("no route to host")})
	m = updated.(model)

	assert.True(t, m.sidebar.exhausted)
	assert.False(t, m.sidebar.paging)
	assert.Empty(t, m.sidebar.notice, "a failed page put a notice over rows that were fine")
	assert.Len(t, m.sidebar.readings.reads, before)
	assert.Nil(t, m.err)
}

// Walking into the tail asks the model for the next page, and the answer lands
// under what is already there.
func TestWalkingIntoTheTailPagesIn(t *testing.T) {
	m := focusedModel(t)

	// The ask goes out on the first move into the tail, not the last: after
	// that one is in flight and the rest of the walk is silent.
	asked := 0
	for range testReadings().count() {
		var cmd tea.Cmd
		m, cmd = press(t, m, "down")
		if cmd != nil {
			asked++
		}
	}
	assert.Equal(t, 1, asked, "the walk asked for %d pages instead of one", asked)
	assert.True(t, m.sidebar.paging)

	more := make([]reading, 3)
	for index := range more {
		more[index] = reading{title: fmt.Sprintf("Older %d", index+1)}
	}
	updated, _ := m.Update(moreReadingsLoadedMsg{page: 2, reads: more})
	m = updated.(model)

	assert.False(t, m.sidebar.paging)
	assert.Equal(t, int32(2), m.sidebar.page)

	// The rows are in the list; whether they are on screen is up to where the
	// cursor has scrolled to.
	titles := make([]string, 0, len(m.sidebar.readings.reads))
	for _, item := range m.sidebar.readings.reads {
		titles = append(titles, item.title)
	}
	assert.Contains(t, strings.Join(titles, "\n"), "Older 1")
}
