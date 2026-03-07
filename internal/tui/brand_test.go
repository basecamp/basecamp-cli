package tui

import (
	"bytes"
	"fmt"
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

func TestAnimateWordmarkAsyncNonTTY(t *testing.T) {
	var buf bytes.Buffer
	w, wait := AnimateWordmarkAsync(&buf, NoColorTheme())

	// Non-TTY: returns original writer and no-op wait
	assert.Equal(t, &buf, w)
	wait() // must not block

	// Static render was written
	output := buf.String()
	assert.Contains(t, output, "⣿")
	assert.Contains(t, output, "Basecamp")
	assert.NotContains(t, output, "\x1b[") // no ANSI escapes
}

func TestVisualLinesNoWrap(t *testing.T) {
	rows, pw := visualLines("hello\n", 80, 0)
	assert.Equal(t, 1, rows)
	assert.Equal(t, 0, pw)
}

func TestVisualLinesMultipleNewlines(t *testing.T) {
	rows, pw := visualLines("hello\nworld\n", 80, 0)
	assert.Equal(t, 2, rows)
	assert.Equal(t, 0, pw)
}

func TestVisualLinesSoftWrap(t *testing.T) {
	// 25 chars in a 20-col terminal wraps to 2 visual rows
	rows, pw := visualLines(strings.Repeat("x", 25)+"\n", 20, 0)
	assert.Equal(t, 2, rows)
	assert.Equal(t, 0, pw)
}

func TestVisualLinesExactFit(t *testing.T) {
	// Exactly cols chars: 1 visual row (deferred wrap, \n handles line break)
	rows, pw := visualLines(strings.Repeat("x", 20)+"\n", 20, 0)
	assert.Equal(t, 1, rows)
	assert.Equal(t, 0, pw)
}

func TestVisualLinesPartialLine(t *testing.T) {
	rows, pw := visualLines("hello", 80, 0)
	assert.Equal(t, 0, rows)
	assert.Equal(t, 5, pw)
}

func TestVisualLinesPartialLineThenNewline(t *testing.T) {
	// First write: partial line
	rows, pw := visualLines("hello", 80, 0)
	assert.Equal(t, 0, rows)
	assert.Equal(t, 5, pw)

	// Second write: newline completes the line
	rows, pw = visualLines(" world\n", 80, pw)
	assert.Equal(t, 1, rows)
	assert.Equal(t, 0, pw)
}

func TestVisualLinesContinuationWraps(t *testing.T) {
	// pendingW=15, then 10 more chars + newline = 25 in 20-col terminal
	rows, pw := visualLines(strings.Repeat("x", 10)+"\n", 20, 15)
	assert.Equal(t, 2, rows)
	assert.Equal(t, 0, pw)
}

func TestVisualLinesEmptyLine(t *testing.T) {
	rows, pw := visualLines("\n", 80, 0)
	assert.Equal(t, 1, rows)
	assert.Equal(t, 0, pw)
}

func TestVisualLinesZeroCols(t *testing.T) {
	// Fallback to \n counting when cols is unknown
	rows, pw := visualLines("hello\nworld\n", 0, 0)
	assert.Equal(t, 2, rows)
	assert.Equal(t, 0, pw)
}

func TestVisualLinesANSIEscapes(t *testing.T) {
	// ANSI escapes don't count toward visual width
	styled := "\x1b[1mhello\x1b[0m\n"
	rows, pw := visualLines(styled, 10, 0)
	assert.Equal(t, 1, rows) // "hello" is 5 chars, fits in 10 cols
	assert.Equal(t, 0, pw)
}

func TestVisualLinesANSIWraps(t *testing.T) {
	// 25 visible chars wrapped in ANSI escapes, 20-col terminal
	styled := "\x1b[38;2;232;162;23m" + strings.Repeat("x", 25) + "\x1b[0m\n"
	rows, pw := visualLines(styled, 20, 0)
	assert.Equal(t, 2, rows)
	assert.Equal(t, 0, pw)
}

func TestVisualLinesExactFitPartialLine(t *testing.T) {
	// Exactly cols chars without \n: deferred wrap — no row consumed yet,
	// pendingW stays at cols to signal deferred state.
	rows, pw := visualLines(strings.Repeat("x", 20), 20, 0)
	assert.Equal(t, 0, rows)
	assert.Equal(t, 20, pw)

	// Next single char triggers the deferred wrap
	rows, pw = visualLines("y", 20, pw)
	assert.Equal(t, 1, rows)
	assert.Equal(t, 1, pw)
}

func TestVisualLinesExactFitPartialThenNewline(t *testing.T) {
	// Deferred wrap at cols, then \n: one row consumed
	rows, pw := visualLines(strings.Repeat("x", 20), 20, 0)
	assert.Equal(t, 0, rows)
	assert.Equal(t, 20, pw)

	rows, pw = visualLines("\n", 20, pw)
	assert.Equal(t, 1, rows)
	assert.Equal(t, 0, pw)
}

func TestVisualLinesDoubleExactFitPartial(t *testing.T) {
	// 40 chars in 20-col terminal without \n: 1 wrap + deferred at end
	rows, pw := visualLines(strings.Repeat("x", 40), 20, 0)
	assert.Equal(t, 1, rows)
	assert.Equal(t, 20, pw)
}

func TestAnimWriterCountsWraps(t *testing.T) {
	var buf bytes.Buffer
	aw := &AnimWriter{
		w:    &buf,
		done: make(chan struct{}),
		cols: 20,
	}

	// Write a line that wraps in 20 cols
	fmt.Fprint(aw, strings.Repeat("x", 25)+"\n")
	assert.Equal(t, 2, aw.linesBelow)

	// Write a short line
	fmt.Fprint(aw, "hi\n")
	assert.Equal(t, 3, aw.linesBelow)
}

func TestInvalidStrategyFallsBackToDefault(t *testing.T) {
	// Verify the animations map lookup + fallback directly.
	// AnimateWordmarkWith on non-TTY hits static render before the lookup,
	// so we test the map and trace function in isolation.
	_, ok := animations["nonexistent"]
	assert.False(t, ok)

	// Fallback to DefaultAnimation produces cells
	traceFn := animations[DefaultAnimation]
	require.NotNil(t, traceFn)

	grid, numLines := wordmarkGrid()
	cells := traceFn(grid, numLines)
	assert.Greater(t, len(cells), 0)
}

func TestAllRegisteredStrategiesProduceCells(t *testing.T) {
	grid, numLines := wordmarkGrid()
	for name, traceFn := range animations {
		cells := traceFn(grid, numLines)
		assert.Greater(t, len(cells), 0, "strategy %q produced no cells", name)
	}
}

func TestAnimationNamesContainsDefault(t *testing.T) {
	names := AnimationNames()
	assert.Contains(t, names, DefaultAnimation)
	assert.Len(t, names, len(animations))
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
