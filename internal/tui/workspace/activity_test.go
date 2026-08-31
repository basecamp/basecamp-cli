package workspace

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// testNow is a fixed clock so "3m ago" means the same thing on every run. A
// Monday, so the day labels read the way they would on the web.
var testNow = time.Date(2026, time.August, 31, 14, 30, 0, 0, time.Local)

func testEvents() []activity {
	return []activity{
		{who: "Clawdito", what: "H1 #3982365: HEY name-tag HTML", where: "App Security",
			at: testNow.Add(-3 * time.Minute)},
		{who: "Jorge M.", what: "added a webhook", where: "BC5 Accessibility",
			at: testNow.Add(-12 * time.Minute)},
		{who: "Farah S.", what: "Ops: What have you worked on today?", where: "What Works [2026]",
			at: testNow.Add(-27 * time.Minute)},
		{who: "Marie C.", what: "Sidebar notifications return", where: "On Call",
			at: testNow.AddDate(0, 0, -1)},
		{who: "Rob Z.", what: "Snowglobe logo", where: "CLIs",
			at: testNow.AddDate(0, 0, -3)},
	}
}

// openActivity is the feed on screen, filled and sized, with the walk stopped so
// a test is not racing a page it never asked for.
func openActivity(t *testing.T, height int) (model, *activityScreen) {
	t.Helper()

	m := resize(t, newTestModel(t), 96, height)
	a := newActivity(m.ctx)
	a.now = func() time.Time { return testNow }
	m.push(a)

	a.Update(activityPageMsg{page: 1, events: testEvents()})
	a.done = true
	m.relayout()
	return m, a
}

// --- Days ---

// The feed is broken into days, and the day the reader is in is named rather
// than dated.
func TestActivityBreaksIntoDays(t *testing.T) {
	_, a := openActivity(t, 40)
	rendered := ansi.Strip(a.View())

	assert.Contains(t, rendered, "TODAY, MONDAY, AUGUST 31")
	assert.Contains(t, rendered, "YESTERDAY, SUNDAY, AUGUST 30")
	assert.Contains(t, rendered, "FRIDAY, AUGUST 28")

	// Three days, three headings — today's events are under one, not each.
	assert.Equal(t, 3, strings.Count(rendered, "AUGUST"))
}

// A heading runs a rule out to the right edge, the way the sidebar's sections do.
func TestActivityDayHeadingsAreRuled(t *testing.T) {
	_, a := openActivity(t, 40)

	for _, line := range strings.Split(ansi.Strip(a.View()), "\n") {
		if strings.Contains(line, "TODAY, MONDAY") {
			assert.Contains(t, line, "─")
			assert.Equal(t, a.width, tui.DisplayWidth(line))
			return
		}
	}
	t.Fatal("no day heading on screen")
}

// Which day an event belongs to is a question about the reader's clock. An event
// at 00:30 local is today even when it is still yesterday in UTC.
func TestDaysAreLocal(t *testing.T) {
	midnight := time.Date(2026, time.August, 31, 0, 30, 0, 0, time.Local)
	assert.True(t, sameDay(midnight, testNow))
	assert.True(t, sameDay(midnight.UTC(), testNow))
}

func TestDayLabel(t *testing.T) {
	assert.Equal(t, "TODAY, MONDAY, AUGUST 31", dayLabel(testNow, testNow))
	assert.Equal(t, "YESTERDAY, SUNDAY, AUGUST 30", dayLabel(testNow.AddDate(0, 0, -1), testNow))
	assert.Equal(t, "FRIDAY, AUGUST 28", dayLabel(testNow.AddDate(0, 0, -3), testNow))

	// The year shows up only once it is not the current one.
	assert.Equal(t, "SUNDAY, AUGUST 31, 2025", dayLabel(testNow.AddDate(-1, 0, 0), testNow))
}

// --- Time ---

