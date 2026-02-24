package chrome

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// Help renders the full-screen keyboard shortcuts overlay.
type Help struct {
	styles     *tui.Styles
	width      int
	height     int
	globalKeys [][]key.Binding
	viewTitle  string
	viewKeys   [][]key.Binding
	offset     int
}

// NewHelp creates a new help overlay component.
func NewHelp(styles *tui.Styles) Help {
	return Help{styles: styles}
}

// SetSize sets the available dimensions for the overlay.
func (h *Help) SetSize(width, height int) {
	h.width = width
	h.height = height
}

// SetGlobalKeys sets the global keybinding groups displayed in the overlay.
func (h *Help) SetGlobalKeys(keys [][]key.Binding) {
	h.globalKeys = keys
}

// SetViewTitle sets the name of the current view's section header.
func (h *Help) SetViewTitle(title string) {
	h.viewTitle = title
}

// SetViewKeys sets the view-specific keybinding groups.
func (h *Help) SetViewKeys(keys [][]key.Binding) {
	h.viewKeys = keys
}

// Update processes key events for the help overlay. It returns true when
// the overlay should be closed.
func (h *Help) Update(msg tea.KeyMsg) (shouldClose bool, cmd tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "?":
		return true, nil
	case "j", "down":
		h.offset++
		h.clampOffset()
		return false, nil
	case "k", "up":
		h.offset--
		h.clampOffset()
		return false, nil
	case "ctrl+d":
		h.offset += h.visibleHeight() / 2
		h.clampOffset()
		return false, nil
	case "ctrl+u":
		h.offset -= h.visibleHeight() / 2
		h.clampOffset()
		return false, nil
	}
	return false, nil
}

// ResetScroll resets the scroll position to the top.
func (h *Help) ResetScroll() {
	h.offset = 0
}

// visibleHeight returns the number of content lines that fit in the viewport.
// Accounts for container padding (1 top + 1 bottom) and the footer line.
func (h Help) visibleHeight() int {
	// padding top (1) + padding bottom (1) + blank line before footer + footer line = 4
	vh := h.height - 4
	if vh < 1 {
		vh = 1
	}
	return vh
}

func (h *Help) clampOffset() {
	if h.offset < 0 {
		h.offset = 0
	}
	maxOffset := h.contentLineCount() - h.visibleHeight()
	if maxOffset < 0 {
		maxOffset = 0
	}
	if h.offset > maxOffset {
		h.offset = maxOffset
	}
}

// contentLineCount returns the number of rendered content lines (excluding footer).
func (h Help) contentLineCount() int {
	count := 2 // title + blank line
	count++    // "Global" header
	for _, row := range h.globalKeys {
		count += len(row)
	}
	if h.viewTitle != "" && len(h.viewKeys) > 0 {
		count++ // blank line
		count++ // section header
		for _, row := range h.viewKeys {
			count += len(row)
		}
	}
	return count
}

// View renders the help overlay.
func (h Help) View() string {
	theme := h.styles.Theme()

	keyCol := lipgloss.NewStyle().Foreground(theme.Primary).Width(16)
	descCol := lipgloss.NewStyle().Foreground(theme.Muted)
	sectionHeader := lipgloss.NewStyle().Bold(true).Foreground(theme.Foreground)

	var lines []string

	// Title
	lines = append(lines,
		lipgloss.NewStyle().Bold(true).Foreground(theme.Primary).Render("Keyboard Shortcuts"))
	lines = append(lines, "")

	// Global keys
	lines = append(lines, sectionHeader.Render("Global"))
	for _, row := range h.globalKeys {
		for _, k := range row {
			help := k.Help()
			lines = append(lines, "  "+keyCol.Render(help.Key)+descCol.Render(help.Desc))
		}
	}

	// View-specific keys
	if h.viewTitle != "" && len(h.viewKeys) > 0 {
		lines = append(lines, "")
		lines = append(lines, sectionHeader.Render(h.viewTitle))
		for _, row := range h.viewKeys {
			for _, k := range row {
				help := k.Help()
				lines = append(lines, "  "+keyCol.Render(help.Key)+descCol.Render(help.Desc))
			}
		}
	}

	// Window the content lines
	visible := h.visibleHeight()
	totalContent := len(lines)
	overflows := totalContent > visible

	if overflows {
		end := h.offset + visible
		if end > totalContent {
			end = totalContent
		}
		start := h.offset
		if start > totalContent {
			start = totalContent
		}
		lines = lines[start:end]
	}

	// Footer
	footerText := "esc close"
	if overflows {
		footerText = "j/k scroll  esc close"
	}
	footer := lipgloss.NewStyle().Foreground(theme.Muted).Render(footerText)

	// Build final output: content lines + blank + footer, wrapped in container
	content := strings.Join(lines, "\n")

	container := lipgloss.NewStyle().
		Width(h.width).
		Height(h.height).
		Padding(1, 2)

	// Join content, blank separator, and footer
	return container.Render(lipgloss.JoinVertical(lipgloss.Left, content, "", footer))
}
