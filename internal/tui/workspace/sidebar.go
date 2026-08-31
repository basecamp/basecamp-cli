package workspace

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

const (
	// sidebarKey shows the sidebar, then focuses it, then hides it again;
	// sidebarLeaveKey hands focus back to the screen without hiding anything.
	// The divider says so, so the reader can find it either way.
	sidebarKey      = "S"
	sidebarLeaveKey = "x"
	sidebarHintText = "shift+s for sidebar"

	// How close to the end of the list the cursor gets before the next page is
	// asked for, so the rows are there by the time it arrives.
	pageAheadBy = 5

	// The gutter is the rule between the two columns and a space either side.
	sidebarGutter = 3

	// The sidebar takes a third of the terminal, between a width where its own
	// contents still read and one past which it is only spending the content's
	// columns.
	sidebarMinWidth = 20
	sidebarMaxWidth = 36

	// Below this the content column is too cramped to be worth the trade, and
	// the sidebar stands down until the terminal is widened.
	contentMinWidth = 34
)

// sidebar is the column beside the content: the notifications, in the three
// groups the web sidebar shows them in.
//
// It belongs to the frame rather than to a screen, which is why every screen is
// handed its container's width rather than the terminal's: what is drawn has to
// fit the column it lands in.
type sidebar struct {
	styles *tui.Styles

	hidden   bool
	focused  bool
	readings readings
	notice   string
	loaded   bool

	// Where the reader is in the list, and the first drawn row on screen.
	cursor int
	offset int

	// The walk down Previous notifications: the page last asked for, whether one
	// is in flight, and whether the server has run out of them.
	page      int32
	paging    bool
	exhausted bool

	width  int
	height int
}

func newSidebar(styles *tui.Styles) sidebar {
	return sidebar{styles: styles}
}

// unread is how much is new, which is what the divider's badge counts.
func (s sidebar) unread() int { return len(s.readings.unreads) }

// ping reports whether any of it was aimed at the reader.
func (s sidebar) ping() bool { return s.readings.pings() }

// summon is what shift+s does, in three steps: show the sidebar, focus it, and
// put it away again. Coming back is one key rather than a chord to remember, and
// the reader never has to know which of the three states it is in.
func (s *sidebar) summon() {
	switch {
	case s.hidden:
		s.hidden = false
		s.focused = true
	case !s.focused:
		s.focused = true
	default:
		s.focused = false
		s.hidden = true
	}
	s.cursor, s.offset = 0, 0
}

// leave hands focus back to the screen and leaves the sidebar where it is.
func (s *sidebar) leave() {
	s.focused = false
}

func (s *sidebar) resize(width, height int) {
	s.width = width
	s.height = height
	s.scrollToCursor()
}

// moveCursor walks the list and answers whether the reader has come close enough
// to the end to want the next page of it.
func (s *sidebar) moveCursor(by int) bool {
	s.cursor = max(min(s.cursor+by, s.readings.count()-1), 0)
	s.scrollToCursor()
	return s.wantsMore()
}

// wantsMore is whether the next page of Previous notifications is worth asking
// for: there are more to have, none in flight, and the cursor is near the end of
// what is already there.
func (s sidebar) wantsMore() bool {
	if s.paging || s.exhausted || !s.loaded || len(s.readings.reads) == 0 {
		return false
	}
	return s.cursor >= s.readings.count()-pageAheadBy
}

