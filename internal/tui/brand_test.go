package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWordmarkDimensions(t *testing.T) {
	lines := strings.Split(Wordmark, "\n")
	require.Len(t, lines, WordmarkLines)

	maxWidth := 0
	for _, line := range lines {
		w := len([]rune(line))
		if w > maxWidth {
			maxWidth = w
		}
	}
	assert.Equal(t, WordmarkWidth, maxWidth)
}

func TestRenderWordmarkNoColor(t *testing.T) {
	rendered := RenderWordmark(NoColorTheme())
	// NoColor theme produces no ANSI escapes
	assert.NotContains(t, rendered, "\x1b[")
	// Content is preserved
	assert.Contains(t, rendered, "⣿")
	// "Basecamp" text appears
	assert.Contains(t, rendered, "Basecamp")
}

func TestRenderWordmarkWithColor(t *testing.T) {
	rendered := RenderWordmark(DefaultTheme(true))
	// Colored theme includes ANSI escapes
	assert.Contains(t, rendered, "\x1b[")
	// Content is still present
	assert.Contains(t, rendered, "⣿")
	// "Basecamp" text appears
	assert.Contains(t, rendered, "Basecamp")
}

func TestRenderWordmarkTextOnCorrectLine(t *testing.T) {
	rendered := RenderWordmark(NoColorTheme())
	lines := strings.Split(rendered, "\n")
	require.Greater(t, len(lines), brandTextLine)
	assert.Contains(t, lines[brandTextLine], "Basecamp")
	// Other lines should not contain "Basecamp"
	for i, line := range lines {
		if i != brandTextLine {
			assert.NotContains(t, line, "Basecamp")
		}
	}
}
