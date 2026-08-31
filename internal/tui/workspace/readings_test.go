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

	"github.com/basecamp/basecamp-cli/internal/output"
)

func testReadings() readings {
	return readings{
		moreBubbleUps: 1,
		bubbleUps: []reading{
			{title: "Platform Documentation", excerpt: "With the release of the HEY CLI",
				who: "Rob Zolkos", where: "CLIs", when: "Aug 30"},
			{title: "Re: Pop a trial expiration date", excerpt: "Ah: it already is a Schedule::Entry",
				who: "Andy Smith", where: "Cycle 4", when: "Aug 26"},
		},
		unreads: []reading{
			{title: "Chat: Ops: On Call", who: "Prometheus", when: "08:29", unread: 1, ping: true},
			{title: "Chat: Ops", who: "Paul", when: "08:28", unread: 2},
		},
		reads: []reading{
			{title: "Chat: SIP", who: "Rosa", when: "08:15"},
		},
	}
}

func loadedSidebar() sidebar {
	return sidebar{loaded: true, readings: testReadings()}
}

// The three groups come in the order the web shows them, headings and all.
func TestSidebarSections(t *testing.T) {
	rendered := ansi.Strip(loadedSidebar().view(plainStyles(t), 36, 40))

	bubbles := strings.Index(rendered, "Recently Bubbled Up")
	unreads := strings.Index(rendered, "New for you")
	reads := strings.Index(rendered, "Previous notifications")

	require.GreaterOrEqual(t, bubbles, 0)
	assert.Less(t, bubbles, unreads)
	assert.Less(t, unreads, reads)
}

// The heading carries what the web's "View N more" link carries.
func TestSidebarSaysHowManyBubbleUpsAreBehindTheTwo(t *testing.T) {
	styles := plainStyles(t)

	assert.Contains(t, ansi.Strip(loadedSidebar().view(styles, 36, 40)),
		"Recently Bubbled Up · 1 more")

	none := loadedSidebar()
	none.readings.moreBubbleUps = 0
	rendered := ansi.Strip(none.view(styles, 36, 40))
	assert.Contains(t, rendered, "Recently Bubbled Up")
	assert.NotContains(t, rendered, "more")
}

// A row is its title, an excerpt when it has one, and who it was from and when.
func TestSidebarRendersAReading(t *testing.T) {
	rendered := ansi.Strip(loadedSidebar().view(plainStyles(t), 36, 40))

	assert.Contains(t, rendered, "Platform Documentation")
	assert.Contains(t, rendered, "With the release of the HEY CLI")
	assert.Contains(t, rendered, "Aug 30 · Rob Zolkos · CLIs")
}

// The unread count sits at the right of its row, the way the web's badge does.
func TestSidebarRendersTheUnreadCount(t *testing.T) {
	lines := strings.Split(ansi.Strip(loadedSidebar().view(plainStyles(t), 36, 40)), "\n")

	var row string
	for _, line := range lines {
		if strings.Contains(line, "Chat: Ops:") {
			row = line
			break
		}
	}
	require.NotEmpty(t, row)
	assert.True(t, strings.HasSuffix(strings.TrimRight(row, " "), "1"))

	// A read row carries no count.
	for _, line := range lines {
		if strings.Contains(line, "Chat: SIP") {
			assert.Equal(t, "Chat: SIP", strings.TrimSpace(line))
		}
	}
}

// Every row fits the column: one column over and the sidebar pushes the content
// out of its own half of the screen.
func TestSidebarRowsFitTheColumn(t *testing.T) {
	for _, width := range []int{20, 28, 36} {
		rendered := loadedSidebar().view(plainStyles(t), width, 40)
		for _, line := range strings.Split(ansi.Strip(rendered), "\n") {
			assert.LessOrEqual(t, len([]rune(line)), width, "at column width %d", width)
		}
	}
}

// A column too short for everything draws what fits and stops. Nothing scrolls
// yet, so the order is what decides: bubble-ups first, reads last.
func TestSidebarClipsToItsHeight(t *testing.T) {
	rendered := loadedSidebar().view(plainStyles(t), 36, 6)

	lines := strings.Split(rendered, "\n")
	assert.Len(t, lines, 6)
	assert.Contains(t, ansi.Strip(rendered), "Recently Bubbled Up")
	assert.NotContains(t, ansi.Strip(rendered), "Previous notifications")
}

