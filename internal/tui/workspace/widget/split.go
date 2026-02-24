package widget

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// SplitPane renders two panels side by side with a divider.
type SplitPane struct {
	styles *tui.Styles
	width  int
	height int
	ratio  float64 // left panel ratio (0.0–1.0)

	leftContent  string
	rightContent string
	collapsed    bool // true when width is too small for split
}

// NewSplitPane creates a new split pane with the given left:right ratio.
func NewSplitPane(styles *tui.Styles, ratio float64) *SplitPane {
	if ratio <= 0 || ratio >= 1 {
		ratio = 0.35
	}
	return &SplitPane{
		styles: styles,
		ratio:  ratio,
	}
}

// SetSize updates dimensions. Automatically collapses below 80 columns.
func (s *SplitPane) SetSize(w, h int) {
	s.width = w
	s.height = h
	s.collapsed = w < 80
}

// SetContent sets the content for both panels.
func (s *SplitPane) SetContent(left, right string) {
	s.leftContent = left
	s.rightContent = right
}

// LeftWidth returns the left panel width (excluding divider).
func (s *SplitPane) LeftWidth() int {
	if s.collapsed {
		return s.width
	}
	return max(0, int(float64(s.width-1)*s.ratio))
}

// RightWidth returns the right panel width (excluding divider).
func (s *SplitPane) RightWidth() int {
	if s.collapsed {
		return s.width
	}
	return max(0, s.width-s.LeftWidth()-1)
}

// IsCollapsed returns true if the pane is showing single-panel mode.
func (s *SplitPane) IsCollapsed() bool {
	return s.collapsed
}

// View renders the split pane.
func (s *SplitPane) View() string {
	if s.width <= 0 || s.height <= 0 {
		return ""
	}

	if s.collapsed {
		// Single-panel mode: show only left content
		return lipgloss.NewStyle().
			Width(s.width).
			Height(s.height).
			Render(s.leftContent)
	}

	theme := s.styles.Theme()
	leftW := s.LeftWidth()
	rightW := s.RightWidth()

	left := lipgloss.NewStyle().
		Width(leftW).
		Height(s.height).
		Render(s.leftContent)

	divider := lipgloss.NewStyle().
		Foreground(theme.Border).
		Height(s.height).
		Render(strings.TrimRight(strings.Repeat("│\n", s.height), "\n"))

	right := lipgloss.NewStyle().
		Width(rightW).
		Height(s.height).
		Render(s.rightContent)

	return lipgloss.JoinHorizontal(lipgloss.Top, left, divider, right)
}
