package chrome

import (
	"github.com/charmbracelet/bubbles/key"
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

// View renders the help overlay.
func (h Help) View() string {
	theme := h.styles.Theme()

	container := lipgloss.NewStyle().
		Width(h.width).
		Height(h.height).
		Padding(1, 2)

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

	// Footer
	lines = append(lines, "")
	lines = append(lines, lipgloss.NewStyle().Foreground(theme.Muted).Render("Press any key to close"))

	return container.Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}
