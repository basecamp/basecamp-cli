package workspace

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// iconColumns is the room a folder icon takes, plus the space after it. An emoji
// is two cells wide wherever the terminal was asked — see CalibrateWidths. A
// project has no icon and takes the space one would anyway, so the names line up
// with the folders'.
const iconColumns = 3

// minTrailingRoom is the least a trailing note may be cut to before it is
// dropped instead. Three letters and an ellipsis says nothing.
const minTrailingRoom = 12

// itemRow is one line of a list of folders and projects: its name, then the
// quieter thing about it after a dash — a project's description, a folder's
// count.
//
// One line rather than two. A list of names is read by running an eye down the
// left edge, and a second line under each one puts a gap in that edge every
// other row; a hundred projects at two lines each is also twice the scrolling.
// The home screen, a folder, and the whole directory all draw their rows with
// this, so a project looks the same wherever it is listed.
type itemRow struct {
	// icon goes in the column before the name, and is empty for a project.
	icon string

	// tint is the color the reader gave a folder, and nil for everything else.
	// It goes on the name rather than on the icon: a folder's icon is an emoji,
	// and an emoji carries its own colors — the terminal paints it from the font
	// and ignores the foreground it was handed, so a color put there is a color
	// nobody ever sees. It is the card the web colors anyway, not the glyph.
	tint color.Color

	label    string
	trailing string

	// indent is the room left to the left of the icon for something else — the
	// directory's letter column.
	indent int

	selected bool
}

func (r itemRow) render(styles *tui.Styles, width int) string {
	theme := styles.Theme()

	marker := "  "
	name := lipgloss.NewStyle().Foreground(theme.Foreground)
	if r.tint != nil {
		name = name.Foreground(r.tint)
	}
	// The cursor wins the row it is on: where the reader is standing has to read
	// at a glance, and a folder's color is not that.
	if r.selected {
		marker = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Render("› ")
		name = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	}

	badge := strings.Repeat(" ", iconColumns)
	if r.icon != "" {
		badge = r.icon + strings.Repeat(" ", max(iconColumns-tui.DisplayWidth(r.icon), 1))
	}

	indent := strings.Repeat(" ", max(r.indent, 0))
	inner := max(width-2-max(r.indent, 0)-iconColumns, 1)

	// The name is never cut to fit what follows it: a name the reader cannot
	// read is not worth the description they could.
	label := truncateToWidth(r.label, inner)
	room := inner - tui.DisplayWidth(label) - 3
	if r.trailing == "" || room < minTrailingRoom {
		return marker + indent + badge + name.Render(label)
	}
	return marker + indent + badge + name.Render(label) +
		styles.Muted.Render(" — "+truncateToWidth(r.trailing, room))
}

// button is a row that leads to a screen of its own — home's "View all" and "See
// all projects", a project's "View all activity". Quieter than a name and with an
// arrow after it, so a row that goes somewhere does not read as one more of the
// things listed above it.
type button struct {
	label    string
	selected bool
}

func (b button) render(styles *tui.Styles, width int) string {
	style := styles.Muted
	marker := "  "
	if b.selected {
		style = lipgloss.NewStyle().Foreground(styles.Theme().Primary).Bold(true)
		marker = style.Render("› ")
	}
	return marker + style.Render(truncateToWidth(b.label+" →", max(width-2, 1)))
}