func TestSidebarWhileLoading(t *testing.T) {
	assert.Contains(t, ansi.Strip(sidebar{}.view(plainStyles(t), 36, 10)), "Loading…")
}

func TestSidebarWithNothingNew(t *testing.T) {
	empty := sidebar{loaded: true}

	assert.Contains(t, ansi.Strip(empty.view(plainStyles(t), 36, 10)), "Nothing new for you")
}

// A sidebar that could not be read says so in its own column and leaves the
// screen alone — it is not what the reader was doing.
func TestSidebarNotice(t *testing.T) {
	m := resize(t, newTestModel(t), 84, 20)

	updated, _ := m.Update(readingsLoadedMsg{err: output.ErrNetwork(errors.New("no route to host"))})
	m = updated.(model)

	assert.Contains(t, m.sidebar.notice, "Could not load notifications")
	assert.Contains(t, m.sidebar.notice, "no route to host")
	// The column is narrow, so the notice wraps rather than reading as one line.
	assert.Contains(t, screen(m), "Could not load")

	assert.Nil(t, m.err, "a sidebar read put an error box over the screen")
	assert.Equal(t, "Home", m.nav.current().Title())
}

// --- Turning notifications into readings ---

func TestToReading(t *testing.T) {
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.Local)
	unreadAt := time.Date(2026, 8, 31, 8, 29, 0, 0, time.Local)

	row := toReading(basecamp.Notification{
		Title:          "Chat: Ops: On Call",
		ContentExcerpt: "Alert fired",
		BucketName:     "On Call",
		Section:        "pings",
		UnreadCount:    3,
		Creator:        &basecamp.Person{Name: "Prometheus"},
		UnreadAt:       &unreadAt,
	}, now)

	assert.Equal(t, reading{
		title:   "Chat: Ops: On Call",
		excerpt: "Alert fired",
		who:     "Prometheus",
		where:   "On Call",
		when:    "08:29",
		unread:  3,
		ping:    true,
	}, row)
}

// A notification carries a name someone typed into Basecamp, so it must not be
// able to repaint the sidebar.
func TestToReadingSanitizes(t *testing.T) {
	row := toReading(basecamp.Notification{
		Title:      "Ship\x1b[2Jit",
		BucketName: "Cycle\x1b[2J4",
	}, time.Now())

	assert.NotContains(t, row.title, "\x1b")
	assert.NotContains(t, row.where, "\x1b")
}

// A creator is optional: an item Basecamp raised itself has none.
func TestToReadingWithoutACreator(t *testing.T) {
	row := toReading(basecamp.Notification{Title: "You've got Boosts!"}, time.Now())

	assert.Equal(t, "", row.who)
}

// The clock for something from today, the date for anything older — the way the
// web sidebar words it.
func TestStamp(t *testing.T) {
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.Local)

	assert.Equal(t, "08:29", stamp(time.Date(2026, 8, 31, 8, 29, 0, 0, time.Local), now))
	assert.Equal(t, "Aug 30", stamp(time.Date(2026, 8, 30, 22, 0, 0, 0, time.Local), now))
	assert.Equal(t, "Aug 31", stamp(time.Date(2025, 8, 31, 8, 29, 0, 0, time.Local), now),
		"a year ago today is not today")
	assert.Equal(t, "", stamp(time.Time{}, now))
}

// The row is timed by whichever transition the notification carries, which is
// the field its own list is ordered by.
func TestReadingTime(t *testing.T) {
	created := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	unread := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	read := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)

	assert.Equal(t, created, readingTime(basecamp.Notification{CreatedAt: created}))
	assert.Equal(t, unread, readingTime(basecamp.Notification{CreatedAt: created, UnreadAt: &unread}))
	assert.Equal(t, read, readingTime(basecamp.Notification{CreatedAt: created, ReadAt: &read}))
}

func TestReadingsPings(t *testing.T) {
	assert.True(t, testReadings().pings())

	quiet := testReadings()
	quiet.unreads[0].ping = false
	assert.False(t, quiet.pings())
	assert.False(t, readings{}.pings())
}
