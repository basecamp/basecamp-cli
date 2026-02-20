// Package chrome provides always-visible shell components for the workspace.
package chrome

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// StatusBar renders the bottom status bar with key hints and status info.
type StatusBar struct {
	styles      *tui.Styles
	width       int
	accountName string
	status      string
	isError     bool
	keyHints    []key.Binding
	globalHints []key.Binding
}

// NewStatusBar creates a new status bar.
func NewStatusBar(styles *tui.Styles) StatusBar {
	return StatusBar{
		styles: styles,
	}
}

// SetAccount sets the displayed account name.
func (s *StatusBar) SetAccount(name string) {
	s.accountName = name
}

// SetStatus sets a temporary status message.
func (s *StatusBar) SetStatus(text string, isError bool) {
	s.status = text
	s.isError = isError
}

// ClearStatus clears the status message.
func (s *StatusBar) ClearStatus() {
	s.status = ""
	s.isError = false
}

// SetKeyHints sets the key bindings shown as hints.
func (s *StatusBar) SetKeyHints(hints []key.Binding) {
	s.keyHints = hints
}

// SetGlobalHints sets the always-visible global key hints shown on the right.
func (s *StatusBar) SetGlobalHints(hints []key.Binding) {
	s.globalHints = hints
}

// SetWidth sets the available width.
func (s *StatusBar) SetWidth(w int) {
	s.width = w
}

// Init implements tea.Model.
func (s StatusBar) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (s StatusBar) Update(msg tea.Msg) (StatusBar, tea.Cmd) {
	return s, nil
}

// View renders the status bar.
func (s StatusBar) View() string {
	if s.width <= 0 {
		return ""
	}

	theme := s.styles.Theme()

	barStyle := lipgloss.NewStyle().
		Width(s.width).
		Foreground(theme.Secondary).
		Background(theme.Background)

	// Build left side: key hints
	var hints []string
	for _, k := range s.keyHints {
		if k.Enabled() {
			help := k.Help()
			hint := lipgloss.NewStyle().
				Foreground(theme.Primary).
				Render(help.Key) +
				lipgloss.NewStyle().
					Foreground(theme.Muted).
					Render(" "+help.Desc)
			hints = append(hints, hint)
		}
	}
	left := strings.Join(hints, "  ")

	// Build right side: status message > global hints > account name
	var right string
	if s.status != "" {
		style := lipgloss.NewStyle().Foreground(theme.Success)
		if s.isError {
			style = lipgloss.NewStyle().Foreground(theme.Error)
		}
		right = style.Render(s.status)
	} else if len(s.globalHints) > 0 {
		right = s.renderGlobalHints(theme, lipgloss.Width(left))
	} else if s.accountName != "" {
		right = lipgloss.NewStyle().
			Foreground(theme.Muted).
			Render("[" + s.accountName + "]")
	}

	// Lay out: left-align hints, right-align status
	gap := s.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}

	return barStyle.Render(left + strings.Repeat(" ", gap) + right)
}

// renderGlobalHints renders as many global hints as fit given the left zone width.
// Hints are rendered in a dimmer color pair (Border/Muted) so they visually
// recede behind the context-specific view hints on the left.
func (s StatusBar) renderGlobalHints(theme tui.Theme, leftWidth int) string {
	const minGap = 2 // minimum space between left and right zones
	budget := s.width - leftWidth - minGap

	keyStyle := lipgloss.NewStyle().Foreground(theme.Border)
	descStyle := lipgloss.NewStyle().Foreground(theme.Muted)

	var parts []string
	used := 0
	for _, k := range s.globalHints {
		if !k.Enabled() {
			continue
		}
		help := k.Help()
		plain := help.Key + " " + help.Desc
		w := lipgloss.Width(plain)
		need := w
		if len(parts) > 0 {
			need += 2 // separator
		}
		if used+need > budget {
			break
		}
		parts = append(parts, keyStyle.Render(help.Key)+descStyle.Render(" "+help.Desc))
		used += need
	}
	return strings.Join(parts, "  ")
}
