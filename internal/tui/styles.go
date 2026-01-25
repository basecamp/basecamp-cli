// Package tui provides terminal user interface components.
package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// Theme defines the color palette for the TUI.
type Theme struct {
	Primary    lipgloss.AdaptiveColor
	Secondary  lipgloss.AdaptiveColor
	Success    lipgloss.AdaptiveColor
	Warning    lipgloss.AdaptiveColor
	Error      lipgloss.AdaptiveColor
	Muted      lipgloss.AdaptiveColor
	Background lipgloss.AdaptiveColor
	Foreground lipgloss.AdaptiveColor
	Border     lipgloss.AdaptiveColor
}

// DefaultTheme returns the default bcq theme.
func DefaultTheme() Theme {
	return Theme{
		Primary:    lipgloss.AdaptiveColor{Light: "#1a73e8", Dark: "#8ab4f8"},
		Secondary:  lipgloss.AdaptiveColor{Light: "#5f6368", Dark: "#9aa0a6"},
		Success:    lipgloss.AdaptiveColor{Light: "#1e8e3e", Dark: "#81c995"},
		Warning:    lipgloss.AdaptiveColor{Light: "#f9ab00", Dark: "#fdd663"},
		Error:      lipgloss.AdaptiveColor{Light: "#d93025", Dark: "#f28b82"},
		Muted:      lipgloss.AdaptiveColor{Light: "#80868b", Dark: "#6e7681"},
		Background: lipgloss.AdaptiveColor{Light: "#ffffff", Dark: "#1f1f1f"},
		Foreground: lipgloss.AdaptiveColor{Light: "#202124", Dark: "#e8eaed"},
		Border:     lipgloss.AdaptiveColor{Light: "#dadce0", Dark: "#3c4043"},
	}
}

// Styles holds the styled components for the TUI.
type Styles struct {
	theme Theme

	// Text styles
	Title    lipgloss.Style
	Subtitle lipgloss.Style
	Heading  lipgloss.Style
	Body     lipgloss.Style
	Muted    lipgloss.Style
	Bold     lipgloss.Style
	Success  lipgloss.Style
	Warning  lipgloss.Style
	Error    lipgloss.Style

	// Container styles
	Box   lipgloss.Style
	Card  lipgloss.Style
	Panel lipgloss.Style

	// Interactive styles
	Focused  lipgloss.Style
	Selected lipgloss.Style
	Cursor   lipgloss.Style

	// Status styles
	StatusOK    lipgloss.Style
	StatusError lipgloss.Style
	StatusInfo  lipgloss.Style
}

// NewStyles creates a new Styles with the default theme.
func NewStyles() *Styles {
	return NewStylesWithTheme(DefaultTheme())
}

// NewStylesWithTheme creates a new Styles with a custom theme.
func NewStylesWithTheme(theme Theme) *Styles {
	s := &Styles{theme: theme}

	// Text styles
	s.Title = lipgloss.NewStyle().
		Bold(true).
		Foreground(theme.Primary).
		MarginBottom(1)

	s.Subtitle = lipgloss.NewStyle().
		Foreground(theme.Secondary)

	s.Heading = lipgloss.NewStyle().
		Bold(true).
		Foreground(theme.Foreground).
		MarginTop(1).
		MarginBottom(1)

	s.Body = lipgloss.NewStyle().
		Foreground(theme.Foreground)

	s.Muted = lipgloss.NewStyle().
		Foreground(theme.Muted)

	s.Bold = lipgloss.NewStyle().
		Bold(true).
		Foreground(theme.Foreground)

	s.Success = lipgloss.NewStyle().
		Foreground(theme.Success)

	s.Warning = lipgloss.NewStyle().
		Foreground(theme.Warning)

	s.Error = lipgloss.NewStyle().
		Foreground(theme.Error)

	// Container styles
	s.Box = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(1, 2)

	s.Card = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(1, 2).
		MarginBottom(1)

	s.Panel = lipgloss.NewStyle().
		Border(lipgloss.HiddenBorder()).
		Padding(0, 1)

	// Interactive styles
	s.Focused = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Primary).
		Padding(1, 2)

	s.Selected = lipgloss.NewStyle().
		Background(theme.Primary).
		Foreground(lipgloss.Color("#ffffff")).
		Padding(0, 1)

	s.Cursor = lipgloss.NewStyle().
		Foreground(theme.Primary).
		Bold(true)

	// Status styles
	s.StatusOK = lipgloss.NewStyle().
		Foreground(theme.Success).
		Bold(true)

	s.StatusError = lipgloss.NewStyle().
		Foreground(theme.Error).
		Bold(true)

	s.StatusInfo = lipgloss.NewStyle().
		Foreground(theme.Primary)

	return s
}

// Theme returns the current theme.
func (s *Styles) Theme() Theme {
	return s.theme
}

// RenderTitle renders a title with optional subtitle.
func (s *Styles) RenderTitle(title string, subtitle ...string) string {
	result := s.Title.Render(title)
	if len(subtitle) > 0 && subtitle[0] != "" {
		result += "\n" + s.Subtitle.Render(subtitle[0])
	}
	return result
}

// RenderKeyValue renders a key-value pair.
func (s *Styles) RenderKeyValue(key, value string) string {
	return s.Muted.Render(key+": ") + s.Body.Render(value)
}

// RenderStatus renders a status message with appropriate styling.
func (s *Styles) RenderStatus(ok bool, message string) string {
	if ok {
		return s.StatusOK.Render("✓ " + message)
	}
	return s.StatusError.Render("✗ " + message)
}

// RenderCheckbox renders a checkbox item.
func (s *Styles) RenderCheckbox(checked bool, label string) string {
	checkbox := "[ ] "
	if checked {
		checkbox = "[✓] "
	}
	return s.Body.Render(checkbox + label)
}
