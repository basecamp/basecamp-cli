package workspace

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
)

const (
	// The web puts a quick-find box beside the two filter dropdowns. It matches
	// against what is already rendered rather than asking the server again, so
	// the same key does the same job here.
	findKey = "/"
)

// activityPageMsg is a page of the feed.
type activityPageMsg struct {
	page   int
	events []activity
	err    error
}

// activityScreen is the Latest Activity feed, read a page at a time and broken
// into days. It serves the whole account, and one project when it was opened
// from that project's own screen.
//
// The account-wide feed shows one day, because that is all
// reports/progress.json will serve. The endpoint takes a `date`, a
// `project_ids[]` and a `people_ids[]`, and honors all three — the SDK passes
// none of them, only `page`. So the day walk the web does, and its two filter
// dropdowns, are not buildable here yet. See "[SDK] Pass the activity date and
// filters through" on the CLIs board.
//
// A project's own feed has no such limit: projects/:id/timeline.json pages back
// through the whole history, so that walk really does reach the end.
type activityScreen struct {
	ctx *Context

	// inside is the project this feed belongs to, and nil for the account-wide
	// one. It decides which endpoint is read and how far back the walk can
	// honestly claim to have gone.
	inside *project

	events []activity
	page   int
	paging bool
	// done is the end of the feed; stalled is a page that did not arrive, which
	// the next key press retries.
	done    bool
	stalled bool
	notice  string

	// The quick-find box, and whether the reader is typing in it.
	find    textinput.Model
	finding bool

	cursor int
	offset int
	width  int
	height int

	// now is when the screen last laid itself out, which is what every "3m ago"
	// is relative to. Held rather than read per row so one frame cannot say two
	// different things about the same event.
	now func() time.Time
}

func newActivity(ctx *Context) *activityScreen {
	find := textinput.New()
	find.Prompt = ""
	find.Placeholder = "Filter…"

	return &activityScreen{ctx: ctx, find: find, now: time.Now}
}

// newProjectActivity is the same feed read through one project. The breadcrumb
// already says which project, so the screen is called what it is called
// everywhere else.
func newProjectActivity(ctx *Context, inside project) *activityScreen {
	screen := newActivity(ctx)
	screen.inside = &inside
	return screen
}

func (a *activityScreen) Init() tea.Cmd {
	a.events, a.page, a.done, a.notice = nil, 0, false, ""
	return a.readMore()
}

func (a *activityScreen) Title() string { return "Latest activity" }

func (a *activityScreen) Loading() bool { return false }

func (a *activityScreen) Resize(width, height int) {
	a.width = width
	a.height = height
	a.find.SetWidth(max(width-2, 1))
	a.scrollToCursor()
}

// CapturingInput is true while the quick-find box has the keys.
func (a *activityScreen) CapturingInput() bool { return a.finding }

// HandleBack closes the quick-find box rather than letting esc pop the screen:
// the box is what the reader opened last, so it is what esc closes.
func (a *activityScreen) HandleBack() bool {
	if !a.finding && a.find.Value() == "" {
		return false
	}
	a.clearFind()
	return true
}

func (a *activityScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	page, ok := msg.(activityPageMsg)
	if !ok {
		if a.finding {
			find, cmd := a.find.Update(msg)
			a.find = find
			return cmd, false
		}
		return nil, false
	}

	a.paging = false
	if page.err != nil {
		// What is on screen is still good, so a page that failed to arrive leaves
		// it alone. The walk stalls rather than ending: a feed that stops dead on
		// one dropped request, with nothing said and no way back, reads as the end
		// of history. The next key press tries again.
		a.stalled = true
		if len(a.events) == 0 {
			a.notice = errorNotice("Could not load the activity", page.err)
		}
		return nil, true
	}

	a.stalled = false
	if len(page.events) == 0 {
		a.done = true
		return nil, true
	}

	a.page = page.page
	a.events = append(a.events, page.events...)
	a.scrollToCursor()
	return nil, true
}

