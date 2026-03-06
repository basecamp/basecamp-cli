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

func TestAnimationNames(t *testing.T) {
	names := AnimationNames()
	require.NotEmpty(t, names)
	assert.Contains(t, names, "warnsdorff")
	assert.Contains(t, names, "outline-in")
	assert.Contains(t, names, "radial")
	assert.Contains(t, names, "scanline")
}

func TestAllAnimationsCoverAllCells(t *testing.T) {
	lines := strings.Split(Wordmark, "\n")
	grid := make([][]rune, len(lines))
	totalNonBlank := 0
	for i, line := range lines {
		grid[i] = []rune(line)
		for _, ch := range grid[i] {
			if ch != blankBraille {
				totalNonBlank++
			}
		}
	}

	for _, name := range AnimationNames() {
		t.Run(name, func(t *testing.T) {
			traceFn := animations[name]
			order := traceFn(grid, len(lines))
			assert.Equal(t, totalNonBlank, len(order), "trace must visit every non-blank cell")

			// Verify no duplicates
			type pos struct{ r, c int }
			seen := make(map[pos]bool)
			for _, c := range order {
				p := pos{c.row, c.col}
				assert.False(t, seen[p], "duplicate cell at (%d,%d)", c.row, c.col)
				seen[p] = true
			}
		})
	}
}

func TestWarnsdorffStartsFromInterior(t *testing.T) {
	lines := strings.Split(Wordmark, "\n")
	grid := make([][]rune, len(lines))
	for i, line := range lines {
		grid[i] = []rune(line)
	}

	order := traceWarnsdorff(grid, len(lines))
	require.NotEmpty(t, order)
	assert.Greater(t, order[0].row, 0, "trace starts from interior, not outline top")
}
