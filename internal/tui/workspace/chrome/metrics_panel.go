package chrome

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
)

// MetricsPanelHeight is the vertical space consumed by the metrics panel.
const MetricsPanelHeight = 8

// MetricsPanel renders a table of per-pool stats for observability.
type MetricsPanel struct {
	styles  *tui.Styles
	width   int
	statsFn func() []data.PoolStatus
	apdexFn func() float64
}

// NewMetricsPanel creates a metrics panel that reads live stats.
func NewMetricsPanel(styles *tui.Styles, statsFn func() []data.PoolStatus, apdexFn func() float64) MetricsPanel {
	return MetricsPanel{
		styles:  styles,
		statsFn: statsFn,
		apdexFn: apdexFn,
	}
}

// SetWidth sets the available width.
func (m *MetricsPanel) SetWidth(w int) {
	m.width = w
}

// View renders the metrics panel.
func (m MetricsPanel) View() string {
	if m.width <= 0 {
		return ""
	}

	theme := m.styles.Theme()

	headerStyle := lipgloss.NewStyle().
		Foreground(theme.Primary).
		Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	cellStyle := lipgloss.NewStyle().Foreground(theme.Secondary)
	errorStyle := lipgloss.NewStyle().Foreground(theme.Error)
	successStyle := lipgloss.NewStyle().Foreground(theme.Success)

	var lines []string

	// Header with Apdex
	apdex := m.apdexFn()
	apdexColor := successStyle
	if apdex < 0.7 {
		apdexColor = errorStyle
	} else if apdex < 0.9 {
		apdexColor = mutedStyle
	}
	header := headerStyle.Render("Pool Status") + "  " +
		mutedStyle.Render("Apdex: ") + apdexColor.Render(fmt.Sprintf("%.2f", apdex))
	lines = append(lines, header)

	// Table header
	tableHeader := mutedStyle.Render(fmt.Sprintf("  %-28s %-8s %-8s %-8s %-10s %-8s", "Pool", "State", "Age", "Poll", "Hit/Miss", "avg"))
	lines = append(lines, tableHeader)

	// Divider
	divWidth := m.width - 4
	if divWidth < 20 {
		divWidth = 20
	}
	lines = append(lines, mutedStyle.Render("  "+strings.Repeat("─", divWidth)))

	// Pool rows
	stats := m.statsFn()
	maxRows := MetricsPanelHeight - 4 // header + table header + divider + bottom border
	for i, ps := range stats {
		if i >= maxRows {
			lines = append(lines, mutedStyle.Render(fmt.Sprintf("  ... and %d more", len(stats)-maxRows)))
			break
		}

		// Truncate key
		key := ps.Key
		if len(key) > 28 {
			key = key[:25] + "..."
		}

		// State with color
		stateStr := stateString(ps.State)
		var stateRendered string
		switch ps.State {
		case data.StateError:
			stateRendered = errorStyle.Render(stateStr)
		case data.StateFresh:
			stateRendered = successStyle.Render(stateStr)
		default:
			stateRendered = cellStyle.Render(stateStr)
		}

		// Age
		age := time.Since(ps.FetchedAt)
		ageStr := formatDuration(age)

		// Poll interval
		pollStr := formatDuration(ps.PollInterval)
		if ps.PollInterval == 0 {
			pollStr = "-"
		}

		// Hit/Miss
		hmStr := fmt.Sprintf("%d/%d", ps.HitCount, ps.MissCount)

		// Avg latency
		avgStr := fmt.Sprintf("%dms", ps.AvgLatency.Milliseconds())

		row := fmt.Sprintf("  %-28s ", key) +
			padRight(stateRendered, 8) + " " +
			cellStyle.Render(fmt.Sprintf("%-8s", ageStr)) + " " +
			cellStyle.Render(fmt.Sprintf("%-8s", pollStr)) + " " +
			cellStyle.Render(fmt.Sprintf("%-10s", hmStr)) + " " +
			cellStyle.Render(fmt.Sprintf("%-8s", avgStr))
		lines = append(lines, row)
	}

	if len(stats) == 0 {
		lines = append(lines, mutedStyle.Render("  No active pools"))
	}

	// Bottom border
	borderStyle := lipgloss.NewStyle().Foreground(theme.Border)
	lines = append(lines, borderStyle.Render(strings.Repeat("─", m.width)))

	return strings.Join(lines, "\n")
}

func stateString(s data.SnapshotState) string {
	switch s {
	case data.StateEmpty:
		return "empty"
	case data.StateFresh:
		return "fresh"
	case data.StateStale:
		return "stale"
	case data.StateLoading:
		return "load"
	case data.StateError:
		return "error"
	default:
		return "?"
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	return fmt.Sprintf("%.0fm", d.Minutes())
}

// padRight pads a styled string to minWidth using spaces.
func padRight(s string, minWidth int) string {
	w := lipgloss.Width(s)
	if w >= minWidth {
		return s
	}
	return s + strings.Repeat(" ", minWidth-w)
}
