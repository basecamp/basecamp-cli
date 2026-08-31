package workspace

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// spinnerFrames is the braille cycle, which reads as motion in a single cell.
var spinnerFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

const (
	spinnerInterval = 80 * time.Millisecond
	loadingLabel    = "Loading…"
)

// spinnerTick advances the animation. It is only armed while something is
// loading, so an idle workspace wakes up for nothing.
func spinnerTick() tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

// loadingView draws the spinner in the middle of the content area.
func loadingView(width, height int, phase int, styles *tui.Styles) string {
	frame := spinnerFrames[phase%len(spinnerFrames)]
	spinner := brandStyle(styles).Render(frame)
	label := lipgloss.NewStyle().Foreground(styles.Theme().Muted).Render(loadingLabel)
	line := centerText(spinner+" "+label, width)

	var b strings.Builder
	for range max((height-1)/2, 0) {
		b.WriteString("\n")
	}
	b.WriteString(line)
	return b.String()
}