// scrollToCursor slides the window until every row of the cursor's reading is on
// screen, keeping the whole block together rather than clipping its last line.
func (s *sidebar) scrollToCursor() {
	// Before the frame has sized it there is no window to scroll, and treating
	// its height as zero would push every row off the top.
	if s.height <= 0 {
		s.offset = 0
		return
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
	if first < 0 {
		s.offset = 0
		return
	}

	s.offset = min(s.offset, topOfSidebar(rows, first))
	if last >= s.offset+s.height {
		s.offset = last - s.height + 1
	}
	s.offset = max(min(s.offset, max(len(rows)-s.height, 0)), 0)
}

// topOfSidebar is the row to scroll to when the cursor lands on the reading
// starting at `first`: the heading and the gap above it come with it, so
// scrolling back up to the first reading of a section brings its heading along.
func topOfSidebar(rows []sidebarRow, first int) int {
	top := first
	for top > 0 && rows[top-1].item == noItem {
		top--
	}
	return top
}

// fits reports whether a terminal this wide has room for both columns. A
// sidebar that would squeeze the content past reading is not worth having.
func (s sidebar) fits(screenWidth int) bool {
	return screenWidth-sidebarGutter-sidebarColumns(screenWidth) >= contentMinWidth
}

// columns is how many the sidebar takes on a terminal this wide, or none when it
// is hidden or does not fit.
func (s sidebar) columns(screenWidth int) int {
	if s.hidden || !s.fits(screenWidth) {
		return 0
	}
	return sidebarColumns(screenWidth)
}

func sidebarColumns(screenWidth int) int {
	return min(max(screenWidth/3, sidebarMinWidth), sidebarMaxWidth)
}

// sidebarRow is one drawn line and the reading it belongs to. The cursor moves
// between readings rather than lines, so scrolling has to know which lines a
// reading owns — noItem marks a heading or a gap, which the cursor skips over.
type sidebarRow struct {
	text string
	item int
}

const noItem = -1

// replace takes a fresh first page — a startup read, an account switch, or the
// answer to a doorbell — and starts the walk over. The pages read so far were
// pages of a list that has since moved.
func (s *sidebar) replace(fresh readings) {
	s.readings = fresh
	s.page = 0
	s.exhausted = false
	s.cursor = min(s.cursor, max(fresh.count()-1, 0))
	s.scrollToCursor()
}

// appendReads puts a page of previous notifications under the ones on screen. A
// page with nothing in it is the end of the list.
func (s *sidebar) appendReads(page int32, reads []reading) {
	if len(reads) == 0 {
		s.exhausted = true
		return
	}
	s.page = page
	s.readings.reads = append(s.readings.reads, reads...)
	s.scrollToCursor()
}

// view is the sidebar itself: the bubble-ups first, then what is new, then what
// has been read — the order the web shows them in, scrolled to wherever the
// cursor is.
func (s sidebar) view() string {
	rows := s.layout()

	end := min(s.offset+s.height, len(rows))
	lines := make([]string, 0, max(end-s.offset, 0))
	for _, row := range rows[min(s.offset, end):end] {
		lines = append(lines, row.text)
	}
	return lipgloss.NewStyle().Width(s.width).Height(s.height).Render(strings.Join(lines, "\n"))
}

func (s sidebar) layout() []sidebarRow {
	plain := func(lines []string) []sidebarRow {
		rows := make([]sidebarRow, 0, len(lines))
		for _, line := range lines {
			rows = append(rows, sidebarRow{text: line, item: noItem})
		}
		return rows
	}

	switch {
	case s.notice != "":
		return plain(wrapText(s.notice, s.width))
	case !s.loaded:
		return plain([]string{s.styles.Muted.Render("Loading…")})
	case s.readings.count() == 0:
		return plain(wrapText("✨ Nothing new for you.", s.width))
	}

	rows := make([]sidebarRow, 0, 32)
	item := 0
	add := func(heading string, style lipgloss.Style, dashed bool, items []reading) {
		if len(items) == 0 {
			return
		}
		if len(rows) > 0 {
			rows = append(rows, sidebarRow{item: noItem})
		}
		rows = append(rows, sidebarRow{text: s.sectionHeading(heading, style, dashed), item: noItem})

		for index, reading := range items {
			// An item with an excerpt is a block rather than a line, and two of
			// them run together without a gap. The one-line rows stay tight:
			// spacing every row would halve what a short column can show.
			if index > 0 && reading.excerpt != "" {
				rows = append(rows, sidebarRow{item: noItem})
			}
			for _, line := range s.readingRows(reading, item, s.width) {
				rows = append(rows, sidebarRow{text: line, item: item})
			}
			item++
		}
	}

	theme := s.styles.Theme()
	heading := lipgloss.NewStyle().Foreground(theme.Foreground).Bold(true)

	add(s.bubbleUpHeading(), heading, false, s.readings.bubbleUps)
	// "New for you" is the one heading the web colors, and it colors it orange —
	// the warning slot here, which an Omarchy palette fills with its own yellow.
	add("New for you", lipgloss.NewStyle().Foreground(theme.Warning).Bold(true), false, s.readings.unreads)
	// A dashed rule while another page is on its way, so a pause before the rows
	// appear reads as loading rather than as the end of the list.
	add("Previous notifications", heading, s.paging, s.readings.reads)
	return rows
}

// sectionHeading is a heading with rule running out to the right edge, so the
// groups read as separate rather than as one long list.
//
//	New for you ────────────────────
func (s sidebar) sectionHeading(label string, style lipgloss.Style, dashed bool) string {
	label = truncateToWidth(label, max(s.width-2, 1))

	rule := s.width - tui.DisplayWidth(label) - 1
	if rule < 1 {
		return style.Render(label)
	}

	dash := "─"
	if dashed {
		dash = "┄"
	}
	return style.Render(label) + " " +
		lipgloss.NewStyle().Foreground(s.styles.Theme().Border).Render(strings.Repeat(dash, rule))
}

// bubbleUpHeading carries what the web's "View N more" link carries: that there
// are bubble-ups behind the two on screen.
func (s sidebar) bubbleUpHeading() string {
	if s.readings.moreBubbleUps > 0 {
		return fmt.Sprintf("Recently Bubbled Up · %d more", s.readings.moreBubbleUps)
	}
	return "Recently Bubbled Up"
}

// readingRows is one notification: its title with the unread count beside it, a
// line of the excerpt when there is one, and who it was from and when.
//
// The count is what marks a row unread, the way the web's badge does — a bold
// title and a number beside it say it twice, and the number is the half that
// carries information.
func (s sidebar) readingRows(item reading, index, width int) []string {
	theme := s.styles.Theme()
	inner := max(width-2, 1)

	// An unread count is red, the way the web's badge is. The accent is spent on
	// the cursor alone: three things in one color is three things you cannot
	// tell apart, and where the reader is standing is the one that has to read
	// at a glance.
	title := lipgloss.NewStyle().Foreground(theme.Foreground)
	count := ""
	if item.unread > 0 {
		title = title.Bold(true)
		count = lipgloss.NewStyle().Foreground(theme.Error).Bold(true).Render(strconv.Itoa(item.unread))
	}

	// The marker only appears while the sidebar has focus: a cursor on a column
	// nobody is driving points at nothing.
	marker := "  "
	if s.focused && index == s.cursor {
		marker = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Render("› ")
		title = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	}

	rows := []string{marker + fitRow(title, item.title, count, inner)}
	if item.excerpt != "" {
		rows = append(rows, "  "+s.styles.Muted.Render(truncateToWidth(item.excerpt, inner)))
	}
	if meta := strings.Join(nonEmpty(item.when, item.who, item.where), " · "); meta != "" {
		rows = append(rows, "  "+s.styles.Muted.Render(truncateToWidth(meta, inner)))
	}
	return rows
}

// fitRow puts text on the left and a tag on the right of one row, giving the
// text whatever the tag leaves. The tag arrives styled; the text is styled here,
// after it has been cut to fit — a string cannot be truncated once it carries
// escape sequences.
func fitRow(style lipgloss.Style, text, tag string, width int) string {
	if tag == "" {
		return style.Render(truncateToWidth(text, max(width, 1)))
	}
	tagWidth := tui.DisplayWidth(tag)
	text = truncateToWidth(text, max(width-tagWidth-1, 1))
	gap := max(width-tui.DisplayWidth(text)-tagWidth, 1)
	return style.Render(text) + strings.Repeat(" ", gap) + tag
}

func nonEmpty(values ...string) []string {
	kept := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			kept = append(kept, value)
		}
	}
	return kept
}

