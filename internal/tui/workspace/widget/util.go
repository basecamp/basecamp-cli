package widget

import "github.com/charmbracelet/lipgloss"

// Truncate truncates s to maxWidth, appending "…" if truncated.
// Uses lipgloss.Width for accurate ANSI-aware width measurement.
func Truncate(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	w := lipgloss.Width(s)
	if w <= maxWidth {
		return s
	}
	if maxWidth <= 1 {
		return string([]rune(s)[:maxWidth])
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes)) > maxWidth-1 {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}