func (a *activityScreen) HandleKey(msg tea.KeyPressMsg) tea.Cmd {
	if a.finding {
		return a.handleFindKey(msg)
	}

	switch {
	case msg.String() == findKey:
		a.finding = true
		return a.find.Focus()
	case msg.Key().Code == tea.KeyUp:
		a.cursor = max(a.cursor-1, 0)
	case msg.Key().Code == tea.KeyDown:
		a.cursor = min(a.cursor+1, max(a.visible()-1, 0))
	default:
		return nil
	}

	a.scrollToCursor()
	return a.readMore()
}

// handleFindKey is the quick-find box's own keys. Enter leaves the box with the
// text still applied — the reader is done typing, not done filtering.
func (a *activityScreen) handleFindKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.Key().Code {
	case tea.KeyEnter:
		a.finding = false
		a.find.Blur()
		return nil
	case tea.KeyEsc:
		a.clearFind()
		return nil
	}

	find, cmd := a.find.Update(msg)
	a.find = find
	a.clampCursor()
	return cmd
}

func (a *activityScreen) clearFind() {
	a.finding = false
	a.find.Blur()
	a.find.SetValue("")
	a.clampCursor()
}

// --- What is on screen ---

// showing is the events the quick-find leaves, which is what the cursor walks
// and what the days are built from.
func (a *activityScreen) showing() []activity {
	needle := strings.TrimSpace(a.find.Value())
	if needle == "" {
		return a.events
	}

	kept := make([]activity, 0, len(a.events))
	for _, event := range a.events {
		if event.matches(needle) {
			kept = append(kept, event)
		}
	}
	return kept
}

func (a *activityScreen) visible() int { return len(a.showing()) }

func (a *activityScreen) clampCursor() {
	a.cursor = max(min(a.cursor, a.visible()-1), 0)
	a.scrollToCursor()
}

// readMore asks for the page below what is loaded, when the cursor has come near
// the end of it or there is not enough to fill the screen.
//
// The walk is driven by everything read, not by what the quick-find leaves: the
// box filters what is here, the way the web's does, rather than reading ahead
// until it finds a match.
func (a *activityScreen) readMore() tea.Cmd {
	if a.paging || a.done {
		return nil
	}
	if len(a.events) > 0 && len(a.events) > a.height && a.cursor < len(a.events)-pageAheadBy {
		return nil
	}

	a.paging = true
	if a.inside != nil {
		return loadProjectActivityPage(a.ctx.Ctx(), a.ctx.app, a.inside.id, a.page+1)
	}
	return loadActivityPage(a.ctx.Ctx(), a.ctx.app, a.page+1)
}

func (a *activityScreen) scrollToCursor() {
	if a.height <= 0 {
		a.offset = 0
		return
	}

	// An entry is two lines, and both of them are the entry: scrolling to its
	// first line and stopping there leaves the second one clipped off the bottom.
	rows := a.layout()
	first, last := -1, -1
	for index, row := range rows {
		if row.item == a.cursor {
			if first < 0 {
				first = index
			}
			last = index
		}
	}
	if first < 0 {
		a.offset = 0
		return
	}

	// Scrolling up to an event brings its day's heading back with it, so the
	// reader can always see which day they are looking at.
	a.offset = min(a.offset, topOf(rows, first))
	if last >= a.offset+a.height {
		a.offset = last - a.height + 1
	}
	a.offset = max(min(a.offset, max(len(rows)-a.height, 0)), 0)
}

// --- Rendering ---

func (a *activityScreen) View() string {
	if a.notice != "" {
		return strings.Join(wrapText(a.notice, a.width), "\n")
	}

	rows := a.layout()
	end := min(a.offset+a.height, len(rows))
	lines := make([]string, 0, max(end-a.offset, 0))
	for _, row := range rows[min(a.offset, end):end] {
		lines = append(lines, row.text)
	}
	return strings.Join(lines, "\n")
}