// gutter is the rule between the columns and the space either side of it, drawn
// the full height of them. Every row carries its own padding: a block padded as
// one string only pads its first line.
func (s sidebar) gutter(styles *tui.Styles, height int) string {
	row := " " + lipgloss.NewStyle().Foreground(styles.Theme().Border).Render("│") + " "
	rows := make([]string, max(height, 1))
	for index := range rows {
		rows[index] = row
	}
	return strings.Join(rows, "\n")
}

// badge is the notification count for the divider: "4 new", or "3 new + ping"
// when some of it was addressed to the reader.
//
// It carries the same two colors the list below it does — orange for what is
// new, matching the heading it counts, and red for the one aimed at the reader.
// Both come from the theme, so an Omarchy retint carries them, and neither is
// the accent: that belongs to the cursor.
func (s sidebar) badge(styles *tui.Styles) string {
	if s.unread() == 0 {
		return ""
	}
	theme := styles.Theme()

	badge := lipgloss.NewStyle().Foreground(theme.Warning).Bold(true).
		Render(fmt.Sprintf("%d new", s.unread()))
	if s.ping() {
		badge += styles.Muted.Render(" + ")
		badge += lipgloss.NewStyle().Foreground(theme.Error).Bold(true).Render("ping")
	}
	return badge
}