// The relative time is what the web shows. The clock time under it is what a
// terminal has instead of a tooltip.
func TestActivityShowsBothTimes(t *testing.T) {
	_, a := openActivity(t, 40)
	rendered := ansi.Strip(a.View())

	assert.Contains(t, rendered, "3m ago")
	assert.Contains(t, rendered, testNow.Add(-3*time.Minute).Format("15:04"))
	assert.Contains(t, rendered, "12m ago")
	assert.Contains(t, rendered, "27m ago")
}

func TestSince(t *testing.T) {
	assert.Equal(t, "now", since(testNow.Add(-30*time.Second), testNow))
	assert.Equal(t, "3m ago", since(testNow.Add(-3*time.Minute), testNow))
	assert.Equal(t, "4h ago", since(testNow.Add(-4*time.Hour), testNow))
	assert.Equal(t, "2d ago", since(testNow.AddDate(0, 0, -2), testNow))
	assert.Equal(t, "3w ago", since(testNow.AddDate(0, 0, -21), testNow))

	// An event with no timestamp says nothing rather than saying 1970.
	assert.Equal(t, "", since(time.Time{}, testNow))
	assert.Equal(t, "", clockOf(time.Time{}))
}

// The times line up down the left rather than running ragged against the text.
func TestActivityTimesAreInAGutter(t *testing.T) {
	assert.Equal(t, gutterWidth, tui.DisplayWidth(gutter("3m ago")))
	assert.Equal(t, gutterWidth, tui.DisplayWidth(gutter("14:27")))
	assert.Equal(t, gutterWidth, tui.DisplayWidth(gutter("")))
	assert.True(t, strings.HasSuffix(gutter("3m ago"), "3m ago"), "the gutter is not right-aligned")
}

// --- Quick find ---

// The web puts a quick-find box beside its dropdowns; / opens ours.
func TestQuickFindFiltersWhatIsLoaded(t *testing.T) {
	m, a := openActivity(t, 40)
	require.Contains(t, ansi.Strip(a.View()), "added a webhook")

	m, _ = press(t, m, findKey)
	require.True(t, a.finding)
	for _, key := range strings.Split("snowglobe", "") {
		m, _ = press(t, m, key)
	}

	rendered := ansi.Strip(a.View())
	assert.Contains(t, rendered, "Snowglobe logo")
	assert.NotContains(t, rendered, "added a webhook")

	// The day the survivor belongs to comes with it; the others go.
	assert.Contains(t, rendered, "FRIDAY, AUGUST 28")
	assert.NotContains(t, rendered, "TODAY, MONDAY")
}

// The project and the person are searched along with the sentence.
func TestQuickFindMatchesProjectAndPerson(t *testing.T) {
	event := testEvents()[0]

	assert.True(t, event.matches("name-tag"))
	assert.True(t, event.matches("app security"))
	assert.True(t, event.matches("clawdito"))
	assert.True(t, event.matches(""))
	assert.False(t, event.matches("nothing here"))
}

func TestQuickFindSaysWhenNothingMatches(t *testing.T) {
	m, a := openActivity(t, 40)

	m, _ = press(t, m, findKey)
	for _, key := range strings.Split("zzz", "") {
		m, _ = press(t, m, key)
	}

	assert.Contains(t, ansi.Strip(a.View()), "Nothing matches zzz.")
}

// Enter leaves the box with the text still applied: the reader is done typing,
// not done filtering.
func TestEnterLeavesTheFindBoxApplied(t *testing.T) {
	m, a := openActivity(t, 40)

	m, _ = press(t, m, findKey)
	m, _ = press(t, m, "c")
	_, _ = press(t, m, "enter")

	assert.False(t, a.finding)
	assert.Equal(t, "c", a.find.Value())
}

