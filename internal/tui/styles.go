// Package tui provides terminal user interface components.
package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Theme defines the color palette for the TUI.
type Theme struct {
	Dark       bool // how this theme was resolved (for downstream LightDark calls)
	Primary    color.Color
	Secondary  color.Color
	Success    color.Color
	Warning    color.Color
	Error      color.Color
	Muted      color.Color
	Background color.Color
	Foreground color.Color
	Border     color.Color
}

// DefaultTheme returns the default basecamp theme resolved for the given background.
func DefaultTheme(dark bool) Theme {
	ld := lipgloss.LightDark(dark)
	return Theme{
		Dark:       dark,
		Primary:    ld(lipgloss.Color("#1a73e8"), lipgloss.Color("#8ab4f8")),
		Secondary:  ld(lipgloss.Color("#5f6368"), lipgloss.Color("#9aa0a6")),
		Success:    ld(lipgloss.Color("#1e8e3e"), lipgloss.Color("#81c995")),
		Warning:    ld(lipgloss.Color("#f9ab00"), lipgloss.Color("#fdd663")),
		Error:      ld(lipgloss.Color("#d93025"), lipgloss.Color("#f28b82")),
		Muted:      ld(lipgloss.Color("#80868b"), lipgloss.Color("#6e7681")),
		Background: ld(lipgloss.Color("#ffffff"), lipgloss.Color("#1f1f1f")),
		Foreground: ld(lipgloss.Color("#202124"), lipgloss.Color("#e8eaed")),
		Border:     ld(lipgloss.Color("#dadce0"), lipgloss.Color("#3c4043")),
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

// NewStyles creates a new Styles with the default dark theme.
func NewStyles() *Styles {
	return NewStylesWithTheme(DefaultTheme(true))
}

// NewStylesWithTheme creates a new Styles with a custom theme.
func NewStylesWithTheme(theme Theme) *Styles {
	s := &Styles{}
	applyTheme(s, theme)
	return s
}

// UpdateTheme re-applies a theme to the existing Styles in place.
// Because all components hold a *Styles pointer, the next View() call
// picks up the new colors with zero propagation.
func (s *Styles) UpdateTheme(theme Theme) {
	applyTheme(s, theme)
}

func applyTheme(s *Styles, theme Theme) {
	s.theme = theme

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
		Foreground(theme.Background).
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
