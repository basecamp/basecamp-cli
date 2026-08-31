package workspace

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

const (
	// The keys that open the menu, and the words the top line says about them.
	menuKey      = "ctrl+j"
	menuAltKey   = "ctrl+k"
	menuHintText = "ctrl+j for menu"

	// The menu takes three fifths of the screen, down to a floor where the
	// labels still have room to sit next to their keys.
	menuWidthNumerator   = 3
	menuWidthDenominator = 5
	menuMinWidth         = 28

	// The row the menu hangs from: one below the top line, which leaves the
	// account and its caret showing above it.
	menuTopRow = 1
)

// menu is the panel behind the wordmark's chevron. It belongs to the model
// rather than to a screen: it opens over whatever the reader is looking at,
// gives it straight back, and is the same menu wherever they are.
//
// What it lists are the top-level destinations, each with the number that also
// reaches it. Those numbers work with the menu shut, which is what the menu is
// for — showing you the keys until you stop needing it.
type menu struct {
	cursor int
	open   bool
}

func (n *menu) toggle() {
	n.open = !n.open
	n.cursor = 0
}

func (n *menu) close() {
	n.open = false
	n.cursor = 0
}

// handleKey routes one key press while the menu is up. Everything it does not
// recognize is swallowed: it is a menu, and the screen behind it is not the one
// being worked on.
func (n *menu) handleKey(m *model, msg tea.KeyPressMsg) tea.Cmd {
	if chosen, ok := sectionForKey(msg.String()); ok {
		n.close()
		return m.openSection(chosen)
	}

	switch msg.Key().Code {
	case tea.KeyEscape:
		n.close()
	case tea.KeyUp:
		n.cursor = max(n.cursor-1, 0)
	case tea.KeyDown:
		n.cursor = min(n.cursor+1, len(sections)-1)
	case tea.KeyEnter:
		chosen := sections[n.cursor]
		n.close()
		return m.openSection(chosen)
	}
	return nil
}

func (n menu) helpBindings() []helpBinding {
	return []helpBinding{{"↑↓", "move"}, {"1-4", "go"}, {"enter", "open"}, {"esc", "close"}}
}

// view draws the menu, or nothing when it is closed. It is drawn to the screen's
// width rather than to its own contents so it keeps one size as it grows.
func (n menu) view(styles *tui.Styles, screenWidth int) string {
	if !n.open {
		return ""
	}
	theme := styles.Theme()
	inner := menuInnerWidth(screenWidth)

	rows := make([]string, 0, len(sections))
	for index, item := range sections {
		marker := "  "
		label := lipgloss.NewStyle().Foreground(theme.Foreground)
		if index == n.cursor {
			marker = "› "
			label = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
		}
		rows = append(rows, marker+renderMenuLabel(item.key, item.label, label))
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(0, 1).
		Width(inner + 4).
		Render(strings.Join(rows, "\n"))
}

// renderMenuLabel puts the key in front of its label with the number underlined,
// the way HEY marks the character that jumps to a section.
func renderMenuLabel(key, label string, base lipgloss.Style) string {
	return base.Underline(true).Render(key) + base.Render(" "+label)
}

// menuInnerWidth is the box's content width: three fifths of the screen, less
// the border and padding around it, and never wider than the screen itself.
func menuInnerWidth(screenWidth int) int {
	width := min(max(screenWidth*menuWidthNumerator/menuWidthDenominator, menuMinWidth), screenWidth)
	return max(width-4, 1)
}