// Esc clears the filter rather than popping the screen — the box is what the
// reader opened last, so it is what esc closes.
func TestEscapeClearsTheFindBeforePopping(t *testing.T) {
	m, a := openActivity(t, 40)
	require.Equal(t, 2, m.nav.depth())

	m, _ = press(t, m, findKey)
	m, _ = press(t, m, "c")
	m, _ = press(t, m, "esc")

	assert.False(t, a.finding)
	assert.Equal(t, "", a.find.Value())
	assert.Equal(t, 2, m.nav.depth(), "esc cleared the filter and popped the screen too")

	// With nothing left to close, esc goes back.
	m, _ = press(t, m, "esc")
	assert.Equal(t, 1, m.nav.depth())
}

// While the box has the keys, letters are letters rather than shortcuts.
func TestFindBoxCapturesInput(t *testing.T) {
	m, a := openActivity(t, 40)
	assert.False(t, a.CapturingInput())

	m, _ = press(t, m, findKey)
	assert.True(t, a.CapturingInput())

	// n makes a new project from home; in here it is just an n.
	m, _ = press(t, m, "n")
	assert.Equal(t, "n", a.find.Value())
	assert.Equal(t, 2, m.nav.depth())
}

// A filter that hides where the cursor was standing brings it back in range.
func TestQuickFindClampsTheCursor(t *testing.T) {
	m, a := openActivity(t, 40)
	for range 4 {
		m, _ = press(t, m, "down")
	}
	require.Equal(t, 4, a.cursor)

	m, _ = press(t, m, findKey)
	for _, key := range strings.Split("webhook", "") {
		m, _ = press(t, m, key)
	}

	assert.Equal(t, 1, a.visible())
	assert.Equal(t, 0, a.cursor)
}

// --- Walking and paging ---

func TestActivityCursorWalksAndStops(t *testing.T) {
	m, a := openActivity(t, 40)

	for range 10 {
		m, _ = press(t, m, "down")
	}
	assert.Equal(t, len(testEvents())-1, a.cursor)

	for range 10 {
		m, _ = press(t, m, "up")
	}
	assert.Equal(t, 0, a.cursor)
}

// Scrolling back up to an event brings its day's heading with it, so the reader
// can always see which day they are looking at.
func TestScrollingUpBringsTheDayBack(t *testing.T) {
	m, a := openActivity(t, 14)
	require.Less(t, a.height, len(a.layout()), "the feed fits, so nothing scrolls")

	for range 4 {
		m, _ = press(t, m, "down")
	}
	for range 4 {
		m, _ = press(t, m, "up")
	}

	assert.Contains(t, ansi.Strip(a.View()), "TODAY, MONDAY")
}

// Walking into the tail asks for the page below it.
func TestActivityPagesInAsYouWalk(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	a := newActivity(m.ctx)
	a.now = func() time.Time { return testNow }
	m.push(a)

	many := make([]activity, 40)
	for index := range many {
		many[index] = activity{what: "Event", at: testNow}
	}
	a.Update(activityPageMsg{page: 1, events: many})
	a.paging = false
	m.relayout()

	assert.Nil(t, a.readMore(), "asked for another page while still near the top")

	for range len(many) {
		m, _ = press(t, m, "down")
	}
	assert.True(t, a.paging, "reaching the end did not ask for another page")
}

// The walk is driven by everything read, not by what the filter leaves: the box
// filters what is here rather than reading ahead until it finds a match.
func TestQuickFindDoesNotDriveThePaging(t *testing.T) {
	m, a := openActivity(t, 40)
	a.done = false
	a.paging = false

	m, _ = press(t, m, findKey)
	for _, key := range strings.Split("zzz", "") {
		m, _ = press(t, m, key)
	}

	assert.Equal(t, 0, a.visible())
	assert.False(t, a.paging, "an empty filter result asked for more pages")
}

func TestActivityEmptyPageEndsTheWalk(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	a := newActivity(m.ctx)
	m.push(a)

	a.Update(activityPageMsg{page: 1})
	assert.True(t, a.done)
	assert.Nil(t, a.readMore())
	assert.Contains(t, ansi.Strip(a.View()), "No activity yet.")
}