// layout draws the whole feed, day by day. The rows carry which event they
// belong to so the cursor and the scrolling can work in events while the screen
// works in lines.
func (a *activityScreen) layout() []homeRow {
	styles := a.ctx.Styles()
	showing := a.showing()

	var rows []homeRow
	plain := func(text string) { rows = append(rows, homeRow{text: text, item: noItem}) }
	item := func(text string, at int) { rows = append(rows, homeRow{text: text, item: at}) }

	rows = append(rows, homeRow{text: a.findRow(), item: noItem})
	plain("")

	switch {
	case len(a.events) == 0 && a.paging:
		plain(styles.Muted.Render("Loading…"))
		return rows
	case len(a.events) == 0:
		plain(styles.Muted.Render("No activity yet."))
		return rows
	case len(showing) == 0:
		plain(styles.Muted.Render("Nothing matches " + strings.TrimSpace(a.find.Value()) + "."))
		return rows
	}

	now := a.now()
	day := time.Time{}
	for index, event := range showing {
		if at := event.at; !sameDay(at, day) {
			day = at
			if index > 0 {
				plain("")
			}
			plain(a.dayHeading(at, now))
		}
		for _, line := range activityRows(styles, event, now, a.width, index == a.cursor) {
			item(line, index)
		}
	}

	plain("")
	plain(a.footer())
	return rows
}

// footer says why the feed ended, so reaching the bottom is never just a list
// that stopped. A reader who cannot tell "that is everything" from "something
// broke" assumes the worse one.
//
// What the end means depends on which feed this is. A project's timeline pages
// back through its whole history, so the bottom of it is the bottom. The
// account-wide feed reaches only the end of a day:
// reports/progress.json serves one day per request and defaults to today, and
// the day it serves is chosen by a `date` parameter the SDK does not pass. Until
// it does, that screen cannot walk back past midnight, and saying "that's
// everything" would be a lie.
func (a *activityScreen) footer() string {
	styles := a.ctx.Styles()
	switch {
	case a.paging:
		return styles.Muted.Render("Loading more…")
	case a.stalled:
		return lipgloss.NewStyle().Foreground(styles.Theme().Error).
			Render("Could not load more. Press ↓ to try again.")
	case a.done && a.inside != nil:
		return styles.Muted.Render("That's everything.")
	case a.done:
		return styles.Muted.Render("That's everything from " + a.oldestDay() + ".")
	default:
		return ""
	}
}

// oldestDay names the day the feed reaches back to, in the words the headings
// use — "today", "yesterday", or the date itself.
func (a *activityScreen) oldestDay() string {
	if len(a.events) == 0 {
		return "today"
	}
	oldest := a.events[len(a.events)-1].at
	if oldest.IsZero() {
		return "today"
	}
	return strings.ToLower(dayLabel(oldest, a.now()))
}

// findRow is the quick-find box, or the hint that opens it.
func (a *activityScreen) findRow() string {
	styles := a.ctx.Styles()
	if a.finding {
		return styles.Muted.Render("/ ") + a.find.View()
	}

	needle := strings.TrimSpace(a.find.Value())
	if needle == "" {
		return styles.Muted.Render("/ to filter")
	}
	return styles.Muted.Render("/ ") +
		lipgloss.NewStyle().Foreground(styles.Theme().Primary).Render(needle) +
		styles.Muted.Render("  esc to clear")
}

// dayHeading is the rule that separates one day from the next, worded the way
// the web words it: today and yesterday by name, everything else by its date.
func (a *activityScreen) dayHeading(at, now time.Time) string {
	styles := a.ctx.Styles()
	heading := lipgloss.NewStyle().Foreground(styles.Theme().Primary).Bold(true)
	return ruledHeading(styles, dayLabel(at, now), heading, a.width, false)
}

func (a *activityScreen) HelpBindings() []helpBinding {
	if a.finding {
		return []helpBinding{{"enter", "apply"}, {"esc", "clear"}}
	}
	return []helpBinding{{"↑↓", "move"}, {"/", "filter"}}
}

// --- Reading ---

func loadActivityPage(ctx context.Context, app *appctx.App, page int) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return activityPageMsg{page: page, err: err}
		}
		result, err := app.Account().Timeline().
			Progress(ctx, &basecamp.TimelineListOptions{Page: page})
		if err != nil {
			return activityPageMsg{page: page, err: err}
		}

		events := make([]activity, 0, len(result.Events))
		for _, event := range result.Events {
			events = append(events, toActivity(event))
		}
		return activityPageMsg{page: page, events: events}
	}
}
