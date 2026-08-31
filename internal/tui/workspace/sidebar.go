package workspace

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

const (
	// sidebarKey hides and shows it; the divider says so, so the reader can find
	// it again once it is gone.
	sidebarKey      = "S"
	sidebarHintText = "shift+s for sidebar"

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
	hidden   bool
	readings readings
	notice   string
	loaded   bool
}

// unread is how much is new, which is what the divider's badge counts.
func (s sidebar) unread() int { return len(s.readings.unreads) }

// ping reports whether any of it was aimed at the reader.
func (s sidebar) ping() bool { return s.readings.pings() }

func (s *sidebar) toggle() {
	s.hidden = !s.hidden
}

// fits reports whether a terminal this wide has room for both columns. A
// sidebar that would squeeze the content past reading is not worth having.
func (s sidebar) fits(screenWidth int) bool {
	return screenWidth-sidebarGutter-sidebarColumns(screenWidth) >= contentMinWidth
}

// width is how many columns the sidebar takes, or none when it is hidden or does
// not fit.
func (s sidebar) width(screenWidth int) int {
	if s.hidden || !s.fits(screenWidth) {
		return 0
	}
	return sidebarColumns(screenWidth)
}

func sidebarColumns(screenWidth int) int {
	return min(max(screenWidth/3, sidebarMinWidth), sidebarMaxWidth)
}

// view is the sidebar itself: the bubble-ups first, then what is new, then what
// has been read — the order the web shows them in.
//
// It draws what fits and stops. Nothing scrolls yet, so the order is what
// decides who gets the rows: the two bubble-ups are the shortest list and the
// most perishable, and the reads at the bottom are the ones already seen.
func (s sidebar) view(styles *tui.Styles, width, height int) string {
	rows := s.rows(styles, width)
	if len(rows) > height {
		rows = rows[:height]
	}
	return lipgloss.NewStyle().Width(width).Height(height).Render(strings.Join(rows, "\n"))
}

func (s sidebar) rows(styles *tui.Styles, width int) []string {
	switch {
	case s.notice != "":
		return wrapText(s.notice, width)
	case !s.loaded:
		return []string{styles.Muted.Render("Loading…")}
	case s.unread() == 0 && len(s.readings.bubbleUps) == 0 && len(s.readings.reads) == 0:
		return wrapText("✨ Nothing new for you.", width)
	}

	rows := make([]string, 0, 32)
	add := func(heading string, style lipgloss.Style, items []reading) {
		if len(items) == 0 {
			return
		}
		if len(rows) > 0 {
			rows = append(rows, "")
		}
		rows = append(rows, style.Render(truncateToWidth(heading, width)))
		for index, item := range items {
			// An item with an excerpt is a block rather than a line, and two of
			// them run together without a gap. The one-line rows stay tight:
			// spacing every row would halve what a short column can show.
			if index > 0 && item.excerpt != "" {
				rows = append(rows, "")
			}
			rows = append(rows, s.readingRows(styles, item, width)...)
		}
	}

	theme := styles.Theme()
	heading := lipgloss.NewStyle().Foreground(theme.Foreground).Bold(true)

	add(s.bubbleUpHeading(), heading, s.readings.bubbleUps)
	// "New for you" is the one heading the web colors, and the accent is what it
	// colors it with.
	add("New for you", lipgloss.NewStyle().Foreground(theme.Primary).Bold(true), s.readings.unreads)
	add("Previous notifications", heading, s.readings.reads)
	return rows
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
func (s sidebar) readingRows(styles *tui.Styles, item reading, width int) []string {
	theme := styles.Theme()
	inner := max(width-2, 1)

	title := lipgloss.NewStyle().Foreground(theme.Foreground)
	count := ""
	if item.unread > 0 {
		title = title.Bold(true)
		count = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Render(strconv.Itoa(item.unread))
	}

	rows := []string{"  " + fitRow(title, item.title, count, inner)}
	if item.excerpt != "" {
		rows = append(rows, "  "+styles.Muted.Render(truncateToWidth(item.excerpt, inner)))
	}
	if meta := strings.Join(nonEmpty(item.when, item.who, item.where), " · "); meta != "" {
		rows = append(rows, "  "+styles.Muted.Render(truncateToWidth(meta, inner)))
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
	tagWidth := lipgloss.Width(tag)
	text = truncateToWidth(text, max(width-tagWidth-1, 1))
	gap := max(width-lipgloss.Width(text)-tagWidth, 1)
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
// The count takes the accent and the ping takes the error color, which on an
// Omarchy palette is the theme's own red — the convention HEY follows for
// anything wanting attention, since that is the slot desktop themes signal
// alerts with. Both come from the theme, so a retint carries them.
func (s sidebar) badge(styles *tui.Styles) string {
	if s.unread() == 0 {
		return ""
	}
	theme := styles.Theme()

	badge := lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).
		Render(fmt.Sprintf("%d new", s.unread()))
	if s.ping() {
		badge += styles.Muted.Render(" + ")
		badge += lipgloss.NewStyle().Foreground(theme.Error).Bold(true).Render("ping")
	}
	return badge
}