// A page that failed to arrive leaves what is on screen alone — it is still
// good — and stalls the walk rather than ending it.
func TestActivityFailedPageKeepsWhatItHas(t *testing.T) {
	m, a := openActivity(t, 40)
	a.done = false

	a.Update(activityPageMsg{page: 2, err: errors.New("no route to host")})
	m.relayout()

	assert.True(t, a.stalled)
	assert.False(t, a.done, "one dropped request ended the feed for good")
	assert.Empty(t, a.notice, "a failed page put a notice over rows that were fine")
	assert.Contains(t, ansi.Strip(a.View()), "added a webhook")
	assert.Nil(t, m.err)
}

// A stall says so, and the next key press tries again. A feed that stops dead
// with nothing said reads as the end of history.
func TestAStalledWalkSaysSoAndRetries(t *testing.T) {
	m, a := openActivity(t, 40)
	a.done = false

	a.Update(activityPageMsg{page: 2, err: errors.New("no route to host")})
	m.relayout()
	assert.Contains(t, ansi.Strip(a.View()), "Could not load more")

	for range len(testEvents()) {
		m, _ = press(t, m, "down")
	}
	assert.True(t, a.paging, "a stalled walk never tried again")
}

// Reaching the end says so too, so "everything" and "something broke" never
// look the same — and it names the day it reached, because the endpoint serves
// one day and "that's everything" would be a lie.
func TestTheEndOfTheFeedNamesTheDayItReached(t *testing.T) {
	_, a := openActivity(t, 40)

	assert.Contains(t, ansi.Strip(a.View()), "That's everything from friday, august 28.")
}

// A feed that only ever got today says today.
func TestTheEndOfATodayOnlyFeed(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	a := newActivity(m.ctx)
	a.now = func() time.Time { return testNow }
	m.push(a)

	a.Update(activityPageMsg{page: 1, events: testEvents()[:2]})
	a.Update(activityPageMsg{page: 2})
	m.relayout()

	assert.Contains(t, ansi.Strip(a.View()), "That's everything from today, monday, august 31.")
}

// A project's own feed reaches the end of its history, not the end of a day:
// projects/:id/timeline.json pages all the way back. So it says so plainly,
// where the account-wide feed has to name the day it got stuck on.
func TestTheEndOfAProjectsFeed(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	a := newProjectActivity(m.ctx, project{id: 48521764, name: "CLIs"})
	a.now = func() time.Time { return testNow }
	m.push(a)

	a.Update(activityPageMsg{page: 1, events: testEvents()})
	a.Update(activityPageMsg{page: 2})
	m.relayout()

	rendered := ansi.Strip(a.View())
	assert.Contains(t, rendered, "That's everything.")
	assert.NotContains(t, rendered, "everything from")
}

// A first page that fails has nothing to keep, so it says why.
func TestActivityFirstPageFailure(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	a := newActivity(m.ctx)
	m.push(a)

	a.Update(activityPageMsg{page: 1, err: errors.New("no route to host")})
	m.relayout()

	assert.Contains(t, a.notice, "Could not load the activity")
	assert.Nil(t, m.err, "an activity read put an error box over the screen")
}

// --- Layout ---

// Every row fits the content column, at every width.
func TestActivityRowsFitTheColumn(t *testing.T) {
	for _, width := range []int{40, 60, 96} {
		m := resize(t, newTestModel(t), width, 26)
		a := newActivity(m.ctx)
		a.now = func() time.Time { return testNow }
		m.push(a)
		a.Update(activityPageMsg{page: 1, events: testEvents()})
		m.relayout()

		for _, line := range strings.Split(ansi.Strip(a.View()), "\n") {
			assert.LessOrEqual(t, tui.DisplayWidth(line), a.width, "at terminal width %d", width)
		}
	}
}

func TestActivityIsInTheTrail(t *testing.T) {
	m, _ := openActivity(t, 26)
	assert.Equal(t, []string{"Home", "Latest activity"}, m.nav.trail())
}
